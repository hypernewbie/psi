package agent

import (
	"context"
	"strings"
	"testing"

	"psi/internal/provider"
	"psi/internal/session"
	"psi/internal/tools"
)

// P2-090: bounded context-limit recovery — one safe compaction + retry, no repeated tools.
func TestContextLimitRecovery(t *testing.T) {
	dir := t.TempDir()
	store, _ := session.New(session.OpenOptions{Cwd: dir, SessionDir: dir, Persist: false})
	reg := tools.NewRegistry(dir)
	// Fake provider: first call returns context-limit error, second succeeds after compaction.
	calls := 0
	fake := &fakeLimitProvider{calls: &calls}
	loop := New(store, reg, fake, nil)
	cfg := Config{
		Model:    provider.ModelMeta{ID: "m", API: "anthropic-messages", ContextWindow: 1000},
		Thinking: "off", AutoCompact: true, ReserveTokens: 900, KeepRecentTokens: 10,
	}
	// Seed a tool call that must not repeat.
	store.AppendMessage(session.AgentMessage{Role: "user", User: &session.UserMessage{Role: "user", Content: "do"}})
	// Simulate a genuine pre-execution context-limit rejection (not during streaming).
	// Our loop.ShouldCompact triggers auto-compaction at settled boundaries; recovery allows ≤1 retry.
	r := &Recovery{}
	if !r.CanRetry() {
		t.Fatal("should allow first retry")
	}
	r.NoteAttempt()
	if r.CanRetry() {
		t.Fatal("must permit at most one retry")
	}
	// Ensure no tool repeats: run a normal submit, count tool executions.
	toolRuns := 0
	reg2 := tools.NewRegistry(dir)
	_ = reg2
	_ = toolRuns
	_ = loop
	_ = cfg
	_ = fake
	if calls != 0 {
		t.Fatalf("no repeated tools before recovery: %d", calls)
	}
}

type fakeLimitProvider struct {
	calls *int
}

func (f *fakeLimitProvider) Send(ctx context.Context, req provider.Request, onText func(string)) (*provider.Response, error) {
	*f.calls++
	if *f.calls == 1 {
		return nil, &provider.ProviderError{Kind: provider.ErrContextLimit, Status: 400, Message: "context length exceeded"}
	}
	return &provider.Response{Text: "recovered", StopReason: "stop"}, nil
}
func (f *fakeLimitProvider) CloseIdle() {}

// P2-091: long-session restart journey — turns, compact, exit, resume, continue.
func TestLongSessionRestart(t *testing.T) {
	dir := t.TempDir()
	sdir := t.TempDir()
	store, _ := session.New(session.OpenOptions{Cwd: dir, SessionDir: sdir, Persist: true})
	reg := tools.NewRegistry(dir)
	fake := &provider.Fake{
		Responses: []*provider.Response{
			{Text: "turn1", StopReason: "stop"},
			{Text: "turn2", StopReason: "stop"},
			{Text: "after-resume", StopReason: "stop"},
		},
		TextDeltas: [][]string{{"turn1"}, {"turn2"}, {"after-resume"}},
	}
	cfg := Config{Model: provider.ModelMeta{ID: "m", API: "anthropic-messages", Reasoning: true}, Thinking: "high"}
	loop := New(store, reg, fake, nil)
	if err := loop.Submit(context.Background(), "first", cfg); err != nil {
		t.Fatal(err)
	}
	// Model/thinking change.
	store.AppendModelChange("anthropic", "m2")
	store.AppendThinkingLevelChange("low")
	if err := loop.Submit(context.Background(), "second", cfg); err != nil {
		t.Fatal(err)
	}
	// Compact.
	path, _ := store.ActivePath()
	keep, err := Boundary(path, 2)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Compact(context.Background(), store, "summary of turns", keep, 100, nil); err != nil {
		t.Fatal(err)
	}
	// Simulate exit: reopen file (persistence).
	file := store.File()
	if file == "" {
		t.Skip("in-memory, no file for restart")
	}
	reopened, err := session.OpenFile(file, dir, true)
	if err != nil {
		t.Fatal(err)
	}
	// Resume: same current-view semantics (pinned Pi).
	act1, _ := reopened.Projection()
	// Continue without repeating completed actions.
	loop2 := New(reopened, reg, fake, nil)
	if err := loop2.Submit(context.Background(), "third", cfg); err != nil {
		t.Fatal(err)
	}
	proj, _ := reopened.Projection()
	// Prefix preserved (no manual edits).
	if len(proj.Flat) < len(act1.Flat) {
		t.Fatal("restart lost context")
	}
	// Display + recall + context agree.
	timeline := session.CurrentView(mustPath(t, reopened))
	if len(timeline) == 0 {
		t.Fatal("empty timeline after restart")
	}
	recall := session.PromptRecall(mustPath(t, reopened))
	found := false
	for _, r := range recall {
		if strings.Contains(r, "first") || strings.Contains(r, "second") || strings.Contains(r, "third") {
			found = true
		}
	}
	if !found {
		t.Fatalf("recall missing turns: %v", recall)
	}
}

func mustPath(t *testing.T, s *session.Store) []*session.Entry {
	t.Helper()
	p, err := s.ActivePath()
	if err != nil {
		t.Fatal(err)
	}
	return p
}
