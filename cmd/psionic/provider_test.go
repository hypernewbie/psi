package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hypernewbie/psi/internal/config"
)

func testModels(t *testing.T) *config.ModelsFile {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "models.json")
	body := `{
  "providers": {
    "demo": {
      "api": "openai-completions",
      "baseUrl": "https://example.invalid/v1",
      "apiKey": "test-key",
      "models": [
        { "id": "large-model", "contextWindow": 128000, "maxTokens": 8192 },
        { "id": "small-model", "contextWindow": 64000, "maxTokens": 4096 }
      ]
    }
  }
}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	models, err := config.LoadModels(path)
	if err != nil {
		t.Fatalf("models: %v", err)
	}
	return models
}

func TestResolveModelKeepsSlashes(t *testing.T) {
	models := testModels(t)
	meta, catalogue, providerID, err := resolveModel(models, "demo/large-model")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if providerID != "demo" || meta.ID != "large-model" || catalogue.ContextWindow != 128000 {
		t.Fatalf("resolved %q %+v %+v", providerID, meta, catalogue)
	}
}

func TestNewProviderSetBuildsPrimaryAndCompactor(t *testing.T) {
	models := testModels(t)
	set, err := newProviderSet(models, t.TempDir(), "demo/large-model", "demo/small-model", "session-id", 1)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	defer set.close()
	if set.main == nil || set.compactor == nil {
		t.Fatal("missing clients")
	}
	if set.model.ID != "large-model" || set.compact.ID != "small-model" {
		t.Fatalf("models = %q %q", set.model.ID, set.compact.ID)
	}
}

func TestNewProviderSetRejectsUnknownModel(t *testing.T) {
	models := testModels(t)
	const privateModelID = "SYNTHETIC_PRIVATE_MODEL_ID"
	if _, err := newProviderSet(models, t.TempDir(), "demo/"+privateModelID, "", "session-id", 1); err == nil || strings.Contains(err.Error(), privateModelID) {
		t.Fatalf("unknown model diagnostic exposed its ID: %v", err)
	}
}
