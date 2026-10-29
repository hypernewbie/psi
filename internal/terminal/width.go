package terminal

import (
	"strings"

	"github.com/rivo/uniseg"
)

// Use one Unicode width policy for editing, wrapping, clipping, and cursor moves.
func Graphemes(s string) []string {
	var out []string
	gr := uniseg.NewGraphemes(s)
	for gr.Next() {
		out = append(out, gr.Str())
	}
	return out
}

func GraphemeWidth(s string) int {
	// UTS #51 keycap sequences occupy two terminal cells. uniseg v0.4.7
	// measures their ASCII base as one cell, even with emoji presentation.
	if strings.HasSuffix(s, "\u20e3") && len(s) > 0 && strings.ContainsRune("#*0123456789", rune(s[0])) {
		return 2
	}
	return uniseg.StringWidth(s)
}

func CellWidth(s string) int {
	cells := 0
	gr := uniseg.NewGraphemes(s)
	for gr.Next() {
		if gr.Str() == "\t" {
			cells += 8 - cells%8
		} else {
			cells += GraphemeWidth(gr.Str())
		}
	}
	return cells
}

func RuneWidth(r rune) int { return GraphemeWidth(string(r)) }

// SliceCells returns the part of s that starts at startCells and spans at most
// maxCells cells. It uses the same grapheme and width policy as the editor so
// a picker query and its cursor column always agree.
func SliceCells(s string, startCells, maxCells int) string {
	if maxCells <= 0 {
		return ""
	}
	if startCells <= 0 {
		return TruncateToWidth(s, maxCells)
	}
	cells := 0
	var out strings.Builder
	gr := uniseg.NewGraphemes(s)
	for gr.Next() {
		w := GraphemeWidth(gr.Str())
		if cells+w <= startCells {
			cells += w
			continue
		}
		if cells+w > startCells+maxCells {
			break
		}
		out.WriteString(gr.Str())
		cells += w
	}
	return out.String()
}

func TruncateToWidth(s string, maxCells int) string {
	var out strings.Builder
	cells := 0
	gr := uniseg.NewGraphemes(s)
	for gr.Next() {
		width := GraphemeWidth(gr.Str())
		if gr.Str() == "\t" {
			width = 8 - cells%8
		}
		if cells+width > maxCells {
			break
		}
		out.WriteString(gr.Str())
		cells += width
	}
	return out.String()
}
