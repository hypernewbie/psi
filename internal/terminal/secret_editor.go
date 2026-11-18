package terminal

import (
	"bytes"
	"unicode"
	"unicode/utf8"

	"github.com/rivo/uniseg"

	"psi/internal/secretmem"
)

// secretNavigationWidth matches the wrapped-draft width used by Editor.HandleKey.
// It is a navigation compatibility value, not a rendering width.
const secretNavigationWidth = 80

const secretNavigationLimit = secretNavigationWidth - 1

// secretEditor keeps a single-line secret in application-owned bytes. The
// draft is never converted to a Go string; rendering exposes only mask runes.
type secretEditor struct {
	buf            []byte
	pos            int // byte offset
	prefCol        int // sticky visual column for Up/Down, -1 when inactive
	rejectedInsert bool
}

func newSecretEditor() *secretEditor {
	return &secretEditor{prefCol: -1}
}

// DisplayBuffer returns mask runes for rendering. It does not expose draft
// bytes or an alias to them.
func (e *secretEditor) DisplayBuffer() []rune {
	count := utf8.RuneCount(e.buf)
	mask := make([]rune, count)
	for i := range mask {
		mask[i] = '*'
	}
	return mask
}

// DisplayPos returns the cursor position in mask runes for DisplayBuffer.
func (e *secretEditor) DisplayPos() int {
	return utf8.RuneCount(e.buf[:e.pos])
}

// detach transfers the draft to the caller. The caller owns the returned bytes
// and must wipe them after its last live use.
func (e *secretEditor) detach() []byte {
	draft := e.buf
	e.buf = nil
	e.pos = 0
	e.prefCol = -1
	e.rejectedInsert = false
	return draft
}

// discard wipes and releases the draft. It is used when the secret is
// cancelled or replaced before submission.
func (e *secretEditor) discard() {
	secretmem.Wipe(e.buf[:cap(e.buf)])
	e.buf = nil
	e.pos = 0
	e.prefCol = -1
	e.rejectedInsert = false
}

func (e *secretEditor) insert(raw []byte) bool {
	e.rejectedInsert = false
	normalized := normalizeSecretBytes(raw)
	defer secretmem.Wipe(normalized[:cap(normalized)])
	if len(normalized) == 0 {
		return true
	}
	oldLen := len(e.buf)
	if len(normalized) > editorBudget-oldLen {
		e.rejectedInsert = true
		return false
	}

	pos := snapSecretBoundary(e.buf, e.pos)
	newLen := oldLen + len(normalized)
	if cap(e.buf) >= newLen {
		e.buf = e.buf[:newLen]
		copy(e.buf[pos+len(normalized):], e.buf[pos:oldLen])
		copy(e.buf[pos:], normalized)
		// Wipe stale copies and unused capacity after shifting.
		secretmem.Wipe(e.buf[newLen:cap(e.buf)])
	} else {
		newCap := cap(e.buf) * 2
		if newCap < newLen {
			newCap = newLen
		}
		if newCap > editorBudget {
			newCap = editorBudget
		}
		next := make([]byte, newLen, newCap)
		copy(next, e.buf[:pos])
		copy(next[pos:], normalized)
		copy(next[pos+len(normalized):], e.buf[pos:oldLen])
		secretmem.Wipe(e.buf[:cap(e.buf)])
		e.buf = next
	}
	e.pos = snapSecretBoundary(e.buf, pos+len(normalized))
	e.prefCol = -1
	return true
}

func normalizeSecretBytes(in []byte) []byte {
	out := make([]byte, 0, len(in))
	for i := 0; i < len(in); {
		switch {
		case in[i] == '\r':
			out = appendOwnedBytes(out, []byte{'\n'})
			if i+1 < len(in) && in[i+1] == '\n' {
				i += 2
			} else {
				i++
			}
		case in[i] < utf8.RuneSelf:
			out = appendOwnedBytes(out, in[i:i+1])
			i++
		default:
			r, size := utf8.DecodeRune(in[i:])
			if r == utf8.RuneError && size == 1 {
				// Replace each invalid byte with U+FFFD, matching Go's
				// UTF-8 replacement policy.
				out = appendOwnedBytes(out, []byte{'\xef', '\xbf', '\xbd'})
				i++
				continue
			}
			out = appendOwnedBytes(out, in[i:i+size])
			i += size
		}
	}
	return out
}

func snapSecretBoundary(buf []byte, off int) int {
	if off <= 0 {
		return 0
	}
	if off >= len(buf) {
		return len(buf)
	}
	state := -1
	for pos := 0; pos < len(buf); {
		if pos >= off {
			return pos
		}
		cluster, _, _, nextState := uniseg.FirstGraphemeCluster(buf[pos:], state)
		if len(cluster) == 0 {
			break
		}
		pos += len(cluster)
		state = nextState
	}
	return len(buf)
}

func prevSecretBoundary(buf []byte, off int) int {
	if off <= 0 {
		return 0
	}
	if off > len(buf) {
		off = len(buf)
	}
	state := -1
	prev := 0
	for pos := 0; pos < len(buf); {
		if pos >= off {
			return prev
		}
		cluster, _, _, nextState := uniseg.FirstGraphemeCluster(buf[pos:], state)
		if len(cluster) == 0 {
			break
		}
		next := pos + len(cluster)
		if next > off {
			return pos
		}
		prev = pos
		pos = next
		state = nextState
	}
	return prev
}

func nextSecretBoundary(buf []byte, off int) int {
	if off < 0 {
		return 0
	}
	if off >= len(buf) {
		return len(buf)
	}
	state := -1
	for pos := 0; pos < len(buf); {
		if pos > off {
			return pos
		}
		cluster, _, _, nextState := uniseg.FirstGraphemeCluster(buf[pos:], state)
		if len(cluster) == 0 {
			break
		}
		pos += len(cluster)
		state = nextState
	}
	return len(buf)
}

func (e *secretEditor) deleteRange(start, end int) {
	if start < 0 {
		start = 0
	}
	if end > len(e.buf) {
		end = len(e.buf)
	}
	if start >= end {
		return
	}
	oldLen := len(e.buf)
	newLen := oldLen - (end - start)
	copy(e.buf[start:], e.buf[end:oldLen])
	secretmem.Wipe(e.buf[newLen:cap(e.buf)])
	e.buf = e.buf[:newLen]
	e.prefCol = -1
}

func (e *secretEditor) backspace() {
	if e.pos <= 0 {
		return
	}
	prev := prevSecretBoundary(e.buf, e.pos)
	e.deleteRange(prev, e.pos)
	e.pos = prev
}

func (e *secretEditor) delete() {
	if e.pos >= len(e.buf) {
		return
	}
	next := nextSecretBoundary(e.buf, e.pos)
	e.deleteRange(e.pos, next)
}

func (e *secretEditor) deleteToLineStart() {
	start := e.pos
	for start > 0 && e.buf[start-1] != '\n' {
		start--
	}
	e.deleteRange(start, e.pos)
	e.pos = start
}

func (e *secretEditor) deleteToLineEnd() {
	end := e.pos
	for end < len(e.buf) && e.buf[end] != '\n' {
		end++
	}
	e.deleteRange(e.pos, end)
}

func (e *secretEditor) deleteWordBackward() {
	newPos := secretWordBackward(e.buf, e.pos)
	e.deleteRange(newPos, e.pos)
	e.pos = newPos
}

func (e *secretEditor) moveLeft() {
	e.pos = prevSecretBoundary(e.buf, e.pos)
	e.prefCol = -1
}

func (e *secretEditor) moveRight() {
	e.pos = nextSecretBoundary(e.buf, e.pos)
	e.prefCol = -1
}

func (e *secretEditor) home() {
	start := e.pos
	for start > 0 && e.buf[start-1] != '\n' {
		start--
	}
	e.pos = start
	e.prefCol = -1
}

func (e *secretEditor) end() {
	end := e.pos
	for end < len(e.buf) && e.buf[end] != '\n' {
		end++
	}
	e.pos = end
	e.prefCol = -1
}

func (e *secretEditor) docStart() {
	e.pos = 0
	e.prefCol = -1
}

func (e *secretEditor) docEnd() {
	e.pos = len(e.buf)
	e.prefCol = -1
}

func (e *secretEditor) moveUp() {
	layout := layoutSecret(e.buf, e.pos)
	if layout.cursorRow <= 0 {
		return
	}
	target := layout.rows[layout.cursorRow-1]
	col := computeVerticalMoveColumn(&e.prefCol, layout.cursorCol, layout.rows[layout.cursorRow].width, target.width)
	e.pos = visualSecretPos(e.buf, target, col)
}

func (e *secretEditor) moveDown() {
	layout := layoutSecret(e.buf, e.pos)
	if layout.cursorRow+1 >= len(layout.rows) {
		return
	}
	target := layout.rows[layout.cursorRow+1]
	col := computeVerticalMoveColumn(&e.prefCol, layout.cursorCol, layout.rows[layout.cursorRow].width, target.width)
	e.pos = visualSecretPos(e.buf, target, col)
}

func (e *secretEditor) handleKey(key []byte) {
	if len(key) == 0 {
		return
	}
	e.rejectedInsert = false
	if len(key) == 1 {
		switch key[0] {
		case '\r':
			return
		case '\n':
			e.insert([]byte{'\n'})
		case 1:
			e.home()
		case 5:
			e.end()
		case 8, 127:
			e.backspace()
		case 11:
			e.deleteToLineEnd()
		case 21:
			e.deleteToLineStart()
		case 23:
			e.deleteWordBackward()
		case 3, 4, 27:
			return
		default:
			if key[0] < 0x20 {
				return
			}
			e.insert(key)
		}
		return
	}

	switch {
	case bytes.Equal(key, []byte("\x1b[D")):
		e.moveLeft()
	case bytes.Equal(key, []byte("\x1b[C")):
		e.moveRight()
	case bytes.Equal(key, []byte("\x1b[A")):
		e.moveUp()
	case bytes.Equal(key, []byte("\x1b[B")):
		e.moveDown()
	case bytes.Equal(key, []byte("\x1b[H")), bytes.Equal(key, []byte("\x1b[1~")), bytes.Equal(key, []byte("\x1bOH")), bytes.Equal(key, []byte("\x1b[7~")):
		e.home()
	case bytes.Equal(key, []byte("\x1b[F")), bytes.Equal(key, []byte("\x1b[4~")), bytes.Equal(key, []byte("\x1bOF")), bytes.Equal(key, []byte("\x1b[8~")):
		e.end()
	case bytes.Equal(key, []byte("\x1b[1;5H")):
		e.docStart()
	case bytes.Equal(key, []byte("\x1b[1;5F")):
		e.docEnd()
	case bytes.Equal(key, []byte("\x1b[3~")):
		e.delete()
	case bytes.Equal(key, []byte("\x1bOD")):
		e.moveLeft()
	case bytes.Equal(key, []byte("\x1bOC")):
		e.moveRight()
	case bytes.Equal(key, []byte("\x1bOA")):
		e.moveUp()
	case bytes.Equal(key, []byte("\x1bOB")):
		e.moveDown()
	default:
		if key[0] != 0x1b {
			e.insert(key)
		}
	}
}

type secretLayoutRow struct {
	start int
	end   int
	width int
}

type secretLayout struct {
	rows      []secretLayoutRow
	cursorRow int
	cursorCol int
}

// layoutSecret mirrors the 80-column navigation geometry used by Editor.
// Rows contain byte ranges only; the draft is never converted to a string.
func layoutSecret(buf []byte, pos int) secretLayout {
	if pos < 0 {
		pos = 0
	}
	if pos > len(buf) {
		pos = len(buf)
	}
	layout := secretLayout{rows: make([]secretLayoutRow, 0, 1)}
	rowStart := 0
	cells := 0
	state := -1
	flush := func(end int) {
		layout.rows = append(layout.rows, secretLayoutRow{start: rowStart, end: end, width: cells})
		rowStart = end
		cells = 0
	}

	for i := 0; i < len(buf); {
		cluster, _, width, nextState := uniseg.FirstGraphemeCluster(buf[i:], state)
		n := len(cluster)
		if n == 0 {
			break
		}
		if n == 1 && cluster[0] == '\n' {
			if i == pos {
				layout.cursorRow = len(layout.rows)
				layout.cursorCol = cells
			}
			flush(i)
			i++
			rowStart = i
			state = nextState
			continue
		}
		if n == 1 && cluster[0] == '\t' {
			width = 8 - cells%8
		} else {
			width = secretGraphemeWidth(cluster, width)
		}
		if cells > 0 && cells+width > secretNavigationLimit {
			flush(i)
			if n == 1 && cluster[0] == '\t' {
				width = 8 - cells%8
			}
		}
		if i == pos {
			layout.cursorRow = len(layout.rows)
			layout.cursorCol = cells
		}
		if n == 1 && cluster[0] == '\t' {
			width = min(width, secretNavigationLimit-cells)
		} else if width > secretNavigationLimit {
			// LayoutDraft uses a one-cell placeholder for a grapheme
			// wider than the entire row.
			width = 1
		}
		cells += width
		i += n
		state = nextState
	}
	if pos == len(buf) {
		layout.cursorRow = len(layout.rows)
		layout.cursorCol = cells
	}
	layout.rows = append(layout.rows, secretLayoutRow{start: rowStart, end: len(buf), width: cells})
	return layout
}

// visualSecretPos maps a visual column on one row back to a source byte
// offset. It uses the same grapheme and cell policy as layoutSecret.
func visualSecretPos(buf []byte, row secretLayoutRow, col int) int {
	cells := 0
	pos := row.start
	state := -1
	for pos < row.end {
		cluster, _, width, nextState := uniseg.FirstGraphemeCluster(buf[pos:], state)
		if len(cluster) == 0 {
			break
		}
		if len(cluster) == 1 && cluster[0] == '\t' {
			width = min(8-cells%8, secretNavigationLimit-cells)
		} else {
			width = secretGraphemeWidth(cluster, width)
			if width > secretNavigationLimit {
				width = 1
			}
		}
		if cells+width > col {
			break
		}
		cells += width
		pos += len(cluster)
		state = nextState
	}
	return pos
}

func secretWordBackward(buf []byte, off int) int {
	if off <= 0 {
		return 0
	}
	if off > len(buf) {
		off = len(buf)
	}
	prefix := buf[:off]
	ascii := true
	for i := 0; i < len(prefix); i++ {
		if prefix[i] >= 0x80 {
			ascii = false
			break
		}
	}
	if ascii {
		return secretWordBackwardASCII(prefix)
	}

	starts := []int{0}
	state := -1
	for pos := 0; pos < len(prefix); {
		cluster, _, _, nextState := uniseg.FirstGraphemeCluster(prefix[pos:], state)
		if len(cluster) == 0 {
			break
		}
		pos += len(cluster)
		starts = append(starts, pos)
		state = nextState
	}

	i := len(starts) - 1
	for i > 0 {
		g := prefix[starts[i-1]:starts[i]]
		if !secretIsSpace(g) {
			break
		}
		i--
	}
	if i <= 0 {
		return 0
	}
	last := prefix[starts[i-1]:starts[i]]
	lastEnd := starts[i]
	isWord := secretIsWordLike(last)
	for i > 0 {
		g := prefix[starts[i-1]:starts[i]]
		if secretIsSpace(g) {
			break
		}
		if secretIsWordLike(g) != isWord {
			break
		}
		if isWord && secretIsPunct(g) && starts[i] != lastEnd-len(last) {
			if !secretIsPunct(last) {
				break
			}
		}
		i--
		if !isWord {
			continue
		}
		if secretIsPunct(g) {
			break
		}
	}
	return starts[i]
}

func secretWordBackwardASCII(s []byte) int {
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

func secretIsSpace(cluster []byte) bool {
	for len(cluster) > 0 {
		r, size := utf8.DecodeRune(cluster)
		if !unicode.IsSpace(r) {
			return false
		}
		cluster = cluster[size:]
	}
	return true
}

func secretIsWordLike(cluster []byte) bool {
	if len(cluster) == 0 || secretIsSpace(cluster) {
		return false
	}
	for len(cluster) > 0 {
		r, size := utf8.DecodeRune(cluster)
		if unicode.IsLetter(r) || unicode.IsNumber(r) || r == '_' {
			return true
		}
		cluster = cluster[size:]
	}
	return false
}

func secretIsPunct(cluster []byte) bool {
	r, size := utf8.DecodeRune(cluster)
	if r == utf8.RuneError && size == 1 {
		return false
	}
	if size != len(cluster) {
		return false
	}
	switch r {
	case '.', ',', ';', ':', '!', '?', '(', ')', '[', ']', '{', '}', '<', '>', '"', '\'', '`', '~', '@', '#', '$', '%', '^', '&', '*', '-', '+', '=', '|', '\\', '/', '…', '—', '：', '；', '！', '？', '（', '）', '［', '］', '｛', '｝', '“', '”', '‘', '’':
		return true
	default:
		return false
	}
}
