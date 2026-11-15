package app

import (
	"io"
	"testing"

	"psi/internal/config"
	"psi/internal/terminal"
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

func TestCycleModelAppliesThinkingDefault(t *testing.T) {
	r := testRuntime(t)
	clearProviderAPIKeys(t)
	r.models = &config.ModelsFile{Providers: map[string]config.ProviderMeta{"local": {
		API: "openai-completions", BaseURL: "https://example.com/v1", APIKey: "synthetic-key",
		Models: []config.ModelMeta{
			{ID: "one", ContextWindow: 200000, MaxTokens: 64000, Reasoning: true},
			{ID: "two", ContextWindow: 200000, MaxTokens: 64000, Reasoning: true},
		},
	}}}
	if _, err := r.cycleModel(1); err != nil {
		t.Fatal(err)
	}
	if r.ov.Model != "one" {
		t.Fatalf("first cycle selected %q, want one", r.ov.Model)
	}
	if _, err := r.command(&terminal.Command{Name: "thinking", Arg: "off"}, io.Discard); err != nil {
		t.Fatal(err)
	}
	r.settings.ModelThinkingLevels = map[string]string{"local/two": "high"}
	if _, err := r.cycleModel(1); err != nil {
		t.Fatal(err)
	}
	if r.ov.Model != "two" || r.ov.Thinking != "high" {
		t.Fatalf("cycle selected %s at %q, want two at high", r.ov.Model, r.ov.Thinking)
	}
}
