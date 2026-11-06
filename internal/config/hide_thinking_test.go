package config

import (
	"path/filepath"
	"testing"
)

func TestHideThinkingBlockLoadsFromUserSettings(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, "home")
	t.Setenv("HOME", home)
	user := filepath.Join(home, ".pi", "agent", "settings.json")
	writeJSON(t, user, `{"hideThinkingBlock":true}`)
	loaded, err := LoadSettings(user, "", true)
	if err != nil {
		t.Fatal(err)
	}
	if !loaded.Settings.HideThinkingBlock {
		t.Fatal("hideThinkingBlock was not loaded")
	}
	if loaded.Sources["hideThinkingBlock"] != "user:"+user {
		t.Fatalf("hideThinkingBlock source = %q", loaded.Sources["hideThinkingBlock"])
	}
}

func TestHideThinkingBlockDefaultsToVisible(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, "home")
	t.Setenv("HOME", home)
	user := filepath.Join(home, ".pi", "agent", "settings.json")
	writeJSON(t, user, `{"outputPad":0}`)
	loaded, err := LoadSettings(user, "", true)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Settings.HideThinkingBlock {
		t.Fatal("hideThinkingBlock must default to false")
	}
}

func TestHideThinkingBlockRejectsNonBoolean(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, "home")
	t.Setenv("HOME", home)
	user := filepath.Join(home, ".pi", "agent", "settings.json")
	writeJSON(t, user, `{"hideThinkingBlock":"yes"}`)
	if _, err := LoadSettings(user, "", true); err == nil {
		t.Fatal("expected an error for a non-boolean hideThinkingBlock")
	}
}
