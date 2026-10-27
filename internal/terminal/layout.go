package terminal

import (
	"strings"

	"github.com/rivo/uniseg"
)

// Layout ports the shared wrap/cursor function (P2-016).
// It calculates soft wraps, logical lines, visual rows, and cursor cells together.
// Tabs display at an eight-cell stop without replacing stored tabs.
type Layout struct {
	Rows       []string // visual rows (with tab expansion for display only)
	RowStarts  []int    // byte offset of each visual row start in source
	CursorRow  int
	CursorCol  int // in cells
	TotalRows  int
	PromptCols int // width of "> " prefix (2)
}

func LayoutDraft(buf []rune, pos int, width int) Layout {
	if width < 4 {
		width = 80
	}
	limit := width - 1 // leave final column unused (avoid delayed-wrap)
	src := string(buf)
	// Map rune index -> grapheme boundaries via uniseg for cursor.
	// Build visual rows with wrapping.
	var rows []string
	var starts []int
	var cur strings.Builder
	curCells := 0
	prefix := "> "
	cur.WriteString(prefix)
	curCells = 2
	byteOff := 0
	// Track cursor row/col.
	cursorRow, cursorCol := 0, 2
	// Iterate graphemes to respect clusters.
	gr := uniseg.NewGraphemes(src)
	// We need rune-pos mapping: pos is rune index. Convert to byte offset.
	bytePos := 0
	rc := 0
	for _, r := range buf {
		if rc == pos {
			break
		}
		bytePos += len(string(r))
		rc++
	}
	consumed := 0
	_ = byteOff
	for gr.Next() {
		start, end := gr.Positions()
		seg := src[start:end]
		if consumed == bytePos {
			cursorRow = len(rows)
			cursorCol = curCells
		}
		consumed = end
		if seg == "\n" {
			rows = append(rows, cur.String())
			starts = append(starts, 0)
			cur.Reset()
			cur.WriteString("  ")
			curCells = 2
			continue
		}
		if seg == "\t" {
			// Advance to next 8-cell stop.
			next := ((curCells + 8) / 8) * 8
			if next-curCells+curCells > limit && curCells > 2 {
				rows = append(rows, cur.String())
				starts = append(starts, 0)
				cur.Reset()
				cur.WriteString("  ")
				curCells = 2
				next = 8
			}
			for curCells < next {
				cur.WriteByte(' ')
				curCells++
			}
			continue
		}
		gw := GraphemeWidth(seg)
		if curCells+gw > limit && curCells > 2 {
			rows = append(rows, cur.String())
			starts = append(starts, 0)
			cur.Reset()
			cur.WriteString("  ")
			curCells = 2
		}
		cur.WriteString(seg)
		curCells += gw
	}
	if consumed == bytePos {
		cursorRow = len(rows)
		cursorCol = curCells
	}
	rows = append(rows, cur.String())
	starts = append(starts, 0)
	return Layout{Rows: rows, RowStarts: starts, CursorRow: cursorRow, CursorCol: cursorCol, TotalRows: len(rows), PromptCols: 2}
}
