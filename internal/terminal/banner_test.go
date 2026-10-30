package terminal

import (
	"regexp"
	"strings"
	"testing"

	"psi/internal/theme"
)

const bannerHints = "esc interrupt · ctrl+c/ctrl+d clear/exit · / commands"

func TestPsiBannerRendersTwoLineMarkWithVersionAndHints(t *testing.T) {
	lines := PsiBanner("1.2.3", bannerHints, 80, nil, true)
	if len(lines) != 2 {
		t.Fatalf("banner lines = %d, want 2: %q", len(lines), lines)
	}
	if lines[0] != "█ ▄ █ v1.2.3" {
		t.Fatalf("logo line = %q", lines[0])
	}
	if lines[1] != " ▀█▀  "+bannerHints {
		t.Fatalf("hints line = %q", lines[1])
	}
	for i, line := range lines {
		if strings.Contains(line, "\x1b") {
			t.Fatalf("no-color banner line %d carries escape codes: %q", i, line)
		}
	}
}

func TestPsiBannerNarrowTerminalsGetPlainTextFallback(t *testing.T) {
	lines := PsiBanner("0.1.0-dev", bannerHints, 20, nil, true)
	if len(lines) != 2 || lines[0] != "psi v0.1.0-dev" || lines[1] != bannerHints {
		t.Fatalf("fallback banner = %q, want version line then hints", lines)
	}
}

func TestPsiBannerStylesMarkWithThemeTokens(t *testing.T) {
	generated := theme.GenerateSystemThemeColors(theme.SystemThemeInput{AppearanceHint: "light"})
	tm, err := theme.NewResolvedTheme("system", generated.Appearance, generated.Colors, theme.ColorMode256, generated.Dim)
	if err != nil {
		t.Fatal(err)
	}
	lines := PsiBanner("1.2.3", bannerHints, 80, tm, false)
	if !strings.Contains(lines[0], "\x1b[") {
		t.Fatalf("themed mark lost its color: %q", lines[0])
	}
	if !strings.Contains(lines[0], "v1.2.3") || !strings.HasSuffix(lines[0], "\x1b[39m") {
		t.Fatalf("logo line lost the dim version or its reset: %q", lines[0])
	}
	if plain := stripEscapes(lines[1]); !strings.Contains(plain, " ▀█▀ ") {
		t.Fatalf("hints line lost the mark: %q", plain)
	}
}

var ansiPattern = regexp.MustCompile("\x1b\\[[0-9;]*m")

func stripEscapes(s string) string {
	return ansiPattern.ReplaceAllString(s, "")
}
