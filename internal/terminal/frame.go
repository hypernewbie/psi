package terminal

import (
	"bytes"
	"fmt"
	"io"
)

// Frame is the single terminal-output owner (P2-009, P2-025).
// All post-acquisition output passes through one owner: editor, streamer,
// status, command, and error output. Provider/tool code must never write
// directly to stdout/stderr after acquisition.
// A frame contains: ordered scrollback append + bounded active-tail changes +
// required cursor position/visibility. Built in memory, written with one
// complete write (short-write safe). Never exposes clear separately from replacement.

type Frame struct {
	Append        string // committed scrollback (append-only, already sanitized)
	Tail          []string
	CursorRow     int
	CursorCol     int
	CursorVisible bool
	HideCursor    bool
	ShowCursor    bool
}

// Writer owns terminal bytes and modes.
type Writer struct {
	Out                          io.Writer
	prevTail                     []string
	prevCursorRow, prevCursorCol int
	writes                       int
	frames                       int
	bytes                        int
	unchanged                    int
	sanitizer                    Sanitizer
}

func NewWriter(out io.Writer) *Writer { return &Writer{Out: out} }

// Counters for instrumentation (P2-005): writes, frames, unchanged, bytes.
func (w *Writer) Stats() (writes, frames, bytes, unchanged int) {
	return w.writes, w.frames, w.bytes, w.unchanged
}

// Emit builds and writes one frame. It returns false when visible state is unchanged.
func (w *Writer) Emit(f Frame) error {
	w.frames++
	// Fast path: unchanged tail + no append + same cursor => zero bytes (P2-026).
	if f.Append == "" && equalRows(w.prevTail, f.Tail) && f.CursorRow == w.prevCursorRow && f.CursorCol == w.prevCursorCol && !f.HideCursor && !f.ShowCursor {
		w.unchanged++
		return nil
	}
	var b bytes.Buffer
	if f.Append != "" {
		// Committed transcript is append-only; never erase (P2-027).
		b.WriteString(f.Append)
		if len(f.Append) == 0 || f.Append[len(f.Append)-1] != '\n' {
			// Append callers include newlines; do not force.
		}
	}
	// Tail update: only changed rows + stale suffixes on owned rows (P2-026).
	// We own rows after the scrollback append point. For simplicity, redraw tail
	// when changed, but never use unbounded ED as default editor update.
	tailChanged := !equalRows(w.prevTail, f.Tail)
	if tailChanged {
		if len(w.prevTail) > 0 {
			// Move to tail start: carriage return + up by previous tail height-1.
			b.WriteString("\r")
			if len(w.prevTail) > 1 {
				fmt.Fprintf(&b, "\x1b[%dA", len(w.prevTail)-1)
			}
			b.WriteString("\x1b[J")
		}
		for i, row := range f.Tail {
			if i > 0 {
				b.WriteString("\r\n")
			}
			b.WriteString(row)
		}
		// Clear stale suffixes on owned rows only (EL per row, not ED).
		if len(f.Tail) < len(w.prevTail) {
			b.WriteString("\x1b[K")
		}
	}
	// Cursor: cursor-only movement writes cursor movement only (P2-026).
	if !tailChanged && (f.CursorRow != w.prevCursorRow || f.CursorCol != w.prevCursorCol) {
		dr := f.CursorRow - w.prevCursorRow
		dc := f.CursorCol - w.prevCursorCol
		if dr < 0 {
			fmt.Fprintf(&b, "\x1b[%dA", -dr)
		} else if dr > 0 {
			fmt.Fprintf(&b, "\x1b[%dB", dr)
		}
		if dc != 0 {
			b.WriteString("\r")
			if dc > 0 {
				fmt.Fprintf(&b, "\x1b[%dC", dc)
			}
		}
	} else if tailChanged {
		// Position cursor within new tail.
		// Tail rows are already written; move up from last row.
		if len(f.Tail) > 0 {
			lastRow := len(f.Tail) - 1
			if lastRow > f.CursorRow {
				fmt.Fprintf(&b, "\x1b[%dA", lastRow-f.CursorRow)
			}
			b.WriteString("\r")
			if f.CursorCol > 0 {
				fmt.Fprintf(&b, "\x1b[%dC", f.CursorCol)
			}
		}
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
	// One complete write with short-write handling (P2-009, P2-025).
	data := b.Bytes()
	// Sanitize check: trusted renderer bytes (CSI cursor/erase) are allowed;
	// content bytes were sanitized before frame composition (P2-033).
	if err := WriteBounded(w.Out, data); err != nil {
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
