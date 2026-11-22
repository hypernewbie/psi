package app

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hypernewbie/psi/internal/config"
	"github.com/hypernewbie/psi/internal/provider"
	"github.com/hypernewbie/psi/internal/terminal"
)

func codexQuotaOAuthEntry(access string, expires time.Time) string {
	return fmt.Sprintf(`{"openai-codex":{"type":"oauth","access":%q,"refresh":"synthetic-refresh","expires":%d,"accountId":"stored-account"}}`, access, expires.UnixMilli())
}

func newCodexQuotaTestRuntime(t *testing.T) *runtime {
	t.Helper()
	t.Setenv("OPENAI_API_KEY", "")
	r := testRuntime(t)
	r.models = openAILoginTestModels(t)
	if r.models.Providers == nil {
		r.models.Providers = map[string]config.ProviderMeta{}
	}
	meta := r.models.Providers["openai-codex"]
	meta.BaseURL = codexQuotaBaseURL
	r.models.Providers["openai-codex"] = meta
	r.settings.MaxRetries = 0
	writeAppFile(t, filepath.Join(r.agentDir, "auth.json"), codexQuotaOAuthEntry("synthetic-access", time.Now().Add(time.Hour)))
	if err := r.configure(config.StartupOverrides{Provider: "openai-codex", Model: "gpt-5.6-luna", Thinking: "off", ThinkingExplicit: true}); err != nil {
		t.Fatal(err)
	}
	if r.authErr != nil {
		t.Fatalf("codex auth: %v", r.authErr)
	}
	return r
}

type codexQuotaClock struct {
	at time.Time
}

func (c *codexQuotaClock) now() time.Time {
	return c.at
}

func newScheduledCodexQuotaOwner(check quotaCheck, at time.Time) (*quotaOwner, *codexQuotaClock) {
	clock := &codexQuotaClock{at: at}
	q := newQuotaOwner(check)
	q.now = clock.now
	q.jitter = func(time.Duration) time.Duration { return 0 }
	return q, clock
}

func codexQuotaSampleResult(used float64, started, expires time.Time) provider.QuotaUsageResult {
	return provider.QuotaUsageResult{
		Outcome: provider.QuotaUsageSupported, Status: 200, UsedPercent: used, Sample: true,
		StartedAt: started, ExpiresAt: expires,
	}
}

func waitCodexQuotaResult(t *testing.T, q *quotaOwner) quotaResult {
	t.Helper()
	select {
	case result := <-q.done:
		return result
	case <-time.After(5 * time.Second):
		t.Fatal("quota worker did not publish a result")
		return quotaResult{}
	}
}

func TestCodexQuotaOwnerPublishesPrimaryReading(t *testing.T) {
	r := newCodexQuotaTestRuntime(t)
	start := time.Unix(1_800_000_000, 0)
	checked := make(chan struct{}, 1)
	var authPath string
	var notBefore time.Time
	q, clock := newScheduledCodexQuotaOwner(func(_ context.Context, path string, before time.Time) provider.QuotaUsageResult {
		authPath, notBefore = path, before
		checked <- struct{}{}
		return codexQuotaSampleResult(42, start, start.Add(time.Hour))
	}, start)
	defer q.close()

	if !q.observe(r) {
		t.Fatal("eligible activation did not change the owner")
	}
	if due, ok := q.due(); !ok || !due.Equal(clock.at) {
		t.Fatalf("first due = %v/%t, want %v", due, ok, clock.at)
	}
	q.pump(r)
	<-checked
	if !q.handleResult(waitCodexQuotaResult(t, q)) {
		t.Fatal("first usable reading did not request a redraw")
	}
	text, token, deadline := q.presentation(r)
	filled, empty := terminal.FooterBarCells(terminal.FooterBarEighths(42, codexQuotaTrackCells), codexQuotaTrackCells)
	want := "[" + filled + empty + "] Codex: 58% Left"
	if text != want || token != terminal.TokenMuted {
		t.Fatalf("presentation = %q/%q, want %q/%q", text, token, want, terminal.TokenMuted)
	}
	if !deadline.Equal(clock.at.Add(quotaSampleTTL)) {
		t.Fatalf("deadline = %v, want %v", deadline, clock.at.Add(quotaSampleTTL))
	}
	if !q.nextStart.Equal(clock.at.Add(60*time.Second)) || !q.nextPoll.Equal(q.nextStart) {
		t.Fatalf("next start/poll = %v/%v", q.nextStart, q.nextPoll)
	}
	if authPath != filepath.Join(r.agentDir, "auth.json") || !notBefore.IsZero() {
		t.Fatalf("check path=%q notBefore=%v", authPath, notBefore)
	}
	if q.failures != 0 {
		t.Fatalf("successful reading kept %d failures", q.failures)
	}
	q.pump(r) // Not due yet: no early check.
	select {
	case <-checked:
		t.Fatal("pump made an early check")
	default:
	}
}

func TestCodexQuotaSuffixUsesUsedPercentageAndEdges(t *testing.T) {
	for _, tc := range []struct {
		used float64
		want string
	}{
		{0, "100% Left"},
		{0.01, "99% Left"},
		{42, "58% Left"},
		{75, "25% Left"},
		{99, "1% Left"},
		{99.5, "<1% Left"},
		{100, "0% Left"},
	} {
		suffix := codexQuotaSuffix(tc.used)
		if !strings.HasSuffix(suffix, "Codex: "+tc.want) {
			t.Fatalf("used %v suffix = %q, want label %q", tc.used, suffix, tc.want)
		}
		close := strings.Index(suffix, "]")
		if close < 0 || terminal.CellWidth(suffix[:close]) != codexQuotaTrackCells+1 {
			t.Fatalf("used %v track = %q, want %d cells", tc.used, suffix, codexQuotaTrackCells)
		}
	}
}

func TestCodexQuotaBaseDelayBoundaries(t *testing.T) {
	for _, tc := range []struct {
		used float64
		want time.Duration
	}{
		{0, 60 * time.Second},
		{74.999, 60 * time.Second},
		{75, 30 * time.Second},
		{89.999, 30 * time.Second},
		{90, 15 * time.Second},
		{98.999, 15 * time.Second},
		{99, 5 * time.Second},
		{100, 5 * time.Second},
	} {
		if got := quotaBaseDelay(tc.used); got != tc.want {
			t.Fatalf("quotaBaseDelay(%v) = %v, want %v", tc.used, got, tc.want)
		}
	}
}

func TestCodexQuotaOwnerSchedulesSuccessWithJitter(t *testing.T) {
	r := newCodexQuotaTestRuntime(t)
	start := time.Unix(1_800_000_000, 0)
	for _, tc := range []struct {
		used float64
		want time.Duration
	}{
		{42, 60*time.Second + 12*time.Second},
		{75, 30*time.Second + 6*time.Second},
		{90, 15*time.Second + 3*time.Second},
		{99, 5*time.Second + time.Second},
	} {
		q, clock := newScheduledCodexQuotaOwner(nil, start)
		q.jitter = func(max time.Duration) time.Duration { return max }
		q.observe(r)
		q.handleResult(quotaResult{generation: q.generation, usage: codexQuotaSampleResult(tc.used, clock.at, clock.at.Add(time.Hour))})
		if got := q.nextStart.Sub(clock.at); got != tc.want {
			t.Fatalf("used %v delay = %v, want %v", tc.used, got, tc.want)
		}
		if !q.nextPoll.Equal(q.nextStart) {
			t.Fatalf("used %v poll = %v, start = %v", tc.used, q.nextPoll, q.nextStart)
		}
		q.close()
	}
}

func TestCodexQuotaOwnerBackoffGrowthCapAndReset(t *testing.T) {
	r := newCodexQuotaTestRuntime(t)
	q, clock := newScheduledCodexQuotaOwner(nil, time.Unix(1_800_000_000, 0))
	defer q.close()
	q.observe(r)
	for index, want := range []time.Duration{5 * time.Minute, 10 * time.Minute, 20 * time.Minute, 30 * time.Minute, 30 * time.Minute, 30 * time.Minute} {
		if index > 0 {
			clock.at = q.nextStart
		}
		q.handleResult(quotaResult{generation: q.generation, usage: provider.QuotaUsageResult{
			Outcome: provider.QuotaUsageTemporarilyUnavailable, Status: 500, StartedAt: clock.at,
		}})
		if got := q.nextStart.Sub(clock.at); got != want {
			t.Fatalf("failure %d delay = %v, want %v", index+1, got, want)
		}
	}
	if q.failures != quotaBackoffMaxFailures {
		t.Fatalf("failure count = %d, want capped %d", q.failures, quotaBackoffMaxFailures)
	}
	clock.at = q.nextStart
	q.handleResult(quotaResult{generation: q.generation, usage: codexQuotaSampleResult(42, clock.at, clock.at.Add(time.Hour))})
	if q.failures != 0 {
		t.Fatalf("valid reading kept %d failures", q.failures)
	}
	if got := q.nextStart.Sub(clock.at); got != 60*time.Second {
		t.Fatalf("post-reset delay = %v, want 60s", got)
	}
}

func TestCodexQuotaOwnerRetryAfterPrecedenceAndStop(t *testing.T) {
	r := newCodexQuotaTestRuntime(t)
	start := time.Unix(1_800_000_000, 0)

	q, clock := newScheduledCodexQuotaOwner(nil, start)
	q.jitter = func(max time.Duration) time.Duration { return max }
	q.observe(r)
	retryAt := clock.at.Add(2 * time.Hour)
	q.handleResult(quotaResult{generation: q.generation, usage: provider.QuotaUsageResult{
		Outcome: provider.QuotaUsageTemporarilyUnavailable, Status: 429, StartedAt: clock.at, RetryAt: retryAt,
	}})
	if want := retryAt.Add(24 * time.Minute); !q.nextStart.Equal(want) {
		t.Fatalf("server deadline next start = %v, want %v", q.nextStart, want)
	}
	q.close()

	q, clock = newScheduledCodexQuotaOwner(nil, start)
	q.jitter = func(max time.Duration) time.Duration { return max }
	q.observe(r)
	q.handleResult(quotaResult{generation: q.generation, usage: provider.QuotaUsageResult{
		Outcome: provider.QuotaUsageTemporarilyUnavailable, Status: 500, StartedAt: clock.at, RetryAt: clock.at.Add(time.Minute),
	}})
	if want := clock.at.Add(6 * time.Minute); !q.nextStart.Equal(want) {
		t.Fatalf("backoff precedence next start = %v, want %v", q.nextStart, want)
	}
	q.close()

	q, clock = newScheduledCodexQuotaOwner(nil, start)
	q.observe(r)
	q.handleResult(quotaResult{generation: q.generation, usage: provider.QuotaUsageResult{
		Outcome: provider.QuotaUsageTemporarilyUnavailable, Status: 429, StartedAt: clock.at, StopPolling: true,
	}})
	if !q.disabled || !q.nextPoll.IsZero() {
		t.Fatalf("permanent stop disabled=%t nextPoll=%v", q.disabled, q.nextPoll)
	}
	if due, ok := q.due(); ok {
		t.Fatalf("disabled owner armed a check at %v", due)
	}
	r.cfg.Model.Custom = true
	q.observe(r)
	r.cfg.Model.Custom = false
	q.observe(r)
	if due, ok := q.due(); ok {
		t.Fatalf("activation switch bypassed permanent stop at %v", due)
	}
}

func TestCodexQuotaOwnerLocalSkipAndDiscardSchedule(t *testing.T) {
	r := newCodexQuotaTestRuntime(t)
	start := time.Unix(1_800_000_000, 0)

	q, clock := newScheduledCodexQuotaOwner(nil, start)
	q.observe(r)
	q.handleResult(quotaResult{generation: q.generation, usage: provider.QuotaUsageResult{Outcome: provider.QuotaUsageNoCredential}})
	if q.failures != 0 || !q.nextStart.IsZero() {
		t.Fatalf("local skip failures=%d nextStart=%v", q.failures, q.nextStart)
	}
	if want := clock.at.Add(5 * time.Minute); !q.nextPoll.Equal(want) {
		t.Fatalf("local skip next poll = %v, want %v", q.nextPoll, want)
	}
	q.close()

	q, clock = newScheduledCodexQuotaOwner(nil, start)
	q.observe(r)
	q.handleResult(quotaResult{generation: q.generation, usage: provider.QuotaUsageResult{
		Outcome: provider.QuotaUsageNoCredential, Status: 200, StartedAt: clock.at,
	}})
	if q.failures != 0 {
		t.Fatalf("discarded GET counted %d failures", q.failures)
	}
	if want := clock.at.Add(60 * time.Second); !q.nextStart.Equal(want) {
		t.Fatalf("discarded GET next start = %v, want %v", q.nextStart, want)
	}
	if want := clock.at.Add(5 * time.Minute); !q.nextPoll.Equal(want) {
		t.Fatalf("discarded GET next poll = %v, want %v", q.nextPoll, want)
	}
	q.close()
}

func TestCodexQuotaOwnerNon200StopsActivation(t *testing.T) {
	r := newCodexQuotaTestRuntime(t)
	q, clock := newScheduledCodexQuotaOwner(nil, time.Unix(1_800_000_000, 0))
	defer q.close()
	q.observe(r)
	q.handleResult(quotaResult{generation: q.generation, usage: codexQuotaSampleResult(42, clock.at, clock.at.Add(time.Hour))})
	q.handleResult(quotaResult{generation: q.generation, usage: provider.QuotaUsageResult{
		Outcome: provider.QuotaUsageUnsupported, Status: 401, StartedAt: clock.at,
	}})
	if !q.stopped || !q.nextPoll.IsZero() {
		t.Fatalf("non-200 stopped=%t nextPoll=%v", q.stopped, q.nextPoll)
	}
	if want := clock.at.Add(60 * time.Second); !q.nextStart.Equal(want) {
		t.Fatalf("non-200 next start = %v, want %v", q.nextStart, want)
	}
	if q.presentationText(r) != "" {
		t.Fatal("non-200 kept the old reading")
	}

	r.cfg.Model.Custom = true
	q.observe(r)
	r.cfg.Model.Custom = false
	q.observe(r)
	if due, ok := q.due(); !ok || !due.Equal(q.nextStart) {
		t.Fatalf("replacement activation due = %v/%t, want %v", due, ok, q.nextStart)
	}
}

func TestCodexQuotaOwnerSchedulesFromCompletion(t *testing.T) {
	r := newCodexQuotaTestRuntime(t)
	q, clock := newScheduledCodexQuotaOwner(nil, time.Unix(1_800_000_000, 0))
	defer q.close()
	q.observe(r)
	completed := clock.at
	clock.at = completed.Add(10 * time.Minute) // The REPL handled the result late.
	q.handleResult(quotaResult{
		generation:  q.generation,
		completedAt: completed,
		usage:       codexQuotaSampleResult(42, completed, completed.Add(time.Hour)),
	})
	if want := completed.Add(60 * time.Second); !q.nextStart.Equal(want) || !q.nextPoll.Equal(want) {
		t.Fatalf("late-handled result start/poll = %v/%v, want %v", q.nextStart, q.nextPoll, want)
	}
	if q.presentationText(r) != "" {
		t.Fatal("expired late sample was displayed")
	}
}

func TestCodexQuotaOwnerStaleFailureExtendsNewActivation(t *testing.T) {
	r := newCodexQuotaTestRuntime(t)
	q, clock := newScheduledCodexQuotaOwner(nil, time.Unix(1_800_000_000, 0))
	defer q.close()
	started := make(chan struct{}, 2)
	results := make(chan provider.QuotaUsageResult, 2)
	q.check = func(context.Context, string, time.Time) provider.QuotaUsageResult {
		started <- struct{}{}
		return <-results
	}
	q.observe(r)
	q.pump(r)
	<-started
	r.cfg.Model.Custom = true
	q.observe(r)
	results <- provider.QuotaUsageResult{Outcome: provider.QuotaUsageTemporarilyUnavailable, Status: 429, StartedAt: clock.at}
	if q.handleResult(waitCodexQuotaResult(t, q)) {
		t.Fatal("stale 429 requested a redraw")
	}
	if q.presentationText(r) != "" {
		t.Fatal("stale 429 published a reading")
	}
	if q.failures != 1 || !q.nextStart.Equal(clock.at.Add(5*time.Minute)) {
		t.Fatalf("stale 429 failures=%d nextStart=%v", q.failures, q.nextStart)
	}

	r.cfg.Model.Custom = false
	q.observe(r)
	if due, ok := q.due(); !ok || !due.Equal(q.nextStart) {
		t.Fatalf("new activation due = %v/%t, want %v", due, ok, q.nextStart)
	}
	q.pump(r)
	select {
	case <-started:
		t.Fatal("new activation bypassed the stale failure delay")
	default:
	}
	clock.at = q.nextStart
	q.pump(r)
	<-started
	results <- codexQuotaSampleResult(7, clock.at, clock.at.Add(time.Hour))
	if !q.handleResult(waitCodexQuotaResult(t, q)) {
		t.Fatal("new activation did not publish after the delay")
	}
	if got := q.presentationText(r); !strings.HasSuffix(got, "93% Left") {
		t.Fatalf("new activation reading = %q", got)
	}
}

func TestCodexQuotaOwnerStaleSuccessKeepsFailureCount(t *testing.T) {
	r := newCodexQuotaTestRuntime(t)
	q, clock := newScheduledCodexQuotaOwner(nil, time.Unix(1_800_000_000, 0))
	defer q.close()
	started := make(chan struct{}, 1)
	results := make(chan provider.QuotaUsageResult, 1)
	q.check = func(context.Context, string, time.Time) provider.QuotaUsageResult {
		started <- struct{}{}
		return <-results
	}
	q.observe(r)
	q.pump(r)
	<-started
	q.failures = 2
	r.cfg.Model.Custom = true
	q.observe(r)
	r.cfg.Model.Custom = false
	q.observe(r)
	results <- codexQuotaSampleResult(42, clock.at, clock.at.Add(time.Hour))
	if q.handleResult(waitCodexQuotaResult(t, q)) {
		t.Fatal("stale success requested a redraw")
	}
	if q.presentationText(r) != "" {
		t.Fatal("stale success published a reading")
	}
	if q.failures != 2 {
		t.Fatalf("stale success reset failures to %d", q.failures)
	}
	if want := clock.at.Add(60 * time.Second); !q.nextStart.Equal(want) {
		t.Fatalf("stale success next start = %v, want %v", q.nextStart, want)
	}
}

func TestCodexQuotaOwnerCancellationAndOneInFlight(t *testing.T) {
	r := newCodexQuotaTestRuntime(t)
	q, clock := newScheduledCodexQuotaOwner(nil, time.Unix(1_800_000_000, 0))
	defer q.close()
	started := make(chan struct{}, 2)
	release := make(chan struct{})
	q.check = func(context.Context, string, time.Time) provider.QuotaUsageResult {
		started <- struct{}{}
		<-release
		return provider.QuotaUsageResult{Outcome: provider.QuotaUsageTemporarilyUnavailable, StartedAt: clock.at}
	}
	q.observe(r)
	q.pump(r)
	<-started
	q.pump(r)
	select {
	case <-started:
		t.Fatal("a second worker started while one was in flight")
	default:
	}
	close(release)
	if q.handleResult(waitCodexQuotaResult(t, q)) {
		t.Fatal("canceled result requested a redraw")
	}
	if q.failures != 1 {
		// This fake does not mark the owner cancellation; it behaves like an
		// observed transport failure, which must count.
		t.Fatalf("failure count = %d, want 1", q.failures)
	}
	if want := clock.at.Add(5 * time.Minute); !q.nextStart.Equal(want) {
		t.Fatalf("failure next start = %v, want %v", q.nextStart, want)
	}

	canceled := quotaResult{generation: q.generation, canceled: true, usage: provider.QuotaUsageResult{
		Outcome: provider.QuotaUsageTemporarilyUnavailable, StartedAt: clock.at,
	}}
	q.failures = 3
	q.nextStart = time.Time{}
	if q.handleResult(canceled) {
		t.Fatal("owner cancellation requested a redraw")
	}
	if q.failures != 3 {
		t.Fatalf("owner cancellation changed failures to %d", q.failures)
	}
	if want := clock.at.Add(60 * time.Second); !q.nextStart.Equal(want) {
		t.Fatalf("owner cancellation next start = %v, want %v", q.nextStart, want)
	}
	q.nextStart = time.Time{}
	q.failures = 3
	if q.handleResult(quotaResult{generation: q.generation, canceled: true, usage: provider.QuotaUsageResult{Outcome: provider.QuotaUsageTemporarilyUnavailable}}) {
		t.Fatal("cancellation without a GET requested a redraw")
	}
	if !q.nextStart.IsZero() || q.failures != 3 {
		t.Fatalf("cancellation without a GET changed state: start=%v failures=%d", q.nextStart, q.failures)
	}
}

func TestCodexQuotaFailureAndCanceledClassification(t *testing.T) {
	start := time.Unix(1_800_000_000, 0)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for _, tc := range []struct {
		name     string
		usage    provider.QuotaUsageResult
		failure  bool
		canceled bool
	}{
		{"transport", provider.QuotaUsageResult{Outcome: provider.QuotaUsageTemporarilyUnavailable, StartedAt: start}, true, true},
		{"unusable 200", provider.QuotaUsageResult{Outcome: provider.QuotaUsageUnsupported, Status: 200, StartedAt: start}, true, true},
		{"408", provider.QuotaUsageResult{Outcome: provider.QuotaUsageTemporarilyUnavailable, Status: 408, StartedAt: start}, true, false},
		{"429", provider.QuotaUsageResult{Outcome: provider.QuotaUsageTemporarilyUnavailable, Status: 429, StartedAt: start}, true, false},
		{"500", provider.QuotaUsageResult{Outcome: provider.QuotaUsageTemporarilyUnavailable, Status: 500, StartedAt: start}, true, false},
		{"401", provider.QuotaUsageResult{Outcome: provider.QuotaUsageUnsupported, Status: 401, StartedAt: start}, false, false},
		{"discarded credential", provider.QuotaUsageResult{Outcome: provider.QuotaUsageNoCredential, Status: 200, StartedAt: start}, false, true},
		{"local skip", provider.QuotaUsageResult{Outcome: provider.QuotaUsageNoCredential}, false, true},
		{"sample", codexQuotaSampleResult(42, start, start.Add(time.Hour)), false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := quotaGETFailure(tc.usage); got != tc.failure {
				t.Fatalf("failure = %t, want %t", got, tc.failure)
			}
			if got := quotaCanceled(tc.usage, ctx); got != tc.canceled {
				t.Fatalf("canceled = %t, want %t", got, tc.canceled)
			}
		})
	}
}

func TestCodexQuotaOwnerStalePermanentAndLocalSkip(t *testing.T) {
	r := newCodexQuotaTestRuntime(t)
	start := time.Unix(1_800_000_000, 0)
	for _, tc := range []struct {
		name      string
		usage     provider.QuotaUsageResult
		wantStart time.Duration
	}{
		{"permanent status", provider.QuotaUsageResult{Outcome: provider.QuotaUsageUnsupported, Status: 401, StartedAt: start}, 60 * time.Second},
		{"local skip", provider.QuotaUsageResult{Outcome: provider.QuotaUsageNoCredential}, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			q, clock := newScheduledCodexQuotaOwner(nil, start)
			defer q.close()
			started := make(chan struct{}, 1)
			results := make(chan provider.QuotaUsageResult, 1)
			q.check = func(context.Context, string, time.Time) provider.QuotaUsageResult {
				started <- struct{}{}
				return <-results
			}
			q.observe(r)
			q.pump(r)
			<-started
			r.cfg.Model.Custom = true
			q.observe(r)
			results <- tc.usage
			if q.handleResult(waitCodexQuotaResult(t, q)) {
				t.Fatal("stale result requested a redraw")
			}
			wantStart := start.Add(tc.wantStart)
			if tc.wantStart == 0 {
				wantStart = time.Time{}
			}
			if !q.nextStart.Equal(wantStart) {
				t.Fatalf("stale next start = %v, want %v", q.nextStart, wantStart)
			}
			r.cfg.Model.Custom = false
			q.observe(r)
			wantDue := clock.at.Add(tc.wantStart)
			if tc.wantStart == 0 {
				wantDue = clock.at
			}
			if due, ok := q.due(); !ok || !due.Equal(wantDue) {
				t.Fatalf("replacement due = %v/%t, want %v", due, ok, wantDue)
			}
		})
	}
}

func TestCodexQuotaOwnerNoCatchUp(t *testing.T) {
	r := newCodexQuotaTestRuntime(t)
	q, clock := newScheduledCodexQuotaOwner(nil, time.Unix(1_800_000_000, 0))
	defer q.close()
	started := make(chan struct{}, 2)
	results := make(chan provider.QuotaUsageResult, 2)
	q.check = func(context.Context, string, time.Time) provider.QuotaUsageResult {
		started <- struct{}{}
		return <-results
	}
	q.observe(r)
	clock.at = clock.at.Add(10 * time.Minute)
	q.pump(r)
	<-started
	q.pump(r)
	select {
	case <-started:
		t.Fatal("due time started two workers")
	default:
	}
	results <- codexQuotaSampleResult(42, clock.at, clock.at.Add(time.Hour))
	q.handleResult(waitCodexQuotaResult(t, q))
	clock.at = clock.at.Add(10 * time.Minute)
	q.pump(r)
	<-started
	q.pump(r)
	select {
	case <-started:
		t.Fatal("late resume ran catch-up workers")
	default:
	}
	results <- codexQuotaSampleResult(42, clock.at, clock.at.Add(time.Hour))
	q.handleResult(waitCodexQuotaResult(t, q))
}

func TestCodexQuotaActivationGates(t *testing.T) {
	r := newCodexQuotaTestRuntime(t)
	codex := r.prov.(*provider.CodexClient)
	calls := 0
	checked := make(chan struct{}, 8)
	q, clock := newScheduledCodexQuotaOwner(func(context.Context, string, time.Time) provider.QuotaUsageResult {
		calls++
		checked <- struct{}{}
		return provider.QuotaUsageResult{Outcome: provider.QuotaUsageTemporarilyUnavailable, Status: 500}
	}, time.Unix(1_800_000_000, 0))
	defer q.close()

	check := func(name string, wantCalls int) {
		t.Helper()
		if q.nextStart.After(clock.at) {
			clock.at = q.nextStart
		}
		q.observe(r)
		q.pump(r)
		if q.running {
			select {
			case <-checked:
			case <-time.After(5 * time.Second):
				t.Fatalf("%s: quota worker did not start", name)
			}
			q.handleResult(waitCodexQuotaResult(t, q))
		}
		if calls != wantCalls {
			t.Fatalf("%s: calls = %d, want %d", name, calls, wantCalls)
		}
	}
	if _, eligible := quotaIdentityFor(r); !eligible {
		t.Fatalf("baseline Codex client was not eligible: provider=%q api=%q custom=%t override=%t base=%q prov=%T",
			r.ov.Provider, r.cfg.Model.API, r.cfg.Model.Custom, r.quotaAuthOverride, codex.BaseURL, r.prov)
	}
	check("eligible", 1)

	r.ov.Provider = "openai"
	r.cfg.Model.API = "openai-responses"
	r.prov = provider.NewResponsesClient("https://api.openai.com/v1", "synthetic-key", 0, 0)
	check("direct openai", 1)
	if q.presentationText(r) != "" {
		t.Fatal("direct OpenAI inherited a Codex reading")
	}

	r.ov.Provider = "openai-codex"
	r.cfg.Model.API = "openai-codex-responses"
	r.prov = codex
	r.cfg.Model.Custom = true
	check("custom model", 1)
	r.cfg.Model.Custom = false

	codex.BaseURL = "https://api.openai.com/v1"
	check("off origin", 1)
	codex.BaseURL = codexQuotaBaseURL + "/"
	r.quotaAuthOverride = true
	check("authorization override", 1)
	r.quotaAuthOverride = false
	check("official trailing slash", 2)

	codex.Auth.Path = ""
	check("missing auth path", 2)
	codex.Auth.Path = filepath.Join(r.agentDir, "auth.json")
	check("restored auth path", 3)
}

func TestConfigureCapturesCodexQuotaAuthorizationOverride(t *testing.T) {
	for _, tc := range []struct {
		name       string
		headers    map[string]string
		authHeader bool
		want       bool
	}{
		{"lowercase header", map[string]string{"authorization": "Bearer configured"}, false, true},
		{"uppercase header", map[string]string{"AUTHORIZATION": "Bearer configured"}, false, true},
		{"authHeader flag", nil, true, true},
		{"no override", nil, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("OPENAI_API_KEY", "")
			r := testRuntime(t)
			r.models = &config.ModelsFile{Providers: map[string]config.ProviderMeta{"openai-codex": {
				API: "openai-codex-responses", BaseURL: codexQuotaBaseURL, Headers: tc.headers, AuthHeader: tc.authHeader,
				Models: []config.ModelMeta{{ID: "gpt-quota-test", API: "openai-codex-responses", MaxTokens: 1024, ContextWindow: 8192, Input: []string{"text"}}},
			}}}
			writeAppFile(t, filepath.Join(r.agentDir, "auth.json"), codexQuotaOAuthEntry("synthetic-access", time.Now().Add(time.Hour)))
			if err := r.configure(config.StartupOverrides{Provider: "openai-codex", Model: "gpt-quota-test", Thinking: "off"}); err != nil {
				t.Fatal(err)
			}
			if r.quotaAuthOverride != tc.want {
				t.Fatalf("override = %t, want %t", r.quotaAuthOverride, tc.want)
			}
			_, eligible := quotaIdentityFor(r)
			if tc.want == eligible {
				t.Fatalf("eligible = %t with override = %t", eligible, tc.want)
			}
		})
	}
}

func TestCodexQuotaIndicatorThroughREPL(t *testing.T) {
	r := newCodexQuotaTestRuntime(t)
	started := time.Now()
	r.quotaCheck = func(context.Context, string, time.Time) provider.QuotaUsageResult {
		return codexQuotaSampleResult(42, started, started.Add(time.Hour))
	}
	_, out, _, _ := startLoginTestREPLWidth(t, r, 160)
	waitAppOutput(t, out, "Codex: 58% Left")
	if strings.Contains(out.String(), "Using ChatGPT plan") || strings.Contains(out.String(), "Manage usage") {
		t.Fatalf("old fallback text remained: %q", out.String())
	}
}

func codexQuotaReplyStream(text string) string {
	var body strings.Builder
	event := func(typ string, fields map[string]any) {
		fields["type"] = typ
		data, _ := json.Marshal(fields)
		fmt.Fprintf(&body, "event: %s\ndata: %s\n\n", typ, data)
	}
	message := map[string]any{
		"type": "message", "id": "msg_quota", "phase": "final_answer",
		"content": []any{map[string]any{"type": "output_text", "text": text}},
	}
	event("response.output_item.added", map[string]any{"output_index": 0, "item": message})
	event("response.output_text.delta", map[string]any{"output_index": 0, "delta": text})
	event("response.output_item.done", map[string]any{"output_index": 0, "item": message})
	event("response.completed", map[string]any{"response": map[string]any{"id": "resp_quota", "status": "completed", "output": []any{message}}})
	return body.String()
}

func TestCodexQuotaFailedPollClearsBarWithoutChangingReply(t *testing.T) {
	r := newCodexQuotaTestRuntime(t)
	codex := r.prov.(*provider.CodexClient)
	codex.HTTP.Transport = roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.String() != codexQuotaBaseURL+"/codex/responses" {
			return nil, fmt.Errorf("unexpected Codex URL %s", req.URL)
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": {"text/event-stream"}},
			Body:       io.NopCloser(strings.NewReader(codexQuotaReplyStream("CODEX_QUOTA_OK"))),
		}, nil
	})
	fakeNow := time.Now()
	r.codexQuotaSuccessDelay = func(float64) time.Duration { return 0 }
	rendered := make(chan string, 16)
	r.codexQuotaRendered = func(quota string) {
		select {
		case rendered <- quota:
		default:
		}
	}
	var calls atomic.Int32
	r.quotaCheck = func(context.Context, string, time.Time) provider.QuotaUsageResult {
		switch calls.Add(1) {
		case 1:
			return codexQuotaSampleResult(42, fakeNow, fakeNow.Add(time.Hour))
		case 2:
			return provider.QuotaUsageResult{Outcome: provider.QuotaUsageUnsupported, Status: http.StatusUnauthorized, StartedAt: fakeNow}
		default:
			t.Error("quota checker ran after a permanent failure")
			return provider.QuotaUsageResult{Outcome: provider.QuotaUsageNoCredential}
		}
	}
	_, out, inputWrite, result := startLoginTestREPLWidth(t, r, 160)
	waitRendered := func(name string, match func(string) bool) string {
		t.Helper()
		timer := time.NewTimer(5 * time.Second)
		defer timer.Stop()
		for {
			select {
			case quota := <-rendered:
				if match(quota) {
					return quota
				}
			case <-timer.C:
				t.Fatalf("timed out waiting for %s", name)
				return ""
			}
		}
	}
	bar := waitRendered("quota bar", func(quota string) bool { return quota != "" })
	if !strings.Contains(bar, "Codex: 58% Left") {
		t.Fatalf("rendered quota = %q", bar)
	}
	waitRendered("cleared quota", func(quota string) bool { return quota == "" })
	if calls.Load() != 2 {
		t.Fatalf("quota calls = %d, want 2", calls.Load())
	}
	waitAppOutput(t, out, "gpt-5.6-luna")
	if _, err := inputWrite.Write([]byte("hello\r")); err != nil {
		t.Fatal(err)
	}
	waitAppOutput(t, out, "CODEX_QUOTA_OK")
	if err := inputWrite.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-result:
		if err != nil {
			t.Fatalf("REPL exit: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("REPL did not exit")
	}
}

func TestCodexQuotaREPLExitDoesNotWaitForCheck(t *testing.T) {
	r := newCodexQuotaTestRuntime(t)
	started := make(chan struct{})
	release := make(chan struct{})
	done := make(chan struct{})
	r.quotaCheck = func(context.Context, string, time.Time) provider.QuotaUsageResult {
		close(started)
		<-release
		close(done)
		return provider.QuotaUsageResult{Outcome: provider.QuotaUsageTemporarilyUnavailable}
	}
	_, _, inputWrite, result := startLoginTestREPLWidth(t, r, 120)
	<-started
	if err := inputWrite.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-result:
		if err != nil {
			t.Fatalf("REPL exit: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("REPL waited for the quota worker")
	}
	close(release)
	<-done
}

func (q *quotaOwner) presentationText(r *runtime) string {
	text, _, _ := q.presentation(r)
	return text
}
