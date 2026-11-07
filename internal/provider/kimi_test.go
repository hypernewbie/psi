package provider

import (
	"testing"

	"psi/internal/config"
)

// Kimi For Coding uses the Chat Completions protocol with the metadata of the
// bundled public catalog: the mapped effort levels, the max_tokens field, and
// the reasoning content on assistant messages.
func TestKimiCodingRequestUsesCatalogMetadata(t *testing.T) {
	var model ModelMeta
	for _, candidate := range config.BundledModels("kimi-coding") {
		if candidate.ID == "k3" {
			model = ModelMeta{ID: candidate.ID, API: candidate.API, Provider: "kimi-coding", BaseURL: candidate.BaseURL,
				Reasoning: candidate.Reasoning, MaxTokens: candidate.MaxTokens, ThinkingLevelMap: candidate.ThinkingLevelMap, Compat: candidate.Compat}
		}
	}
	if model.ID == "" {
		t.Fatal("the bundled catalog has no Kimi K3 model")
	}
	request, err := BuildRequest(Request{Model: model, Thinking: "high", System: "sys", Messages: []Message{
		{Role: "user", Content: []Block{{Type: "text", Text: "hi"}}},
		{Role: "assistant", Provider: "kimi-coding", API: model.API, Model: model.ID, Content: []Block{{Type: "toolCall", ID: "call-1", Name: "read", Arguments: map[string]any{}}}},
		{Role: "toolResult", ToolCallID: "call-1", ToolName: "read", Content: []Block{{Type: "text", Text: "done"}}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if request["reasoning_effort"] != "high" {
		t.Fatalf("Kimi request lost the mapped effort: %#v", request)
	}
	if _, present := request["max_completion_tokens"]; present {
		t.Fatalf("Kimi request used the wrong max tokens field: %#v", request)
	}
	if request["max_tokens"] != model.MaxTokens {
		t.Fatalf("Kimi request max tokens = %#v", request["max_tokens"])
	}
	messages, _ := request["messages"].([]any)
	found := false
	for _, raw := range messages {
		message, ok := raw.(map[string]any)
		if !ok || message["role"] != "assistant" {
			continue
		}
		if _, present := message["reasoning_content"]; present {
			found = true
		}
	}
	if !found {
		t.Fatalf("Kimi request did not replay the reasoning content field: %#v", messages)
	}
}

// Kimi Code reports every model as always-thinking, so the thinking levels
// come from the catalog map.
func TestKimiCodingThinkingLevelsComeFromTheCatalog(t *testing.T) {
	model := ModelMeta{ID: "k3", API: "openai-completions", Provider: "kimi-coding", BaseURL: "https://api.kimi.com/coding/v1",
		Reasoning: true, MaxTokens: 1000, ThinkingLevelMap: map[string]*string{"off": nil, "low": stringPtr("low"), "high": stringPtr("high")}}
	levels := SupportedThinking(model)
	if len(levels) == 0 {
		t.Fatal("a Kimi model advertises no thinking level")
	}
	for _, level := range levels {
		if level == "off" {
			t.Fatal("a Kimi model advertised the disabled thinking level")
		}
	}
}

func stringPtr(value string) *string { return &value }
