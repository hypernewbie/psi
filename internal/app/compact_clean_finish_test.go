//go:build unix

package app

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"psi/internal/agent"
	"psi/internal/config"
	"psi/internal/provider"
	"psi/internal/session"
	"psi/internal/terminal"
	"psi/internal/tools"
)

type compactCleanFinishProvider struct {
	mu       sync.Mutex
	calls    int
	bodies   []string
	first    chan struct{}
	release  chan struct{}
	summary  chan string
	complete chan struct{}
}

func (p *compactCleanFinishProvider) Send(_ context.Context, req provider.Request, onDelta func(provider.Delta)) (*provider.Response, error) {
	raw, _ := json.Marshal(req)
	p.mu.Lock()
	p.calls++
	index := p.calls
	p.bodies = append(p.bodies, string(raw))
	p.mu.Unlock()
	switch index {
	case 1:
		onDelta(provider.Delta{Kind: provider.DeltaText, Text: "ORIGINAL_PARTIAL"})
		close(p.first)
		<-p.release
		return &provider.Response{Text: "ORIGINAL_PARTIAL_FIRST_DONE", StopReason: "stop"}, nil
	case 2:
		p.summary <- string(raw)
		<-p.complete
		return &provider.Response{Text: "SUMMARY_COMMITTED", StopReason: "stop"}, nil
	default:
		return &provider.Response{Text: "EXTRA_REPLY", StopReason: "stop"}, nil
	}
}

func (*compactCleanFinishProvider) CloseIdle() {}

func compactCleanFinishRuntime(t *testing.T, p provider.Provider, work string) (*runtime, *session.Store) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	store, err := session.New(session.OpenOptions{Cwd: work, SessionDir: filepath.Join(home, "sessions"), AgentDir: filepath.Join(home, "agent"), Persist: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.TryLock(); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AppendMessage(agentMessageUser("seed")); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AppendMessage(session.AgentMessage{Role: "assistant", Assistant: &session.AssistantMessage{
		Role: "assistant", API: "anthropic-messages", Provider: "anthropic", Model: "m",
		StopReason: "stop", Content: []session.ContentBlock{{Type: "text", Text: &session.TextContent{Type: "text", Text: strings.Repeat("prior ", 6000)}}},
	}}); err != nil {
		t.Fatal(err)
	}
	settings := config.Settings{
		DefaultProvider: "anthropic", DefaultModel: "m", DefaultThinking: "off",
		Compaction: config.CompactionConfig{Enabled: false, KeepRecentTokens: 50},
	}
	r := &runtime{
		store: store, agentDir: filepath.Join(home, "agent"), settings: settings,
		models: &config.ModelsFile{Providers: map[string]config.ProviderMeta{"anthropic": {Models: []config.ModelMeta{{ID: "m", MaxTokens: 4096, ContextWindow: 22000}}}}},
		prompt: "test", live: newLiveConfig(), prov: p, reg: tools.NewRegistry(work),
	}
	r.ov = config.StartupOverrides{Provider: "anthropic", Model: "m", Thinking: "off"}
	r.cfg = agent.Config{
		Model:            provider.ModelMeta{ID: "m", Provider: "anthropic", API: "anthropic-messages", ContextWindow: 22000, MaxTokens: 4096},
		Thinking:         "off",
		System:           "test",
		AutoCompact:      false,
		KeepRecentTokens: 50,
	}
	r.publishLive()
	r.restoreAuthState()
	t.Cleanup(func() {
		store.Unlock()
		p.CloseIdle()
	})
	return r, store
}

func compactFinishOutputWait(t *testing.T, out *synchronizedBuffer, marker string) {
	t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(out.String(), marker) {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("terminal never showed %q: %q", marker, out.String())
}

// TestActualAppCompactCleanFinishKeepsQueuedInput pins the clean-finish race:
// the outgoing run completes successfully after the compact reservation, so
// its undelivered steering must return to the editor instead of continuing the
// old loop or being sent into the summary.
func TestActualAppCompactCleanFinishKeepsQueuedInput(t *testing.T) {
	work := t.TempDir()
	p := &compactCleanFinishProvider{
		first: make(chan struct{}), release: make(chan struct{}),
		summary: make(chan string, 1), complete: make(chan struct{}),
	}
	r, store := compactCleanFinishRuntime(t, p, work)

	input, inputWrite, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	out := &synchronizedBuffer{}
	console := terminal.NewConsole(input, out)
	console.SizeFunc = func() (int, int) { return 80, 24 }
	result := make(chan error, 1)
	go func() {
		err := r.repl(console)
		console.Close()
		result <- err
	}()
	stopped := false
	defer func() {
		_ = inputWrite.Close()
		if !stopped {
			_ = input.Close()
			select {
			case <-result:
			case <-time.After(5 * time.Second):
				t.Error("REPL did not stop")
			}
		}
	}()

	compactFinishOutputWait(t, out, "(anthropic) m")
	if _, err := inputWrite.WriteString("work\r"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-p.first:
	case <-time.After(3 * time.Second):
		t.Fatalf("run never reached the provider: %q", out.String())
	}
	compactFinishOutputWait(t, out, "ORIGINAL_PARTIAL")

	var armed atomic.Bool
	armed.Store(true)
	atSync := make(chan struct{})
	releaseSync := make(chan struct{})
	r.modelSyncHook = func() {
		if armed.CompareAndSwap(true, false) {
			close(atSync)
			<-releaseSync
		}
	}

	if _, err := inputWrite.WriteString("/compact FOCUS_MARKER\r"); err != nil {
		t.Fatal(err)
	}
	compactFinishOutputWait(t, out, "Cancelling the run for compaction.")
	close(p.release)
	select {
	case <-atSync:
	case <-time.After(8 * time.Second):
		t.Fatalf("run never reached the settlement gate: %q", out.String())
	}
	// The waiting manual compact owns the slot even though its summary
	// worker has not started. /ac compact must not queue a second summary.
	if _, err := inputWrite.WriteString("/ac compact\r"); err != nil {
		t.Fatal(err)
	}
	compactFinishOutputWait(t, out, "Compaction is already running.")
	// The final queue read already happened. This input is accepted, then
	// must be recovered instead of continued after the worker settles.
	if _, err := inputWrite.WriteString("RECOVER_ME\r"); err != nil {
		t.Fatal(err)
	}
	compactFinishOutputWait(t, out, "Steering: RECOVER_ME")
	close(releaseSync)

	var summary string
	select {
	case summary = <-p.summary:
	case <-time.After(8 * time.Second):
		t.Fatalf("summary request never arrived: %q", out.String())
	}
	if strings.Contains(summary, "RECOVER_ME") {
		t.Fatalf("queued input leaked into the summary request: %s", summary)
	}
	close(p.complete)
	compactFinishOutputWait(t, out, "Compacted from")

	_ = inputWrite.Close()
	select {
	case <-result:
		stopped = true
	case <-time.After(8 * time.Second):
		t.Fatalf("REPL did not stop after compaction: %q", out.String())
	}
	if got := console.Editor.Text(); strings.Count(got, "RECOVER_ME") != 1 {
		t.Fatalf("recovered editor text = %q, want one RECOVER_ME", got)
	}
	p.mu.Lock()
	calls := p.calls
	bodies := append([]string(nil), p.bodies...)
	p.mu.Unlock()
	if calls != 2 {
		t.Fatalf("provider calls = %d, want the clean run and one summary", calls)
	}
	if len(bodies) != 2 || !strings.Contains(bodies[1], "FOCUS_MARKER") {
		t.Fatalf("summary request lacks the focus marker: %v", bodies)
	}
	entries := store.Entries()
	for _, entry := range entries {
		if entry.Message != nil && entry.Message.Message.User != nil && entry.Message.Message.User.Content == "RECOVER_ME" {
			t.Fatal("recovered input was persisted as a user message")
		}
		if strings.Contains(fmt.Sprint(entry.Raw), "RECOVER_ME") {
			t.Fatalf("recovered input reached the session: %s", entry.Raw)
		}
	}
}
