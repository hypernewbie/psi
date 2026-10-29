package terminal

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	pitheme "psi/internal/theme"
)

func markdownHeadingTheme(t *testing.T, color string) *pitheme.Theme {
	t.Helper()
	data, err := pitheme.BuiltinJSON("dark")
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	doc["colors"].(map[string]any)["text"] = color
	data, err = json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := pitheme.Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	palette, err := parsed.Compile(pitheme.ColorMode256)
	if err != nil {
		t.Fatal(err)
	}
	return palette
}

func TestThemeChangeRendersOnlyActiveMarkdownTail(t *testing.T) {
	oldTheme := markdownHeadingTheme(t, "#123456")
	newTheme := markdownHeadingTheme(t, "#abcdef")
	oldHeading, err := oldTheme.ForegroundANSI("text")
	if err != nil {
		t.Fatal(err)
	}
	newHeading, err := newTheme.ForegroundANSI("text")
	if err != nil {
		t.Fatal(err)
	}
	if oldHeading == newHeading {
		t.Fatalf("fixture headings quantized to the same color %q", oldHeading)
	}
	stream := NewMarkdownStream(64, false)
	stream.SetTheme(oldTheme)
	stable, _ := stream.Feed("committed **heading**\n\n")
	committed := append([]string(nil), stable...)
	stream.Feed("active **heading**")
	oldTail := append([]string(nil), stream.DisplayTail()...)
	if !strings.Contains(strings.Join(oldTail, "\n"), oldHeading) {
		t.Fatalf("active Markdown did not use initial theme: %q", oldTail)
	}
	stream.SetTheme(newTheme)
	newTail := stream.DisplayTail()
	if !strings.Contains(strings.Join(newTail, "\n"), newHeading) || strings.Contains(strings.Join(newTail, "\n"), oldHeading) {
		t.Fatalf("active Markdown did not use reloaded theme: old=%q new=%q", oldTail, newTail)
	}
	if !reflect.DeepEqual(committed, stable) || strings.Contains(strings.Join(committed, "\n"), newHeading) {
		t.Fatalf("theme change rewrote committed scrollback: before=%q after=%q", committed, stable)
	}
	if got, want := strings.Join(newTail, "\n"), strings.Join(oldTail, "\n"); strings.ReplaceAll(got, newHeading, "<heading>") != strings.ReplaceAll(want, oldHeading, "<heading>") {
		t.Fatalf("active source changed during theme refresh: old=%q new=%q", oldTail, newTail)
	}
}
