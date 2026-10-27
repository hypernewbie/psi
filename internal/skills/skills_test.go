package skills

import (
	"os"
	"path/filepath"
	"testing"
)

func writeSkill(t *testing.T, dir, name, frontmatter, body string) string {
	t.Helper()
	sub := filepath.Join(dir, name)
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	fp := filepath.Join(sub, "SKILL.md")
	content := "---\n" + frontmatter + "\n---\n" + body + "\n"
	if err := os.WriteFile(fp, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return fp
}

func TestDiscoverDeterministic(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	agent := t.TempDir()
	ws := t.TempDir()
	writeSkill(t, filepath.Join(agent, "skills"), "alpha", "name: alpha\ndescription: Does alpha things", "body alpha")
	writeSkill(t, filepath.Join(ws, ".pi", "skills"), "beta", "name: beta\ndescription: Does beta things", "body beta")
	got, diags := Discover(ws, agent, nil, true)
	if len(diags) != 0 {
		t.Errorf("diags: %+v", diags)
	}
	if len(got) != 2 || got[0].Name != "alpha" || got[1].Name != "beta" {
		t.Errorf("deterministic order failed: %+v", got)
	}
	// Consent deny excludes project skills.
	got2, _ := Discover(ws, agent, nil, false)
	if len(got2) != 1 || got2[0].Name != "alpha" {
		t.Errorf("consent deny: %+v", got2)
	}
}

func TestMalformedAndCollision(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	agent := t.TempDir()
	ws := t.TempDir()
	writeSkill(t, filepath.Join(agent, "skills"), "dup", "name: dup\ndescription: first", "first")
	writeSkill(t, filepath.Join(ws, ".pi", "skills"), "dup2", "name: dup\ndescription: second", "second")
	// Malformed: missing name.
	badDir := filepath.Join(agent, "skills", "bad")
	if err := os.MkdirAll(badDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(badDir, "SKILL.md"), []byte("---\ndescription: no name\n---\nbody\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, diags := Discover(ws, agent, nil, true)
	if len(got) != 1 {
		t.Errorf("collision should keep first: %+v", got)
	}
	if len(diags) < 2 {
		t.Errorf("expected malformed + collision diagnostics, got %+v", diags)
	}
}

func TestAdvertiseExcludesExplicit(t *testing.T) {
	skills := []Skill{
		{Name: "auto", Description: "auto skill", FilePath: "/a/SKILL.md"},
		{Name: "manual", Description: "manual", FilePath: "/b/SKILL.md", Disable: true},
	}
	prompt := FormatForPrompt(skills)
	if !contains(prompt, "auto") || contains(prompt, "manual") {
		t.Errorf("explicit-only must be excluded from advertisement: %q", prompt)
	}
	// Full body never advertised.
	if contains(prompt, "secret-body") {
		t.Errorf("body leaked")
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

func TestInvoke(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	agent := t.TempDir()
	ws := t.TempDir()
	fp := writeSkill(t, filepath.Join(agent, "skills"), "doer", "name: doer\ndescription: Does things", "follow these steps")
	got, _ := Discover(ws, agent, nil, true)
	_ = fp
	out, err := Invoke(got, "doer", "--fast")
	if err != nil || !contains(out, "follow these steps") || !contains(out, "--fast") {
		t.Errorf("invoke: %q %v", out, err)
	}
	if _, err := Invoke(got, "missing", ""); err == nil {
		t.Errorf("missing skill must fail without provider request")
	}
}
