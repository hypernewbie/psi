//go:build unix

package app

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"psi/internal/session"
	pitheme "psi/internal/theme"
)

func TestActualStartupUsesUnmodifiedPiDarkAndLightThemes(t *testing.T) {
	home, work := t.TempDir(), t.TempDir()
	agentDir := filepath.Join(home, "agent")
	reviewSettings(t, agentDir, "http://127.0.0.1:1")
	writeAppFile(t, filepath.Join(agentDir, "settings.json"), `{"defaultProvider":"anthropic","defaultModel":"m","compaction":{"enabled":false},"terminal":{"trueColor":false}}`)
	for _, name := range []string{"dark", "light"} {
		data, err := pitheme.BuiltinJSON(name)
		if err != nil {
			t.Fatal(err)
		}
		doc, err := pitheme.Parse(data)
		if err != nil {
			t.Fatal(err)
		}
		palette, err := doc.Compile(pitheme.ColorMode256)
		if err != nil {
			t.Fatal(err)
		}
		foreground, err := palette.ForegroundANSI("userMessageText")
		if err != nil {
			t.Fatal(err)
		}
		background, err := palette.BackgroundANSI("userMessageBg")
		if err != nil {
			t.Fatal(err)
		}
		store, err := session.New(session.OpenOptions{Cwd: work, SessionDir: filepath.Join(home, "sessions"), AgentDir: agentDir, Persist: true})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := store.AppendMessage(agentMessageUser("unmodified " + name + " surface")); err != nil {
			t.Fatal(err)
		}
		q := startReviewPTY(t, home, work, agentDir, "--deny-project", "--no-tools", "--session", store.File(), "--use-theme", name)
		q.prompt()
		if !strings.Contains(q.screen.Text(), "› unmodified "+name+" surface") || !strings.Contains(q.raw.String(), foreground) || !strings.Contains(q.raw.String(), background) {
			t.Fatalf("unmodified Pi %s theme did not render its user surface: fg=%q bg=%q screen=%q raw=%q", name, foreground, background, q.screen.Text(), q.raw.String())
		}
		q.write("\x04")
		q.finish()
	}
}

func TestActualStartupUsesTwoDistinctPiUserSurfaces(t *testing.T) {
	home, work := t.TempDir(), t.TempDir()
	agentDir := filepath.Join(home, "agent")
	themeDir := filepath.Join(agentDir, "themes")
	if err := os.MkdirAll(themeDir, 0700); err != nil {
		t.Fatal(err)
	}
	reviewSettings(t, agentDir, "http://127.0.0.1:1")
	fixtures := []struct{ name, foreground, background string }{
		{name: "plan5-teal", foreground: "#123456", background: "#abcdef"},
		{name: "plan5-amber", foreground: "#ffcc00", background: "#330044"},
	}
	for _, fixture := range fixtures {
		data, err := pitheme.BuiltinJSON("dark")
		if err != nil {
			t.Fatal(err)
		}
		var document map[string]any
		if err := json.Unmarshal(data, &document); err != nil {
			t.Fatal(err)
		}
		document["name"] = fixture.name
		colors := document["colors"].(map[string]any)
		colors["userMessageText"], colors["userMessageBg"] = fixture.foreground, fixture.background
		data, err = json.Marshal(document)
		if err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(themeDir, fixture.name+".json")
		if err := os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
		doc, err := pitheme.Parse(data)
		if err != nil {
			t.Fatal(err)
		}
		palette, err := doc.Compile(pitheme.ColorMode256)
		if err != nil {
			t.Fatal(err)
		}
		fg, err := palette.ForegroundANSI("userMessageText")
		if err != nil {
			t.Fatal(err)
		}
		bg, err := palette.BackgroundANSI("userMessageBg")
		if err != nil {
			t.Fatal(err)
		}
		store, err := session.New(session.OpenOptions{Cwd: work, SessionDir: filepath.Join(home, "sessions"), AgentDir: agentDir, Persist: true})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := store.AppendMessage(agentMessageUser("theme surface " + fixture.name)); err != nil {
			t.Fatal(err)
		}
		q := startReviewPTY(t, home, work, agentDir, "--deny-project", "--no-tools", "--session", store.File(), "--use-theme", fixture.name)
		q.prompt()
		if !strings.Contains(q.screen.Text(), "› theme surface "+fixture.name) || !strings.Contains(q.raw.String(), fg) || !strings.Contains(q.raw.String(), bg) {
			t.Fatalf("selected theme %q did not emit its own user surface: fg=%q bg=%q screen=%q", fixture.name, fg, bg, q.screen.Text())
		}
		q.write("\x04")
		q.finish()
	}
}

func TestActualStartupUsesSelectedPiThemeForUserSurface(t *testing.T) {
	home, work := t.TempDir(), t.TempDir()
	agentDir := filepath.Join(home, "agent")
	if err := os.MkdirAll(filepath.Join(agentDir, "themes"), 0700); err != nil {
		t.Fatal(err)
	}
	reviewSettings(t, agentDir, "http://127.0.0.1:1")
	data, err := pitheme.BuiltinJSON("dark")
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if err := json.Unmarshal(data, &document); err != nil {
		t.Fatal(err)
	}
	document["name"] = "selected"
	colors := document["colors"].(map[string]any)
	colors["userMessageText"], colors["userMessageBg"] = "#123456", "#abcdef"
	data, err = json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	themePath := filepath.Join(agentDir, "themes", "selected.json")
	if err := os.WriteFile(themePath, data, 0600); err != nil {
		t.Fatal(err)
	}
	doc, err := pitheme.Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	palette, err := doc.Compile(pitheme.ColorMode256)
	if err != nil {
		t.Fatal(err)
	}
	foreground, _ := palette.ForegroundANSI("userMessageText")
	background, _ := palette.BackgroundANSI("userMessageBg")

	store, err := session.New(session.OpenOptions{Cwd: work, SessionDir: filepath.Join(home, "sessions"), AgentDir: agentDir, Persist: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.AppendMessage(agentMessageUser("selected theme canary")); err != nil {
		t.Fatal(err)
	}
	q := startReviewPTY(t, home, work, agentDir, "--deny-project", "--no-tools", "--session", store.File(), "--use-theme", "selected")
	q.prompt()
	raw := q.raw.String()
	if !strings.Contains(q.screen.Text(), "› selected theme canary") {
		t.Fatalf("user role missing from startup transcript: %s", q.screen.Text())
	}
	if !strings.Contains(raw, foreground) || !strings.Contains(raw, background) {
		t.Fatalf("selected Pi foreground/background were not emitted: foreground=%q background=%q raw=%q", foreground, background, raw)
	}
	q.write("\x04")
	q.finish()
}

func TestLateAppearanceReplySelectsThemePairWithoutBecomingInput(t *testing.T) {
	home, work := t.TempDir(), t.TempDir()
	agentDir := filepath.Join(home, "agent")
	reviewSettings(t, agentDir, "http://127.0.0.1:1")
	q := startReviewPTY(t, home, work, agentDir, "--deny-project", "--no-tools", "--no-session", "--use-theme", "light/dark")
	q.prompt()
	q.write("\x1b[?997;2n")
	q.prompt()
	if strings.Contains(q.screen.Text(), "997;2") {
		t.Fatalf("appearance reply entered terminal text: %s", q.screen.Text())
	}
	q.write("/session\r")
	q.wait("theme light (light")
	q.prompt()
	q.write("\x04")
	q.finish()
}

func TestThemeDiagnosticsDoNotPrintThemeContents(t *testing.T) {
	path := filepath.Join(t.TempDir(), "broken.json")
	const canary = "private-content-canary"
	if err := os.WriteFile(path, []byte(`{"name":"broken","colors":{"text":"`+canary+`"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	catalog := pitheme.LoadCatalog(pitheme.CatalogOptions{CLIPaths: []string{path}, Mode: pitheme.ColorMode256})
	var out strings.Builder
	writeThemeDiagnostics(&out, catalog.Diagnostics(), "theme \"missing\" not found")
	if strings.Contains(out.String(), canary) {
		t.Fatalf("theme diagnostic included source contents: %s", out.String())
	}
}
