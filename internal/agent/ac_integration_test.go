package agent

import (
	"context"
	"strings"
	"testing"

	"psi/internal/ac"
	"psi/internal/provider"
)

func TestAdaptiveCompactionRunsBeforeNextToolTurn(t *testing.T) {
	store, _, fake, loop, _ := testSetup(t)
	if _, err := store.AppendMessage(userMsg("old request")); err != nil {
		t.Fatal(err)
	}
	appendAssistantText(t, store, strings.Repeat("older context ", 6_000))
	fake.Responses = []*provider.Response{
		{Text: "running a tool", StopReason: "toolUse", ToolCalls: []provider.Block{{Type: "toolCall", ID: "ac-tool", Name: "bash", Arguments: map[string]any{"command": "true"}}}},
		{Text: "summary of older context", StopReason: "stop"},
		{Text: "summary of the split turn", StopReason: "stop"},
		{Text: "continued after adaptive compact", StopReason: "stop"},
	}
	controller := ac.NewController()
	cfg := Config{
		Model:            provider.ModelMeta{ID: "small", API: "anthropic-messages", ContextWindow: 40_000, MaxTokens: 2_000},
		Thinking:         "off",
		Tools:            toolDefs(),
		AutoCompact:      true,
		ReserveTokens:    1_000,
		KeepRecentTokens: 10,
		ACPolicy:         controller,
	}
	if err := loop.Submit(context.Background(), "continue the task", cfg); err != nil {
		t.Fatal(err)
	}
	if fake.CallCount() != 4 {
		t.Fatalf("provider calls = %d, want tool turn, compact summaries, continuation", fake.CallCount())
	}
	state := controller.Snapshot()
	if state.CompactionCount != 1 || state.LastCompaction == nil || state.LastCompaction.Source != "automatic" {
		t.Fatalf("adaptive compaction state = %+v", state)
	}
	request := fake.Calls[3]
	if !strings.Contains(stringifyMessages(request.Messages), "summary of older context") {
		t.Fatalf("continuation request did not use compacted context: %+v", request.Messages)
	}
}

func stringifyMessages(messages []provider.Message) string {
	var out strings.Builder
	for _, message := range messages {
		for _, block := range message.Content {
			out.WriteString(block.Text)
		}
	}
	return out.String()
}
