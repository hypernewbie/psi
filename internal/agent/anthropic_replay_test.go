package agent

import (
	"context"
	"testing"

	"psi/internal/provider"
	"psi/internal/session"
	"psi/internal/tools"
)

func TestMiniMaxAndOpenCodeAnthropicSessionReplayAfterRestart(t *testing.T) {
	for _, tc := range []struct {
		providerID, modelID, signature string
		allowEmpty                     bool
	}{
		{"minimax", "MiniMax-M3.1-Flash", "synthetic-minimax-thinking-signature", false},
		{"opencode-go", "qwen3.8-flash", "", true},
	} {
		t.Run(tc.providerID, func(t *testing.T) {
			cwd := t.TempDir()
			store, err := session.New(session.OpenOptions{Cwd: cwd, SessionDir: t.TempDir(), Persist: true})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := store.AppendMessage(session.AgentMessage{Role: "user", User: &session.UserMessage{Role: "user", Content: "synthetic previous request", Timestamp: 1}}); err != nil {
				t.Fatal(err)
			}
			thinking := &session.ThinkingContent{Type: "thinking", Thinking: "synthetic prior reasoning"}
			if tc.signature != "" {
				thinking.ThinkingSignature = &tc.signature
			}
			if _, err := store.AppendMessage(session.AgentMessage{Role: "assistant", Assistant: &session.AssistantMessage{
				Role: "assistant", API: "anthropic-messages", Provider: tc.providerID, Model: tc.modelID,
				Content: []session.ContentBlock{
					{Type: "thinking", Thinking: thinking},
					{Type: "toolCall", Call: &session.ToolCall{Type: "toolCall", ID: "call_a", Name: "read", Arguments: map[string]any{"path": "a.txt"}}},
				}, StopReason: "toolUse", Timestamp: 2,
			}}); err != nil {
				t.Fatal(err)
			}
			if _, err := store.AppendMessage(session.AgentMessage{Role: "toolResult", ToolResult: &session.ToolResultMessage{
				Role: "toolResult", ToolCallID: "call_a", ToolName: "read", Content: []session.UserContentBlock{{Type: "text", Text: &session.TextContent{Type: "text", Text: "synthetic file content"}}}, Timestamp: 3,
			}}); err != nil {
				t.Fatal(err)
			}

			reopened, err := session.OpenFile(store.File(), cwd, false)
			if err != nil {
				t.Fatal(err)
			}
			fake := &provider.Fake{Responses: []*provider.Response{{Text: "synthetic continued answer", StopReason: "stop"}}}
			loop := New(reopened, tools.NewRegistry(cwd), fake, nil)
			model := provider.ModelMeta{ID: tc.modelID, API: "anthropic-messages", Provider: tc.providerID, MaxTokens: 16384, Reasoning: true, AdaptiveThinking: tc.providerID == "minimax", AllowEmptySignature: tc.allowEmpty}
			cfg := Config{Model: model, Thinking: "off", Tools: toolDefs(), MaxTurns: 2, System: "synthetic system"}
			if err := loop.Submit(context.Background(), "synthetic continuation", cfg); err != nil {
				t.Fatal(err)
			}
			if len(fake.Calls) != 1 || fake.Calls[0].Model.Provider != tc.providerID || fake.Calls[0].Model.ID != tc.modelID {
				t.Fatalf("restarted request lost provider/model identity: calls=%d", len(fake.Calls))
			}
			body, err := provider.BuildAnthropicRequest(fake.Calls[0])
			if err != nil {
				t.Fatal(err)
			}
			messages := body["messages"].([]map[string]any)
			assistant := messages[1]["content"].([]any)
			thinkingBlock := assistant[0].(map[string]any)
			if thinkingBlock["type"] != "thinking" || thinkingBlock["thinking"] != "synthetic prior reasoning" {
				t.Fatalf("%s historical thinking was not replayed: %+v", tc.providerID, thinkingBlock)
			}
			if tc.allowEmpty {
				if thinkingBlock["signature"] != "" {
					t.Fatalf("%s did not use its allowEmptySignature compatibility: %+v", tc.providerID, thinkingBlock)
				}
			} else if thinkingBlock["signature"] != tc.signature {
				t.Fatalf("%s historical signature changed: %+v", tc.providerID, thinkingBlock)
			}
			toolCall := assistant[1].(map[string]any)
			if toolCall["id"] != "call_a" || messages[2]["content"].([]any)[0].(map[string]any)["tool_use_id"] != "call_a" {
				t.Fatalf("%s historical tool/result linkage changed: %+v", tc.providerID, messages)
			}
		})
	}
}
