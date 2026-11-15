package app

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"psi/internal/terminal"
)

func readDefaultThinkingLevel(t *testing.T, r *runtime) (string, bool) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(r.agentDir, "settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	var raw struct {
		Level string `json:"defaultThinkingLevel"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	return raw.Level, raw.Level != ""
}

func TestSaveThinkingDefaultWritesGlobalDefault(t *testing.T) {
	r := testRuntime(t)
	if err := r.saveThinkingDefault("high", &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if got, ok := readDefaultThinkingLevel(t, r); !ok || got != "high" {
		t.Fatalf("stored default = %q (set=%v), want high", got, ok)
	}
	if r.settings.DefaultThinking != "high" || !r.settings.DefaultThinkingSet {
		t.Fatalf("in-memory default = %q (set=%v)", r.settings.DefaultThinking, r.settings.DefaultThinkingSet)
	}
	if r.ov.Thinking != "high" || r.cfg.Thinking != "high" {
		t.Fatalf("session thinking = %q/%q, want high", r.ov.Thinking, r.cfg.Thinking)
	}
}

func TestSaveThinkingDefaultKeepsSessionLevelWhenWriteFails(t *testing.T) {
	r := testRuntime(t)
	path := filepath.Join(r.agentDir, "settings.json")
	content := `{"defaultThinkingLevel":`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	err := r.saveThinkingDefault("high", &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "Saved for this session only") {
		t.Fatalf("error = %v", err)
	}
	if r.ov.Thinking != "high" || r.cfg.Thinking != "high" {
		t.Fatalf("session thinking = %q/%q, want high", r.ov.Thinking, r.cfg.Thinking)
	}
	if r.settings.DefaultThinking != "off" || r.settings.DefaultThinkingSet {
		t.Fatalf("failed write changed defaults: %q (set=%v)", r.settings.DefaultThinking, r.settings.DefaultThinkingSet)
	}
	data, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(data) != content {
		t.Fatalf("malformed file changed: %q", data)
	}
}

func TestSaveThinkingDefaultIsUsedByLaterModelSwitch(t *testing.T) {
	r := testRuntime(t)
	addSettingsModel(r)
	if err := r.saveThinkingDefault("high", &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.command(&terminal.Command{Name: "model", Arg: "anthropic/m2"}, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if r.ov.Thinking != "high" {
		t.Fatalf("switch thinking = %q, want saved global high", r.ov.Thinking)
	}
}

func TestSaveThinkingDefaultRejectsInvalidLevel(t *testing.T) {
	r := testRuntime(t)
	err := r.saveThinkingDefault("turbo", &bytes.Buffer{})
	if err == nil {
		t.Fatal("invalid level was accepted")
	}
	if strings.Contains(err.Error(), "Saved for this session only") {
		t.Fatalf("rejected command claimed a partial save: %v", err)
	}
	if r.ov.Thinking != "off" || r.settings.DefaultThinkingSet {
		t.Fatalf("invalid level changed state: %q (set=%v)", r.ov.Thinking, r.settings.DefaultThinkingSet)
	}
	if _, statErr := os.Stat(filepath.Join(r.agentDir, "settings.json")); !os.IsNotExist(statErr) {
		t.Fatalf("invalid level wrote settings: %v", statErr)
	}
}
