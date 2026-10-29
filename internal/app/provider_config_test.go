package app

import (
	"os"
	"path/filepath"
	"testing"

	"psi/internal/config"
	"psi/internal/provider"
)

func TestConfigureUsesProviderSpecificAnthropicKeysAndCatalogPaths(t *testing.T) {
	r := testRuntime(t)
	t.Setenv("MINIMAX_API_KEY", "synthetic-minimax-key")
	t.Setenv("OPENCODE_API_KEY", "")
	if err := r.configure(config.StartupOverrides{Provider: "minimax", Model: "MiniMax-M3.1-Flash", Thinking: "high"}); err != nil {
		t.Fatal(err)
	}
	minimax, ok := r.prov.(*provider.Client)
	if !ok || minimax.APIKey != "synthetic-minimax-key" || minimax.BaseURL != "https://api.minimax.io/anthropic" || r.authErr != nil {
		t.Fatal("MiniMax did not use its provider key and public Anthropic endpoint")
	}
	if !r.cfg.Model.AdaptiveThinking || r.cfg.Model.Provider != "minimax" {
		t.Fatal("MiniMax adaptive-thinking metadata was not selected")
	}

	t.Setenv("MINIMAX_API_KEY", "must-not-cross-provider")
	t.Setenv("OPENCODE_API_KEY", "synthetic-opencode-key")
	modelsPath := filepath.Join(r.agentDir, "models.json")
	if err := os.WriteFile(modelsPath, []byte(`{"providers":{"opencode-go":{"headers":{"X-OpenCode-Session":"explicit-session"}}}}`), 0600); err != nil {
		t.Fatal(err)
	}
	cache := `{"opencode-go":{"lastModified":4102444800000,"models":[{"id":"qwen3.8-flash","provider":"opencode-go","type":"chat","api":"anthropic-messages","baseUrl":"https://opencode.ai/inference/go/anthropic","contextWindow":1000000,"maxTokens":131072,"reasoning":true,"input":["text"],"compat":{"allowEmptySignature":true}}]}}`
	if err := os.WriteFile(filepath.Join(r.agentDir, "models-store.json"), []byte(cache), 0600); err != nil {
		t.Fatal(err)
	}
	var err error
	r.models, err = config.LoadModels(modelsPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.configure(config.StartupOverrides{Provider: "opencode-go", Model: "qwen3.8-flash", Thinking: "off"}); err != nil {
		t.Fatal(err)
	}
	openCode, ok := r.prov.(*provider.Client)
	if !ok || openCode.APIKey != "synthetic-opencode-key" || openCode.BaseURL != "https://opencode.ai/inference/go/anthropic" || openCode.SessionID != r.store.Header().ID || openCode.Headers["X-OpenCode-Session"] != "explicit-session" || r.authErr != nil {
		t.Fatal("OpenCode did not use its cached model endpoint, provider key, session ID, and explicit header")
	}
	if !r.cfg.Model.AllowEmptySignature {
		t.Fatal("OpenCode model compatibility was not carried to the request model")
	}

	t.Setenv("OPENCODE_API_KEY", "")
	if err := r.configure(config.StartupOverrides{Provider: "opencode-go", Model: "qwen3.8-flash", Thinking: "off"}); err != nil {
		t.Fatal(err)
	}
	openCode, ok = r.prov.(*provider.Client)
	if !ok || openCode.APIKey != "" || r.authErr == nil {
		t.Fatal("OpenCode unexpectedly used a different provider's credential")
	}
}
