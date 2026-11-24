package app

import (
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hypernewbie/psi/internal/config"
	"github.com/hypernewbie/psi/internal/provider"
	"github.com/hypernewbie/psi/internal/testutil"
)

const zaiUsageKey = "synthetic-zai-key"

func newUsageZAIRuntime(t *testing.T) *runtime {
	t.Helper()
	clearProviderAPIKeys(t)
	r := testRuntime(t)
	r.models = openAILoginTestModels(t)
	r.settings.MaxRetries = 0
	// The test runtime can open a REPL before configuring any manual lookup.
	// Stub the independent startup poll so tests never contact Z.AI.
	r.planQuotaCheck = func(context.Context, string, string, time.Time) provider.QuotaUsageResult {
		return provider.QuotaUsageResult{Outcome: provider.QuotaUsageNoCredential}
	}
	writeAppFile(t, filepath.Join(r.agentDir, "auth.json"), `{"zai":{"type":"api_key","key":"`+zaiUsageKey+`"}}`)
	if err := r.configure(config.StartupOverrides{Provider: "zai", Model: "glm-5.3", Thinking: "off", ThinkingExplicit: true}); err != nil {
		t.Fatal(err)
	}
	if r.authErr != nil {
		t.Fatalf("Z.AI auth: %v", r.authErr)
	}
	if !r.quotaInstall.hasKey || r.quotaInstall.provider != "zai" {
		t.Fatalf("installed identity = %+v", r.quotaInstall)
	}
	return r
}

// zaiPlanSample returns both plan windows. The footer still reads UsedPercent.
func zaiPlanSample(fiveHour, weekly float64) provider.QuotaUsageResult {
	return provider.QuotaUsageResult{
		Outcome: provider.QuotaUsageSupported, Status: http.StatusOK, Sample: true,
		UsedPercent: fiveHour,
		Plan:        &provider.PlanWindows{FiveHour: &fiveHour, Weekly: &weekly},
		StartedAt:   time.Now(),
	}
}

func waitAppOutputCount(t *testing.T, out *notifyingBuffer, want string, count int) {
	t.Helper()
	deadline := time.After(60 * time.Second)
	for strings.Count(out.String(), want) < count {
		select {
		case <-out.notify:
		case <-deadline:
			t.Fatalf("timed out waiting for %d copies of %q; output=%q", count, want, out.String())
		}
	}
}

// The panel and the Z.AI footer read the same five-hour window of the
// installed client's account, while the panel also shows the weekly window.
// Nothing about the command or its result reaches the transcript.
func TestUsageZAIThroughREPL(t *testing.T) {
	r := newUsageZAIRuntime(t)
	r.planQuotaCheck = func(_ context.Context, kind, key string, _ time.Time) provider.QuotaUsageResult {
		if kind != "zai" || key != zaiUsageKey {
			t.Errorf("usage check used %q/%q", kind, key)
		}
		return zaiPlanSample(27.5, 40)
	}
	entriesBefore := len(r.store.Entries())
	_, out, inputWrite, _ := startLoginTestREPLWidth(t, r, 160)
	waitAppOutput(t, out, "GLM 5h 72% Left")

	if _, err := inputWrite.Write([]byte("/usage zai\r")); err != nil {
		t.Fatal(err)
	}
	waitAppOutput(t, out, "5 hour")
	waitAppOutput(t, out, "72% left")
	waitAppOutput(t, out, "Weekly")
	waitAppOutput(t, out, "60% left")
	if got := len(r.store.Entries()); got != entriesBefore {
		t.Fatalf("/usage stored %d session entries", got-entriesBefore)
	}

	// Ctrl+C dismisses the panel and the editor takes the next keystroke.
	// The test harness sends one control byte so it cannot coalesce with the
	// following text into an Alt sequence.
	if _, err := inputWrite.Write([]byte("\x03")); err != nil {
		t.Fatal(err)
	}
	if _, err := inputWrite.Write([]byte("draft")); err != nil {
		t.Fatal(err)
	}
	waitAppOutput(t, out, "draft")
	vt := testutil.NewVTModel(160, 24)
	vt.Feed([]byte(out.String()))
	visible := strings.Join(vt.Scrollback, "\n") + "\n" + vt.Text()
	if strings.Contains(visible, "Usage · Z.AI") || strings.Contains(visible, "60% left") {
		t.Fatal("dismissed usage detail remained on screen or in scrollback")
	}
}

// A provider without a usage fetcher is refused inside the panel, never as a
// transcript notice.
func TestUsageUnsupportedProviderStaysInPanel(t *testing.T) {
	r := newUsageZAIRuntime(t)
	_, out, inputWrite, _ := startLoginTestREPLWidth(t, r, 160)
	if _, err := inputWrite.Write([]byte("/usage anthropic\r")); err != nil {
		t.Fatal(err)
	}
	waitAppOutput(t, out, "provider is not supported")
	writeUsageKeys(t, inputWrite, "\x03syntax draft")
	waitAppOutput(t, out, "syntax draft")
	vt := testutil.NewVTModel(160, 24)
	vt.Feed([]byte(out.String()))
	if strings.Contains(strings.Join(vt.Scrollback, "\n")+vt.Text(), "provider is not supported") {
		t.Fatal("usage refusal remained on screen or in scrollback after dismissal")
	}
}

// A canceled worker never resolves a credential, and a rejected stored key
// never falls back to the environment variable.
func TestUsageAuthCancellation(t *testing.T) {
	r := newUsageZAIRuntime(t)
	t.Setenv("ZAI_API_KEY", "env-fallback-key")
	capture, refusal := r.usageCapture("zai")
	if refusal != "" {
		t.Fatalf("capture refused: %q", refusal)
	}
	fetched := false
	capture.checkPlan = func(context.Context, string, string, time.Time) provider.QuotaUsageResult {
		fetched = true
		return zaiPlanSample(10, 20)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if report := fetchUsageReport(ctx, capture); report.usable() || report.reason != usageReasonTimeout {
		t.Fatalf("canceled report = %+v", report)
	}
	if fetched {
		t.Fatal("canceled worker fetched usage")
	}

	writeAppFile(t, filepath.Join(r.agentDir, "auth.json"), `{"zai":{"type":"api_key","key":""}}`)
	if report := fetchUsageReport(context.Background(), capture); report.usable() || report.reason != usageReasonNoCredential {
		t.Fatalf("rejected key report = %+v", report)
	}
	if fetched {
		t.Fatal("usage fell back to the environment key")
	}
}

// A custom route, an auth override, or a rotated key must refuse the request
// before any usage traffic leaves the process.
func TestUsageZAIRefusesRouteAndCredential(t *testing.T) {
	r := newUsageZAIRuntime(t)
	capture, refusal := r.usageCapture("zai")
	if refusal != "" || capture.providerID != "zai" {
		t.Fatalf("official route refused: %q", refusal)
	}
	if got, refusal := r.usageCapture(""); refusal != "" || got.providerID != "zai" {
		t.Fatalf("bare command refused: %q", refusal)
	}

	providerMeta := r.models.Providers["zai"]
	customRoute := providerMeta
	customRoute.BaseURL = "https://custom.example/v1"
	r.models.Providers["zai"] = customRoute
	if _, refusal := r.usageCapture("zai"); !strings.Contains(refusal, string(usageReasonIneligible)) {
		t.Fatalf("custom route refusal = %q", refusal)
	}
	authHeader := providerMeta
	authHeader.AuthHeader = true
	r.models.Providers["zai"] = authHeader
	if _, refusal := r.usageCapture("zai"); !strings.Contains(refusal, string(usageReasonIneligible)) {
		t.Fatalf("authHeader refusal = %q", refusal)
	}
	r.models.Providers["zai"] = providerMeta

	if _, refusal := r.usageCapture("not-a-provider"); refusal != "Usage: unknown provider." {
		t.Fatalf("unknown provider refusal = %q", refusal)
	}
	if _, refusal := r.usageCapture("zai extra"); refusal != "Usage: use /usage [provider]." {
		t.Fatalf("extra argument refusal = %q", refusal)
	}

	fetched := false
	capture.checkPlan = func(context.Context, string, string, time.Time) provider.QuotaUsageResult {
		fetched = true
		return zaiPlanSample(10, 20)
	}
	writeAppFile(t, filepath.Join(r.agentDir, "auth.json"), `{"zai":{"type":"api_key","key":"rotated-zai-key"}}`)
	report := fetchUsageReport(context.Background(), capture)
	if report.usable() || report.reason != usageReasonChangedKey {
		t.Fatalf("rotated key report = %+v", report)
	}
	if fetched {
		t.Fatal("usage was fetched with a rotated key")
	}
	if err := os.Remove(filepath.Join(r.agentDir, "auth.json")); err != nil {
		t.Fatal(err)
	}
	if report := fetchUsageReport(context.Background(), capture); report.usable() || report.reason != usageReasonNoCredential {
		t.Fatalf("missing key report = %+v", report)
	}
}

// A short terminal shows only part of the panel; the existing navigation keys
// reach the rows below the viewport.
func TestUsagePanelReachesLastRowAtShortHeight(t *testing.T) {
	r := newUsageZAIRuntime(t)
	r.planQuotaCheck = func(context.Context, string, string, time.Time) provider.QuotaUsageResult {
		return zaiPlanSample(27.5, 40)
	}
	_, out, inputWrite, _ := startLoginTestREPLSize(t, r, 80, 8)
	if _, err := inputWrite.Write([]byte("/usage\r")); err != nil {
		t.Fatal(err)
	}
	waitAppOutput(t, out, "5 hour")
	if strings.Contains(out.String(), "Weekly") {
		t.Fatalf("short panel already showed the clipped row: %q", out.String())
	}
	if _, err := inputWrite.Write([]byte("\x1b[B")); err != nil {
		t.Fatal(err)
	}
	waitAppOutput(t, out, "Weekly")
}

func TestUsageCompletionCutoff(t *testing.T) {
	cutoff := time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC)
	request := usageRequest{cutoff: cutoff}
	// A ready result and timer can be selected in either order. Only the
	// worker's actual completion time determines which sample can display.
	for _, tc := range []struct {
		when time.Time
		want bool
	}{
		{cutoff.Add(-time.Nanosecond), true},
		{cutoff, true},
		{cutoff.Add(time.Nanosecond), false},
	} {
		if got := request.completedInTime(usageJobResult{completedAt: tc.when}); got != tc.want {
			t.Errorf("completion at cutoff %+v: accepted=%t, want %t", tc.when.Sub(cutoff), got, tc.want)
		}
	}
}

// The lookup deadline prints once, keeps the slot occupied for the late
// worker, and never shows the expired reading.
func TestUsageZAIDeadline(t *testing.T) {
	r := newUsageZAIRuntime(t)
	trigger := make(chan time.Time, 1)
	r.usageDeadline = func() <-chan time.Time { return trigger }
	observed := make(chan bool, 4)
	r.usageObserved = func(_ usageReport, printable bool) { observed <- printable }
	started := make(chan struct{}, 4)
	release := make(chan struct{}, 4)
	var calls int
	r.planQuotaCheck = func(_ context.Context, kind, key string, _ time.Time) provider.QuotaUsageResult {
		calls++
		started <- struct{}{}
		if calls == 2 {
			// The first manual request is held past the deadline.
			<-release
		}
		return zaiPlanSample(27.5, 40)
	}
	_, out, inputWrite, _ := startLoginTestREPLWidth(t, r, 160)
	<-started
	waitAppOutput(t, out, "GLM 5h 72% Left")

	if _, err := inputWrite.Write([]byte("/usage zai\r")); err != nil {
		t.Fatal(err)
	}
	<-started
	trigger <- time.Now()
	waitAppOutput(t, out, "request timed out.")
	// Dismissing the panel retires the request but keeps its slot until the
	// worker returns, so a new command sees the busy row.
	if _, err := inputWrite.Write([]byte("\x03")); err != nil {
		t.Fatal(err)
	}
	if _, err := inputWrite.Write([]byte("/usage zai\r")); err != nil {
		t.Fatal(err)
	}
	waitAppOutput(t, out, "Usage request already in progress.")
	release <- struct{}{}
	if printable := <-observed; printable {
		t.Fatal("late result printed after the deadline")
	}
	if got := strings.Count(out.String(), "72% left"); got != 0 {
		t.Fatalf("expired reading printed %d times", got)
	}
	// The late completion frees the slot; the next request reports normally.
	// The busy panel owns the keyboard, so it must be dismissed first.
	if _, err := inputWrite.Write([]byte("\x03")); err != nil {
		t.Fatal(err)
	}
	if _, err := inputWrite.Write([]byte("/usage zai\r")); err != nil {
		t.Fatal(err)
	}
	<-started
	waitAppOutput(t, out, "72% left")
	if got := strings.Count(out.String(), "72% left"); got != 1 {
		t.Fatalf("usable report printed %d times", got)
	}
}

// A held inference stream keeps running while /usage fetches and the panel
// opens. The panel never steers the run, never adds a provider request, and
// closing it leaves the editor usable before the reply arrives.
func TestUsageBusyREPL(t *testing.T) {
	r := newUsageZAIRuntime(t)
	r.planQuotaCheck = func(context.Context, string, string, time.Time) provider.QuotaUsageResult {
		return zaiPlanSample(27.5, 40)
	}
	client, ok := r.prov.(*provider.CompletionsClient)
	if !ok {
		t.Fatalf("installed provider = %T", r.prov)
	}
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	var releaseOnce sync.Once
	releaseNow := func() { releaseOnce.Do(func() { close(release) }) }
	defer releaseNow()
	var requests atomic.Int64
	var requestBody atomic.Value
	client.HTTP.Transport = roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		requests.Add(1)
		if body, err := io.ReadAll(req.Body); err == nil {
			requestBody.Store(string(body))
		}
		select {
		case started <- struct{}{}:
		default:
		}
		<-release
		stream := "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"held reply\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n"
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(stream))}, nil
	})
	_, out, inputWrite, result := startLoginTestREPLWidth(t, r, 160)
	if _, err := inputWrite.Write([]byte("held prompt\r")); err != nil {
		t.Fatal(err)
	}
	<-started
	if _, err := inputWrite.Write([]byte("/usage\r")); err != nil {
		t.Fatal(err)
	}
	waitAppOutput(t, out, "72% left")
	if _, err := inputWrite.Write([]byte("\x03")); err != nil {
		t.Fatal(err)
	}
	// A local command proves the editor took focus back from the panel. It
	// must not reach the provider or steer the held run.
	if _, err := inputWrite.Write([]byte("/hotkeys\r")); err != nil {
		t.Fatal(err)
	}
	waitAppOutput(t, out, "Keys (fixed")
	releaseNow()
	waitAppOutput(t, out, "held reply")
	if _, err := inputWrite.Write([]byte("/quit\r")); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-result:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("REPL did not exit")
	}
	if got := requests.Load(); got != 1 {
		t.Fatalf("provider requests = %d, want the original run only", got)
	}
	if body, _ := requestBody.Load().(string); strings.Contains(body, "/usage") {
		t.Fatalf("usage command reached the provider: %s", body)
	}
}
