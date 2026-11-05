package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"psi/internal/config"
	"psi/internal/session"
)

func TestReloadResourcesRefreshesInstructionsAndSkills(t *testing.T) {
	// The global .agents fallback must not leak into the fixture.
	t.Setenv("HOME", t.TempDir())
	cwd, agentDir := t.TempDir(), t.TempDir()
	store, err := session.New(session.OpenOptions{Cwd: cwd, AgentDir: agentDir, Persist: false})
	if err != nil {
		t.Fatal(err)
	}
	r := &runtime{store: store, agentDir: agentDir, projectTrusted: true, settings: config.Settings{DefaultTools: []string{"read"}}}
	skillDir := filepath.Join(cwd, ".pi", "skills", "fresh")
	if err := os.MkdirAll(skillDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cwd, "AGENTS.md"), []byte("new instructions"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("---\nname: fresh\ndescription: fresh skill\n---\ncontent\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := r.reloadResources(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(r.prompt, "new instructions") || !strings.Contains(r.prompt, "fresh") || len(r.skills) != 1 || r.cfg.System != r.prompt {
		t.Fatalf("resources not refreshed: skills=%v prompt=%s", r.skills, r.prompt)
	}
	r.projectTrusted = false
	if err := r.reloadResources(); err != nil {
		t.Fatal(err)
	}
	if len(r.skills) != 0 {
		t.Fatalf("untrusted project skill loaded: %v", r.skills)
	}
}

func TestReloadResourcesHonorsDisabledOptions(t *testing.T) {
	cwd, agentDir := t.TempDir(), t.TempDir()
	store, err := session.New(session.OpenOptions{Cwd: cwd, AgentDir: agentDir})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cwd, "AGENTS.md"), []byte("disabled instruction"), 0600); err != nil {
		t.Fatal(err)
	}
	r := &runtime{store: store, agentDir: agentDir, options: Options{NoContextFiles: true, NoSkills: true}}
	if err := r.reloadResources(); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(r.prompt, "disabled instruction") || len(r.skills) != 0 {
		t.Fatalf("disabled resources loaded: %s", r.prompt)
	}
}
