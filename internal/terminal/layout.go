package terminal

import (
	"strings"
	"unicode"
	"unicode/utf8"

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
	// Pi's editor has horizontal borders, not a prompt prefix. The owned
	// editor rows therefore contain draft text only.
	promptCols := 0
	pos = max(0, min(pos, len(buf)))
	var rows []string
	var ranges [][2]int
	var line strings.Builder
	cells, sourcePos, rowStart := promptCols, 0, 0
	cursorRow, cursorCol := 0, promptCols
	cursorSet := false
	// The last UAX #14 opportunity in the current row: source offset after
	// the breakable cluster, cells at that offset, and rendered bytes there.
	wrapOpp, wrapOppCells, wrapOppLine := -1, 0, 0
	trimSoftCut := func(s string) string {
		return strings.TrimRightFunc(s, func(r rune) bool {
			// Keep nonbreaking spaces visible even at an emergency cut.
			switch r {
			case '\u00a0', // NO-BREAK SPACE
				'\u2007', // FIGURE SPACE
				'\u202f': // NARROW NO-BREAK SPACE
				return false
			}
			return unicode.Is(unicode.Zs, r)
		})
	}
	flush := func(trim bool) {
		row := line.String()
		if trim {
			row = trimSoftCut(row)
		}
		rows = append(rows, row)
		ranges = append(ranges, [2]int{rowStart, sourcePos})
		line.Reset()
		cells = promptCols
		rowStart = sourcePos
		wrapOpp = -1
	}
	// Re-render a tab-containing candidate suffix at column zero. Its
	// columns cannot be recovered from the old row's rendered bytes.
	renderCandidate := func(start, end, cursorPos int) (string, int, int, bool) {
		var b strings.Builder
		next, foundCursor := promptCols, -1
		candidatePos, state := start, -1
		rest := string(buf[start:end])
		for len(rest) > 0 {
			text, remainder, _, newState := uniseg.StepString(rest, state)
			rest, state = remainder, newState
			if text == "\n" {
				return "", 0, -1, false
			}
			gw := 1
			if len(text) != 1 || text[0] >= 0x80 {
				gw = GraphemeWidth(text)
			}
			if text == "\t" {
				gw = 8 - next%8
			}
			if next > promptCols && next+gw > limit {
				return "", 0, -1, false
			}
			if candidatePos == cursorPos {
				foundCursor = next
			}
			if text == "\t" {
				gw = min(gw, limit-next)
				b.WriteString(strings.Repeat(" ", max(0, gw)))
			} else if gw > limit-promptCols {
				text, gw = "?", 1
				b.WriteString(text)
			} else {
				b.WriteString(text)
			}
			next += gw
			candidatePos += utf8.RuneCountInString(text)
		}
		if candidatePos == cursorPos {
			foundCursor = next
		}
		return b.String(), next, foundCursor, candidatePos == end
	}
	// tryWrap moves the current row suffix to a new row when that suffix and
	// the current grapheme fit together there.
	tryWrap := func(text string, gw int) (int, bool) {
		if wrapOpp <= rowStart || wrapOpp > sourcePos {
			return gw, false
		}
		cursorInSuffix := cursorSet && cursorRow == len(rows) && pos >= wrapOpp && pos < sourcePos
		tabSuffix := false
		for _, r := range buf[wrapOpp:sourcePos] {
			if r == '\t' {
				tabSuffix = true
				break
			}
		}
		var suffix string
		var suffixCells, candidateCursor int
		if tabSuffix {
			var ok bool
			suffix, suffixCells, candidateCursor, ok = renderCandidate(wrapOpp, sourcePos, pos)
			if !ok {
				return gw, false
			}
		} else {
			suffix = line.String()[wrapOppLine:]
			suffixCells = cells - wrapOppCells
		}
		candidateGW := gw
		if text == "\t" {
			candidateGW = 8 - suffixCells%8
		} else if gw > limit-promptCols {
			candidateGW = 1
		}
		if suffixCells+candidateGW > limit {
			return gw, false
		}
		rows = append(rows, trimSoftCut(line.String()[:wrapOppLine]))
		ranges = append(ranges, [2]int{rowStart, wrapOpp})
		line.Reset()
		line.WriteString(suffix)
		cells = suffixCells
		rowStart = wrapOpp
		wrapOpp = -1
		if text == "\t" {
			gw = 8 - cells%8
		}
		if cursorInSuffix {
			if tabSuffix {
				cursorCol = candidateCursor
			} else {
				cursorCol -= wrapOppCells
			}
			cursorRow = len(rows)
		}
		return gw, true
	}
	rest, state := string(buf), -1
	for len(rest) > 0 {
		text, remainder, boundaries, newState := uniseg.StepString(rest, state)
		rest, state = remainder, newState
		if len(text) == 1 && text[0] < 0x80 {
			if text[0] == '\n' {
				if sourcePos == pos {
					cursorRow, cursorCol = len(rows), cells
					cursorSet = true
				}
				flush(false)
				sourcePos++
				rowStart = sourcePos
				continue
			}
			gw := 1
			if text[0] == '\t' {
				gw = 8 - cells%8
			}
			if cells > promptCols && cells+gw > limit {
				var wrapped bool
				gw, wrapped = tryWrap(text, gw)
				if !wrapped {
					flush(true)
					if text[0] == '\t' {
						gw = 8 - cells%8
					}
				}
			}
			if sourcePos == pos {
				cursorRow, cursorCol = len(rows), cells
				cursorSet = true
			}
			if text[0] == '\t' {
				gw = min(gw, limit-cells)
				line.WriteString(strings.Repeat(" ", max(0, gw)))
			} else {
				line.WriteByte(text[0])
			}
			cells += gw
			sourcePos++
			if boundaries&uniseg.MaskLine == uniseg.LineCanBreak {
				wrapOpp, wrapOppCells, wrapOppLine = sourcePos, cells, line.Len()
			}
			continue
		}
		n := utf8.RuneCountInString(text)
		gw := GraphemeWidth(text)
		if text == "\t" {
			gw = 8 - cells%8
		}
		if cells > promptCols && cells+gw > limit {
			var wrapped bool
			gw, wrapped = tryWrap(text, gw)
			if !wrapped {
				flush(true)
				if text == "\t" {
					gw = 8 - cells%8
				}
			}
		}
		// Capture the cursor after deciding which row owns this source boundary.
		if sourcePos == pos {
			cursorRow, cursorCol = len(rows), cells
			cursorSet = true
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
		if boundaries&uniseg.MaskLine == uniseg.LineCanBreak {
			wrapOpp, wrapOppCells, wrapOppLine = sourcePos, cells, line.Len()
		}
	}
	if pos == len(buf) {
		cursorRow, cursorCol = len(rows), cells
	}
	rows = append(rows, line.String())
	ranges = append(ranges, [2]int{rowStart, len(buf)})
	return Layout{Rows: rows, RowRunes: ranges, CursorRow: cursorRow, CursorCol: min(cursorCol, width-1), TotalRows: len(rows), PromptCols: promptCols, limit: limit}
}

func (l Layout) softWrappedRow(row int) bool {
	return row+1 < len(l.RowRunes) && l.RowRunes[row][1] == l.RowRunes[row+1][0]
}

// verticalMoveMaxCol keeps navigation within the visible part of the row,
// at or before its last owned cursor boundary.
func (l Layout) verticalMoveMaxCol(buf []rune, row int) int {
	width := l.RowWidth(row)
	if !l.softWrappedRow(row) {
		return width
	}
	_, col := l.visualRunePos(buf, row, width)
	return col
}

// VisualRunePos maps a column to a cursor boundary owned by the requested row.
func (l Layout) VisualRunePos(buf []rune, row, col int) int {
	pos, _ := l.visualRunePos(buf, row, col)
	return pos
}

func (l Layout) visualRunePos(buf []rune, row, col int) (runePos, actualCol int) {
	if len(l.RowRunes) == 0 {
		return len(buf), 0
	}
	row = max(0, min(row, len(l.RowRunes)-1))
	start, end := l.RowRunes[row][0], l.RowRunes[row][1]
	softWrap := l.softWrappedRow(row)
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
		next := pos + utf8.RuneCountInString(text)
		// A soft cut's end boundary is the start of the following row.
		if cells+gw > col || (softWrap && next == end) {
			break
		}
		cells += gw
		pos = next
	}
	return pos, cells
}

// VisibleWidth returns the visual cell width of a string.
func VisibleWidth(s string) int {
	return CellWidth(s)
}

// RowWidth returns the visual width (maxVisualCol) of a visual row.
func (l Layout) RowWidth(row int) int {
	if row < 0 || row >= len(l.Rows) {
		return 0
	}
	return VisibleWidth(l.Rows[row])
}
