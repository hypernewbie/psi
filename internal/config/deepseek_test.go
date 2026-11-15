package config

import "testing"

func TestPiDefaultModels(t *testing.T) {
	for _, tc := range []struct {
		provider string
		model    string
	}{
		{"deepseek", "deepseek-v4-pro"},
		{"google", "gemini-3.1-pro-preview"},
		{"kimi-coding", "kimi-for-coding"},
		{"minimax", "MiniMax-M2.7"},
		{"opencode-go", "kimi-k3"},
		{"zai", "glm-5.3"},
	} {
		t.Run(tc.provider, func(t *testing.T) {
			model, err := (&ModelsFile{}).SelectModel(tc.provider, "")
			if err != nil {
				t.Fatal(err)
			}
			if model.ID != tc.model {
				t.Fatalf("default model = %q, want %q", model.ID, tc.model)
			}
		})
	}
}
