package terminal

import (
	"strings"

	"psi/internal/theme"
)

// psiMark is psi's startup mark: the Greek letter Ψ in block pixels, five
// rows tall. Pi's startup header carries a small pixel logo with the dim
// version beside it; psi keeps that structure with its own letter.
var psiMark = [5]string{
	"█   █",
	"█ █ █",
	" ███ ",
	"  █  ",
	" ███ ",
}

// PsiBannerWidth is the smallest terminal width that fits the mark plus the
// version on its baseline. Narrow terminals get the plain text line instead,
// like Pi's wordmark fallback for terminals that cannot draw its logo.
const PsiBannerWidth = 28

// PsiBanner renders the startup header lines. The mark uses the theme accent;
// the baseline row carries the dim version beside it, like Pi's logo line.
func PsiBanner(version string, width int, tm *theme.Theme, noColor bool) []string {
	versionText := Sanitize("psi v" + version)
	if width < PsiBannerWidth {
		return []string{versionText}
	}
	lines := make([]string, len(psiMark))
	for i, row := range psiMark {
		mark := bannerStyle(strings.TrimRight(row, " "), "accent", tm, noColor)
		if i == len(psiMark)-1 {
			mark += "  " + bannerStyle(versionText, "dim", tm, noColor)
		}
		lines[i] = mark
	}
	return lines
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
