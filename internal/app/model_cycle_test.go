package app

import (
	"testing"

	"psi/internal/config"
)

func TestCycleModelWhenCurrentIsUnavailable(t *testing.T) {
	for _, tc := range []struct {
		name      string
		models    []config.ModelMeta
		direction int
		want      string
	}{
		{"only available model", []config.ModelMeta{{ID: "one", ContextWindow: 8000, MaxTokens: 1000}}, 1, "one"},
		{"forward selects first", []config.ModelMeta{{ID: "one", ContextWindow: 8000, MaxTokens: 1000}, {ID: "two", ContextWindow: 8000, MaxTokens: 1000}}, 1, "one"},
		{"backward selects last", []config.ModelMeta{{ID: "one", ContextWindow: 8000, MaxTokens: 1000}, {ID: "two", ContextWindow: 8000, MaxTokens: 1000}}, -1, "two"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := testRuntime(t)
			clearProviderAPIKeys(t)
			r.models = &config.ModelsFile{Providers: map[string]config.ProviderMeta{"local": {
				API: "openai-completions", BaseURL: "https://example.com/v1", APIKey: "synthetic-key", Models: tc.models,
			}}}
			status, err := r.cycleModel(tc.direction)
			if err != nil {
				t.Fatal(err)
			}
			if r.ov.Provider != "local" || r.ov.Model != tc.want {
				t.Fatalf("cycle %q selected %s/%s, want local/%s", status, r.ov.Provider, r.ov.Model, tc.want)
			}
			if len(tc.models) == 1 {
				status, err = r.cycleModel(tc.direction)
				if err != nil || status != "Only one model available" {
					t.Fatalf("cycling the current sole model = %q, %v", status, err)
				}
			}
		})
	}
}
