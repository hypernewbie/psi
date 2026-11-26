package app

import (
	"context"
	"crypto/sha256"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/hypernewbie/psi/internal/provider"
	"github.com/hypernewbie/psi/internal/providerregistry"
)

func usePlanQuotaClient(r *runtime, kind, api string) *provider.Client {
	r.ov.Provider = kind
	r.cfg.Model.Provider = kind
	r.cfg.Model.API = api
	r.cfg.Model.Custom = false
	r.authErr = nil
	r.quotaAuthOverride = false
	base := providerregistry.DefaultEndpoint(kind, api)
	var client *provider.Client
	switch api {
	case "openai-completions":
		p := provider.NewCompletionsClient(base, "synthetic-key", 0, 0)
		r.prov, client = p, p.Client
	case "openai-responses":
		p := provider.NewResponsesClient(base, "synthetic-key", 0, 0)
		r.prov, client = p, p.Client
	case "google-generative-ai":
		p := provider.NewGoogleClient(base, "synthetic-key", 0, 0)
		r.prov, client = p, p.Client
	case "anthropic-messages":
		p := provider.NewClient(base, "synthetic-key", 0, 0)
		r.prov, client = p, p
	}
	if client != nil {
		r.quotaInstall = quotaInstallation{
			provider: kind,
			model:    r.ov.Model,
			api:      api,
			base:     strings.TrimRight(base, "/"),
			keySHA:   sha256.Sum256([]byte(client.APIKey)),
			hasKey:   client.APIKey != "",
		}
	}
	return client
}

func TestPlanQuotaEligibility(t *testing.T) {
	for _, tc := range []struct{ kind, api string }{
		{"zai", "openai-completions"}, {"opencode-go", "openai-completions"},
		{"opencode-go", "openai-responses"}, {"opencode-go", "google-generative-ai"}, {"opencode-go", "anthropic-messages"},
	} {
		t.Run(tc.kind+"/"+tc.api, func(t *testing.T) {
			r := newCodexQuotaTestRuntime(t)
			client := usePlanQuotaClient(r, tc.kind, tc.api)
			if _, ok := quotaIdentityFor(r); !ok {
				t.Fatal("official provider not eligible")
			}
			client.Headers = map[string]string{"X-API-Key": "override"}
			if _, ok := quotaIdentityFor(r); ok {
				t.Fatal("header override eligible")
			}
			client.Headers = nil
			client.BaseURL = "https://untrusted.example"
			if _, ok := quotaIdentityFor(r); ok {
				t.Fatal("untrusted endpoint eligible")
			}
			client.BaseURL = providerregistry.DefaultEndpoint(tc.kind, tc.api)
			r.cfg.Model.Custom = true
			if _, ok := quotaIdentityFor(r); ok {
				t.Fatal("custom model eligible")
			}
		})
	}
}

func TestPlanQuotaShowsAllGoWindowsAndSwitches(t *testing.T) {
	r := newCodexQuotaTestRuntime(t)
	usePlanQuotaClient(r, "opencode-go", "openai-completions")
	start := time.Unix(1_800_000_000, 0)
	q, clock := newScheduledCodexQuotaOwner(nil, start)
	defer q.close()
	q.checkPlan = func(_ context.Context, kind, key string, _ time.Time) provider.QuotaUsageResult {
		if kind != "opencode-go" || key != "synthetic-key" {
			t.Errorf("wrong active credential")
		}
		return provider.QuotaUsageResult{Outcome: provider.QuotaUsageSupported, Status: http.StatusOK, Sample: true, UsedPercent: 10, Windows: provider.QuotaWindows{Rolling: 10, Weekly: 55.5, Monthly: 99.5}, StartedAt: start}
	}
	q.observe(r)
	q.pump(r)
	if !q.handleResult(waitCodexQuotaResult(t, q)) {
		t.Fatal("sample did not redraw")
	}
	if got, _, _ := q.presentation(r); got != "Go R90% W44% M<1%" {
		t.Fatalf("footer = %q", got)
	}
	if want := clock.at.Add(5 * time.Second); !q.nextStart.Equal(want) {
		t.Fatalf("most used window should set cadence: %v", q.nextStart)
	}
	updated := provider.QuotaUsageResult{Outcome: provider.QuotaUsageSupported, Status: 200, Sample: true, UsedPercent: 10, Windows: provider.QuotaWindows{Rolling: 10, Weekly: 70, Monthly: 99.5}, StartedAt: start}
	if !q.handleResult(quotaResult{kind: "opencode-go", generation: q.generation, usage: updated}) {
		t.Fatal("weekly change did not redraw")
	}
	if got, _, _ := q.presentation(r); got != "Go R90% W30% M<1%" {
		t.Fatalf("updated footer = %q", got)
	}
	usePlanQuotaClient(r, "zai", "openai-completions")
	q.observe(r)
	if got, _, _ := q.presentation(r); got != "" {
		t.Fatalf("old provider remained visible: %q", got)
	}
	q.handleResult(quotaResult{kind: "zai", generation: q.generation, usage: provider.QuotaUsageResult{Outcome: provider.QuotaUsageSupported, Status: 200, Sample: true, UsedPercent: 25, StartedAt: start}})
	if got, _, _ := q.presentation(r); got != "[███████▌░░] GLM 5h 75% Left" {
		t.Fatalf("GLM footer = %q", got)
	}
}

func TestPlanQuotaIndicatorThroughREPL(t *testing.T) {
	r := newCodexQuotaTestRuntime(t)
	usePlanQuotaClient(r, "opencode-go", "openai-completions")
	r.planQuotaCheck = func(_ context.Context, kind, key string, _ time.Time) provider.QuotaUsageResult {
		return provider.QuotaUsageResult{Outcome: provider.QuotaUsageSupported, Status: 200, Sample: true, UsedPercent: 10, Windows: provider.QuotaWindows{Rolling: 10, Weekly: 55, Monthly: 99.5}, StartedAt: time.Now()}
	}
	_, out, _, _ := startLoginTestREPLWidth(t, r, 160)
	waitAppOutput(t, out, "Go R90% W45% M<1%")
}

func TestPlanQuotaSwitchCancelsAndDiscardsInFlightSample(t *testing.T) {
	r := newCodexQuotaTestRuntime(t)
	usePlanQuotaClient(r, "opencode-go", "openai-completions")
	start := time.Unix(1_800_000_000, 0)
	q, _ := newScheduledCodexQuotaOwner(nil, start)
	defer q.close()
	started := make(chan struct{})
	q.checkPlan = func(ctx context.Context, kind, key string, _ time.Time) provider.QuotaUsageResult {
		close(started)
		<-ctx.Done()
		return provider.QuotaUsageResult{Outcome: provider.QuotaUsageSupported, Status: 200, Sample: true, UsedPercent: 10, Windows: provider.QuotaWindows{Rolling: 10, Weekly: 20, Monthly: 30}, StartedAt: start}
	}
	q.observe(r)
	q.pump(r)
	<-started
	usePlanQuotaClient(r, "zai", "openai-completions")
	q.observe(r)
	if q.handleResult(waitCodexQuotaResult(t, q)) {
		t.Fatal("stale sample requested a redraw")
	}
	if text, _, _ := q.presentation(r); text != "" {
		t.Fatalf("stale sample displayed: %q", text)
	}
	if due, ok := q.due(); !ok || !due.Equal(start) {
		t.Fatalf("new provider delayed by stale request: %v/%t", due, ok)
	}
}

func TestPlanQuotaStaleProviderStopIsScoped(t *testing.T) {
	r := newCodexQuotaTestRuntime(t)
	q, _ := newScheduledCodexQuotaOwner(nil, time.Unix(1_800_000_000, 0))
	defer q.close()
	q.observe(r)
	oldGeneration := q.generation
	usePlanQuotaClient(r, "opencode-go", "openai-completions")
	q.observe(r)
	q.handleResult(quotaResult{kind: "openai-codex", generation: oldGeneration, usage: provider.QuotaUsageResult{Status: 429, StopPolling: true}})
	if q.disabled {
		t.Fatal("old provider stopped new provider")
	}
	if _, ok := q.due(); !ok {
		t.Fatal("new provider no longer scheduled")
	}
	// The old provider's permanent stop still applies when returning to it.
	r = newCodexQuotaTestRuntime(t)
	q.observe(r)
	if !q.disabled {
		t.Fatal("old provider stop was lost")
	}
}
