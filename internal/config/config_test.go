package config

import (
	"os"
	"path/filepath"
	"testing"
)

func writeJSON(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestThemeSettingsMergeAndResourceBases(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, "home")
	t.Setenv("HOME", home)
	userDir := filepath.Join(home, ".pi", "agent")
	projectDir := filepath.Join(root, "workspace", ".pi")
	user := filepath.Join(userDir, "settings.json")
	project := filepath.Join(projectDir, "settings.json")
	writeJSON(t, user, `{"theme":"user-light/user-dark","themes":["themes/*.json","!themes/disabled.json","+~/shared.json"],"terminal":{"trueColor":false}}`)
	writeJSON(t, project, `{"theme":"project-light/project-dark","themes":["+local/theme.json"],"terminal":{"trueColor":"auto"}}`)
	loaded, err := LoadSettings(user, project, true)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Settings.Theme != "project-light/project-dark" || loaded.Settings.TerminalTrueColor != "auto" {
		t.Fatalf("project scalar precedence: %+v", loaded.Settings)
	}
	wantPaths := []string{
		filepath.Join(userDir, "themes", "*.json"),
		"!" + filepath.Join(userDir, "themes", "disabled.json"),
		"+" + filepath.Join(home, "shared.json"),
		"+" + filepath.Join(projectDir, "local", "theme.json"),
	}
	if len(loaded.Settings.ThemePaths) != len(wantPaths) {
		t.Fatalf("theme paths=%q", loaded.Settings.ThemePaths)
	}
	for i := range wantPaths {
		if loaded.Settings.ThemePaths[i] != wantPaths[i] {
			t.Errorf("theme path %d=%q, want %q", i, loaded.Settings.ThemePaths[i], wantPaths[i])
		}
	}
	if loaded.Sources["theme"] != "project:"+project || loaded.Sources["terminal.trueColor"] != "project:"+project {
		t.Fatalf("theme setting sources=%v", loaded.Sources)
	}
	untrusted, err := LoadSettings(user, project, false)
	if err != nil {
		t.Fatal(err)
	}
	if untrusted.Settings.Theme != "user-light/user-dark" || len(untrusted.Settings.ThemePaths) != 3 || untrusted.Settings.TerminalTrueColor != "false" {
		t.Fatalf("untrusted project settings leaked: %+v", untrusted.Settings)
	}
}

func TestThemeSettingDefaultsAndRejectsInvalidShapes(t *testing.T) {
	defaults := defaultSettings()
	if defaults.Theme != "system" || defaults.TerminalTrueColor != "auto" {
		t.Fatalf("theme defaults=%+v", defaults)
	}
	for _, value := range []string{`{"theme":null}`, `{"themes":null}`, `{"terminal":null}`, `{"terminal":{"trueColor":null}}`, `{"terminal":{"trueColor":1}}`, `{"terminal":{"trueColor":"yes"}}`} {
		path := filepath.Join(t.TempDir(), "settings.json")
		writeJSON(t, path, value)
		if _, err := LoadSettings(path, "", false); err == nil {
			t.Errorf("accepted invalid theme settings %s", value)
		}
	}
}

func TestLoadMergePrecedence(t *testing.T) {
	dir := t.TempDir()
	user := filepath.Join(dir, "user.json")
	proj := filepath.Join(dir, "proj.json")
	writeJSON(t, user, `{"defaultProvider":"anthropic","defaultModel":"m1","defaultThinkingLevel":"low","sessionDir":"/tmp/u","compaction":{"enabled":true,"reserveTokens":100,"keepRecentTokens":200}}`)
	writeJSON(t, proj, `{"defaultModel":"m2","compaction":{"keepRecentTokens":999}}`)
	res, err := LoadSettings(user, proj, true)
	if err != nil {
		t.Fatal(err)
	}
	if res.Settings.DefaultProvider != "anthropic" || res.Settings.DefaultModel != "m2" {
		t.Errorf("merge wrong: %+v", res.Settings)
	}
	if res.Settings.Compaction.ReserveTokens != 100 || res.Settings.Compaction.KeepRecentTokens != 999 {
		t.Errorf("compaction merge wrong: %+v", res.Settings.Compaction)
	}
	// Declining project consent keeps user values.
	res2, err := LoadSettings(user, proj, false)
	if err != nil {
		t.Fatal(err)
	}
	if res2.Settings.DefaultModel != "m1" {
		t.Errorf("consent deny should keep user model, got %q", res2.Settings.DefaultModel)
	}
}

func TestMalformedDiagnostics(t *testing.T) {
	dir := t.TempDir()
	bad := filepath.Join(dir, "bad.json")
	writeJSON(t, bad, `{"defaultProvider":}`)
	if _, err := LoadSettings(bad, "", true); err == nil {
		t.Errorf("malformed should error with source file")
	} else if !contains(err.Error(), "bad.json") {
		t.Errorf("error must cite file: %v", err)
	}
}

func TestDefaultToolsSemantics(t *testing.T) {
	// Plain replaces.
	got, err := resolveTools([]string{"read", "bash"})
	if err != nil || len(got) != 2 {
		t.Errorf("replace: %v %v", got, err)
	}
	// +/- modifies.
	got, err = resolveTools([]string{"-write"})
	if err != nil || len(got) != 3 || containsStr(got, "write") {
		t.Errorf("remove: %v %v", got, err)
	}
	got, err = resolveTools([]string{"+write"})
	if err != nil || len(got) != 4 {
		t.Errorf("add: %v %v", got, err)
	}
	if _, err := resolveTools([]string{"exec"}); err == nil {
		t.Errorf("unknown tool must fail")
	}
}

func containsStr(list []string, s string) bool {
	for _, e := range list {
		if e == s {
			return true
		}
	}
	return false
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (func() bool {
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				return true
			}
		}
		return false
	})()
}

func TestUnsupportedProviderRejected(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "s.json")
	writeJSON(t, p, `{"defaultProvider":"openai"}`)
	loaded, err := LoadSettings(p, "", true)
	if err != nil {
		t.Fatal(err)
	}
	// Validate the selected provider after CLI/session overrides, not while
	// reading shared defaults that an explicit override can replace.
	if _, err := (&ModelsFile{}).SelectModel(loaded.Settings.DefaultProvider, "m"); err == nil || !contains(err.Error(), "anthropic") {
		t.Errorf("active unsupported provider must fail with actionable error, got %v", err)
	}
}

func TestCredentialResolution(t *testing.T) {
	t.Setenv("PSI_TEST_KEY", "secret123")
	got, err := ResolveCredential("$PSI_TEST_KEY")
	if err != nil || got != "secret123" {
		t.Errorf("env ref: %q %v", got, err)
	}
	got, err = ResolveCredential("prefix-${PSI_TEST_KEY}-suffix")
	if err != nil || got != "prefix-secret123-suffix" {
		t.Errorf("template: %q %v", got, err)
	}
	got, err = ResolveCredential("$$PSI_TEST_KEY")
	if err != nil || got != "$PSI_TEST_KEY" {
		t.Errorf("escape: %q %v", got, err)
	}
	if _, err := ResolveCredential("!echo hi"); err == nil {
		t.Errorf("command must be rejected, never executed")
	}
	if _, err := ResolveCredential("$DOES_NOT_EXIST_XYZ"); err == nil {
		t.Errorf("missing env must fail")
	}
	if r := RedactCredential("abcdef123"); r == "abcdef123" {
		t.Errorf("redaction must hide: %q", r)
	}
}

func TestStartupPrecedence(t *testing.T) {
	settings := defaultSettings()
	settings.DefaultProvider = "anthropic"
	settings.DefaultModel = "base"
	settings.DefaultThinking = "low"
	o := ResolveStartup("anthropic", "cli-model", "", "", false, "anthropic", "restored", "medium", settings, "")
	if o.Model != "cli-model" {
		t.Errorf("CLI model should win, got %q", o.Model)
	}
	if o.Explain == "" {
		t.Errorf("conflicting explicit value needs one concise explanation")
	}
	o2 := ResolveStartup("", "", "", "/flag", false, "", "", "", settings, "/env")
	if o2.SessionDir != "/flag" {
		t.Errorf("CLI session-dir wins, got %q", o2.SessionDir)
	}
}

func TestInstructionDiscovery(t *testing.T) {
	ws := t.TempDir()
	home := t.TempDir()
	writeCfgFile(t, filepath.Join(ws, "AGENTS.md"), "workspace agents")
	writeCfgFile(t, filepath.Join(ws, "AGENTS.override.md"), "override wins")
	writeCfgFile(t, filepath.Join(home, "AGENTS.md"), "home agents")
	writeCfgFile(t, filepath.Join(ws, ".pi", "APPEND_SYSTEM.md"), "appendix")
	got, err := DiscoverInstructions(ws, home, true)
	if err != nil {
		t.Fatal(err)
	}
	paths := []string{}
	for _, f := range got {
		paths = append(paths, f.Path)
	}
	// Override replaces workspace AGENTS.md.
	for _, p := range paths {
		if p == filepath.Join(ws, "AGENTS.md") {
			t.Errorf("override should replace AGENTS.md: %v", paths)
		}
	}
	foundOverride, foundAppend := false, false
	for _, f := range got {
		if f.Kind == "agents-override" {
			foundOverride = true
		}
		if f.Kind == "append-system" {
			foundAppend = true
		}
	}
	if !foundOverride || !foundAppend {
		t.Errorf("missing override/append: %v", paths)
	}
	// No duplicates.
	seen := map[string]bool{}
	for _, p := range paths {
		if seen[p] {
			t.Errorf("duplicate %s", p)
		}
		seen[p] = true
	}
	// Consent deny excludes project files.
	got2, _ := DiscoverInstructions(ws, home, false)
	for _, f := range got2 {
		if f.Kind == "append-system" || f.Kind == "system" {
			t.Errorf("consent deny leaked project file %s", f.Path)
		}
	}
}

func writeCfgFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	_ = t
}

func TestAssemblePromptDeterministic(t *testing.T) {
	ins := []InstructionFile{{Path: "/a", Content: "A", Kind: "agents"}, {Path: "/b", Content: "B", Kind: "agents"}}
	p1 := AssemblePrompt("/ws", ins, []string{"<skill>x</skill>"}, []string{"read", "bash"})
	p2 := AssemblePrompt("/ws", ins, []string{"<skill>x</skill>"}, []string{"read", "bash"})
	if p1 != p2 || p1 == "" {
		t.Errorf("prompt must be deterministic and non-empty")
	}
}
