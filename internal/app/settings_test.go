package app

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
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

func TestSettingsPickerHasEightRows(t *testing.T) {
	r := testSettingsRuntime(t)
	picker := r.newSettingsPicker()
	labels := make([]string, len(picker.Items))
	for i, item := range picker.Items {
		labels[i] = item.Label
	}
	want := []string{"Auto-compact", "Editor padding", "Output padding", "Autocomplete max items", "Code block indent", "Quiet startup", "Theme", "Color mode"}
	if strings.Join(labels, "|") != strings.Join(want, "|") {
		t.Fatalf("labels = %v", labels)
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
