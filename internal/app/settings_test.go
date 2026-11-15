package app

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"psi/internal/config"
	"psi/internal/terminal"
	pitheme "psi/internal/theme"
)

func testSettingsRuntime(t *testing.T) *runtime {
	t.Helper()
	r := testRuntime(t)
	// Mirror the defaults LoadSettings installs.
	r.settings.OutputPad = 0
	r.settings.AutocompleteMaxVisible = 5
	r.settings.MarkdownCodeBlockIndent = "  "
	r.settings.Compaction.Enabled = true
	return r
}

func TestSettingsCommandListsCurrentValues(t *testing.T) {
	r := testSettingsRuntime(t)
	var out bytes.Buffer
	quit, err := r.command(&terminal.Command{Name: "settings"}, &out)
	if err != nil || quit {
		t.Fatalf("quit=%v err=%v", quit, err)
	}
	got := out.String()
	for _, want := range []string{
		"Auto-compact: on",
		"Editor padding: 0",
		"Output padding: 0",
		"Autocomplete max items: 5",
		"Code block indent: 2",
		"Quiet startup: off",
		"Theme: system",
		"Color mode: auto",
		"Language: English",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in %q", want, got)
		}
	}
}

func TestSettingsCommandRejectsArguments(t *testing.T) {
	r := testRuntime(t)
	if _, err := r.command(&terminal.Command{Name: "settings", Arg: "theme"}, &bytes.Buffer{}); err == nil {
		t.Fatal("argument accepted")
	}
}

func TestSettingsPickerRowsAndLanguage(t *testing.T) {
	r := testSettingsRuntime(t)
	picker := r.newSettingsPicker()
	labels := make([]string, len(picker.Items))
	for i, item := range picker.Items {
		labels[i] = item.Label
	}
	want := []string{"Auto-compact", "Editor padding", "Output padding", "Hide thinking", "Autocomplete max items", "Code block indent", "Quiet startup", "Theme", "Color mode", "Language"}
	if strings.Join(labels, "|") != strings.Join(want, "|") {
		t.Fatalf("labels = %v", labels)
	}
	item := picker.Items[len(picker.Items)-1]
	if item.ID != "language" {
		t.Fatalf("language item ID = %q", item.ID)
	}
	values := make([]string, 0, len(item.Values))
	for _, value := range item.Values {
		values = append(values, fmt.Sprint(value.Value)+"="+value.Label)
	}
	if got, want := strings.Join(values, "|"), "en=English|zh=简体中文"; got != want {
		t.Fatalf("language values = %q, want %q", got, want)
	}
}

func TestApplySettingRows(t *testing.T) {
	r := testSettingsRuntime(t)
	r.themeCatalog = pitheme.LoadCatalog(pitheme.CatalogOptions{Mode: pitheme.ColorModeTruecolor})
	r.themeSetting = "dark"
	r.themeMode = pitheme.ColorModeTruecolor

	cases := []struct {
		id    string
		value terminal.SettingsValue
		path  []string
		want  string
	}{
		{id: "editor-padding", value: terminal.SettingsValue{Label: "2", Value: 2}, path: []string{"editorPaddingX"}, want: "2"},
		{id: "output-padding", value: terminal.SettingsValue{Label: "1", Value: 1}, path: []string{"outputPad"}, want: "1"},
		{id: "autocomplete-max", value: terminal.SettingsValue{Label: "7", Value: 7}, path: []string{"autocompleteMaxVisible"}, want: "7"},
		{id: "code-indent", value: terminal.SettingsValue{Label: "4", Value: "    "}, path: []string{"markdown", "codeBlockIndent"}, want: `"    "`},
		{id: "quiet-startup", value: terminal.SettingsValue{Label: "on", Value: true}, path: []string{"quietStartup"}, want: "true"},
		{id: "theme", value: terminal.SettingsValue{Label: "light", Value: "light"}, path: []string{"theme"}, want: `"light"`},
		{id: "color-mode", value: terminal.SettingsValue{Label: "false", Value: false}, path: []string{"terminal", "trueColor"}, want: "false"},
	}
	for _, c := range cases {
		path, err := r.applySetting(c.id, c.value, nil)
		if err != nil {
			t.Fatalf("%s: %v", c.id, err)
		}
		if strings.Join(path, ".") != strings.Join(c.path, ".") {
			t.Fatalf("%s path = %v", c.id, path)
		}
		if err := r.persistSettingsValue(path, c.value.Value); err != nil {
			t.Fatalf("%s: %v", c.id, err)
		}
	}

	if r.settings.EditorPaddingX != 2 || r.settings.OutputPad != 1 || r.settings.AutocompleteMaxVisible != 7 ||
		r.settings.MarkdownCodeBlockIndent != "    " || !r.settings.QuietStartup || r.settings.Theme != "light" ||
		r.settings.TerminalTrueColor != "false" {
		t.Fatalf("settings = %+v", r.settings)
	}
	if r.themeMode != pitheme.ColorMode256 {
		t.Fatalf("mode = %v", r.themeMode)
	}

	data, err := os.ReadFile(filepath.Join(r.agentDir, "settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]json.RawMessage
	if err := json.Unmarshal(data, &document); err != nil {
		t.Fatal(err)
	}
	lookup := func(path ...string) string {
		raw := document[path[0]]
		for _, part := range path[1:] {
			var nested map[string]json.RawMessage
			if err := json.Unmarshal(raw, &nested); err != nil {
				t.Fatalf("%v: %v", path, err)
			}
			raw = nested[part]
		}
		return string(raw)
	}
	for _, c := range cases {
		if got := lookup(c.path...); got != c.want {
			t.Fatalf("%s = %s, want %s\n%s", strings.Join(c.path, "."), got, c.want, data)
		}
	}
}

func TestApplyAutoCompactIsSessionOnly(t *testing.T) {
	r := testRuntime(t)
	path, err := r.applySetting("auto-compact", terminal.SettingsValue{Label: "on", Value: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if path != nil {
		t.Fatalf("path = %v, want session-only", path)
	}
	if !r.settings.Compaction.Enabled || !r.cfg.AutoCompact {
		t.Fatalf("settings=%v cfg=%v", r.settings.Compaction.Enabled, r.cfg.AutoCompact)
	}
	if _, err := os.Stat(filepath.Join(r.agentDir, "settings.json")); !os.IsNotExist(err) {
		t.Fatalf("auto-compact wrote a file: %v", err)
	}
}

func TestApplyThemeWithCLIOverrideKeepsDisplay(t *testing.T) {
	r := testRuntime(t)
	r.options.UseTheme = "dark"
	r.themeSetting = "dark"
	r.themeCatalog = pitheme.LoadCatalog(pitheme.CatalogOptions{Mode: pitheme.ColorModeTruecolor})
	r.settings.Theme = "dark"
	path, err := r.applySetting("theme", terminal.SettingsValue{Label: "light", Value: "light"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(path, ".") != "theme" {
		t.Fatalf("path = %v", path)
	}
	if r.settings.Theme != "light" {
		t.Fatalf("stored theme = %q", r.settings.Theme)
	}
	if r.themeSetting != "dark" {
		t.Fatalf("display override changed: %q", r.themeSetting)
	}
}

func TestThemeValuesIncludePair(t *testing.T) {
	r := testRuntime(t)
	r.settings.Theme = "light/dark"
	values, selected := r.themeSettingValues()
	if values[selected].Value != "light/dark" {
		t.Fatalf("selected = %v", values[selected])
	}
}

func TestApplySettingOutputPadding(t *testing.T) {
	r := testRuntime(t)
	path, err := r.applySetting("output-padding", terminal.SettingsValue{Label: "1", Value: 1}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if r.settings.OutputPad != 1 {
		t.Fatalf("outputPad = %d", r.settings.OutputPad)
	}
	if strings.Join(path, ".") != "outputPad" {
		t.Fatalf("path = %v", path)
	}
	if _, err := r.applySetting("missing", terminal.SettingsValue{Value: 1}, nil); err == nil {
		t.Fatal("unknown setting accepted")
	}
}

func TestPersistSettingsValueWritesAgentFile(t *testing.T) {
	r := testRuntime(t)
	path := filepath.Join(r.agentDir, "settings.json")
	if err := os.WriteFile(path, []byte(`{"keep":true,"outputPad":0}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := r.persistSettingsValue([]string{"outputPad"}, 1); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded["outputPad"] != float64(1) {
		t.Fatalf("outputPad = %v", decoded["outputPad"])
	}
	if decoded["keep"] != true {
		t.Fatalf("unknown key lost: %s", data)
	}
	loaded, err := config.LoadSettings(path, "", false)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Settings.OutputPad != 1 {
		t.Fatalf("loaded outputPad = %d", loaded.Settings.OutputPad)
	}
}

func TestApplyThemeChangesDisplay(t *testing.T) {
	r := testSettingsRuntime(t)
	r.themeCatalog = pitheme.LoadCatalog(pitheme.CatalogOptions{Mode: pitheme.ColorModeTruecolor})
	r.themeMode = pitheme.ColorModeTruecolor
	r.themeSetting = "dark"
	r.settings.Theme = "dark"
	r.footerKey = "stale"
	if err := r.applyThemeSetting(nil, "light"); err != nil {
		t.Fatal(err)
	}
	if r.settings.Theme != "light" || r.themeSetting != "light" || r.themeName != "light" || r.theme == nil {
		t.Fatalf("settings=%q setting=%q name=%q", r.settings.Theme, r.themeSetting, r.themeName)
	}
	if r.footerKey != "" {
		t.Fatal("footer cache not invalidated")
	}
}

func TestApplyThemeFailureKeepsState(t *testing.T) {
	r := testSettingsRuntime(t)
	r.themeCatalog = pitheme.LoadCatalog(pitheme.CatalogOptions{Mode: pitheme.ColorModeTruecolor})
	r.themeMode = pitheme.ColorModeTruecolor
	r.themeSetting = "dark"
	r.settings.Theme = "dark"
	if err := r.applyThemeSetting(nil, "missing-theme"); err == nil {
		t.Fatal("unknown theme applied")
	}
	if r.settings.Theme != "dark" || r.themeSetting != "dark" {
		t.Fatalf("state changed: settings=%q setting=%q", r.settings.Theme, r.themeSetting)
	}
}

func TestApplyColorModeRebuildsCatalog(t *testing.T) {
	r := testSettingsRuntime(t)
	r.settings.Theme = "system"
	r.themeSetting = "system"
	r.themeCatalog = pitheme.LoadCatalog(pitheme.CatalogOptions{Mode: pitheme.ColorModeTruecolor})
	r.themeMode = pitheme.ColorModeTruecolor
	if err := r.applyColorMode(nil, "false"); err != nil {
		t.Fatal(err)
	}
	if r.settings.TerminalTrueColor != "false" || r.themeMode != pitheme.ColorMode256 {
		t.Fatalf("trueColor=%q mode=%v", r.settings.TerminalTrueColor, r.themeMode)
	}
}

func TestOutputPaddingRefreshesPresentation(t *testing.T) {
	r := testSettingsRuntime(t)
	input, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	defer input.Close()
	var out bytes.Buffer
	console := terminal.NewConsole(input, &out)
	defer console.Close()
	r.refreshPresentation(console)
	if r.footerSnapshot.Display.OutputPad != 0 {
		t.Fatalf("initial padding = %d", r.footerSnapshot.Display.OutputPad)
	}
	if _, err := r.applySetting("output-padding", terminal.SettingsValue{Label: "1", Value: 1}, console); err != nil {
		t.Fatal(err)
	}
	r.footerKey = ""
	r.refreshPresentation(console)
	if r.footerSnapshot.Display.OutputPad != 1 {
		t.Fatalf("refreshed padding = %d", r.footerSnapshot.Display.OutputPad)
	}
}

func TestApplyLanguageSetting(t *testing.T) {
	r := testSettingsRuntime(t)
	r.settings.Language = config.LanguageEnglish
	r.settings.DefaultTools = []string{"read"}
	r.prompt, r.sections = "OLD_PROMPT", []config.PromptSection{{Name: "old", Body: "OLD"}}
	r.cfg.System, r.cfg.Sections = "OLD_CFG", []config.PromptSection{{Name: "old-cfg", Body: "OLD_CFG"}}
	settingsPath := filepath.Join(r.agentDir, "settings.json")
	if err := os.WriteFile(settingsPath, []byte(`{"unknown":{"keep":true},"language":"en"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	path, err := r.applySetting("language", terminal.SettingsValue{Label: "简体中文", Value: config.LanguageChinese}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(path, ".") != "language" {
		t.Fatalf("path = %v", path)
	}
	assertPromptLanguage(t, r.prompt, config.LanguageChinese)
	if r.cfg.System != r.prompt || !reflect.DeepEqual(r.cfg.Sections, r.sections) {
		t.Fatal("cfg prompt fields were not rebuilt with the prompt")
	}
	if strings.Join(r.settings.DefaultTools, ",") != "read" {
		t.Fatalf("tools changed: %v", r.settings.DefaultTools)
	}
	if err := r.persistSettingsValue(path, config.LanguageChinese); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatal(err)
	}
	var stored map[string]any
	if err := json.Unmarshal(data, &stored); err != nil {
		t.Fatal(err)
	}
	if stored["language"] != config.LanguageChinese {
		t.Fatalf("language not persisted: %s", data)
	}
	unknown, _ := stored["unknown"].(map[string]any)
	if unknown["keep"] != true {
		t.Fatalf("unknown key lost: %s", data)
	}

	t.Run("consented project override returns after reload", func(t *testing.T) {
		r := testSettingsRuntime(t)
		r.projectTrusted = true
		r.settings.Language = config.LanguageChinese
		r.settings.DefaultTools = []string{"read"}
		userPath := filepath.Join(r.agentDir, "settings.json")
		projectPath := filepath.Join(r.store.Cwd(), ".pi", "settings.json")
		writeAppFile(t, userPath, `{"language":"en","defaultTools":["read"]}`)
		writeAppFile(t, projectPath, `{"language":"zh"}`)
		if err := r.reloadResources(); err != nil {
			t.Fatal(err)
		}
		assertPromptLanguage(t, r.prompt, config.LanguageChinese)

		path, err := r.applySetting("language", terminal.SettingsValue{Label: "English", Value: config.LanguageEnglish}, nil)
		if err != nil {
			t.Fatal(err)
		}
		assertPromptLanguage(t, r.prompt, config.LanguageEnglish)
		if err := r.persistSettingsValue(path, config.LanguageEnglish); err != nil {
			t.Fatal(err)
		}
		userData, err := os.ReadFile(userPath)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(userData), `"language":"en"`) {
			t.Fatalf("menu choice did not update the user file: %s", userData)
		}
		projectData, err := os.ReadFile(projectPath)
		if err != nil {
			t.Fatal(err)
		}
		if string(projectData) != `{"language":"zh"}` {
			t.Fatalf("project file changed: %s", projectData)
		}
		if err := r.reloadResources(); err != nil {
			t.Fatal(err)
		}
		assertPromptLanguage(t, r.prompt, config.LanguageChinese)
	})

	t.Run("invalid values keep state", func(t *testing.T) {
		for _, override := range []string{"", config.LanguageChinese} {
			r := testSettingsRuntime(t)
			r.options.Language = override
			r.settings.Language = config.LanguageEnglish
			r.prompt, r.sections = "OLD_PROMPT", []config.PromptSection{{Name: "old", Body: "OLD"}}
			r.cfg.System, r.cfg.Sections = "OLD_CFG", []config.PromptSection{{Name: "old-cfg", Body: "OLD_CFG"}}
			before := r.settings
			for _, value := range []terminal.SettingsValue{{Label: "fr", Value: "fr"}, {Label: "true", Value: true}, {Label: "ZH", Value: "ZH"}} {
				path, err := r.applySetting("language", value, nil)
				if err == nil || path != nil {
					t.Fatalf("override %q accepted %v: path=%v err=%v", override, value, path, err)
				}
			}
			if r.prompt != "OLD_PROMPT" || r.cfg.System != "OLD_CFG" || !reflect.DeepEqual(r.settings, before) {
				t.Fatalf("invalid value changed state: %+v", r.settings)
			}
			if _, err := os.Stat(filepath.Join(r.agentDir, "settings.json")); !os.IsNotExist(err) {
				t.Fatalf("invalid value wrote a file: %v", err)
			}
		}
	})
}

func TestApplyLanguageFailureKeepsState(t *testing.T) {
	r := testSettingsRuntime(t)
	r.settings.DefaultTools = []string{"read"}
	settingsPath := filepath.Join(r.agentDir, "settings.json")
	writeAppFile(t, settingsPath, `{"language":"en","defaultTools":["read"]}`)
	if err := r.reloadResources(); err != nil {
		t.Fatal(err)
	}
	beforeSettingsData, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatal(err)
	}
	beforePrompt, beforeSections, beforeSkills := r.prompt, r.sections, r.skills
	beforeSystem, beforeCfgSections := r.cfg.System, r.cfg.Sections
	beforeSettings := r.settings

	if err := os.Mkdir(filepath.Join(r.agentDir, "AGENTS.override.md"), 0o700); err != nil {
		t.Fatal(err)
	}
	path, err := r.applySetting("language", terminal.SettingsValue{Label: "简体中文", Value: config.LanguageChinese}, nil)
	if err == nil || path != nil {
		t.Fatalf("instruction read error accepted: path=%v err=%v", path, err)
	}
	afterSettingsData, readErr := os.ReadFile(settingsPath)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(afterSettingsData) != string(beforeSettingsData) {
		t.Fatalf("settings file changed on failure: %s", afterSettingsData)
	}
	if r.prompt != beforePrompt || !reflect.DeepEqual(r.sections, beforeSections) || !reflect.DeepEqual(r.skills, beforeSkills) ||
		r.cfg.System != beforeSystem || !reflect.DeepEqual(r.cfg.Sections, beforeCfgSections) || !reflect.DeepEqual(r.settings, beforeSettings) {
		t.Fatal("failed language change modified live state")
	}
}

func TestApplyLanguageCLIOverride(t *testing.T) {
	r := testSettingsRuntime(t)
	r.settings.Language = config.LanguageEnglish
	r.options.Language = config.LanguageChinese
	r.prompt, r.sections = "OLD_PROMPT", []config.PromptSection{{Name: "old", Body: "OLD"}}
	r.cfg.System = "OLD_CFG"
	path, err := r.applySetting("language", terminal.SettingsValue{Label: "简体中文", Value: config.LanguageChinese}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(path, ".") != "language" {
		t.Fatalf("path = %v", path)
	}
	if r.settings.Language != config.LanguageChinese {
		t.Fatalf("stored default = %q", r.settings.Language)
	}
	if r.prompt != "OLD_PROMPT" || r.cfg.System != "OLD_CFG" {
		t.Fatal("CLI override rebuilt the active prompt")
	}
	if effectivePromptLanguage(r.options, r.settings) != config.LanguageChinese {
		t.Fatal("CLI override no longer effective")
	}
}

func TestLanguagePersistenceFailureKeepsSession(t *testing.T) {
	r := testSettingsRuntime(t)
	r.settings.DefaultTools = []string{"read"}
	settingsPath := filepath.Join(r.agentDir, "settings.json")
	writeAppFile(t, settingsPath, `{"language":"en","defaultTools":["read"]}`)
	if err := r.reloadResources(); err != nil {
		t.Fatal(err)
	}
	corrupt := []byte(`{"broken":`)
	if err := os.WriteFile(settingsPath, corrupt, 0o600); err != nil {
		t.Fatal(err)
	}
	path, err := r.applySetting("language", terminal.SettingsValue{Label: "简体中文", Value: config.LanguageChinese}, nil)
	if err != nil {
		t.Fatal(err)
	}
	assertPromptLanguage(t, r.prompt, config.LanguageChinese)
	if err := r.persistSettingsValue(path, config.LanguageChinese); err == nil {
		t.Fatal("corrupt settings file was overwritten")
	}
	after, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(corrupt) {
		t.Fatalf("corrupt file changed: %q", after)
	}
	assertPromptLanguage(t, r.prompt, config.LanguageChinese)
}
