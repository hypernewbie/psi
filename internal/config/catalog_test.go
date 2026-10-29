package config

import (
	"encoding/json"
	"testing"

	"psi/internal/modelmeta"
	"psi/internal/providerregistry"
)

// P2-069,070,071,075: catalog snapshot, request construction, thinking, accounting.
// Reference: temp/pi-mono model catalog + provider BuildRequest.
func TestP6BundledCatalogUsesPiProviderAPIsAndMetadata(t *testing.T) {
	for _, providerID := range []string{"minimax", "opencode-go", "deepseek", "zai", "google"} {
		models := BundledModels(providerID)
		if len(models) == 0 {
			t.Fatalf("bundled Pi catalog for %q is empty", providerID)
		}
		for _, model := range models {
			if model.ID == "" || model.API == "" || model.BaseURL == "" || !providerregistry.SupportsAPI(providerID, model.API) {
				t.Errorf("invalid bundled model %s/%s: %+v", providerID, model.ID, model)
			}
		}
	}
	apis := map[string]bool{}
	for _, model := range BundledModels("opencode-go") {
		apis[model.API] = true
		if !providerregistry.TrustedCachedEndpoint("opencode-go", model.API, model.BaseURL) {
			t.Errorf("unreviewed pinned OpenCode endpoint %s for %s", model.BaseURL, model.API)
		}
	}
	for _, api := range []string{"anthropic-messages", "google-generative-ai", "openai-completions", "openai-responses"} {
		if !apis[api] {
			t.Errorf("Pi OpenCode catalog omitted API %q", api)
		}
	}
	deepseek, ok := bundledModel("deepseek", "deepseek-v4-pro")
	if !ok {
		t.Fatal("public DeepSeek model was not bundled")
	}
	thinkingFormat, hasValue, present := deepseek.Compat.String("thinkingFormat")
	if !present || !hasValue || thinkingFormat != "deepseek" {
		t.Fatalf("DeepSeek thinking format = %q,%t,%t", thinkingFormat, hasValue, present)
	}
	zai, ok := bundledModel("zai", "glm-4.7")
	if !ok {
		t.Fatal("public Z.AI model was not bundled")
	}
	if enabled, hasValue, present := zai.Compat.Bool("zaiToolStream"); !present || !hasValue || !enabled {
		t.Fatalf("Z.AI tool stream compatibility lost: %t,%t,%t", enabled, hasValue, present)
	}
}

func TestModelAPIEndpointAndCompatPrecedence(t *testing.T) {
	models := &ModelsFile{Providers: map[string]ProviderMeta{
		"opencode-go": {
			API: "anthropic-messages", BaseURL: "https://provider.example/base",
			Compat:         modelmeta.Compat{"supportsStore": json.RawMessage("true"), "supportsStrictMode": json.RawMessage("true")},
			Models:         []ModelMeta{{ID: "custom", API: "google-generative-ai", BaseURL: "https://model.example/google", ContextWindow: 8000, MaxTokens: 1000, Input: []string{"text"}}},
			ModelOverrides: map[string]json.RawMessage{"custom": json.RawMessage(`{"api":"openai-responses","baseUrl":"https://override.example/responses","compat":{"supportsStore":false,"supportsStrictMode":null}}`)},
		},
	}}
	model, err := models.SelectModel("opencode-go", "custom")
	if err != nil {
		t.Fatal(err)
	}
	if model.API != "openai-responses" || model.BaseURL != "https://override.example/responses" {
		t.Fatalf("model override lost precedence: %+v", model)
	}
	if value, hasValue, present := model.Compat.Bool("supportsStore"); !present || !hasValue || value {
		t.Fatalf("explicit false was lost: %t,%t,%t", value, hasValue, present)
	}
	if _, hasValue, present := model.Compat.Bool("supportsStrictMode"); hasValue || !present || !model.Compat.IsNull("supportsStrictMode") {
		t.Fatalf("explicit null was lost: hasValue=%t present=%t", hasValue, present)
	}
}

func TestCatalogRequestPaths(t *testing.T) {
	mf := &ModelsFile{}
	// Bundled Anthropic models must resolve.
	for _, id := range []string{"claude-sonnet-4-5", "claude-haiku-4-5", "claude-opus-4-1"} {
		m, err := mf.SelectModel("anthropic", id)
		if err != nil {
			t.Fatalf("bundled anthropic %s: %v", id, err)
		}
		if m.API != "anthropic-messages" {
			t.Fatalf("%s api %q", id, m.API)
		}
		if !m.Reasoning {
			t.Fatalf("%s should support reasoning", id)
		}
		if m.MaxTokens <= 0 || m.ContextWindow <= 0 {
			t.Fatalf("%s missing window: %+v", id, m)
		}
	}
	// Unknown model needs explicit metadata (no silent default).
	if _, err := mf.SelectModel("anthropic", "unknown-xyz"); err == nil {
		t.Fatal("unknown model should require metadata")
	}
	// Unsupported provider rejected without changing defaults.
	if _, err := mf.SelectModel("openai", "x"); err == nil {
		t.Fatal("unsupported provider should fail")
	}
	if len(ValidThinkingLevels) != 7 {
		t.Fatalf("thinking levels: %v", ValidThinkingLevels)
	}
	// Codex thinking map preserved (no lossy conversion).
	m, err := mf.SelectModel("openai-codex", "")
	if err != nil {
		t.Fatalf("codex default: %v", err)
	}
	if m.API != "openai-codex-responses" {
		t.Fatalf("codex api %q", m.API)
	}
}
