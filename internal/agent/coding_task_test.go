package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"psi/internal/event"
	"psi/internal/provider"
	"psi/internal/session"
	"psi/internal/tools"
)

// The fixture has a real failing behavior test. An unconditional "echo ok"
// cannot establish that a code change fixed the reported defect.
func TestDisposableCodingTask(t *testing.T) {
	ws, sdir := t.TempDir(), t.TempDir()
	broken := "parse() { if [ -z \"$1\" ]; then printf '1\\n'; else printf '1\\n'; fi; }\n"
	checks := ". ./parse.sh\ntest \"$(parse '')\" = 0 && test \"$(parse value)\" = 1\n"
	if err := os.WriteFile(filepath.Join(ws, "parse.sh"), []byte(broken), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ws, "test.sh"), []byte(checks), 0600); err != nil {
		t.Fatal(err)
	}
	store, err := session.New(session.OpenOptions{Cwd: ws, SessionDir: sdir, Persist: true})
	if err != nil {
		t.Fatal(err)
	}
	reg := tools.NewRegistry(ws)
	if baseline := reg.DispatchCtx(context.Background(), tools.Invocation{Name: "bash", Arguments: map[string]any{"command": "bash test.sh"}}); !baseline.IsError {
		t.Fatal("fixture did not fail before the edit")
	}
	fake := &provider.Fake{
		Responses: []*provider.Response{
			{Text: "inspect", ToolCalls: []provider.Block{{Type: "toolCall", ID: "c1", Name: "read", Arguments: map[string]any{"path": "parse.sh"}}}},
			{Text: "fix", ToolCalls: []provider.Block{{Type: "toolCall", ID: "c2", Name: "edit", Arguments: map[string]any{"path": "parse.sh", "edits": []any{map[string]any{"oldText": "then printf '1\\n'", "newText": "then printf '0\\n'"}}}}}},
			{Text: "check", ToolCalls: []provider.Block{{Type: "toolCall", ID: "c3", Name: "bash", Arguments: map[string]any{"command": "bash test.sh"}}}},
			{Text: "done: empty input returns 0", StopReason: "stop"},
		},
		TextDeltas: [][]string{{"inspect"}, {"fix"}, {"check"}, {"done"}},
	}
	cfg := Config{Model: provider.ModelMeta{ID: "m", API: "anthropic-messages"}, Thinking: "off", Tools: tools.Definitions([]string{"read", "edit", "bash"})}
	var visible strings.Builder
	if err := New(store, reg, fake, func(e event.Event) {
		if e.Type == event.TextDelta {
			visible.WriteString(e.Text)
		}
	}).Submit(context.Background(), "fix the empty-input parser", cfg); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(ws, "parse.sh"))
	if err != nil || string(data) == broken || !strings.Contains(string(data), "then printf '0\\n'") {
		t.Fatal("parser was not fixed")
	}
	if !strings.Contains(visible.String(), "done: empty input returns 0") {
		t.Fatal("final answer suffix missing")
	}
	reopened, err := session.OpenFile(store.File(), ws, true)
	if err != nil {
		t.Fatal(err)
	}
	fake2 := &provider.Fake{Responses: []*provider.Response{{Text: "continued", StopReason: "stop"}}}
	if err := New(reopened, reg, fake2, nil).Submit(context.Background(), "continue", cfg); err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, e := range mustPath2(t, reopened) {
		if e.Kind == "message" && e.Message.Message.ToolResult != nil {
			count++
			if e.Message.Message.ToolResult.IsError {
				t.Fatal("coding task claimed success after a failed tool")
			}
		}
	}
	if count != 3 {
		t.Fatalf("tool results repeated or missing: %d", count)
	}
}

func mustPath2(t *testing.T, s *session.Store) []*session.Entry {
	t.Helper()
	path, err := s.ActivePath()
	if err != nil {
		t.Fatal(err)
	}
	return path
}
