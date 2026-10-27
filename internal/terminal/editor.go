package terminal

import (
	"strings"

	"github.com/rivo/uniseg"
)

// Editor is the inline multiline prompt editor (PLAN2 3.4, pi editor.ts subset).
// Grapheme-based movement/deletion via uniseg; one cell-width policy via width.go.
// History is session-scoped (200, set via SetHistory); no plaintext history file.
// Budget: 256 KiB UTF-8 after newline normalization; insertion never splits a grapheme.

const (
	editorBudget = 256 * 1024
	maxHistory   = 200
)

type Editor struct {
	buf      []rune
	pos      int // rune index (always on grapheme boundary)
	history  []string
	histIdx  int // -1 = current draft
	draft    string
	draftPos int
}

func NewEditor() *Editor { return &Editor{histIdx: -1} }

// graphemeBounds returns byte offsets of grapheme boundaries for s.
func graphemeBounds(s string) []int {
	var bounds []int
	gr := uniseg.NewGraphemes(s)
	for gr.Next() {
		start, _ := gr.Positions()
		bounds = append(bounds, start)
	}
	bounds = append(bounds, len(s))
	return bounds
}

// runeToByte converts rune index to byte offset.
func runeToByte(buf []rune, pos int) int {
	n := 0
	for i := 0; i < pos && i < len(buf); i++ {
		n += len(string(buf[i]))
	}
	return n
}

// byteToRune converts byte offset to rune index.
func byteToRune(buf []rune, boff int) int {
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

// snapToGrapheme moves a byte offset to the nearest valid grapheme boundary (forward).
func snapByte(s string, off int) int {
	bounds := graphemeBounds(s)
	for _, b := range bounds {
		if b >= off {
			return b
		}
	}
	return len(s)
}

func (e *Editor) Text() string { return string(e.buf) }
func (e *Editor) Pos() int     { return e.pos }
func (e *Editor) Clear() {
	e.buf = nil
	e.pos = 0
	e.histIdx = -1
}

// SetHistory replaces recall history (session-scoped, up to 200, oldest first).
func (e *Editor) SetHistory(h []string) {
	if len(h) > maxHistory {
		h = h[len(h)-maxHistory:]
	}
	e.history = append([]string(nil), h...)
	e.histIdx = -1
	e.draft, e.draftPos = "", 0
}

// History returns a copy of recall history.
func (e *Editor) History() []string { return append([]string(nil), e.history...) }

// Insert inserts s at the cursor without splitting graphemes to fit the budget.
// It normalizes CRLF/lone CR to LF first. Returns false if the whole insert was rejected.
func (e *Editor) Insert(s string) bool {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	if s == "" {
		return true
	}
	cur := string(e.buf)
	if len(cur)+len(s) > editorBudget {
		// Try to fit a grapheme-prefix of s; never split a cluster.
		allowed := editorBudget - len(cur)
		if allowed <= 0 {
			return false
		}
		var b strings.Builder
		gr := uniseg.NewGraphemes(s)
		for gr.Next() {
			seg := string(gr.Str())
			if b.Len()+len(seg) > allowed {
				break
			}
			b.WriteString(seg)
		}
		s = b.String()
		if s == "" {
			return false
		}
	}
	boff := runeToByte(e.buf, e.pos)
	// Ensure cursor is on a boundary (defensive).
	boff = snapByte(cur, boff)
	newText := cur[:boff] + s + cur[boff:]
	e.buf = []rune(newText)
	e.pos = byteToRune(e.buf, boff+len(s))
	if e.histIdx != -1 {
		// Editing while browsing keeps browsing state until submit/navigation;
		// pi exits history browsing on edit. Match pi: exit browsing.
		e.histIdx = -1
	}
	return true
}

func (e *Editor) Backspace() {
	if e.pos <= 0 {
		return
	}
	s := string(e.buf)
	boff := runeToByte(e.buf, e.pos)
	bounds := graphemeBounds(s)
	// Find previous boundary.
	prev := 0
	for _, b := range bounds {
		if b < boff {
			prev = b
		} else {
			break
		}
	}
	newText := s[:prev] + s[boff:]
	e.buf = []rune(newText)
	e.pos = byteToRune(e.buf, prev)
	e.histIdx = -1
}

func (e *Editor) Delete() {
	s := string(e.buf)
	boff := runeToByte(e.buf, e.pos)
	if boff >= len(s) {
		return
	}
	bounds := graphemeBounds(s)
	next := len(s)
	for _, b := range bounds {
		if b > boff {
			next = b
			break
		}
	}
	newText := s[:boff] + s[next:]
	e.buf = []rune(newText)
	// pos unchanged (byte offset same => rune index recomputed same logical spot)
	e.pos = byteToRune(e.buf, boff)
	e.histIdx = -1
}

func (e *Editor) MoveLeft() {
	s := string(e.buf)
	boff := runeToByte(e.buf, e.pos)
	if boff <= 0 {
		return
	}
	bounds := graphemeBounds(s)
	prev := 0
	for _, b := range bounds {
		if b < boff {
			prev = b
		} else {
			break
		}
	}
	e.pos = byteToRune(e.buf, prev)
}

func (e *Editor) MoveRight() {
	s := string(e.buf)
	boff := runeToByte(e.buf, e.pos)
	if boff >= len(s) {
		return
	}
	bounds := graphemeBounds(s)
	for _, b := range bounds {
		if b > boff {
			e.pos = byteToRune(e.buf, b)
			return
		}
	}
	e.pos = len(e.buf)
}

// logicalLineBounds returns rune indices of the logical line containing pos.
func (e *Editor) logicalLineBounds() (start, end int) {
	start = e.pos
	for start > 0 && e.buf[start-1] != '\n' {
		start--
	}
	end = e.pos
	for end < len(e.buf) && e.buf[end] != '\n' {
		end++
	}
	return start, end
}

func (e *Editor) Home() {
	s, _ := e.logicalLineBounds()
	e.pos = s
}
func (e *Editor) End() {
	_, en := e.logicalLineBounds()
	e.pos = en
}
func (e *Editor) DocStart() { e.pos = 0 }
func (e *Editor) DocEnd()   { e.pos = len(e.buf) }

// DeleteWordBackward implements Ctrl+W using pi word-navigation semantics:
// skip trailing whitespace, then one word-like or punctuation run.
func (e *Editor) DeleteWordBackward() {
	if e.pos <= 0 {
		return
	}
	s := string(e.buf)
	boff := runeToByte(e.buf, e.pos)
	newOff := wordBackward(s, boff)
	newText := s[:newOff] + s[boff:]
	e.buf = []rune(newText)
	e.pos = byteToRune(e.buf, newOff)
	e.histIdx = -1
}

func (e *Editor) DeleteToLineStart() {
	s, _ := e.logicalLineBounds()
	boff := runeToByte(e.buf, e.pos)
	startBoff := runeToByte(e.buf, s)
	t := string(e.buf)
	e.buf = []rune(t[:startBoff] + t[boff:])
	e.pos = s
	e.histIdx = -1
}

func (e *Editor) DeleteToLineEnd() {
	_, en := e.logicalLineBounds()
	boff := runeToByte(e.buf, e.pos)
	endBoff := runeToByte(e.buf, en)
	t := string(e.buf)
	e.buf = []rune(t[:boff] + t[endBoff:])
	e.histIdx = -1
}

// wordBackward ports pi findWordBackward (simplified, uniseg words).
func wordBackward(s string, off int) int {
	if off <= 0 {
		return 0
	}
	prefix := s[:off]
	// Walk back: skip trailing whitespace, then one word/punct run.
	i := len(prefix)
	// skip whitespace
	for i > 0 {
		// last grapheme
		g := lastGrapheme(prefix[:i])
		if strings.TrimSpace(g) != "" {
			break
		}
		i -= len(g)
	}
	if i <= 0 {
		return 0
	}
	// Determine class of last unit.
	last := lastGrapheme(prefix[:i])
	isWord := _isWordLike(last)
	for i > 0 {
		g := lastGrapheme(prefix[:i])
		if strings.TrimSpace(g) == "" {
			break
		}
		if _isWordLike(g) != isWord {
			break
		}
		// ASCII punctuation boundary inside word-like (pi PUNCTUATION handling): stop at punctuation.
		if isWord && isPunct(g) && i != len(prefix)-len(last) {
			// keep punctuation groups together: continue while punct
			if !isPunct(last) {
				break
			}
		}
		i -= len(g)
		if !isWord {
			// consume whole punct run
			continue
		}
		// consume whole word run: continue while word-like non-punct
		if isPunct(g) {
			break
		}
	}
	return i
}

func lastGrapheme(s string) string {
	var last string
	gr := uniseg.NewGraphemes(s)
	for gr.Next() {
		last = string(gr.Str())
	}
	return last
}

func _isWordLike(s string) bool {
	if s == "" || strings.TrimSpace(s) == "" {
		return false
	}
	for _, r := range s {
		if (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '_' || r > 0x7F {
			return true
		}
	}
	return false
}

func isPunct(s string) bool {
	if len([]rune(s)) != 1 {
		return false
	}
	r := []rune(s)[0]
	return strings.ContainsRune(".,;:!?()[]{}<>\"'`~@#$%^&*-+=|\\/…—：；！？（）［］｛｝“”‘’", r)
}

func (e *Editor) PrevHistory() {
	if len(e.history) == 0 {
		return
	}
	if e.histIdx == -1 {
		e.draft = string(e.buf)
		e.draftPos = e.pos
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
		e.pos = e.draftPos
		if e.pos > len(e.buf) {
			e.pos = len(e.buf)
		}
	} else {
		e.histIdx++
		e.buf = []rune(e.history[e.histIdx])
		e.pos = len(e.buf)
	}
}

func (e *Editor) Submit() string {
	s := string(e.buf)
	if strings.TrimSpace(s) != "" {
		// Avoid consecutive duplicates (pi editor behavior).
		if len(e.history) == 0 || e.history[len(e.history)-1] != s {
			e.history = append(e.history, s)
			if len(e.history) > maxHistory {
				e.history = e.history[len(e.history)-maxHistory:]
			}
		}
	}
	e.buf = nil
	e.pos = 0
	e.histIdx = -1
	e.draft, e.draftPos = "", 0
	return s
}

// HandleKey processes one input unit; returns submitted line + submitted flag.
// pasteActive indicates bracketed-paste mode from the outer reader.
func (e *Editor) HandleKey(key []byte, pasteActive bool) (string, bool) {
	if pasteActive {
		e.Insert(string(key))
		return "", false
	}
	if len(key) == 1 {
		switch key[0] {
		case '\r':
			return e.Submit(), true
		case '\n':
			e.Insert("\n")
			return "", false
		case 1: // Ctrl+A
			e.Home()
			return "", false
		case 5: // Ctrl+E
			e.End()
			return "", false
		case 11: // Ctrl+K
			e.DeleteToLineEnd()
			return "", false
		case 21: // Ctrl+U
			e.DeleteToLineStart()
			return "", false
		case 23: // Ctrl+W
			e.DeleteWordBackward()
			return "", false
		case 3, 4, 127, 8:
			// Handled by outer Console (clear/exit/delete) to preserve lifecycle parity.
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
	case "\x1b[1;5H":
		e.DocStart()
	case "\x1b[1;5F":
		e.DocEnd()
	default:
		// Pass through printable multi-byte (UTF-8) insertions.
		if !strings.HasPrefix(s, "\x1b") {
			e.Insert(s)
		}
	}
	return "", false
}
