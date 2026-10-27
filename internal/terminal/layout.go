package terminal

import (
	"strings"

	"github.com/rivo/uniseg"
)

// Layout ports the shared wrap/cursor function (P2-016, pi utils.ts/tui editor).
// It calculates soft wraps, logical lines, visual rows, and cursor cells together.
// Tabs display at an eight-cell stop without replacing stored tabs.
type Layout struct {
	Rows       []string // visual rows (display only, tabs expanded, sanitized by caller)
	RowRunes   [][2]int // rune index range [start,end) in source buf for each visual row
	CursorRow  int
	CursorCol  int // in cells (including 2-cell prompt prefix)
	TotalRows  int
	PromptCols int // width of "> " prefix (2)
}

func LayoutDraft(buf []rune, pos int, width int) Layout {
	if width < 4 {
		width = 80
	}
	if pos < 0 {
		pos = 0
	}
	if pos > len(buf) {
		pos = len(buf)
	}
	limit := width - 1 // leave final column unused (avoid delayed-wrap)
	src := string(buf)
	// Byte offset of cursor.
	bytePos := 0
	for i := 0; i < pos && i < len(buf); i++ {
		bytePos += len(string(buf[i]))
	}
	// Split source into grapheme segments with byte ranges.
	type seg struct {
		text       string
		start, end int // byte offsets
		newline    bool
		tab        bool
		width      int
	}
	var segs []seg
	gr := uniseg.NewGraphemes(src)
	for gr.Next() {
		start, end := gr.Positions()
		t := src[start:end]
		if t == "\n" {
			segs = append(segs, seg{text: t, start: start, end: end, newline: true})
			continue
		}
		if t == "\t" {
			segs = append(segs, seg{text: t, start: start, end: end, tab: true})
			continue
		}
		segs = append(segs, seg{text: t, start: start, end: end, width: GraphemeWidth(t)})
	}
	// Build visual rows.
	var rows []string
	var rowRunes [][2]int
	var cur strings.Builder
	cur.WriteString("> ")
	curCells := 2
	rowStartRune := 0
	runeIdx := 0 // rune index of next seg start
	cursorRow, cursorCol := 0, 2
	// Map byte offset -> rune index for row starts and cursor.
	byteToRune := func(boff int) int {
		n, ri := 0, 0
		for ri < len(buf) {
			l := len(string(buf[ri]))
			if n+l > boff {
				break
			}
			n += l
			ri++
		}
		return ri
	}
	flush := func() {
		rows = append(rows, cur.String())
		// rowRunes end is current runeIdx (start of next row).
		rowRunes = append(rowRunes, [2]int{rowStartRune, runeIdx})
		cur.Reset()
		cur.WriteString("  ")
		curCells = 2
		rowStartRune = runeIdx
	}
	for _, sg := range segs {
		// Cursor before this seg?
		if sg.start == bytePos {
			cursorRow = len(rows)
			cursorCol = curCells
		}
		// seg rune length.
		segRunes := len([]rune(sg.text))
		if sg.newline {
			flush()
			runeIdx += segRunes // consume \n (1 rune)
			continue
		}
		if sg.tab {
			next := ((curCells + 8) / 8) * 8
			if curCells > 2 && next > limit {
				flush()
				next = 8
			}
			// If cursor inside tab, snap to tab start (tabs are atomic for cursor).
			for curCells < next {
				cur.WriteByte(' ')
				curCells++
			}
			runeIdx += segRunes
			continue
		}
		if curCells > 2 && curCells+sg.width > limit {
			flush()
		}
		cur.WriteString(sg.text)
		curCells += sg.width
		runeIdx += segRunes
	}
	if bytePos == len(src) {
		cursorRow = len(rows)
		cursorCol = curCells
	}
	rows = append(rows, cur.String())
	rowRunes = append(rowRunes, [2]int{rowStartRune, len(buf)})
	_ = byteToRune
	return Layout{Rows: rows, RowRunes: rowRunes, CursorRow: cursorRow, CursorCol: cursorCol, TotalRows: len(rows), PromptCols: 2}
}

// VisualRunePos maps (row, col cells) to a rune index (grapheme-safe).
func (l Layout) VisualRunePos(buf []rune, row, col int) int {
	if row < 0 {
		row = 0
	}
	if row >= len(l.Rows) {
		row = len(l.Rows) - 1
	}
	if row < 0 || row >= len(l.RowRunes) {
		return len(buf)
	}
	start, end := l.RowRunes[row][0], l.RowRunes[row][1]
	// Walk graphemes from start, accumulating cells (prefix 2).
	cells := 2
	src := string(buf)
	// Convert rune range to byte range.
	boff := 0
	for i := 0; i < start && i < len(buf); i++ {
		boff += len(string(buf[i]))
	}
	endBoff := boff
	for i := start; i < end && i < len(buf); i++ {
		endBoff += len(string(buf[i]))
	}
	sub := ""
	if boff <= len(src) && endBoff <= len(src) && boff <= endBoff {
		sub = src[boff:endBoff]
	}
	// Remove newlines (row terminators) from measurement.
	sub = strings.ReplaceAll(sub, "\n", "")
	gr := uniseg.NewGraphemes(sub)
	consumed := 0
	ri := start
	for gr.Next() {
		seg := string(gr.Str())
		if seg == "\t" {
			next := ((cells + 8) / 8) * 8
			if cells >= col {
				break
			}
			cells = next
			consumed += len(seg)
			ri += len([]rune(seg))
			continue
		}
		gw := GraphemeWidth(seg)
		if cells+gw > col {
			break
		}
		cells += gw
		consumed += len(seg)
		ri += len([]rune(seg))
	}
	if ri > end {
		ri = end
	}
	return ri
}
