package app

import (
	"testing"

	"psi/internal/config"
	"psi/internal/modelmeta"
	"psi/internal/provider"
	"psi/internal/providerregistry"
	"psi/internal/session"
)

// Every advertised model either has a request path or stays out of the
// picker. The picker must not offer a model whose request cannot be built.
func TestAdvertisedModelsHaveRequestPathsOrStayOutOfThePicker(t *testing.T) {
	r := testRuntime(t)
	r.models = &config.ModelsFile{Providers: map[string]config.ProviderMeta{}}
	// Give every provider below a credential, so the picker checks the new
	// filtering rule instead of an empty catalog.
	families := []string{"minimax", "opencode-go", "deepseek", "zai", "google", "xiaomi", "xiaomi-token-plan-cn", "xiaomi-token-plan-ams", "xiaomi-token-plan-sgp", "moonshotai", "qwen-token-plan", "openrouter", "xai", "groq", "mistral"}
	for _, family := range families {
		if spec, ok := providerregistry.Lookup(family); ok && spec.Environment != "" {
			t.Setenv(spec.Environment, "synthetic-"+family+"-key")
		}
	}
	checked := 0
	selector := modelSelector(r)
	for _, family := range families {
		for _, meta := range config.BundledModels(family) {
			if meta.API != "anthropic-messages" && meta.API != "openai-completions" && meta.API != "google-generative-ai" && meta.API != "openai-responses" && meta.API != "mistral-conversations" {
				continue
			}
			checked++
			model := providerModel(family, meta)
			_, buildErr := provider.BuildRequest(provider.Request{Model: model, Thinking: "off"})
			if buildErr != nil {
				t.Errorf("%s/%s cannot build a request: %v", family, model.ID, buildErr)
				// A model whose request cannot be built is not offered at all.
				for _, item := range selector.Items {
					if item.ID == family+"/"+model.ID {
						t.Errorf("picker offered unusable %s/%s", family, model.ID)
					}
				}
				continue
			}
			for _, level := range provider.SupportedThinking(model) {
				if _, err := provider.BuildRequest(provider.Request{Model: model, Thinking: level}); err != nil {
					t.Errorf("available model %s/%s advertised unsupported thinking %q: %v", family, model.ID, level, err)
				}
			}
		}
	}
	if checked == 0 {
		t.Fatal("catalog compatibility coverage: no models checked")
	}
}

func TestRestoreUnsupportedCompletionModelKeepsImportedHistoryReadable(t *testing.T) {
	r := testRuntime(t)
	if _, err := r.store.AppendMessage(session.AgentMessage{Role: "user", User: &session.UserMessage{Role: "user", Content: "synthetic imported history"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.store.AppendModelChange("opencode-go", "future-model"); err != nil {
		t.Fatal(err)
	}
	r.models = &config.ModelsFile{Providers: map[string]config.ProviderMeta{"opencode-go": {Models: []config.ModelMeta{{
		ID: "future-model", API: "openai-completions", BaseURL: "https://opencode.ai/inference/go/v1", Reasoning: true, MaxTokens: 8192, Input: []string{"text"},
		Compat: modelmeta.Compat{"syntheticUnknownCompatField": []byte("true")},
	}}}}}
	if err := r.restore(); err != nil {
		t.Fatal(err)
	}
	if r.prov != nil || r.authErr == nil || r.cfg.Model.ID != "future-model" || r.ov.Provider != "opencode-go" {
		t.Fatalf("unavailable restored model not held read-only: cfg=%+v provider=%v auth=%v", r.cfg, r.prov, r.authErr)
	}
	proj, err := r.store.Projection()
	if err != nil {
		t.Fatal(err)
	}
	if len(proj.Flat) == 0 || proj.State.Provider != "opencode-go" || proj.State.ModelID != "future-model" {
		t.Fatalf("readable imported history/state lost: flat=%d state=%+v", len(proj.Flat), proj.State)
	}
}
