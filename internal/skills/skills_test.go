package skills

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/hypernewbie/psi/internal/testutil/leakcheck"
)

func TestMain(m *testing.M) {
	leakcheck.VerifyTestMain(m)
}

func setSyntheticHome(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	if runtime.GOOS == "windows" {
		volume := filepath.VolumeName(home)
		t.Setenv("USERPROFILE", home)
		t.Setenv("HOMEDRIVE", volume)
		t.Setenv("HOMEPATH", strings.TrimPrefix(home, volume))
	}
}

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
	setSyntheticHome(t)
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

func TestRepoAddSlashCommandSkill(t *testing.T) {
	setSyntheticHome(t)
	relPath := filepath.Join(".agents", "skills", "add-slash-command", "SKILL.md")
	body, err := os.ReadFile(filepath.Join("..", "..", relPath))
	if err != nil {
		t.Fatal(err)
	}
	workspace := t.TempDir()
	dstPath := filepath.Join(workspace, relPath)
	if err := os.MkdirAll(filepath.Dir(dstPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dstPath, body, 0o644); err != nil {
		t.Fatal(err)
	}

	got, diags := Discover(workspace, t.TempDir(), nil, true)
	if len(diags) != 0 {
		t.Fatalf("diagnostics: %+v", diags)
	}
	if len(got) != 1 {
		t.Fatalf("discovered %d skills, want 1: %+v", len(got), got)
	}
	if got[0].Name != "add-slash-command" {
		t.Fatalf("skill name = %q, want add-slash-command", got[0].Name)
	}
	if got[0].FilePath != dstPath {
		t.Fatalf("skill path = %q, want %q", got[0].FilePath, dstPath)
	}
	if prompt := FormatForPrompt(got); prompt != "" {
		t.Fatalf("explicit-only skill was advertised in the prompt: %q", prompt)
	}
	if _, err := Invoke(got, "add-slash-command", ""); err != nil {
		t.Fatalf("invoke add-slash-command: %v", err)
	}
}

func TestMalformedAndCollision(t *testing.T) {
	setSyntheticHome(t)
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
	setSyntheticHome(t)
	agent := t.TempDir()
	ws := t.TempDir()
	fp := writeSkill(t, filepath.Join(agent, "skills"), "doer", "name: doer\ndescription: Does things", "  follow these steps  \n")
	got, _ := Discover(ws, agent, nil, true)
	base := filepath.Dir(fp)
	block := "<skill name=\"doer\" location=\"" + fp + "\">\n" +
		"References are relative to " + base + ".\n\n" +
		"follow these steps\n</skill>"
	out, err := Invoke(got, "doer", "--fast")
	if err != nil || out != block+"\n\n--fast" {
		t.Errorf("invoke with args: %q %v, want %q", out, err, block+"\n\n--fast")
	}
	out, err = Invoke(got, "doer", "")
	if err != nil || out != block {
		t.Errorf("invoke without args: %q %v, want %q", out, err, block)
	}
	out, err = Invoke(got, "doer", "  --fast  ")
	if err != nil || out != block+"\n\n--fast" {
		t.Errorf("invoke trimmed args: %q %v, want %q", out, err, block+"\n\n--fast")
	}
	out, err = Invoke(got, "doer", "  \t ")
	if err != nil || out != block {
		t.Errorf("blank args added a tail: %q %v", out, err)
	}
	if _, err := Invoke(got, "missing", ""); err == nil {
		t.Errorf("missing skill must fail without provider request")
	}
}

func TestSkillValidationAndEdgeCases(t *testing.T) {
	setSyntheticHome(t)
	agent := t.TempDir()
	ws := t.TempDir()
	skillsDir := filepath.Join(agent, "skills")

	// 1. Invalid names: > 64 chars, uppercase, underscores, leading/trailing hyphens
	longName := strings.Repeat("a", 65)
	writeSkill(t, skillsDir, "toolong", "name: "+longName+"\ndescription: desc", "body")
	writeSkill(t, skillsDir, "upper", "name: UpperCase\ndescription: desc", "body")
	writeSkill(t, skillsDir, "under", "name: has_underscore\ndescription: desc", "body")
	writeSkill(t, skillsDir, "leadhyph", "name: -leading\ndescription: desc", "body")
	writeSkill(t, skillsDir, "trailhyph", "name: trailing-\ndescription: desc", "body")

	// 2. Long description (> 1024 chars) and missing description
	longDesc := strings.Repeat("d", 1025)
	writeSkill(t, skillsDir, "longdesc", "name: longdesc\ndescription: "+longDesc, "body")
	writeSkill(t, skillsDir, "nodesc", "name: nodesc", "body")

	// 3. CRLF line endings
	crlfDir := filepath.Join(skillsDir, "crlf-skill")
	if err := os.MkdirAll(crlfDir, 0o755); err != nil {
		t.Fatal(err)
	}
	crlfContent := "---\r\nname: crlf-skill\r\ndescription: Windows line endings\r\n---\r\nbody content\r\n"
	if err := os.WriteFile(filepath.Join(crlfDir, "SKILL.md"), []byte(crlfContent), 0o644); err != nil {
		t.Fatal(err)
	}

	// 4. Standalone root .md file in skillsDir (not in subfolder)
	rootMd := filepath.Join(skillsDir, "root-skill.md")
	if err := os.WriteFile(rootMd, []byte("---\nname: root-skill\ndescription: Standalone root md\n---\nbody"), 0o644); err != nil {
		t.Fatal(err)
	}

	// 5. Node modules and hidden dirs ignored
	nodeDir := filepath.Join(skillsDir, "node_modules", "ignore-me")
	if err := os.MkdirAll(nodeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(nodeDir, "SKILL.md"), []byte("---\nname: ignored\ndescription: should be ignored\n---\n"), 0o644)

	hiddenDir := filepath.Join(skillsDir, ".hidden-skill")
	if err := os.MkdirAll(hiddenDir, 0o755); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(hiddenDir, "SKILL.md"), []byte("---\nname: hidden\ndescription: should be ignored\n---\n"), 0o644)

	// 6. Symlink cycle
	cycleDir := filepath.Join(skillsDir, "cycle-dir")
	if err := os.MkdirAll(cycleDir, 0o755); err != nil {
		t.Fatal(err)
	}
	_ = os.Symlink(skillsDir, filepath.Join(cycleDir, "link-to-parent"))

	got, diags := Discover(ws, agent, nil, true)
	names := make(map[string]bool)
	for _, s := range got {
		names[s.Name] = true
	}

	// crlf-skill and root-skill should succeed
	if !names["crlf-skill"] {
		t.Errorf("crlf-skill should have been discovered")
	}
	if !names["root-skill"] {
		t.Errorf("root-skill should have been discovered")
	}

	// Ignored/invalid should NOT be discovered
	if names["ignored"] || names["hidden"] || names["toolong"] || names["upper"] || names["under"] || names["longdesc"] || names["nodesc"] {
		t.Errorf("invalid or ignored skills were discovered: %+v", names)
	}

	if len(diags) == 0 {
		t.Errorf("expected diagnostics for invalid skills")
	}
}

func TestOversizedSkillFile(t *testing.T) {
	setSyntheticHome(t)
	agent := t.TempDir()
	ws := t.TempDir()
	skillsDir := filepath.Join(agent, "skills")

	bigDir := filepath.Join(skillsDir, "huge")
	if err := os.MkdirAll(bigDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// > 256 KiB
	bigContent := "---\nname: huge\ndescription: huge\n---\n" + strings.Repeat("x", 260*1024)
	if err := os.WriteFile(filepath.Join(bigDir, "SKILL.md"), []byte(bigContent), 0o644); err != nil {
		t.Fatal(err)
	}

	got, diags := Discover(ws, agent, nil, true)
	for _, s := range got {
		if s.Name == "huge" {
			t.Errorf("huge skill (>256KB) should have been rejected")
		}
	}
	foundDiag := false
	for _, d := range diags {
		if strings.Contains(d.Message, "too large") || strings.Contains(d.Message, "oversized") {
			foundDiag = true
			break
		}
	}
	if !foundDiag {
		t.Errorf("expected oversized diagnostic, got: %+v", diags)
	}
}

func TestDiscoverPathsLoadsOnlyExplicitDirectories(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	global := filepath.Join(home, ".agents", "skills", "global")
	if err := os.MkdirAll(global, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(global, "SKILL.md"), []byte("---\nname: global\ndescription: hidden\n---\nbody\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	explicit := filepath.Join(t.TempDir(), "skills", "local")
	if err := os.MkdirAll(explicit, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(explicit, "SKILL.md"), []byte("---\nname: local\ndescription: visible\n---\nbody\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	found, diags := DiscoverPaths([]string{explicit})
	if len(diags) != 0 {
		t.Fatalf("diagnostics = %+v", diags)
	}
	if len(found) != 1 || found[0].Name != "local" {
		t.Fatalf("skills = %+v", found)
	}
}

func TestDiscoverPathsBoundsTheSkillCount(t *testing.T) {
	root := t.TempDir()
	for i := 0; i < 257; i++ {
		dir := filepath.Join(root, fmt.Sprintf("skill-%03d", i))
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		body := fmt.Sprintf("---\nname: skill-%03d\ndescription: bounded\n---\nbody\n", i)
		if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	found, diags := DiscoverPaths([]string{root})
	if len(found) != 256 || len(diags) == 0 {
		t.Fatalf("found = %d, diagnostics = %+v", len(found), diags)
	}
}
