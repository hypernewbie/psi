package app

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hypernewbie/psi/internal/agent"
	"github.com/hypernewbie/psi/internal/config"
	"github.com/hypernewbie/psi/internal/provider"
	"github.com/hypernewbie/psi/internal/terminal"
	pitheme "github.com/hypernewbie/psi/internal/theme"
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

// testContextUsageRuntime primes one completed 79k/1k provider response on a
// 200k window so the footer has an exact 80k context anchor to display.
func testContextUsageRuntime(t *testing.T, mode string) (*runtime, *terminal.Console) {
	t.Helper()
	r := testRuntime(t)
	r.settings.ContextUsageDisplay = mode
	r.cfg.Model.ContextWindow = 200000
	r.cfg.AutoCompact = false
	fake := &provider.Fake{Responses: []*provider.Response{{
		Text:       "context usage answer",
		Usage:      provider.Usage{Input: 79000, Output: 1000, TotalTokens: 80000},
		StopReason: "end_turn",
	}}}
	if err := agent.New(r.store, r.reg, fake, nil).Submit(context.Background(), "show context usage", r.cfg); err != nil {
		t.Fatal(err)
	}
	input, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	console := terminal.NewConsole(input, io.Discard)
	t.Cleanup(func() {
		console.Close()
		input.Close()
	})
	r.refreshPresentation(console)
	return r, console
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
		"Context usage: percent",
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

func TestSettingsPickerRows(t *testing.T) {
	r := testSettingsRuntime(t)
	picker := r.newSettingsPicker()
	labels := make([]string, len(picker.Items))
	for i, item := range picker.Items {
		labels[i] = item.Label
	}
	want := []string{"Auto-compact", "Editor padding", "Output padding", "Hide thinking", "Soft wrap", "Autocomplete max items", "Code block indent", "Quiet startup", "Theme", "Color mode", "Context usage", "Default thinking level per model"}
	if strings.Join(labels, "|") != strings.Join(want, "|") {
		t.Fatalf("labels = %v", labels)
	}
	last := picker.Items[len(picker.Items)-1]
	if last.ID != "model-thinking" || last.Values[last.Selected].Label != "none" {
		t.Fatalf("model thinking row = %+v", last)
	}
	if last.Description != "Override the default thinking level for specific models" {
		t.Fatalf("model thinking description = %q", last.Description)
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
		{id: "soft-wrap", value: terminal.SettingsValue{Label: "off", Value: false}, path: []string{"softWrap"}, want: "false"},
		{id: "quiet-startup", value: terminal.SettingsValue{Label: "on", Value: true}, path: []string{"quietStartup"}, want: "true"},
		{id: "theme", value: terminal.SettingsValue{Label: "light", Value: "light"}, path: []string{"theme"}, want: `"light"`},
		{id: "color-mode", value: terminal.SettingsValue{Label: "false", Value: false}, path: []string{"terminal", "trueColor"}, want: "false"},
		{id: "context-usage", value: terminal.SettingsValue{Label: "tokens", Value: "tokens"}, path: []string{"contextUsageDisplay"}, want: `"tokens"`},
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
		r.settings.TerminalTrueColor != "false" || r.settings.ContextUsageDisplay != "tokens" || r.settings.SoftWrap {
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

func TestApplyContextUsageRejectsInvalidPickerValues(t *testing.T) {
	r := testSettingsRuntime(t)
	settingsPath := filepath.Join(r.agentDir, "settings.json")
	original := []byte(`{"unknown":{"keep":true},"outputPad":0}`)
	if err := os.WriteFile(settingsPath, original, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, value := range []terminal.SettingsValue{
		{Label: "", Value: ""},
		{Label: "bogus", Value: "bogus"},
		{Label: "tokens", Value: 1},
	} {
		path, err := r.applySetting("context-usage", value, nil)
		if err == nil {
			t.Errorf("accepted invalid context usage value %#v", value)
			continue
		}
		if path != nil {
			t.Errorf("invalid context usage value %#v returned persistence path %v", value, path)
		}
		if r.settings.ContextUsageDisplay != "" {
			t.Errorf("invalid context usage value %#v changed memory to %q", value, r.settings.ContextUsageDisplay)
		}
		data, readErr := os.ReadFile(settingsPath)
		if readErr != nil {
			t.Fatal(readErr)
		}
		if !bytes.Equal(data, original) {
			t.Errorf("invalid context usage value %#v changed the settings file: %s", value, data)
		}
	}
}

func TestContextUsageDisplaySaveFailureKeepsSessionChange(t *testing.T) {
	r, console := testContextUsageRuntime(t, "")
	settingsPath := filepath.Join(r.agentDir, "settings.json")
	malformed := []byte(`{"broken":`)
	if err := os.WriteFile(settingsPath, malformed, 0o600); err != nil {
		t.Fatal(err)
	}
	path, err := r.applySetting("context-usage", terminal.SettingsValue{Label: "tokens", Value: "tokens"}, console)
	if err != nil {
		t.Fatal(err)
	}
	if r.settings.ContextUsageDisplay != "tokens" {
		t.Fatalf("session display = %q, want tokens", r.settings.ContextUsageDisplay)
	}
	if err := r.persistSettingsValue(path, "tokens"); err == nil {
		t.Fatal("malformed settings file accepted a save")
	}
	r.refreshPresentation(console)
	if got := r.footerSnapshot.ContextUsageLabel; got != "80k" {
		t.Fatalf("session caption after save failure = %q, want 80k", got)
	}
	if got := r.footerSnapshot.ContextEstimate; got != "80k/200k" {
		t.Fatalf("session estimate after save failure = %q, want 80k/200k", got)
	}
	if r.footerSnapshot.ContextPercent != 40 || r.contextTokens != 80000 || r.contextWindow != 200000 {
		t.Fatalf("save failure changed context telemetry: percent=%v tokens=%v window=%v",
			r.footerSnapshot.ContextPercent, r.contextTokens, r.contextWindow)
	}
	data, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(data, malformed) {
		t.Fatalf("failed save changed the malformed file: %q", data)
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

func addSettingsModel(r *runtime) {
	meta := r.models.Providers["anthropic"]
	meta.Models = append(meta.Models, config.ModelMeta{ID: "m2", Reasoning: true, MaxTokens: 16384})
	r.models.Providers["anthropic"] = meta
}

func readModelThinkingLevels(t *testing.T, r *runtime) map[string]string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(r.agentDir, "settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	var raw struct {
		Levels map[string]string `json:"modelThinkingLevels"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	return raw.Levels
}

func TestApplyModelThinkingLevelWritesOverride(t *testing.T) {
	r := testRuntime(t)
	if err := r.applyModelThinkingLevel("anthropic/m", "high", &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if got := readModelThinkingLevels(t, r)["anthropic/m"]; got != "high" {
		t.Fatalf("stored level = %q, want high", got)
	}
	if got := r.settings.ModelThinkingLevels["anthropic/m"]; got != "high" {
		t.Fatalf("in-memory level = %q, want high", got)
	}
}

func TestApplyModelThinkingLevelClearsOverride(t *testing.T) {
	t.Run("configured global default", func(t *testing.T) {
		r := testRuntime(t)
		r.settings.DefaultThinking = "low"
		r.settings.DefaultThinkingSet = true
		if err := r.applyModelThinkingLevel("anthropic/m", "high", &bytes.Buffer{}); err != nil {
			t.Fatal(err)
		}
		if err := r.applyModelThinkingLevel("anthropic/m", modelThinkingClearChoice, &bytes.Buffer{}); err != nil {
			t.Fatal(err)
		}
		if _, exists := readModelThinkingLevels(t, r)["anthropic/m"]; exists {
			t.Fatal("override remains in the file")
		}
		if len(r.settings.ModelThinkingLevels) != 0 {
			t.Fatalf("in-memory overrides remain: %v", r.settings.ModelThinkingLevels)
		}
		if r.ov.Thinking != "low" {
			t.Fatalf("session thinking = %q, want global low", r.ov.Thinking)
		}
	})

	t.Run("unset global default", func(t *testing.T) {
		r := testRuntime(t)
		if err := r.applyModelThinkingLevel("anthropic/m", "high", &bytes.Buffer{}); err != nil {
			t.Fatal(err)
		}
		if err := r.applyModelThinkingLevel("anthropic/m", modelThinkingClearChoice, &bytes.Buffer{}); err != nil {
			t.Fatal(err)
		}
		if _, exists := readModelThinkingLevels(t, r)["anthropic/m"]; exists {
			t.Fatal("override remains in the file")
		}
		if r.ov.Thinking != "off" {
			t.Fatalf("session thinking = %q, want off", r.ov.Thinking)
		}
	})

	t.Run("empty configured global default", func(t *testing.T) {
		r := testRuntime(t)
		r.settings.DefaultThinking = ""
		r.settings.DefaultThinkingSet = true
		if err := r.applyModelThinkingLevel("anthropic/m", "high", &bytes.Buffer{}); err != nil {
			t.Fatal(err)
		}
		if err := r.applyModelThinkingLevel("anthropic/m", modelThinkingClearChoice, &bytes.Buffer{}); err != nil {
			t.Fatal(err)
		}
		if r.ov.Thinking != "off" {
			t.Fatalf("session thinking = %q, want off", r.ov.Thinking)
		}
	})
}

func TestApplyModelThinkingLevelAppliesToCurrentModelOnly(t *testing.T) {
	r := testRuntime(t)
	addSettingsModel(r)
	before := r.ov.Thinking
	if err := r.applyModelThinkingLevel("anthropic/m2", "high", &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if r.ov.Thinking != before {
		t.Fatalf("other-model override changed session thinking to %q", r.ov.Thinking)
	}
	if got := r.settings.ModelThinkingLevels["anthropic/m2"]; got != "high" {
		t.Fatalf("other-model override = %q, want high", got)
	}
	if err := r.applyModelThinkingLevel("anthropic/m", "medium", &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if r.ov.Thinking != "medium" {
		t.Fatalf("current-model override left session thinking at %q", r.ov.Thinking)
	}
}

func TestApplyModelThinkingLevelKeepsSessionChangeWhenWriteFails(t *testing.T) {
	writeMalformed := func(t *testing.T, r *runtime) string {
		t.Helper()
		path := filepath.Join(r.agentDir, "settings.json")
		content := `{"modelThinkingLevels":`
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		return content
	}

	t.Run("set current model", func(t *testing.T) {
		r := testRuntime(t)
		content := writeMalformed(t, r)
		err := r.applyModelThinkingLevel("anthropic/m", "high", &bytes.Buffer{})
		if err == nil || !strings.Contains(err.Error(), "Saved for this session only") {
			t.Fatalf("error = %v", err)
		}
		if r.ov.Thinking != "high" {
			t.Fatalf("session thinking = %q, want high", r.ov.Thinking)
		}
		if len(r.settings.ModelThinkingLevels) != 0 {
			t.Fatalf("failed write changed the map: %v", r.settings.ModelThinkingLevels)
		}
		data, readErr := os.ReadFile(filepath.Join(r.agentDir, "settings.json"))
		if readErr != nil {
			t.Fatal(readErr)
		}
		if string(data) != content {
			t.Fatalf("malformed file changed: %q", data)
		}
	})

	t.Run("clear current model", func(t *testing.T) {
		r := testRuntime(t)
		r.settings.ModelThinkingLevels = map[string]string{"anthropic/m": "high"}
		content := writeMalformed(t, r)
		err := r.applyModelThinkingLevel("anthropic/m", modelThinkingClearChoice, &bytes.Buffer{})
		if err == nil || !strings.Contains(err.Error(), "Saved for this session only") {
			t.Fatalf("error = %v", err)
		}
		if r.ov.Thinking != "off" {
			t.Fatalf("session thinking = %q, want off", r.ov.Thinking)
		}
		if r.settings.ModelThinkingLevels["anthropic/m"] != "high" {
			t.Fatalf("failed clear changed the map: %v", r.settings.ModelThinkingLevels)
		}
		data, readErr := os.ReadFile(filepath.Join(r.agentDir, "settings.json"))
		if readErr != nil {
			t.Fatal(readErr)
		}
		if string(data) != content {
			t.Fatalf("malformed file changed: %q", data)
		}
	})

	t.Run("other model", func(t *testing.T) {
		r := testRuntime(t)
		addSettingsModel(r)
		before := r.ov.Thinking
		writeMalformed(t, r)
		err := r.applyModelThinkingLevel("anthropic/m2", "high", &bytes.Buffer{})
		if err == nil || !strings.Contains(err.Error(), "Failed to save model thinking default") {
			t.Fatalf("error = %v", err)
		}
		if strings.Contains(err.Error(), "Saved for this session only") {
			t.Fatalf("other-model failure claimed a session save: %v", err)
		}
		if r.ov.Thinking != before {
			t.Fatalf("other-model failure changed session thinking to %q", r.ov.Thinking)
		}
		if len(r.settings.ModelThinkingLevels) != 0 {
			t.Fatalf("failed write changed the map: %v", r.settings.ModelThinkingLevels)
		}
	})
}

func TestApplyModelThinkingLevelRejectsInvalidInput(t *testing.T) {
	for _, tc := range []struct {
		name   string
		target string
		level  string
	}{
		{"empty target", "", "high"},
		{"missing model", "anthropic/", "high"},
		{"missing provider", "/m", "high"},
		{"no separator", "m", "high"},
		{"unknown model", "anthropic/missing", "high"},
		{"unsupported level", "anthropic/m", "turbo"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := testRuntime(t)
			before := r.ov.Thinking
			if err := r.applyModelThinkingLevel(tc.target, tc.level, &bytes.Buffer{}); err == nil {
				t.Fatal("invalid input was accepted")
			}
			if r.ov.Thinking != before {
				t.Fatalf("invalid input changed session thinking to %q", r.ov.Thinking)
			}
			if len(r.settings.ModelThinkingLevels) != 0 {
				t.Fatalf("invalid input changed the map: %v", r.settings.ModelThinkingLevels)
			}
			if _, err := os.Stat(filepath.Join(r.agentDir, "settings.json")); !os.IsNotExist(err) {
				t.Fatalf("invalid input wrote settings: %v", err)
			}
		})
	}
}

func TestWriteUserSettingsRequiresAgentDirectory(t *testing.T) {
	r := testRuntime(t)
	r.agentDir = ""
	t.Chdir(t.TempDir())
	err := r.writeUserSettings([]config.SettingsUpdate{{Path: []string{"theme"}, Value: json.RawMessage(`"dark"`)}})
	if err == nil || !strings.Contains(err.Error(), "agent directory is unavailable") {
		t.Fatalf("error = %v", err)
	}
	if _, statErr := os.Stat("settings.json"); !os.IsNotExist(statErr) {
		t.Fatalf("relative settings file was written: %v", statErr)
	}
}

func TestModelThinkingRowIsInert(t *testing.T) {
	r := testRuntime(t)
	path, err := r.applySetting("model-thinking", terminal.SettingsValue{Label: "none", Value: "none"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if path != nil {
		t.Fatalf("path = %v, want nil", path)
	}
	if _, err := os.Stat(filepath.Join(r.agentDir, "settings.json")); !os.IsNotExist(err) {
		t.Fatalf("row apply wrote settings: %v", err)
	}
}

func TestApplyModelThinkingLevelWritesUserSettingsOnly(t *testing.T) {
	r := testRuntime(t)
	projectPath := filepath.Join(r.store.Cwd(), ".pi", "settings.json")
	const projectSettings = `{"modelThinkingLevels":{"anthropic/m":"off"},"theme":"dark"}`
	writeAppFile(t, projectPath, projectSettings)

	if err := r.applyModelThinkingLevel("anthropic/m", "high", &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(projectPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != projectSettings {
		t.Fatalf("project settings changed: %q", data)
	}
	if got := readModelThinkingLevels(t, r)["anthropic/m"]; got != "high" {
		t.Fatalf("user override = %q, want high", got)
	}
}

func TestApplyModelThinkingLevelClearKeepsOtherOverrides(t *testing.T) {
	r := testRuntime(t)
	addSettingsModel(r)
	if err := r.applyModelThinkingLevel("anthropic/m2", "medium", &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if err := r.applyModelThinkingLevel("anthropic/m", "high", &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if err := r.applyModelThinkingLevel("anthropic/m", modelThinkingClearChoice, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if got := r.settings.ModelThinkingLevels["anthropic/m2"]; got != "medium" {
		t.Fatalf("in-memory sibling = %q, want medium", got)
	}
	if got := readModelThinkingLevels(t, r)["anthropic/m2"]; got != "medium" {
		t.Fatalf("stored sibling = %q, want medium", got)
	}
	if _, exists := readModelThinkingLevels(t, r)["anthropic/m"]; exists {
		t.Fatal("cleared key remains in the file")
	}
}

func TestModelThinkingLevelItemsIncludeClearRow(t *testing.T) {
	r := testRuntime(t)
	meta, err := r.models.SelectModel("anthropic", "m")
	if err != nil {
		t.Fatal(err)
	}
	levels := provider.SupportedThinking(providerModel("anthropic", meta))
	if len(levels) == 0 {
		t.Fatal("fixture supports no thinking levels")
	}
	items := modelThinkingLevelItems(r, "anthropic", "m", meta)
	if len(items) != len(levels) {
		t.Fatalf("items without override = %d, want %d", len(items), len(levels))
	}
	for i, level := range levels {
		if items[i].ID != level {
			t.Fatalf("item %d = %q, want %q", i, items[i].ID, level)
		}
	}

	r.settings.DefaultThinking = "low"
	r.settings.DefaultThinkingSet = true
	r.settings.ModelThinkingLevels = map[string]string{"anthropic/m": "high"}
	items = modelThinkingLevelItems(r, "anthropic", "m", meta)
	if len(items) != len(levels)+1 {
		t.Fatalf("items with override = %d, want %d", len(items), len(levels)+1)
	}
	clear := items[len(items)-1]
	if clear.ID != modelThinkingClearChoice {
		t.Fatalf("clear ID = %q, want %q", clear.ID, modelThinkingClearChoice)
	}
	if clear.FirstMessage != "Revert to global default (low)" {
		t.Fatalf("clear description = %q", clear.FirstMessage)
	}

	r.settings.DefaultThinkingSet = false
	items = modelThinkingLevelItems(r, "anthropic", "m", meta)
	clear = items[len(items)-1]
	if clear.FirstMessage != "Revert to global default (off)" {
		t.Fatalf("unset clear description = %q", clear.FirstMessage)
	}

	r.settings.DefaultThinkingSet = true
	r.settings.DefaultThinking = ""
	items = modelThinkingLevelItems(r, "anthropic", "m", meta)
	clear = items[len(items)-1]
	if clear.FirstMessage != "Revert to global default (off)" {
		t.Fatalf("empty clear description = %q", clear.FirstMessage)
	}
}
