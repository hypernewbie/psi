package provider

import "testing"

func TestManagedDefaultEffortNeverClaimsDisabledThinking(t *testing.T) {
	model := ModelMeta{ID: "synthetic", Provider: "anthropic", API: "anthropic-messages", Reasoning: true, AdaptiveThinking: true, MidConvoEffort: true, MaxTokens: 64000, ThinkingLevelMap: map[string]*string{"off": nil}}
	body, err := BuildRequest(Request{Model: model, Thinking: "off", Messages: []Message{{Role: "assistant", Provider: "anthropic", API: "anthropic-messages", ThinkingLevel: "minimal", Content: []Block{{Type: "text", Text: "old budget-model reply"}}}, {Role: "user", Content: []Block{{Type: "text", Text: "continue"}}}}})
	if err != nil {
		t.Fatal(err)
	}
	if body["thinking"].(map[string]any)["type"] != "adaptive" {
		t.Fatal("model without an off switch received disabled thinking")
	}
	messages := body["messages"].([]map[string]any)
	if len(messages) != 3 || messages[2]["output_config"].(map[string]any)["effort"] != "high" {
		t.Fatal("invalid historical effort or missing default effort marker")
	}
	if description := ThinkingDescription(model, "off"); description == "off" {
		t.Fatal("provider default was described as disabled")
	}
}
