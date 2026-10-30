package terminal

import (
	"psi/internal/theme"
)

// Pi's startup logo (pi-logo.ts) is a two-line half-block pixel mark, four
// cells wide: the first line carries the dim version beside it, the second
// line the key hints. psi keeps that anatomy with its own letter, a Psi drawn
// on a 5x4 pixel grid:
//
//	X . . . X   arms rise above the stem
//	X . X . X   stem starts below the arm tops
//	. X X X .   arms curve in and merge with the stem
//	. . X . .   bare stem to the baseline
//
// Packed into half blocks that is "█ ▄ █" over " ▀█▀ ". Pi paints its mark in
// fixed brand colors that stay constant across themes and only follow the
// terminal color mode (pi-logo.ts); psi's mark does the same with its own
// trio: violet left arm, amber stem, teal right arm.
var (
	psiViolet = theme.RGBColor(172, 140, 250)
	psiAmber  = theme.RGBColor(240, 190, 105)
	psiTeal   = theme.RGBColor(84, 196, 176)
)

// PsiBanner renders the two startup header lines. Narrow terminals get the
// plain fallback instead, like Pi's wordmark swap for terminals that cannot
// draw the logo: the version line first, key hints below.
func PsiBanner(version, hints string, width int, tm *theme.Theme, noColor bool) []string {
	versionText := "v" + Sanitize(version)
	if width < 5+1+max(len(versionText), len(hints)) {
		// Pi's wordmark fallback: the version line first, key hints below.
		return []string{Sanitize("psi v" + version), hints}
	}
	mode := theme.ColorMode256
	if tm != nil && tm.Mode != "" {
		mode = tm.Mode
	}
	brand := func(cell string, color theme.Color) string {
		if noColor {
			return cell
		}
		seq, err := theme.ForegroundANSI(color, mode)
		if err != nil {
			return cell
		}
		return seq + cell + "\x1b[39m"
	}
	top := brand("█", psiViolet) + " " + brand("▄", psiAmber) + " " + brand("█", psiTeal) + " " + bannerStyle(versionText, "dim", tm, noColor)
	bottom := " " + brand("▀", psiViolet) + brand("█", psiAmber) + brand("▀", psiTeal) + " " + " " + hints
	return []string{top, bottom}
}

func bannerStyle(text, token string, tm *theme.Theme, noColor bool) string {
	if noColor || tm == nil || text == "" {
		return text
	}
	styled, err := tm.Style(text, token, "", theme.TextStyle{})
	if err != nil {
		return text
	}
	return styled
}
