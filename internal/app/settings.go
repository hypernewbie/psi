package app

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	goruntime "runtime"
	"strconv"
	"strings"

	"psi/internal/config"
	"psi/internal/terminal"
	pitheme "psi/internal/theme"
)

// settingsEntry is one live setting. persist is nil for session-only rows.
type settingsEntry struct {
	item    terminal.SettingsItem
	persist []string
	apply   func(*runtime, *terminal.Console, terminal.SettingsValue) error
}

func (r *runtime) settingsEntries() []settingsEntry {
	autoCompact := 0
	if r.settings.Compaction.Enabled {
		autoCompact = 1
	}
	editorPadding := min(max(r.settings.EditorPaddingX, 0), 3)
	outputPad := 0
	if r.settings.OutputPad != 0 {
		outputPad = 1
	}
	autocomplete, autocompleteSelected := intSettingValues(
		[]string{"3", "5", "7", "10", "15", "20"},
		[]int{3, 5, 7, 10, 15, 20},
		r.settings.AutocompleteMaxVisible,
	)
	indentValues := []terminal.SettingsValue{{Label: "0", Value: ""}, {Label: "2", Value: "  "}, {Label: "4", Value: "    "}}
	indentSelected := 0
	indentFound := false
	for i, candidate := range indentValues {
		if candidate.Value == r.settings.MarkdownCodeBlockIndent {
			indentSelected, indentFound = i, true
		}
	}
	if !indentFound {
		indentValues = append(indentValues, terminal.SettingsValue{Label: strconv.Quote(r.settings.MarkdownCodeBlockIndent), Value: r.settings.MarkdownCodeBlockIndent})
		indentSelected = len(indentValues) - 1
	}
	quiet := 0
	if r.settings.QuietStartup {
		quiet = 1
	}
	themeValues, themeSelected := r.themeSettingValues()
	colorMode := r.settings.TerminalTrueColor
	if colorMode == "" {
		colorMode = "auto"
	}
	colorValues := []terminal.SettingsValue{
		{Label: "auto", Value: "auto"},
		{Label: "true", Value: true},
		{Label: "false", Value: false},
	}
	colorSelected := 0
	switch colorMode {
	case "true":
		colorSelected = 1
	case "false":
		colorSelected = 2
	}

	return []settingsEntry{
		{
			item: terminal.SettingsItem{
				ID:          "auto-compact",
				Label:       "Auto-compact",
				Description: "Compact automatically when the context fills.",
				Values:      []terminal.SettingsValue{{Label: "off", Value: false}, {Label: "on", Value: true}},
				Selected:    autoCompact,
			},
			apply: func(r *runtime, _ *terminal.Console, value terminal.SettingsValue) error {
				enabled, ok := value.Value.(bool)
				if !ok {
					return fmt.Errorf("invalid auto-compact value")
				}
				r.settings.Compaction.Enabled = enabled
				// configure() derives this field, so keep both in step.
				r.cfg.AutoCompact = enabled
				r.footerKey = ""
				return nil
			},
		},
		{
			item: terminal.SettingsItem{
				ID:          "editor-padding",
				Label:       "Editor padding",
				Description: "Horizontal padding around the editor.",
				Values: []terminal.SettingsValue{
					{Label: "0", Value: 0},
					{Label: "1", Value: 1},
					{Label: "2", Value: 2},
					{Label: "3", Value: 3},
				},
				Selected: editorPadding,
			},
			persist: []string{"editorPaddingX"},
			apply: func(r *runtime, _ *terminal.Console, value terminal.SettingsValue) error {
				padding, ok := value.Value.(int)
				if !ok {
					return fmt.Errorf("invalid editor padding")
				}
				r.settings.EditorPaddingX = padding
				return nil
			},
		},
		{
			item: terminal.SettingsItem{
				ID:          "output-padding",
				Label:       "Output padding",
				Description: "Blank rows between transcript blocks.",
				Values: []terminal.SettingsValue{
					{Label: "0", Value: 0},
					{Label: "1", Value: 1},
				},
				Selected: outputPad,
			},
			persist: []string{"outputPad"},
			apply: func(r *runtime, _ *terminal.Console, value terminal.SettingsValue) error {
				rows, ok := value.Value.(int)
				if !ok {
					return fmt.Errorf("invalid output padding")
				}
				r.settings.OutputPad = rows
				return nil
			},
		},
		{
			item: terminal.SettingsItem{
				ID:          "autocomplete-max",
				Label:       "Autocomplete max items",
				Description: "Rows shown in the completion list.",
				Values:      autocomplete,
				Selected:    autocompleteSelected,
			},
			persist: []string{"autocompleteMaxVisible"},
			apply: func(r *runtime, _ *terminal.Console, value terminal.SettingsValue) error {
				count, ok := value.Value.(int)
				if !ok {
					return fmt.Errorf("invalid autocomplete limit")
				}
				r.settings.AutocompleteMaxVisible = count
				return nil
			},
		},
		{
			item: terminal.SettingsItem{
				ID:          "code-indent",
				Label:       "Code block indent",
				Description: "Leading spaces inside fenced code blocks.",
				Values:      indentValues,
				Selected:    indentSelected,
			},
			persist: []string{"markdown", "codeBlockIndent"},
			apply: func(r *runtime, _ *terminal.Console, value terminal.SettingsValue) error {
				indent, ok := value.Value.(string)
				if !ok {
					return fmt.Errorf("invalid code block indent")
				}
				r.settings.MarkdownCodeBlockIndent = indent
				return nil
			},
		},
		{
			item: terminal.SettingsItem{
				ID:          "quiet-startup",
				Label:       "Quiet startup",
				Description: "Hide the startup header on the next session.",
				Values:      []terminal.SettingsValue{{Label: "off", Value: false}, {Label: "on", Value: true}},
				Selected:    quiet,
			},
			persist: []string{"quietStartup"},
			apply: func(r *runtime, _ *terminal.Console, value terminal.SettingsValue) error {
				enabled, ok := value.Value.(bool)
				if !ok {
					return fmt.Errorf("invalid quiet startup value")
				}
				r.settings.QuietStartup = enabled
				return nil
			},
		},
		{
			item: terminal.SettingsItem{
				ID:          "theme",
				Label:       "Theme",
				Description: "Active palette, built-in or discovered.",
				Values:      themeValues,
				Selected:    themeSelected,
			},
			persist: []string{"theme"},
			apply: func(r *runtime, console *terminal.Console, value terminal.SettingsValue) error {
				setting, ok := value.Value.(string)
				if !ok || setting == "" {
					return fmt.Errorf("invalid theme")
				}
				return r.applyThemeSetting(console, setting)
			},
		},
		{
			item: terminal.SettingsItem{
				ID:          "color-mode",
				Label:       "Color mode",
				Description: "Terminal color depth; false means 256 colors.",
				Values:      colorValues,
				Selected:    colorSelected,
			},
			persist: []string{"terminal", "trueColor"},
			apply: func(r *runtime, console *terminal.Console, value terminal.SettingsValue) error {
				setting := "auto"
				switch v := value.Value.(type) {
				case string:
					if v != "auto" {
						return fmt.Errorf("invalid color mode")
					}
				case bool:
					setting = strconv.FormatBool(v)
				default:
					return fmt.Errorf("invalid color mode")
				}
				return r.applyColorMode(console, setting)
			},
		},
	}
}

func intSettingValues(labels []string, values []int, current int) ([]terminal.SettingsValue, int) {
	out := make([]terminal.SettingsValue, 0, len(values)+1)
	selected := 0
	found := false
	for i, value := range values {
		if value == current {
			selected, found = i, true
		}
		out = append(out, terminal.SettingsValue{Label: labels[i], Value: value})
	}
	if !found {
		out = append(out, terminal.SettingsValue{Label: strconv.Itoa(current), Value: current})
		selected = len(out) - 1
	}
	return out, selected
}

func (r *runtime) newSettingsPicker() *terminal.SettingsPicker {
	entries := r.settingsEntries()
	items := make([]terminal.SettingsItem, len(entries))
	for i, entry := range entries {
		items[i] = entry.item
	}
	return &terminal.SettingsPicker{Items: items}
}

// applySetting changes one setting in memory and returns the persistence path.
// A nil path means the value is session-only.
func (r *runtime) applySetting(id string, value terminal.SettingsValue, console *terminal.Console) ([]string, error) {
	for _, entry := range r.settingsEntries() {
		if entry.item.ID != id {
			continue
		}
		if err := entry.apply(r, console, value); err != nil {
			return nil, err
		}
		return entry.persist, nil
	}
	return nil, fmt.Errorf("unknown setting")
}

// persistSettingsValue merges one value into the agent settings file.
func (r *runtime) persistSettingsValue(path []string, value any) error {
	if path == nil {
		return nil
	}
	if r.agentDir == "" {
		return fmt.Errorf("agent directory is unavailable")
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return config.UpdateUserSettings(filepath.Join(r.agentDir, "settings.json"), []config.SettingsUpdate{
		{Path: path, Value: raw},
	})
}

func (r *runtime) handleSettingsCommand(arg string, out io.Writer) (bool, error) {
	if strings.TrimSpace(arg) != "" {
		return false, fmt.Errorf("use /settings")
	}
	for _, item := range r.newSettingsPicker().Items {
		value, _ := item.Current()
		fmt.Fprintf(out, "%s: %s\n", terminal.Sanitize(item.Label), terminal.Sanitize(value.Label))
	}
	return false, nil
}

func (r *runtime) themeSettingValues() ([]terminal.SettingsValue, int) {
	setting := r.settings.Theme
	if setting == "" {
		setting = "system"
	}
	var names []string
	if r.themeCatalog != nil {
		for _, info := range r.themeCatalog.Themes() {
			names = append(names, info.Name)
		}
	} else {
		names = []string{"system", "dark", "light"}
	}
	seen := map[string]bool{}
	var values []terminal.SettingsValue
	for _, name := range names {
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		values = append(values, terminal.SettingsValue{Label: name, Value: name})
	}
	if !seen[setting] {
		values = append(values, terminal.SettingsValue{Label: setting, Value: setting})
	}
	selected := 0
	for i, candidate := range values {
		if candidate.Value == setting {
			selected = i
		}
	}
	return values, selected
}

func (r *runtime) applyThemeSetting(console *terminal.Console, setting string) error {
	if r.themeCatalog != nil && !r.themeAvailable(setting) {
		return fmt.Errorf("theme %q is not available", setting)
	}
	if r.options.UseTheme != "" {
		// The CLI override stays on screen; only the stored default changes.
		r.settings.Theme = setting
		return nil
	}
	if r.themeCatalog == nil {
		return fmt.Errorf("theme catalog is unavailable")
	}
	if err := r.installThemeDisplay(setting); err != nil {
		return err
	}
	r.settings.Theme = setting
	r.themeSetting = setting
	if console != nil && !noColorEnvironment() {
		console.SetTerminalColorSchemeNotifications(r.themeFollowsAppearance())
	}
	return nil
}

func (r *runtime) applyColorMode(console *terminal.Console, setting string) error {
	mode := pitheme.DetectColorMode(setting, envMap(), goruntime.GOOS)
	options := themeCatalogOptions(r.settings, r.options, r.agentDir, r.store.Cwd(), r.projectTrusted, mode)
	catalog := pitheme.LoadCatalog(options)
	display := selectedThemeSetting(r.settings, r.options)
	if err := r.installThemeSnapshot(catalog, mode, display); err != nil {
		return err
	}
	r.settings.TerminalTrueColor = setting
	if console != nil && !noColorEnvironment() {
		console.SetTerminalColorSchemeNotifications(r.themeFollowsAppearance())
	}
	return nil
}

func (r *runtime) installThemeDisplay(setting string) error {
	return r.installThemeSnapshot(r.themeCatalog, r.themeMode, setting)
}

// themeAvailable accepts system, an exact catalog name, or a light/dark pair
// whose two names both exist in this run's catalog.
func (r *runtime) themeAvailable(setting string) bool {
	if setting == "" || setting == "system" {
		return true
	}
	if r.themeCatalog == nil {
		return false
	}
	first, second, pair := strings.Cut(setting, "/")
	if !pair {
		_, ok := r.themeCatalog.Get(setting)
		return ok
	}
	for _, name := range []string{strings.TrimSpace(first), strings.TrimSpace(second)} {
		if name == "" {
			return false
		}
		if name == "system" {
			continue
		}
		if _, ok := r.themeCatalog.Get(name); !ok {
			return false
		}
	}
	return true
}

// installThemeSnapshot resolves before mutating live theme state, so a failed
// lookup leaves the previous palette in place.
func (r *runtime) installThemeSnapshot(catalog *pitheme.Catalog, mode pitheme.ColorMode, setting string) error {
	if catalog == nil {
		return fmt.Errorf("theme catalog is unavailable")
	}
	appearance := pitheme.DetectTerminalAppearance(r.themeColors, r.reportedColorScheme, os.Getenv("COLORFGBG"))
	saturation := 0.0
	if r.themeColorsReceived {
		saturation = 1
	}
	active, name, fallback := resolveThemeSnapshot(catalog, setting, appearance, r.themeColors, saturation, mode)
	if active == nil {
		if fallback == "" {
			fallback = "theme resolved to no palette"
		}
		return fmt.Errorf("theme fallback: %s", fallback)
	}
	r.themeCatalog, r.themeMode = catalog, mode
	r.theme, r.themeName, r.themeAppearance, r.themeFallback = active, name, appearance, fallback
	r.themeDiagnostics = catalog.Diagnostics()
	r.footerKey = ""
	return nil
}
