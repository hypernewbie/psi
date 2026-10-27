package agent

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"psi/internal/provider"
	"psi/internal/session"
	"psi/internal/tools"
)

// P2-084: disposable coding task — synthetic repo, defect + tests,
// read→edit/write→bash→final, resume + continue without repeating actions.
func TestDisposableCodingTask(t *testing.T) {
	ws := t.TempDir()
	sdir := t.TempDir()
	// Synthetic repo with defect.
	os.WriteFile(filepath.Join(ws, "parse.go"), []byte("package p\nfunc Parse(s string) int { return 1 }\n"), 0o644)
	os.WriteFile(filepath.Join(ws, "parse_test.go"), []byte("package p\n"), 0o644)
	store, _ := session.New(session.OpenOptions{Cwd: ws, SessionDir: sdir, Persist: true})
	reg := tools.NewRegistry(ws)
	fake := &provider.Fake{
		Responses: []*provider.Response{
			{Text: "inspect", StopReason: "toolUse", ToolCalls: []provider.Block{{Type: "toolCall", ID: "c1", Name: "read", Arguments: map[string]any{"path": "parse.go"}}}},
			{Text: "fix", StopReason: "toolUse", ToolCalls: []provider.Block{{Type: "toolCall", ID: "c2", Name: "edit", Arguments: map[string]any{"path": "parse.go", "edits": []any{map[string]any{"oldText": "return 1", "newText": "return 0"}}}}}},
			{Text: "verify", StopReason: "toolUse", ToolCalls: []provider.Block{{Type: "toolCall", ID: "c3", Name: "bash", Arguments: map[string]any{"command": "echo ok"}}}},
			{Text: "done: empty file returns 0", StopReason: "stop"},
		},
		TextDeltas: [][]string{{"inspect"}, {"fix"}, {"verify"}, {"done"}},
	}
	cfg := Config{Model: provider.ModelMeta{ID: "m", API: "anthropic-messages"}, Thinking: "off", Tools: []provider.ToolDef{{Name: "read"}, {Name: "edit"}, {Name: "bash"}}}
	if err := New(store, reg, fake, nil).Submit(context.Background(), "fix parser", cfg); err != nil {
		t.Fatal(err)
	}
	// Files + transcript + session agree.
	data, _ := os.ReadFile(filepath.Join(ws, "parse.go"))
	if string(data) == "" || contains(string(data), "return 1") {
		t.Fatalf("fix not applied: %q", data)
	}
	// Resume saved session and continue without repeating completed actions.
	file := store.File()
	reopened, err := session.OpenFile(file, ws, true)
	if err != nil {
		t.Fatal(err)
	}
	before, _ := reopened.Projection()
	fake2 := &provider.Fake{
		Responses:  []*provider.Response{{Text: "confirmed", StopReason: "stop"}},
		TextDeltas: [][]string{{"confirmed"}},
	}
	if err := New(reopened, reg, fake2, nil).Submit(context.Background(), "confirm", cfg); err != nil {
		t.Fatal(err)
	}
	after, _ := reopened.Projection()
	if len(after.Flat) <= len(before.Flat) {
		t.Fatal("resume did not extend")
	}
	// No repeated tool side effects: only one edit persisted.
	count := 0
	for _, e := range mustPath2(t, reopened) {
		if e.Kind == "message" && e.Message.Message.ToolResult != nil {
			count++
		}
	}
	if count != 3 {
		t.Fatalf("tool results repeated or missing: %d", count)
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (func() bool {
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				return true
			}
		}
		return false
	})()
}

func mustPath2(t *testing.T, s *session.Store) []*session.Entry {
	t.Helper()
	p, err := s.ActivePath()
	if err != nil {
		t.Fatal(err)
	}
	return p
}
