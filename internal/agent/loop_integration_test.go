package agent

import (
	"context"
	"strings"
	"testing"

	"psi/internal/provider"
	"psi/internal/session"
	"psi/internal/tools"
)

func TestHiddenContinuationAddsModelContextWithoutVisibleTranscript(t *testing.T) {
	store, _, fake, loop, _ := testSetup(t)
	fake.Responses = []*provider.Response{{Text: "first turn", StopReason: "stop"}, {Text: "final turn", StopReason: "stop"}}
	continuations := 0
	cfg := Config{
		Model: modelMeta(),
		ContinueOnEnd: func() (string, string, bool) {
			continuations++
			if continuations == 1 {
				return "psi.loop.nudge", "continue the active task", true
			}
			return "", "", false
		},
	}
	if err := loop.Submit(context.Background(), "start", cfg); err != nil {
		t.Fatal(err)
	}
	if len(fake.Calls) != 2 {
		t.Fatalf("provider requests = %d, want continuation request", len(fake.Calls))
	}
	last := fake.Calls[1].Messages[len(fake.Calls[1].Messages)-1]
	if last.Role != "user" || !containsBlockText(last.Content, "continue the active task") {
		t.Fatalf("hidden continuation request message = %+v", last)
	}
	var timeline strings.Builder
	if _, err := store.Timeline(true, 0, func(item session.TranscriptItem) bool {
		timeline.WriteString(item.Text)
		return true
	}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(timeline.String(), "continue the active task") {
		t.Fatalf("hidden continuation appeared in transcript: %s", timeline.String())
	}
}

func TestDynamicToolsUpdateAfterInternalToolStateChanges(t *testing.T) {
	_, _, fake, loop, _ := testSetup(t)
	control := provider.ToolDef{Name: "loop_control", Description: "control", Schema: map[string]any{"type": "object"}}
	active := true
	fake.Responses = []*provider.Response{
		{Text: "control", StopReason: "toolUse", ToolCalls: []provider.Block{{Type: "toolCall", ID: "control-1", Name: "loop_control", Arguments: map[string]any{"status": "done", "summary": "complete"}}}},
		{Text: "finished", StopReason: "stop"},
	}
	cfg := Config{
		Model: modelMeta(),
		DynamicConfig: func() (string, []provider.ToolDef) {
			if active {
				return "active loop", []provider.ToolDef{control}
			}
			return "normal", nil
		},
		InternalTools: map[string]InternalToolHandler{
			"loop_control": func(_ context.Context, _ tools.Invocation) tools.Result {
				active = false
				return tools.Result{Content: []tools.Block{{Type: "text", Text: "loop complete"}}}
			},
		},
	}
	if err := loop.Submit(context.Background(), "start", cfg); err != nil {
		t.Fatal(err)
	}
	if len(fake.Calls) != 2 || len(fake.Calls[0].Tools) != 1 || fake.Calls[0].Tools[0].Name != "loop_control" || len(fake.Calls[1].Tools) != 0 {
		t.Fatalf("dynamic tool sets = %#v", []any{fake.Calls[0].Tools, fake.Calls[1].Tools})
	}
	if fake.Calls[1].System != "normal" {
		t.Fatalf("refreshed system prompt = %q", fake.Calls[1].System)
	}
}

func containsBlockText(blocks []provider.Block, want string) bool {
	for _, block := range blocks {
		if block.Type == "text" && strings.Contains(block.Text, want) {
			return true
		}
	}
	return false
}
