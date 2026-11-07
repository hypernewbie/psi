package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"psi/internal/modelmeta"
	"psi/internal/providerregistry"
)

// A provider that models.json declares composes its models the way Pi does:
// the provider supplies the API, the endpoint, and the compatibility
// defaults, the model entry overrides them, and modelOverrides apply last.
func TestConfiguredProviderComposesModelsFromModelsJSON(t *testing.T) {
	models := &ModelsFile{Providers: map[string]ProviderMeta{
		"local-openai": {
			API: "openai-completions", BaseURL: "https://provider.example/v1",
			Compat:  modelmeta.Compat{"supportsStore": json.RawMessage("true")},
			Headers: map[string]string{"X-Provider": "yes"},
			Models: []ModelMeta{
				{ID: "plain", ContextWindow: 32000, MaxTokens: 4096, Input: []string{"text"}},
				{ID: "own-endpoint", API: "openai-responses", BaseURL: "https://model.example/v1", Reasoning: true, Input: []string{"text", "image"}},
			},
		},
	}}
	model, err := models.SelectModel("local-openai", "plain")
	if err != nil {
		t.Fatal(err)
	}
	if model.API != "openai-completions" || model.BaseURL != "https://provider.example/v1" {
		t.Fatalf("provider defaults were not applied: %+v", model)
	}
	if model.ContextWindow != 32000 || model.MaxTokens != 4096 || model.Name != "plain" {
		t.Fatalf("model metadata was not applied: %+v", model)
	}
	if !model.Custom {
		t.Fatal("a provider from models.json was not marked as configured")
	}
	if value, hasValue, present := model.Compat.Bool("supportsStore"); !present || !hasValue || !value {
		t.Fatalf("provider compatibility was not applied: %t %t %t", value, hasValue, present)
	}
	if model.Headers["X-Provider"] != "yes" {
		t.Fatalf("provider headers were not applied: %+v", model.Headers)
	}
	own, err := models.SelectModel("local-openai", "own-endpoint")
	if err != nil {
		t.Fatal(err)
	}
	if own.API != "openai-responses" || own.BaseURL != "https://model.example/v1" || !own.Reasoning || !own.InputImage {
		t.Fatalf("model entry did not override the provider: %+v", own)
	}
}

// Pi's defaults apply when the model entry omits the optional fields.
func TestConfiguredProviderAppliesPiModelDefaults(t *testing.T) {
	models := &ModelsFile{Providers: map[string]ProviderMeta{
		"local": {API: "openai-completions", BaseURL: "https://provider.example/v1", Models: []ModelMeta{{ID: "m"}}},
	}}
	model, err := models.SelectModel("local", "m")
	if err != nil {
		t.Fatal(err)
	}
	if model.ContextWindow != 128000 || model.MaxTokens != 16384 || model.Name != "m" || model.Reasoning {
		t.Fatalf("Pi model defaults were not applied: %+v", model)
	}
	if len(model.Input) != 1 || model.Input[0] != "text" || model.InputImage {
		t.Fatalf("Pi input default was not applied: %+v", model)
	}
}

// A configured provider without an API or an endpoint fails closed instead of
// guessing a protocol or a host.
func TestConfiguredProviderRequiresAPIAndBaseURL(t *testing.T) {
	models := &ModelsFile{Providers: map[string]ProviderMeta{
		"no-endpoint": {API: "openai-completions", Models: []ModelMeta{{ID: "m"}}},
		"no-api":      {BaseURL: "https://provider.example/v1", Models: []ModelMeta{{ID: "m"}}},
		"codex":       {API: "openai-codex-responses", BaseURL: "https://provider.example/v1", Models: []ModelMeta{{ID: "m"}}},
	}}
	if _, err := models.SelectModel("no-endpoint", "m"); err == nil || !strings.Contains(err.Error(), "baseUrl") {
		t.Fatalf("missing baseUrl was accepted: %v", err)
	}
	if _, err := models.SelectModel("no-api", "m"); err == nil || !strings.Contains(err.Error(), "api") {
		t.Fatalf("missing api was accepted: %v", err)
	}
	if _, err := models.SelectModel("codex", "m"); err == nil || !strings.Contains(err.Error(), "openai-codex-responses") {
		t.Fatalf("the Codex adapter was accepted for a configured provider: %v", err)
	}
	if _, err := models.SelectModel("missing", "m"); err == nil || !strings.Contains(err.Error(), "unsupported provider") {
		t.Fatalf("an undeclared provider was accepted: %v", err)
	}
}

// Model overrides apply after the model entry, and an override for an unknown
// model does not create a model.
func TestConfiguredProviderAppliesOverridesLast(t *testing.T) {
	models := &ModelsFile{Providers: map[string]ProviderMeta{
		"local": {
			API: "openai-completions", BaseURL: "https://provider.example/v1",
			Models: []ModelMeta{{ID: "m", ContextWindow: 1000, Compat: modelmeta.Compat{"supportsStore": json.RawMessage("true")}}},
			ModelOverrides: map[string]json.RawMessage{
				"m":       json.RawMessage(`{"contextWindow":4242,"compat":{"supportsStore":false}}`),
				"unknown": json.RawMessage(`{"contextWindow":1}`),
			},
		},
	}}
	model, err := models.SelectModel("local", "m")
	if err != nil {
		t.Fatal(err)
	}
	if model.ContextWindow != 4242 {
		t.Fatalf("model override was not applied: %+v", model)
	}
	if value, hasValue, present := model.Compat.Bool("supportsStore"); !present || !hasValue || value {
		t.Fatalf("explicit false was lost: %t %t %t", value, hasValue, present)
	}
	for _, option := range models.ModelOptions("local") {
		if option.Model.ID == "unknown" {
			t.Fatalf("an override for an unknown model created a model: %+v", option)
		}
	}
}

// The effective provider set is the built-in providers plus the configured
// ones, and it never changes the global registry.
func TestProviderIDsIncludeConfiguredProvidersOnlyForThisFile(t *testing.T) {
	models := &ModelsFile{Providers: map[string]ProviderMeta{"local": {API: "openai-completions"}}}
	ids := models.ProviderIDs()
	if len(ids) != len(providerregistry.IDs())+1 {
		t.Fatalf("effective provider set = %v", ids)
	}
	found := false
	for _, id := range ids {
		if id == "local" {
			found = true
		}
	}
	if !found {
		t.Fatalf("effective provider set = %v", ids)
	}
	if !models.ConfiguredProvider("local") || models.ConfiguredProvider("anthropic") {
		t.Fatal("configured provider detection is wrong")
	}
	if _, ok := providerregistry.Lookup("local"); ok {
		t.Fatal("a configured provider changed the global registry")
	}
	other := &ModelsFile{Providers: map[string]ProviderMeta{"other": {API: "openai-completions"}}}
	if other.ConfiguredProvider("local") {
		t.Fatal("one configuration leaked a provider into another")
	}
}

// Two configurations with the same provider ID keep their own endpoints and
// keys.
func TestConfiguredProvidersStayIsolatedBetweenFiles(t *testing.T) {
	first := &ModelsFile{Providers: map[string]ProviderMeta{"local": {
		API: "openai-completions", BaseURL: "https://first.example/v1", APIKey: "first-key",
		Models: []ModelMeta{{ID: "m"}},
	}}}
	second := &ModelsFile{Providers: map[string]ProviderMeta{"local": {
		API: "openai-completions", BaseURL: "https://second.example/v1", APIKey: "second-key",
		Models: []ModelMeta{{ID: "m"}},
	}}}
	firstModel, err := first.SelectModel("local", "m")
	if err != nil {
		t.Fatal(err)
	}
	secondModel, err := second.SelectModel("local", "m")
	if err != nil {
		t.Fatal(err)
	}
	if firstModel.BaseURL != "https://first.example/v1" || firstModel.APIKey != "first-key" {
		t.Fatalf("first configuration changed: %+v", firstModel)
	}
	if secondModel.BaseURL != "https://second.example/v1" || secondModel.APIKey != "second-key" {
		t.Fatalf("second configuration changed: %+v", secondModel)
	}
}

// A configured provider needs an explicit model, and its models appear in the
// model options.
func TestConfiguredProviderModelsAppearInModelOptions(t *testing.T) {
	models := &ModelsFile{Providers: map[string]ProviderMeta{"local": {
		API: "openai-completions", BaseURL: "https://provider.example/v1",
		Models: []ModelMeta{{ID: "b", ContextWindow: 10}, {ID: "a", ContextWindow: 10}},
	}}}
	if _, err := models.SelectModel("local", ""); err == nil {
		t.Fatal("a configured provider accepted an empty model")
	}
	options := models.ModelOptions("local")
	if len(options) != 2 || options[0].Model.ID != "a" || options[1].Model.ID != "b" {
		t.Fatalf("configured model options = %+v", options)
	}
	for _, option := range options {
		if !option.Available {
			t.Fatalf("configured model %s is unavailable: %s", option.Model.ID, option.Reason)
		}
	}
}

// Pi accepts a byte order mark, // line comments, and trailing commas in
// models.json.
func TestLoadModelsAcceptsPiJSONComments(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "models.json")
	raw := "\xef\xbb\xbf{\n  // the local endpoint\n  \"providers\": {\n    \"local\": {\n      \"api\": \"openai-completions\",\n      \"baseUrl\": \"https://provider.example/v1\", // trailing comment\n      \"models\": [{\"id\": \"m\", \"headers\": {\"X-Path\": \"a//b\"},}],\n    },\n  },\n}\n"
	if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
		t.Fatal(err)
	}
	models, err := LoadModels(path)
	if err != nil {
		t.Fatal(err)
	}
	model, err := models.SelectModel("local", "m")
	if err != nil {
		t.Fatal(err)
	}
	if model.BaseURL != "https://provider.example/v1" || model.Headers["X-Path"] != "a//b" {
		t.Fatalf("comment handling changed the values: %+v", model)
	}
}
