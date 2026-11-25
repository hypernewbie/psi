package app

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/hypernewbie/psi/internal/event"
	"github.com/hypernewbie/psi/internal/provider"
	"github.com/hypernewbie/psi/internal/session"
)

// rpcRetryLabels lists message events with their role and the auto-retry
// events, the way Pi's retry tests normalize an event stream.
func rpcRetryLabels(objs []map[string]any) []string {
	var out []string
	for _, obj := range objs {
		switch obj["type"] {
		case "message_start", "message_end":
			out = append(out, fmt.Sprintf("%s:%s", obj["type"], obj["message"].(map[string]any)["role"]))
		case "auto_retry_start", "auto_retry_end", "agent_start", "agent_settled", "extension_error":
			out = append(out, obj["type"].(string))
		}
	}
	return out
}

func rpcRetryEvent(t *testing.T, objs []map[string]any, typ string) map[string]any {
	t.Helper()
	for _, obj := range objs {
		if obj["type"] == typ {
			return obj
		}
	}
	t.Fatalf("missing %s in %v", typ, objs)
	return nil
}

func rpcWantLabels(t *testing.T, objs []map[string]any, want ...string) {
	t.Helper()
	if got := fmt.Sprint(rpcRetryLabels(objs)); got != fmt.Sprint(want) {
		t.Fatalf("event order = %s, want %s", got, fmt.Sprint(want))
	}
}

// A failed response that is retried closes with its own message_end, Pi's
// auto_retry_start, and one auto_retry_end for the whole retry.
func TestRPCPromptAutoRetryStreamsPiEvents(t *testing.T) {
	for _, tc := range []struct {
		name         string
		errors       []error
		responses    []*provider.Response
		maxRetries   int
		wantSuccess  bool
		wantFinalErr string
		wantStop     string
		wantCalls    int
	}{
		{
			name: "recovers after one retry",
			errors: []error{
				errors.New("overloaded_error"),
			},
			responses: []*provider.Response{
				nil, {Text: "recovered", StopReason: "stop"},
			},
			maxRetries: 3, wantSuccess: true, wantStop: "stop", wantCalls: 2,
		},
		{
			name: "reports the final failure after the budget",
			errors: []error{
				errors.New("overloaded_error"), errors.New("overloaded_error"),
			},
			responses: []*provider.Response{
				nil, nil,
			},
			maxRetries: 1, wantFinalErr: "overloaded_error", wantStop: "error", wantCalls: 2,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fake := &provider.Fake{Errors: tc.errors, Responses: tc.responses}
			s, store, log := newRPCTestRunner(t, fake, nil)
			// runtime.configure copies the retry settings into the base config.
			s.r.cfg.RetryAttempts, s.r.cfg.RetryBaseDelayMs, s.r.cfg.RetryMaxAgentDelayMs = tc.maxRetries, 1, 60000
			resp := s.handle(rpcCommand{ID: strptr("p1"), Type: "prompt", Message: "hi"})
			if resp == nil || !resp.Success {
				t.Fatalf("prompt response = %+v", resp)
			}
			objs := log.waitFor(t, "agent_settled", isType("agent_settled"))
			want := []string{"message_start:user", "message_end:user", "agent_start",
				"message_start:assistant", "message_end:assistant", "auto_retry_start",
				"message_start:assistant", "message_end:assistant", "auto_retry_end"}
			if !tc.wantSuccess {
				want = append(want, "extension_error")
			}
			rpcWantLabels(t, objs, append(want, "agent_settled")...)
			start := rpcRetryEvent(t, objs, "auto_retry_start")
			if start["attempt"] != float64(1) || start["maxAttempts"] != float64(tc.maxRetries) || start["delayMs"] != float64(1) || start["errorMessage"] != "overloaded_error" {
				t.Fatalf("auto_retry_start = %v", start)
			}
			end := rpcRetryEvent(t, objs, "auto_retry_end")
			if end["attempt"] != float64(1) || end["success"] != tc.wantSuccess {
				t.Fatalf("auto_retry_end = %v", end)
			}
			if tc.wantSuccess {
				if _, present := end["finalError"]; present {
					t.Fatalf("successful auto_retry_end carries finalError: %v", end)
				}
			} else if end["finalError"] != tc.wantFinalErr {
				t.Fatalf("auto_retry_end = %v, want finalError %q", end, tc.wantFinalErr)
			}
			// Every failed response is on the stream with Pi's error fields.
			var failed []map[string]any
			for _, obj := range objs {
				if obj["type"] != "message_end" {
					continue
				}
				message := obj["message"].(map[string]any)
				if message["role"] == "assistant" && message["stopReason"] == "error" {
					failed = append(failed, message)
				}
			}
			if len(failed) != len(tc.errors) {
				t.Fatalf("failed responses on the stream = %d, want %d: %v", len(failed), len(tc.errors), objs)
			}
			for _, message := range failed {
				if message["errorMessage"] != "overloaded_error" {
					t.Fatalf("failed response = %v", message)
				}
			}
			// Retried attempts are omitted: the projection keeps the user prompt
			// and the durable outcome of the run.
			proj, err := store.Projection()
			if err != nil {
				t.Fatal(err)
			}
			var roles []string
			for _, message := range proj.Flat {
				roles = append(roles, message.Role)
			}
			if fmt.Sprint(roles) != "[system user assistant]" || proj.Flat[2].Assistant.StopReason != tc.wantStop {
				t.Fatalf("projection = %s with stop reason %q, want the prompt and the %s response", fmt.Sprint(roles), proj.Flat[2].Assistant.StopReason, tc.wantStop)
			}
			if fake.CallCount() != tc.wantCalls {
				t.Fatalf("provider calls = %d, want %d", fake.CallCount(), tc.wantCalls)
			}
		})
	}
}

// An abort during the backoff is Pi's abortRetry: the retry ends with
// "Retry cancelled" and no further request is made.
func TestRPCAbortDuringAutoRetryReportsRetryCancelled(t *testing.T) {
	fake := &provider.Fake{Errors: []error{errors.New("overloaded_error")}, Responses: []*provider.Response{nil}}
	s, _, log := newRPCTestRunner(t, fake, nil)
	// The wait must outlast the abort, so the run cannot retry first.
	s.r.cfg.RetryAttempts, s.r.cfg.RetryBaseDelayMs, s.r.cfg.RetryMaxAgentDelayMs = 3, 60000, 60000
	log.notify = make(chan struct{}, 1)
	resp := s.handle(rpcCommand{ID: strptr("p1"), Type: "prompt", Message: "hi"})
	if resp == nil || !resp.Success {
		t.Fatalf("prompt response = %+v", resp)
	}
	log.waitFor(t, "auto_retry_start", isType("auto_retry_start"))
	if abort := s.handle(rpcCommand{ID: strptr("a1"), Type: "abort"}); abort == nil || !abort.Success {
		t.Fatalf("abort response = %+v", abort)
	}
	objs := log.waitFor(t, "agent_settled", isType("agent_settled"))
	end := rpcRetryEvent(t, objs, "auto_retry_end")
	if end["success"] != false || end["finalError"] != "Retry cancelled" || end["attempt"] != float64(1) {
		t.Fatalf("auto_retry_end = %v, want the cancelled retry", end)
	}
	settled := objs[len(objs)-1]
	if settled["type"] != "agent_settled" || settled["aborted"] != true {
		t.Fatalf("settled = %v", settled)
	}
	if fake.CallCount() != 1 {
		t.Fatalf("provider calls = %d, want no request after the cancelled wait", fake.CallCount())
	}
	deadline := time.Now().Add(10 * time.Second)
	for s.isRunning() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if s.isRunning() {
		t.Fatal("run still active after the cancelled retry")
	}
}

// The sink maps the agent's retry events to Pi's wire shape directly.
func TestRPCAutoRetryEventsMatchPiShape(t *testing.T) {
	s, store, log := newRPCTestRunner(t, &provider.Fake{}, nil)
	failedID, err := store.AppendMessage(session.AgentMessage{Role: "assistant", Assistant: &session.AssistantMessage{
		Role: "assistant", API: "anthropic-messages", Provider: "anthropic", Model: "rpc-test-model", StopReason: "error",
		Content:      []session.ContentBlock{{Type: "text", Text: &session.TextContent{Type: "text", Text: ""}}},
		ErrorMessage: strptr("stream was interrupted"),
	}})
	if err != nil {
		t.Fatal(err)
	}
	// The agent omits the failed attempt before it announces the retry.
	if _, err := store.AppendContextEdit(failedID, nil); err != nil {
		t.Fatal(err)
	}
	s.sink(event.Event{Type: event.AutoRetryStart, RetryAttempt: 2, RetryMaximum: 3, RetryDelayMs: 4000, Text: "stream was interrupted", CommittedID: failedID})
	s.sink(event.Event{Type: event.AutoRetryEnd, RetryAttempt: 2})
	s.sink(event.Event{Type: event.AutoRetryStart, RetryAttempt: 1, RetryMaximum: 3, RetryDelayMs: 2000, Text: "overloaded", CommittedID: "missing-entry"})
	s.sink(event.Event{Type: event.AutoRetryEnd, RetryAttempt: 1, Err: errors.New("Retry cancelled")})
	objs := log.objects(t)
	rpcWantLabels(t, objs, "message_end:assistant", "auto_retry_start", "auto_retry_end", "auto_retry_start", "auto_retry_end")
	failed := objs[0]["message"].(map[string]any)
	if failed["stopReason"] != "error" || failed["errorMessage"] != "stream was interrupted" {
		t.Fatalf("failed response = %v", failed)
	}
	if !hasEvent(objs, "auto_retry_start", map[string]any{"attempt": float64(2), "maxAttempts": float64(3), "delayMs": float64(4000), "errorMessage": "stream was interrupted"}) {
		t.Fatalf("auto_retry_start shape = %v", objs)
	}
	if !hasEvent(objs, "auto_retry_end", map[string]any{"success": true, "attempt": float64(2)}) {
		t.Fatalf("auto_retry_end shape = %v", objs)
	}
	if !hasEvent(objs, "auto_retry_end", map[string]any{"success": false, "attempt": float64(1), "finalError": "Retry cancelled"}) {
		t.Fatalf("failed auto_retry_end shape = %v", objs)
	}
}
