package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestConfigPathDiscovery proves Psionic finds its settings without a flag:
// the flag wins, then the environment variable, then the standard file.
func TestConfigPathDiscovery(t *testing.T) {
	explicit := filepath.Join(t.TempDir(), "explicit.json5")
	if err := os.WriteFile(explicit, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	envPath := filepath.Join(t.TempDir(), "from-env.json5")
	if err := os.WriteFile(envPath, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := resolveConfigPath(explicit, func(string) (string, bool) { return "", false })
	if err != nil || got != explicit {
		t.Fatalf("explicit path = %q, %v", got, err)
	}

	got, err = resolveConfigPath("", func(name string) (string, bool) {
		if name == "PSIONIC_CONFIG" {
			return envPath, true
		}
		return "", false
	})
	if err != nil || got != envPath {
		t.Fatalf("environment path = %q, %v", got, err)
	}

	// Without the flag or the variable, the standard file is used.
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	standard := filepath.Join(home, "config", "psionic", "config.json5")
	if err := os.MkdirAll(filepath.Dir(standard), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(standard, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err = resolveConfigPath("", func(string) (string, bool) { return "", false })
	if err != nil || got != standard {
		t.Fatalf("standard path = %q, %v", got, err)
	}

	// With no file at all, the error names the file to create.
	if err := os.Remove(standard); err != nil {
		t.Fatal(err)
	}
	if _, err := resolveConfigPath("", func(string) (string, bool) { return "", false }); err == nil {
		t.Fatal("a missing configuration file was accepted")
	} else if !strings.Contains(err.Error(), "config.json5") {
		t.Fatalf("error = %v", err)
	}
}

// TestDiscordConfigReferenceSuppliesOnlyDiscordSettings proves the
// referenced file contributes the Discord block and nothing else: its model,
// tool, and agent settings are not Psionic's to read.
func TestDiscordConfigReferenceSuppliesOnlyDiscordSettings(t *testing.T) {
	dir := t.TempDir()
	referenced := filepath.Join(dir, "referenced.json5")
	referencedWorkspace := filepath.Join(dir, "referenced-workspace")
	serviceWorkspace := filepath.Join(dir, "service-workspace")
	body := fmt.Sprintf(`{
  agents: { defaults: { workspace: %q, model: { primary: "other/model" } } },
  tools: { profile: "coding" },
  models: { providers: { other: { apiKey: "not-used" } } },
  channels: { discord: { token: "referenced-token", allowFrom: ["123456789012345678"], groupPolicy: "allowlist" } },
}`, referencedWorkspace)
	if err := os.WriteFile(referenced, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	main := filepath.Join(dir, "config.json5")
	mainBody := fmt.Sprintf(`{
  discordConfig: "referenced.json5",
  agents: { defaults: { workspace: %q } },
}`, serviceWorkspace)
	if err := os.WriteFile(main, []byte(mainBody), 0o600); err != nil {
		t.Fatal(err)
	}

	settings, err := loadFileSettings(main, func(string) (string, bool) { return "", false })
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if settings.Channels == nil || settings.Channels.Discord == nil {
		t.Fatal("the Discord block was not taken from the referenced file")
	}
	if settings.Channels.Discord.Token == nil || settings.Channels.Discord.Token.value != "referenced-token" {
		t.Fatal("the referenced token was not used")
	}
	if settings.Tools != nil {
		t.Fatal("the referenced tool settings were read")
	}
	agent, err := resolveAgent(settings, "")
	if err != nil {
		t.Fatal(err)
	}
	if agent.Workspace != resolvePath(serviceWorkspace) {
		t.Fatalf("workspace = %q", agent.Workspace)
	}
	if agent.Model != "" {
		t.Fatalf("model = %q", agent.Model)
	}
}

// TestDiscordConfigReferenceRejectsAnInlineBlock proves the two Discord
// sources cannot disagree silently.
func TestDiscordConfigReferenceRejectsAnInlineBlock(t *testing.T) {
	dir := t.TempDir()
	referenced := filepath.Join(dir, "referenced.json5")
	if err := os.WriteFile(referenced, []byte(`{ channels: { discord: { token: "referenced-token" } } }`), 0o600); err != nil {
		t.Fatal(err)
	}
	main := filepath.Join(dir, "config.json5")
	body := `{
  discordConfig: "referenced.json5",
  channels: { discord: { token: "inline-token" } },
}`
	if err := os.WriteFile(main, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadFileSettings(main, func(string) (string, bool) { return "", false }); err == nil {
		t.Fatal("two Discord sources were accepted")
	}
}

// TestDiscordConfigReferenceRequiresADiscordBlock proves a referenced file
// without a Discord block fails closed.
func TestDiscordConfigReferenceRequiresADiscordBlock(t *testing.T) {
	dir := t.TempDir()
	referenced := filepath.Join(dir, "referenced.json5")
	if err := os.WriteFile(referenced, []byte(`{ agents: { defaults: { workspace: "/w" } } }`), 0o600); err != nil {
		t.Fatal(err)
	}
	main := filepath.Join(dir, "config.json5")
	if err := os.WriteFile(main, []byte(`{ discordConfig: "referenced.json5" }`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadFileSettings(main, func(string) (string, bool) { return "", false }); err == nil {
		t.Fatal("a referenced file without a Discord block was accepted")
	}
}

// TestSettingsDirectoriesResolve proves the settings file can name the Psi
// directories and the transcript directory.
func TestSettingsDirectoriesResolve(t *testing.T) {
	dir := t.TempDir()
	agent := filepath.Join(dir, "service-agent")
	shared := filepath.Join(dir, "shared-agent")
	state := filepath.Join(t.TempDir(), "state")
	for _, path := range []string{agent, shared, state} {
		if err := os.MkdirAll(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
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
	if err := os.WriteFile(filepath.Join(agent, "settings.json"), []byte(`{"defaultProvider":"demo","defaultModel":"large-model"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	config := filepath.Join(dir, "config.json5")
	body := fmt.Sprintf(`{
  psi: { agentDir: %q, sharedAgentDir: %q },
  stateDir: %q,
  agents: { defaults: { workspace: %q } },
  channels: { discord: { token: "test-token" } },
}`, agent, shared, state, dir)
	if err := os.WriteFile(config, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	loaded, err := prepare(options{configPath: config})
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	if loaded.agentDir != agent {
		t.Fatalf("agent dir = %q", loaded.agentDir)
	}
	if loaded.authPath != filepath.Join(shared, "auth.json") {
		t.Fatalf("auth path = %q", loaded.authPath)
	}
	if !loaded.sharedCatalogue {
		t.Fatal("the shared catalogue was not reported")
	}
	resolvedState, err := resolveStateDir(loaded.settings.StateDir)
	if err != nil || resolvedState != state {
		t.Fatalf("state dir = %q, %v", resolvedState, err)
	}
}
