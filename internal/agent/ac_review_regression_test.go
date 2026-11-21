package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/hypernewbie/psi/internal/ac"
	"github.com/hypernewbie/psi/internal/event"
	"github.com/hypernewbie/psi/internal/provider"
	"github.com/hypernewbie/psi/internal/session"
)

func TestFinalThresholdUsesNewUsageAfterEarlierCompaction(t *testing.T) {
	store, reg, _, _, _ := testSetup(t)
	if _, err := store.AppendMessage(userMsg("older task")); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AppendMessage(session.AgentMessage{Role: "assistant", Assistant: &session.AssistantMessage{
		Role: "assistant", StopReason: "stop", Usage: session.Usage{TotalTokens: 95_000},
		Content: []session.ContentBlock{{Type: "text", Text: &session.TextContent{Type: "text", Text: strings.Repeat("old context ", 200)}}},
	}}); err != nil {
		t.Fatal(err)
	}
	var phases []string
	fake := reviewProvider(func(_ context.Context, req provider.Request, _ func(provider.Delta)) (*provider.Response, error) {
		if strings.Contains(req.System, "context summarization assistant") {
			phases = append(phases, "summary")
			return &provider.Response{Text: "checkpoint", StopReason: "stop"}, nil
		}
		phases = append(phases, "answer")
		return &provider.Response{Text: strings.Repeat("new final answer ", 200), StopReason: "stop",
			Usage: provider.Usage{Input: 95_000, Output: 20, TotalTokens: 95_020}}, nil
	})
	starts := 0
	loop := New(store, reg, fake, func(e event.Event) {
		if e.Type == event.CompactionStart && e.State == event.CompactionThreshold {
			starts++
		}
	})
	cfg := Config{Model: provider.ModelMeta{ID: "small", API: "anthropic-messages", ContextWindow: 100_000, MaxTokens: 2_000},
		AutoCompact: true, ReserveTokens: 16_384, KeepRecentTokens: 20}
	if err := loop.Submit(t.Context(), "current task", cfg); err != nil {
		t.Fatal(err)
	}
	checkpoints := 0
	for _, entry := range store.Entries() {
		if entry.Compaction != nil {
			checkpoints++
			if checkpoints == 2 && entry.Compaction.TokensBefore != 95_020 {
				t.Fatalf("final checkpoint tokensBefore = %v", entry.Compaction.TokensBefore)
			}
		}
	}
	if starts != 2 || checkpoints != 2 || len(phases) < 3 || phases[0] != "summary" || phases[len(phases)-1] != "summary" {
		t.Fatalf("threshold starts=%d checkpoints=%d requests=%v; want summary, answer, summary", starts, checkpoints, phases)
	}
}

func TestFinalThresholdDetectsNewUnmeasuredGrowthAfterCompaction(t *testing.T) {
	store, reg, _, _, _ := testSetup(t)
	if _, err := store.AppendMessage(userMsg("older task")); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AppendMessage(session.AgentMessage{Role: "assistant", Assistant: &session.AssistantMessage{
		Role: "assistant", StopReason: "stop", Usage: session.Usage{TotalTokens: 950},
		Content: []session.ContentBlock{{Type: "text", Text: &session.TextContent{Type: "text", Text: strings.Repeat("old ", 200)}}},
	}}); err != nil {
		t.Fatal(err)
	}
	var requests []string
	fake := reviewProvider(func(_ context.Context, req provider.Request, _ func(provider.Delta)) (*provider.Response, error) {
		if strings.Contains(req.System, "context summarization assistant") {
			requests = append(requests, "summary")
			return &provider.Response{Text: "checkpoint", StopReason: "stop"}, nil
		}
		requests = append(requests, "answer")
		// A provider can omit usage entirely. The final text itself exceeds the
		// window after the previous compaction reduced the estimated context.
		return &provider.Response{Text: strings.Repeat("answer ", 800), StopReason: "stop"}, nil
	})
	loop := New(store, reg, fake, nil)
	cfg := Config{Model: provider.ModelMeta{ID: "small", API: "anthropic-messages", ContextWindow: 1_000, MaxTokens: 200},
		AutoCompact: true, ReserveTokens: 100, KeepRecentTokens: 10}
	if err := loop.Submit(t.Context(), "current task", cfg); err != nil {
		t.Fatal(err)
	}
	checkpoints := 0
	for _, entry := range store.Entries() {
		if entry.Compaction != nil {
			checkpoints++
		}
	}
	if checkpoints != 2 || len(requests) < 3 || requests[0] != "summary" || requests[len(requests)-1] != "summary" {
		t.Fatalf("checkpoints=%d requests=%v; want compaction before and after the answer", checkpoints, requests)
	}
}

func TestACCompactionFailureAndCancellation(t *testing.T) {
	for _, cancelSummary := range []bool{false, true} {
		name := "summary failure pauses"
		if cancelSummary {
			name = "cancelled summary does not pause"
		}
		t.Run(name, func(t *testing.T) {
			store, reg, _, _, _ := testSetup(t)
			if _, err := store.AppendMessage(userMsg("old task")); err != nil {
				t.Fatal(err)
			}
			appendAssistantText(t, store, strings.Repeat("old answer ", 300))
			if _, err := store.AppendMessage(userMsg("unfinished task")); err != nil {
				t.Fatal(err)
			}
			before, err := store.Projection()
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			calls := 0
			fake := reviewProvider(func(ctx context.Context, _ provider.Request, _ func(provider.Delta)) (*provider.Response, error) {
				calls++
				if cancelSummary {
					cancel()
					return nil, ctx.Err()
				}
				return nil, &provider.ProviderError{Kind: provider.ErrAuth, Status: 401, Message: "synthetic summary failure"}
			})
			policy := ac.NewController()
			loop := New(store, reg, fake, nil)
			cfg := Config{Model: modelMeta(), KeepRecentTokens: 10, ACPolicy: policy}
			err = loop.CompactAndResume(ctx, cfg, "")
			if err == nil || (cancelSummary && !errors.Is(err, context.Canceled)) || (!cancelSummary && !IsSummaryFailure(err)) {
				t.Fatalf("compaction error = %v", err)
			}
			state := policy.Snapshot()
			if state.StoppedAfterError == cancelSummary || state.Compacting || state.CompactionCount != 0 || calls != 1 {
				t.Fatalf("state=%+v calls=%d", state, calls)
			}
			after, err := store.Projection()
			if err != nil {
				t.Fatal(err)
			}
			beforeJSON, _ := json.Marshal(before.Flat)
			afterJSON, _ := json.Marshal(after.Flat)
			if string(beforeJSON) != string(afterJSON) {
				t.Fatal("failed or cancelled summary changed the conversation")
			}
			for _, entry := range store.Entries() {
				if entry.Compaction != nil {
					t.Fatal("failed or cancelled summary wrote a checkpoint")
				}
			}
			if !cancelSummary {
				policy.SetEnabled(true)
				if policy.Snapshot().StoppedAfterError {
					t.Fatal("enabling AC did not clear the failure")
				}
			}
		})
	}
}

func TestACCheckpointDoesNotResumeAfterCancellationOrAuthChange(t *testing.T) {
	for _, cancelAtCheckpoint := range []bool{true, false} {
		name := "active model becomes unavailable"
		if cancelAtCheckpoint {
			name = "cancelled after checkpoint"
		}
		t.Run(name, func(t *testing.T) {
			store, reg, fake, _, _ := testSetup(t)
			if _, err := store.AppendMessage(userMsg("old task")); err != nil {
				t.Fatal(err)
			}
			appendAssistantText(t, store, strings.Repeat("old answer ", 300))
			if _, err := store.AppendMessage(userMsg("unfinished task")); err != nil {
				t.Fatal(err)
			}
			fake.Responses = []*provider.Response{{Text: "checkpoint", StopReason: "stop"}}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var authErr error
			loop := New(store, reg, fake, func(e event.Event) {
				if e.Type == event.CompactionEnd && e.Err == nil {
					if cancelAtCheckpoint {
						cancel()
					} else {
						authErr = errors.New("active model is unavailable")
					}
				}
			})
			policy := ac.NewController()
			cfg := Config{Model: modelMeta(), KeepRecentTokens: 10, ACPolicy: policy,
				LiveSettings: func() LiveSettings { return LiveSettings{AuthErr: authErr} }}
			err := loop.CompactAndResume(ctx, cfg, "")
			if err == nil || (cancelAtCheckpoint && !errors.Is(err, context.Canceled)) || (!cancelAtCheckpoint && !strings.Contains(err.Error(), "continuation is unavailable")) {
				t.Fatalf("continuation error = %v", err)
			}
			checkpoints, resumes := 0, 0
			for _, entry := range store.Entries() {
				if entry.Compaction != nil {
					checkpoints++
				}
				if entry.Message != nil && entry.Message.Message.User != nil && strings.Contains(fmt.Sprint(entry.Message.Message.User.Content), "Continue the current task from the compacted context") {
					resumes++
				}
			}
			if checkpoints != 1 || resumes != 0 || len(fake.Calls) != 1 || policy.Snapshot().StoppedAfterError {
				t.Fatalf("checkpoints=%d resumes=%d calls=%d state=%+v", checkpoints, resumes, len(fake.Calls), policy.Snapshot())
			}
		})
	}
}

func TestNativeThresholdStillWorksWhileACPaused(t *testing.T) {
	store, loop, cfg, events, _, _ := usageDrivenSetup(t, 95_000)
	cfg.ACPolicy.RecordCompaction(false, "automatic", 60_000)
	if err := loop.Submit(t.Context(), "continue", cfg); err != nil {
		t.Fatal(err)
	}
	checkpoints, threshold := 0, false
	for _, entry := range store.Entries() {
		if entry.Compaction != nil {
			checkpoints++
		}
	}
	for _, e := range *events {
		if e.Type == event.CompactionStart && e.State == event.CompactionThreshold {
			threshold = true
		}
	}
	if checkpoints != 1 || !threshold || !cfg.ACPolicy.Snapshot().StoppedAfterError {
		t.Fatalf("checkpoints=%d threshold=%v state=%+v", checkpoints, threshold, cfg.ACPolicy.Snapshot())
	}
}
