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

	"github.com/hypernewbie/psi/internal/agent"
	"github.com/hypernewbie/psi/internal/config"
	"github.com/hypernewbie/psi/internal/provider"
	"github.com/hypernewbie/psi/internal/session"
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

func TestCompactionProviderUsesReloadedCatalog(t *testing.T) {
	r := testRuntime(t)
	// The active model in this fixture is offline. Only the configured
	// compactor needs a client for this test, and it sends no requests.
	r.authErr = nil
	r.publishLive()
	const compactor = "local-openai/summary"
	writeCatalog := func(maxTokens string) {
		t.Helper()
		raw := `{"providers":{"local-openai":{"api":"openai-completions","baseUrl":"https://provider.example/v1","apiKey":"synthetic-local-key","models":[{"id":"summary","maxTokens":` + maxTokens + `}]}}}`
		if err := os.WriteFile(filepath.Join(r.agentDir, "models.json"), []byte(raw), 0600); err != nil {
			t.Fatal(err)
		}
	}
	writeCatalog("2048")
	if err := r.reloadModels(); err != nil {
		t.Fatal(err)
	}
	writeCatalog("4096")

	type resolution struct {
		model provider.ModelMeta
		err   error
	}
	settings := r.settings
	resolve := func() resolution {
		_, model, cleanup, err := r.compactionProvider(compactor, settings)
		if cleanup != nil {
			cleanup()
		}
		return resolution{model, err}
	}
	start := make(chan struct{})
	resolved := make(chan resolution, 1)
	go func() {
		<-start
		resolved <- resolve()
	}()
	close(start)
	reloadErr := r.reloadModels()
	overlapping := <-resolved
	if reloadErr != nil {
		t.Fatal(reloadErr)
	}
	if overlapping.err != nil || overlapping.model.ID != "summary" || (overlapping.model.MaxTokens != 2048 && overlapping.model.MaxTokens != 4096) {
		t.Fatalf("compactor resolution during reload = %+v, %v; want summary from either catalog", overlapping.model, overlapping.err)
	}
	latest := resolve()
	if latest.err != nil || latest.model.ID != "summary" || latest.model.MaxTokens != 4096 {
		t.Fatalf("compactor resolution after reload = %+v, %v; want summary from the new catalog", latest.model, latest.err)
	}
}

func TestCompactionPinsExplicitActiveModelAcrossSplit(t *testing.T) {
	r := testRuntime(t)
	for _, task := range []struct {
		prompt string
		spans  int
	}{{"older task", 1}, {"current task", 4}} {
		if _, err := r.store.AppendMessage(agentMessageUser(task.prompt)); err != nil {
			t.Fatal(err)
		}
		for range task.spans {
			if _, err := r.store.AppendMessage(session.AgentMessage{Role: "assistant", Assistant: &session.AssistantMessage{
				Role: "assistant", Content: []session.ContentBlock{{Type: "text", Text: &session.TextContent{Type: "text", Text: strings.Repeat("work ", 16_000)}}},
			}}); err != nil {
				t.Fatal(err)
			}
		}
	}
	oldModel := r.cfg.Model
	newModel := oldModel
	newModel.ID = "next"
	old := &provider.Fake{Responses: []*provider.Response{{Text: "history summary", StopReason: "stop"}, {Text: "prefix summary", StopReason: "stop"}}}
	next := &provider.Fake{Responses: []*provider.Response{{Text: "next response", StopReason: "stop"}, {Text: "next response", StopReason: "stop"}}}
	r.prov, r.authErr = old, nil
	r.cfg.KeepRecentTokens = 20_000
	r.publishLive()
	cfg := r.cfg
	cfg.CompactionProvider = func() (provider.Provider, provider.ModelMeta, func(), error) {
		return r.compactionProvider("anthropic/m", r.settings)
	}
	cfg.LiveSettings = func() agent.LiveSettings {
		if old.CallCount() != 0 {
			return agent.LiveSettings{Provider: next, Model: newModel, Thinking: "off", KeepRecentTokens: 20_000}
		}
		return r.liveSettings()
	}
	if err := r.newLoop(nil).CompactAndResume(t.Context(), cfg, ""); err != nil {
		t.Fatal(err)
	}
	summaries, continuation := old.Requests(), next.Requests()
	if len(summaries) != 2 || summaries[0].Model.ID != oldModel.ID || summaries[1].Model.ID != oldModel.ID ||
		len(continuation) != 1 || continuation[0].Model.ID != newModel.ID {
		t.Fatalf("explicit compactor should handle both summaries, then resume on the new model; calls: compactor=%d next=%d", len(summaries), len(continuation))
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
