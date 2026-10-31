package terminal

import (
	"bytes"
	"fmt"
	"io"
	"strings"
)

// Frame represents one inline main-screen update. Ordinary frames append source
// and replace only the bounded active tail; FullRedraw replaces the retained document.
// Content is sanitized before composition. Cursor coordinates are tail-relative.
type Frame struct {
	Append       string
	DocumentRows []string
	FullRedraw   bool
	Tail         []string
	// TailCellWidth carries plain-text cell widths when Tail rows contain
	// trusted presentation SGR. Content width must never count ANSI bytes.
	TailCellWidth []int
	CursorRow     int
	CursorCol     int
	CursorVisible bool
	HideCursor    bool
	ShowCursor    bool
	Height        int
	Width         int
}

// Writer is owned by the application event loop. No other writer may move its cursor.
type Writer struct {
	Out                          io.Writer
	prevTail                     []string
	prevTailCellWidth            []int
	prevCursorRow, prevCursorCol int
	resizeCursorRow              int
	resizeCursorKnown            bool
	writes, frames, bytes        int
	unchanged                    int
	invalidTail                  bool
}

func NewWriter(out io.Writer) *Writer { return &Writer{Out: out} }

// InvalidateTail discards differential row assumptions for the compatibility
// path that redraws only an owned tail. Full-document reflow uses Frame.FullRedraw.
func (w *Writer) InvalidateTail() { w.invalidTail = true }

// SetResizeCursorPosition supplies a cursor position for the owned-tail
// compatibility path. Coordinates are zero-based screen cells.
func (w *Writer) SetResizeCursorPosition(row, _ int) {
	w.resizeCursorRow = max(0, row)
	w.resizeCursorKnown = true
}

func (w *Writer) Stats() (writes, frames, bytes, unchanged int) {
	return w.writes, w.frames, w.bytes, w.unchanged
}

func (w *Writer) Emit(f Frame) error {
	w.frames++
	if f.FullRedraw {
		return w.emitFullDocument(f)
	}
	if !w.invalidTail && f.Append == "" && equalRows(w.prevTail, f.Tail) && f.CursorRow == w.prevCursorRow && f.CursorCol == w.prevCursorCol && !f.HideCursor && !f.ShowCursor {
		w.unchanged++
		return nil
	}
	if w.invalidTail && w.resizeCursorKnown && f.Width > 0 && f.Height > 0 {
		return w.emitResizedFrame(f)
	}
	var b bytes.Buffer
	row, col := w.prevCursorRow, w.prevCursorCol
	move := func(r, c int) {
		if r < row {
			fmt.Fprintf(&b, "\x1b[%dA", row-r)
		} else if r > row {
			fmt.Fprintf(&b, "\x1b[%dB", r-row)
		}
		if c != col || r != row {
			b.WriteByte('\r')
			if c > 0 {
				fmt.Fprintf(&b, "\x1b[%dC", c)
			}
		}
		row, col = r, c
	}
	old := w.prevTail
	invalidated := w.invalidTail
	oldLen := len(old)
	if invalidated && f.Height > 0 && oldLen > f.Height {
		oldLen = f.Height
	}
	staleRows := oldLen
	if invalidated {
		if f.Height > 0 && row >= f.Height {
			row = f.Height - 1
		}
		move(0, 0)
		old = nil
		oldLen = 0
	}
	if f.Append != "" {
		if len(old) > 0 {
			// Remove only the old viewport, never rows preceding its anchor.
			// Replace row zero before erasing its stale suffix to avoid a blank frame.
			for i := len(old) - 1; i > 0; i-- {
				move(i, 0)
				b.WriteString("\x1b[K")
			}
			move(0, 0)
			if i := strings.IndexByte(f.Append, '\n'); i >= 0 {
				b.WriteString(strings.TrimSuffix(f.Append[:i], "\r"))
				b.WriteString("\x1b[K\r\n")
				b.WriteString(f.Append[i+1:])
			} else {
				b.WriteString(f.Append)
				b.WriteString("\x1b[K")
			}
		} else if w.invalidTail {
			// Native reflow may have left old chrome on the rows receiving
			// committed text. Clear each suffix without walking the stale
			// old row count (which can exceed the new viewport height).
			text := strings.ReplaceAll(f.Append, "\r\n", "\n")
			text = strings.ReplaceAll(text, "\n", "\x1b[K\r\n")
			b.WriteString(text)
			if !strings.HasSuffix(f.Append, "\n") {
				b.WriteString("\x1b[K")
			}
		} else {
			b.WriteString(f.Append)
		}
		old = nil
		oldLen = 0
		row, col = 0, 0
	}
	maxRows := max(max(oldLen, staleRows), len(f.Tail))
	if invalidated && f.Height > 0 && maxRows > f.Height {
		maxRows = f.Height
	}
	for i := 0; i < maxRows; i++ {
		if invalidated && i >= len(f.Tail) {
			continue // Clear stale rows after restoring the new cursor position.
		}
		if !w.invalidTail && i < len(old) && i < len(f.Tail) && old[i] == f.Tail[i] {
			continue
		}
		if i >= oldLen && i > 0 {
			// CSI down clamps at the screen edge. CRLF allocates a new row
			// and lets the terminal scroll naturally instead.
			move(i-1, 0)
			b.WriteString("\r\n")
			row, col = i, 0
		} else {
			move(i, 0)
		}
		if i < len(f.Tail) {
			b.WriteString(f.Tail[i])
			col = frameTailWidth(f, i)
		}
		if i < oldLen || w.invalidTail {
			b.WriteString("\x1b[K")
		}
	}
	if len(f.Tail) > 0 {
		targetCursorRow := f.CursorRow
		if invalidated && f.Height > 0 && targetCursorRow >= f.Height {
			targetCursorRow = f.Height - 1
		}
		// Always use the target column, not a signed column delta.
		if w.invalidTail || !equalRows(old, f.Tail) {
			// Keep the initial prompt positioning explicit as well.
			if row != targetCursorRow {
				move(targetCursorRow, 0)
			}
			b.WriteByte('\r')
			if f.CursorCol > 0 {
				fmt.Fprintf(&b, "\x1b[%dC", f.CursorCol)
			}
			row, col = targetCursorRow, f.CursorCol
		} else {
			move(targetCursorRow, f.CursorCol)
		}
	} else if f.Append == "" && len(old) > 0 {
		move(0, 0)
	}
	if invalidated && staleRows > len(f.Tail) {
		b.WriteString("\x1b[s")
		for i := len(f.Tail); i < min(staleRows, maxRows); i++ {
			move(i, 0)
			b.WriteString("\x1b[K")
		}
		b.WriteString("\x1b[u")
	}
	if f.HideCursor {
		b.WriteString("\x1b[?25l")
	}
	if f.ShowCursor {
		b.WriteString("\x1b[?25h")
	}
	if b.Len() == 0 {
		w.unchanged++
		return nil
	}
	if err := WriteBounded(w.Out, b.Bytes()); err != nil {
		return err
	}
	w.invalidTail = false
	w.writes++
	w.bytes += b.Len()
	w.prevTail = append([]string(nil), f.Tail...)
	w.prevTailCellWidth = append([]int(nil), f.TailCellWidth...)
	w.prevCursorRow, w.prevCursorCol = f.CursorRow, f.CursorCol
	return nil
}

func (w *Writer) emitFullDocument(f Frame) error {
	width := max(1, f.Width)
	w.invalidTail = true
	write := func(s string) error {
		if s == "" {
			return nil
		}
		if err := WriteBounded(w.Out, []byte(s)); err != nil {
			return err
		}
		w.bytes += len(s)
		return nil
	}
	endSync := func() {
		const end = "\x1b[?2026l"
		if err := WriteBounded(w.Out, []byte(end)); err == nil {
			w.bytes += len(end)
		}
	}
	fail := func(err error) error {
		endSync()
		return err
	}
	if err := write("\x1b[?2026h"); err != nil {
		return fail(err)
	}
	// Match Pi's main-screen rebuild: clear the viewport, home the cursor, then
	// erase native scrollback before writing the retained document.
	if err := write("\x1b[2J\x1b[H\x1b[3J"); err != nil {
		return fail(err)
	}
	for _, line := range f.DocumentRows {
		if err := write(line + "\r\n"); err != nil {
			return fail(err)
		}
	}
	tailRows := len(f.Tail)
	if tailRows > 0 {
		for i, line := range f.Tail {
			if err := write(line); err != nil {
				return fail(err)
			}
			if i+1 < tailRows {
				if err := write("\r\n"); err != nil {
					return fail(err)
				}
			}
		}
		cursorRow := min(tailRows-1, max(0, f.CursorRow))
		if delta := tailRows - 1 - cursorRow; delta > 0 {
			if err := write(fmt.Sprintf("\x1b[%dA", delta)); err != nil {
				return fail(err)
			}
		}
		if err := write("\r"); err != nil {
			return fail(err)
		}
		if col := min(width-1, max(0, f.CursorCol)); col > 0 {
			if err := write(fmt.Sprintf("\x1b[%dC", col)); err != nil {
				return fail(err)
			}
		}
	}
	if f.HideCursor {
		if err := write("\x1b[?25l"); err != nil {
			return fail(err)
		}
	}
	if f.ShowCursor {
		if err := write("\x1b[?25h"); err != nil {
			return fail(err)
		}
	}
	if err := write("\x1b[?2026l"); err != nil {
		return fail(err)
	}
	w.invalidTail = false
	w.resizeCursorKnown = false
	w.writes++
	w.prevTail = append([]string(nil), f.Tail...)
	w.prevTailCellWidth = append([]int(nil), f.TailCellWidth...)
	w.prevCursorRow, w.prevCursorCol = f.CursorRow, f.CursorCol
	return nil
}

func (w *Writer) emitResizedFrame(f Frame) error {
	width, height := max(1, f.Width), max(1, f.Height)
	oldVisualRows := visualRowsAfterResize(w.prevTail, w.prevTailCellWidth, width)
	oldCursorOffset := visualCursorOffset(w.prevTail, w.prevTailCellWidth, w.prevCursorRow, w.prevCursorCol, width)
	oldAnchor := max(0, w.resizeCursorRow-oldCursorOffset)
	anchor := max(oldAnchor, w.resizeCursorRow-f.CursorRow)
	anchor = min(height-1, anchor)

	var b bytes.Buffer
	cup := func(row, col int) {
		fmt.Fprintf(&b, "\x1b[%d;%dH", row+1, col+1)
	}

	for logicalRow := 0; logicalRow < oldVisualRows; logicalRow++ {
		screenRow := oldAnchor + logicalRow
		if screenRow >= 0 && screenRow < height {
			cup(screenRow, 0)
			b.WriteString("\x1b[K")
		}
	}

	if f.Append != "" {
		appendRow := max(0, oldAnchor)
		if appendRow >= height {
			appendRow = height - 1
		}
		cup(appendRow, 0)
		b.WriteString(f.Append)
		anchor = min(height-1, appendRow+appendVisualRows(f.Append, width))
		if !strings.HasSuffix(f.Append, "\n") {
			anchor = max(appendRow, anchor-1)
		}
	}

	rows := max(oldVisualRows, len(f.Tail))
	for logicalRow := max(0, -anchor); logicalRow < rows; logicalRow++ {
		screenRow := anchor + logicalRow
		if screenRow < 0 || screenRow >= height {
			continue
		}
		cup(screenRow, 0)
		if logicalRow < len(f.Tail) {
			b.WriteString(f.Tail[logicalRow])
		}
		b.WriteString("\x1b[K")
	}

	cursorRow := max(0, min(height-1, anchor+f.CursorRow))
	cursorCol := max(0, min(width-1, f.CursorCol))
	cup(cursorRow, cursorCol)
	if f.HideCursor {
		b.WriteString("\x1b[?25l")
	}
	if f.ShowCursor {
		b.WriteString("\x1b[?25h")
	}
	if err := WriteBounded(w.Out, b.Bytes()); err != nil {
		return err
	}
	w.invalidTail = false
	w.resizeCursorKnown = false
	w.writes++
	w.bytes += b.Len()
	w.prevTail = append([]string(nil), f.Tail...)
	w.prevTailCellWidth = append([]int(nil), f.TailCellWidth...)
	w.prevCursorRow, w.prevCursorCol = f.CursorRow, f.CursorCol
	return nil
}

func visualRowsAfterResize(rows []string, widths []int, width int) int {
	total := 0
	for i, row := range rows {
		cells := CellWidth(row)
		if i < len(widths) {
			cells = widths[i]
		}
		total += max(1, (cells+width-1)/width)
	}
	return total
}

func visualCursorOffset(rows []string, widths []int, cursorRow, cursorCol, width int) int {
	offset := 0
	for i := 0; i < min(cursorRow, len(rows)); i++ {
		cells := CellWidth(rows[i])
		if i < len(widths) && widths[i] > 0 {
			cells = widths[i]
		}
		offset += max(1, (cells+width-1)/width)
	}
	return offset + max(0, cursorCol)/width
}

func appendVisualRows(text string, width int) int {
	normalized := strings.ReplaceAll(text, "\r\n", "\n")
	parts := strings.Split(normalized, "\n")
	rows := strings.Count(normalized, "\n")
	for _, part := range parts {
		cells := CellWidth(part)
		if cells > 0 {
			rows += (cells - 1) / width
		}
	}
	return rows
}

func frameTailWidth(f Frame, row int) int {
	if row < len(f.TailCellWidth) {
		return f.TailCellWidth[row]
	}
	return CellWidth(f.Tail[row])
}

func equalRows(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
