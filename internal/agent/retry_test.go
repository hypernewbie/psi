package agent

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hypernewbie/psi/internal/event"
	"github.com/hypernewbie/psi/internal/provider"
	"github.com/hypernewbie/psi/internal/session"
	"github.com/hypernewbie/psi/internal/tools"
)

// Port of Pi's assistant-turn retry tests: test/agent-session-retry.test.ts,
// test/suite/agent-session-retry-events.test.ts, and the regressions for
// issues 3317, 6019, and 6904. Pi's settings in those tests are
// retry {enabled: true, maxRetries, baseDelayMs: 1}.

// autoRetryTransientError is Pi's stand-in for a transient provider failure.
const autoRetryTransientError = "overloaded_error"

// retryRecorder labels Pi's auto-retry events the way Pi's tests do.
type retryRecorder struct {
	labels []string
	starts []event.Event
	ends   []event.Event
	tools  int
}

func (r *retryRecorder) record(e event.Event) {
	switch e.Type {
	case event.AutoRetryStart:
		r.labels = append(r.labels, fmt.Sprintf("start:%d", e.RetryAttempt))
		r.starts = append(r.starts, e)
	case event.AutoRetryEnd:
		r.labels = append(r.labels, fmt.Sprintf("end:success=%t", e.Err == nil))
		r.ends = append(r.ends, e)
	case event.ToolStart:
		r.tools++
	}
}

func (r *retryRecorder) wantLabels(t *testing.T, want ...string) {
	t.Helper()
	if got := strings.Join(r.labels, " "); got != strings.Join(want, " ") {
		t.Fatalf("auto-retry events = %q, want %q", got, strings.Join(want, " "))
	}
}

// wantCancelledEnd checks Pi's auto_retry_end for an aborted retry.
func (r *retryRecorder) wantCancelledEnd(t *testing.T, attempt int) {
	t.Helper()
	if len(r.ends) != 1 || r.ends[0].RetryAttempt != attempt || r.ends[0].Err == nil || r.ends[0].Err.Error() != "Retry cancelled" {
		t.Fatalf("auto_retry_end = %+v, want attempt %d cancelled", r.ends, attempt)
	}
}

// autoRetryHarness is an in-memory loop whose retries wait 1 ms, like Pi's
// tests. ctx is the run context of every prompt; cancel is Pi's abort.
type autoRetryHarness struct {
	loop   *Loop
	store  *session.Store
	cfg    Config
	rec    *retryRecorder
	ctx    context.Context
	cancel context.CancelFunc
	dir    string
}

func newAutoRetryHarness(t *testing.T, send reviewProvider, maxRetries int) *autoRetryHarness {
	t.Helper()
	dir := t.TempDir()
	store, err := session.New(session.OpenOptions{Cwd: dir, SessionDir: dir, AgentDir: dir, Persist: false})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	rec := &retryRecorder{}
	h := &autoRetryHarness{
		store: store, rec: rec, ctx: ctx, cancel: cancel, dir: dir,
		cfg: Config{
			Model:                provider.ModelMeta{ID: "m", ContextWindow: 100000, MaxTokens: 4096},
			RetryAttempts:        maxRetries,
			RetryBaseDelayMs:     1,
			RetryMaxAgentDelayMs: 60000,
		},
	}
	h.loop = New(store, tools.NewRegistry(dir), send, rec.record)
	return h
}

func TestAutoRetryRetriesTransientErrorAndSucceeds(t *testing.T) {
	calls := 0
	fake := reviewProvider(func(context.Context, provider.Request, func(provider.Delta)) (*provider.Response, error) {
		calls++
		if calls == 1 {
			return nil, errors.New(autoRetryTransientError)
		}
		return &provider.Response{Text: "Success", StopReason: "stop"}, nil
	})
	h := newAutoRetryHarness(t, fake, 3)
	if err := h.loop.Submit(h.ctx, "Test", h.cfg); err != nil {
		t.Fatal(err)
	}
	h.rec.wantLabels(t, "start:1", "end:success=true")
	if calls != 2 {
		t.Fatalf("provider calls = %d, want the failed call and one retry", calls)
	}
	if starts := h.rec.starts; starts[0].RetryMaximum != 3 || starts[0].RetryDelayMs != 1 || starts[0].Text != autoRetryTransientError {
		t.Fatalf("auto_retry_start = %+v", starts)
	}
	h.wantProjectedTurns(t, "Test", "Success")
}

func TestAutoRetryRetriesMultipleFailuresThenSucceeds(t *testing.T) {
	calls := 0
	fake := reviewProvider(func(context.Context, provider.Request, func(provider.Delta)) (*provider.Response, error) {
		calls++
		if calls <= 2 {
			return nil, errors.New(autoRetryTransientError)
		}
		return &provider.Response{Text: "success", StopReason: "stop"}, nil
	})
	h := newAutoRetryHarness(t, fake, 3)
	if err := h.loop.Submit(h.ctx, "test", h.cfg); err != nil {
		t.Fatal(err)
	}
	h.rec.wantLabels(t, "start:1", "start:2", "end:success=true")
	if calls != 3 {
		t.Fatalf("provider calls = %d, want two failures and one retry", calls)
	}
}

func TestAutoRetryExhaustsBudgetAndReportsFailure(t *testing.T) {
	calls := 0
	fake := reviewProvider(func(context.Context, provider.Request, func(provider.Delta)) (*provider.Response, error) {
		calls++
		return nil, errors.New(autoRetryTransientError)
	})
	h := newAutoRetryHarness(t, fake, 2)
	err := h.loop.Submit(h.ctx, "test", h.cfg)
	if err == nil || err.Error() != autoRetryTransientError {
		t.Fatalf("run error = %v, want the final provider error", err)
	}
	h.rec.wantLabels(t, "start:1", "start:2", "end:success=false")
	if calls != 3 {
		t.Fatalf("provider calls = %d, want the first call and two retries", calls)
	}
	// The final report carries the completed retry count and the last error.
	if ends := h.rec.ends; len(ends) != 1 || ends[0].RetryAttempt != 2 || ends[0].Err == nil || ends[0].Err.Error() != autoRetryTransientError {
		t.Fatalf("auto_retry_end = %+v", ends)
	}
	if h.loop.retryAttempt != 0 {
		t.Fatalf("retry counter = %d after the run, want a reset counter", h.loop.retryAttempt)
	}
}

// Regression for Pi issue #8826: maxAgentDelayMs caps each retry delay.
func TestAutoRetryCapsAgentDelay(t *testing.T) {
	calls := 0
	fake := reviewProvider(func(context.Context, provider.Request, func(provider.Delta)) (*provider.Response, error) {
		calls++
		return nil, errors.New(autoRetryTransientError)
	})
	h := newAutoRetryHarness(t, fake, 5)
	h.cfg.RetryMaxAgentDelayMs = 5
	if err := h.loop.Submit(h.ctx, "test", h.cfg); err == nil {
		t.Fatal("a permanent failure finished the run without an error")
	}
	h.rec.wantLabels(t, "start:1", "start:2", "start:3", "start:4", "start:5", "end:success=false")
	var delays []int64
	for _, start := range h.rec.starts {
		delays = append(delays, start.RetryDelayMs)
	}
	if fmt.Sprint(delays) != "[1 2 4 5 5]" {
		t.Fatalf("retry delays = %v ms, want [1 2 4 5 5]", delays)
	}
}

// retryDelayMs is Pi's retryDelayMs. The settings are not range-checked, so
// an out-of-range value must not overflow the wait.
func TestRetryDelayDoublesPerAttemptAndStaysInRange(t *testing.T) {
	largestWait := time.Duration(math.MaxInt64/time.Millisecond) * time.Millisecond
	for _, tc := range []struct {
		name                        string
		baseDelayMs, attempt, capMs int
		want                        time.Duration
	}{
		{name: "first retry waits the base delay", baseDelayMs: 2000, attempt: 1, capMs: 60000, want: 2 * time.Second},
		{name: "second retry waits twice the base delay", baseDelayMs: 2000, attempt: 2, capMs: 60000, want: 4 * time.Second},
		{name: "third retry waits four times the base delay", baseDelayMs: 2000, attempt: 3, capMs: 60000, want: 8 * time.Second},
		{name: "fifth retry waits eight times the base delay", baseDelayMs: 2000, attempt: 5, capMs: 60000, want: 32 * time.Second},
		{name: "maxAgentDelayMs caps the wait", baseDelayMs: 2000, attempt: 6, capMs: 60000, want: time.Minute},
		{name: "a larger cap lifts the wait", baseDelayMs: 2000, attempt: 6, capMs: 120000, want: 64 * time.Second},
		{name: "negative base delay waits nothing", baseDelayMs: -5, attempt: 1, capMs: 60000, want: 0},
		{name: "negative cap waits nothing", baseDelayMs: 2000, attempt: 1, capMs: -1, want: 0},
		{name: "largest base delay is capped", baseDelayMs: math.MaxInt, attempt: 1, capMs: 60000, want: time.Minute},
		{name: "largest base delay and cap cannot overflow", baseDelayMs: math.MaxInt, attempt: 10, capMs: math.MaxInt, want: largestWait},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got, want := retryDelayMs(tc.baseDelayMs, tc.attempt, tc.capMs), tc.want; got != want {
				t.Fatalf("retryDelayMs(%d, %d, %d) = %v, want %v", tc.baseDelayMs, tc.attempt, tc.capMs, got, want)
			}
		})
	}
}

func TestAutoRetrySkipsDisabledBudgetAndNonRetryableErrors(t *testing.T) {
	for _, tc := range []struct {
		name       string
		maxRetries int
		message    string
	}{
		{name: "retry disabled", maxRetries: 0, message: autoRetryTransientError},
		{name: "non-retryable error", maxRetries: 3, message: "invalid_api_key"},
		{name: "quota is not transient", maxRetries: 3, message: "insufficient_quota"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			fake := reviewProvider(func(context.Context, provider.Request, func(provider.Delta)) (*provider.Response, error) {
				calls++
				return nil, errors.New(tc.message)
			})
			h := newAutoRetryHarness(t, fake, tc.maxRetries)
			if err := h.loop.Submit(h.ctx, "test", h.cfg); err == nil || err.Error() != tc.message {
				t.Fatalf("run error = %v, want the provider error %q", err, tc.message)
			}
			if calls != 1 {
				t.Fatalf("provider calls = %d, want the single failed call", calls)
			}
			if len(h.rec.labels) != 0 {
				t.Fatalf("auto-retry events = %q, want none", strings.Join(h.rec.labels, " "))
			}
		})
	}
}

// Pi's continue() sends a queued steering or follow-up message with the next
// request; with neither queued, it repeats the failed request.
func TestAutoRetryDeliversQueuedMessagesBeforeTheRetriedRequest(t *testing.T) {
	for _, queued := range []string{"nothing", "steering", "follow-up"} {
		t.Run(queued, func(t *testing.T) {
			calls := 0
			var requests []provider.Request
			var h *autoRetryHarness
			fake := reviewProvider(func(_ context.Context, req provider.Request, _ func(provider.Delta)) (*provider.Response, error) {
				calls++
				requests = append(requests, req)
				if calls > 1 {
					return &provider.Response{Text: "Success", StopReason: "stop"}, nil
				}
				switch queued {
				case "steering":
					h.loop.Steer("queued steering")
				case "follow-up":
					h.loop.FollowUp("queued follow-up")
				}
				return nil, errors.New(autoRetryTransientError)
			})
			h = newAutoRetryHarness(t, fake, 3)
			if err := h.loop.Submit(h.ctx, "Test", h.cfg); err != nil {
				t.Fatal(err)
			}
			h.rec.wantLabels(t, "start:1", "end:success=true")
			if calls != 2 {
				t.Fatalf("provider calls = %d, want the failed call and one retry", calls)
			}
			want := "Test\n"
			if queued == "steering" {
				want += "queued steering\n"
			}
			if queued == "follow-up" {
				want += "queued follow-up\n"
			}
			if got := requestText(requests[1]); got != want {
				t.Fatalf("retried request text = %q, want %q", got, want)
			}
		})
	}
}

// Pi's abortRetry(): cancelling the run during the backoff ends the retry
// with "Retry cancelled" and no further request.
func TestAutoRetryCancelDuringBackoffReportsRetryCancelled(t *testing.T) {
	calls := 0
	fake := reviewProvider(func(context.Context, provider.Request, func(provider.Delta)) (*provider.Response, error) {
		calls++
		return nil, errors.New(autoRetryTransientError)
	})
	h := newAutoRetryHarness(t, fake, 3)
	// The wait must outlast the cancellation, so the run cannot retry first.
	h.cfg.RetryBaseDelayMs = 60000
	h.loop.sink = func(e event.Event) {
		h.rec.record(e)
		if e.Type == event.AutoRetryStart {
			h.cancel()
		}
	}
	if err := h.loop.Submit(h.ctx, "test", h.cfg); !errors.Is(err, context.Canceled) {
		t.Fatalf("run error = %v, want the cancellation", err)
	}
	h.rec.wantLabels(t, "start:1", "end:success=false")
	if calls != 1 {
		t.Fatalf("provider calls = %d, want no request after the cancelled wait", calls)
	}
	h.rec.wantCancelledEnd(t, 1)
}

// Pi issue #9340: an abort requested while a retry is in progress finalizes
// the retry state with one "Retry cancelled" report.
func TestAutoRetryAbortAfterRetriedAttemptFailsFinalizesRetryState(t *testing.T) {
	calls := 0
	var h *autoRetryHarness
	fake := reviewProvider(func(context.Context, provider.Request, func(provider.Delta)) (*provider.Response, error) {
		calls++
		if calls == 2 {
			// The retried attempt fails, and the user aborts at that moment.
			h.cancel()
		}
		return nil, errors.New(autoRetryTransientError)
	})
	h = newAutoRetryHarness(t, fake, 3)
	if err := h.loop.Submit(h.ctx, "test", h.cfg); !errors.Is(err, context.Canceled) {
		t.Fatalf("run error = %v, want the cancellation", err)
	}
	h.rec.wantLabels(t, "start:1", "end:success=false")
	if calls != 2 {
		t.Fatalf("provider calls = %d, want the failed call and the aborted retry", calls)
	}
	h.rec.wantCancelledEnd(t, 1)
	if h.loop.retryAttempt != 0 {
		t.Fatalf("retry counter = %d after the aborted run, want a reset counter", h.loop.retryAttempt)
	}
}

// Pi's prompt() waits for the whole tool loop that a retried response starts.
func TestAutoRetryWaitsForTheToolLoopOfTheRetriedResponse(t *testing.T) {
	calls := 0
	fake := reviewProvider(func(context.Context, provider.Request, func(provider.Delta)) (*provider.Response, error) {
		calls++
		switch calls {
		case 1:
			return nil, errors.New(autoRetryTransientError)
		case 2:
			return &provider.Response{Text: "Looking that up now.", StopReason: "toolUse", ToolCalls: []provider.Block{{Type: "toolCall", ID: "call_1", Name: "write", Arguments: map[string]any{"path": "echo.txt", "content": "hello"}}}}, nil
		default:
			return &provider.Response{Text: "Final answer.", StopReason: "stop"}, nil
		}
	})
	h := newAutoRetryHarness(t, fake, 3)
	h.cfg.Tools = tools.Definitions([]string{"write"})
	if err := h.loop.Submit(h.ctx, "Test", h.cfg); err != nil {
		t.Fatal(err)
	}
	h.rec.wantLabels(t, "start:1", "end:success=true")
	if calls != 3 || h.rec.tools != 1 {
		t.Fatalf("provider calls = %d, tool runs = %d, want the retry, one tool call, and the final answer", calls, h.rec.tools)
	}
	if content, err := os.ReadFile(filepath.Join(h.dir, "echo.txt")); err != nil || string(content) != "hello" {
		t.Fatalf("the tool call of the retried response wrote %q, %v", content, err)
	}
	if h.loop.Running() {
		t.Fatal("loop still running after the retried tool loop")
	}
	if err := h.loop.Submit(h.ctx, "Follow-up", h.cfg); err != nil {
		t.Fatal(err)
	}
	if calls != 4 {
		t.Fatalf("provider calls = %d, want the follow-up prompt to run", calls)
	}
}

// Pi's _omitRecoveryAttempt keeps the failed response in raw history and
// durably removes it from model projection.
func TestAutoRetryOmitsFailedAttemptFromProjectionAndKeepsHistory(t *testing.T) {
	for _, partial := range []string{"", "partial before the drop"} {
		t.Run(fmt.Sprintf("partial=%t", partial != ""), func(t *testing.T) {
			calls := 0
			var requests []provider.Request
			fake := reviewProvider(func(_ context.Context, req provider.Request, delta func(provider.Delta)) (*provider.Response, error) {
				calls++
				requests = append(requests, req)
				if calls == 1 {
					if partial != "" {
						delta(provider.Delta{Kind: provider.DeltaText, Text: partial})
					}
					return nil, errors.New(autoRetryTransientError)
				}
				return &provider.Response{Text: "Success", StopReason: "stop"}, nil
			})
			dir, sdir := t.TempDir(), t.TempDir()
			store, err := session.New(session.OpenOptions{Cwd: dir, SessionDir: sdir, AgentDir: dir, Persist: true})
			if err != nil {
				t.Fatal(err)
			}
			rec := &retryRecorder{}
			loop := New(store, tools.NewRegistry(dir), fake, rec.record)
			cfg := Config{Model: provider.ModelMeta{ID: "m", ContextWindow: 100000, MaxTokens: 4096}, RetryAttempts: 3, RetryBaseDelayMs: 1, RetryMaxAgentDelayMs: 60000}
			if err := loop.Submit(context.Background(), "Test", cfg); err != nil {
				t.Fatal(err)
			}
			rec.wantLabels(t, "start:1", "end:success=true")
			// The retried request carries the same model context as the failed one.
			if len(requests) != 2 || requestText(requests[0]) != "Test\n" || requestText(requests[1]) != "Test\n" {
				t.Fatalf("model context of the failed and retried requests = %q and %q, want the user prompt only", requestText(requests[0]), requestText(requests[1]))
			}
			var failedID string
			failed, edits := 0, 0
			for _, entry := range store.Entries() {
				if entry.ContextEdit != nil {
					// A null replacement omits the target; an unparsed edit is empty.
					replacement := string(entry.ContextEdit.Replacement)
					if entry.ContextEdit.TargetID != failedID || (replacement != "" && replacement != "null") {
						t.Fatalf("context edit = %+v, want a null edit for the failed attempt %q", entry.ContextEdit, failedID)
					}
					edits++
				}
				if entry.Kind != "message" || entry.Message == nil || entry.Message.Message.Assistant == nil {
					continue
				}
				assistant := entry.Message.Message.Assistant
				if assistant.StopReason != "error" {
					continue
				}
				failed++
				failedID = entry.ID()
				if assistant.ErrorMessage == nil || *assistant.ErrorMessage != autoRetryTransientError {
					t.Fatalf("failed attempt error = %v, want the provider error", assistant.ErrorMessage)
				}
				if got := assistantText(entry.Message.Message); got != partial {
					t.Fatalf("failed attempt text = %q, want the streamed partial %q kept in history", got, partial)
				}
			}
			if failed != 1 || edits != 1 {
				t.Fatalf("failed attempts = %d, omission edits = %d, want one of each in raw history", failed, edits)
			}
			wantProjectedTurns(t, store, "Test", "Success")
			// The omission is durable: a reopened session projects the same context.
			reopened, err := session.OpenFile(store.File(), dir, true)
			if err != nil {
				t.Fatal(err)
			}
			wantProjectedTurns(t, reopened, "Test", "Success")
		})
	}
}

// Pi issues 3317, 6019, and 6904: the provider's own transient message is
// the retry's errorMessage.
func TestAutoRetryReportsTheProviderErrorMessage(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
	}{
		{"network connection lost (#3317)", errors.New("Network connection lost.")},
		{"dns lookup failure (#6904)", errors.New("The pending stream has been canceled (caused by: getaddrinfo ENOTFOUND models.example.invalid)")},
		{"openai retry guidance (#6019)", errors.New("An error occurred while processing your request. You can retry your request, or contact us through our help center if the error persists.")},
		{"bedrock retry guidance (#6019)", errors.New(`{"message":"The system encountered an unexpected error during processing. Try your request again."}`)},
		{"provider network_error", errors.New("Provider finish_reason: network_error")},
		{"codex http/2 stream drop", &provider.StreamInterrupt{Err: errors.New("Codex stream interrupted: stream error: stream ID 29; INTERNAL_ERROR; received from peer")}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			fake := reviewProvider(func(context.Context, provider.Request, func(provider.Delta)) (*provider.Response, error) {
				calls++
				if calls == 1 {
					return nil, tc.err
				}
				return &provider.Response{Text: "recovered", StopReason: "stop"}, nil
			})
			h := newAutoRetryHarness(t, fake, 3)
			if err := h.loop.Submit(h.ctx, "test", h.cfg); err != nil {
				t.Fatal(err)
			}
			h.rec.wantLabels(t, "start:1", "end:success=true")
			if starts := h.rec.starts; len(starts) != 1 || starts[0].Text != tc.err.Error() {
				t.Fatalf("auto_retry_start = %+v, want errorMessage %q", starts, tc.err.Error())
			}
			h.wantProjectedTurns(t, "test", "recovered")
		})
	}
}

// Context overflow belongs to compaction recovery, not to the auto-retry
// (Pi's _isRetryableError).
func TestAutoRetryLeavesContextOverflowToCompactionRecovery(t *testing.T) {
	limit := &provider.ProviderError{Kind: provider.ErrContextLimit, Status: 400, Message: "prompt is too long"}
	for _, tc := range []struct {
		name       string
		retried    bool // the context overflows on the retried call
		wantLabels []string
	}{
		{name: "overflow on the first call"},
		{name: "overflow on the retried call", retried: true, wantLabels: []string{"start:1", "end:success=false"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// The overflow ends the first call, or the retried call after the
			// transient failure of the first one.
			wantFailures := 1
			if tc.retried {
				wantFailures = 2
			}
			calls, summaries := 0, 0
			fake := reviewProvider(func(_ context.Context, req provider.Request, _ func(provider.Delta)) (*provider.Response, error) {
				calls++
				if calls == 1 && tc.retried {
					return nil, errors.New(autoRetryTransientError)
				}
				if calls <= wantFailures {
					return nil, limit
				}
				// Overflow recovery requests tool-free summaries until one is
				// in the context; then the original request is repeated.
				if strings.Contains(requestText(req), "summary part") {
					return &provider.Response{Text: "recovered", StopReason: "stop"}, nil
				}
				if len(req.Tools) != 0 || !strings.Contains(strings.ToLower(req.System), "summar") {
					t.Error("overflow recovery did not request a tool-free summary")
				}
				summaries++
				return &provider.Response{Text: "summary part", StopReason: "stop"}, nil
			})
			h := newAutoRetryHarness(t, fake, 3)
			h.cfg.AutoCompact, h.cfg.KeepRecentTokens = true, 1
			// Overflow recovery summarizes earlier turns; seed one.
			if _, err := h.store.AppendMessage(userMsg("older request")); err != nil {
				t.Fatal(err)
			}
			if _, err := h.store.AppendMessage(toSessionAssistant(&provider.Response{Text: "older answer"}, h.cfg)); err != nil {
				t.Fatal(err)
			}
			if err := h.loop.Submit(h.ctx, "current task", h.cfg); err != nil {
				t.Fatal(err)
			}
			h.rec.wantLabels(t, tc.wantLabels...)
			if ends := h.rec.ends; tc.retried && (len(ends) != 1 || ends[0].Err == nil || ends[0].Err.Error() != limit.Error()) {
				t.Fatalf("auto_retry_end = %+v, want the overflow error as the final error", ends)
			}
			// Every later request is a summary until the recovered response.
			if summaries == 0 || calls != wantFailures+summaries+1 {
				t.Fatalf("provider calls = %d with %d summaries, want %d failures, summaries, then one recovered response", calls, summaries, wantFailures)
			}
		})
	}
}

// wantProjectedTurns checks that a store projects one user prompt followed
// by one assistant answer, without any omitted attempt.
func wantProjectedTurns(t *testing.T, store *session.Store, prompt, answer string) {
	t.Helper()
	proj, err := store.Projection()
	if err != nil {
		t.Fatal(err)
	}
	var turns []string
	for _, message := range proj.Flat {
		switch {
		case message.Role == "system":
			continue
		case message.User != nil:
			turns = append(turns, "user:"+userText(message))
		case message.Assistant != nil:
			turns = append(turns, "assistant:"+message.Assistant.StopReason+":"+assistantText(message))
		default:
			turns = append(turns, message.Role)
		}
	}
	if want := fmt.Sprintf("[user:%s assistant:stop:%s]", prompt, answer); fmt.Sprint(turns) != want {
		t.Fatalf("projected turns = %s, want %s", fmt.Sprint(turns), want)
	}
}

func (h *autoRetryHarness) wantProjectedTurns(t *testing.T, prompt, answer string) {
	t.Helper()
	wantProjectedTurns(t, h.store, prompt, answer)
}

// userText is the text content of a user message.
func userText(message session.AgentMessage) string {
	if message.User == nil {
		return "<not a user message>"
	}
	if text, ok := message.User.Content.(string); ok {
		return text
	}
	var out string
	blocks, ok := message.User.Content.([]session.UserContentBlock)
	if !ok {
		return fmt.Sprint(message.User.Content)
	}
	for _, block := range blocks {
		if block.Text != nil {
			out += block.Text.Text
		}
	}
	return out
}

// assistantText is the text content of an assistant message.
func assistantText(message session.AgentMessage) string {
	if message.Assistant == nil {
		return "<not an assistant message>"
	}
	var text strings.Builder
	for _, block := range message.Assistant.Content {
		if block.Text != nil {
			text.WriteString(block.Text.Text)
		}
	}
	return text.String()
}

// requestText is the text content of a provider request's messages.
func requestText(req provider.Request) string {
	var text strings.Builder
	for _, message := range req.Messages {
		for _, block := range message.Content {
			if block.Type == "text" {
				text.WriteString(block.Text)
				text.WriteByte('\n')
			}
		}
	}
	return text.String()
}
