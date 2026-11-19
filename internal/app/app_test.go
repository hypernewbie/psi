package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	goruntime "runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/creack/pty"

	"psi/internal/agent"
	"psi/internal/config"
	"psi/internal/provider"
	"psi/internal/providerregistry"
	"psi/internal/session"
	"psi/internal/terminal"
	"psi/internal/testutil"
)

func TestParseBoundary(t *testing.T) {
	o, _, err := Parse([]string{"--provider", "anthropic", "--model", "m", "--thinking", "high", "--session-dir", "/tmp", "--tools", "read,bash", "--no-session", "--use-theme", "light/dark", "--theme", "./themes/a.json", "--theme", "./themes/b.json", "--no-themes"})
	if err != nil || o.Model != "m" || !o.NoSession || o.UseTheme != "light/dark" || !o.NoThemes || len(o.ThemePaths) != 2 {
		t.Fatalf("%+v %v", o, err)
	}
	for _, args := range [][]string{{"--continue", "--resume"}, {"--allow-project", "--deny-project"}, {"--print", "prompt"}, {"--tui-mode", "fullscreen"}} {
		if _, _, err := Parse(args); err == nil {
			t.Errorf("accepted conflicting/unsupported arguments: %v", args)
		}
	}
	o, _, err = Parse([]string{"--mode", "rpc"})
	if err != nil || o.Mode != "rpc" {
		t.Fatalf("rejected --mode rpc: %+v %v", o, err)
	}
}

func TestPiCLICompatibilityAliasesAndInitialMessages(t *testing.T) {
	o, _, err := Parse([]string{"first prompt", "-c", "--model", "openai-codex/gpt-5.6-luna:xhigh", "second prompt", "-t", "read,write", "--name", "Refactor auth", "--no-context-files", "-ns", "--mode=text", "--tui-mode=regular", "--system-prompt", "replace base", "--append-system-prompt", "extra rules", "--append-system-prompt", "more rules", "-nbt"})
	if err != nil {
		t.Fatal(err)
	}
	if !o.Continue || o.Provider != "openai-codex" || o.Model != "gpt-5.6-luna" || o.Thinking != "xhigh" || o.Tools != "read,write" || o.Name != "Refactor auth" || !o.NoContextFiles || !o.NoSkills || !o.NoTools || o.SystemPrompt != "replace base" || !reflect.DeepEqual(o.AppendSystemPrompts, []string{"extra rules", "more rules"}) {
		t.Fatalf("parsed Pi options = %+v", o)
	}
	if !reflect.DeepEqual(o.Messages, []string{"first prompt", "second prompt"}) {
		t.Fatalf("initial messages = %#v", o.Messages)
	}
	o, _, err = Parse([]string{"--", "-dash prompt"})
	if err != nil || !reflect.DeepEqual(o.Messages, []string{"-dash prompt"}) {
		t.Fatalf("-- positional handling: options=%+v err=%v", o, err)
	}
	o, _, err = Parse([]string{"--list-models", "claude-sonnet"})
	if err != nil || !o.ListModels || o.ListModelsFilter != "claude-sonnet" || len(o.Messages) != 0 {
		t.Fatalf("optional list-models argument: options=%+v err=%v", o, err)
	}
	o, _, err = Parse([]string{"--list-models", "--offline"})
	if err != nil || !o.ListModels || !o.Offline || o.ListModelsFilter != "" {
		t.Fatalf("bare list-models argument: options=%+v err=%v", o, err)
	}
}

func TestLocalModelListUsesCachedCatalog(t *testing.T) {
	var output bytes.Buffer
	if err := listAvailableModels(&output, t.TempDir(), "claude-sonnet"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "anthropic") || !strings.Contains(output.String(), "claude-sonnet") || !strings.Contains(output.String(), "context") {
		t.Fatalf("model list output = %s", output.String())
	}
	output.Reset()
	if err := listAvailableModels(&output, t.TempDir(), "not-a-model"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), `No models matching "not-a-model"`) {
		t.Fatalf("empty search output = %s", output.String())
	}
}

func TestCLISystemPromptOverrideAndFileInput(t *testing.T) {
	instructions := []config.InstructionFile{{Path: "system.md", Kind: "system", Content: "configured base"}, {Path: "AGENTS.md", Kind: "agents", Content: "project context"}}
	prompt := config.AssemblePromptWithOverrides("/workspace", instructions, []string{"skill info"}, []string{"read"}, "CLI base", true, []string{"CLI addendum"})
	if !strings.HasPrefix(prompt, "CLI base") {
		t.Errorf("CLI base is not the preamble: %s", prompt)
	}
	last := -1
	for _, want := range []string{"CLI base", "<addendum>", "CLI addendum", "<project_context>", "project context", "<skills>", "skill info", "<cwd>", "/workspace"} {
		at := strings.Index(prompt, want)
		if at < 0 {
			t.Fatalf("prompt missing %q: %s", want, prompt)
		}
		if at < last {
			t.Fatalf("%q out of order: %s", want, prompt)
		}
		last = at
	}
	for _, absent := range []string{"configured base", "<tools>", "<rules>", "<docs>", "Available tools:", "Workspace:"} {
		if strings.Contains(prompt, absent) {
			t.Fatalf("override prompt contains %q: %s", absent, prompt)
		}
	}

	defaults := config.AssemblePromptWithOverrides("/workspace", nil, nil, []string{"read"}, "", false, nil)
	if !strings.Contains(defaults, "<tools>\n- read: Read file contents\n</tools>") {
		t.Fatalf("default prompt tools section = %s", defaults)
	}
	if !strings.Contains(defaults, "Use read to examine files instead of cat or sed.") {
		t.Fatalf("default prompt rules section = %s", defaults)
	}
	path := filepath.Join(t.TempDir(), "prompt.txt")
	if err := os.WriteFile(path, []byte("\uFEFFloaded from file"), 0600); err != nil {
		t.Fatal(err)
	}
	if got := resolveCLIPrompt(path); got != "loaded from file" {
		t.Fatalf("resolved prompt file = %q", got)
	}
	if got := resolveCLIPrompt("literal prompt"); got != "literal prompt" {
		t.Fatalf("resolved literal prompt = %q", got)
	}
}

func TestOpenSessionResolvesPiSessionIDPrefix(t *testing.T) {
	cwd, agentDir, sessionDir := t.TempDir(), t.TempDir(), t.TempDir()
	store, err := session.New(session.OpenOptions{Cwd: cwd, AgentDir: agentDir, SessionDir: sessionDir, Persist: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.AppendMessage(agentMessageUser("existing session")); err != nil {
		t.Fatal(err)
	}
	id := store.Header().ID
	opened, err := openSession(Options{SessionPath: id[:8]}, cwd, agentDir, config.Settings{}, config.StartupOverrides{SessionDir: sessionDir}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if opened.Header().ID != id {
		t.Fatalf("opened session ID = %s, want %s", opened.Header().ID, id)
	}
}

func TestFooterSnapshotUsesPiTokenFormatting(t *testing.T) {
	r := testRuntime(t)
	_, err := r.store.AppendMessage(session.AgentMessage{Role: "assistant", Assistant: &session.AssistantMessage{
		Role: "assistant", API: "anthropic-messages", Provider: "anthropic", Model: "m",
		Usage: session.Usage{Input: 1250},
	}})
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	input, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	defer input.Close()
	console := terminal.NewConsole(input, &output)
	defer console.Close()
	r.refreshPresentation(console)
	if r.footerSnapshot.Usage != "↑1.3k" {
		t.Fatalf("stored usage displayed as %q; want Pi format ↑1.3k", r.footerSnapshot.Usage)
	}
}

func TestFooterCacheStatsMatchPi(t *testing.T) {
	r := testRuntime(t)
	_, err := r.store.AppendMessage(session.AgentMessage{Role: "assistant", Assistant: &session.AssistantMessage{
		Role: "assistant", API: "anthropic-messages", Provider: "anthropic", Model: "m",
		Usage: session.Usage{Input: 126, Output: 30, CacheRead: 874},
	}})
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	input, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	defer input.Close()
	console := terminal.NewConsole(input, &output)
	defer console.Close()
	r.refreshPresentation(console)
	if r.footerSnapshot.Usage != "↑126 ↓30 R874 CH87.4%" {
		t.Fatalf("footer usage=%q; want Pi format ↑126 ↓30 R874 CH87.4%%", r.footerSnapshot.Usage)
	}
}

func TestFooterCacheStatsOmitZeroCounters(t *testing.T) {
	r := testRuntime(t)
	_, err := r.store.AppendMessage(session.AgentMessage{Role: "assistant", Assistant: &session.AssistantMessage{
		Role: "assistant", API: "anthropic-messages", Provider: "anthropic", Model: "m",
		Usage: session.Usage{Input: 126, Output: 30},
	}})
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	input, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	defer input.Close()
	console := terminal.NewConsole(input, &output)
	defer console.Close()
	r.refreshPresentation(console)
	if r.footerSnapshot.Usage != "↑126 ↓30" {
		t.Fatalf("footer usage=%q; want ↑126 ↓30 with no cache segments", r.footerSnapshot.Usage)
	}
}

func TestFooterPercentFormattingMatchesPi(t *testing.T) {
	for _, tc := range []struct {
		percent float64
		want    string
	}{
		{10.15, "10.2%"}, {10.25, "10.3%"}, {10.35, "10.3%"}, {99.95, "100.0%"},
	} {
		if got := formatFooterFixedOneDecimal(tc.percent, "%"); got != tc.want {
			t.Errorf("footer percent %v=%q; want Pi toFixed output %q", tc.percent, got, tc.want)
		}
	}
}

func TestFooterTokenFormattingMatchesPi(t *testing.T) {
	for _, tc := range []struct {
		tokens float64
		want   string
	}{
		{0, "0"}, {999, "999"}, {999.9, "999.9"},
		{1000, "1.0k"}, {1050, "1.1k"}, {1150, "1.1k"}, {1250, "1.3k"}, {1350, "1.4k"},
		{1450, "1.4k"}, {1550, "1.6k"}, {9999, "10.0k"}, {10000, "10k"}, {999999, "1000k"},
		{1000000, "1.0M"}, {1250000, "1.3M"}, {9999999, "10.0M"}, {10000000, "10M"},
	} {
		if got := formatFooterTokens(tc.tokens); got != tc.want {
			t.Errorf("formatFooterTokens(%v)=%q; want Pi format %q", tc.tokens, got, tc.want)
		}
	}
}

func clearProviderAPIKeys(t *testing.T) {
	t.Helper()
	for _, family := range providerregistry.IDs() {
		if spec, ok := providerregistry.Lookup(family); ok && spec.Environment != "" {
			t.Setenv(spec.Environment, "")
		}
	}
}

func testRuntime(t *testing.T) *runtime {
	t.Helper()
	return testRuntimeWithCwd(t, t.TempDir())
}

func testRuntimeWithCwd(t *testing.T, cwd string) *runtime {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "")
	s, err := session.New(session.OpenOptions{Cwd: cwd, SessionDir: t.TempDir(), Persist: true})
	if err != nil {
		t.Fatal(err)
	}
	r := &runtime{store: s, agentDir: t.TempDir(), settings: config.Settings{DefaultProvider: "anthropic", DefaultModel: "m", DefaultThinking: "off", DefaultTools: []string{"read"}},
		models: &config.ModelsFile{Providers: map[string]config.ProviderMeta{"anthropic": {Models: []config.ModelMeta{{ID: "m", Reasoning: true, MaxTokens: 16384}}}}}, prompt: "workspace instructions", live: newLiveConfig()}
	if err := s.TryLock(); err != nil {
		t.Fatal(err)
	}
	if err := r.restore(); err != nil {
		t.Fatal(err)
	}
	r.publishLive()
	t.Cleanup(func() {
		r.store.Unlock()
		if r.prov != nil {
			r.prov.CloseIdle()
		}
	})
	return r
}

// Queueing is allowed during startup/settlement windows but never for a run
// that is already cancelling, exiting, or storage-blocked.
func TestBusySkillRefusalPreconditions(t *testing.T) {
	r := testRuntime(t)
	loop := &agent.Loop{}
	cancel := func() {}
	r.state = StateIdle
	if r.busySkillRefused(loop, cancel, false) {
		t.Fatal("settlement window rejected a skill queue")
	}
	r.state = StateCancelling
	if !r.busySkillRefused(loop, cancel, false) {
		t.Fatal("cancelling run accepted a queued skill")
	}
	r.state = StateIdle
	if !r.busySkillRefused(loop, cancel, true) {
		t.Fatal("exiting run accepted a queued skill")
	}
	if !r.busySkillRefused(nil, cancel, false) {
		t.Fatal("missing active loop accepted a queued skill")
	}
	if !r.busySkillRefused(loop, nil, false) {
		t.Fatal("missing cancellation accepted a queued skill")
	}
	r.store.FailWritesForTest(fmt.Errorf("blocked for test"))
	if _, err := r.store.AppendMessage(agentMessageUser("blocked")); err == nil {
		t.Fatal("expected the injected write failure")
	}
	if r.store.WriteError() == nil || !r.busySkillRefused(loop, cancel, false) {
		t.Fatal("storage-blocked run accepted a queued skill")
	}
}

func TestFooterSnapshotIncludesGitBranch(t *testing.T) {
	fixture := t.TempDir()
	head := filepath.Join(fixture, ".git", "HEAD")
	if err := os.MkdirAll(filepath.Dir(head), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(head, []byte("ref: refs/heads/main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	r := testRuntimeWithCwd(t, fixture)
	var output bytes.Buffer
	input, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	defer input.Close()
	console := terminal.NewConsole(input, &output)
	defer console.Close()
	r.refreshPresentation(console)
	if r.footerSnapshot.GitBranch != "main" {
		t.Fatalf("footer branch=%q; want main", r.footerSnapshot.GitBranch)
	}
	if err := os.WriteFile(head, []byte("ref: refs/heads/other\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	future := time.Now().Add(time.Second)
	if err := os.Chtimes(head, future, future); err != nil {
		t.Fatal(err)
	}
	r.refreshPresentation(console)
	if r.footerSnapshot.GitBranch != "other" {
		t.Fatalf("footer branch after checkout=%q; want other", r.footerSnapshot.GitBranch)
	}
}

func TestFooterContextUsageDisplay(t *testing.T) {
	r := testRuntime(t)
	r.cfg.Model.ContextWindow = 200000
	r.cfg.AutoCompact = false
	var output bytes.Buffer
	input, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	defer input.Close()
	console := terminal.NewConsole(input, &output)
	defer console.Close()

	fake := &provider.Fake{Responses: []*provider.Response{{
		Text:       "context usage answer",
		Usage:      provider.Usage{Input: 79000, Output: 1000, TotalTokens: 80000},
		StopReason: "end_turn",
	}}}
	if err := agent.New(r.store, r.reg, fake, nil).Submit(context.Background(), "show the context usage", r.cfg); err != nil {
		t.Fatal(err)
	}

	assertCaption := func(mode, label, estimate string) {
		t.Helper()
		if got := r.footerSnapshot.ContextUsageLabel; got != label {
			t.Fatalf("%s label = %q, want %q", mode, got, label)
		}
		if got := r.footerSnapshot.ContextEstimate; got != estimate {
			t.Fatalf("%s estimate = %q, want %q", mode, got, estimate)
		}
	}

	r.draw(console)
	assertCaption("zero-value", "40.0%", "40.0%/200k")
	firstPercent := r.footerSnapshot.ContextPercent
	firstTokens, firstWindow := r.contextTokens, r.contextWindow
	firstLeaf := r.store.LeafID()
	firstAccounting, err := r.store.Accounting()
	if err != nil {
		t.Fatal(err)
	}
	firstCalls := fake.CallCount()
	if firstPercent != 40.0 || firstTokens != 80000 || firstWindow != 200000 {
		t.Fatalf("initial context values: percent=%v tokens=%v window=%v", firstPercent, firstTokens, firstWindow)
	}

	r.settings.ContextUsageDisplay = "tokens"
	r.draw(console)
	assertCaption("tokens", "80k", "80k/200k")
	if !strings.Contains(terminal.Sanitize(output.String()), "80k/200k") {
		t.Fatal("token caption was not drawn")
	}
	key, snapshot := r.footerKey, r.footerSnapshot
	bytesBeforeRedraw := output.Len()
	r.draw(console)
	if r.footerKey != key || r.footerSnapshot != snapshot || output.Len() != bytesBeforeRedraw {
		t.Fatalf("identical token redraw was not cached: key changed=%t bytes=%d", r.footerKey != key, output.Len()-bytesBeforeRedraw)
	}
	accounting, err := r.store.Accounting()
	if err != nil {
		t.Fatal(err)
	}
	if r.footerSnapshot.ContextPercent != firstPercent || r.contextTokens != firstTokens || r.contextWindow != firstWindow ||
		r.store.LeafID() != firstLeaf || !reflect.DeepEqual(accounting, firstAccounting) || fake.CallCount() != firstCalls {
		t.Fatalf("tokens mode changed context telemetry: percent=%v tokens=%v/%v leaf=%q calls=%d accounting=%v",
			r.footerSnapshot.ContextPercent, r.contextTokens, r.contextWindow, r.store.LeafID(), fake.CallCount(), accounting)
	}

	r.settings.ContextUsageDisplay = "percent"
	r.draw(console)
	assertCaption("explicit percent", "40.0%", "40.0%/200k")
	accounting, err = r.store.Accounting()
	if err != nil {
		t.Fatal(err)
	}
	if r.footerSnapshot.ContextPercent != firstPercent || r.contextTokens != firstTokens || r.contextWindow != firstWindow ||
		r.store.LeafID() != firstLeaf || !reflect.DeepEqual(accounting, firstAccounting) || fake.CallCount() != firstCalls {
		t.Fatalf("percent mode changed context telemetry: percent=%v tokens=%v/%v leaf=%q calls=%d accounting=%v",
			r.footerSnapshot.ContextPercent, r.contextTokens, r.contextWindow, r.store.LeafID(), fake.CallCount(), accounting)
	}
}

func TestFooterContextUsageAfterInterruptedTurn(t *testing.T) {
	const interruptedInputTokens = 180000
	r := testRuntime(t)
	r.cfg.Model.ContextWindow = 200000
	r.cfg.AutoCompact = false
	var output bytes.Buffer
	input, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	defer input.Close()
	console := terminal.NewConsole(input, &output)
	defer console.Close()

	fake := &provider.Fake{
		Responses: []*provider.Response{{
			Text:       "completed answer",
			Usage:      provider.Usage{Input: 79000, Output: 1000, TotalTokens: 80000},
			StopReason: "end_turn",
		}},
		Errors: []error{nil, &provider.StreamInterrupt{
			Partial: &provider.Response{Usage: provider.Usage{Input: interruptedInputTokens}},
			Err:     errors.New("synthetic stream interrupted"),
		}},
		TextDeltas: [][]string{nil, {"partial"}},
	}
	loop := agent.New(r.store, r.reg, fake, nil)
	if err := loop.Submit(context.Background(), "first prompt", r.cfg); err != nil {
		t.Fatal(err)
	}
	usage, anchor, ok := r.store.LastContextUsage()
	if !ok || usage != 80000 || anchor == "" {
		t.Fatalf("completed anchor = %d/%q/%t, want 80000 with an id", usage, anchor, ok)
	}
	if err := loop.Submit(context.Background(), "interrupted prompt", r.cfg); err == nil {
		t.Fatal("interrupted submit succeeded")
	}
	usage, anchorAfter, ok := r.store.LastContextUsage()
	if !ok || usage != 80000 || anchorAfter != anchor {
		t.Fatalf("interrupted turn replaced the anchor: %d/%q/%t, want 80000/%q", usage, anchorAfter, ok, anchor)
	}

	r.refreshPresentation(console)
	beforePercent := r.footerSnapshot.ContextPercent
	beforeTokens := r.contextTokens
	beforeWindow := r.contextWindow
	beforeCalls := fake.CallCount()
	// The completed usage plus short trailing messages must remain below the
	// interrupted stream's input count; partial accounting is not an anchor.
	if beforeTokens <= float64(usage) || beforeTokens >= interruptedInputTokens || beforeWindow != 200000 {
		t.Fatalf("footer lost the completed usage anchor: tokens=%v window=%v", beforeTokens, beforeWindow)
	}
	if beforePercent != 40 || r.footerSnapshot.ContextUsageLabel != "40.0%" {
		t.Fatalf("interrupted-turn percent caption = %q (%v), want 40.0%%", r.footerSnapshot.ContextUsageLabel, beforePercent)
	}
	if beforeCalls != 2 {
		t.Fatalf("provider calls before display toggle = %d, want 2", beforeCalls)
	}

	r.settings.ContextUsageDisplay = "tokens"
	r.refreshPresentation(console)
	if r.footerSnapshot.ContextPercent != beforePercent || r.contextTokens != beforeTokens ||
		r.contextWindow != beforeWindow || fake.CallCount() != beforeCalls {
		t.Fatalf("token toggle changed context telemetry: percent=%v tokens=%v/%v calls=%d",
			r.footerSnapshot.ContextPercent, r.contextTokens, r.contextWindow, fake.CallCount())
	}
	if r.footerSnapshot.ContextUsageLabel != "80k" || r.footerSnapshot.ContextEstimate != "80k/200k" {
		t.Fatalf("interrupted-turn token caption = %q, estimate = %q", r.footerSnapshot.ContextUsageLabel, r.footerSnapshot.ContextEstimate)
	}
	r.settings.ContextUsageDisplay = "percent"
	r.refreshPresentation(console)
	if r.footerSnapshot.ContextPercent != beforePercent || r.contextTokens != beforeTokens ||
		r.contextWindow != beforeWindow || fake.CallCount() != beforeCalls {
		t.Fatalf("percent toggle changed context telemetry: percent=%v tokens=%v/%v calls=%d",
			r.footerSnapshot.ContextPercent, r.contextTokens, r.contextWindow, fake.CallCount())
	}
	if r.footerSnapshot.ContextUsageLabel != "40.0%" || r.footerSnapshot.ContextEstimate != "40.0%/200k" {
		t.Fatalf("interrupted-turn percent caption = %q, estimate = %q", r.footerSnapshot.ContextUsageLabel, r.footerSnapshot.ContextEstimate)
	}
}

func TestFooterEstimateDoesNotBuildProjection(t *testing.T) {
	for _, mode := range []string{"percent", "tokens"} {
		t.Run(mode, func(t *testing.T) {
			r := testRuntime(t)
			r.settings.ContextUsageDisplay = mode
			r.cfg.Model.ContextWindow = 100000
			var output bytes.Buffer
			input, err := os.Open(os.DevNull)
			if err != nil {
				t.Fatal(err)
			}
			defer input.Close()
			console := terminal.NewConsole(input, &output)
			defer console.Close()

			r.refreshPresentation(console)
			if r.footerSnapshot.ContextEstimate == "" {
				t.Fatal("empty session lost the baseline estimate")
			}
			if r.footerSnapshot.ContextUsageLabel == "" || r.footerSnapshot.ContextWindowLabel != "100k" {
				t.Fatalf("footer context bar labels=%q/%q; want a formatted caption and 100k", r.footerSnapshot.ContextUsageLabel, r.footerSnapshot.ContextWindowLabel)
			}
			if mode == "percent" {
				if got := formatFooterFixedOneDecimal(r.footerSnapshot.ContextPercent, "%"); got != r.footerSnapshot.ContextUsageLabel {
					t.Fatalf("footer bar percent=%q but label=%q", got, r.footerSnapshot.ContextUsageLabel)
				}
			} else if got, want := r.footerSnapshot.ContextUsageLabel, formatFooterTokens(r.contextTokens); got != want {
				t.Fatalf("footer token label=%q, want %q", got, want)
			}
			if got, want := r.footerSnapshot.ContextEstimate, r.footerSnapshot.ContextUsageLabel+"/100k"; got != want {
				t.Fatalf("footer estimate=%q; want %q", got, want)
			}
			if r.store.ProjectionIfCached() != nil {
				t.Fatal("footer built a projection for an empty session")
			}

			if _, err := r.store.AppendMessage(agentMessageUser("one prompt")); err != nil {
				t.Fatal(err)
			}
			r.refreshPresentation(console)
			if r.footerSnapshot.ContextEstimate != "" {
				t.Fatalf("existing session without usage fabricated an estimate: %q", r.footerSnapshot.ContextEstimate)
			}
			if r.footerSnapshot.ContextUsageLabel != "" || r.footerSnapshot.ContextWindowLabel != "" {
				t.Fatalf("footer fabricated context bar labels without an estimate: %q/%q", r.footerSnapshot.ContextUsageLabel, r.footerSnapshot.ContextWindowLabel)
			}
			if r.store.ProjectionIfCached() != nil {
				t.Fatal("footer built a projection for an existing session")
			}

			if _, err := r.store.Projection(); err != nil {
				t.Fatal(err)
			}
			r.refreshPresentation(console)
			if r.footerSnapshot.ContextEstimate == "" {
				t.Fatal("footer ignored a projection cached after the last redraw")
			}
			if r.footerSnapshot.ContextUsageLabel == "" || r.footerSnapshot.ContextWindowLabel != "100k" {
				t.Fatalf("footer context bar labels missing after projection: %q/%q", r.footerSnapshot.ContextUsageLabel, r.footerSnapshot.ContextWindowLabel)
			}
		})
	}

	for _, mode := range []string{"percent", "tokens"} {
		t.Run("unknown-window-"+mode, func(t *testing.T) {
			r := testRuntime(t)
			r.settings.ContextUsageDisplay = mode
			r.cfg.Model.ContextWindow = 0
			var output bytes.Buffer
			input, err := os.Open(os.DevNull)
			if err != nil {
				t.Fatal(err)
			}
			defer input.Close()
			console := terminal.NewConsole(input, &output)
			defer console.Close()

			r.refreshPresentation(console)
			if r.footerSnapshot.ContextUsageLabel != "" || r.footerSnapshot.ContextWindowLabel != "" || r.footerSnapshot.ContextEstimate != "" || r.footerSnapshot.ContextPercent != 0 {
				t.Fatalf("unknown window produced context labels: %+v", r.footerSnapshot)
			}
			if r.contextTokens != 0 || r.contextWindow != 0 {
				t.Fatalf("unknown window produced numerical context: tokens=%v window=%v", r.contextTokens, r.contextWindow)
			}
			if r.store.ProjectionIfCached() != nil {
				t.Fatal("unknown window built a projection")
			}
		})
	}
}

func TestFooterSnapshotIsCachedAcrossRedraws(t *testing.T) {
	r := testRuntime(t)
	var output bytes.Buffer
	input, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	defer input.Close()
	console := terminal.NewConsole(input, &output)
	defer console.Close()
	r.draw(console)
	first, key := r.footerSnapshot, r.footerKey
	bytesAfterFirst := output.Len()
	r.draw(console)
	if r.footerKey != key || r.footerSnapshot != first {
		t.Fatal("unchanged session/config snapshot was recomputed")
	}
	if output.Len() != bytesAfterFirst {
		t.Fatalf("idle redraw emitted %d extra bytes", output.Len()-bytesAfterFirst)
	}
	if strings.Contains(first.WorkingDirectory, "SYNTHETIC") || strings.Contains(first.Usage, "$") {
		t.Fatalf("footer fabricated or leaked data: %+v", first)
	}
	if first.AutoCompact != r.cfg.AutoCompact {
		t.Fatalf("footer auto-compaction=%v, configuration=%v", first.AutoCompact, r.cfg.AutoCompact)
	}
	r.cfg.AutoCompact = !r.cfg.AutoCompact
	r.refreshPresentation(console)
	if r.footerKey == key || r.footerSnapshot.AutoCompact != r.cfg.AutoCompact {
		t.Fatalf("footer cache did not track auto-compaction configuration: key=%q snapshot=%v config=%v", r.footerKey, r.footerSnapshot.AutoCompact, r.cfg.AutoCompact)
	}
}

func TestCommandsReplaceSessionAndValidateBeforeMutation(t *testing.T) {
	r := testRuntime(t)
	if r.authErr == nil {
		t.Fatal("missing credentials must remain an error, not a fake provider")
	}
	if r.cfg.System != r.prompt || len(r.cfg.Tools[0].Schema["required"].([]string)) == 0 {
		t.Fatal("prompt/tool schemas not wired")
	}
	old := r.store
	if _, err := r.command(&terminal.Command{Name: "new"}, io.Discard); err != nil {
		t.Fatal(err)
	}
	if r.store == old || r.store.Header().ID == old.Header().ID {
		t.Fatal("new did not replace active session")
	}
	before := len(r.store.Entries())
	if _, err := r.command(&terminal.Command{Name: "model", Arg: "openai bad"}, io.Discard); err == nil {
		t.Fatal("unsupported provider accepted")
	}
	if r.ov.Provider != "anthropic" || len(r.store.Entries()) != before {
		t.Fatal("failed selection changed state")
	}
	if _, err := r.command(&terminal.Command{Name: "thinking", Arg: "high"}, io.Discard); err != nil {
		t.Fatal(err)
	}
	if r.ov.Thinking != "high" {
		t.Fatal("thinking did not change")
	}
	var b bytes.Buffer
	if _, err := r.command(&terminal.Command{Name: "session"}, &b); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(b.String(), r.store.Header().ID) {
		t.Fatal(b.String())
	}
}

// Run the real entrypoint in a PTY, with an isolated HOME and a local HTTP
// provider. This catches wiring bugs that unit tests of unused helpers miss.
func TestAppProcess(t *testing.T) {
	if os.Getenv("PSI_APP_TEST_PROCESS") != "1" || flag.Lookup("test.run").Value.String() != "^TestAppProcess$" {
		return
	}
	args := []string{"--deny-project", "--tools", "read"}
	if raw := os.Getenv("PSI_APP_TEST_ARGS"); raw != "" {
		if err := json.Unmarshal([]byte(raw), &args); err != nil {
			fmt.Fprintln(os.Stderr, "invalid PSI_APP_TEST_ARGS:", err)
			os.Exit(1)
		}
	}
	if testTools := os.Getenv("PSI_APP_TEST_TOOLS"); testTools != "" && len(args) > 2 {
		args[2] = testTools
	}
	if err := Run(args, "test"); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	os.Exit(0)
}

// Use an allowlist instead of os.Environ: test subprocesses must not inherit
// developer credentials, Pi directories, proxies, or live-test opt-in flags.
func appTestEnv(home, agentDir string) []string {
	return []string{"PATH=/usr/local/bin:/usr/bin:/bin", "HOME=" + home, "PI_CODING_AGENT_DIR=" + agentDir,
		"PI_CODING_AGENT_SESSION_DIR=", "TERM=xterm", "LANG=C.UTF-8", "PSI_APP_TEST_PROCESS=1"}
}

func TestAppSubprocessEnvironmentIsIsolated(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "synthetic-parent-secret")
	t.Setenv("ANTHROPIC_API_KEY", "synthetic-parent-secret")
	t.Setenv("PI_CODING_AGENT_DIR", "/synthetic-parent-pi-directory")
	t.Setenv("PSI_LIVE_PROVIDER", "1")
	t.Setenv("HTTPS_PROXY", "http://synthetic-parent-proxy.invalid")
	env := strings.Join(appTestEnv("/synthetic-home", "/synthetic-agent"), "\n")
	for _, forbidden := range []string{"synthetic-parent", "OPENAI_API_KEY", "ANTHROPIC_API_KEY", "PSI_LIVE_PROVIDER", "HTTPS_PROXY"} {
		if strings.Contains(env, forbidden) {
			t.Fatal("test subprocess inherited a forbidden environment value")
		}
	}
}

func writeAppFile(t *testing.T, path, text string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestActualAppPTYWorkflow(t *testing.T) {
	if goruntime.GOOS == "windows" {
		t.Skip("app PTY workflow needs a Unix PTY")
	}
	home, ws := t.TempDir(), t.TempDir()
	agentDir := filepath.Join(home, ".pi", "agent")
	requests := make(chan map[string]any, 16)
	var providerCalls atomic.Int32
	slowRelease := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Path != "/v1/messages" || req.Header.Get("x-api-key") != "file-test-key" {
			t.Errorf("endpoint/auth not used: %s", req.URL.Path)
		}
		providerCalls.Add(1)
		var body map[string]any
		if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
			t.Error(err)
			return
		}
		requests <- body
		messages, _ := body["messages"].([]any)
		last, _ := messages[len(messages)-1].(map[string]any)
		encoded, _ := json.Marshal(last)
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "event: message_start\ndata: {\"message\":{\"id\":\"r1\",\"model\":\"local-model\",\"usage\":{\"input_tokens\":5}}}\n\n")
		if strings.Contains(string(encoded), "cancel-this") {
			w.(http.Flusher).Flush()
			<-req.Context().Done()
			return
		}
		if strings.Contains(string(encoded), "ten-thousand-deltas") {
			fmt.Fprint(w, "event: content_block_start\ndata: {\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n")
			for i := 0; i < 10_000; i++ {
				fmt.Fprint(w, "event: content_block_delta\ndata: {\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"a\"}}\n\n")
				if (i+1)%333 == 0 {
					w.(http.Flusher).Flush()
					time.Sleep(33 * time.Millisecond)
				}
			}
			fmt.Fprint(w, "event: content_block_delta\ndata: {\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"BURST_DONE\"}}\n\nevent: content_block_stop\ndata: {\"index\":0}\n\nevent: message_delta\ndata: {\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":10001}}\n\nevent: message_stop\ndata: {}\n\n")
			return
		}
		if strings.Contains(string(encoded), "slow-stream") {
			fmt.Fprint(w, "event: content_block_start\ndata: {\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n")
			fmt.Fprint(w, "event: content_block_delta\ndata: {\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"**SLOW_PART\"}}\n\n")
			w.(http.Flusher).Flush()
			select {
			case <-slowRelease:
			case <-req.Context().Done():
				return
			}
			fmt.Fprint(w, "event: content_block_delta\ndata: {\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"IAL**\\n\\nSLOW_DONE\"}}\n\n")
			fmt.Fprint(w, "event: content_block_stop\ndata: {\"index\":0}\n\nevent: message_delta\ndata: {\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":2}}\n\nevent: message_stop\ndata: {}\n\n")
			return
		}
		fmt.Fprint(w, "event: content_block_start\ndata: {\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n")
		fmt.Fprint(w, "event: content_block_delta\ndata: {\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"APP_OK\"}}\n\n")
		fmt.Fprint(w, "event: content_block_stop\ndata: {\"index\":0}\n\nevent: message_delta\ndata: {\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":2}}\n\nevent: message_stop\ndata: {}\n\n")
	}))
	defer server.Close()
	writeAppFile(t, filepath.Join(agentDir, "settings.json"), `{"defaultProvider":"anthropic","defaultModel":"local-model","compaction":{"enabled":false}}`)
	writeAppFile(t, filepath.Join(agentDir, "models.json"), `{"providers":{"anthropic":{"baseUrl":"`+server.URL+`/v1","models":[{"id":"local-model","maxTokens":4096,"contextWindow":8192}]}}}`)
	writeAppFile(t, filepath.Join(agentDir, "auth.json"), `{"anthropic":{"type":"api_key","key":"file-test-key"}}`)
	writeAppFile(t, filepath.Join(agentDir, "AGENTS.md"), "FOLLOW_WORKSPACE_INSTRUCTIONS")
	writeAppFile(t, filepath.Join(agentDir, "skills", "doer", "SKILL.md"), "---\nname: doer\ndescription: >\n  Do important work.\n---\nSKILL_BODY\n")
	cmd := exec.Command(os.Args[0], "-test.run=^TestAppProcess$")
	cmd.Dir = ws
	cmd.Env = appTestEnv(home, agentDir)
	master, err := pty.Start(cmd)
	if err != nil {
		t.Fatal(err)
	}
	defer master.Close()
	defer func() {
		if cmd.ProcessState == nil {
			cmd.Process.Kill()
			cmd.Wait()
		}
	}()
	chunks := make(chan string, 256)
	go func() {
		defer close(chunks)
		buf := make([]byte, 4096)
		for {
			n, err := master.Read(buf)
			if n > 0 {
				chunks <- string(buf[:n])
			}
			if err != nil {
				return
			}
		}
	}()
	var pending string
	screen := testutil.NewVTModel(80, 24)
	wait := func(want string) {
		t.Helper()
		waitForAppOutput(t, chunks, screen, &pending, want)
	}
	write := func(s string) {
		t.Helper()
		if _, err := master.WriteString(s); err != nil {
			t.Fatal(err)
		}
	}
	// waitScreen consumes VT output until every expected substring is shown.
	// The timer is a deadlock guard, not a settle delay.
	screenView := func() string {
		rows := append(append([]string{}, screen.Scrollback...), strings.Split(screen.Text(), "\n")...)
		return strings.Join(rows, "\n")
	}
	waitScreen := func(want ...string) string {
		t.Helper()
		timer := time.NewTimer(8 * time.Second)
		defer timer.Stop()
		for {
			text := screenView()
			complete := true
			for _, part := range want {
				if !strings.Contains(text, part) {
					complete = false
					break
				}
			}
			if complete {
				return text
			}
			select {
			case chunk, ok := <-chunks:
				if !ok {
					t.Fatalf("process exited waiting for screen %q: %q", want, text)
				}
				pending += chunk
				screen.Feed([]byte(chunk))
			case <-timer.C:
				t.Fatalf("timeout waiting for screen %q: %q", want, text)
			}
		}
	}
	lastUserContent := func(body map[string]any) string {
		t.Helper()
		messages, _ := body["messages"].([]any)
		if len(messages) == 0 {
			t.Fatal("provider request carried no messages")
		}
		last, _ := messages[len(messages)-1].(map[string]any)
		if text, ok := last["content"].(string); ok {
			return text
		}
		blocks, _ := last["content"].([]any)
		var text strings.Builder
		for _, raw := range blocks {
			block, _ := raw.(map[string]any)
			if block["type"] == "text" {
				value, _ := block["text"].(string)
				text.WriteString(value)
			}
		}
		return text.String()
	}
	waitEditor := func() { waitForEditorScreen(t, chunks, screen, &pending) }
	wait("/ commands")
	waitEditor()
	write("hello\r")
	wait("APP_OK")
	waitEditor()
	if footer := screen.Text(); !strings.Contains(footer, "%/8.2k") || !strings.Contains(footer, "[") || !strings.Contains(footer, "░") {
		t.Fatalf("actual app footer lacks the inline context bar: %q", footer)
	}
	body := <-requests
	initialSessions, err := session.Discover(session.DefaultSessionDir(ws, agentDir))
	if err != nil || len(initialSessions) != 1 {
		t.Fatalf("first session not persisted: %v", err)
	}
	contender, err := session.OpenFile(initialSessions[0].Path, ws, true)
	if err != nil {
		t.Fatal(err)
	}
	if err := contender.TryLock(); err == nil {
		contender.Unlock()
		t.Fatal("running app did not lock its session")
	}
	if !strings.Contains(body["system"].(string), "FOLLOW_WORKSPACE_INSTRUCTIONS") || !strings.Contains(body["system"].(string), "doer") {
		t.Fatal("instructions or skills not sent")
	}
	defs := body["tools"].([]any)
	if len(defs) != 2 || defs[0].(map[string]any)["name"] != "read" || defs[1].(map[string]any)["name"] != "read_session" {
		t.Fatalf("CLI tools selection or internal history tool mismatch: %v", defs)
	}
	write("/skill:doer run-it\r")
	wait("APP_OK")
	waitEditor()
	skillRequest := <-requests
	skillPath := filepath.Join(agentDir, "skills", "doer", "SKILL.md")
	wantSkill := "<skill name=\"doer\" location=\"" + skillPath + "\">\n" +
		"References are relative to " + filepath.Dir(skillPath) + ".\n\nSKILL_BODY\n</skill>\n\nrun-it"
	if got := lastUserContent(skillRequest); got != wantSkill {
		t.Fatalf("skill provider content = %q, want %q", got, wantSkill)
	}
	view := waitScreen("[skill] doer (ctrl+o to expand)", "› run-it")
	if strings.Contains(view, "SKILL_BODY") {
		t.Fatalf("collapsed skill was visible by default: %q", view)
	}
	// The toggle keeps an in-progress draft and cursor untouched.
	write("draft-keep")
	waitScreen("draft-keep")
	settingsPath := filepath.Join(agentDir, "settings.json")
	settingsBefore, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatal(err)
	}
	callsBeforeToggle := providerCalls.Load()
	write("\x0f")
	view = waitScreen("Skill and web cards: expanded", "SKILL_BODY", "References are relative to")
	if !strings.Contains(view, "draft-keep") {
		t.Fatalf("expanding skill blocks lost the draft: %q", view)
	}
	write("\x0f")
	view = waitScreen("Skill and web cards: collapsed", "[skill] doer (ctrl+o to expand)")
	if strings.Contains(view, "SKILL_BODY") {
		t.Fatalf("collapsing skill blocks left the body visible: %q", view)
	}
	if !strings.Contains(view, "draft-keep") {
		t.Fatalf("collapsing skill blocks lost the draft: %q", view)
	}
	if providerCalls.Load() != callsBeforeToggle {
		t.Fatalf("Ctrl+O invoked the provider: %d calls, want %d", providerCalls.Load(), callsBeforeToggle)
	}
	settingsAfter, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(settingsBefore, settingsAfter) {
		t.Fatalf("Ctrl+O wrote settings:\nbefore %q\nafter  %q", settingsBefore, settingsAfter)
	}
	write("\x15")
	// Idle refusals restore the captured draft instead of submitting text.
	write("/skill:nope nope-draft\r")
	wait("skill \"nope\" not found")
	waitScreen("/skill:nope nope-draft")
	write("\x15")
	if err := os.Remove(skillPath); err != nil {
		t.Fatal(err)
	}
	write("/skill:doer gone\r")
	wait("skill \"doer\" unreadable")
	waitScreen("/skill:doer gone")
	write("\x15")
	writeAppFile(t, skillPath, "---\nname: doer\ndescription: >\n  Do important work.\n---\nSKILL_BODY\n")
	write("/new\r")
	wait("New session started")
	write("fresh-session\r")
	wait("APP_OK")
	waitEditor()
	fresh := <-requests
	encoded, _ := json.Marshal(fresh["messages"])
	if bytes.Contains(encoded, []byte("hello")) || bytes.Contains(encoded, []byte("draft-keep")) {
		t.Fatal("new command retained old history or the cleared skill draft")
	}
	if err := contender.TryLock(); err != nil {
		t.Fatalf("new leaked the old session lock: %v", err)
	}
	contender.Unlock()
	write("/resume\r")
	wait("Sessions ·")
	waitEditor()
	write("\x1b[B\r")
	wait("resumed ")
	waitEditor()
	write("resumed-session\r")
	wait("APP_OK")
	waitEditor()
	resumed := <-requests
	encoded, _ = json.Marshal(resumed["messages"])
	if !bytes.Contains(encoded, []byte("SKILL_BODY")) || bytes.Contains(encoded, []byte("fresh-session")) {
		t.Fatal("resume did not switch back to the selected history")
	}
	write("slow-stream\r")
	select {
	case <-requests:
	case <-time.After(5 * time.Second):
		t.Fatal("slow stream request not received")
	}
	wait("SLOW_PART")
	if screen.CursorVisible || !strings.Contains(screen.Text(), "Working") || !strings.Contains(screen.Text(), "local-model") || !strings.Contains(screen.Text(), "─") {
		t.Fatalf("active stream lost its indicator/footer/editor: cursor=%v screen=%q", screen.CursorVisible, screen.Text())
	}
	// Toggles in the busy stream keep the assistant output, the draft, and the
	// hidden busy cursor; Ctrl+T stays independent of the skill flag.
	write("draft-during-stream")
	waitScreen("draft-during-stream")
	write("\x0f")
	// Confirmations are output events: a later spinner frame can replace them
	// within one PTY read. Check retained screen state separately.
	waitForAppOutput(t, chunks, screen, &pending, "Skill and web cards: expanded")
	view = screenView()
	if !strings.Contains(view, "SLOW_PART") || !strings.Contains(view, "draft-during-stream") || screen.CursorVisible {
		t.Fatalf("Ctrl+O during a run disturbed output/draft/cursor: cursor=%v %q", screen.CursorVisible, view)
	}
	write("\x14")
	waitForAppOutput(t, chunks, screen, &pending, "Thinking blocks: hidden")
	view = screenView()
	if !strings.Contains(view, "SLOW_PART") || !strings.Contains(view, "draft-during-stream") || screen.CursorVisible {
		t.Fatalf("Ctrl+T during a run disturbed output/draft/cursor: cursor=%v %q", screen.CursorVisible, view)
	}
	assertThinkingSetting := func(want bool) {
		t.Helper()
		data, err := os.ReadFile(settingsPath)
		if err != nil {
			t.Fatal(err)
		}
		var stored struct {
			HideThinkingBlock *bool `json:"hideThinkingBlock"`
		}
		if err := json.Unmarshal(data, &stored); err != nil {
			t.Fatal(err)
		}
		if stored.HideThinkingBlock == nil || *stored.HideThinkingBlock != want {
			t.Fatalf("busy Ctrl+T did not persist hideThinkingBlock=%v: %s", want, data)
		}
	}
	assertThinkingSetting(true)
	write("\x14")
	waitForAppOutput(t, chunks, screen, &pending, "Thinking blocks: visible")
	assertThinkingSetting(false)
	write("\x0f")
	waitForAppOutput(t, chunks, screen, &pending, "Skill and web cards: collapsed")
	if !strings.Contains(screenView(), "draft-during-stream") {
		t.Fatalf("busy skill toggles lost the draft: %q", screen.Text())
	}
	write("\x15")
	waitScreen("local-model")
	close(slowRelease)
	wait("SLOW_DONE")
	waitEditor()
	if strings.Contains(screen.Text(), "**") || !strings.Contains(screen.Text(), "SLOW_PARTIAL") {
		t.Fatalf("live Markdown delimiters were not reconciled after the split delta: %q", screen.Text())
	}
	burstStart := time.Now()
	write("ten-thousand-deltas\r")
	select {
	case <-requests:
	case <-time.After(5 * time.Second):
		t.Fatal("10,000-delta request not received")
	}
	wait("BURST_DONE")
	waitEditor()
	burstElapsed := time.Since(burstStart)
	t.Logf("actual-app 10k-delta stream over controlled ~1s: elapsed=%s", burstElapsed)
	if burstElapsed < 750*time.Millisecond {
		t.Fatalf("synthetic stream did not span a controlled second: %s", burstElapsed)
	}
	if !strings.Contains(screen.Text(), "local-model") {
		t.Fatalf("footer disappeared after 10k deltas: %q", screen.Text())
	}
	burstSession, err := os.ReadFile(initialSessions[0].Path)
	if err != nil {
		t.Fatal(err)
	}
	burstText := append(bytes.Repeat([]byte("a"), 10_000), []byte("BURST_DONE")...)
	if !bytes.Contains(burstSession, burstText) {
		t.Fatal("10k deltas were not saved in order")
	}
	write("cancel-this\r")
	select {
	case <-requests:
	case <-time.After(5 * time.Second):
		t.Fatalf("cancel request not received; cursorVisible=%v screen=%q pending=%q", screen.CursorVisible, screen.Text(), pending)
	}
	write("\x1b") // lone Escape is decoded after the bounded prefix timeout
	wait("Request cancelled.")
	waitEditor()
	write("/quit\r")
	finished := make(chan error, 1)
	go func() { finished <- cmd.Wait() }()
	select {
	case err := <-finished:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("quit did not exit")
	}
	infos, err := session.Discover(session.DefaultSessionDir(ws, agentDir))
	if err != nil || len(infos) != 2 {
		t.Fatalf("session lifecycle: %d %v", len(infos), err)
	}
	for _, info := range infos {
		s, err := session.OpenFile(info.Path, ws, true)
		if err != nil {
			t.Fatal(err)
		}
		if info.Path == initialSessions[0].Path {
			provenance := map[string]struct{ invocation, source string }{}
			for _, entry := range s.Entries() {
				if entry.Kind != "custom" || entry.Custom == nil || entry.Custom.CustomType != "psi.input" {
					continue
				}
				var p struct {
					TargetID   string `json:"targetId"`
					Invocation string `json:"invocation"`
					Source     string `json:"source"`
				}
				if json.Unmarshal(entry.Custom.Data, &p) == nil && p.TargetID != "" {
					provenance[p.TargetID] = struct{ invocation, source string }{p.Invocation, p.Source}
				}
			}
			skillUsers := 0
			for _, entry := range s.Entries() {
				if entry.Kind != "message" || entry.Message == nil || entry.Message.Message.User == nil {
					continue
				}
				content, _ := entry.Message.Message.User.Content.(string)
				if !strings.HasPrefix(content, "<skill name=\"doer\"") {
					continue
				}
				skillUsers++
				if content != wantSkill {
					t.Fatalf("stored skill content = %q, want %q", content, wantSkill)
				}
				p, ok := provenance[entry.ID()]
				if !ok || p.invocation != "/skill:doer run-it" || p.source != "skill" {
					t.Fatalf("stored skill provenance = %+v, ok=%v", p, ok)
				}
			}
			if skillUsers != 1 {
				t.Fatalf("stored skill user messages = %d, want 1", skillUsers)
			}
		}
		if err := s.TryLock(); err != nil {
			t.Fatalf("lock leaked on exit: %v", err)
		}
		s.Unlock()
	}
}

// semanticEditorReady synchronizes on a visible cursor between editor borders,
// not an implementation-specific prompt prefix or cursor escape.
func semanticEditorReady(screen *testutil.VTModel) bool {
	if screen == nil || !screen.CursorVisible || screen.CursorRow <= 0 || screen.CursorRow >= screen.Height-1 {
		return false
	}
	rows := strings.Split(screen.Text(), "\n")
	above, below := false, false
	for i, row := range rows {
		if !strings.Contains(row, "─") {
			continue
		}
		if i < screen.CursorRow {
			above = true
		}
		if i > screen.CursorRow {
			below = true
		}
	}
	return above && below
}

func TestWaitForAppOutputWithCoalescedRedraw(t *testing.T) {
	const status = "Thinking blocks: visible"
	var output bytes.Buffer
	writer := terminal.NewWriter(&output)
	for _, activity := range []string{status, "Working"} {
		if err := writer.Emit(terminal.Frame{
			Tail:       []string{"**SLOW_PART", activity, "draft-during-stream"},
			CursorRow:  2,
			CursorCol:  len("draft-during-stream"),
			HideCursor: true,
			Width:      80,
			Height:     24,
		}); err != nil {
			t.Fatal(err)
		}
	}
	raw := output.String()
	split := strings.Index(raw, status) + len("Thinking blocks:")
	for _, tc := range []struct {
		name   string
		chunks []string
	}{
		{"coalesced", []string{raw}},
		{"split confirmation", []string{raw[:split], raw[split:]}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			chunks := make(chan string, len(tc.chunks))
			for _, chunk := range tc.chunks {
				chunks <- chunk
			}
			close(chunks)
			screen := testutil.NewVTModel(80, 24)
			var pending string
			t.Logf("raw toggle/spinner output: %q", raw)
			waitForAppOutput(t, chunks, screen, &pending, status)
			view := screen.Text()
			if strings.Contains(view, status) || !strings.Contains(view, "Working") || !strings.Contains(view, "**SLOW_PART") || !strings.Contains(view, "draft-during-stream") || screen.CursorVisible {
				t.Fatalf("coalesced redraw did not retain busy screen state: cursor=%v screen=%q", screen.CursorVisible, view)
			}
			if !strings.Contains(pending, "Working") || strings.Contains(pending, status) {
				t.Fatalf("output acknowledgement lost or replayed bytes: %q", pending)
			}
		})
	}
}

func waitForAppOutput(t *testing.T, chunks <-chan string, screen *testutil.VTModel, pending *string, want string) {
	t.Helper()
	timer := time.NewTimer(8 * time.Second)
	defer timer.Stop()
	for {
		if i := strings.Index(*pending, want); i >= 0 {
			*pending = (*pending)[i+len(want):]
			return
		}
		select {
		case chunk, ok := <-chunks:
			if !ok {
				t.Fatalf("process exited waiting for %q: %q", want, *pending)
			}
			*pending += chunk
			screen.Feed([]byte(chunk))
		case <-timer.C:
			t.Fatalf("timeout waiting for %q: %q", want, *pending)
		}
	}
}

func waitForEditorScreen(t *testing.T, chunks <-chan string, screen *testutil.VTModel, pending *string) {
	t.Helper()
	deadline := time.NewTimer(8 * time.Second)
	defer deadline.Stop()
	quiet := time.NewTimer(40 * time.Millisecond)
	defer quiet.Stop()
	for {
		ready := semanticEditorReady(screen)
		if ready {
			select {
			case chunk, ok := <-chunks:
				if !ok {
					t.Fatalf("process exited before editor became ready: %q", *pending)
				}
				*pending += chunk
				screen.Feed([]byte(chunk))
				if !quiet.Stop() {
					select {
					case <-quiet.C:
					default:
					}
				}
				quiet.Reset(40 * time.Millisecond)
			case <-quiet.C:
				*pending = ""
				return
			case <-deadline.C:
				t.Fatalf("timeout waiting for bordered editor: %q", *pending)
			}
		} else {
			select {
			case chunk, ok := <-chunks:
				if !ok {
					t.Fatalf("process exited before editor became ready: %q", *pending)
				}
				*pending += chunk
				screen.Feed([]byte(chunk))
			case <-deadline.C:
				t.Fatalf("timeout waiting for bordered editor: %q", *pending)
			}
		}
	}
}

// Pi's footer names the thinking level only for a model that supports
// reasoning, and it spells out the disabled state.
func TestFooterSnapshotShowsLatestSessionNameAndTracksLeaf(t *testing.T) {
	path := filepath.Join(t.TempDir(), "named.jsonl")
	lines := []string{
		`{"type":"session","version":3,"id":"named","timestamp":"2026-01-01T00:00:00.000Z","cwd":"/work"}`,
		`{"type":"session_info","id":"root","parentId":null,"timestamp":"2026-01-01T00:00:01.000Z","name":"root name"}`,
		`{"type":"session_info","id":"branch-a","parentId":"root","timestamp":"2026-01-01T00:00:02.000Z","name":"branch A"}`,
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := session.OpenFile(path, "/work", false)
	if err != nil {
		t.Fatal(err)
	}
	r := &runtime{store: store, models: &config.ModelsFile{}, ov: config.StartupOverrides{Provider: "anthropic", Model: "m"}}
	console := &terminal.Console{}
	r.refreshPresentation(console)
	view := terminal.ComposeEditorView(80, 10, nil, 0, r.footerSnapshot, "")
	if !strings.Contains(strings.Join(view.Rows, "\n"), "branch A") {
		t.Fatalf("footer omitted the active session name: %q", view.Rows)
	}
	oldKey := r.footerKey
	if err := store.Branch("root"); err != nil {
		t.Fatal(err)
	}
	r.refreshPresentation(console)
	if r.footerKey == oldKey || r.footerSnapshot.SessionName != "branch A" {
		t.Fatalf("footer cache did not follow the changed leaf while preserving the latest session name: key=%q name=%q", r.footerKey, r.footerSnapshot.SessionName)
	}
}

func TestFooterProviderCountUsesAvailableCatalogs(t *testing.T) {
	if got := footerProviderCount(&config.ModelsFile{}); got != 19 {
		t.Fatalf("available provider count=%d, want all embedded provider catalogs", got)
	}
	if got := footerProviderCount(nil); got != 0 {
		t.Fatalf("nil model catalog has provider count %d", got)
	}
}

func TestFooterThinkingLabelFollowsPi(t *testing.T) {
	if got := footerThinkingLabel(provider.ModelMeta{ID: "m"}, "high"); got != "" {
		t.Fatalf("non-reasoning model shows a thinking level: %q", got)
	}
	if got := footerThinkingLabel(provider.ModelMeta{ID: "m", Reasoning: true}, "high"); got != "high" {
		t.Fatalf("reasoning model lost its thinking level: %q", got)
	}
	if got := footerThinkingLabel(provider.ModelMeta{ID: "m", Reasoning: true}, "off"); got != "thinking off" {
		t.Fatalf("disabled thinking level is not spelled out: %q", got)
	}
	if got := footerThinkingLabel(provider.ModelMeta{ID: "m", Reasoning: true}, ""); got != "thinking off" {
		t.Fatalf("unset thinking level is not spelled out: %q", got)
	}
}

func TestFooterContextTracksProviderUsageAcrossProjectionCacheAndCompaction(t *testing.T) {
	r := testRuntime(t)
	r.cfg.Model.ContextWindow = 100000
	r.cfg.AutoCompact = false
	r.cfg.KeepRecentTokens = 20
	var output bytes.Buffer
	input, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	defer input.Close()
	console := terminal.NewConsole(input, &output)
	defer console.Close()

	fake := &provider.Fake{Responses: []*provider.Response{
		{Text: strings.Repeat("first answer ", 20), Usage: provider.Usage{Input: 16000, Output: 600, TotalTokens: 16600}},
		{Text: strings.Repeat("second answer ", 20), Usage: provider.Usage{Input: 41000, Output: 1000, TotalTokens: 42000}},
		{Text: "third answer", Usage: provider.Usage{Input: 64000, Output: 1000, TotalTokens: 65000}},
		{Text: "compacted conversation summary", Usage: provider.Usage{Input: 5000, Output: 500, TotalTokens: 5500}},
		{Text: "post-compaction answer", Usage: provider.Usage{Input: 12000, Output: 500, TotalTokens: 12500}},
	}}
	r.prov = fake
	loop := agent.New(r.store, r.reg, fake, nil)

	if err := loop.Submit(context.Background(), "first question with enough text", r.cfg); err != nil {
		t.Fatal(err)
	}
	if r.store.ProjectionIfCached() == nil {
		t.Fatal("expected Submit to cache the session projection")
	}
	r.refreshPresentation(console)
	if got := r.footerSnapshot.ContextUsageLabel; got != "16.6%" {
		t.Fatalf("turn 1 percent label = %q; want 16.6%% (provider usage must not be replaced by character estimate when projection is cached)", got)
	}

	if err := loop.Submit(context.Background(), "second question with enough text", r.cfg); err != nil {
		t.Fatal(err)
	}
	r.refreshPresentation(console)
	if got := r.footerSnapshot.ContextUsageLabel; got != "42.0%" {
		t.Fatalf("turn 2 percent label = %q; want 42.0%%", got)
	}

	// Reopening the session starts with an uncached projection; both states
	// must agree on the provider-reported context usage.
	r.store.Unlock()
	reopened, err := session.OpenFile(r.store.File(), r.store.Cwd(), false)
	if err != nil {
		t.Fatal(err)
	}
	if err := reopened.TryLock(); err != nil {
		t.Fatal(err)
	}
	r.store = reopened
	r.footerKey = ""
	r.refreshPresentation(console)
	if got := r.footerSnapshot.ContextUsageLabel; got != "42.0%" {
		t.Fatalf("reopened percent label = %q; want 42.0%%", got)
	}

	loop = agent.New(r.store, r.reg, fake, nil)
	if err := loop.Submit(context.Background(), strings.Repeat("third question ", 10), r.cfg); err != nil {
		t.Fatal(err)
	}
	r.refreshPresentation(console)
	if got := r.footerSnapshot.ContextUsageLabel; got != "65.0%" {
		t.Fatalf("turn 3 after reopen percent label = %q; want 65.0%%", got)
	}

	// Compaction invalidates pre-compaction usage and warms the post-compaction
	// projection so the footer immediately shows the compacted estimate.
	if err := loop.Compact(context.Background(), r.cfg, ""); err != nil {
		t.Fatal(err)
	}
	r.refreshPresentation(console)
	if r.footerSnapshot.ContextUsageLabel == "" || r.footerSnapshot.ContextPercent >= 65.0 {
		t.Fatalf("after compaction: label=%q percent=%v; want non-empty compacted estimate below 65%%", r.footerSnapshot.ContextUsageLabel, r.footerSnapshot.ContextPercent)
	}

	if err := loop.Submit(context.Background(), "post-compaction question", r.cfg); err != nil {
		t.Fatal(err)
	}
	r.refreshPresentation(console)
	if got := r.footerSnapshot.ContextUsageLabel; got != "12.5%" {
		t.Fatalf("post-compaction turn percent label = %q; want 12.5%%", got)
	}
}

type gatedToolProvider struct {
	mu            sync.Mutex
	calls         int
	toolStarted   chan struct{}
	releaseSecond chan struct{}
}

func (p *gatedToolProvider) Send(ctx context.Context, _ provider.Request, _ func(provider.Delta)) (*provider.Response, error) {
	p.mu.Lock()
	p.calls++
	call := p.calls
	p.mu.Unlock()
	if call == 1 {
		return &provider.Response{
			StopReason: "toolUse",
			Usage:      provider.Usage{Input: 41000, Output: 1000, TotalTokens: 42000},
			ToolCalls: []provider.Block{{
				Type:      "toolCall",
				ID:        "call-1",
				Name:      "read",
				Arguments: map[string]any{"path": "note.txt"},
			}},
		}, nil
	}
	close(p.toolStarted)
	select {
	case <-p.releaseSecond:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return &provider.Response{
		Text:       "done",
		StopReason: "stop",
		Usage:      provider.Usage{Input: 50000, Output: 1000, TotalTokens: 51000},
	}, nil
}

func (*gatedToolProvider) CloseIdle() {}

type notifyingBuffer struct {
	mu     sync.Mutex
	b      strings.Builder
	notify chan struct{}
}

func (b *notifyingBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	n, err := b.b.Write(p)
	b.mu.Unlock()
	select {
	case b.notify <- struct{}{}:
	default:
	}
	return n, err
}

func (b *notifyingBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.String()
}

func TestReplUpdatesFooterContextDuringToolLoop(t *testing.T) {
	r := testRuntime(t)
	r.authErr = nil
	r.cfg.Model.ContextWindow = 100000
	if err := os.WriteFile(filepath.Join(r.store.Cwd(), "note.txt"), []byte("hello\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	prov := &gatedToolProvider{
		toolStarted:   make(chan struct{}),
		releaseSecond: make(chan struct{}),
	}
	r.prov = prov
	input, inputWrite, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	out := &notifyingBuffer{notify: make(chan struct{}, 1)}
	console := terminal.NewConsole(input, out)
	console.SizeFunc = func() (int, int) { return 80, 24 }
	if !console.Editor.Insert("run tool") {
		t.Fatal("could not install draft")
	}
	defer func() {
		_ = inputWrite.Close()
		console.Close()
		_ = input.Close()
	}()

	result := make(chan error, 1)
	go func() { result <- r.repl(console) }()
	for !strings.Contains(out.String(), "(anthropic) m") {
		<-out.notify
	}
	if _, err := inputWrite.Write([]byte("\r")); err != nil {
		t.Fatal(err)
	}
	<-prov.toolStarted
	for !strings.Contains(out.String(), "42.0%/100k") {
		<-out.notify
	}
	// The footer row is rendered from the presentation snapshot's
	// ContextUsageLabel, so the observed string pins the mid-run value.
	close(prov.releaseSecond)
	for !strings.Contains(out.String(), "51.0%/100k") {
		<-out.notify
	}
	if err := inputWrite.Close(); err != nil {
		t.Fatal(err)
	}
	if err := <-result; err != nil {
		t.Fatalf("repl failed: %v", err)
	}
}

// startupQueueProvider gates the first run after its final follow-up poll.
// The adaptive-compaction check snapshots the live request settings there,
// which installs the retry observer on the current provider.
type startupQueueProvider struct {
	*provider.Fake
	atSettlement chan struct{}
	settledLast  chan struct{}
	release      chan struct{}
	stop         <-chan struct{}
	once         sync.Once
}

func (p *startupQueueProvider) SetRetryObserver(observer func(provider.RetryInfo)) {
	if observer == nil {
		if p.CallCount() == 4 {
			close(p.settledLast)
		}
		return
	}
	if p.CallCount() == 1 {
		p.once.Do(func() {
			close(p.atSettlement)
			select {
			case <-p.release:
			case <-p.stop:
			}
		})
	}
}

func TestLateFollowUpPrecedesRemainingStartupMessages(t *testing.T) {
	r := testRuntime(t)
	r.state, r.authErr = StateIdle, nil
	r.options.Messages = []string{"STARTUP_FIRST", "STARTUP_SECOND", "STARTUP_THIRD"}
	p := &startupQueueProvider{
		Fake: &provider.Fake{Responses: []*provider.Response{
			{Text: "FIRST_ANSWER", StopReason: "stop"},
			{Text: "SECOND_ANSWER", StopReason: "stop"},
			{Text: "THIRD_ANSWER", StopReason: "stop"},
			{Text: "LAST_ANSWER", StopReason: "stop"},
		}},
		atSettlement: make(chan struct{}),
		settledLast:  make(chan struct{}),
		release:      make(chan struct{}),
		stop:         t.Context().Done(),
	}
	r.prov = p
	_, out, send, finish := startBumpREPL(t, r)
	waitBumpSettlement(t, p.atSettlement)
	send("LATE_FOLLOW_UP\x1b\r")
	waitBumpOutput(t, out, "Follow-up: LATE_FOLLOW_UP")
	send("/settings\r")
	waitBumpOutput(t, out, "Auto-compact")
	close(p.release)
	assertPrompts := func(want ...string) {
		t.Helper()
		requests := p.Requests()
		if len(requests) < len(want) {
			t.Fatalf("provider received %d requests, want at least %d", len(requests), len(want))
		}
		for i, expected := range want {
			messages := requests[i].Messages
			if len(messages) == 0 {
				t.Fatalf("request %d has no messages", i+1)
			}
			last := messages[len(messages)-1]
			if last.Role != "user" || len(last.Content) == 0 || last.Content[0].Text != expected {
				t.Fatalf("request %d ends with %+v, want user prompt %q", i+1, last, expected)
			}
		}
	}
	waitBumpOutput(t, out, "SECOND_ANSWER")
	assertPrompts("STARTUP_FIRST", "LATE_FOLLOW_UP")
	waitBumpSettlement(t, p.settledLast)
	send("\x03/quit\r") // Ctrl+C closes settings before /quit reaches the editor.
	finish()
	assertPrompts("STARTUP_FIRST", "LATE_FOLLOW_UP", "STARTUP_SECOND", "STARTUP_THIRD")
	if p.CallCount() != 4 {
		t.Fatalf("provider received %d requests, want exactly 4", p.CallCount())
	}
}

func TestPickerClosePreservesStorageBlocked(t *testing.T) {
	r := testRuntime(t)
	r.authErr = nil
	// A run outcome of unsaved surfaces as storage-blocked on picker close.
	if got := r.pickerCloseState(false, "unsaved", StateSelecting); got != StateStorageBlocked {
		t.Fatalf("unsaved outcome close = %q, want storage_blocked", got)
	}
	// A clean outcome with no failures restores idle (covered below with r2);
	// the stored-write-error branch shares the same blocked outcome as unsaved.
	// While a run is active the underlying run state is preserved.
	for _, current := range []State{StateWaiting, StateCancelling, StateCompacting, StateRunningTool} {
		if got := r.pickerCloseState(true, "", current); got != current {
			t.Fatalf("busy close from %q = %q, want preserved", current, got)
		}
	}
	// Idle close with no failures restores idle, or offline when auth failed.
	r2 := testRuntime(t)
	r2.authErr = nil
	if got := r2.pickerCloseState(false, "", StateSelecting); got != StateIdle {
		t.Fatalf("clean close = %q, want idle", got)
	}
	r2.authErr = errors.New("no key")
	if got := r2.pickerCloseState(false, "", StateSelecting); got != StateOffline {
		t.Fatalf("offline close = %q, want offline", got)
	}
}
