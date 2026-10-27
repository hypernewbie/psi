package config

import (
	"path/filepath"
	"testing"
)

func TestCodexBuiltinsAndModelOverrides(t *testing.T) {
	// Synthetic local files only; do not copy a developer's shared Pi config.
	root := t.TempDir()
	settingsPath, modelsPath := filepath.Join(root, "settings.json"), filepath.Join(root, "models.json")
	writeJSON(t, settingsPath, `{"defaultProvider":"openai-codex","defaultModel":"gpt-5.6-luna","defaultThinkingLevel":"xhigh"}`)
	writeJSON(t, modelsPath, `{"providers":{"openai-codex":{"baseUrl":"http://127.0.0.1:1","modelOverrides":{"gpt-5.6-luna":{"contextWindow":424242}}}}}`)
	loaded, err := LoadSettings(settingsPath, "", false)
	if err != nil {
		t.Fatal(err)
	}
	models, err := LoadModels(modelsPath)
	if err != nil {
		t.Fatal(err)
	}
	m, err := models.SelectModel(loaded.Settings.DefaultProvider, loaded.Settings.DefaultModel)
	if err != nil {
		t.Fatal(err)
	}
	if m.API != "openai-codex-responses" || m.ContextWindow != 424242 || !m.InputImage || !m.Reasoning || m.BaseURL != "http://127.0.0.1:1" {
		t.Fatal("Codex builtin/override selection was not applied")
	}
	if m.ThinkingLevelMap["xhigh"] == nil || *m.ThinkingLevelMap["xhigh"] != "xhigh" {
		t.Fatal("Codex reasoning effort metadata was lost")
	}
}
