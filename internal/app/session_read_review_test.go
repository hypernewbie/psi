package app

import (
	"context"
	"strings"
	"testing"

	"github.com/hypernewbie/psi/internal/config"
	"github.com/hypernewbie/psi/internal/provider"
)

func TestRPCSessionHistoryToolWorksBeforeCompaction(t *testing.T) {
	fake := &provider.Fake{Responses: []*provider.Response{
		{StopReason: "toolUse", ToolCalls: []provider.Block{{Type: "toolCall", ID: "history", Name: "read_session", Arguments: map[string]any{"action": "search", "query": "needle"}}}},
		{Text: "finished", StopReason: "stop"},
	}}
	s, store, _ := newRPCTestRunner(t, fake, nil)
	s.r.settings.DefaultTools = nil
	if _, err := store.AppendMessage(agentMessageUser("earlier needle")); err != nil {
		t.Fatal(err)
	}
	if err := s.loop.Submit(context.Background(), "find the earlier fact", s.runConfig()); err != nil {
		t.Fatal(err)
	}
	requests := fake.Requests()
	if len(requests) != 2 || len(requests[0].Tools) != 1 || requests[0].Tools[0].Name != "read_session" {
		t.Fatalf("RPC requests: %+v", requests)
	}
	found := false
	for _, message := range requests[1].Messages {
		if message.Role == "toolResult" && message.ToolCallID == "history" {
			for _, block := range message.Content {
				found = found || strings.Contains(block.Text, "earlier needle")
			}
		}
	}
	if !found {
		t.Fatal("raw history search did not reach the model")
	}
}

func TestSessionHistoryPromptIsPresentWithoutSelectedTools(t *testing.T) {
	names := promptToolNames(nil, nil)
	prompt := config.RenderSections(config.BuildPromptSections(t.TempDir(), nil, nil, names, "", false, nil))
	if !strings.Contains(prompt, "read_session: Read a narrow raw excerpt") || !strings.Contains(prompt, "Search or list before reading") {
		t.Fatalf("history tool guidance missing: %s", prompt)
	}
}
