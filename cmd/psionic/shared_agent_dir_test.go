package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestSharedAgentDirectorySuppliesTheCatalogueAndCredentials proves the
// service can keep its own settings.json while sharing one catalogue and one
// credential file with the main Psi installation, without copying or linking
// files.
func TestSharedAgentDirectorySuppliesTheCatalogueAndCredentials(t *testing.T) {
	shared := t.TempDir()
	agent := t.TempDir()
	workspace := t.TempDir()

	// The shared directory holds the catalogue and the credential file.
	models := `{
  "providers": {
    "demo": {
      "api": "openai-completions",
      "baseUrl": "https://example.invalid/v1",
      "apiKey": "shared-key",
      "models": [{ "id": "large-model", "contextWindow": 128000, "maxTokens": 8192 }]
    }
  }
}`
	if err := os.WriteFile(filepath.Join(shared, "models.json"), []byte(models), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(shared, "auth.json"), []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	// The service directory holds only the settings file.
	settingsBody := `{"defaultProvider":"demo","defaultModel":"large-model"}`
	if err := os.WriteFile(filepath.Join(agent, "settings.json"), []byte(settingsBody), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(agent, "models.json")); !os.IsNotExist(err) {
		t.Fatal("the service directory must not need a catalogue file")
	}

	loaded, err := prepare(options{
		configPath:     writeTestSettings(t, workspace),
		agentDir:       agent,
		sharedAgentDir: shared,
		stateDir:       t.TempDir(),
	})
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	if loaded.agent.Model != "demo/large-model" {
		t.Fatalf("model = %q", loaded.agent.Model)
	}
	if loaded.authPath != filepath.Join(shared, "auth.json") {
		t.Fatalf("auth path = %q", loaded.authPath)
	}
	if !loaded.sharedCatalogue {
		t.Fatal("the shared catalogue was not reported")
	}
	var output bytes.Buffer
	if err := printCheck(&output, loaded); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "model catalogue: shared Psi agent directory") {
		t.Fatalf("check output = %q", output.String())
	}
}

// TestSharedAgentDirectoryDefaultsToTheAgentDirectory proves the default
// behavior is unchanged: one directory holds all three files.
func TestSharedAgentDirectoryDefaultsToTheAgentDirectory(t *testing.T) {
	agent := t.TempDir()
	workspace := t.TempDir()
	models := `{
  "providers": {
    "demo": {
      "api": "openai-completions",
      "baseUrl": "https://example.invalid/v1",
      "apiKey": "shared-key",
      "models": [{ "id": "large-model", "contextWindow": 128000, "maxTokens": 8192 }]
    }
  }
}`
	if err := os.WriteFile(filepath.Join(agent, "models.json"), []byte(models), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(agent, "settings.json"), []byte(`{"defaultProvider":"demo","defaultModel":"large-model"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, err := prepare(options{
		configPath: writeTestSettings(t, workspace),
		agentDir:   agent,
		stateDir:   t.TempDir(),
	})
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	if loaded.authPath != filepath.Join(agent, "auth.json") {
		t.Fatalf("auth path = %q", loaded.authPath)
	}
	if loaded.sharedCatalogue {
		t.Fatal("a single directory was reported as shared")
	}
}

// TestSharedAgentDirectoryIsRefusedAsAStateDirectory proves the shared
// directory cannot hold Psionic transcripts.
func TestSharedAgentDirectoryIsRefusedAsAStateDirectory(t *testing.T) {
	shared := t.TempDir()
	if err := checkStateDir(filepath.Join(shared, "psionic"), t.TempDir(), "", shared); err == nil {
		t.Fatal("a state directory inside the shared agent directory was accepted")
	}
}
