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

func TestPsiBannerPaintsMarkInFixedBrandColors(t *testing.T) {
	generated := theme.GenerateSystemThemeColors(theme.SystemThemeInput{AppearanceHint: "light"})
	tm, err := theme.NewResolvedTheme("system", generated.Appearance, generated.Colors, theme.ColorMode256, generated.Dim)
	if err != nil {
		t.Fatal(err)
	}
	lines := PsiBanner("1.2.3", bannerHints, 80, tm, false)
	violet, err := theme.ForegroundANSI(psiViolet, tm.Mode)
	if err != nil {
		t.Fatal(err)
	}
	amber, err := theme.ForegroundANSI(psiAmber, tm.Mode)
	if err != nil {
		t.Fatal(err)
	}
	teal, err := theme.ForegroundANSI(psiTeal, tm.Mode)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(lines[0], violet+"█") || !strings.Contains(lines[0], amber+"▄") || !strings.Contains(lines[0], teal+"█") {
		t.Fatalf("mark lost its brand colors: %q", lines[0])
	}
	if !strings.Contains(lines[0], "v1.2.3") {
		t.Fatalf("logo line lost the version: %q", lines[0])
	}
	if plain := stripEscapes(lines[1]); !strings.Contains(plain, " ▀█▀ ") {
		t.Fatalf("hints line lost the mark: %q", plain)
	}
}

var ansiPattern = regexp.MustCompile("\x1b\\[[0-9;]*m")

func stripEscapes(s string) string {
	return ansiPattern.ReplaceAllString(s, "")
}
