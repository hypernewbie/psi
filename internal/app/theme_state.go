package app

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"time"

	"psi/internal/config"
	"psi/internal/terminal"
	pitheme "psi/internal/theme"
)

func resolveCLIThemePaths(cwd string, paths []string) []string {
	out := make([]string, 0, len(paths))
	for _, path := range paths {
		path = strings.TrimSpace(path)
		if path == "~" || strings.HasPrefix(path, "~/") {
			if home, err := os.UserHomeDir(); err == nil {
				path = filepath.Join(home, strings.TrimPrefix(path, "~/"))
			}
		}
		if !filepath.IsAbs(path) {
			path = filepath.Join(cwd, path)
		}
		out = append(out, filepath.Clean(path))
	}
	return out
}

func presentationDisplay(settings config.Settings) terminal.DisplaySettings {
	return terminal.DisplaySettings{
		OutputPad: settings.OutputPad, EditorPaddingX: settings.EditorPaddingX,
		AutocompleteMaxVisible: settings.AutocompleteMaxVisible,
		CodeBlockIndent:        settings.MarkdownCodeBlockIndent,
	}
}

func themeCatalogOptions(settings config.Settings, options Options, agentDir, cwd string, trusted bool, mode pitheme.ColorMode) pitheme.CatalogOptions {
	return pitheme.CatalogOptions{
		AgentDir: agentDir, ProjectDir: cwd, ProjectTrusted: trusted, NoThemes: options.NoThemes, Mode: mode,
		UserPaths: settings.UserThemePaths, ProjectPaths: settings.ProjectThemePaths,
		CLIPaths: resolveCLIThemePaths(cwd, options.ThemePaths),
	}
}

func selectedThemeSetting(settings config.Settings, options Options) string {
	if options.UseTheme != "" {
		return options.UseTheme
	}
	return settings.Theme
}

func resolveThemeSnapshot(catalog *pitheme.Catalog, setting, appearance string, colors pitheme.TerminalColors, saturation float64, mode pitheme.ColorMode) (*pitheme.Theme, string, string) {
	active, name, err := catalog.Resolve(setting, appearance, colors.SystemInput(appearance, saturation), mode)
	if err != nil {
		return active, name, err.Error()
	}
	return active, name, ""
}

type terminalColorCollector struct {
	colors   pitheme.TerminalColors
	seen     map[string]bool
	palette  [16]*pitheme.RGB
	finished bool
}

func newTerminalColorCollector(previous pitheme.TerminalColors) *terminalColorCollector {
	return &terminalColorCollector{colors: previous, seen: make(map[string]bool)}
}

func sameThemeRGB(a, b *pitheme.RGB) bool {
	return a == b || a != nil && b != nil && *a == *b
}

func (c *terminalColorCollector) Accept(data []byte) (scheme string, changed, complete bool) {
	value := string(data)
	if detected, ok := pitheme.ParseTerminalColorSchemeReport(value); ok {
		return detected, false, false
	}
	if pitheme.IsDeviceAttributesReply(value) {
		if !c.finished {
			c.finished = true
			return "", false, true
		}
		return "", false, false
	}
	reply, ok := pitheme.ParseOSCColorResponse(value)
	if !ok || c.finished {
		return "", false, false
	}
	key := reply.Target
	if reply.Target == "palette" {
		key = fmt.Sprintf("palette:%d", reply.Index)
	}
	if c.seen[key] {
		return "", false, false
	}
	c.seen[key] = true
	if reply.Color != nil {
		color := *reply.Color
		switch reply.Target {
		case "foreground":
			if !sameThemeRGB(c.colors.Foreground, &color) {
				c.colors.Foreground = &color
				changed = true
			}
		case "background":
			if !sameThemeRGB(c.colors.Background, &color) {
				c.colors.Background = &color
				changed = true
			}
		case "palette":
			c.palette[reply.Index] = &color
		}
	}
	paletteCount, allPalette := 0, true
	for _, color := range c.palette {
		if color == nil {
			allPalette = false
		} else {
			paletteCount++
		}
	}
	if paletteCount == len(c.palette) && allPalette {
		palette := make([]pitheme.RGB, 16)
		for i, color := range c.palette {
			palette[i] = *color
		}
		if !sameThemePalette(c.colors.Palette, palette) {
			c.colors.Palette = palette
			changed = true
		}
	}
	if len(c.seen) == 18 {
		c.finished, complete = true, true
	}
	return "", changed, complete
}

func (c *terminalColorCollector) hasReportedColors() bool {
	return c.colors.Foreground != nil || c.colors.Background != nil || len(c.colors.Palette) == 16
}

func sameThemePalette(a, b []pitheme.RGB) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func (r *runtime) selectedUserThemePath() string {
	if r.theme == nil || r.theme.SourcePath == "" || r.agentDir == "" {
		return ""
	}
	path := filepath.Clean(r.theme.SourcePath)
	base := filepath.Clean(filepath.Join(r.agentDir, "themes"))
	pathDir, pathErr := filepath.Abs(filepath.Dir(path))
	baseDir, baseErr := filepath.Abs(base)
	if pathErr != nil || baseErr != nil || pathDir != baseDir {
		return ""
	}
	return path
}

func (r *runtime) themeFollowsAppearance() bool {
	if r.themeSetting == "" || r.themeSetting == "system" {
		return true
	}
	first, second, pair := strings.Cut(r.themeSetting, "/")
	return pair && !strings.Contains(second, "/") && strings.TrimSpace(first) != "" && strings.TrimSpace(second) != ""
}

func (r *runtime) applyResolvedTheme(console *terminal.Console) {
	appearance := pitheme.DetectTerminalAppearance(r.themeColors, r.reportedColorScheme, os.Getenv("COLORFGBG"))
	saturation := 0.0
	if r.themeColorsReceived {
		saturation = 1
	}
	active, name, fallback := resolveThemeSnapshot(r.themeCatalog, r.themeSetting, appearance, r.themeColors, saturation, r.themeMode)
	if active == nil {
		return
	}
	r.theme, r.themeName, r.themeAppearance, r.themeFallback = active, name, appearance, fallback
	r.footerKey = ""
	if console != nil {
		r.refreshPresentation(console)
	}
}

func noColorEnvironment() bool {
	_, disabled := os.LookupEnv("NO_COLOR")
	return disabled
}

func (r *runtime) beginTerminalColorQuery(console *terminal.Console) {
	if console == nil || noColorEnvironment() {
		return
	}
	r.themeColorCollector = newTerminalColorCollector(r.themeColors)
	if err := console.QueryTerminalColors(); err != nil {
		return
	}
}

func (r *runtime) queryTerminalColors(console *terminal.Console, timeout time.Duration) {
	if console == nil || noColorEnvironment() {
		return
	}
	if r.themeFollowsAppearance() {
		console.SetTerminalColorSchemeNotifications(true)
	}
	r.beginTerminalColorQuery(console)
	collector := r.themeColorCollector
	if collector == nil {
		return
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	for {
		select {
		case input, ok := <-console.Inputs:
			if !ok {
				goto settled
			}
			if len(input.Reply) > 0 {
				scheme, _, complete := collector.Accept(input.Reply)
				if scheme != "" {
					r.reportedColorScheme = scheme
				}
				if reply, valid := pitheme.ParseOSCColorResponse(string(input.Reply)); valid && reply.Color != nil {
					r.themeColorsReceived = true
				}
				r.themeColors = collector.colors
				if complete {
					goto settled
				}
				continue
			}
			console.HoldInput(input)
		case <-timer.C:
			goto settled
		}
	}
settled:
	r.themeColors = collector.colors
	if collector.hasReportedColors() {
		r.themeColorsReceived = true
	}
	r.applyResolvedTheme(console)
}

func (r *runtime) handleTerminalReply(console *terminal.Console, data []byte) bool {
	if r.themeColorCollector == nil {
		return false
	}
	collector := r.themeColorCollector
	scheme, changed, _ := collector.Accept(data)
	if scheme != "" && scheme != r.reportedColorScheme {
		r.reportedColorScheme = scheme
		r.applyResolvedTheme(console)
		if r.themeFollowsAppearance() {
			r.beginTerminalColorQuery(console)
		}
		return true
	}
	if reply, valid := pitheme.ParseOSCColorResponse(string(data)); valid && reply.Color != nil {
		r.themeColorsReceived = true
	}
	if changed {
		r.themeColors = collector.colors
		r.applyResolvedTheme(console)
		return true
	}
	return false
}

func writeThemeDiagnostics(out io.Writer, diagnostics []pitheme.Diagnostic, fallback string) {
	for _, diagnostic := range diagnostics {
		if diagnostic.Level != "warning" {
			continue
		}
		message := "theme warning: " + diagnostic.Message
		if diagnostic.Name != "" {
			message += " (" + diagnostic.Name + ")"
		}
		if diagnostic.Path != "" {
			message += " — " + diagnostic.Path
		}
		fmt.Fprintln(out, terminal.Sanitize(message))
	}
	if fallback != "" {
		fmt.Fprintln(out, terminal.Sanitize("theme fallback: "+fallback))
	}
}

func (r *runtime) reloadTheme() error {
	userPath := filepath.Join(r.agentDir, "settings.json")
	projectPath := filepath.Join(r.store.Cwd(), ".pi", "settings.json")
	loaded, err := config.LoadSettings(userPath, projectPath, r.projectTrusted)
	if err != nil {
		return fmt.Errorf("reload theme settings: %w", err)
	}
	mode := pitheme.DetectColorMode(loaded.Settings.TerminalTrueColor, envMap(), goruntime.GOOS)
	setting := selectedThemeSetting(loaded.Settings, r.options)
	appearance := pitheme.DetectTerminalAppearance(r.themeColors, r.reportedColorScheme, os.Getenv("COLORFGBG"))
	catalog := pitheme.LoadCatalog(themeCatalogOptions(loaded.Settings, r.options, r.agentDir, r.store.Cwd(), r.projectTrusted, mode))
	saturation := 0.0
	if r.themeColorsReceived {
		saturation = 1
	}
	active, name, fallback := resolveThemeSnapshot(catalog, setting, appearance, r.themeColors, saturation, mode)
	if fallback != "" || active == nil {
		if fallback == "" {
			fallback = "theme resolved to no palette"
		}
		return fmt.Errorf("reload theme settings: %s", fallback)
	}
	// Install the complete snapshot only after settings, resources, and theme
	// resolution all succeed. A failed reload leaves the last good palette live.
	r.settings.Theme = loaded.Settings.Theme
	r.settings.UserThemePaths = append([]string(nil), loaded.Settings.UserThemePaths...)
	r.settings.ProjectThemePaths = append([]string(nil), loaded.Settings.ProjectThemePaths...)
	r.settings.TerminalTrueColor = loaded.Settings.TerminalTrueColor
	r.settings.OutputPad = loaded.Settings.OutputPad
	r.settings.EditorPaddingX = loaded.Settings.EditorPaddingX
	r.settings.AutocompleteMaxVisible = loaded.Settings.AutocompleteMaxVisible
	r.settings.MarkdownCodeBlockIndent = loaded.Settings.MarkdownCodeBlockIndent
	r.themeCatalog, r.theme, r.themeSetting, r.themeName = catalog, active, setting, name
	r.themeMode, r.themeAppearance = mode, appearance
	r.themeFallback, r.themeDiagnostics = "", catalog.Diagnostics()
	r.footerKey = ""
	return nil
}
