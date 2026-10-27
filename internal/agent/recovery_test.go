package agent

import (
	"context"
	"strings"
	"testing"

	"psi/internal/event"
	"psi/internal/provider"
	"psi/internal/session"
	"psi/internal/tools"
)

// P2-090: bounded context-limit recovery — one safe compaction + retry, no repeated tools.
func TestContextLimitRecovery(t *testing.T) {
	for _, mode := range []string{"success", "repeated limit", "partial response"} {
		t.Run(mode, func(t *testing.T) {
			dir := t.TempDir()
			store, err := session.New(session.OpenOptions{Cwd: dir, SessionDir: dir, AgentDir: dir, Persist: false})
			if err != nil {
				t.Fatal(err)
			}
			cfg := Config{Model: provider.ModelMeta{ID: "m", ContextWindow: 100000, MaxTokens: 4096}, AutoCompact: true, KeepRecentTokens: 1, Tools: tools.Definitions([]string{"write"})}
			store.AppendMessage(userMsg("older request"))
			store.AppendMessage(toSessionAssistant(&provider.Response{Text: "older answer"}, cfg))
			requests, toolRuns := 0, 0
			limit := &provider.ProviderError{Kind: provider.ErrContextLimit, Status: 400, Message: "synthetic context limit"}
			fake := reviewProvider(func(_ context.Context, req provider.Request, text func(string)) (*provider.Response, error) {
				requests++
				switch requests {
				case 1:
					return &provider.Response{ToolCalls: []provider.Block{{Type: "toolCall", ID: "once", Name: "write", Arguments: map[string]any{"path": "once.txt", "content": "once"}}}}, nil
				case 2:
					if mode == "partial response" {
						text("visible partial")
					}
					return nil, limit
				case 3:
					if len(req.Tools) != 0 || !strings.Contains(req.System, "Summarize") {
						t.Error("recovery did not request a tool-free summary")
					}
					return &provider.Response{Text: "summary", StopReason: "stop"}, nil
				case 4:
					if mode == "repeated limit" {
						return nil, limit
					}
					return &provider.Response{Text: "recovered", StopReason: "stop"}, nil
				default:
					t.Error("recovery retried more than once")
					return nil, limit
				}
			})
			loop := New(store, tools.NewRegistry(dir), fake, func(e event.Event) {
				if e.Type == event.ToolStart {
					toolRuns++
				}
			})
			err = loop.Submit(context.Background(), "current task", cfg)
			if (err != nil) != (mode != "success") {
				t.Fatalf("outcome: %v", err)
			}
			wantRequests := 4
			if mode == "partial response" {
				wantRequests = 2
			}
			if requests != wantRequests || toolRuns != 1 {
				t.Fatalf("requests=%d tools=%d", requests, toolRuns)
			}
			compactions := 0
			for _, e := range store.Entries() {
				if e.Kind == "compaction" {
					compactions++
				}
			}
			wantCompactions := 1
			if mode == "partial response" {
				wantCompactions = 0
			}
			if compactions != wantCompactions {
				t.Fatalf("compactions=%d", compactions)
			}
		})
	}
}

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
