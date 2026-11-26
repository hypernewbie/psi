package app

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hypernewbie/psi/internal/config"
	"github.com/hypernewbie/psi/internal/provider"
	"github.com/hypernewbie/psi/internal/testutil"
)

func TestUsageAccountPicker(t *testing.T) {
	t.Run("switch accounts without switching inference", func(t *testing.T) {
		r := newUsageZAIRuntime(t)
		t.Setenv("NO_COLOR", "1")
		t.Setenv("DEEPSEEK_API_KEY", deepSeekUsageKey)
		var zaiCalls, deepSeekCalls atomic.Int32
		r.planQuotaCheck = func(context.Context, string, string, time.Time) provider.QuotaUsageResult {
			return zaiPlanSample(27.5, 40)
		}
		initialReply := make(chan struct{})
		r.usagePlanCheck = func(ctx context.Context, _ string, _ string, _ time.Time) provider.QuotaUsageResult {
			zaiCalls.Add(1)
			select {
			case <-initialReply:
				return zaiPlanSample(27.5, 40)
			case <-ctx.Done():
				return provider.QuotaUsageResult{}
			}
		}
		r.usageBalanceCheck = func(_ context.Context, key string, _ time.Time) provider.QuotaUsageResult {
			if key != deepSeekUsageKey {
				t.Error("used the wrong balance credential")
			}
			deepSeekCalls.Add(1)
			return deepSeekBalanceSample(time.Now())
		}
		inference := make(chan string, 1)
		r.prov.(*provider.CompletionsClient).HTTP.Transport = roundTripperFunc(func(req *http.Request) (*http.Response, error) {
			inference <- req.URL.Host
			stream := `data: {"choices":[{"index":0,"delta":{"content":"synthetic reply"},"finish_reason":"stop"}]}` + "\n\ndata: [DONE]\n\n"
			return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(stream))}, nil
		})
		lookups := make(chan usageReport, 4)
		r.usageObserved = func(report usageReport, presented bool) {
			if presented && report.providerID != "" {
				lookups <- report
			}
		}
		_, out, input, _ := startLoginTestREPLWidth(t, r, 160)
		waitAppOutput(t, out, "GLM 5h 72% Left")
		writeUsageKeys(t, input, "/usage\r")
		// The list is available without Enter or a completed provider request.
		waitAppOutput(t, out, "→ ✓ Z.AI")
		writeUsageKeys(t, input, "\x1b[B")
		waitAppOutput(t, out, "→   DeepSeek")
		close(initialReply)
		if report := waitUsageLookup(t, lookups); report.providerID != "zai" || !report.usable() {
			t.Fatalf("bare /usage did not load the active provider: %+v", report)
		}
		waitAppOutput(t, out, "72% left")
		vt := testutil.NewVTModel(160, 24)
		vt.Feed([]byte(out.String()))
		visible := vt.Text()
		detail, accounts := strings.Index(visible, "72% left"), strings.Index(visible, "Accounts")
		if detail < 0 || accounts <= detail || !strings.Contains(visible[accounts:], "→   DeepSeek") {
			t.Fatalf("expected Z.AI detail above Accounts with DeepSeek highlighted: %q", visible)
		}
		if zaiCalls.Load() != 1 || deepSeekCalls.Load() != 0 {
			t.Fatal("opening or navigating Accounts sent an extra GET")
		}
		footerBefore := strings.Count(out.String(), "GLM 5h 72% Left")
		writeUsageKeys(t, input, "\r")
		if report := waitUsageLookup(t, lookups); report.providerID != "deepseek" || !report.usable() {
			t.Fatalf("selection did not load DeepSeek: %+v", report)
		}
		waitAppOutput(t, out, "Balance: USD 1.23")
		waitAppOutput(t, out, "→ ✓ DeepSeek")
		if zaiCalls.Load() != 1 || deepSeekCalls.Load() != 1 {
			t.Fatal("selection did not make exactly one DeepSeek lookup")
		}
		if strings.Contains(out.String(), "DeepSeek USD 1.23") {
			t.Fatal("viewing an inactive account replaced the footer")
		}
		// Dismiss with a single control byte that cannot form an Alt sequence
		// with following text in the test pipe. Esc shares the same branch.
		writeUsageKeys(t, input, "\x03held inference\r")
		waitAppOutput(t, out, "synthetic reply")
		waitAppOutputCount(t, out, "GLM 5h 72% Left", footerBefore+1)
		if got := <-inference; got != "api.z.ai" {
			t.Fatalf("inference went to %q instead of the installed route", got)
		}
	})

	for _, tc := range []struct {
		name   string
		key    string
		config config.ProviderMeta
	}{
		{name: "missing credential"},
		{name: "unsafe credential", key: "unsafe key"},
		{name: "alternate proxy", key: deepSeekUsageKey, config: config.ProviderMeta{BaseURL: "https://proxy.example"}},
		{name: "alternate auth override", key: deepSeekUsageKey, config: config.ProviderMeta{Headers: map[string]string{"X-Api-Key": "synthetic"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newUsageZAIRuntime(t)
			r.models.Providers["deepseek"] = tc.config
			t.Setenv("NO_COLOR", "1")
			t.Setenv("DEEPSEEK_API_KEY", tc.key)
			t.Setenv("ANTHROPIC_API_KEY", "synthetic-unsupported")
			r.planQuotaCheck = func(context.Context, string, string, time.Time) provider.QuotaUsageResult {
				return zaiPlanSample(27.5, 40)
			}
			r.usageBalanceCheck = func(context.Context, string, time.Time) provider.QuotaUsageResult {
				t.Error("discovery sent a balance request")
				return provider.QuotaUsageResult{}
			}
			_, out, input, _ := startLoginTestREPLWidth(t, r, 110)
			writeUsageKeys(t, input, "/usage\r")
			waitAppOutput(t, out, "72% left")
			waitAppOutput(t, out, "→ ✓ Z.AI")
			if strings.Contains(out.String(), "DeepSeek") {
				t.Fatal("ineligible alternate was listed in Accounts")
			}
		})
	}

	t.Run("timeout and dismissal retain slot but discard late discovery", func(t *testing.T) {
		r := newUsageZAIRuntime(t)
		t.Setenv("DEEPSEEK_API_KEY", deepSeekUsageKey)
		r.planQuotaCheck = func(context.Context, string, string, time.Time) provider.QuotaUsageResult {
			return zaiPlanSample(27.5, 40)
		}
		r.usageBalanceCheck = func(context.Context, string, time.Time) provider.QuotaUsageResult {
			t.Error("discovery sent a balance request")
			return provider.QuotaUsageResult{}
		}
		r.usagePlanCheck = func(context.Context, string, string, time.Time) provider.QuotaUsageResult {
			t.Error("initial lookup sent a request after discovery timed out")
			return provider.QuotaUsageResult{}
		}
		trigger := make(chan time.Time, 1)
		r.usageDeadline = func() <-chan time.Time { return trigger }
		observed := make(chan bool, 3)
		r.usageObserved = func(_ usageReport, visible bool) { observed <- visible }
		started := make(chan struct{})
		returnList := make(chan struct{}, 1)
		stopTest := make(chan struct{})
		defer close(stopTest)
		var calls atomic.Int32
		r.usageDiscover = func(context.Context, []usageCapture) ([]string, usageReason) {
			if calls.Add(1) == 1 {
				close(started)
			}
			select {
			case <-returnList:
			case <-stopTest:
			}
			return []string{"zai", "deepseek"}, ""
		}
		_, out, input, _ := startLoginTestREPLSize(t, r, 110, 8)
		writeUsageKeys(t, input, "/usage\r")
		waitBalanceStart(t, started)
		trigger <- time.Now()
		waitAppOutput(t, out, "Accounts: request timed out.")
		writeUsageKeys(t, input, "\x03/usage\r")
		waitAppOutput(t, out, "Usage request already in progress.")
		returnList <- struct{}{}
		if <-observed || calls.Load() != 1 {
			t.Fatal("late discovery was presented or Enter started a second job")
		}
		if text := out.String(); strings.Contains(text, "→ ✓ Z.AI") || strings.Contains(text, "→   DeepSeek") {
			t.Fatal("late discovery reopened Accounts")
		}
	})
}

func TestUsageEnterRequiresVisibleAccountChoice(t *testing.T) {
	view := &usageView{}
	view.showReport(usageReport{providerID: "zai", plan: zaiPlanSample(27.5, 40).Plan})
	view.showAccounts([]string{"zai", "deepseek"})
	view.moveAccount(1)
	view.view(80, 1, true, nil)
	if got := view.selectedAccount(); got != "deepseek" {
		t.Fatalf("visible choice = %q, want deepseek", got)
	}
	view.pageDetail(1)
	view.view(80, 1, true, nil)
	if got := view.selectedAccount(); got != "" {
		t.Errorf("Enter would load hidden choice %q", got)
	}
	view.view(80, 12, true, nil)
	if got := view.selectedAccount(); got != "deepseek" {
		t.Errorf("choice after resizing = %q, want deepseek", got)
	}
}

func TestUsageSelectedProviderGuard(t *testing.T) {
	t.Run("credentials lost between highlight and Enter", func(t *testing.T) {
		r := newUsageZAIRuntime(t)
		t.Setenv("NO_COLOR", "1")
		writeAppFile(t, filepath.Join(r.agentDir, "auth.json"), `{"zai":{"type":"api_key","key":"`+zaiUsageKey+`"},"deepseek":{"type":"api_key","key":"`+deepSeekUsageKey+`"}}`)
		r.planQuotaCheck = func(context.Context, string, string, time.Time) provider.QuotaUsageResult {
			return zaiPlanSample(27.5, 40)
		}
		r.usageBalanceCheck = func(context.Context, string, time.Time) provider.QuotaUsageResult {
			t.Error("sent a balance GET after the key was removed")
			return provider.QuotaUsageResult{}
		}
		_, out, input, _ := startLoginTestREPLWidth(t, r, 110)
		writeUsageKeys(t, input, "/usage\r")
		waitAppOutput(t, out, "Weekly")
		waitAppOutput(t, out, "→ ✓ Z.AI")
		writeUsageKeys(t, input, "\x1b[B")
		waitAppOutput(t, out, "→   DeepSeek")
		writeAppFile(t, filepath.Join(r.agentDir, "auth.json"), `{"zai":{"type":"api_key","key":"`+zaiUsageKey+`"}}`)
		writeUsageKeys(t, input, "\r")
		waitAppOutput(t, out, "Usage · DeepSeek")
		waitAppOutput(t, out, "no usable credential.")
		vt := testutil.NewVTModel(110, 24)
		vt.Feed([]byte(out.String()))
		_, accounts, found := strings.Cut(vt.Text(), "Accounts")
		if !found || !strings.Contains(accounts, "→   Z.AI") || strings.Contains(accounts, "DeepSeek") {
			t.Fatalf("stale account remained selectable: %q", accounts)
		}
	})

	t.Run("catalog replacement and route refusals", func(t *testing.T) {
		r := newUsageZAIRuntime(t)
		r.models.Providers["deepseek"] = config.ProviderMeta{BaseURL: "https://api.deepseek.com//", APIKey: deepSeekUsageKey}
		if _, refusal := r.usageCapture("deepseek"); !strings.Contains(refusal, string(usageReasonIneligible)) {
			t.Fatalf("alternate route with an ambiguous path was accepted: %q", refusal)
		}
		r.models.Providers["deepseek"] = config.ProviderMeta{BaseURL: "https://api.deepseek.com/", APIKey: deepSeekUsageKey}
		r.usageBalanceCheck = func(context.Context, string, time.Time) provider.QuotaUsageResult {
			return deepSeekBalanceSample(time.Now())
		}
		capture, refusal := r.usageCapture("deepseek")
		if refusal != "" {
			t.Fatal(refusal)
		}
		ids, reason := discoverUsageAccounts(context.Background(), r.usageAccountCandidates("zai"))
		if reason != "" || !slices.Equal(ids, []string{"zai", "deepseek"}) {
			t.Fatalf("local discovery: %v, %s", ids, reason)
		}
		if report := fetchUsageReport(context.Background(), capture); !report.usable() || !r.usageStillCurrent(capture) {
			t.Fatalf("direct shortcut did not produce a current reading: %+v", report)
		}
		replacement := *r.models
		replacement.Providers = map[string]config.ProviderMeta{
			"deepseek": {
				APIKey:         deepSeekUsageKey,
				ModelOverrides: map[string]json.RawMessage{"deepseek-v4-pro": json.RawMessage(`{"baseUrl":"https://proxy.example"}`)},
			},
			"zai": {APIKey: zaiUsageKey},
		}
		r.models = &replacement
		if r.usageStillCurrent(capture) {
			t.Fatal("accepted a result from the previous catalog")
		}
		if _, refusal := r.usageCapture("deepseek"); !strings.Contains(refusal, string(usageReasonIneligible)) {
			t.Fatalf("selected model override was not rejected: %q", refusal)
		}
		// A valid local key and direct route do not let an active refusal fall
		// through to the non-active guard.
		r.authErr = io.ErrUnexpectedEOF
		if _, refusal := r.usageCapture("zai"); !strings.Contains(refusal, string(usageReasonIneligible)) {
			t.Fatalf("refused active model fell back to the alternate guard: %q", refusal)
		}
	})
}

func writeUsageKeys(t *testing.T, input *os.File, keys string) {
	t.Helper()
	if _, err := input.Write([]byte(keys)); err != nil {
		t.Fatal(err)
	}
}

func waitUsageLookup(t *testing.T, reports <-chan usageReport) usageReport {
	t.Helper()
	select {
	case report := <-reports:
		return report
	case <-time.After(usageLookupTimeout):
		t.Fatal("usage lookup did not complete")
		return usageReport{}
	}
}
