package app

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"psi/internal/config"
	"psi/internal/provider"
)

// A command line reference to a provider from models.json resolves after the
// file loads.
func TestConfiguredProviderResolvesCommandLineReferences(t *testing.T) {
	models := &config.ModelsFile{Providers: map[string]config.ProviderMeta{"local-openai": {
		API: "openai-completions", BaseURL: "https://provider.example/v1",
		Models: []config.ModelMeta{{ID: "qwen2.5-coder:7b"}},
	}}}
	resolved, err := resolveConfiguredModelOption(Options{Model: "local-openai/qwen2.5-coder:7b"}, models)
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Provider != "local-openai" || resolved.Model != "qwen2.5-coder:7b" {
		t.Fatalf("qualified reference = %+v", resolved)
	}
	resolved, err = resolveConfiguredModelOption(Options{Provider: "local-openai", Model: "qwen2.5-coder:7b"}, models)
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Provider != "local-openai" || resolved.Model != "qwen2.5-coder:7b" {
		t.Fatalf("provider and model = %+v", resolved)
	}
	resolved, err = resolveConfiguredModelOption(Options{Model: "local-openai/meta-llama/Llama-3"}, models)
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Provider != "local-openai" || resolved.Model != "meta-llama/Llama-3" {
		t.Fatalf("model with a slash = %+v", resolved)
	}
	if _, err := resolveConfiguredModelOption(Options{Provider: "anthropic", Model: "local-openai/m"}, models); err == nil {
		t.Fatal("a conflicting provider and model were accepted")
	}
	if _, err := resolveConfiguredModelOption(Options{Provider: "not-a-provider"}, models); err == nil {
		t.Fatal("an unknown provider was accepted")
	}
}

// A provider from models.json appears in the model list and in the picker
// when it has a credential.
func TestConfiguredProviderAppearsInModelListAndPicker(t *testing.T) {
	r := testRuntime(t)
	modelsPath := filepath.Join(r.agentDir, "models.json")
	raw := `{"providers":{"local-openai":{"name":"Local","api":"openai-completions","baseUrl":"https://provider.example/v1","apiKey":"synthetic-local-key","models":[{"id":"qwen2.5-coder","contextWindow":32000,"maxTokens":4096}]}}}`
	if err := os.WriteFile(modelsPath, []byte(raw), 0600); err != nil {
		t.Fatal(err)
	}
	loaded, err := config.LoadModels(modelsPath)
	if err != nil {
		t.Fatal(err)
	}
	r.models = loaded

	var output bytes.Buffer
	if err := listAvailableModels(&output, r.agentDir, ""); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "local-openai") || !strings.Contains(output.String(), "qwen2.5-coder") {
		t.Fatalf("model list does not show the configured provider:\n%s", output.String())
	}

	selector := modelSelector(r)
	found := false
	for _, item := range selector.Items {
		if item.ID == "local-openai/qwen2.5-coder" {
			found = true
		}
	}
	if !found {
		t.Fatalf("the picker does not offer the configured model: %+v", selector.Items)
	}
}

// The reload reads models.json again, and a malformed file keeps the current
// model configuration.
func TestReloadReadsModelsJSONAgain(t *testing.T) {
	r := testRuntime(t)
	modelsPath := filepath.Join(r.agentDir, "models.json")
	raw := `{"providers":{"local-openai":{"api":"openai-completions","baseUrl":"https://provider.example/v1","apiKey":"synthetic-local-key","models":[{"id":"qwen2.5-coder"}]}}}`
	if err := os.WriteFile(modelsPath, []byte(raw), 0600); err != nil {
		t.Fatal(err)
	}
	if err := r.reloadModels(); err != nil {
		t.Fatal(err)
	}
	if _, err := r.models.SelectModel("local-openai", "qwen2.5-coder"); err != nil {
		t.Fatalf("the reloaded models.json was not installed: %v", err)
	}
	if err := os.WriteFile(modelsPath, []byte(`{"providers":`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := r.reloadModels(); err == nil {
		t.Fatal("a malformed models.json was accepted")
	}
	if _, err := r.models.SelectModel("local-openai", "qwen2.5-coder"); err != nil {
		t.Fatalf("a malformed models.json replaced the working configuration: %v", err)
	}
}

// A provider from models.json runs through the existing protocol client with
// its configured key, headers, and bearer header.
func TestConfiguredProviderRunsThroughTheCompletionsClient(t *testing.T) {
	var seen http.Header
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r.Header.Clone()
		if r.URL.Path != "/v1/chat/completions" {
			t.Errorf("request path = %q", r.URL.Path)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	}))
	defer server.Close()
	r := testRuntime(t)
	modelsPath := filepath.Join(r.agentDir, "models.json")
	raw := `{"providers":{"local-openai":{"api":"openai-completions","baseUrl":"` + server.URL + `/v1","apiKey":"synthetic-local-key","authHeader":true,"headers":{"X-Deployment":"synthetic"},"models":[{"id":"qwen2.5-coder","contextWindow":32000,"maxTokens":4096}]}}}`
	if err := os.WriteFile(modelsPath, []byte(raw), 0600); err != nil {
		t.Fatal(err)
	}
	loaded, err := config.LoadModels(modelsPath)
	if err != nil {
		t.Fatal(err)
	}
	r.models = loaded
	if err := r.configure(config.StartupOverrides{Provider: "local-openai", Model: "qwen2.5-coder", Thinking: "off"}); err != nil {
		t.Fatal(err)
	}
	if r.authErr != nil {
		t.Fatalf("the configured provider did not resolve a credential: %v", r.authErr)
	}
	response, err := r.prov.Send(context.Background(), provider.Request{Model: r.cfg.Model, Thinking: "off", Messages: []provider.Message{{Role: "user", Content: []provider.Block{{Type: "text", Text: "hi"}}}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if response.Text != "ok" {
		t.Fatalf("response = %+v", response)
	}
	if seen.Get("Authorization") != "Bearer synthetic-local-key" {
		t.Fatalf("authorization header = %q", seen.Get("Authorization"))
	}
	if seen.Get("X-Deployment") != "synthetic" {
		t.Fatalf("configured header = %q", seen.Get("X-Deployment"))
	}
}
