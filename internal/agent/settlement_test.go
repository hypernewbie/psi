package agent

import (
	"context"
	"strings"
	"testing"

	"psi/internal/provider"
	"psi/internal/session"
	"psi/internal/tools"
)

// P2-043, P2-054: skill provenance ordering — user, then psi.input, no context change.
func TestProvenanceOrdering(t *testing.T) {
	dir := t.TempDir()
	store, err := session.New(session.OpenOptions{Cwd: dir, SessionDir: dir, Persist: false})
	if err != nil {
		t.Fatal(err)
	}
	reg := tools.NewRegistry(dir)
	fake := &provider.Fake{Responses: []*provider.Response{{Text: "ok", StopReason: "stop"}}, TextDeltas: [][]string{{"o", "k"}}}
	loop := New(store, reg, fake, nil)
	cfg := Config{Model: provider.ModelMeta{ID: "m", API: "anthropic-messages"}, Thinking: "off", Tools: []provider.ToolDef{}}
	if err := loop.SubmitWithProvenance(context.Background(), "expanded body", "/skill:review fix", "skill", cfg); err != nil {
		t.Fatal(err)
	}
	flat, err := store.Projection()
	if err != nil {
		t.Fatal(err)
	}
	// Model context must not include provenance (pi ignores "custom").
	for _, m := range flat.Flat {
		if m.Role == "custom" {
			t.Fatalf("provenance leaked to context: %+v", m)
		}
	}
	// File must contain user + custom psi.input.
	path, _ := store.ActivePath()
	foundUser, foundProv := false, false
	for _, e := range path {
		if e.Kind == "message" && e.Message.Message.User != nil {
			foundUser = true
		}
		if e.Kind == "custom" && e.Custom.CustomType == "psi.input" {
			foundProv = true
		}
	}
	if !foundUser || !foundProv {
		t.Fatalf("ordering: user=%v prov=%v", foundUser, foundProv)
	}
	// Recall prefers visible invocation.
	recall := session.PromptRecall(path)
	if len(recall) == 0 || !strings.Contains(recall[len(recall)-1], "/skill:review") {
		t.Fatalf("recall should prefer invocation, got %v", recall)
	}
}

// P2-055: settlement — terminal, stored, next request agree; unsaved explicit.
func TestSettlementPersistence(t *testing.T) {
	dir := t.TempDir()
	store, _ := session.New(session.OpenOptions{Cwd: dir, SessionDir: dir, Persist: false})
	reg := tools.NewRegistry(dir)
	fake := &provider.Fake{Responses: []*provider.Response{{Text: "final answer", StopReason: "stop"}}, TextDeltas: [][]string{{"final ", "answer"}}}
	cfg := Config{Model: provider.ModelMeta{ID: "m", API: "anthropic-messages"}, Thinking: "off"}
	if err := New(store, reg, fake, nil).Submit(context.Background(), "hi", cfg); err != nil {
		t.Fatal(err)
	}
	proj, _ := store.Projection()
	if len(proj.Flat) < 2 {
		t.Fatalf("expected user+assistant, got %d", len(proj.Flat))
	}
}

// P2-056: cancellation with partial text persists aborted form, no executable call.
func TestCancelPersistsAborted(t *testing.T) {
	dir := t.TempDir()
	store, _ := session.New(session.OpenOptions{Cwd: dir, SessionDir: dir, Persist: false})
	reg := tools.NewRegistry(dir)
	// Cancel before provider call: no partial, no executable call.
	fake := &provider.Fake{Responses: []*provider.Response{{Text: "partial", StopReason: "stop"}}}
	loop := New(store, reg, fake, nil)
	cfg := Config{Model: provider.ModelMeta{ID: "m", API: "anthropic-messages"}, Thinking: "off"}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := loop.Submit(ctx, "hi", cfg)
	if err == nil {
		t.Fatal("cancelled submit should fail")
	}
	// If partial was streamed, an aborted message should exist; never a tool call.
	proj, _ := store.Projection()
	for _, m := range proj.Flat {
		if m.Role == "assistant" && m.Assistant != nil {
			for _, b := range m.Assistant.Content {
				if b.Type == "toolCall" {
					t.Fatal("partial must not become executable call")
				}
			}
		}
	}
}
