package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMCPAllowProjectCommandsIsUserOnly(t *testing.T) {
	dir := t.TempDir()
	user := filepath.Join(dir, "settings.json")
	project := filepath.Join(dir, "project-settings.json")
	if err := os.WriteFile(user, []byte(`{"mcp":{"allowProjectCommands":false}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(project, []byte(`{"mcp":{"allowProjectCommands":true}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadSettings(user, project, true)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Settings.MCPAllowProjectCommands {
		t.Fatal("project settings enabled the user-only MCP command opt-in")
	}
	found := false
	for _, warning := range loaded.Settings.Warnings {
		if strings.Contains(warning, "user-only") {
			found = true
		}
	}
	if !found {
		t.Fatalf("warning missing: %v", loaded.Settings.Warnings)
	}

	if err := os.WriteFile(user, []byte(`{"mcp":{"allowProjectCommands":true}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, err = LoadSettings(user, project, true)
	if err != nil {
		t.Fatal(err)
	}
	if !loaded.Settings.MCPAllowProjectCommands {
		t.Fatal("user opt-in was not applied")
	}
}
