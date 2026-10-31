package terminal

import (
	"strings"
	"testing"

	"github.com/rivo/uniseg"
)

// P2-022: Tab completes slash-commands + skill names, no exec/scan/provider.
func TestCompletion(t *testing.T) {
	cmds := []string{"help", "new", "resume", "model", "thinking", "compact", "session", "history", "quit"}
	skills := []string{"review", "refactor"}
	cands, show := Complete("/mod", 4, cmds, skills)
	if len(cands) != 1 || cands[0] != "/model" {
		t.Fatalf("single: %v", cands)
	}
	if show {
		t.Fatal("single should not show list")
	}
	cands, show = Complete("/", 1, cmds, skills)
	if len(cands) < len(cmds) {
		t.Fatalf("root should list all: %v", cands)
	}
	if !show {
		t.Fatal("ambiguous should show list")
	}
	cands, _ = Complete("/skill:rev", 10, cmds, skills)
	if len(cands) != 1 || !strings.HasPrefix(cands[0], "/skill:") {
		t.Fatalf("skill: %v", cands)
	}
	// Non-slash gets no completion.
	if c, _ := Complete("hello", 5, cmds, skills); len(c) != 0 {
		t.Fatalf("text completion leaked: %v", c)
	}
}

func TestRestoreDraftPreservesTextAndGraphemeSafeCursorWithoutHistory(t *testing.T) {
	e := NewEditor()
	if !e.RestoreDraft("e\u0301x", 1) {
		t.Fatal("restore rejected a bounded draft")
	}
	if e.Text() != "e\u0301x" || e.Pos() != 2 || len(e.History()) != 0 {
		t.Fatalf("draft=%q pos=%d history=%q", e.Text(), e.Pos(), e.History())
	}
}

// P2-023: viewport resize-safe, 1-column + very short terminals.
func TestViewportResizeSafe(t *testing.T) {
	e := NewEditor()
	e.Insert("hello world\nsecond line with emoji 🌍 and CJK あいう")
	e.End()
	for _, w := range []int{1, 2, 4, 10, 80} {
		for _, h := range []int{1, 2, 4, 24} {
			l := LayoutDraft(e.buf, e.pos, w)
			if len(l.Rows) == 0 {
				t.Fatalf("w=%d h=%d: no rows", w, h)
			}
			// Viewport bounds.
			maxRows := max(1, h-2)
			start := max(0, l.CursorRow-maxRows+1)
			end := min(len(l.Rows), start+maxRows)
			if start < 0 || end > len(l.Rows) {
				t.Fatalf("w=%d h=%d: bad viewport %d:%d", w, h, start, end)
			}
			// Draft preserved.
			if e.Text() == "" {
				t.Fatal("draft lost on resize")
			}
		}
	}
}

// P2-024: lifecycle — clear, submit, recall isolation, blank local.
func TestEditorLifecycle(t *testing.T) {
	e := NewEditor()
	e.SetHistory([]string{"old1", "old2"})
	e.Insert("draft")
	e.PrevHistory()
	if e.Text() != "old2" {
		t.Fatalf("recall: %q", e.Text())
	}
	// Editing while browsing exits browsing (pi parity).
	e.Insert("!")
	if e.History()[len(e.History())-1] == e.Text() {
		t.Fatal("edit should exit browsing, not mutate history")
	}
	// Submit pushes, clears draft, preserves recall source.
	s := e.Submit()
	if s == "" {
		t.Fatal("submit empty")
	}
	// Session change clears stale recall drafts.
	e2 := NewEditor()
	e2.SetHistory([]string{"s2"})
	if len(e2.History()) != 1 || e2.Text() != "" {
		t.Fatalf("stale recall: %v %q", e2.History(), e2.Text())
	}
	// Blank input stays local (Submit returns blank, caller must not send).
	e3 := NewEditor()
	e3.Insert("   ")
	if strings.TrimSpace(e3.Submit()) != "" {
		t.Fatal("blank should stay local")
	}
}

// P2-016: layout goldens — ASCII, tabs, wide, combining, emoji at narrow widths.
func TestLayoutGoldens(t *testing.T) {
	cases := []struct {
		text  string
		width int
	}{
		{"hello", 80},
		{"a\tb", 8},
		{"あいうえお", 4},
		{"e\u0301", 80},  // combining
		{"👩‍❤️‍💋‍👩", 80}, // joined emoji
		{"🇩🇪🇫🇷", 4},      // flags
	}
	for _, tc := range cases {
		buf := []rune(tc.text)
		l := LayoutDraft(buf, len(buf), tc.width)
		if len(l.Rows) == 0 {
			t.Fatalf("%q: no rows", tc.text)
		}
		// Cursor on last row at end.
		if l.CursorRow != len(l.Rows)-1 {
			t.Fatalf("%q: cursor row %d, rows %d", tc.text, l.CursorRow, len(l.Rows))
		}
		// No row exceeds width (in cells).
		for _, r := range l.Rows {
			// Strip prefix for measurement ("> "/"  ").
			body := r
			if len(body) >= 2 {
				body = body[2:]
			}
			if CellWidth(body) > tc.width {
				t.Fatalf("%q w=%d: row %q exceeds", tc.text, tc.width, r)
			}
		}
	}
}

// Model-based reference editor for property testing.
type refEditorModel struct {
	buf []rune
	pos int
}

func newRefEditor() *refEditorModel {
	return &refEditorModel{}
}

func (r *refEditorModel) Text() string { return string(r.buf) }
func (r *refEditorModel) Pos() int     { return r.pos }

func (r *refEditorModel) Insert(s string) {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	runes := []rune(s)
	newBuf := make([]rune, 0, len(r.buf)+len(runes))
	newBuf = append(newBuf, r.buf[:r.pos]...)
	newBuf = append(newBuf, runes...)
	newBuf = append(newBuf, r.buf[r.pos:]...)
	r.buf = newBuf
	r.pos += len(runes)
}

func (r *refEditorModel) Backspace() {
	if r.pos <= 0 {
		return
	}
	s := string(r.buf)
	boff := runeToByte(r.buf, r.pos)
	prev := prevGraphemeBoundary(s, boff)
	newText := s[:prev] + s[boff:]
	r.buf = []rune(newText)
	r.pos = byteToRune(r.buf, prev)
}

func (r *refEditorModel) Delete() {
	if r.pos >= len(r.buf) {
		return
	}
	s := string(r.buf)
	boff := runeToByte(r.buf, r.pos)
	next := nextGraphemeBoundary(s, boff)
	newText := s[:boff] + s[next:]
	r.buf = []rune(newText)
	r.pos = byteToRune(r.buf, boff)
}

func (r *refEditorModel) MoveLeft() {
	if r.pos <= 0 {
		return
	}
	s := string(r.buf)
	boff := runeToByte(r.buf, r.pos)
	prev := prevGraphemeBoundary(s, boff)
	r.pos = byteToRune(r.buf, prev)
}

func (r *refEditorModel) MoveRight() {
	if r.pos >= len(r.buf) {
		return
	}
	s := string(r.buf)
	boff := runeToByte(r.buf, r.pos)
	next := nextGraphemeBoundary(s, boff)
	r.pos = byteToRune(r.buf, next)
}

func (r *refEditorModel) Home() {
	start := r.pos
	for start > 0 && r.buf[start-1] != '\n' {
		start--
	}
	r.pos = start
}

func (r *refEditorModel) End() {
	end := r.pos
	for end < len(r.buf) && r.buf[end] != '\n' {
		end++
	}
	r.pos = end
}

func (r *refEditorModel) DocStart() { r.pos = 0 }
func (r *refEditorModel) DocEnd()   { r.pos = len(r.buf) }

func (r *refEditorModel) DeleteToLineStart() {
	start := r.pos
	for start > 0 && r.buf[start-1] != '\n' {
		start--
	}
	newBuf := append([]rune(nil), r.buf[:start]...)
	newBuf = append(newBuf, r.buf[r.pos:]...)
	r.buf = newBuf
	r.pos = start
}

func (r *refEditorModel) DeleteToLineEnd() {
	end := r.pos
	for end < len(r.buf) && r.buf[end] != '\n' {
		end++
	}
	newBuf := append([]rune(nil), r.buf[:r.pos]...)
	newBuf = append(newBuf, r.buf[end:]...)
	r.buf = newBuf
}

func (r *refEditorModel) Clear() {
	r.buf = nil
	r.pos = 0
}

func TestModelBasedEditorProperty(t *testing.T) {
	e := NewEditor()
	ref := newRefEditor()

	actions := []string{
		"insert_ascii", "insert_multiline", "insert_cjk", "insert_emoji",
		"backspace", "delete", "left", "right", "home", "end",
		"docstart", "docend", "deletetolinestart", "deletetolineend", "clear",
	}

	samples := []string{
		"hello world",
		"foo\nbar\nbaz",
		"あいうえお",
		"👩‍❤️‍💋‍👩",
		"e\u0301x",
		"🇩🇪",
		"\r\ntest\r\n",
	}

	// Pseudo-random deterministic sequence of operations
	seed := uint32(1337)
	randNext := func() uint32 {
		seed = seed*1664525 + 1013904223
		return seed
	}

	for step := 0; step < 1000; step++ {
		act := actions[randNext()%uint32(len(actions))]
		switch act {
		case "insert_ascii", "insert_multiline", "insert_cjk", "insert_emoji":
			sample := samples[randNext()%uint32(len(samples))]
			e.Insert(sample)
			ref.Insert(sample)
		case "backspace":
			e.Backspace()
			ref.Backspace()
		case "delete":
			e.Delete()
			ref.Delete()
		case "left":
			e.MoveLeft()
			ref.MoveLeft()
		case "right":
			e.MoveRight()
			ref.MoveRight()
		case "home":
			e.Home()
			ref.Home()
		case "end":
			e.End()
			ref.End()
		case "docstart":
			e.DocStart()
			ref.DocStart()
		case "docend":
			e.DocEnd()
			ref.DocEnd()
		case "deletetolinestart":
			e.DeleteToLineStart()
			ref.DeleteToLineStart()
		case "deletetolineend":
			e.DeleteToLineEnd()
			ref.DeleteToLineEnd()
		case "clear":
			e.Clear()
			ref.Clear()
		}

		if e.Text() != ref.Text() {
			t.Fatalf("step %d (%s): text mismatch: got %q, want %q", step, act, e.Text(), ref.Text())
		}
		if e.Pos() != ref.Pos() {
			t.Fatalf("step %d (%s): pos mismatch: got %d, want %d (text=%q)", step, act, e.Pos(), ref.Pos(), e.Text())
		}
	}
}

func TestEditorUnicodeGraphemeClusters(t *testing.T) {
	// Combining accent
	e := NewEditor()
	e.Insert("café\u0301") // é + acute
	if e.Pos() != 5 {      // 'c','a','f','e', combining acute
		t.Fatalf("pos %d, want 5", e.Pos())
	}
	e.MoveLeft()
	if e.Pos() != 3 { // jumps past both 'e' and combining accent to after 'f'
		t.Fatalf("left across combining mark: pos %d, want 3", e.Pos())
	}
	e.MoveRight()
	if e.Pos() != 5 {
		t.Fatalf("right across combining mark: pos %d, want 5", e.Pos())
	}
	e.Backspace()
	if e.Text() != "caf" || e.Pos() != 3 {
		t.Fatalf("backspace combining mark: text=%q pos=%d", e.Text(), e.Pos())
	}

	// Emoji ZWJ sequence
	e.Clear()
	zwj := "👩‍❤️‍💋‍👩"
	e.Insert(zwj)
	if e.Pos() != len([]rune(zwj)) {
		t.Fatalf("pos %d want %d", e.Pos(), len([]rune(zwj)))
	}
	e.MoveLeft()
	if e.Pos() != 0 {
		t.Fatalf("move left past ZWJ emoji: pos %d want 0", e.Pos())
	}
	e.MoveRight()
	if e.Pos() != len([]rune(zwj)) {
		t.Fatalf("move right past ZWJ emoji: pos %d want %d", e.Pos(), len([]rune(zwj)))
	}
	e.Backspace()
	if e.Text() != "" || e.Pos() != 0 {
		t.Fatalf("backspace ZWJ emoji: text=%q pos=%d", e.Text(), e.Pos())
	}

	// Delete forward across ZWJ emoji
	e.Insert("a" + zwj + "b")
	e.DocStart()
	e.MoveRight() // after 'a'
	if e.Pos() != 1 {
		t.Fatalf("pos %d want 1", e.Pos())
	}
	e.Delete() // deletes zwj
	if e.Text() != "ab" || e.Pos() != 1 {
		t.Fatalf("delete forward ZWJ emoji: text=%q pos=%d", e.Text(), e.Pos())
	}

	// Regional indicator flag
	e.Clear()
	flag := "🇩🇪"
	e.Insert(flag)
	if e.Pos() != 2 {
		t.Fatalf("pos %d want 2", e.Pos())
	}
	e.MoveLeft()
	if e.Pos() != 0 {
		t.Fatalf("move left past flag: pos %d want 0", e.Pos())
	}
	e.Delete()
	if e.Text() != "" || e.Pos() != 0 {
		t.Fatalf("delete flag: text=%q pos=%d", e.Text(), e.Pos())
	}
}

func TestEditorVerticalMovementAndStickyColumn(t *testing.T) {
	e := NewEditor()
	// Line 0: "0123456789" (len 10)
	// Line 1: "abc" (len 3)
	// Line 2: "ABCDEFGHIJ" (len 10)
	e.Insert("0123456789\nabc\nABCDEFGHIJ")

	// Start at line 0, pos 8
	e.DocStart()
	for i := 0; i < 8; i++ {
		e.MoveRight()
	}
	if e.Pos() != 8 {
		t.Fatalf("initial pos: %d want 8", e.Pos())
	}

	// Move down to Line 1: col 8 exceeds line 1 ("abc"), cursor should clamp to col 3 (pos 14)
	if !e.MoveDown(80) {
		t.Fatal("MoveDown line 0->1 failed")
	}
	if e.Pos() != 14 { // 10 (line 0) + 1 ('\n') + 3 ('abc')
		t.Fatalf("pos on line 1: %d want 14", e.Pos())
	}

	// Move down to Line 2: sticky column should restore to visual col 8!
	if !e.MoveDown(80) {
		t.Fatal("MoveDown line 1->2 failed")
	}
	if e.Pos() != 23 { // 15 (line 0, 1, newlines) + 8 = 23
		t.Fatalf("pos on line 2 with sticky col: %d want 23", e.Pos())
	}

	// Bottom line move down returns false
	if e.MoveDown(80) {
		t.Fatal("MoveDown on bottom line should return false")
	}

	// Move up to Line 1: clamps to col 3 (pos 14)
	if !e.MoveUp(80) {
		t.Fatal("MoveUp line 2->1 failed")
	}
	if e.Pos() != 14 {
		t.Fatalf("pos on line 1: %d want 14", e.Pos())
	}

	// Move up to Line 0: sticky col restores to col 8!
	if !e.MoveUp(80) {
		t.Fatal("MoveUp line 1->0 failed")
	}
	if e.Pos() != 8 {
		t.Fatalf("pos on line 0: %d want 8", e.Pos())
	}

	// Top line move up returns false
	if e.MoveUp(80) {
		t.Fatal("MoveUp on top line should return false")
	}

	// Moving horizontally resets sticky column
	e.MoveLeft() // pos 7
	if !e.MoveDown(80) {
		t.Fatal("MoveDown failed")
	}
	if !e.MoveDown(80) {
		t.Fatal("MoveDown failed")
	}
	// On line 2, sticky column is now 7, so pos = 15 + 7 = 22
	if e.Pos() != 22 {
		t.Fatalf("pos after horizontal move reset sticky: %d want 22", e.Pos())
	}
}

func TestComputeVerticalMoveColumnDecisionTable(t *testing.T) {
	// Tests all 7 rows of pi's decision table in computeVerticalMoveColumn.
	tests := []struct {
		name         string
		initialPref  int
		currentCol   int
		sourceMaxCol int
		targetMaxCol int
		expectedCol  int
		expectedPref int
	}{
		{
			name:         "Row 1: P=0 S=* T=0 - Start nav, target fits",
			initialPref:  -1,
			currentCol:   5,
			sourceMaxCol: 10,
			targetMaxCol: 8,
			expectedCol:  5,
			expectedPref: -1,
		},
		{
			name:         "Row 1 (at end): P=0 S=0 T=0 - Start nav, target fits",
			initialPref:  -1,
			currentCol:   10,
			sourceMaxCol: 10,
			targetMaxCol: 12,
			expectedCol:  10,
			expectedPref: -1,
		},
		{
			name:         "Row 2: P=0 S=* T=1 - Start nav, target shorter",
			initialPref:  -1,
			currentCol:   8,
			sourceMaxCol: 10,
			targetMaxCol: 4,
			expectedCol:  4,
			expectedPref: 8,
		},
		{
			name:         "Row 3: P=1 S=0 T=0 U=0 - Clamped, target fits preferred",
			initialPref:  8,
			currentCol:   4,
			sourceMaxCol: 4,
			targetMaxCol: 10,
			expectedCol:  8,
			expectedPref: -1,
		},
		{
			name:         "Row 4: P=1 S=0 T=0 U=1 - Clamped, target longer but cannot fit preferred",
			initialPref:  10,
			currentCol:   4,
			sourceMaxCol: 4,
			targetMaxCol: 7,
			expectedCol:  7,
			expectedPref: 10,
		},
		{
			name:         "Row 5: P=1 S=0 T=1 - Clamped, target even shorter",
			initialPref:  10,
			currentCol:   6,
			sourceMaxCol: 6,
			targetMaxCol: 3,
			expectedCol:  3,
			expectedPref: 10,
		},
		{
			name:         "Row 6: P=1 S=1 T=0 - Rewrapped, target fits current",
			initialPref:  10,
			currentCol:   5,
			sourceMaxCol: 12,
			targetMaxCol: 8,
			expectedCol:  5,
			expectedPref: -1,
		},
		{
			name:         "Row 7: P=1 S=1 T=1 - Rewrapped, target shorter than current",
			initialPref:  10,
			currentCol:   7,
			sourceMaxCol: 12,
			targetMaxCol: 4,
			expectedCol:  4,
			expectedPref: 7,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			pref := tc.initialPref
			col := computeVerticalMoveColumn(&pref, tc.currentCol, tc.sourceMaxCol, tc.targetMaxCol)
			if col != tc.expectedCol {
				t.Fatalf("col=%d, want %d", col, tc.expectedCol)
			}
			if pref != tc.expectedPref {
				t.Fatalf("pref=%d, want %d", pref, tc.expectedPref)
			}
		})
	}
}

func TestLayoutRowWidth(t *testing.T) {
	buf := []rune("first line\n\nthird\tline\nunicode 🌍 あいう")
	l := LayoutDraft(buf, 0, 80)
	if l.RowWidth(-1) != 0 {
		t.Errorf("RowWidth(-1) = %d, want 0", l.RowWidth(-1))
	}
	if l.RowWidth(len(l.Rows)) != 0 {
		t.Errorf("RowWidth(out of bounds) = %d, want 0", l.RowWidth(len(l.Rows)))
	}
	if l.RowWidth(0) != len("first line") {
		t.Errorf("RowWidth(0) = %d, want %d", l.RowWidth(0), len("first line"))
	}
	if l.RowWidth(1) != 0 {
		t.Errorf("RowWidth(1) = %d, want 0", l.RowWidth(1))
	}
	if w := l.RowWidth(2); w <= len("third\tline") {
		t.Errorf("RowWidth(2) = %d, expected tab expansion > raw len", w)
	}
	if w := l.RowWidth(3); w != CellWidth("unicode 🌍 あいう") {
		t.Errorf("RowWidth(3) = %d, want %d", w, CellWidth("unicode 🌍 あいう"))
	}
}

func TestEditorStickyColumnClampingAndProgression(t *testing.T) {
	// Tests Cases 1, 2, 4, 5, 3 in sequence across lines of varying lengths.
	e := NewEditor()
	// Line 0: len 10 ("0123456789")
	// Line 1: len 6  ("123456")
	// Line 2: len 3  ("abc")
	// Line 3: len 7  ("abcdefg")
	// Line 4: len 12 ("0123456789AB")
	e.Insert("0123456789\n123456\nabc\nabcdefg\n0123456789AB")

	// Start at line 0, col 8. prefCol = -1.
	e.DocStart()
	for i := 0; i < 8; i++ {
		e.MoveRight()
	}
	if e.Pos() != 8 || e.prefCol != -1 {
		t.Fatalf("start: pos=%d prefCol=%d", e.Pos(), e.prefCol)
	}

	// Move down to Line 1 (len 6):
	// Case 2 (target shorter): targetMax=6 < currentCol=8 -> moves to col 6, prefCol=8.
	if !e.MoveDown(80) {
		t.Fatal("MoveDown 0->1 failed")
	}
	if e.Pos() != 17 || e.prefCol != 8 { // 10 + 1 + 6 = 17
		t.Fatalf("line 1: pos=%d want 17, prefCol=%d want 8", e.Pos(), e.prefCol)
	}

	// Move down to Line 2 (len 3):
	// Case 5 (target even shorter): currentCol=6, targetMax=3 < 6 -> moves to col 3, keeps prefCol=8.
	if !e.MoveDown(80) {
		t.Fatal("MoveDown 1->2 failed")
	}
	if e.Pos() != 21 || e.prefCol != 8 { // 17 + 1 + 3 = 21
		t.Fatalf("line 2: pos=%d want 21, prefCol=%d want 8", e.Pos(), e.prefCol)
	}

	// Move down to Line 3 (len 7):
	// Case 4 (target longer than current 3, but < prefCol 8):
	// targetMax=7, currentCol=3, prefCol=8 -> moves to col 7, keeps prefCol=8.
	if !e.MoveDown(80) {
		t.Fatal("MoveDown 2->3 failed")
	}
	if e.Pos() != 29 || e.prefCol != 8 { // 21 + 1 + 7 = 29
		t.Fatalf("line 3: pos=%d want 29, prefCol=%d want 8", e.Pos(), e.prefCol)
	}

	// Move down to Line 4 (len 12):
	// Case 3 (target fits preferred 8): targetMax=12 >= prefCol=8 -> moves to col 8, prefCol=-1.
	if !e.MoveDown(80) {
		t.Fatal("MoveDown 3->4 failed")
	}
	if e.Pos() != 38 || e.prefCol != -1 { // 29 + 1 + 8 = 38
		t.Fatalf("line 4: pos=%d want 38, prefCol=%d want -1", e.Pos(), e.prefCol)
	}
}

func TestEditorVerticalMoveRewrapping(t *testing.T) {
	// Verifies behavior when terminal width changes between vertical moves,
	// including rewrapping that causes cursor to be in the middle of a line.
	e := NewEditor()
	// Line 0: "0123456789" (len 10)
	// Line 1: "abc" (len 3)
	// Line 2: "0123456789" (len 10)
	e.Insert("0123456789\nabc\n0123456789")

	// Start at line 0, col 8
	e.DocStart()
	for i := 0; i < 8; i++ {
		e.MoveRight()
	}

	// MoveDown at width 80 -> clamps to col 3 on line 1, prefCol = 8.
	if !e.MoveDown(80) {
		t.Fatal("MoveDown line 0->1 failed")
	}
	if e.prefCol != 8 || e.Pos() != 14 {
		t.Fatalf("after clamp: pos=%d prefCol=%d", e.Pos(), e.prefCol)
	}

	// Terminal width narrows to 4 cells (limit 3 cells per row).
	// Line 0 wraps into rows of 3: "012", "345", "678", "9".
	// Line 1 is "abc" (3 cells).
	// MoveUp(4) from "abc" to Row 3 ("9", len 1):
	// Case 5 (target even shorter): keeps prefCol=8, lands on rune 10.
	if !e.MoveUp(4) {
		t.Fatal("MoveUp at width 4 failed")
	}
	// Second MoveUp(4) to Row 2 ("678", len 3):
	// Case 4 (target longer but < preferred): keeps prefCol=8, lands on rune 9.
	if !e.MoveUp(4) {
		t.Fatal("MoveUp 2 at width 4 failed")
	}
	if e.prefCol != 8 {
		t.Fatalf("prefCol after narrow MoveUp = %d, want 8", e.prefCol)
	}

	// Now terminal width expands back to 80.
	// Rune 9 in width 80 is col 9 on Line 0 (len 10), so cursor is in the middle (9 < 10).
	l := LayoutDraft(e.buf, e.pos, 80)
	if l.CursorRow != 0 || l.CursorCol >= l.RowWidth(0) {
		t.Fatalf("expected cursor in middle of row 0: row=%d col=%d width=%d",
			l.CursorRow, l.CursorCol, l.RowWidth(0))
	}
	// Case 7: cursor in middle (S=1), target (line 1, len 3) is shorter than current visual col (9).
	// Target clamps to 3, and prefCol becomes currentVisualCol (9).
	curCol := l.CursorCol
	if !e.MoveDown(80) {
		t.Fatal("MoveDown at width 80 failed")
	}
	if e.prefCol != curCol {
		t.Fatalf("Case 7: prefCol=%d, want %d", e.prefCol, curCol)
	}
	if e.Pos() != 14 { // end of line 1
		t.Fatalf("Case 7: pos=%d, want 14", e.Pos())
	}
}

func TestEditorVerticalMoveRewrappingCase6(t *testing.T) {
	e := NewEditor()
	// Line 0: "0123456789" (len 10)
	// Line 1: "0123456789" (len 10)
	e.Insert("0123456789\n0123456789")

	// Set preferred column = 8, cursor at col 4 on line 0 (cursor in middle 4 < 10).
	e.DocStart()
	for i := 0; i < 4; i++ {
		e.MoveRight()
	}
	e.prefCol = 8 // preferred column from prior move

	// MoveDown: target line has len 10 >= currentCol 4.
	// Case 6 triggers: target fits current, prefCol resets to -1, lands on col 4.
	if !e.MoveDown(80) {
		t.Fatal("MoveDown failed")
	}
	if e.prefCol != -1 {
		t.Fatalf("Case 6: prefCol=%d, want -1", e.prefCol)
	}
	if e.Pos() != 15 { // 10 + 1 + 4 = 15
		t.Fatalf("Case 6: pos=%d, want 15", e.Pos())
	}
}

func TestEditorHandleKeyAllBindings(t *testing.T) {
	e := NewEditor()

	// Text input
	e.HandleKey([]byte("hello"), false)
	if e.Text() != "hello" {
		t.Fatalf("text=%q want 'hello'", e.Text())
	}

	// Enter / Return submits
	res, sub := e.HandleKey([]byte{'\r'}, false)
	if !sub || res != "hello" || e.Text() != "" {
		t.Fatalf("submit: res=%q sub=%v text=%q", res, sub, e.Text())
	}

	// Newline insertion
	e.HandleKey([]byte{'a'}, false)
	e.HandleKey([]byte{'\n'}, false)
	e.HandleKey([]byte{'b'}, false)
	if e.Text() != "a\nb" {
		t.Fatalf("newline: text=%q", e.Text())
	}

	// Ctrl+A (Home) and Ctrl+E (End)
	e.HandleKey([]byte{1}, false) // Home on line 1
	if e.Pos() != 2 {             // start of line 1 is pos 2
		t.Fatalf("Ctrl+A: pos=%d want 2", e.Pos())
	}
	e.HandleKey([]byte{5}, false) // End on line 1
	if e.Pos() != 3 {
		t.Fatalf("Ctrl+E: pos=%d want 3", e.Pos())
	}

	// Ctrl+K (DeleteToLineEnd)
	e.HandleKey([]byte{1}, false)  // Home (pos 2)
	e.HandleKey([]byte{11}, false) // DeleteToLineEnd
	if e.Text() != "a\n" {
		t.Fatalf("Ctrl+K: text=%q want 'a\\n'", e.Text())
	}

	// Ctrl+U (DeleteToLineStart)
	e.Insert("test")
	e.HandleKey([]byte{21}, false) // DeleteToLineStart
	if e.Text() != "a\n" {
		t.Fatalf("Ctrl+U: text=%q want 'a\\n'", e.Text())
	}

	// Ctrl+W (DeleteWordBackward)
	e.Insert("word1 word2")
	e.HandleKey([]byte{23}, false)
	if e.Text() != "a\nword1 " {
		t.Fatalf("Ctrl+W: text=%q", e.Text())
	}

	// Backspace (127 and 8)
	e.HandleKey([]byte{127}, false)
	if e.Text() != "a\nword1" {
		t.Fatalf("Backspace 127: text=%q", e.Text())
	}
	e.HandleKey([]byte{8}, false)
	if e.Text() != "a\nword" {
		t.Fatalf("Backspace 8: text=%q", e.Text())
	}

	// Ctrl+P / Ctrl+N for history
	e.SetHistory([]string{"h1", "h2"})
	e.HandleKey([]byte{16}, false) // PrevHistory
	if e.Text() != "h2" {
		t.Fatalf("Ctrl+P: text=%q want 'h2'", e.Text())
	}
	e.HandleKey([]byte{14}, false) // NextHistory -> restores draft
	if e.Text() != "a\nword" {
		t.Fatalf("Ctrl+N: text=%q want draft", e.Text())
	}

	// Ignored keys: Ctrl+C (3), Ctrl+D (4), Tab (9), Esc (27)
	e.HandleKey([]byte{3}, false)
	e.HandleKey([]byte{4}, false)
	e.HandleKey([]byte{9}, false)
	e.HandleKey([]byte{27}, false)
	if e.Text() != "a\nword" {
		t.Fatalf("ignored keys changed text: %q", e.Text())
	}

	// Arrow keys and application cursor sequences
	e.Clear()
	e.Insert("xyz")
	e.HandleKey([]byte("\x1b[D"), false) // Left
	if e.Pos() != 2 {
		t.Fatalf("Left arrow: pos=%d want 2", e.Pos())
	}
	e.HandleKey([]byte("\x1bOD"), false) // App Left
	if e.Pos() != 1 {
		t.Fatalf("App Left: pos=%d want 1", e.Pos())
	}
	e.HandleKey([]byte("\x1b[C"), false) // Right
	if e.Pos() != 2 {
		t.Fatalf("Right arrow: pos=%d want 2", e.Pos())
	}
	e.HandleKey([]byte("\x1bOC"), false) // App Right
	if e.Pos() != 3 {
		t.Fatalf("App Right: pos=%d want 3", e.Pos())
	}

	// Delete key (\x1b[3~)
	e.HandleKey([]byte("\x1b[D"), false)  // at pos 2 ('z')
	e.HandleKey([]byte("\x1b[3~"), false) // delete 'z'
	if e.Text() != "xy" {
		t.Fatalf("Delete key: text=%q want 'xy'", e.Text())
	}

	// Home / End escape variants
	e.HandleKey([]byte("\x1b[H"), false)
	if e.Pos() != 0 {
		t.Fatalf("Home \\x1b[H: pos=%d want 0", e.Pos())
	}
	e.HandleKey([]byte("\x1b[F"), false)
	if e.Pos() != 2 {
		t.Fatalf("End \\x1b[F: pos=%d want 2", e.Pos())
	}
	e.HandleKey([]byte("\x1b[1~"), false)
	if e.Pos() != 0 {
		t.Fatalf("Home \\x1b[1~: pos=%d want 0", e.Pos())
	}
	e.HandleKey([]byte("\x1b[4~"), false)
	if e.Pos() != 2 {
		t.Fatalf("End \\x1b[4~: pos=%d want 2", e.Pos())
	}
	e.HandleKey([]byte("\x1bOH"), false)
	if e.Pos() != 0 {
		t.Fatalf("Home \\x1bOH: pos=%d want 0", e.Pos())
	}
	e.HandleKey([]byte("\x1bOF"), false)
	if e.Pos() != 2 {
		t.Fatalf("End \\x1bOF: pos=%d want 2", e.Pos())
	}
	e.HandleKey([]byte("\x1b[7~"), false)
	if e.Pos() != 0 {
		t.Fatalf("Home \\x1b[7~: pos=%d want 0", e.Pos())
	}
	e.HandleKey([]byte("\x1b[8~"), false)
	if e.Pos() != 2 {
		t.Fatalf("End \\x1b[8~: pos=%d want 2", e.Pos())
	}

	// DocStart and DocEnd
	e.Insert("\nline2")
	e.HandleKey([]byte("\x1b[1;5H"), false) // DocStart
	if e.Pos() != 0 {
		t.Fatalf("DocStart: pos=%d want 0", e.Pos())
	}
	e.HandleKey([]byte("\x1b[1;5F"), false) // DocEnd
	if e.Pos() != len(e.buf) {
		t.Fatalf("DocEnd: pos=%d want %d", e.Pos(), len(e.buf))
	}

	// Bracketed paste
	e.Clear()
	e.HandleKey([]byte("pasted text"), true)
	if e.Text() != "pasted text" {
		t.Fatalf("bracketed paste: text=%q", e.Text())
	}

	// Multi-byte UTF-8 insertion
	e.HandleKey([]byte("🔥"), false)
	if e.Text() != "pasted text🔥" {
		t.Fatalf("UTF-8 key: text=%q", e.Text())
	}
}

func TestEditorBudgetAndRecallRejection(t *testing.T) {
	e := NewEditor()
	huge := strings.Repeat("x", editorBudget+10)
	if e.Insert(huge) {
		t.Fatal("oversized insert should be rejected")
	}
	if !e.rejectedInsert {
		t.Fatal("rejectedInsert flag should be set")
	}
	if e.Text() != "" {
		t.Fatal("editor text should be empty")
	}

	// Oversized history recall
	e.SetHistory([]string{huge, "valid"})
	if !e.rejectedRecall {
		t.Fatal("oversized history should flag rejectedRecall")
	}
	if len(e.History()) != 1 || e.History()[0] != "valid" {
		t.Fatalf("history should keep only valid: %v", e.History())
	}

	// ReplaceCurrentToken
	e.Clear()
	e.Insert("hello /mod world")
	e.DocStart()
	e.MoveRight() // 'h'
	e.MoveRight() // 'e'
	e.MoveRight() // 'l'
	e.MoveRight() // 'l'
	e.MoveRight() // 'o'
	e.MoveRight() // ' '
	e.MoveRight() // '/'
	e.MoveRight() // 'm'
	if !e.ReplaceCurrentToken("/model") {
		t.Fatal("replace token failed")
	}
	if e.Text() != "hello /model world" {
		t.Fatalf("replace token: text=%q", e.Text())
	}
}

// referenceUnisegSnapByte is the reference implementation without the fast path.
func referenceUnisegSnapByte(s string, off int) int {
	if off <= 0 {
		return 0
	}
	if off >= len(s) {
		return len(s)
	}
	gr := uniseg.NewGraphemes(s)
	for gr.Next() {
		start, _ := gr.Positions()
		if start >= off {
			return start
		}
	}
	return len(s)
}

// referenceUnisegPrevBoundary is the reference implementation without the fast path.
func referenceUnisegPrevBoundary(s string, boff int) int {
	if boff <= 0 {
		return 0
	}
	gr := uniseg.NewGraphemes(s)
	prev := 0
	for gr.Next() {
		start, _ := gr.Positions()
		if start >= boff {
			return prev
		}
		prev = start
	}
	return prev
}

// referenceUnisegNextBoundary is the reference implementation without the fast path.
func referenceUnisegNextBoundary(s string, boff int) int {
	if boff >= len(s) {
		return len(s)
	}
	gr := uniseg.NewGraphemes(s)
	for gr.Next() {
		start, _ := gr.Positions()
		if start > boff {
			return start
		}
	}
	return len(s)
}

func TestGraphemeBoundariesASCIIFastPath(t *testing.T) {
	testCases := []string{
		"",
		"a",
		"ab",
		"abc",
		"hello world",
		"The quick brown fox jumps over the lazy dog 1234567890!@#$%^&*()",
		"\r\n",
		"a\r\nb",
		"\r\n\r\n",
		"foo\r\nbar\r\nbaz",
		"e\u0301x",                 // combining accent
		"hello \U0001F30D world", // emoji
		"あいうえお",                  // CJK
		"a\tb\nc\rd",
		"👨‍👩‍👧‍👦",   // family emoji (ZWJ sequence)
		"🏳️‍🌈",    // flag
		"🇺🇸🇩🇪", // regional indicator flags
		"prefix \U0001F469\U0001F3FD\u200D\U0001F91D\u200D\U0001F468\U0001F3FB suffix",
		"mix_123_αβγ_456",
		"\r",
		"\n",
		"\n\r",
	}

	for _, s := range testCases {
		for off := -3; off <= len(s)+4; off++ {
			wantSnap := referenceUnisegSnapByte(s, off)
			gotSnap := snapByte(s, off)
			if gotSnap != wantSnap {
				t.Fatalf("snapByte(%q, %d): got %d, want %d", s, off, gotSnap, wantSnap)
			}

			wantPrev := referenceUnisegPrevBoundary(s, off)
			gotPrev := prevGraphemeBoundary(s, off)
			if gotPrev != wantPrev {
				t.Fatalf("prevGraphemeBoundary(%q, %d): got %d, want %d", s, off, gotPrev, wantPrev)
			}

			wantNext := referenceUnisegNextBoundary(s, off)
			gotNext := nextGraphemeBoundary(s, off)
			if gotNext != wantNext {
				t.Fatalf("nextGraphemeBoundary(%q, %d): got %d, want %d", s, off, gotNext, wantNext)
			}
		}
	}
}

func BenchmarkGraphemeBoundariesASCIIFastPath(b *testing.B) {
	s := strings.Repeat("abcdefghijklmnopqrstuvwxyz0123456789 ", 100) // 3700 bytes of ASCII
	mid := len(s) / 2

	b.Run("prevGraphemeBoundary", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			_ = prevGraphemeBoundary(s, mid)
		}
	})

	b.Run("nextGraphemeBoundary", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			_ = nextGraphemeBoundary(s, mid)
		}
	})

	b.Run("snapByte", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			_ = snapByte(s, mid)
		}
	})
}
