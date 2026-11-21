package main

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/hypernewbie/psi/internal/config"
)

func writeTestModels(t *testing.T, agentDir string) {
	t.Helper()
	body := `{
  "providers": {
    "demo": {
      "api": "openai-completions",
      "baseUrl": "https://example.invalid/v1",
      "apiKey": "test-key",
      "models": [
        { "id": "large-model", "contextWindow": 128000, "maxTokens": 8192 }
      ]
    }
  }
}`
	if err := os.WriteFile(filepath.Join(agentDir, "models.json"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func loadTestModels(t *testing.T, agentDir string) (*config.ModelsFile, error) {
	t.Helper()
	return config.LoadModels(filepath.Join(agentDir, "models.json"))
}

func writeTestSettings(t *testing.T, workspace string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "settings.json5")
	body := fmt.Sprintf(`{
  agents: { defaults: { workspace: %q, model: "demo/large-model" } },
  channels: { discord: { token: "test-token", dmPolicy: "allowlist", allowFrom: ["223456789012345678"] } },
}`, workspace)
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func configDefaults() config.Settings {
	defaults, err := config.LoadSettings("", "", false)
	if err != nil {
		panic(err)
	}
	return defaults.Settings
}
