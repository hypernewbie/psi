package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeWorkspace(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, content := range files {
		path := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestLoadWorkspaceFilesScopesPrivateFiles(t *testing.T) {
	dir := writeWorkspace(t, map[string]string{
		"AGENTS.md":   "# Rules\nBe exact.",
		"SOUL.md":     "Persona.",
		"IDENTITY.md": "Identity.",
		"USER.md":     "Private preference.",
		"MEMORY.md":   "Private fact.",
		"TOOLS.md":    "Tool notes.",
	})
	private, err := loadWorkspaceFiles(dir, true)
	if err != nil {
		t.Fatalf("private: %v", err)
	}
	if len(private.contents) != 6 {
		t.Fatalf("private files = %d", len(private.contents))
	}
	shared, err := loadWorkspaceFiles(dir, false)
	if err != nil {
		t.Fatalf("shared: %v", err)
	}
	if _, ok := shared.contents["USER.md"]; ok {
		t.Fatal("shared route loaded USER.md")
	}
	if _, ok := shared.contents["MEMORY.md"]; ok {
		t.Fatal("shared route loaded MEMORY.md")
	}
}

func TestBuildPromptSectionsOrderAndContent(t *testing.T) {
	dir := writeWorkspace(t, map[string]string{
		"AGENTS.md":   "rules",
		"SOUL.md":     "voice",
		"IDENTITY.md": "face",
		"USER.md":     "profile",
		"MEMORY.md":   "memory",
	})
	files, err := loadWorkspaceFiles(dir, true)
	if err != nil {
		t.Fatal(err)
	}
	sections := buildPromptSections(selectedAgent{Identity: identity{Name: "Aster"}}, files, []string{"memory_search", "message"}, nil, "automatic")
	if len(sections) != 4 {
		t.Fatalf("sections = %+v", sections)
	}
	if sections[0].Name != "preamble" || !strings.Contains(sections[0].Body, "Aster") {
		t.Fatalf("preamble = %+v", sections[0])
	}
	context := sections[1].Body
	order := []string{"## AGENTS.md", "## SOUL.md", "## IDENTITY.md", "## USER.md", "## MEMORY.md"}
	last := -1
	for _, heading := range order {
		at := strings.Index(context, heading)
		if at < 0 || at < last {
			t.Fatalf("heading %q order in %q", heading, context)
		}
		last = at
	}
	if strings.Contains(sections[3].Body, "final text is delivered automatically") == false {
		t.Fatalf("messaging = %q", sections[3].Body)
	}
}

func TestLoadWorkspaceFilesRejectsSymlinkEscape(t *testing.T) {
	dir := t.TempDir()
	outside := filepath.Join(t.TempDir(), "secret.md")
	if err := os.WriteFile(outside, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, "AGENTS.md")); err != nil {
		t.Skip("symlinks unavailable")
	}
	if _, err := loadWorkspaceFiles(dir, true); err == nil {
		t.Fatal("expected a confinement error")
	}
}

func TestSystemOverrideAndAddendumReload(t *testing.T) {
	dir := writeWorkspace(t, map[string]string{
		"SYSTEM.md":        "Custom base.",
		"APPEND_SYSTEM.md": "Custom addendum.",
		"AGENTS.md":        "rules",
	})
	files, err := loadWorkspaceFiles(dir, true)
	if err != nil {
		t.Fatal(err)
	}
	sections := buildPromptSections(selectedAgent{}, files, nil, nil, "automatic")
	if sections[0].Body != "Custom base." {
		t.Fatalf("preamble = %q", sections[0].Body)
	}
	if sections[len(sections)-1].Name != "addendum" || sections[len(sections)-1].Body != "Custom addendum." {
		t.Fatalf("addendum = %+v", sections[len(sections)-1])
	}
	if err := os.WriteFile(filepath.Join(dir, "APPEND_SYSTEM.md"), []byte("Edited."), 0o600); err != nil {
		t.Fatal(err)
	}
	files, err = loadWorkspaceFiles(dir, true)
	if err != nil {
		t.Fatal(err)
	}
	edited := buildPromptSections(selectedAgent{}, files, nil, nil, "automatic")
	if edited[len(edited)-1].Body != "Edited." {
		t.Fatalf("reload = %q", edited[len(edited)-1].Body)
	}
}
