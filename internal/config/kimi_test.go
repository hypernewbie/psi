package config

import (
	"strings"
	"testing"

	"psi/internal/providerregistry"
)

// Kimi For Coding ships with the bundled public catalog and uses the Chat
// Completions protocol at Pi's endpoint.
func TestKimiCodingCatalogMatchesPiMetadata(t *testing.T) {
	spec, ok := providerregistry.Lookup("kimi-coding")
	if !ok || spec.Environment != "KIMI_API_KEY" || spec.DefaultAPI != "openai-completions" {
		t.Fatalf("kimi-coding registry entry = %+v", spec)
	}
	if endpoint := providerregistry.DefaultEndpoint("kimi-coding", "openai-completions"); endpoint != "https://api.kimi.com/coding/v1" {
		t.Fatalf("kimi-coding endpoint = %q", endpoint)
	}
	models := BundledModels("kimi-coding")
	if len(models) == 0 {
		t.Fatal("the bundled catalog has no Kimi For Coding model")
	}
	for _, model := range models {
		if model.API != "openai-completions" || model.BaseURL != "https://api.kimi.com/coding/v1" {
			t.Fatalf("Kimi model %s = %+v", model.ID, model)
		}
		if !model.Reasoning || model.ContextWindow <= 0 || model.MaxTokens <= 0 {
			t.Fatalf("Kimi model %s lost its metadata: %+v", model.ID, model)
		}
		// Kimi Code reports every model as always-thinking.
		if off, exists := model.ThinkingLevelMap["off"]; !exists || off != nil {
			t.Fatalf("Kimi model %s does not disable thinking: %+v", model.ID, model.ThinkingLevelMap)
		}
	}
	model, err := (&ModelsFile{}).SelectModel("kimi-coding", "k3")
	if err != nil {
		t.Fatal(err)
	}
	if model.API != "openai-completions" || model.BaseURL != "https://api.kimi.com/coding/v1" {
		t.Fatalf("Kimi model selection = %+v", model)
	}
	effort, hasEffort, present := model.Compat.Bool("supportsReasoningEffort")
	if !present || !hasEffort || !effort {
		t.Fatalf("Kimi reasoning effort metadata lost: %t %t %t", effort, hasEffort, present)
	}
	format, hasFormat, present := model.Compat.String("thinkingFormat")
	if !present || !hasFormat || format != "openai" {
		t.Fatalf("Kimi thinking format = %q %t %t", format, hasFormat, present)
	}
}

// Kimi For Coding needs an explicit model, the way the other providers
// without a catalog default do.
func TestKimiCodingNeedsAnExplicitModel(t *testing.T) {
	if _, err := (&ModelsFile{}).SelectModel("kimi-coding", ""); err == nil || !strings.Contains(err.Error(), "explicit model") {
		t.Fatalf("an empty Kimi model was accepted: %v", err)
	}
}
