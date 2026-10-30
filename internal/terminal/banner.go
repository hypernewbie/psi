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
// fixed brand colors; psi has no brand palette, so the mark follows the theme:
// arms in accent, stem in borderAccent.
func PsiBanner(version, hints string, width int, tm *theme.Theme, noColor bool) []string {
	versionText := "v" + Sanitize(version)
	if width < 5+1+max(len(versionText), len(hints)) {
		// Pi's wordmark fallback: the version line first, key hints below.
		return []string{Sanitize("psi v" + version), hints}
	}
	arm := func(cell string) string { return bannerStyle(cell, "accent", tm, noColor) }
	stem := func(cell string) string { return bannerStyle(cell, "borderAccent", tm, noColor) }
	top := arm("█") + " " + stem("▄") + " " + arm("█") + " " + bannerStyle(versionText, "dim", tm, noColor)
	bottom := " " + arm("▀") + stem("█") + arm("▀") + " " + " " + hints
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
