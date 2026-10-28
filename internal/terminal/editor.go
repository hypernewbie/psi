package terminal

import (
	"strings"
	"unicode"

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
	buf            []rune
	pos            int // rune index (always on grapheme boundary)
	history        []string
	histIdx        int // -1 = current draft
	draft          string
	draftPos       int
	prefCol        int // sticky visual column, -1 when inactive
	rejectedInsert bool
	// rejectedRecall signals a skipped oversized recall entry (F11).
	rejectedRecall bool
}

func NewEditor() *Editor { return &Editor{histIdx: -1, prefCol: -1} }

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

// RestoreDraft restores a submitted draft after application preflight rejects
// the operation. It does not add the text to recall history.
func (e *Editor) RestoreDraft(text string, pos int) bool {
	text = normalizeRecall(text)
	if len(text) > editorBudget {
		return false
	}
	e.buf = []rune(text)
	pos = max(0, min(pos, len(e.buf)))
	offset := runeToByte(e.buf, pos)
	e.pos = byteToRune(e.buf, snapByte(text, offset))
	e.histIdx = -1
	e.draft, e.draftPos = "", 0
	e.prefCol = -1
	return true
}
func (e *Editor) Clear() {
	e.buf = nil
	e.pos = 0
	e.histIdx = -1
	e.draft, e.draftPos, e.prefCol = "", 0, -1
}

// SetHistory replaces recall history (session-scoped, up to 200, oldest first).
// Every entry passes the same whole-draft admission as insertion (F11):
// oversized entries are skipped, never silently cut, with a bounded notice.
func (e *Editor) SetHistory(h []string) {
	if len(h) > maxHistory {
		h = h[len(h)-maxHistory:]
	}
	kept := make([]string, 0, len(h))
	for _, s := range h {
		n := normalizeRecall(s)
		if len(n) > editorBudget {
			e.rejectedRecall = true
			continue
		}
		kept = append(kept, n)
	}
	e.history = kept
	e.histIdx = -1
	e.draft, e.draftPos = "", 0
	e.prefCol = -1
}

// normalizeRecall applies insertion normalization to recalled drafts so the
// budget check matches what Insert would enforce.
func normalizeRecall(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	return strings.ReplaceAll(s, "\r", "\n")
}

// History returns a copy of recall history.
func (e *Editor) History() []string { return append([]string(nil), e.history...) }

// Insert inserts s at the cursor without splitting graphemes to fit the budget.
// It normalizes CRLF/lone CR to LF first. Returns false if the whole insert was rejected.
func (e *Editor) Insert(s string) bool {
	e.rejectedInsert = false
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	if s == "" {
		return true
	}
	cur := string(e.buf)
	if len(cur)+len(s) > editorBudget {
		e.rejectedInsert = true
		return false
	}
	boff := runeToByte(e.buf, e.pos)
	// Ensure cursor is on a boundary (defensive).
	boff = snapByte(cur, boff)
	newText := cur[:boff] + s + cur[boff:]
	e.buf = []rune(newText)
	e.pos = byteToRune(e.buf, snapByte(newText, boff+len(s)))
	e.prefCol = -1
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
	e.prefCol = -1
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
	e.prefCol = -1
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
	e.pos, e.prefCol = s, -1
}
func (e *Editor) End() {
	_, en := e.logicalLineBounds()
	e.pos, e.prefCol = en, -1
}
func (e *Editor) DocStart() { e.pos, e.prefCol = 0, -1 }
func (e *Editor) DocEnd()   { e.pos, e.prefCol = len(e.buf), -1 }

// MoveUp moves through visual draft rows; returns false when already on first row.
// It preserves the preferred visual column (pi sticky column).
func (e *Editor) MoveUp(width int) bool {
	layout := LayoutDraft(e.buf, e.pos, width)
	if layout.CursorRow <= 0 {
		return false
	}
	target := layout.CursorRow - 1
	col := layout.CursorCol
	if e.prefCol >= 0 {
		col = e.prefCol
	} else {
		e.prefCol = col
	}
	e.pos = layout.VisualRunePos(e.buf, target, col)
	return true
}

// MoveDown moves through visual draft rows; returns false when already on last row.
func (e *Editor) MoveDown(width int) bool {
	layout := LayoutDraft(e.buf, e.pos, width)
	if layout.CursorRow >= len(layout.Rows)-1 {
		return false
	}
	target := layout.CursorRow + 1
	col := layout.CursorCol
	if e.prefCol >= 0 {
		col = e.prefCol
	} else {
		e.prefCol = col
	}
	e.pos = layout.VisualRunePos(e.buf, target, col)
	return true
}

// Complete returns slash-command and skill-name candidates for Tab (P2-022).
// No tool/shell execution, no filesystem scan, no provider request.
func Complete(draft string, pos int, commands []string, skills []string) (candidates []string, showList bool) {
	// Find current token start (after whitespace or start).
	start := pos
	runes := []rune(draft)
	if start > len(runes) {
		start = len(runes)
	}
	for start > 0 && runes[start-1] != ' ' && runes[start-1] != '\n' && runes[start-1] != '\t' {
		start--
	}
	token := string(runes[start:pos])
	if !startsWith(token, "/") {
		return nil, false
	}
	// Strip leading / and optional skill: prefix handling.
	var pool []string
	for _, c := range commands {
		pool = append(pool, "/"+c)
	}
	for _, sk := range skills {
		pool = append(pool, "/skill:"+sk)
	}
	for _, p := range pool {
		if startsWith(p, token) && p != token {
			candidates = append(candidates, p)
		}
	}
	return candidates, len(candidates) > 1
}

func startsWith(s, prefix string) bool {
	if len(s) < len(prefix) {
		return false
	}
	return s[:len(prefix)] == prefix
}

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
// It segments the prefix once and walks grapheme boundaries: the previous
// repeated full-prefix scans were quadratic in word length (F09).
func wordBackward(s string, off int) int {
	if off <= 0 {
		return 0
	}
	prefix := s[:off]
	// ASCII graphemes need no offset table. CRLF is a whitespace pair,
	// so skipping both bytes gives the same boundary as segmentation.
	ascii := true
	for i := 0; i < len(prefix); i++ {
		if prefix[i] >= 0x80 {
			ascii = false
			break
		}
	}
	if ascii {
		return wordBackwardASCII(prefix)
	}
	// Keep offsets only, not a second array of grapheme strings.
	gr := uniseg.NewGraphemes(prefix)
	starts := []int{0}
	for gr.Next() {
		_, end := gr.Positions()
		starts = append(starts, end)
	}
	// Walk back: skip trailing whitespace, then one word/punct run.
	i := len(starts) - 1
	for i > 0 {
		g := prefix[starts[i-1]:starts[i]]
		if strings.TrimSpace(g) != "" {
			break
		}
		i--
	}
	if i <= 0 {
		return 0
	}
	last := prefix[starts[i-1]:starts[i]]
	lastEnd := starts[i]
	isWord := _isWordLike(last)
	for i > 0 {
		g := prefix[starts[i-1]:starts[i]]
		if strings.TrimSpace(g) == "" {
			break
		}
		if _isWordLike(g) != isWord {
			break
		}
		if isWord && isPunct(g) && starts[i] != lastEnd-len(last) {
			if !isPunct(last) {
				break
			}
		}
		i--
		if !isWord {
			continue
		}
		if isPunct(g) {
			break
		}
	}
	return starts[i]
}

func wordBackwardASCII(s string) int {
	space := func(c byte) bool { return c == ' ' || c >= '\t' && c <= '\r' }
	word := func(c byte) bool {
		return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_'
	}
	i := len(s)
	for i > 0 && space(s[i-1]) {
		i--
	}
	if i == 0 {
		return 0
	}
	wordLike := word(s[i-1])
	for i > 0 && !space(s[i-1]) && word(s[i-1]) == wordLike {
		i--
	}
	return i
}

func _isWordLike(s string) bool {
	if s == "" || strings.TrimSpace(s) == "" {
		return false
	}
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsNumber(r) || r == '_' {
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
	// Skip oversized entries without installing them (F11).
	for e.histIdx >= 0 && len(e.history[e.histIdx]) > editorBudget {
		e.rejectedRecall = true
		if e.histIdx == 0 {
			e.histIdx = -1
			e.buf = []rune(e.draft)
			e.pos = e.draftPos
			return
		}
		e.histIdx--
	}
	if e.histIdx == -1 {
		return
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
		// Skip oversized entries without installing them (F11).
		for e.histIdx < len(e.history) && len(e.history[e.histIdx]) > editorBudget {
			e.rejectedRecall = true
			e.histIdx++
		}
		if e.histIdx >= len(e.history) {
			e.histIdx = -1
			e.buf = []rune(e.draft)
			e.pos = e.draftPos
			if e.pos > len(e.buf) {
				e.pos = len(e.buf)
			}
			return
		}
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
		case 127, 8:
			e.Backspace()
			return "", false
		case 16:
			e.PrevHistory()
			return "", false
		case 14:
			e.NextHistory()
			return "", false
		case 3, 4:
			// Clear/exit are handled by Console.
			return "", false
		default:
			if key[0] == 27 || key[0] < 0x20 {
				return "", false
			}
			e.Insert(string(key))
			return "", false
		}
	}
	// Ctrl+P / Ctrl+N browse history directly (3.4).
	if len(key) == 1 && (key[0] == 16 || key[0] == 14) {
		if key[0] == 16 {
			e.PrevHistory()
		} else {
			e.NextHistory()
		}
		return "", false
	}
	// Tab completion is handled by outer Console (needs skill/command lists).
	// Editor exposes Complete(); HandleKey leaves Tab for the caller.
	if len(key) == 1 && key[0] == 9 {
		return "", false
	}
	s := string(key)
	switch s {
	case "\x1b[D":
		e.MoveLeft()
	case "\x1b[C":
		e.MoveRight()
	case "\x1b[A":
		if !e.MoveUp(80) {
			e.PrevHistory()
		}
	case "\x1b[B":
		if !e.MoveDown(80) {
			e.NextHistory()
		}
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
