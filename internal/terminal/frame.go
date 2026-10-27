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
	Append        string
	Tail          []string
	CursorRow     int
	CursorCol     int
	CursorVisible bool
	HideCursor    bool
	ShowCursor    bool
}

// Writer is owned by the application event loop. No other writer may move its cursor.
type Writer struct {
	Out                          io.Writer
	prevTail                     []string
	prevCursorRow, prevCursorCol int
	writes, frames, bytes        int
	unchanged                    int
}

func NewWriter(out io.Writer) *Writer { return &Writer{Out: out} }

func (w *Writer) Stats() (writes, frames, bytes, unchanged int) {
	return w.writes, w.frames, w.bytes, w.unchanged
}

func (w *Writer) Emit(f Frame) error {
	w.frames++
	if f.Append == "" && equalRows(w.prevTail, f.Tail) && f.CursorRow == w.prevCursorRow && f.CursorCol == w.prevCursorCol && !f.HideCursor && !f.ShowCursor {
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
		} else {
			b.WriteString(f.Append)
		}
		old = nil
		row, col = 0, 0
	}
	for i := 0; i < max(len(old), len(f.Tail)); i++ {
		if i < len(old) && i < len(f.Tail) && old[i] == f.Tail[i] {
			continue
		}
		if i >= len(old) && i > 0 {
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
			col = CellWidth(f.Tail[i])
		}
		if i < len(old) {
			b.WriteString("\x1b[K")
		}
	}
	if len(f.Tail) > 0 {
		// Always use the target column, not a signed column delta.
		if !equalRows(old, f.Tail) {
			// Keep the initial prompt positioning explicit as well.
			if row != f.CursorRow {
				move(f.CursorRow, 0)
			}
			b.WriteByte('\r')
			if f.CursorCol > 0 {
				fmt.Fprintf(&b, "\x1b[%dC", f.CursorCol)
			}
			row, col = f.CursorRow, f.CursorCol
		} else {
			move(f.CursorRow, f.CursorCol)
		}
	} else if f.Append == "" && len(old) > 0 {
		move(0, 0)
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
	w.writes++
	w.bytes += b.Len()
	w.prevTail = append([]string(nil), f.Tail...)
	w.prevCursorRow, w.prevCursorCol = f.CursorRow, f.CursorCol
	return nil
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
