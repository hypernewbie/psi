package app

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"psi/internal/event"
	"psi/internal/provider"
	"psi/internal/session"
)

func TestRPCImmediateAbortAndBusyAdmission(t *testing.T) {
	for i := 0; i < 20; i++ {
		block := newRPCBlockingProvider()
		s, _, log := newRPCTestRunner(t, block, nil)
		first := s.handle(rpcCommand{Type: "prompt", Message: "hello"})
		if first == nil || !first.Success {
			t.Fatalf("prompt: %+v", first)
		}
		second := s.handle(rpcCommand{Type: "prompt", Message: "must not start"})
		if second == nil || second.Success {
			t.Fatalf("second prompt accepted: %+v", second)
		}
		s.handle(rpcCommand{Type: "abort"})
		objs := log.waitFor(t, "immediate abort", isType("agent_settled"))
		if objs[len(objs)-1]["aborted"] != true {
			t.Fatalf("not aborted: %v", objs)
		}
		close(block.release)
	}
}

func TestRPCResumedTranscriptNotReplayed(t *testing.T) {
	fake := &provider.Fake{Responses: []*provider.Response{{Text: "new answer", StopReason: "stop"}}}
	s, store, log := newRPCTestRunner(t, fake, nil)
	_, err := store.AppendMessage(session.AgentMessage{Role: "assistant", Assistant: &session.AssistantMessage{Role: "assistant", Content: []session.ContentBlock{{Type: "text", Text: &session.TextContent{Type: "text", Text: "old answer"}}}}})
	if err != nil {
		t.Fatal(err)
	}
	resp := s.handle(rpcCommand{Type: "prompt", Message: "new question"})
	if !resp.Success {
		t.Fatalf("prompt: %+v", resp)
	}
	objs := log.waitFor(t, "settled", isType("agent_settled"))
	for _, obj := range objs {
		raw, _ := json.Marshal(obj)
		if obj["type"] == "message_end" && strings.Contains(string(raw), "old answer") {
			t.Fatalf("replayed old message: %v", obj)
		}
	}
}

func TestRPCModelSwitchRebuildsLoop(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "test")
	s, _, _ := newRPCTestRunner(t, &provider.Fake{}, nil)
	old := s.loop
	resp := s.handle(rpcCommand{Type: "set_model", Provider: "anthropic", ModelID: "rpc-test-model-2"})
	if !resp.Success {
		t.Fatalf("switch: %+v", resp)
	}
	if s.loop == old {
		t.Fatal("model switch retained old agent loop and provider")
	}
}

func TestRPCAutomaticCompactionPublishesCompletion(t *testing.T) {
	s, _, log := newRPCTestRunner(t, &provider.Fake{}, nil)
	s.sink(event.Event{Type: event.CompactionStart, State: event.CompactionThreshold})
	s.sink(event.Event{Type: event.CompactionEnd, State: event.CompactionThreshold, Err: errors.New("failed")})
	if !hasEvent(log.objects(t), "compaction_end", map[string]any{"reason": "threshold", "errorMessage": "failed"}) {
		t.Fatalf("missing automatic compaction outcome: %v", log.objects(t))
	}
}

func TestRPCSummarizationRetryUsesPiEvents(t *testing.T) {
	s, _, log := newRPCTestRunner(t, &provider.Fake{}, nil)
	s.sink(event.Event{Type: event.SummarizationRetryScheduled, RetryAttempt: 1, RetryMaximum: 3, RetryDelayMs: 2000, Text: "stream was interrupted"})
	s.sink(event.Event{Type: event.SummarizationRetryAttemptStart, State: event.CompactionThreshold})
	s.sink(event.Event{Type: event.SummarizationRetryFinished})
	objs := log.objects(t)
	if !hasEvent(objs, "summarization_retry_scheduled", map[string]any{"attempt": float64(1), "maxAttempts": float64(3), "delayMs": float64(2000), "errorMessage": "stream was interrupted"}) ||
		!hasEvent(objs, "summarization_retry_attempt_start", map[string]any{"source": "compaction", "reason": "threshold"}) ||
		!hasEvent(objs, "summarization_retry_finished", nil) {
		t.Fatalf("missing Pi summarization retry events: %v", objs)
	}
	if hasEvent(objs, "auto_retry_start", nil) {
		t.Fatalf("summary retry was reported as an assistant-turn retry: %v", objs)
	}
}

func TestRPCEmptyCompactionDoesNotResetTranscript(t *testing.T) {
	s, _, log := newRPCTestRunner(t, &provider.Fake{}, nil)
	resp := s.handle(rpcCommand{Type: "compact"})
	if resp.Success {
		t.Fatal("empty compaction succeeded")
	}
	if hasEvent(log.objects(t), "compaction_end", nil) {
		t.Fatal("unstarted compaction emitted destructive reset event")
	}
}
