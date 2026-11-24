package config

import (
	"encoding/json"
	"testing"

	"github.com/hypernewbie/psi/internal/modelmeta"
	"github.com/hypernewbie/psi/internal/providerregistry"
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
	minimax, ok := bundledModel("minimax", "MiniMax-M3.1-Flash")
	if !ok {
		t.Fatal("public MiniMax model was not bundled")
	}
	adaptive, hasAdaptive, hasAdaptiveField := minimax.Compat.Bool("forceAdaptiveThinking")
	offMapping, hasOffMapping := minimax.ThinkingLevelMap["off"]
	if !hasAdaptiveField || !hasAdaptive || !adaptive || !hasOffMapping || offMapping != nil {
		t.Fatalf("MiniMax adaptive metadata lost: %+v", minimax)
	}
	openCode, ok := bundledModel("opencode-go", "qwen3.8-flash")
	if !ok {
		t.Fatal("public OpenCode Anthropic model was not bundled")
	}
	allowEmpty, hasAllowEmpty, hasAllowEmptyField := openCode.Compat.Bool("allowEmptySignature")
	if !hasAllowEmptyField || !hasAllowEmpty || !allowEmpty {
		t.Fatalf("OpenCode allowEmptySignature metadata lost: %+v", openCode)
	}
	missingCompat, ok := bundledModel("opencode-go", "claude-haiku-5-5")
	if !ok {
		t.Fatal("public OpenCode adaptive model was not bundled")
	}
	if _, hasValue, present := missingCompat.Compat.Bool("allowEmptySignature"); hasValue || present {
		t.Fatal("an absent allowEmptySignature field was converted to a value")
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

func TestEveryBuiltinAPIKeyProviderHasABundledDefault(t *testing.T) {
	for _, providerID := range providerregistry.IDs() {
		spec, ok := providerregistry.Lookup(providerID)
		if !ok || spec.Auth != providerregistry.AuthAPIKey {
			continue
		}
		model, err := (&ModelsFile{}).SelectModel(providerID, "")
		if err != nil {
			t.Errorf("%s has no default model: %v", providerID, err)
			continue
		}
		if _, ok := bundledModel(providerID, model.ID); !ok {
			t.Errorf("%s default model %q is not in its bundled catalog", providerID, model.ID)
		}
	}
}

func TestXiaomiTokenPlanRegionalProvidersResolveLikePi(t *testing.T) {
	mf := &ModelsFile{Providers: map[string]ProviderMeta{}}
	for providerID, endpoint := range map[string]string{
		"xiaomi-token-plan-cn":  "https://token-plan-cn.xiaomimimo.com/v1",
		"xiaomi-token-plan-ams": "https://token-plan-ams.xiaomimimo.com/v1",
		"xiaomi-token-plan-sgp": "https://token-plan-sgp.xiaomimimo.com/v1",
	} {
		m, err := mf.SelectModel(providerID, "")
		if err != nil {
			t.Fatalf("%s default model: %v", providerID, err)
		}
		if m.ID != "mimo-v2.6-pro" || m.API != "openai-completions" || m.BaseURL != endpoint {
			t.Fatalf("%s resolved unexpected model: %+v", providerID, m)
		}
		format, hasValue, present := m.Compat.String("thinkingFormat")
		if !present || !hasValue || format != "deepseek" {
			t.Fatalf("%s lost the Xiaomi thinking format: %q,%t,%t", providerID, format, hasValue, present)
		}
	}
	ids := map[string]bool{}
	for _, id := range mf.ProviderIDs() {
		ids[id] = true
	}
	if !ids["xiaomi-token-plan-sgp"] {
		t.Fatalf("token-plan providers missing from the provider set: %v", mf.ProviderIDs())
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

func TestBundledOpenAIAndCodexContextLimits(t *testing.T) {
	// Direct OpenAI 1.05M models expose 922K max input. psi stores one
	// contextWindow value and uses it for threshold compaction, so the catalog
	// uses the input limit. The 272K long-context price threshold is not a
	// context limit.
	for _, test := range []struct {
		provider                 string
		id                       string
		contextWindow, maxTokens int
	}{
		{"openai", "gpt-5.4", 922000, 128000},
		{"openai", "gpt-5.4-pro", 922000, 128000},
		{"openai", "gpt-5.5", 922000, 128000},
		{"openai", "gpt-5.5-pro", 922000, 128000},
		{"openai", "gpt-5.6-luna", 922000, 128000},
		{"openai", "gpt-5.6-sol", 922000, 128000},
		{"openai", "gpt-5.6-terra", 922000, 128000},
		{"openai", "gpt-6-astra", 922000, 128000},
		{"openai", "gpt-6-cyber", 922000, 128000},
		{"openai", "gpt-6-sol", 922000, 128000},
		{"openai", "gpt-6-luna", 922000, 128000},
		{"openai", "gpt-6.1-sol", 922000, 128000},
		{"openai", "gpt-6.1-terra", 922000, 128000},
		{"openai", "gpt-6.2-astra", 922000, 128000},
		{"openai", "gpt-6.2-sol", 922000, 128000},
		{"openai", "gpt-daybreak-blue-latest", 922000, 128000},
		{"openai", "gpt-5-pro", 400000, 272000},
		// Codex uses the bundled defaults from openai/codex, not the API limits.
		{"openai-codex", "gpt-5.6-luna", 272000, 128000},
		{"openai-codex", "gpt-5.6-sol", 272000, 128000},
		{"openai-codex", "gpt-5.6-terra", 272000, 128000},
		{"openai-codex", "gpt-6-astra", 272000, 128000},
		{"openai-codex", "gpt-6-sol", 272000, 128000},
		{"openai-codex", "gpt-6-luna", 272000, 128000},
		{"openai-codex", "gpt-6.1-sol", 500000, 128000},
		{"openai-codex", "gpt-6.1-terra", 500000, 128000},
		{"openai-codex", "gpt-6.2-astra", 272000, 128000},
		{"openai-codex", "gpt-6.2-sol", 500000, 128000},
	} {
		model, ok := bundledModel(test.provider, test.id)
		if !ok {
			t.Fatalf("bundled model %s/%s is missing", test.provider, test.id)
		}
		if model.ContextWindow != test.contextWindow || model.MaxTokens != test.maxTokens {
			t.Errorf("%s/%s limits = %d/%d, want %d/%d", test.provider, test.id,
				model.ContextWindow, model.MaxTokens, test.contextWindow, test.maxTokens)
		}
	}
}

func TestBundledOpenCodeGoContextLimits(t *testing.T) {
	// OpenCode Go exposes full context windows but smaller max input limits on
	// these models. psi uses one contextWindow value for threshold compaction,
	// so the catalog uses the input limit and leaves room for the output budget.
	for _, test := range []struct {
		id                       string
		contextWindow, maxTokens int
	}{
		{"hy3", 192000, 128000},
		{"gpt-5.6-luna", 922000, 128000},
		{"gpt-6-luna", 922000, 128000},
	} {
		model, ok := bundledModel("opencode-go", test.id)
		if !ok {
			t.Fatalf("bundled model opencode-go/%s is missing", test.id)
		}
		if model.ContextWindow != test.contextWindow || model.MaxTokens != test.maxTokens {
			t.Errorf("opencode-go/%s limits = %d/%d, want %d/%d", test.id,
				model.ContextWindow, model.MaxTokens, test.contextWindow, test.maxTokens)
		}
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
