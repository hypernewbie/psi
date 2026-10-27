package terminal

import (
	"strings"
	"unicode/utf8"
)

// Editor is a narrow line-editor adapter (Step 80).
// In-memory history first; no persistent plaintext history file by default.
// Multiline via Ctrl+J; Enter submits (outside bracketed paste, pasted
// newlines are literal, not submissions).

type Editor struct {
	buf     []rune
	pos     int
	history []string
	histIdx int // -1 = current draft
	draft   string
	paste   bool // inside bracketed paste
}

func NewEditor() *Editor { return &Editor{histIdx: -1} }

// Insert text (paste-safe: newlines literal).
func (e *Editor) Insert(s string) {
	runes := []rune(s)
	buf := make([]rune, 0, len(e.buf)+len(runes))
	buf = append(buf, e.buf[:e.pos]...)
	buf = append(buf, runes...)
	buf = append(buf, e.buf[e.pos:]...)
	e.buf = buf
	e.pos += len(runes)
	e.histIdx = -1
}

func (e *Editor) Backspace() {
	if e.pos > 0 {
		e.buf = append(e.buf[:e.pos-1], e.buf[e.pos:]...)
		e.pos--
	}
}

func (e *Editor) Delete() {
	if e.pos < len(e.buf) {
		e.buf = append(e.buf[:e.pos], e.buf[e.pos+1:]...)
	}
}

func (e *Editor) MoveLeft() {
	if e.pos > 0 {
		e.pos--
	}
}
func (e *Editor) MoveRight() {
	if e.pos < len(e.buf) {
		e.pos++
	}
}
func (e *Editor) Home() { e.pos = 0 }
func (e *Editor) End()  { e.pos = len(e.buf) }

func (e *Editor) PrevHistory() {
	if len(e.history) == 0 {
		return
	}
	if e.histIdx == -1 {
		e.draft = string(e.buf)
		e.histIdx = len(e.history) - 1
	} else if e.histIdx > 0 {
		e.histIdx--
	}
	e.buf = []rune(e.history[e.histIdx])
	e.pos = len(e.buf)
}

func (e *Editor) NextHistory() {
	if e.histIdx == -1 {
		return
	}
	if e.histIdx == len(e.history)-1 {
		e.histIdx = -1
		e.buf = []rune(e.draft)
	} else {
		e.histIdx++
		e.buf = []rune(e.history[e.histIdx])
	}
	e.pos = len(e.buf)
}

// Text returns the current buffer.
func (e *Editor) Text() string { return string(e.buf) }

// Clear empties the buffer (idle Ctrl+C first press).
func (e *Editor) Clear() { e.buf = nil; e.pos = 0; e.histIdx = -1 }

// Submit returns the buffer and pushes history (Enter outside paste).
func (e *Editor) Submit() string {
	s := string(e.buf)
	if strings.TrimSpace(s) != "" {
		e.history = append(e.history, s)
		if len(e.history) > 200 {
			e.history = e.history[len(e.history)-200:]
		}
	}
	e.buf = nil
	e.pos = 0
	e.histIdx = -1
	e.draft = ""
	return s
}

// HandleKey processes one input unit; returns submitted line + submitted flag.
// pasteActive indicates bracketed-paste mode from the outer reader.
func (e *Editor) HandleKey(key []byte, pasteActive bool) (string, bool) {
	if pasteActive {
		// Pasted newlines are literal, never submissions.
		e.Insert(string(key))
		return "", false
	}
	if len(key) == 1 {
		switch key[0] {
		case '\r': // Enter submits
			return e.Submit(), true
		case '\n': // Ctrl+J inserts newline
			e.Insert("\n")
			return "", false
		case 3: // Ctrl+C handled by outer loop (clear/exit), not inserted
			return "", false
		case 4: // Ctrl+D: EOF when empty
			if len(e.buf) == 0 {
				return "", true // outer maps empty submit + EOF to exit
			}
			e.Delete()
			return "", false
		case 127, 8: // Backspace
			e.Backspace()
			return "", false
		default:
			if key[0] == 27 || key[0] < 0x20 {
				return "", false
			}
			e.Insert(string(key))
			return "", false
		}
	}
	s := string(key)
	switch s {
	case "\x1b[D":
		e.MoveLeft()
	case "\x1b[C":
		e.MoveRight()
	case "\x1b[A":
		e.PrevHistory()
	case "\x1b[B":
		e.NextHistory()
	case "\x1b[H", "\x1b[1~":
		e.Home()
	case "\x1b[F", "\x1b[4~":
		e.End()
	case "\x1b[3~":
		e.Delete()
	default:
		if utf8.Valid(key) && !strings.HasPrefix(s, "\x1b") {
			e.Insert(s)
		}
	}
	return "", false
}
