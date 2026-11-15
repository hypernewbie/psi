package config

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestResolveThinkingLevelPrecedence(t *testing.T) {
	cases := []struct {
		name     string
		settings Settings
		provider string
		model    string
		carried  string
		explicit bool
		want     string
	}{
		{
			name:     "explicit wins over defaults",
			settings: Settings{DefaultThinking: "low", DefaultThinkingSet: true, ModelThinkingLevels: map[string]string{"anthropic/m": "high"}},
			provider: "anthropic", model: "m", carried: "minimal", explicit: true, want: "minimal",
		},
		{
			name:     "explicit off wins",
			settings: Settings{DefaultThinking: "high", DefaultThinkingSet: true, ModelThinkingLevels: map[string]string{"anthropic/m": "max"}},
			provider: "anthropic", model: "m", carried: "off", explicit: true, want: "off",
		},
		{
			name:     "per-model wins over global",
			settings: Settings{DefaultThinking: "low", DefaultThinkingSet: true, ModelThinkingLevels: map[string]string{"anthropic/m": "high"}},
			provider: "anthropic", model: "m", carried: "minimal", want: "high",
		},
		{
			name:     "per-model off wins over global",
			settings: Settings{DefaultThinking: "high", DefaultThinkingSet: true, ModelThinkingLevels: map[string]string{"anthropic/m": "off"}},
			provider: "anthropic", model: "m", carried: "low", want: "off",
		},
		{
			name:     "global default when no per-model entry",
			settings: Settings{DefaultThinking: "off", DefaultThinkingSet: true, ModelThinkingLevels: map[string]string{"anthropic/other": "high"}},
			provider: "anthropic", model: "m", carried: "medium", want: "off",
		},
		{
			name:     "carried level without defaults",
			settings: Settings{ModelThinkingLevels: map[string]string{}},
			provider: "anthropic", model: "m", carried: "medium", want: "medium",
		},
		{
			name:     "nil map keeps carried level",
			settings: Settings{},
			provider: "anthropic", model: "m", carried: "medium", want: "medium",
		},
		{
			name:     "exact key with dashes",
			settings: Settings{ModelThinkingLevels: map[string]string{"anthropic/claude-sonnet-4-5": "max"}},
			provider: "anthropic", model: "claude-sonnet-4-5", carried: "off", want: "max",
		},
		{
			name:     "model ID containing a slash",
			settings: Settings{ModelThinkingLevels: map[string]string{"anthropic/vendor/model": "high"}},
			provider: "anthropic", model: "vendor/model", carried: "off", want: "high",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ResolveThinkingLevel(tc.settings, tc.provider, tc.model, tc.carried, tc.explicit); got != tc.want {
				t.Fatalf("ResolveThinkingLevel() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestLoadSettingsMergesModelThinkingLevelsPerKey(t *testing.T) {
	dir := t.TempDir()
	user := filepath.Join(dir, "user.json")
	project := filepath.Join(dir, ".pi", "settings.json")
	writeJSON(t, user, `{"modelThinkingLevels":{"anthropic/a":"low","anthropic/b":"high"}}`)
	writeJSON(t, project, `{"modelThinkingLevels":{"anthropic/b":"medium","anthropic/c":"max"}}`)

	loaded, err := LoadSettings(user, project, true)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"anthropic/a": "low", "anthropic/b": "medium", "anthropic/c": "max"}
	if len(loaded.Settings.ModelThinkingLevels) != len(want) {
		t.Fatalf("merged map = %v, want %v", loaded.Settings.ModelThinkingLevels, want)
	}
	for key, level := range want {
		if got := loaded.Settings.ModelThinkingLevels[key]; got != level {
			t.Errorf("merged[%q] = %q, want %q", key, got, level)
		}
	}
	if loaded.Sources["modelThinkingLevels"] != "project:"+project {
		t.Fatalf("source = %q, want project source", loaded.Sources["modelThinkingLevels"])
	}

	untrusted, err := LoadSettings(user, project, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(untrusted.Settings.ModelThinkingLevels) != 2 || untrusted.Settings.ModelThinkingLevels["anthropic/b"] != "high" {
		t.Fatalf("untrusted project entries leaked: %v", untrusted.Settings.ModelThinkingLevels)
	}

	nullProject := filepath.Join(dir, "null", ".pi", "settings.json")
	writeJSON(t, nullProject, `{"modelThinkingLevels":null}`)
	withNull, err := LoadSettings(user, nullProject, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(withNull.Settings.ModelThinkingLevels) != 2 || withNull.Settings.ModelThinkingLevels["anthropic/b"] != "high" {
		t.Fatalf("null project map changed user entries: %v", withNull.Settings.ModelThinkingLevels)
	}

	emptyProject := filepath.Join(dir, "empty", ".pi", "settings.json")
	writeJSON(t, emptyProject, `{"modelThinkingLevels":{}}`)
	withEmpty, err := LoadSettings(user, emptyProject, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(withEmpty.Settings.ModelThinkingLevels) != 2 || withEmpty.Settings.ModelThinkingLevels["anthropic/a"] != "low" {
		t.Fatalf("empty project map cleared user entries: %v", withEmpty.Settings.ModelThinkingLevels)
	}
}

func TestLoadSettingsRejectsInvalidModelThinkingLevel(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")
	writeJSON(t, path, `{"modelThinkingLevels":{"anthropic/m":"turbo"}}`)

	_, err := LoadSettings(path, "", false)
	if err == nil {
		t.Fatal("invalid level was accepted")
	}
	if !strings.Contains(err.Error(), "modelThinkingLevels") {
		t.Fatalf("error does not name modelThinkingLevels: %v", err)
	}
}

func TestLoadSettingsRejectsNonObjectModelThinkingLevels(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")
	writeJSON(t, path, `{"modelThinkingLevels":["high"]}`)

	_, err := LoadSettings(path, "", false)
	if err == nil {
		t.Fatal("array value was accepted")
	}
	if !strings.Contains(err.Error(), "modelThinkingLevels") {
		t.Fatalf("error does not name modelThinkingLevels: %v", err)
	}
}

func TestLoadSettingsRejectsNonStringModelThinkingLevel(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")
	writeJSON(t, path, `{"modelThinkingLevels":{"anthropic/m":5}}`)

	_, err := LoadSettings(path, "", false)
	if err == nil {
		t.Fatal("numeric value was accepted")
	}
	if !strings.Contains(err.Error(), "modelThinkingLevels") {
		t.Fatalf("error does not name modelThinkingLevels: %v", err)
	}
}
