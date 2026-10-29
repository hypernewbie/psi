package theme

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeCatalogTheme(t *testing.T, path, name, accent string) {
	t.Helper()
	colors := completeColors()
	colors["accent"] = accent
	data := themeFixture(t, colors, nil)
	// Give the theme its requested identity without weakening strict schema parsing.
	data = []byte(strings.Replace(string(data), `"name":"fixture"`, `"name":"`+name+`"`, 1))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestCatalogUsesFirstWinnerPrecedenceAndReportsCollisions(t *testing.T) {
	root := t.TempDir()
	projectConfigured := filepath.Join(root, "project-configured.json")
	projectAuto := filepath.Join(root, "workspace", ".pi", "themes", "shared.json")
	userConfigured := filepath.Join(root, "user-configured.json")
	userAuto := filepath.Join(root, "agent", "themes", "shared.json")
	writeCatalogTheme(t, projectConfigured, "shared", "#123456")
	writeCatalogTheme(t, projectAuto, "project-auto", "#234567")
	writeCatalogTheme(t, userConfigured, "shared", "#345678")
	writeCatalogTheme(t, userAuto, "shared", "#456789")
	catalog := LoadCatalog(CatalogOptions{
		AgentDir: root + "/agent", ProjectDir: root + "/workspace", ProjectTrusted: true,
		ProjectPaths: []string{projectConfigured}, UserPaths: []string{userConfigured},
	})
	selected, ok := catalog.Get("shared")
	if !ok || selected.SourcePath != projectConfigured {
		t.Fatalf("project settings did not win: %+v", selected)
	}
	if selected.Appearance == "" {
		t.Fatal("compiled selected theme has no detected appearance")
	}
	collisions := 0
	for _, diagnostic := range catalog.Diagnostics() {
		if diagnostic.Name == "shared" && diagnostic.WinnerPath == projectConfigured {
			collisions++
		}
	}
	if collisions != 2 {
		t.Fatalf("collision diagnostics=%d, all=%+v", collisions, catalog.Diagnostics())
	}
	infos := catalog.Themes()
	if len(infos) == 0 || infos[0].Name != "system" {
		t.Fatalf("theme order=%+v", infos)
	}
}

func TestCatalogTrustDenialDoesNotLoadProjectThemePaths(t *testing.T) {
	root := t.TempDir()
	projectTheme := filepath.Join(root, "workspace", ".pi", "themes", "private.json")
	writeCatalogTheme(t, projectTheme, "private", "#123456")
	catalog := LoadCatalog(CatalogOptions{ProjectDir: filepath.Join(root, "workspace"), ProjectTrusted: false, ProjectPaths: []string{projectTheme}})
	if _, ok := catalog.Get("private"); ok {
		t.Fatal("untrusted project theme was loaded")
	}
	for _, diagnostic := range catalog.Diagnostics() {
		if strings.Contains(diagnostic.Path, filepath.Join("workspace", ".pi")) {
			t.Fatalf("read protected path before trust: %+v", diagnostic)
		}
	}
}

func TestCatalogAppliesPiLocalPathExpressionsAndNoThemes(t *testing.T) {
	root := t.TempDir()
	one := filepath.Join(root, "themes", "one.json")
	two := filepath.Join(root, "themes", "two.json")
	writeCatalogTheme(t, one, "one", "#123456")
	writeCatalogTheme(t, two, "two", "#234567")
	catalog := LoadCatalog(CatalogOptions{UserPaths: []string{filepath.Join(root, "themes", "*.json"), "!" + two, "+" + two}})
	if _, ok := catalog.Get("one"); !ok {
		t.Fatal("plain glob theme missing")
	}
	if _, ok := catalog.Get("two"); !ok {
		t.Fatal("force-included theme missing")
	}
	withoutResources := LoadCatalog(CatalogOptions{NoThemes: true, UserPaths: []string{one}})
	if _, ok := withoutResources.Get("one"); ok {
		t.Fatal("--no-themes loaded configured custom path")
	}
	// A direct explicit CLI path remains available with --no-themes.
	withCLI := LoadCatalog(CatalogOptions{NoThemes: true, CLIPaths: []string{one}})
	if _, ok := withCLI.Get("one"); !ok {
		t.Fatal("--no-themes suppressed explicit CLI theme")
	}
}

func TestCatalogReadsInstalledPackageThemeDataWithoutExecutingIt(t *testing.T) {
	root := t.TempDir()
	packageRoot := filepath.Join(root, "packages", "offline")
	themePath := filepath.Join(packageRoot, "theme-data", "offline.json")
	writeCatalogTheme(t, themePath, "offline", "#123456")
	manifest := `{"name":"offline","scripts":{"postinstall":"touch SHOULD_NOT_RUN"},"pi":{"themes":["theme-data/*.json"]}}`
	if err := os.WriteFile(filepath.Join(packageRoot, "package.json"), []byte(manifest), 0o600); err != nil {
		t.Fatal(err)
	}
	catalog := LoadCatalog(CatalogOptions{PackageDirs: []string{filepath.Join(root, "packages")}})
	selected, ok := catalog.Get("offline")
	if !ok || selected.SourcePath != themePath {
		t.Fatalf("package theme not discovered: %+v", selected)
	}
	if _, err := os.Stat(filepath.Join(root, "SHOULD_NOT_RUN")); !os.IsNotExist(err) {
		t.Fatal("package script was executed")
	}
}

func TestResolveThemeSettingPairsAndFallback(t *testing.T) {
	if got := ResolveThemeSetting("paper/ink", "light"); got != "paper" {
		t.Fatalf("light pair=%q", got)
	}
	if got := ResolveThemeSetting("paper/ink", "dark"); got != "ink" {
		t.Fatalf("dark pair=%q", got)
	}
	for _, bad := range []string{"", "paper/", "/ink", "a/b/c"} {
		if bad != "" && ResolveThemeSetting(bad, "dark") != "system" {
			t.Errorf("bad setting %q did not use system", bad)
		}
	}
	catalog := LoadCatalog(CatalogOptions{})
	selected, name, err := catalog.Resolve("missing", "dark", SystemThemeInput{}, ColorMode256)
	if err == nil || name != "system" || selected == nil {
		t.Fatalf("missing selection fallback=%p %q %v", selected, name, err)
	}
}
