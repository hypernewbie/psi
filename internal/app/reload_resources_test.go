package app

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/hypernewbie/psi/internal/config"
	"github.com/hypernewbie/psi/internal/session"
	"github.com/hypernewbie/psi/internal/skills"
)

func TestReloadResourcesRefreshesInstructionsAndSkills(t *testing.T) {
	// The global .agents fallback must not leak into the fixture.
	t.Setenv("HOME", t.TempDir())
	cwd, agentDir := t.TempDir(), t.TempDir()
	store, err := session.New(session.OpenOptions{Cwd: cwd, AgentDir: agentDir, Persist: false})
	if err != nil {
		t.Fatal(err)
	}
	r := &runtime{store: store, agentDir: agentDir, projectTrust: projectTrusted, settings: config.Settings{DefaultTools: []string{"read"}}}
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
	r.projectTrust = projectUntrusted
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

func TestReloadResourcesInstallsMCPOptInTransactionally(t *testing.T) {
	cwd, agentDir := t.TempDir(), t.TempDir()
	store, err := session.New(session.OpenOptions{Cwd: cwd, AgentDir: agentDir})
	if err != nil {
		t.Fatal(err)
	}
	r := &runtime{store: store, agentDir: agentDir, projectTrust: projectTrusted, settings: config.Settings{DefaultTools: []string{"read"}}}
	settingsPath := filepath.Join(agentDir, "settings.json")
	if err := os.WriteFile(settingsPath, []byte(`{"mcp":{"allowProjectCommands":true}}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := r.reloadResources(); err != nil {
		t.Fatal(err)
	}
	if !r.settings.MCPAllowProjectCommands {
		t.Fatal("MCP opt-in was not installed")
	}
	if err := os.WriteFile(settingsPath, []byte(`{"mcp":`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := r.reloadResources(); err == nil {
		t.Fatal("malformed settings must fail the reload")
	}
	if !r.settings.MCPAllowProjectCommands {
		t.Fatal("failed reload changed the MCP opt-in")
	}
}

func TestReloadResourcesAppliesQueueModes(t *testing.T) {
	cwd, agentDir := t.TempDir(), t.TempDir()
	store, err := session.New(session.OpenOptions{Cwd: cwd, AgentDir: agentDir})
	if err != nil {
		t.Fatal(err)
	}
	r := &runtime{store: store, agentDir: agentDir, projectTrust: projectTrusted, settings: config.Settings{
		DefaultTools: []string{"read"}, SteeringMode: "one-at-a-time", FollowUpMode: "one-at-a-time",
	}}
	settingsPath := filepath.Join(agentDir, "settings.json")
	if err := os.WriteFile(settingsPath, []byte(`{"steeringMode":"all","followUpMode":"all"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := r.reloadResources(); err != nil {
		t.Fatal(err)
	}
	if r.settings.SteeringMode != "all" || r.settings.FollowUpMode != "all" {
		t.Fatalf("queue modes not reloaded: steering=%q followUp=%q", r.settings.SteeringMode, r.settings.FollowUpMode)
	}
	// A failed reload keeps the last valid modes instead of reverting to defaults.
	if err := os.WriteFile(settingsPath, []byte(`{"steeringMode":`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := r.reloadResources(); err == nil {
		t.Fatal("malformed settings must fail the reload")
	}
	if r.settings.SteeringMode != "all" || r.settings.FollowUpMode != "all" {
		t.Fatalf("failed reload changed queue modes: steering=%q followUp=%q", r.settings.SteeringMode, r.settings.FollowUpMode)
	}
}

func TestReloadLanguage(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", t.TempDir())
	cwd, agentDir := t.TempDir(), t.TempDir()
	store, err := session.New(session.OpenOptions{Cwd: cwd, AgentDir: agentDir})
	if err != nil {
		t.Fatal(err)
	}
	r := &runtime{store: store, agentDir: agentDir, settings: config.Settings{DefaultTools: []string{"read"}, Language: config.LanguageEnglish}}
	settingsPath := filepath.Join(agentDir, "settings.json")

	writeSettings := func(language string) {
		t.Helper()
		if err := os.WriteFile(settingsPath, []byte(`{"language":"`+language+`"}`), 0600); err != nil {
			t.Fatal(err)
		}
	}

	writeSettings(config.LanguageEnglish)
	if err := r.reloadResources(); err != nil {
		t.Fatal(err)
	}
	assertPromptLanguage(t, r.prompt, config.LanguageEnglish)
	if r.settings.Language != config.LanguageEnglish {
		t.Fatalf("language = %q", r.settings.Language)
	}

	writeSettings(config.LanguageChinese)
	if err := r.reloadResources(); err != nil {
		t.Fatal(err)
	}
	assertPromptLanguage(t, r.prompt, config.LanguageChinese)
	if r.settings.Language != config.LanguageChinese || r.cfg.System != r.prompt {
		t.Fatalf("Chinese reload not installed: language=%q cfg=%q", r.settings.Language, r.cfg.System)
	}

	writeSettings(config.LanguageEnglish)
	if err := r.reloadResources(); err != nil {
		t.Fatal(err)
	}
	assertPromptLanguage(t, r.prompt, config.LanguageEnglish)

	// An active CLI override survives reload and keeps the loaded setting
	// distinct from the effective prompt language.
	r.options.Language = config.LanguageChinese
	writeSettings(config.LanguageEnglish)
	if err := r.reloadResources(); err != nil {
		t.Fatal(err)
	}
	assertPromptLanguage(t, r.prompt, config.LanguageChinese)
	if r.settings.Language != config.LanguageEnglish {
		t.Fatalf("loaded default = %q, want en", r.settings.Language)
	}

	// A CLI override changes the prompt language, not the settings contract:
	// malformed settings still fail the reload and leave the old state.
	if err := os.WriteFile(settingsPath, []byte(`{"language":`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := r.reloadResources(); err == nil {
		t.Fatal("malformed settings were hidden by the CLI override")
	}
	assertPromptLanguage(t, r.prompt, config.LanguageChinese)
	if r.settings.Language != config.LanguageEnglish || r.cfg.System != r.prompt {
		t.Fatalf("failed reload changed state: language=%q cfg=%q", r.settings.Language, r.cfg.System)
	}
}

func TestReloadLanguageAtomicity(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", t.TempDir())
	cwd, agentDir := t.TempDir(), t.TempDir()
	store, err := session.New(session.OpenOptions{Cwd: cwd, AgentDir: agentDir})
	if err != nil {
		t.Fatal(err)
	}
	writeAppFile(t, filepath.Join(agentDir, "skills", "local", "SKILL.md"), "---\nname: local\ndescription: LOCAL_SKILL\n---\nbody\n")
	settingsPath := filepath.Join(agentDir, "settings.json")
	writeAppFile(t, settingsPath, `{"language":"en","steeringMode":"one-at-a-time","followUpMode":"one-at-a-time"}`)
	r := &runtime{store: store, agentDir: agentDir, settings: config.Settings{DefaultTools: []string{"read"}}}
	if err := r.reloadResources(); err != nil {
		t.Fatal(err)
	}
	beforePrompt, beforeSections, beforeSkills := r.prompt, r.sections, r.skills
	beforeSystem, beforeCfgSections := r.cfg.System, r.cfg.Sections
	beforeSettings := r.settings

	writeAppFile(t, settingsPath, `{"language":"zh","steeringMode":"all","followUpMode":"all"}`)
	guardPath := filepath.Join(agentDir, "AGENTS.override.md")
	if err := os.Mkdir(guardPath, 0700); err != nil {
		t.Fatal(err)
	}
	if err := r.reloadResources(); err == nil {
		t.Fatal("instruction read error did not fail reload")
	}
	assertReloadLanguageUnchanged(t, r, beforePrompt, beforeSections, beforeSkills, beforeSystem, beforeCfgSections, beforeSettings)
	if err := os.Remove(guardPath); err != nil {
		t.Fatal(err)
	}

	for _, payload := range []string{`{"language":`, `{"language":"fr"}`, `{"language":null,"steeringMode":"all","followUpMode":"all"}`} {
		writeAppFile(t, settingsPath, payload)
		if err := r.reloadResources(); err == nil {
			t.Fatalf("settings %s did not fail reload", payload)
		}
		assertReloadLanguageUnchanged(t, r, beforePrompt, beforeSections, beforeSkills, beforeSystem, beforeCfgSections, beforeSettings)
	}

	writeAppFile(t, settingsPath, `{"language":"zh","steeringMode":"one-at-a-time","followUpMode":"one-at-a-time"}`)
	if err := r.reloadResources(); err != nil {
		t.Fatal(err)
	}
	assertPromptLanguage(t, r.prompt, config.LanguageChinese)
}

func assertReloadLanguageUnchanged(t *testing.T, r *runtime, prompt string, sections []config.PromptSection, skills []skills.Skill, system string, cfgSections []config.PromptSection, settings config.Settings) {
	t.Helper()
	if r.prompt != prompt || !reflect.DeepEqual(r.sections, sections) || !reflect.DeepEqual(r.skills, skills) {
		t.Fatal("failed reload changed prompt resources")
	}
	if r.cfg.System != system || !reflect.DeepEqual(r.cfg.Sections, cfgSections) {
		t.Fatal("failed reload changed cfg prompt fields")
	}
	if r.settings.Language != settings.Language || !reflect.DeepEqual(r.settings.Skills, settings.Skills) || r.settings.SteeringMode != settings.SteeringMode || r.settings.FollowUpMode != settings.FollowUpMode {
		t.Fatalf("failed reload changed settings: %+v", r.settings)
	}
}
