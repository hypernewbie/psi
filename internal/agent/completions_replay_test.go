package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"psi/internal/modelmeta"
	"psi/internal/provider"
	"psi/internal/session"
	"psi/internal/tools"
)

func TestDeepSeekAndOpenCodeCompletionsReplayAfterRestart(t *testing.T) {
	for _, tc := range []struct {
		providerID, modelID, reasoningField string
	}{
		{"deepseek", "synthetic-deepseek", "reasoning_content"},
		{"opencode-go", "synthetic-opencode", "reasoning"},
	} {
		t.Run(tc.providerID, func(t *testing.T) {
			cwd := t.TempDir()
			store, err := session.New(session.OpenOptions{Cwd: cwd, SessionDir: t.TempDir(), Persist: true})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := store.AppendMessage(session.AgentMessage{Role: "user", User: &session.UserMessage{Role: "user", Content: "synthetic prior request", Timestamp: 1}}); err != nil {
				t.Fatal(err)
			}
			longToolID := "call_1234567890123456789012345678901234567890|fc_abcdef123456789"
			reasoning := &session.ThinkingContent{Type: "thinking", Thinking: "synthetic prior reasoning", ThinkingSignature: &tc.reasoningField}
			call := &session.ToolCall{Type: "toolCall", ID: longToolID, Name: "read", Arguments: map[string]any{"path": "a.txt"}}
			if _, err := store.AppendMessage(session.AgentMessage{Role: "assistant", Assistant: &session.AssistantMessage{
				Role: "assistant", API: "openai-completions", Provider: tc.providerID, Model: tc.modelID,
				Content: []session.ContentBlock{{Type: "thinking", Thinking: reasoning}, {Type: "toolCall", Call: call}}, StopReason: "toolUse", Timestamp: 2,
			}}); err != nil {
				t.Fatal(err)
			}
			if _, err := store.AppendMessage(session.AgentMessage{Role: "toolResult", ToolResult: &session.ToolResultMessage{
				Role: "toolResult", ToolCallID: longToolID, ToolName: "read", Content: []session.UserContentBlock{{Type: "text", Text: &session.TextContent{Type: "text", Text: "synthetic file content"}}}, Timestamp: 3,
			}}); err != nil {
				t.Fatal(err)
			}
			reopened, err := session.OpenFile(store.File(), cwd, false)
			if err != nil {
				t.Fatal(err)
			}
			fake := &provider.Fake{Responses: []*provider.Response{{Text: "synthetic continued answer", StopReason: "stop"}}}
			loop := New(reopened, tools.NewRegistry(cwd), fake, nil)
			compatJSON, _ := json.Marshal(map[string]any{"maxTokensField": "max_tokens", "requiresReasoningContentOnAssistantMessages": tc.providerID == "deepseek", "thinkingFormat": map[string]string{"deepseek": "deepseek", "opencode-go": "openai"}[tc.providerID]})
			var compat modelmeta.Compat
			if err := json.Unmarshal(compatJSON, &compat); err != nil {
				t.Fatal(err)
			}
			model := provider.ModelMeta{ID: tc.modelID, API: "openai-completions", Provider: tc.providerID, MaxTokens: 8192, Reasoning: true, Compat: compat}
			cfg := Config{Model: model, Thinking: "off", Tools: toolDefs(), MaxTurns: 2, System: "synthetic system"}
			if err := loop.Submit(context.Background(), "synthetic continuation", cfg); err != nil {
				t.Fatal(err)
			}
			if len(fake.Calls) != 1 || fake.Calls[0].Model.Provider != tc.providerID || fake.Calls[0].Model.ID != tc.modelID {
				t.Fatalf("restarted request lost provider/model identity: calls=%d", len(fake.Calls))
			}
			body, err := provider.BuildCompletionsRequest(fake.Calls[0])
			if err != nil {
				t.Fatal(err)
			}
			messages := body["messages"].([]any)
			var replay map[string]any
			for _, raw := range messages {
				candidate := raw.(map[string]any)
				if candidate["role"] == "assistant" && candidate["tool_calls"] != nil {
					replay = candidate
					break
				}
			}
			if replay == nil || replay["reasoning_content"] != "synthetic prior reasoning" {
				t.Fatalf("%s reasoning replay changed: %#v", tc.providerID, messages)
			}
			calls := replay["tool_calls"].([]any)
			callID := calls[0].(map[string]any)["id"].(string)
			if len(callID) > 40 || callID == longToolID {
				t.Fatalf("cross-API call ID not normalized: %q", callID)
			}
			var linked bool
			for _, raw := range messages {
				candidate := raw.(map[string]any)
				if candidate["role"] == "tool" && candidate["tool_call_id"] == callID && strings.Contains(candidate["content"].(string), "synthetic file content") {
					linked = true
				}
			}
			if !linked {
				t.Fatalf("tool result lost normalized call link: %#v", messages)
			}
		})
	}
}
