package theme

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestReviewNoThemesSkipsAutomaticUserResources(t *testing.T) {
	agent := t.TempDir()
	path := filepath.Join(agent, "themes", "local.json")
	writeCatalogTheme(t, path, "local", "#123456")
	catalog := LoadCatalog(CatalogOptions{AgentDir: agent, NoThemes: true})
	if _, ok := catalog.Get("local"); ok {
		t.Fatal("--no-themes still discovered a user theme")
	}
	if len(catalog.Diagnostics()) != 0 {
		t.Fatalf("disabled resources produced diagnostics: %+v", catalog.Diagnostics())
	}
	explicit := LoadCatalog(CatalogOptions{AgentDir: agent, NoThemes: true, CLIPaths: []string{path}})
	if _, ok := explicit.Get("local"); !ok {
		t.Fatal("explicit CLI theme was disabled")
	}
}

func TestReviewInvalidThemeSelectionReportsFallback(t *testing.T) {
	catalog := LoadCatalog(CatalogOptions{})
	for _, selection := range []string{"light/", "/dark", "a/b/c"} {
		selected, name, err := catalog.Resolve(selection, "dark", SystemThemeInput{}, ColorMode256)
		if selected == nil || name != "system" || err == nil {
			t.Errorf("invalid selection %q silently accepted: theme=%v name=%q err=%v", selection, selected, name, err)
		}
	}
}

func TestReviewThemeCollisionNamesActualWinner(t *testing.T) {
	root := t.TempDir()
	first, last := filepath.Join(root, "first.json"), filepath.Join(root, "last.json")
	writeCatalogTheme(t, first, "same", "#123456")
	writeCatalogTheme(t, last, "same", "#654321")
	catalog := LoadCatalog(CatalogOptions{CLIPaths: []string{first}, UserPaths: []string{last}})
	for _, d := range catalog.Diagnostics() {
		if d.Name == "same" {
			if !strings.Contains(d.Message, first) {
				t.Fatalf("collision message does not name actual winner: %+v", d)
			}
			return
		}
	}
	t.Fatal("missing collision diagnostic")
}
