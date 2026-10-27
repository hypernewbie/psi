package terminal

import (
	"strings"

	"github.com/rivo/uniseg"
)

// Layout shares source boundaries, soft wraps and cursor cells with navigation.
type Layout struct {
	Rows       []string
	RowRunes   [][2]int
	CursorRow  int
	CursorCol  int
	TotalRows  int
	PromptCols int
	limit      int
}

func LayoutDraft(buf []rune, pos int, width int) Layout {
	width = max(1, width)
	limit := max(1, width-1)
	prefix, continuation := "> ", "  "
	if width < 4 {
		prefix, continuation = "", ""
	}
	promptCols := len(prefix)
	pos = max(0, min(pos, len(buf)))
	var rows []string
	var ranges [][2]int
	var line strings.Builder
	line.WriteString(prefix)
	cells, sourcePos, rowStart := promptCols, 0, 0
	cursorRow, cursorCol := 0, promptCols
	flush := func() {
		rows = append(rows, line.String())
		ranges = append(ranges, [2]int{rowStart, sourcePos})
		line.Reset()
		line.WriteString(continuation)
		cells = promptCols
		rowStart = sourcePos
	}
	gr := uniseg.NewGraphemes(string(buf))
	for gr.Next() {
		text := gr.Str()
		n := len([]rune(text))
		if text == "\n" {
			if sourcePos == pos {
				cursorRow, cursorCol = len(rows), cells
			}
			flush()
			sourcePos += n
			rowStart = sourcePos // the next row must not include the newline
			continue
		}
		gw := GraphemeWidth(text)
		if text == "\t" {
			gw = 8 - cells%8
		}
		if cells > promptCols && cells+gw > limit {
			flush()
			if text == "\t" {
				gw = 8 - cells%8
			}
		}
		// Capture the cursor after deciding which row owns this source boundary.
		if sourcePos == pos {
			cursorRow, cursorCol = len(rows), cells
		}
		if text == "\t" {
			gw = min(gw, limit-cells)
			line.WriteString(strings.Repeat(" ", max(0, gw)))
		} else if gw > limit-promptCols {
			// A wide cluster cannot fit a one-cell terminal. Preserve its source
			// and use a one-cell placeholder only in the viewport.
			text, gw = "?", 1
			line.WriteString(text)
		} else {
			line.WriteString(text)
		}
		cells += gw
		sourcePos += n
	}
	if pos == len(buf) {
		cursorRow, cursorCol = len(rows), cells
	}
	rows = append(rows, line.String())
	ranges = append(ranges, [2]int{rowStart, len(buf)})
	return Layout{Rows: rows, RowRunes: ranges, CursorRow: cursorRow, CursorCol: min(cursorCol, width-1), TotalRows: len(rows), PromptCols: promptCols, limit: limit}
}

func (l Layout) VisualRunePos(buf []rune, row, col int) int {
	row = max(0, min(row, len(l.RowRunes)-1))
	if len(l.RowRunes) == 0 {
		return len(buf)
	}
	start, end := l.RowRunes[row][0], l.RowRunes[row][1]
	cells, pos := l.PromptCols, start
	gr := uniseg.NewGraphemes(string(buf[start:end]))
	for gr.Next() {
		text := gr.Str()
		gw := GraphemeWidth(text)
		if text == "\t" {
			gw = min(8-cells%8, l.limit-cells)
		} else if gw > l.limit-l.PromptCols {
			gw = 1
		}
		if cells+gw > col {
			break
		}
		cells += gw
		pos += len([]rune(text))
	}
	return pos
}
