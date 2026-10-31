package terminal

import (
	"bytes"
	"fmt"
	"io"
	"strings"
)

// Frame appends committed text and replaces only the bounded, owned tail.
// Content is sanitized before composition. Cursor coordinates are tail-relative.
type Frame struct {
	Append string
	Tail   []string
	// TailCellWidth carries plain-text cell widths when Tail rows contain
	// trusted presentation SGR. Content width must never count ANSI bytes.
	TailCellWidth []int
	CursorRow     int
	CursorCol     int
	CursorVisible bool
	HideCursor    bool
	ShowCursor    bool
	Height        int
}

// Writer is owned by the application event loop. No other writer may move its cursor.
type Writer struct {
	Out                          io.Writer
	prevTail                     []string
	prevCursorRow, prevCursorCol int
	writes, frames, bytes        int
	unchanged                    int
	invalidTail                  bool
}

func NewWriter(out io.Writer) *Writer { return &Writer{Out: out} }

// InvalidateTail discards differential row assumptions after native resize or
// reflow. Unlike Pi's whole-document resize path, only psi's owned tail is
// redrawn; committed conversation is never cleared or replayed.
func (w *Writer) InvalidateTail() { w.invalidTail = true }

func (w *Writer) Stats() (writes, frames, bytes, unchanged int) {
	return w.writes, w.frames, w.bytes, w.unchanged
}

func (w *Writer) Emit(f Frame) error {
	w.frames++
	if !w.invalidTail && f.Append == "" && equalRows(w.prevTail, f.Tail) && f.CursorRow == w.prevCursorRow && f.CursorCol == w.prevCursorCol && !f.HideCursor && !f.ShowCursor {
		w.unchanged++
		return nil
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
	w.prevCursorRow, w.prevCursorCol = f.CursorRow, f.CursorCol
	return nil
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
