package agent

// Regressions for Pi's summarization retry policy (retryAssistantCall) and
// Pi's auto-compaction failure handling: a transient summary failure retries
// with backoff, and a summary that ultimately fails is reported without
// failing the run.

import (
	"context"
	"errors"
	"strings"
	"testing"

	"psi/internal/event"
	"psi/internal/provider"
)

func TestSummaryRequestRetriesTransientStreamFailure(t *testing.T) {
	store, reg, fake, _, _ := testSetup(t)
	var events []event.Event
	loop := New(store, reg, fake, func(e event.Event) { events = append(events, e) })
	fake.Errors = []error{
		&provider.StreamInterrupt{Partial: &provider.Response{Text: "partial"}, Err: providerErr("provider stream interrupted (not retried): stream terminated")},
		nil,
	}
	fake.Responses = []*provider.Response{nil, {Text: "checkpoint", StopReason: "stop"}}
	cfg := Config{Model: modelMeta(), RetryAttempts: 2, RetryBaseDelayMs: 0, RetryMaxAgentDelayMs: 60_000}

	resp, err := loop.requestSummary(t.Context(), fake, cfg, "", "conversation", "", false)
	if err != nil {
		t.Fatalf("retryable stream failure not retried: %v", err)
	}
	if resp == nil || resp.Text != "checkpoint" {
		t.Fatalf("summary response = %+v", resp)
	}
	if len(fake.Calls) != 2 {
		t.Fatalf("summary calls = %d, want 2", len(fake.Calls))
	}
	var retryEvents []event.Event
	for _, e := range events {
		if e.Type == event.SummarizationRetryScheduled || e.Type == event.SummarizationRetryAttemptStart || e.Type == event.SummarizationRetryFinished {
			retryEvents = append(retryEvents, e)
		}
	}
	if len(retryEvents) != 3 || retryEvents[0].Type != event.SummarizationRetryScheduled || retryEvents[0].RetryAttempt != 1 || retryEvents[0].RetryMaximum != 2 || retryEvents[0].Text == "" || retryEvents[1].Type != event.SummarizationRetryAttemptStart || retryEvents[1].State != event.CompactionManual || retryEvents[2].Type != event.SummarizationRetryFinished {
		t.Fatalf("retry events = %+v", retryEvents)
	}
}

func TestSummaryRequestStopsRetryingAfterBudget(t *testing.T) {
	store, reg, fake, _, _ := testSetup(t)
	var events []event.Event
	loop := New(store, reg, fake, func(e event.Event) { events = append(events, e) })
	interrupt := &provider.StreamInterrupt{Err: providerErr("OpenAI Completions stream interrupted (not retried): unexpected EOF")}
	fake.Errors = []error{interrupt, interrupt, interrupt, interrupt}
	cfg := Config{Model: modelMeta(), RetryAttempts: 2, RetryBaseDelayMs: 0, RetryMaxAgentDelayMs: 60_000}

	_, err := loop.requestSummary(t.Context(), fake, cfg, "", "conversation", "", false)
	if err == nil || !strings.Contains(err.Error(), "summary failed; original context is unchanged") {
		t.Fatalf("exhausted retries returned %v", err)
	}
	if !IsSummaryFailure(err) {
		t.Fatal("exhausted stream failures must be summary failures")
	}
	if len(fake.Calls) != 3 {
		t.Fatalf("summary calls = %d, want 3 (initial + 2 retries)", len(fake.Calls))
	}
	if retries := countRetries(events); retries != 2 {
		t.Fatalf("retry events = %d, want 2", retries)
	}
}

func TestSummaryRetryCancellationStopsBeforeNextRequest(t *testing.T) {
	store, reg, fake, _, _ := testSetup(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	var retries int
	loop := New(store, reg, fake, func(e event.Event) {
		if e.Type == event.SummarizationRetryScheduled {
			retries++
			cancel()
		}
	})
	fake.Errors = []error{&provider.StreamInterrupt{Err: providerErr("stream interrupted: terminated")}}
	cfg := Config{Model: modelMeta(), RetryAttempts: 3, RetryBaseDelayMs: 2000, RetryMaxAgentDelayMs: 60_000}

	_, err := loop.requestSummary(ctx, fake, cfg, "", "conversation", "", false)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled backoff error = %v", err)
	}
	if fake.CallCount() != 1 || retries != 1 {
		t.Fatalf("cancellation did not stop retry: provider calls=%d retry events=%d", fake.CallCount(), retries)
	}
}

func TestSummaryRequestDoesNotRetryQuotaErrors(t *testing.T) {
	store, reg, fake, _, _ := testSetup(t)
	var events []event.Event
	loop := New(store, reg, fake, func(e event.Event) { events = append(events, e) })
	fake.Errors = []error{&provider.ProviderError{Kind: provider.ErrRateLimit, Status: 429, Message: "insufficient_quota: add credits"}}
	cfg := Config{Model: modelMeta(), RetryAttempts: 3, RetryBaseDelayMs: 0, RetryMaxAgentDelayMs: 60_000}

	_, err := loop.requestSummary(t.Context(), fake, cfg, "", "conversation", "", false)
	if err == nil || !strings.Contains(err.Error(), "insufficient_quota") {
		t.Fatalf("quota error = %v", err)
	}
	if len(fake.Calls) != 1 {
		t.Fatalf("quota error retried: calls = %d", len(fake.Calls))
	}
	if retries := countRetries(events); retries != 0 {
		t.Fatalf("quota error emitted retry events: %d", retries)
	}
}

func TestAutomaticCompactionFailureKeepsRunAlive(t *testing.T) {
	store, reg, fake, _, _ := testSetup(t)
	if _, err := store.AppendMessage(userMsg(strings.Repeat("older history ", 1000))); err != nil {
		t.Fatal(err)
	}
	// Non-retryable summary failure, then a normal answer after the run continues.
	fake.Errors = []error{&provider.ProviderError{Kind: provider.ErrAuth, Status: 401, Message: "compactor key expired"}}
	fake.Responses = []*provider.Response{nil, {Text: "answer", StopReason: "stop"}}
	var phases []event.Event
	loop := New(store, reg, fake, func(e event.Event) {
		if e.Type == event.CompactionStart || e.Type == event.CompactionEnd {
			phases = append(phases, e)
		}
	})
	cfg := Config{Model: provider.ModelMeta{ID: "m", API: "anthropic-messages", ContextWindow: 10000, MaxTokens: 1000},
		AutoCompact: true, ReserveTokens: 8000, KeepRecentTokens: 1, RetryAttempts: 1, RetryBaseDelayMs: 0, RetryMaxAgentDelayMs: 60_000}

	if err := loop.Submit(t.Context(), "current task", cfg); err != nil {
		t.Fatalf("automatic compaction failure ended the run: %v", err)
	}
	if len(fake.Calls) != 2 {
		t.Fatalf("provider calls = %d, want summary attempt plus model request", len(fake.Calls))
	}
	if len(phases) != 2 || phases[1].Type != event.CompactionEnd || phases[1].Err == nil {
		t.Fatalf("compaction phases = %+v", phases)
	}
	for _, entry := range store.Entries() {
		if entry.Kind == "compaction" {
			t.Fatal("failed automatic compaction wrote a checkpoint")
		}
	}
}

func TestAutomaticCompactionStorageFailureStillStopsRun(t *testing.T) {
	store, reg, fake, _, _ := testSetup(t)
	if _, err := store.AppendMessage(userMsg(strings.Repeat("older history ", 1000))); err != nil {
		t.Fatal(err)
	}
	fake.Responses = []*provider.Response{{Text: "summary", StopReason: "stop"}}
	loop := New(store, reg, fake, func(e event.Event) {
		if e.Type == event.CompactionStart {
			store.FailWritesForTest(errors.New("disk refusal"))
		}
	})
	cfg := Config{Model: provider.ModelMeta{ID: "m", API: "anthropic-messages", ContextWindow: 10000, MaxTokens: 1000},
		AutoCompact: true, ReserveTokens: 8000, KeepRecentTokens: 1}
	err := loop.Submit(t.Context(), "current task", cfg)
	if err == nil || !strings.Contains(err.Error(), "automatic compaction") {
		t.Fatalf("storage failure during automatic compaction = %v", err)
	}
}

func providerErr(message string) error {
	return &provider.ProviderError{Kind: provider.ErrTransport, Status: 503, Message: message}
}

func countRetries(events []event.Event) int {
	count := 0
	for _, e := range events {
		if e.Type == event.SummarizationRetryScheduled {
			count++
		}
	}
	return count
}
