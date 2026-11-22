package app

import (
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	goruntime "runtime"
	"strconv"
	"strings"

	"github.com/hypernewbie/psi/internal/config"
	"github.com/hypernewbie/psi/internal/provider"
	"github.com/hypernewbie/psi/internal/session"
	"github.com/hypernewbie/psi/internal/terminal"
	pitheme "github.com/hypernewbie/psi/internal/theme"
)

// settingsEntry is one live setting. persist is nil for session-only rows.
type settingsEntry struct {
	item    terminal.SettingsItem
	persist []string
	apply   func(*runtime, *terminal.Console, terminal.SettingsValue) error
}

func (r *runtime) settingsEntries() []settingsEntry {
	uiLanguage := effectivePromptLanguage(r.options, r.settings)
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
	hideThinkingSelected := 0
	if r.settings.HideThinkingBlock {
		hideThinkingSelected = 1
	}
	contextUsageSelected := 0
	if r.settings.ContextUsageDisplay == "tokens" {
		contextUsageSelected = 1
	}
	softWrapSelected := 0
	if r.settings.SoftWrap {
		softWrapSelected = 1
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
	languageValues := []terminal.SettingsValue{
		{Label: "English", Value: config.LanguageEnglish},
		{Label: "简体中文", Value: config.LanguageChinese},
	}
	language := r.settings.Language
	if language == "" {
		language = config.LanguageEnglish
	}
	languageSelected := 0
	for i, candidate := range languageValues {
		if candidate.Value == language {
			languageSelected = i
		}
	}
	languageDescription := settingsDescription(uiLanguage, "language")
	if r.options.Language != "" {
		languageDescription += " " + settingsText(uiLanguage, "settings.language.cli-override")
	}

	return []settingsEntry{
		{
			item: terminal.SettingsItem{
				ID:          "auto-compact",
				Label:       settingsLabel(uiLanguage, "auto-compact"),
				Description: settingsDescription(uiLanguage, "auto-compact"),
				Values:      []terminal.SettingsValue{{Label: settingsBoolText(uiLanguage, false), Value: false}, {Label: settingsBoolText(uiLanguage, true), Value: true}},
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
				// Publish so a change during a run reaches the loop's next
				// adaptive-compaction check.
				r.publishLive()
				return nil
			},
		},
		{
			item: terminal.SettingsItem{
				ID:          "editor-padding",
				Label:       settingsLabel(uiLanguage, "editor-padding"),
				Description: settingsDescription(uiLanguage, "editor-padding"),
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
				Label:       settingsLabel(uiLanguage, "output-padding"),
				Description: settingsDescription(uiLanguage, "output-padding"),
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
				ID:          "hide-thinking",
				Label:       settingsLabel(uiLanguage, "hide-thinking"),
				Description: settingsDescription(uiLanguage, "hide-thinking"),
				Values: []terminal.SettingsValue{
					{Label: settingsBoolText(uiLanguage, false), Value: false},
					{Label: settingsBoolText(uiLanguage, true), Value: true},
				},
				Selected: hideThinkingSelected,
			},
			persist: []string{"hideThinkingBlock"},
			apply: func(r *runtime, _ *terminal.Console, value terminal.SettingsValue) error {
				hidden, ok := value.Value.(bool)
				if !ok {
					return fmt.Errorf("invalid hide thinking value")
				}
				r.settings.HideThinkingBlock = hidden
				return nil
			},
		},
		{
			item: terminal.SettingsItem{
				ID:          "soft-wrap",
				Label:       settingsLabel(uiLanguage, "soft-wrap"),
				Description: settingsDescription(uiLanguage, "soft-wrap"),
				Values: []terminal.SettingsValue{
					{Label: settingsBoolText(uiLanguage, false), Value: false},
					{Label: settingsBoolText(uiLanguage, true), Value: true},
				},
				Selected: softWrapSelected,
			},
			persist: []string{"softWrap"},
			apply: func(r *runtime, _ *terminal.Console, value terminal.SettingsValue) error {
				soft, ok := value.Value.(bool)
				if !ok {
					return fmt.Errorf("invalid soft wrap value")
				}
				r.settings.SoftWrap = soft
				return nil
			},
		},
		{
			item: terminal.SettingsItem{
				ID:          "autocomplete-max",
				Label:       settingsLabel(uiLanguage, "autocomplete-max"),
				Description: settingsDescription(uiLanguage, "autocomplete-max"),
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
				Label:       settingsLabel(uiLanguage, "code-indent"),
				Description: settingsDescription(uiLanguage, "code-indent"),
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
				Label:       settingsLabel(uiLanguage, "quiet-startup"),
				Description: settingsDescription(uiLanguage, "quiet-startup"),
				Values:      []terminal.SettingsValue{{Label: settingsBoolText(uiLanguage, false), Value: false}, {Label: settingsBoolText(uiLanguage, true), Value: true}},
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
				Label:       settingsLabel(uiLanguage, "theme"),
				Description: settingsDescription(uiLanguage, "theme"),
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
				Label:       settingsLabel(uiLanguage, "color-mode"),
				Description: settingsDescription(uiLanguage, "color-mode"),
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
		{
			item: terminal.SettingsItem{
				ID:          "context-usage",
				Label:       settingsLabel(uiLanguage, "context-usage"),
				Description: settingsDescription(uiLanguage, "context-usage"),
				Values: []terminal.SettingsValue{
					{Label: settingsText(uiLanguage, "settings.value.percent"), Value: "percent"},
					{Label: settingsText(uiLanguage, "settings.value.tokens"), Value: "tokens"},
				},
				Selected: contextUsageSelected,
			},
			persist: []string{"contextUsageDisplay"},
			apply: func(r *runtime, _ *terminal.Console, value terminal.SettingsValue) error {
				display, ok := value.Value.(string)
				if !ok || (display != "percent" && display != "tokens") {
					return fmt.Errorf("invalid context usage value")
				}
				r.settings.ContextUsageDisplay = display
				return nil
			},
		},
		{
			item: terminal.SettingsItem{
				ID:          "language",
				Label:       settingsLabel(uiLanguage, "language"),
				Description: languageDescription,
				Values:      languageValues,
				Selected:    languageSelected,
			},
			persist: []string{"language"},
			apply: func(r *runtime, _ *terminal.Console, value terminal.SettingsValue) error {
				language, ok := value.Value.(string)
				if !ok || !config.ValidLanguage(language) {
					return fmt.Errorf("invalid language")
				}
				if r.options.Language != "" {
					// The CLI override stays effective; only the stored default changes.
					r.settings.Language = language
					return nil
				}
				candidate := r.settings
				candidate.Language = language
				prepared, err := preparePromptResources(r.options, r.store.Cwd(), r.agentDir, candidate, promptToolNames(candidate.DefaultTools, r.web), r.projectTrust.allowsResources())
				if err != nil {
					return err
				}
				r.settings.Language = language
				r.skills, r.prompt, r.sections = prepared.skills, prepared.prompt, prepared.sections
				r.cfg.System = prepared.prompt
				r.cfg.Sections = prepared.sections
				return nil
			},
		},
		{
			item: terminal.SettingsItem{
				ID:          "model-thinking",
				Label:       settingsLabel(uiLanguage, "model-thinking"),
				Description: settingsDescription(uiLanguage, "model-thinking"),
				Values:      []terminal.SettingsValue{{Label: modelThinkingSummary(uiLanguage, r), Value: modelThinkingSummary(uiLanguage, r)}},
			},
			apply: func(*runtime, *terminal.Console, terminal.SettingsValue) error {
				return nil
			},
		},
	}
}

func modelThinkingSummary(language string, r *runtime) string {
	count := len(r.settings.ModelThinkingLevels)
	if count == 0 {
		return settingsText(language, "settings.value.none")
	}
	return fmt.Sprintf(settingsText(language, "settings.value.configured"), count)
}

// globalThinkingDefault is the effective global default shown to the picker.
// An empty configured value behaves as off, matching resolution.
func (r *runtime) globalThinkingDefault() string {
	if r.settings.DefaultThinkingSet && r.settings.DefaultThinking != "" {
		return r.settings.DefaultThinking
	}
	return "off"
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
	uiLanguage := effectivePromptLanguage(r.options, r.settings)
	return &terminal.SettingsPicker{
		Items:     items,
		Hint:      settingsText(uiLanguage, "settings.picker.hint"),
		ShortHint: settingsText(uiLanguage, "settings.picker.short-hint"),
	}
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
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return r.writeUserSettings([]config.SettingsUpdate{{Path: path, Value: raw}})
}

// writeUserSettings applies settings updates to the agent settings file.
func (r *runtime) writeUserSettings(updates []config.SettingsUpdate) error {
	if r.agentDir == "" {
		return fmt.Errorf("agent directory is unavailable")
	}
	return config.UpdateUserSettings(filepath.Join(r.agentDir, "settings.json"), updates)
}

const modelThinkingClearChoice = "(clear override)"

// modelThinkingLevelItems builds the level choices for one target model and
// appends a clear row when that model already has an override.
func modelThinkingLevelItems(r *runtime, providerID, modelID string, meta config.ModelMeta) []session.SessionInfo {
	pm := providerModel(providerID, meta)
	levels := provider.SupportedThinking(pm)
	items := make([]session.SessionInfo, 0, len(levels)+1)
	for _, level := range levels {
		items = append(items, session.SessionInfo{ID: level, FirstMessage: provider.ThinkingDescription(pm, level)})
	}
	if _, exists := r.settings.ModelThinkingLevels[providerID+"/"+modelID]; exists {
		items = append(items, session.SessionInfo{ID: modelThinkingClearChoice, FirstMessage: "Revert to global default (" + r.globalThinkingDefault() + ")"})
	}
	return items
}

// applyModelThinkingLevel writes or clears one per-model default. A change for
// the live model also applies to the current session; another model waits for
// its next switch.
func (r *runtime) applyModelThinkingLevel(target, level string, out io.Writer) error {
	providerID, modelID, found := strings.Cut(target, "/")
	providerID, modelID = strings.TrimSpace(providerID), strings.TrimSpace(modelID)
	if !found || providerID == "" || modelID == "" {
		return fmt.Errorf("invalid model %q", target)
	}
	meta, err := r.models.SelectModel(providerID, modelID)
	if err != nil {
		return err
	}
	clear := level == modelThinkingClearChoice
	update := config.SettingsUpdate{Path: []string{"modelThinkingLevels", target}}
	if clear {
		update.Remove = true
	} else {
		pm := providerModel(providerID, meta)
		supported := false
		for _, candidate := range provider.SupportedThinking(pm) {
			if candidate == level {
				supported = true
				break
			}
		}
		if !supported {
			return fmt.Errorf("unsupported thinking level %q for %s/%s", level, providerID, modelID)
		}
		raw, err := json.Marshal(level)
		if err != nil {
			return err
		}
		update.Value = raw
	}
	current := providerID == r.ov.Provider && meta.ID == r.ov.Model
	if current {
		sessionLevel := level
		if clear {
			sessionLevel = r.globalThinkingDefault()
		}
		sessionLevel = provider.ClampThinkingLevel(r.cfg.Model, sessionLevel)
		if _, err := r.command(&terminal.Command{Name: "thinking", Arg: sessionLevel}, out); err != nil {
			return err
		}
	}
	if err := r.writeUserSettings([]config.SettingsUpdate{update}); err != nil {
		if current {
			return fmt.Errorf("Saved for this session only: %w", err)
		}
		return fmt.Errorf("Failed to save model thinking default: %w", err)
	}
	levels := maps.Clone(r.settings.ModelThinkingLevels)
	if clear {
		delete(levels, target)
	} else {
		if levels == nil {
			levels = map[string]string{}
		}
		levels[target] = level
	}
	r.settings.ModelThinkingLevels = levels
	return nil
}

// saveThinkingDefault applies a level to the live session and persists it as
// the global default. A failed write keeps the session change but saves none.
func (r *runtime) saveThinkingDefault(level string, out io.Writer) error {
	if _, err := r.command(&terminal.Command{Name: "thinking", Arg: level}, out); err != nil {
		return err
	}
	raw, err := json.Marshal(level)
	if err != nil {
		return err
	}
	if err := r.writeUserSettings([]config.SettingsUpdate{{Path: []string{"defaultThinkingLevel"}, Value: raw}}); err != nil {
		return fmt.Errorf("Saved for this session only: %w", err)
	}
	r.settings.DefaultThinking = level
	r.settings.DefaultThinkingSet = true
	return nil
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
	options := themeCatalogOptions(r.settings, r.options, r.agentDir, r.store.Cwd(), r.projectTrust.allowsResources(), mode)
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
