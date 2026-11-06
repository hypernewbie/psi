//go:build unix

package app

import (
	"encoding/json"
	"os"
	"path/filepath"
	goruntime "runtime"
	"testing"
)

// TestCtrlTHidesThinkingAndPersistsTheSetting covers the Pi parity key
// binding: the toggle reports the new state and survives a restart.
func TestCtrlTHidesThinkingAndPersistsTheSetting(t *testing.T) {
	if goruntime.GOOS == "windows" {
		t.Skip("app key binding workflow needs a Unix PTY")
	}
	home, ws := t.TempDir(), t.TempDir()
	agentDir := filepath.Join(home, ".pi", "agent")
	settingsPath := filepath.Join(agentDir, "settings.json")
	writeAppFile(t, settingsPath, `{"defaultProvider":"anthropic","defaultModel":"local-model","outputPad":0}`)
	writeAppFile(t, filepath.Join(agentDir, "models.json"), `{"providers":{"anthropic":{"baseUrl":"http://127.0.0.1:9/v1","models":[{"id":"local-model","maxTokens":4096,"contextWindow":8192}]}}}`)
	writeAppFile(t, filepath.Join(agentDir, "auth.json"), `{"anthropic":{"type":"api_key","key":"file-test-key"}}`)

	session := startSettingsApp(t, home, agentDir, ws)
	session.wait("/ commands")
	session.write("\x14")
	session.waitScreen("Thinking blocks: hidden")
	session.quit()

	data, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatal(err)
	}
	var stored struct {
		HideThinkingBlock bool `json:"hideThinkingBlock"`
	}
	if err := json.Unmarshal(data, &stored); err != nil {
		t.Fatal(err)
	}
	if !stored.HideThinkingBlock {
		t.Fatalf("hideThinkingBlock was not persisted:\n%s", data)
	}

	// A second toggle restores the visible default.
	session = startSettingsApp(t, home, agentDir, ws)
	session.wait("/ commands")
	session.write("\x14")
	session.waitScreen("Thinking blocks: visible")
	session.quit()
	data, err = os.ReadFile(settingsPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &stored); err != nil {
		t.Fatal(err)
	}
	if stored.HideThinkingBlock {
		t.Fatalf("hideThinkingBlock was not cleared:\n%s", data)
	}
}
