package provider

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestAnthropicConvertsForeignReplayWithoutMutatingStoredMetadata(t *testing.T) {
	const opaque = `{"type":"reasoning","id":"rs_foreign","encrypted_content":"SYNTHETIC_OPAQUE_REASONING"}`
	source := []Block{{Type: "thinking", Thinking: "readable historical reasoning", ThinkingSignature: opaque}, {Type: "toolCall", ID: "call_foreign|fc_foreign", Name: "read", Arguments: map[string]any{"path": "synthetic"}}}
	req := Request{Model: ModelMeta{ID: "claude-synthetic", API: "anthropic-messages", Provider: "anthropic", MaxTokens: 4096}, Messages: []Message{
		{Role: "assistant", Provider: "openai-codex", API: "openai-codex-responses", Model: "foreign-model", Content: source},
		{Role: "toolResult", ToolCallID: "call_foreign|fc_foreign", ToolName: "read", Content: []Block{{Type: "text", Text: "synthetic result"}}},
	}}
	body, err := BuildRequest(req)
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(body)
	if strings.Contains(string(encoded), "SYNTHETIC_OPAQUE_REASONING") || !strings.Contains(string(encoded), "readable historical reasoning") {
		t.Fatal("foreign opaque signature replayed or readable content lost")
	}
	messages := body["messages"].([]map[string]any)
	call := messages[0]["content"].([]any)[1].(map[string]any)
	result := messages[1]["content"].([]any)[0].(map[string]any)
	if call["id"] != result["tool_use_id"] || strings.Contains(call["id"].(string), "|") {
		t.Fatal("foreign call/result pairing broken")
	}
	if source[0].ThinkingSignature != opaque || source[1].ID != "call_foreign|fc_foreign" {
		t.Fatal("request translation mutated durable replay metadata")
	}
}

func TestAnthropicErrorImagesFollowTheCompleteToolResultBatch(t *testing.T) {
	body, err := BuildRequest(Request{Model: ModelMeta{ID: "synthetic", API: "anthropic-messages", MaxTokens: 4096, InputImage: true}, Messages: []Message{
		{Role: "toolResult", ToolCallID: "a", ToolName: "read", IsError: true, Content: []Block{{Type: "text", Text: "error"}, {Type: "image", MimeType: "image/png", Data: "c3ludGhldGlj"}}},
		{Role: "toolResult", ToolCallID: "b", ToolName: "read", Content: []Block{{Type: "text", Text: "ok"}}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	messages := body["messages"].([]map[string]any)
	if len(messages) != 1 {
		t.Fatal("tool results split into separate user messages")
	}
	content := messages[0]["content"].([]any)
	if len(content) != 4 || content[0].(map[string]any)["type"] != "tool_result" || content[1].(map[string]any)["type"] != "tool_result" || content[3].(map[string]any)["type"] != "image" {
		t.Fatalf("wrong result/image order: %+v", content)
	}
	inner := content[0].(map[string]any)["content"].([]any)
	if len(inner) != 1 || inner[0].(map[string]any)["type"] != "text" {
		t.Fatal("error tool result contains forbidden image content")
	}
}
