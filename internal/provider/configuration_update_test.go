package provider

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/hypernewbie/psi/internal/modelmeta"
)

// midConvoModel supports thinking-level changes as configuration_update items.
func midConvoModel(api, provider string) ModelMeta {
	return ModelMeta{
		ID: "gpt-6.2-test", Provider: provider, API: api, Reasoning: true, InputImage: true,
		ContextWindow: 272000, MaxTokens: 128000,
		ThinkingLevelMap: map[string]*string{
			"off":     nil,
			"minimal": nil,
			"low":     responsesString("low"),
			"medium":  responsesString("medium"),
			"high":    responsesString("high"),
			"xhigh":   responsesString("xhigh"),
			"max":     responsesString("max"),
		},
		Compat: modelmeta.Compat{
			"supportsMidConvoEffort": json.RawMessage(`true`),
		},
	}
}

func configurationUpdates(input []any) []string {
	var efforts []string
	for _, item := range input {
		fields, ok := item.(map[string]any)
		if !ok || fields["type"] != "configuration_update" {
			continue
		}
		reasoning := fields["reasoning"].(map[string]any)
		efforts = append(efforts, reasoning["effort"].(string))
	}
	return efforts
}

func TestResponsesThinkingChangesUseConfigurationUpdates(t *testing.T) {
	model := midConvoModel("openai-responses", "openai")
	req := Request{Model: model, Thinking: "high", System: "synthetic instructions", Messages: []Message{
		{Role: "user", Content: []Block{{Type: "text", Text: "go"}}},
		{Role: "assistant", Provider: "openai", API: "openai-responses", Model: model.ID, ThinkingLevel: "low", Content: []Block{{Type: "text", Text: "first"}}},
		{Role: "user", Content: []Block{{Type: "text", Text: "more"}}},
	}}
	body, err := BuildResponsesRequest(req)
	if err != nil {
		t.Fatal(err)
	}
	reasoning := body["reasoning"].(map[string]any)
	if reasoning["effort"] != "medium" {
		t.Fatalf("request effort = %v, want the fixed configuration effort", reasoning["effort"])
	}
	got := configurationUpdates(body["input"].([]any))
	if len(got) != 2 || got[0] != "low" || got[1] != "high" {
		t.Fatalf("configuration updates = %v, want the replayed and current levels", got)
	}

	// Without the capability the request keeps a single reasoning effort.
	model.Compat = modelmeta.Compat{}
	req.Model = model
	body, err = BuildResponsesRequest(req)
	if err != nil {
		t.Fatal(err)
	}
	if body["reasoning"].(map[string]any)["effort"] != "high" {
		t.Fatalf("plain request effort = %v", body["reasoning"])
	}
	if got := configurationUpdates(body["input"].([]any)); len(got) != 0 {
		t.Fatalf("configuration updates without the capability: %v", got)
	}
}

func TestCodexThinkingChangesUseConfigurationUpdates(t *testing.T) {
	model := midConvoModel("openai-codex-responses", "openai-codex")
	req := Request{Model: model, Thinking: "high", System: "synthetic instructions", Messages: []Message{
		{Role: "user", Content: []Block{{Type: "text", Text: "go"}}},
		{Role: "assistant", Provider: "openai-codex", API: "openai-codex-responses", Model: model.ID, ThinkingLevel: "low", Content: []Block{{Type: "text", Text: "first"}}},
		{Role: "user", Content: []Block{{Type: "text", Text: "more"}}},
	}}
	body, err := BuildCodexRequest(req)
	if err != nil {
		t.Fatal(err)
	}
	input := body["input"].([]any)
	system := input[0].(map[string]any)
	if system["role"] != "developer" || system["content"] != "synthetic instructions" {
		t.Fatalf("first input item is not the system prompt: %#v", system)
	}
	if _, exists := body["instructions"]; exists {
		t.Fatal("Codex still sends top-level instructions")
	}
	if reasoning, _ := body["reasoning"].(map[string]any); reasoning["effort"] != "medium" {
		t.Fatalf("Codex request effort = %v, want the fixed configuration effort", body["reasoning"])
	}
	if body["max_output_tokens"] != 128000 {
		t.Fatalf("Codex max_output_tokens = %#v", body["max_output_tokens"])
	}
	got := configurationUpdates(input)
	if len(got) != 2 || got[0] != "low" || got[1] != "high" {
		t.Fatalf("configuration updates = %v", got)
	}
	// A level recorded for a different model is not replayed here.
	req.Messages[1].Model = "other-model"
	body, err = BuildCodexRequest(req)
	if err != nil {
		t.Fatal(err)
	}
	if got := configurationUpdates(body["input"].([]any)); len(got) != 1 || got[0] != "high" {
		t.Fatalf("foreign level replay = %v", got)
	}
}

func TestResponsesThinkingLevelMatchesTheRequest(t *testing.T) {
	model := midConvoModel("openai-responses", "openai")
	// off is unavailable here, so the request clamps to the lowest level; the
	// recorded effort must be the same value the request sends.
	if got := responsesThinkingLevel(Request{Model: model, Thinking: "off"}); got != "low" {
		t.Fatalf("recorded off effort = %q, want the clamped level", got)
	}
	model.ThinkingLevelMap = map[string]*string{"high": responsesString("high")}
	if got := responsesThinkingLevel(Request{Model: model, Thinking: "off"}); got != "none" {
		t.Fatalf("recorded off effort = %q, want none", got)
	}
	if got := responsesThinkingLevel(Request{Model: model, Thinking: "high"}); got != "high" {
		t.Fatalf("recorded high effort = %q", got)
	}
}

func TestResponsesEffortRejectsUnsupportedLevels(t *testing.T) {
	model := midConvoModel("openai-responses", "openai")
	if effort, ok, err := responsesEffort(model, "off"); err != nil || ok {
		t.Fatalf("off = %q ok=%v err=%v, want no reasoning field", effort, ok, err)
	}
	// Levels the model does not advertise clamp to the nearest supported one.
	if effort, ok, err := responsesEffort(model, "minimal"); err != nil || !ok || effort != "low" {
		t.Fatalf("minimal = %q ok=%v err=%v", effort, ok, err)
	}
	if _, _, err := responsesEffort(model, "bogus"); err == nil || !strings.Contains(err.Error(), "thinking level") {
		t.Fatalf("bogus error = %v", err)
	}
	model.ThinkingLevelMap = map[string]*string{"high": responsesString("high")}
	if effort, ok, err := responsesEffort(model, "high"); err != nil || !ok || effort != "high" {
		t.Fatalf("high = %q ok=%v err=%v", effort, ok, err)
	}
}
