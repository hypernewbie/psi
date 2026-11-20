package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/hypernewbie/psi/internal/config"
	"github.com/hypernewbie/psi/internal/provider"
	"github.com/hypernewbie/psi/internal/tools"
)

func TestDynamicToolsRefreshBeforeFirstRequestAfterCompaction(t *testing.T) {
	store, _, fake, runner, _ := testSetup(t)
	if _, err := store.AppendMessage(userMsg(strings.Repeat("older history ", 1000))); err != nil {
		t.Fatal(err)
	}
	fake.Responses = []*provider.Response{
		{Text: "summary", StopReason: "stop"},
		batchResponse(provider.Block{Type: "toolCall", ID: "history", Name: "read_session", Arguments: map[string]any{"action": "list"}}),
		{Text: "answer", StopReason: "stop"},
	}
	calls := 0
	cfg := Config{
		Model:       provider.ModelMeta{ID: "m", API: "anthropic-messages", ContextWindow: 10000, MaxTokens: 1000},
		AutoCompact: true, ReserveTokens: 8000, KeepRecentTokens: 1,
		DynamicConfig: func() (string, []config.PromptSection, []provider.ToolDef) {
			if store.CompactionCount() > 0 {
				return "", nil, []provider.ToolDef{{Name: "read_session", Schema: map[string]any{"type": "object"}}}
			}
			return "", nil, nil
		},
		InternalTools: map[string]InternalToolHandler{
			"read_session": func(context.Context, tools.Invocation) tools.Result {
				calls++
				return tools.Result{Content: []tools.Block{{Type: "text", Text: "history"}}}
			},
		},
	}
	if err := runner.Submit(context.Background(), "current task", cfg); err != nil {
		t.Fatal(err)
	}
	requests := fake.Requests()
	if len(requests) != 3 || len(requests[1].Tools) != 1 || requests[1].Tools[0].Name != "read_session" || calls != 1 {
		t.Fatalf("post-compaction requests=%+v, history calls=%d", requests, calls)
	}
}
