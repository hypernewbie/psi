package app

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"psi/internal/config"
	"psi/internal/session"
	pitheme "psi/internal/theme"
)

func TestThemeReloadIsTransactionalAndRetainsLastGoodSnapshot(t *testing.T) {
	root := t.TempDir()
	agentDir := filepath.Join(root, "agent")
	themeDir := filepath.Join(agentDir, "themes")
	if err := os.MkdirAll(themeDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(agentDir, "settings.json"), []byte(`{"theme":"selected"}`), 0600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(themeDir, "selected.json")
	writeTheme := func(color string) []byte {
		t.Helper()
		data, err := pitheme.BuiltinJSON("dark")
		if err != nil {
			t.Fatal(err)
		}
		var document map[string]any
		if err := json.Unmarshal(data, &document); err != nil {
			t.Fatal(err)
		}
		document["name"] = "selected"
		document["colors"].(map[string]any)["accent"] = color
		encoded, err := json.Marshal(document)
		if err != nil {
			t.Fatal(err)
		}
		return encoded
	}
	if err := os.WriteFile(path, writeTheme("#123456"), 0600); err != nil {
		t.Fatal(err)
	}
	store, err := session.New(session.OpenOptions{Cwd: root, SessionDir: filepath.Join(root, "sessions"), AgentDir: agentDir})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Unlock()
	mode := pitheme.ColorMode256
	initial, name, fallback := resolveThemeSnapshot(pitheme.LoadCatalog(themeCatalogOptions(config.Settings{Theme: "selected"}, Options{}, agentDir, root, false, mode)), "selected", "dark", pitheme.TerminalColors{}, 0, mode)
	if initial == nil || fallback != "" || name != "selected" {
		t.Fatalf("initial theme=%v name=%q fallback=%q", initial, name, fallback)
	}
	r := &runtime{store: store, agentDir: agentDir, theme: initial, themeMode: mode, themeAppearance: "dark"}
	if err := r.reloadTheme(); err != nil {
		t.Fatal(err)
	}
	good := r.theme
	if good == initial || good.Name != "selected" {
		t.Fatalf("successful reload did not install a resolved snapshot: %p %p %q", initial, good, good.Name)
	}
	if err := os.WriteFile(path, []byte(`{"name":"selected","colors":{"accent":"not-a-color"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := r.reloadTheme(); err == nil {
		t.Fatal("invalid theme reload succeeded")
	}
	if r.theme != good {
		t.Fatal("failed reload replaced the last good immutable theme")
	}
}
