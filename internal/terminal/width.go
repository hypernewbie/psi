// Package terminal width/layout ports temp/pi-mono/packages/tui/src/utils.ts.
// Reference: visibleWidth, graphemeWidth, truncateToWidth, wrap logic.
// Go uses github.com/rivo/uniseg (grapheme) + github.com/mattn/go-runewidth (east asian).
package terminal

import (
	"strings"

	"github.com/mattn/go-runewidth"
	"github.com/rivo/uniseg"
)

// Graphemes splits s into grapheme clusters (Intl.Segmenter equivalent).
func Graphemes(s string) []string {
	var out []string
	gr := uniseg.NewGraphemes(s)
	for gr.Next() {
		out = append(out, string(gr.Str()))
	}
	return out
}

// GraphemeWidth ports pi's graphemeWidth: RGI emoji 2, regional indicators 2,
// eastAsianWidth base, spacing-mark trailing handling.
func GraphemeWidth(seg string) int {
	if seg == "" {
		return 0
	}
	// Strip leading non-printing (control/format/mark/surrogate approx).
	// runewidth handles most; we approximate pi's leadingNonPrintingRegex.
	runes := []rune(seg)
	// Find first printable base.
	base := rune(0)
	for _, r := range runes {
		if isNonPrinting(r) {
			continue
		}
		base = r
		break
	}
	if base == 0 {
		return 0
	}
	// Regional indicators always 2 (pi conservative).
	if base >= 0x1F1E6 && base <= 0x1F1FF {
		return 2
	}
	w := runewidth.RuneWidth(base)
	if w == 0 {
		w = 1
		// Emoji presentation: runewidth returns 1 for some emoji? force 2 for RGI ranges.
		if isEmojiBase(base) {
			w = 2
		}
	}
	// Trailing spacing marks (pi terminalSpacingMarkRegex approx): spacing marks add 1.
	for _, r := range runes[1:] {
		if isSpacingMark(r) {
			w++
		}
	}
	return w
}

func isNonPrinting(r rune) bool {
	if r < 0x20 || (r >= 0x7F && r <= 0x9F) {
		return true
	}
	// Cf, Me, Mn approx via runewidth zero-width + unicode ranges.
	if runewidth.RuneWidth(r) == 0 {
		return true
	}
	return false
}

func isSpacingMark(r rune) bool {
	// Pi terminalSpacingMarkRegex: Spacing_Mark except U+1734,U+302E,U+302F, plus listed.
	if r == 0x1734 || r == 0x302E || r == 0x302F {
		return false
	}
	// Mc (spacing mark) approximation.
	if r >= 0x0903 && r <= 0x0903 {
		return true
	}
	// Use unicode Mc range check via runewidth? Conservative: Mc runes have width 1 in runewidth but pi counts extra.
	// We treat known spacing-mark blocks.
	if (r >= 0x0900 && r <= 0x097F) || (r >= 0x0E30 && r <= 0x0E3A) || (r >= 0xFF00 && r <= 0xFFEF) {
		return runewidth.RuneWidth(r) > 0
	}
	return false
}

func isEmojiBase(r rune) bool {
	if r >= 0x1F000 && r <= 0x1FBFF {
		return true
	}
	if r >= 0x2300 && r <= 0x23FF {
		return true
	}
	if r >= 0x2600 && r <= 0x27BF {
		return true
	}
	if r >= 0x2B50 && r <= 0x2B55 {
		return true
	}
	return false
}

// CellWidth returns display cells (tabs = 8-cell stops, ANSI stripped by caller).
func CellWidth(s string) int {
	s = strings.ReplaceAll(s, "\t", "        ")
	w := 0
	gr := uniseg.NewGraphemes(s)
	for gr.Next() {
		w += GraphemeWidth(string(gr.Str()))
	}
	return w
}

func RuneWidth(r rune) int {
	return GraphemeWidth(string(r))
}

// TruncateToWidth cuts s to at most maxCells (no partial grapheme).
func TruncateToWidth(s string, maxCells int) string {
	if maxCells <= 0 {
		return ""
	}
	w := 0
	var b strings.Builder
	gr := uniseg.NewGraphemes(s)
	for gr.Next() {
		seg := string(gr.Str())
		// Tab expands.
		if seg == "\t" {
			if w+8 > maxCells {
				break
			}
			b.WriteString(seg)
			w += 8
			continue
		}
		gw := GraphemeWidth(seg)
		if w+gw > maxCells {
			break
		}
		b.WriteString(seg)
		w += gw
	}
	return b.String()
}
