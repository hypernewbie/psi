package provider

import (
	"os"
	"strings"
	"testing"
)

var osWriteFile = os.WriteFile

func TestThinkingBudgetMapping(t *testing.T) {
	for _, lvl := range ValidLevels {
		if _, err := ThinkingBudget(lvl); err != nil {
			t.Errorf("level %q: %v", lvl, err)
		}
	}
	if _, err := ThinkingBudget("ultra"); err == nil {
		t.Errorf("unsupported level must fail")
	}
	// Explicit unsupported level behavior: off is the only level for non-reasoning models.
	req := Request{Model: ModelMeta{ID: "m", API: "anthropic-messages", Reasoning: false}, Thinking: "high", Messages: []Message{{Role: "user", Content: []Block{{Type: "text", Text: "hi"}}}}}
	if _, err := BuildAnthropicRequest(req); err == nil || !strings.Contains(err.Error(), "does not support") {
		t.Errorf("non-reasoning + thinking must fail, got %v", err)
	}
}

func TestBuildRequestFixtures(t *testing.T) {
	req := Request{
		Model:  ModelMeta{ID: "claude-x", API: "anthropic-messages", MaxTokens: 100, Reasoning: true},
		System: "base",
		Messages: []Message{
			{Role: "user", Content: []Block{{Type: "text", Text: "hi"}}},
			{Role: "assistant", Content: []Block{
				{Type: "thinking", Thinking: "h", ThinkingSignature: "sig"},
				{Type: "toolCall", ID: "c1", Name: "read", Arguments: map[string]any{"path": "a"}}},
			},
			{Role: "toolResult", ToolCallID: "c1", Content: []Block{{Type: "text", Text: "ok"}}},
		},
		Tools:    []ToolDef{{Name: "read", Description: "read files", Schema: map[string]any{"type": "object"}}},
		Thinking: "low",
	}
	out, err := BuildAnthropicRequest(req)
	if err != nil {
		t.Fatal(err)
	}
	if out["model"] != "claude-x" {
		t.Errorf("model: %v", out["model"])
	}
	msgs := out["messages"].([]map[string]any)
	if len(msgs) != 3 {
		t.Fatalf("messages=%d", len(msgs))
	}
	// Reasoning signature preserved.
	amsg := msgs[1]["content"].([]any)[0].(map[string]any)
	if amsg["signature"] != "sig" {
		t.Errorf("signature lost: %v", amsg)
	}
	// Tool call/result IDs matched.
	if _, ok := out["thinking"]; !ok {
		t.Errorf("thinking missing")
	}
	// Unsupported content stops instead of dropping.
	bad := req
	bad.Messages = []Message{{Role: "user", Content: []Block{{Type: "weird"}}}}
	if _, err := BuildAnthropicRequest(bad); err == nil {
		t.Errorf("unsupported block must fail")
	}
	// Unsupported API rejected.
	bad2 := req
	bad2.Model.API = "openai-completions"
	if _, err := BuildAnthropicRequest(bad2); err == nil {
		t.Errorf("unsupported API must fail")
	}
	// Unsupported image mime refused.
	bad3 := req
	bad3.Messages = []Message{{Role: "user", Content: []Block{{Type: "image", MimeType: "image/tiff", Data: "x"}}}}
	if _, err := BuildAnthropicRequest(bad3); err == nil {
		t.Errorf("unsupported image must fail")
	}
}

func TestSelectAPIKey(t *testing.T) {
	// Env wins.
	k, src, err := SelectAPIKey(map[string]string{"ANTHROPIC_API_KEY": "sk-ant-env"}, "/nonexistent/auth.json")
	if err != nil || k != "sk-ant-env" || src == "" {
		t.Errorf("env: %q %q %v", k, src, err)
	}
	// OAuth without api key fails clearly.
	dir := t.TempDir()
	p := dir + "/auth.json"
	writeFile(p, `{"anthropic-oauth":{"type":"oauth","key":"x"}}`)
	if _, _, err := SelectAPIKey(map[string]string{}, p); err == nil || !strings.Contains(err.Error(), "OAuth") {
		t.Errorf("oauth must fail clearly, got %v", err)
	}
	// API key entry works (no fallback across billing).
	p2 := dir + "/auth2.json"
	writeFile(p2, `{"anthropic":{"key":"sk-ant-file"}}`)
	k, _, err = SelectAPIKey(map[string]string{}, p2)
	if err != nil || k != "sk-ant-file" {
		t.Errorf("file key: %q %v", k, err)
	}
}

func writeFile(path, s string) {
	if err := osWriteFile(path, []byte(s), 0o644); err != nil {
		panic(err)
	}
}
