package theme

import (
	"strings"
	"testing"
)

func TestCompilePiThemeStylesByTokenSlot(t *testing.T) {
	colors := completeColors()
	colors["accent"] = "okhsl(295 73% 59%)"
	colors["userMessageBg"] = "#112233"
	doc, err := Parse(themeFixture(t, colors, nil))
	if err != nil {
		t.Fatal(err)
	}
	theme, err := doc.Compile(ColorModeTruecolor)
	if err != nil {
		t.Fatal(err)
	}
	fg, err := theme.ForegroundANSI("accent")
	if err != nil {
		t.Fatal(err)
	}
	bg, err := theme.BackgroundANSI("userMessageBg")
	if err != nil {
		t.Fatal(err)
	}
	if fg != "\x1b[38;2;151;118;229m" || bg != "\x1b[48;2;17;34;51m" {
		t.Fatalf("theme colors: %q %q", fg, bg)
	}
	if _, err := theme.BackgroundANSI("accent"); err == nil || !strings.Contains(err.Error(), "foreground slot") {
		t.Fatalf("wrong-slot access error=%v", err)
	}
	styled, err := theme.Style("X", "accent", "userMessageBg", TextStyle{Bold: true, Italic: true})
	if err != nil {
		t.Fatal(err)
	}
	want := "\x1b[38;2;151;118;229m\x1b[48;2;17;34;51m\x1b[1m\x1b[3mX\x1b[23m\x1b[22m\x1b[49m\x1b[39m"
	if styled != want {
		t.Fatalf("style=%q, want %q", styled, want)
	}
}

func TestCompileUsesDefaultResetsAndPiAppearanceDetection(t *testing.T) {
	colors := completeColors()
	colors["text"] = "#ffffff"
	colors["userMessageBg"] = "#000000"
	doc, err := Parse(themeFixture(t, colors, nil))
	if err != nil {
		t.Fatal(err)
	}
	theme, err := doc.Compile(ColorMode256)
	if err != nil {
		t.Fatal(err)
	}
	if theme.Appearance != "dark" {
		t.Fatalf("detected appearance=%q", theme.Appearance)
	}
	if got, err := theme.ForegroundANSI("muted"); err != nil || got != "\x1b[39m" {
		t.Fatalf("terminal-default foreground reset=%q, %v", got, err)
	}
	if got, err := theme.BackgroundANSI("selectedBg"); err != nil || got != "\x1b[49m" {
		t.Fatalf("terminal-default background reset=%q, %v", got, err)
	}
}

func TestCompileRejectsInvalidColorAfterReferenceResolution(t *testing.T) {
	colors := completeColors()
	colors["accent"] = "#ggg"
	doc, err := Parse(themeFixture(t, colors, nil))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := doc.Compile(ColorModeTruecolor); err == nil || !strings.Contains(err.Error(), "colors.accent") {
		t.Fatalf("invalid color compile error=%v", err)
	}
}
