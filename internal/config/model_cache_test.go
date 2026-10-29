package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPiCatalogCacheSelectionAndPrecedence(t *testing.T) {
	dir := t.TempDir()
	cache := `{"unrelated-provider":{"models":"incompatible"},"openai-codex":{"lastModified":4102444800000,"models":[
		{"id":"malformed-model","contextWindow":"not a number"},
		{"id":"synthetic-new","provider":"openai-codex","api":"openai-codex-responses","type":"chat","contextWindow":500000,"maxTokens":128000,"reasoning":true,"input":["text","image"],"thinkingLevelMap":{"off":"none","xhigh":"xhigh"},"baseUrl":"https://untrusted.invalid","headers":{"Secret":"bad"}},
		{"id":"gpt-5.6-luna","provider":"openai-codex","api":"openai-codex-responses","contextWindow":333333,"maxTokens":12345},
		{"id":"wrong-provider","provider":"anthropic","api":"openai-codex-responses","contextWindow":1,"maxTokens":1},
		{"id":"wrong-api","provider":"openai-codex","api":"openai-responses","contextWindow":1,"maxTokens":1},
		{"id":"image-model","provider":"openai-codex","api":"openai-codex-responses","type":"image","contextWindow":1,"maxTokens":1}
	]}}`
	if err := os.WriteFile(filepath.Join(dir, "models-store.json"), []byte(cache), 0600); err != nil {
		t.Fatal(err)
	}
	mf, err := LoadModels(filepath.Join(dir, "models.json"))
	if err != nil {
		t.Fatal(err)
	}
	m, err := mf.SelectModel("openai-codex", "synthetic-new")
	if err != nil || m.ContextWindow != 500000 || m.MaxTokens != 128000 || !m.Reasoning || !m.InputImage || m.ThinkingLevelMap["off"] == nil || *m.ThinkingLevelMap["off"] != "none" || m.BaseURL != "https://chatgpt.com/backend-api" || len(m.Headers) != 0 {
		t.Fatalf("cached model: %+v, %v", m, err)
	}
	m, err = mf.SelectModel("openai-codex", "gpt-5.6-luna")
	if err != nil || m.ContextWindow != 333333 {
		t.Fatalf("overlay did not replace bundle: %+v %v", m, err)
	}
	for _, id := range []string{"wrong-provider", "wrong-api", "image-model"} {
		if _, err := mf.SelectModel("openai-codex", id); err == nil {
			t.Fatalf("accepted %s", id)
		}
	}
	found := false
	for _, candidate := range mf.LocalModels("openai-codex") {
		if candidate.ID == "synthetic-new" {
			found = true
		}
	}
	if !found {
		t.Fatal("model picker omitted cached model")
	}
	mf.Providers = map[string]ProviderMeta{"openai-codex": {Models: []ModelMeta{{ID: "synthetic-new", ContextWindow: 42, MaxTokens: 20}}}}
	m, err = mf.SelectModel("openai-codex", "synthetic-new")
	if err != nil || m.ContextWindow != 42 {
		t.Fatalf("explicit model did not win: %+v %v", m, err)
	}
}

func TestPiModelCacheSupportsRequestedProvidersAndTrustsOnlyOpenCodePaths(t *testing.T) {
	dir := t.TempDir()
	cache := `{
		"minimax":{"lastModified":4102444800000,"models":[{"id":"cached-m","provider":"minimax","type":"chat","api":"anthropic-messages","contextWindow":200000,"maxTokens":16000,"reasoning":true,"input":["text"],"baseUrl":"https://attacker.invalid/anthropic","apiKey":"cache-secret","headers":{"x-cache-secret":"cache-secret"},"compat":{"forceAdaptiveThinking":false,"supportsTemperature":null}}]},
		"opencode-go":{"lastModified":4102444800000,"models":[
			{"id":"oc-anthropic","provider":"opencode-go","type":"chat","api":"anthropic-messages","baseUrl":"https://opencode.ai/inference/go/anthropic","contextWindow":100000,"maxTokens":1000},
			{"id":"oc-google","provider":"opencode-go","type":"chat","api":"google-generative-ai","baseUrl":"https://opencode.ai/inference/go/google/v1beta","contextWindow":100000,"maxTokens":1000},
			{"id":"oc-completions","provider":"opencode-go","type":"chat","api":"openai-completions","baseUrl":"https://opencode.ai/inference/go/openai/v1","contextWindow":100000,"maxTokens":1000},
			{"id":"oc-responses","provider":"opencode-go","type":"chat","api":"openai-responses","baseUrl":"https://opencode.ai/inference/go/v1","contextWindow":100000,"maxTokens":1000},
			{"id":"oc-bad-path","provider":"opencode-go","type":"chat","api":"openai-responses","baseUrl":"https://opencode.ai/inference/go/v1/next","contextWindow":100000,"maxTokens":1000},
			{"id":"oc-bad-api","provider":"opencode-go","type":"chat","api":"openai-codex-responses","baseUrl":"https://opencode.ai/inference/go/v1","contextWindow":100000,"maxTokens":1000}
		]},
		"deepseek":{"lastModified":4102444800000,"models":[{"id":"cached-d","provider":"deepseek","type":"chat","api":"openai-completions","contextWindow":200000,"maxTokens":8000}]},
		"zai":{"lastModified":4102444800000,"models":[{"id":"cached-z","provider":"zai","type":"chat","api":"openai-completions","contextWindow":200000,"maxTokens":8000},{"id":"z-bad-api","provider":"zai","type":"chat","api":"google-generative-ai","contextWindow":200000,"maxTokens":8000}]},
		"google":{"lastModified":4102444800000,"models":[{"id":"cached-g","provider":"google","type":"chat","api":"google-generative-ai","contextWindow":200000,"maxTokens":8000}]}
	}`
	if err := os.WriteFile(filepath.Join(dir, "models-store.json"), []byte(cache), 0600); err != nil {
		t.Fatal(err)
	}
	mf, err := LoadModels(filepath.Join(dir, "models.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ provider, id, api string }{
		{"minimax", "cached-m", "anthropic-messages"},
		{"opencode-go", "oc-anthropic", "anthropic-messages"},
		{"opencode-go", "oc-google", "google-generative-ai"},
		{"opencode-go", "oc-completions", "openai-completions"},
		{"opencode-go", "oc-responses", "openai-responses"},
		{"deepseek", "cached-d", "openai-completions"},
		{"zai", "cached-z", "openai-completions"},
		{"google", "cached-g", "google-generative-ai"},
	} {
		model, err := mf.SelectModel(tc.provider, tc.id)
		if err != nil || model.API != tc.api || model.ContextWindow != 200000 && tc.provider != "opencode-go" {
			t.Fatalf("cached %s/%s = %+v, %v", tc.provider, tc.id, model, err)
		}
	}
	minimax, err := mf.SelectModel("minimax", "cached-m")
	if err != nil {
		t.Fatal(err)
	}
	if minimax.BaseURL != "https://api.minimax.io/anthropic" || minimax.APIKey != "" || minimax.Headers["x-cache-secret"] != "" {
		t.Fatalf("untrusted cached request metadata was retained: %+v", minimax)
	}
	if value, hasValue, present := minimax.Compat.Bool("forceAdaptiveThinking"); value || !hasValue || !present {
		t.Fatalf("cached explicit false lost: %t %t %t", value, hasValue, present)
	}
	if _, hasValue, present := minimax.Compat.Bool("supportsTemperature"); hasValue || !present || !minimax.Compat.IsNull("supportsTemperature") {
		t.Fatalf("cached explicit null lost: %t %t", hasValue, present)
	}
	for _, id := range []string{"oc-bad-path", "oc-bad-api"} {
		if _, err := mf.SelectModel("opencode-go", id); err == nil || !strings.Contains(err.Error(), "unavailable") {
			t.Fatalf("unsafe cached model %q was not unavailable: %v", id, err)
		}
	}
	explicitProvider := &ModelsFile{Providers: map[string]ProviderMeta{"opencode-go": {BaseURL: "http://127.0.0.1:8123/custom"}}, cachedModels: mf.cachedModels}
	if model, err := explicitProvider.SelectModel("opencode-go", "oc-bad-path"); err != nil || model.BaseURL != "http://127.0.0.1:8123/custom" {
		t.Fatalf("explicit provider endpoint did not replace rejected cache URL: %+v %v", model, err)
	}
	explicitModel := &ModelsFile{Providers: map[string]ProviderMeta{"opencode-go": {ModelOverrides: map[string]json.RawMessage{"oc-bad-path": json.RawMessage(`{"baseUrl":"https://user.example/custom"}`)}}}, cachedModels: mf.cachedModels}
	if model, err := explicitModel.SelectModel("opencode-go", "oc-bad-path"); err != nil || model.BaseURL != "https://user.example/custom" {
		t.Fatalf("explicit model endpoint did not replace rejected cache URL: %+v %v", model, err)
	}
	for providerID, id := range map[string]string{"opencode-go": "oc-bad-path", "zai": "z-bad-api"} {
		found := false
		for _, option := range mf.ModelOptions(providerID) {
			if option.Model.ID == id {
				found = true
				if option.Available || !strings.Contains(option.Reason, "unavailable") {
					t.Fatalf("unsupported cache model %s/%s was not shown unavailable: %+v", providerID, id, option)
				}
			}
		}
		if !found {
			t.Fatalf("unsupported cached model %s/%s disappeared from model options", providerID, id)
		}
	}
}

func TestPiModelCacheMissingAndOversizedLeaveBundleUsable(t *testing.T) {
	dir := t.TempDir()
	modelsPath := filepath.Join(dir, "models.json")
	if err := os.WriteFile(modelsPath, []byte(`{}`), 0600); err != nil {
		t.Fatal(err)
	}
	missing, err := LoadModels(modelsPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := missing.SelectModel("google", "gemini-3.1-pro-preview"); err != nil {
		t.Fatalf("missing cache disabled public bundle: %v", err)
	}
	cachePath := filepath.Join(dir, "models-store.json")
	if err := os.WriteFile(cachePath, []byte(strings.Repeat(" ", (32<<20)+1)), 0600); err != nil {
		t.Fatal(err)
	}
	oversized, err := LoadModels(modelsPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := oversized.SelectModel("google", "gemini-3.1-pro-preview"); err != nil {
		t.Fatalf("oversized cache disabled public bundle: %v", err)
	}
}

func TestPiCatalogCacheOldOrMalformedIsIgnored(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "models-store.json")
	for _, data := range []string{`{`, `{"openai-codex":{"lastModified":1,"models":[{"id":"old","provider":"openai-codex","api":"openai-codex-responses","contextWindow":1,"maxTokens":1}]}}`} {
		if err := os.WriteFile(path, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
		mf, err := LoadModels(filepath.Join(dir, "models.json"))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := mf.SelectModel("openai-codex", "old"); err == nil || !strings.Contains(err.Error(), "needs explicit metadata") {
			t.Fatalf("old cache accepted: %v", err)
		}
		if _, err := mf.SelectModel("openai-codex", "gpt-5.6-luna"); err != nil {
			t.Fatal(err)
		}
	}
}
