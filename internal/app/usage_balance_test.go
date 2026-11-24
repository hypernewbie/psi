package app

import (
	"context"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/hypernewbie/psi/internal/config"
	"github.com/hypernewbie/psi/internal/provider"
	"github.com/hypernewbie/psi/internal/terminal"
)

const deepSeekUsageKey = "synthetic-deepseek-key"

func newUsageDeepSeekRuntime(t *testing.T) *runtime {
	t.Helper()
	clearProviderAPIKeys(t)
	r := testRuntime(t)
	r.models = openAILoginTestModels(t)
	writeDeepSeekUsageKey(t, r, deepSeekUsageKey)
	if err := r.configure(config.StartupOverrides{Provider: "deepseek", Model: "deepseek-v4-pro", Thinking: "off", ThinkingExplicit: true}); err != nil {
		t.Fatal(err)
	}
	if r.authErr != nil {
		t.Fatal(r.authErr)
	}
	return r
}

func writeDeepSeekUsageKey(t *testing.T, r *runtime, key string) {
	t.Helper()
	writeAppFile(t, filepath.Join(r.agentDir, "auth.json"), `{"deepseek":{"type":"api_key","key":"`+key+`"}}`)
}

func deepSeekBalanceSample(start time.Time) provider.QuotaUsageResult {
	return provider.QuotaUsageResult{
		Outcome: provider.QuotaUsageSupported, Status: http.StatusOK, Sample: true, StartedAt: start,
		Balance: &provider.DeepSeekBalance{Balances: []provider.DeepSeekCurrencyBalance{
			{Currency: "CNY", Total: "0.00", Granted: "0.00"}, {Currency: "USD", Total: "1.23", ToppedUp: "1.00"},
		}},
	}
}

func TestUsageDeepSeekThroughREPL(t *testing.T) {
	r := newUsageDeepSeekRuntime(t)
	r.usageBalanceCheck = func(_ context.Context, key string, _ time.Time) provider.QuotaUsageResult {
		if key != deepSeekUsageKey {
			t.Error("balance used the wrong key")
		}
		return deepSeekBalanceSample(time.Now())
	}
	_, out, input, _ := startLoginTestREPLWidth(t, r, 160)
	waitAppOutput(t, out, "DeepSeek USD 1.23")
	if _, err := input.Write([]byte("/usage deepseek\r")); err != nil {
		t.Fatal(err)
	}
	waitAppOutput(t, out, "Balance: USD 1.23")
	waitAppOutput(t, out, "Balance: CNY 0.00")
	waitAppOutput(t, out, "Topped up: USD 1.00")
}

func TestUsageFooterLifecycle(t *testing.T) {
	t.Run("credential rotation before and during a request", func(t *testing.T) {
		r, q, now := newBalanceOwnerTest(t)
		writeDeepSeekUsageKey(t, r, "rotated-before")
		calls := make(chan struct{}, 1)
		r.usageBalanceCheck = func(context.Context, string, time.Time) provider.QuotaUsageResult {
			calls <- struct{}{}
			return deepSeekBalanceSample(*now)
		}
		q.pump(r)
		if result := waitBalanceResult(t, q); result.usage.Outcome != provider.QuotaUsageNoCredential || q.handleResult(result) {
			t.Fatalf("rotated credential published: %+v", result)
		}
		if len(calls) != 0 {
			t.Fatal("sent a GET after the credential changed")
		}

		writeDeepSeekUsageKey(t, r, deepSeekUsageKey)
		*now = q.nextPoll
		started := make(chan struct{})
		release := make(chan struct{})
		defer close(release)
		returnSample := make(chan struct{}, 1)
		start := *now
		r.usageBalanceCheck = func(ctx context.Context, _ string, _ time.Time) provider.QuotaUsageResult {
			close(started)
			select {
			case <-returnSample:
			case <-release:
			case <-ctx.Done():
			}
			return deepSeekBalanceSample(start)
		}
		q.pump(r)
		waitBalanceStart(t, started)
		writeDeepSeekUsageKey(t, r, "rotated-during")
		returnSample <- struct{}{}
		res := waitBalanceResult(t, q)
		if res.usage.Sample || res.usage.Balance != nil || res.usage.Status != http.StatusOK || q.handleResult(res) {
			t.Fatalf("did not discard the wrong-account sample and retain status: %+v", res)
		}
	})

	t.Run("expiry does not poll early and identity change or failure clears", func(t *testing.T) {
		r, q, now := newBalanceOwnerTest(t)
		start := *now
		r.usageBalanceCheck = func(context.Context, string, time.Time) provider.QuotaUsageResult {
			return deepSeekBalanceSample(start)
		}
		q.pump(r)
		if !q.handleResult(waitBalanceResult(t, q)) || q.nextPoll != start.Add(accountQuotaPoll) {
			t.Fatal("first sample did not publish or schedule from completion")
		}
		// An existing cooldown may be later than this sample's expiry.
		q.pushStart(start.Add(2 * accountQuotaTTL))
		at, ok := q.due()
		if !ok || at != start.Add(accountQuotaTTL) {
			t.Fatalf("next wake = %v, want sample expiry", at)
		}
		*now = at
		if !q.expireAccount() || q.expireAccount() {
			t.Fatal("expiry should request exactly one redraw")
		}
		q.pump(r)
		if q.running {
			t.Fatal("expiry started a poll before the cooldown")
		}
		*now = q.nextStart
		start = *now
		q.pump(r)
		q.handleResult(waitBalanceResult(t, q))
		*now = q.nextPoll
		r.usageBalanceCheck = func(context.Context, string, time.Time) provider.QuotaUsageResult {
			return provider.QuotaUsageResult{StartedAt: start, Status: http.StatusTooManyRequests}
		}
		q.pump(r)
		if !q.handleResult(waitBalanceResult(t, q)) || q.displayValid {
			t.Fatal("failed refresh left a stale amount")
		}
		*now = q.nextPoll
		start = *now
		r.usageBalanceCheck = func(context.Context, string, time.Time) provider.QuotaUsageResult {
			return deepSeekBalanceSample(start)
		}
		q.pump(r)
		q.handleResult(waitBalanceResult(t, q))
		r.models.Providers["deepseek"] = config.ProviderMeta{BaseURL: "https://custom.example"}
		if !q.observe(r) || q.displayValid || q.eligible {
			t.Fatal("picker-only route change did not invalidate balance")
		}
	})

	t.Run("retiring holds the slot and enforces spacing from completion", func(t *testing.T) {
		r, q, now := newBalanceOwnerTest(t)
		start := *now
		started := make(chan struct{})
		release := make(chan struct{})
		unhold := make(chan struct{}, 1)
		defer close(release)
		r.usageBalanceCheck = func(ctx context.Context, _ string, _ time.Time) provider.QuotaUsageResult {
			close(started)
			select {
			case <-unhold:
			case <-release:
			}
			return deepSeekBalanceSample(start)
		}
		q.pump(r)
		waitBalanceStart(t, started)
		q.pauseAccount()
		q.observe(r)
		q.pump(r)
		if !q.running || !q.eligible {
			t.Fatal("retirement freed the worker slot or prevented reactivation")
		}
		unhold <- struct{}{}
		if q.handleResult(waitBalanceResult(t, q)) || q.displayValid {
			t.Fatal("late result published after reactivation")
		}
		at, ok := q.due()
		if !ok || at.Before(now.Add(quotaMinSpacing)) {
			t.Fatalf("retired poll did not keep its completion floor: %v", at)
		}
	})
}

func newBalanceOwnerTest(t *testing.T) (*runtime, *quotaOwner, *time.Time) {
	t.Helper()
	r := newUsageDeepSeekRuntime(t)
	q := newQuotaOwner(nil)
	now := time.Date(2030, 1, 2, 0, 0, 0, 0, time.UTC)
	q.now = func() time.Time { return now }
	q.jitter = func(time.Duration) time.Duration { return 0 }
	if !q.observe(r) || !q.eligible {
		t.Fatal("direct DeepSeek route not eligible")
	}
	t.Cleanup(q.close)
	return r, q, &now
}

func waitBalanceStart(t *testing.T, started <-chan struct{}) {
	t.Helper()
	select {
	case <-started:
	case <-time.After(usageLookupTimeout):
		t.Fatal("balance worker did not start")
	}
}

func waitBalanceResult(t *testing.T, q *quotaOwner) quotaResult {
	t.Helper()
	select {
	case result := <-q.done:
		return result
	case <-time.After(usageLookupTimeout):
		t.Fatal("no balance completion")
		return quotaResult{}
	}
}

func TestUsageDeepSeekFooterPriority(t *testing.T) {
	balance := deepSeekBalanceSample(time.Time{}).Balance
	if text, token := deepSeekQuotaSuffix(balance); text != "DeepSeek USD 1.23" || token != terminal.TokenText {
		t.Fatalf("preferred USD: %q %q", text, token)
	}
	balance.Balances = balance.Balances[:1]
	if text, token := deepSeekQuotaSuffix(balance); text != "DeepSeek CNY 0.00" || token != terminal.TokenError {
		t.Fatalf("alternate zero balance: %q %q", text, token)
	}
	balance.Balances[0].Total = "4.23"
	available := false
	balance.Available = &available
	if text, token := deepSeekQuotaSuffix(balance); text != "DeepSeek unavailable" || token != terminal.TokenError {
		t.Fatalf("explicit unavailability: %q %q", text, token)
	}
}
