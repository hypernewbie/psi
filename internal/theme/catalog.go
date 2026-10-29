package theme

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Diagnostic describes a local theme load, fallback, or name collision. It
// contains no theme document contents.
type Diagnostic struct {
	Level      string
	Message    string
	Name       string
	Path       string
	WinnerPath string
	LoserPath  string
}

type ThemeInfo struct {
	Name string
	Path string
}

// CatalogOptions names only local resource roots. Package resources are read
// as JSON data; this loader never executes package code or installs resources.
type CatalogOptions struct {
	AgentDir       string
	ProjectDir     string
	ProjectTrusted bool
	NoThemes       bool
	Mode           ColorMode
	UserPaths      []string
	ProjectPaths   []string
	CLIPaths       []string
	PackageDirs    []string
}

type catalogEntry struct {
	theme *Theme
	path  string
}

// Catalog is an immutable, first-winner registry of validated Pi themes.
type Catalog struct {
	themes      map[string]catalogEntry
	ordered     []ThemeInfo
	diagnostics []Diagnostic
}

func LoadCatalog(options CatalogOptions) *Catalog {
	if options.Mode == "" {
		options.Mode = ColorMode256
	}
	catalog := &Catalog{themes: make(map[string]catalogEntry), ordered: []ThemeInfo{{Name: "system"}}}
	for _, name := range []string{"dark", "light"} {
		doc, err := BuiltinDocument(name)
		if err != nil {
			catalog.diagnostics = append(catalog.diagnostics, Diagnostic{Level: "error", Message: err.Error(), Name: name})
			continue
		}
		compiled, err := doc.Compile(options.Mode)
		if err != nil {
			catalog.diagnostics = append(catalog.diagnostics, Diagnostic{Level: "error", Message: err.Error(), Name: name})
			continue
		}
		catalog.themes[name] = catalogEntry{theme: compiled}
		catalog.ordered = append(catalog.ordered, ThemeInfo{Name: name})
	}

	// CLI additions are supplied before settings resources by Pi's resolver.
	catalog.addSpecs(options.CLIPaths, "cli", options.Mode)
	if options.ProjectTrusted && !options.NoThemes {
		catalog.addSpecs(options.ProjectPaths, "project-settings", options.Mode)
		if options.ProjectDir != "" {
			catalog.addDirectory(filepath.Join(options.ProjectDir, ".pi", "themes"), "project-auto", options.Mode, false)
		}
	}
	if !options.NoThemes {
		catalog.addSpecs(options.UserPaths, "user-settings", options.Mode)
	}
	if options.AgentDir != "" && !options.NoThemes {
		catalog.addDirectory(filepath.Join(options.AgentDir, "themes"), "user-auto", options.Mode, false)
	}
	if !options.NoThemes {
		packageDirs := append([]string(nil), options.PackageDirs...)
		if options.AgentDir != "" {
			packageDirs = append(packageDirs, filepath.Join(options.AgentDir, "packages"))
		}
		if options.ProjectTrusted && options.ProjectDir != "" {
			packageDirs = append(packageDirs, filepath.Join(options.ProjectDir, ".pi", "packages"))
		}
		catalog.addPackages(packageDirs, options.Mode)
	}
	return catalog
}

func (c *Catalog) addDirectory(dir, source string, mode ColorMode, missingWarning bool) {
	if dir == "" {
		return
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if !os.IsNotExist(err) || missingWarning {
			c.diagnostics = append(c.diagnostics, Diagnostic{Level: "warning", Message: "could not read theme directory", Path: dir})
		}
		return
	}
	for _, entry := range entries {
		if entry.Name() == "node_modules" || strings.HasPrefix(entry.Name(), ".") || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		info, err := os.Stat(path) // follows symlinks, as Pi's theme loader does.
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		c.addFile(path, source, mode)
	}
}

func (c *Catalog) addSpecs(specs []string, source string, mode ColorMode) {
	var candidates []string
	var patterns []string
	for _, spec := range specs {
		prefix := ""
		path := spec
		if len(path) > 0 && strings.ContainsRune("!+-", rune(path[0])) {
			prefix, path = path[:1], path[1:]
		}
		if prefix != "" {
			patterns = append(patterns, spec)
			continue
		}
		candidates = append(candidates, c.expandPath(path, source)...)
	}
	if len(patterns) > 0 {
		candidates = applyThemePathPatterns(candidates, patterns)
	}
	seen := map[string]bool{}
	for _, path := range candidates {
		canonical, err := filepath.Abs(path)
		if err != nil {
			canonical = filepath.Clean(path)
		}
		if seen[canonical] {
			continue
		}
		seen[canonical] = true
		c.addFile(path, source, mode)
	}
}

func (c *Catalog) expandPath(path, source string) []string {
	matches := []string{path}
	if strings.ContainsAny(path, "*?") {
		var err error
		matches, err = filepath.Glob(path)
		if err != nil {
			c.diagnostics = append(c.diagnostics, Diagnostic{Level: "warning", Message: "invalid theme path pattern", Path: path})
			return nil
		}
		if len(matches) == 0 {
			c.diagnostics = append(c.diagnostics, Diagnostic{Level: "warning", Message: "theme path does not exist", Path: path})
		}
	}
	var files []string
	for _, match := range matches {
		info, err := os.Stat(match)
		if err != nil {
			if !os.IsNotExist(err) {
				c.diagnostics = append(c.diagnostics, Diagnostic{Level: "warning", Message: err.Error(), Path: match})
			} else if !strings.ContainsAny(path, "*?") {
				c.diagnostics = append(c.diagnostics, Diagnostic{Level: "warning", Message: "theme path does not exist", Path: match})
			}
			continue
		}
		if info.IsDir() {
			entries, err := os.ReadDir(match)
			if err != nil {
				c.diagnostics = append(c.diagnostics, Diagnostic{Level: "warning", Message: "could not read theme directory", Path: match})
				continue
			}
			for _, entry := range entries {
				if strings.HasPrefix(entry.Name(), ".") || !strings.HasSuffix(entry.Name(), ".json") {
					continue
				}
				candidate := filepath.Join(match, entry.Name())
				if file, err := os.Stat(candidate); err == nil && file.Mode().IsRegular() {
					files = append(files, candidate)
				}
			}
		} else if info.Mode().IsRegular() && strings.HasSuffix(match, ".json") {
			files = append(files, match)
		} else {
			c.diagnostics = append(c.diagnostics, Diagnostic{Level: "warning", Message: "theme path is not a json file or directory", Path: match})
		}
	}
	sort.Strings(files)
	return files
}

func applyThemePathPatterns(all []string, patterns []string) []string {
	var excludes, forceIncludes, forceExcludes []string
	for _, pattern := range patterns {
		if strings.HasPrefix(pattern, "+") {
			forceIncludes = append(forceIncludes, filepath.Clean(pattern[1:]))
		} else if strings.HasPrefix(pattern, "-") {
			forceExcludes = append(forceExcludes, filepath.Clean(pattern[1:]))
		} else {
			excludes = append(excludes, filepath.Clean(pattern[1:]))
		}
	}
	matches := func(path string, patterns []string, exact bool) bool {
		for _, pattern := range patterns {
			if exact && filepath.Clean(path) == pattern {
				return true
			}
			if !exact {
				if matched, _ := filepath.Match(pattern, filepath.Clean(path)); matched || filepath.Clean(path) == pattern {
					return true
				}
				if matched, _ := filepath.Match(pattern, filepath.Base(path)); matched {
					return true
				}
			}
		}
		return false
	}
	var out []string
	for _, path := range all {
		if matches(path, excludes, false) && !matches(path, forceIncludes, true) {
			continue
		}
		if matches(path, forceExcludes, true) {
			continue
		}
		out = append(out, path)
	}
	for _, path := range all {
		if matches(path, forceIncludes, true) && !matches(path, forceExcludes, true) {
			found := false
			for _, existing := range out {
				if filepath.Clean(existing) == filepath.Clean(path) {
					found = true
					break
				}
			}
			if !found {
				out = append(out, path)
			}
		}
	}
	return out
}

func (c *Catalog) addFile(path, source string, mode ColorMode) {
	data, err := os.ReadFile(path)
	if err != nil {
		c.diagnostics = append(c.diagnostics, Diagnostic{Level: "warning", Message: "failed to read theme file", Path: path})
		return
	}
	doc, err := Parse(data)
	if err != nil {
		c.diagnostics = append(c.diagnostics, Diagnostic{Level: "warning", Message: fmt.Sprintf("invalid theme: %v", err), Path: path})
		return
	}
	compiled, err := doc.Compile(mode)
	if err != nil {
		c.diagnostics = append(c.diagnostics, Diagnostic{Level: "warning", Message: fmt.Sprintf("invalid theme: %v", err), Name: doc.Name, Path: path})
		return
	}
	compiled.SourcePath = path
	if doc.Name == "system" {
		c.diagnostics = append(c.diagnostics, Diagnostic{Level: "warning", Message: "theme name is reserved", Name: doc.Name, Path: path})
		return
	}
	if existing, ok := c.themes[doc.Name]; ok {
		winner := existing.path
		if winner == "" {
			winner = "built-in " + doc.Name
		}
		c.diagnostics = append(c.diagnostics, Diagnostic{
			Level: "warning", Message: fmt.Sprintf("theme name %q collision (keeping %s)", doc.Name, winner),
			Name: doc.Name, Path: path, WinnerPath: existing.path, LoserPath: path,
		})
		return
	}
	c.themes[doc.Name] = catalogEntry{theme: compiled, path: path}
	c.ordered = append(c.ordered, ThemeInfo{Name: doc.Name, Path: path})
}

type packageThemeManifest struct {
	Pi struct {
		Themes []string `json:"themes"`
	} `json:"pi"`
}

func (c *Catalog) addPackages(dirs []string, mode ColorMode) {
	var roots []string
	seen := map[string]bool{}
	for _, dir := range dirs {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			root := filepath.Join(dir, entry.Name())
			if info, err := os.Stat(root); err == nil && info.IsDir() {
				canonical, _ := filepath.Abs(root)
				if !seen[canonical] {
					seen[canonical] = true
					roots = append(roots, root)
				}
			}
		}
	}
	sort.Strings(roots)
	for _, root := range roots {
		manifestPath := filepath.Join(root, "package.json")
		data, err := os.ReadFile(manifestPath)
		if err != nil {
			continue
		}
		var manifest packageThemeManifest
		if err := json.Unmarshal(data, &manifest); err != nil {
			c.diagnostics = append(c.diagnostics, Diagnostic{Level: "warning", Message: "invalid local package manifest", Path: manifestPath})
			continue
		}
		if len(manifest.Pi.Themes) > 0 {
			var paths []string
			for _, entry := range manifest.Pi.Themes {
				clean := filepath.Clean(filepath.Join(root, entry))
				rel, err := filepath.Rel(root, clean)
				if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
					c.diagnostics = append(c.diagnostics, Diagnostic{Level: "warning", Message: "package theme path escapes its package", Path: clean})
					continue
				}
				paths = append(paths, clean)
			}
			c.addSpecs(paths, "package", mode)
		} else {
			c.addDirectory(filepath.Join(root, "themes"), "package", mode, false)
		}
	}
}

// Themes returns deterministic discovery order. Built-in names are present
// after the generated system role, followed by first-winning local resources.
func (c *Catalog) Themes() []ThemeInfo { return append([]ThemeInfo(nil), c.ordered...) }

func (c *Catalog) Diagnostics() []Diagnostic { return append([]Diagnostic(nil), c.diagnostics...) }

func (c *Catalog) Get(name string) (*Theme, bool) {
	entry, ok := c.themes[name]
	return entry.theme, ok
}

func (c *Catalog) Resolve(setting, terminalTheme string, input SystemThemeInput, mode ColorMode) (*Theme, string, error) {
	if first, second, pair := strings.Cut(setting, "/"); pair && (strings.Contains(second, "/") || strings.TrimSpace(first) == "" || strings.TrimSpace(second) == "") {
		fallback, err := NewSystemTheme(input, mode)
		if err == nil {
			err = fmt.Errorf("invalid theme selection %q; use a name or light/dark pair", setting)
		}
		return fallback, "system", err
	}
	name := ResolveThemeSetting(setting, terminalTheme)
	if name == "" || name == "system" {
		resolved, err := NewSystemTheme(input, mode)
		return resolved, "system", err
	}
	if selected, ok := c.Get(name); ok {
		return selected, name, nil
	}
	system, err := NewSystemTheme(input, mode)
	if err != nil {
		return nil, "system", err
	}
	return system, "system", fmt.Errorf("theme %q not found; using system theme", name)
}

func ResolveThemeSetting(setting, terminalTheme string) string {
	if terminalTheme != "light" {
		terminalTheme = "dark"
	}
	if setting == "" {
		return "system"
	}
	first, second, hasPair := strings.Cut(setting, "/")
	if hasPair {
		if strings.Contains(second, "/") || strings.TrimSpace(first) == "" || strings.TrimSpace(second) == "" {
			return "system"
		}
		if terminalTheme == "light" {
			return strings.TrimSpace(first)
		}
		return strings.TrimSpace(second)
	}
	return setting
}
