package app

import (
	"testing"

	"psi/internal/config"
	"psi/internal/modelmeta"
	"psi/internal/provider"
	"psi/internal/session"
)

func TestCompletionCatalogModelsHaveRequestPathsOrPreciseUnavailableReasons(t *testing.T) {
	r := testRuntime(t)
	r.models = &config.ModelsFile{Providers: map[string]config.ProviderMeta{}}
	checked, unavailable := 0, 0
	selector := modelSelector(r)
	for _, family := range []string{"deepseek", "zai", "opencode-go"} {
		for _, meta := range config.BundledModels(family) {
			if meta.API != "openai-completions" {
				continue
			}
			checked++
			model := providerModel(family, meta)
			_, buildErr := provider.BuildRequest(provider.Request{Model: model, Thinking: "off"})
			if buildErr != nil {
				unavailable++
				if !containsStr(buildErr.Error(), "unsupported") {
					t.Errorf("%s/%s has unclear unavailable reason %q", family, model.ID, buildErr)
				}
				found := false
				for _, item := range selector.Items {
					if item.ID == family+"/"+model.ID && item.Unavailable != "" && containsStr(item.Unavailable, "unsupported") {
						found = true
					}
				}
				if !found {
					t.Errorf("picker did not expose %s/%s as unavailable", family, model.ID)
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
	if checked == 0 || unavailable == 0 {
		t.Fatalf("catalog compatibility coverage: checked=%d unavailable=%d", checked, unavailable)
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
		Compat: modelmeta.Compat{"supportsMidConvoSystemMessages": []byte("true")},
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
