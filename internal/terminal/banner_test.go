package terminal

import (
	"strings"
	"testing"

	"psi/internal/theme"
)

func TestPsiBannerRendersMarkWithVersionOnBaseline(t *testing.T) {
	lines := PsiBanner("1.2.3", 80, nil, true)
	if len(lines) != 5 {
		t.Fatalf("banner lines = %d, want 5: %q", len(lines), lines)
	}
	want := []string{"█   █", "█ █ █", " ███", "  █", " ███  psi v1.2.3"}
	for i, line := range lines {
		if line != want[i] {
			t.Fatalf("line %d = %q, want %q", i, line, want[i])
		}
		if strings.Contains(line, "\x1b") {
			t.Fatalf("no-color banner carries escape codes: %q", line)
		}
	}
}

func TestPsiBannerNarrowTerminalsGetPlainTextLine(t *testing.T) {
	for _, width := range []int{0, 8, 16, PsiBannerWidth - 1} {
		lines := PsiBanner("0.1.0-dev", width, nil, true)
		if len(lines) != 1 || lines[0] != "psi v0.1.0-dev" {
			t.Fatalf("width %d banner = %q, want the single plain text line", width, lines)
		}
	}
}

func TestPsiBannerStylesMarkWithThemeAccent(t *testing.T) {
	generated := theme.GenerateSystemThemeColors(theme.SystemThemeInput{AppearanceHint: "light"})
	tm, err := theme.NewResolvedTheme("system", generated.Appearance, generated.Colors, theme.ColorMode256, generated.Dim)
	if err != nil {
		t.Fatal(err)
	}
	lines := PsiBanner("1.2.3", 80, tm, false)
	if !strings.Contains(lines[0], "\x1b[") {
		t.Fatalf("themed mark lost its accent color: %q", lines[0])
	}
	if !strings.Contains(lines[len(lines)-1], "psi v1.2.3") {
		t.Fatalf("baseline lost the version: %q", lines[len(lines)-1])
	}
	if !strings.HasSuffix(lines[len(lines)-1], "\x1b[39m") {
		t.Fatalf("themed baseline is not reset: %q", lines[len(lines)-1])
	}
}
