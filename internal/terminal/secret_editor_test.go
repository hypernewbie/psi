package terminal

import (
	"bytes"
	"strings"
	"testing"
)

func TestSecretEditorNormalizesInsertionAndReplacesInvalidUTF8(t *testing.T) {
	e := newSecretEditor()
	if !e.insert([]byte("a\r\nb\rc")) {
		t.Fatal("insert rejected")
	}
	if !bytes.Equal(e.buf, []byte("a\nb\nc")) {
		t.Fatalf("normalized draft = %q", e.buf)
	}
	if !e.insert([]byte{0xff}) {
		t.Fatal("invalid UTF-8 insert rejected")
	}
	if !bytes.Equal(e.buf, []byte("a\nb\nc\uFFFD")) {
		t.Fatalf("replacement draft = %q", e.buf)
	}
}

func TestSecretEditorBudgetCountsNormalizedBytes(t *testing.T) {
	e := newSecretEditor()
	if !e.insert(bytes.Repeat([]byte("x"), editorBudget-2)) {
		t.Fatal("could not fill near budget")
	}
	if e.insert([]byte{0xff}) || !e.rejectedInsert {
		t.Fatal("invalid UTF-8 expansion was not counted against the budget")
	}
	if len(e.buf) != editorBudget-2 {
		t.Fatalf("rejected insert changed length: %d", len(e.buf))
	}
	if !e.insert([]byte("\r\n")) {
		t.Fatal("normalized one-byte insert rejected at boundary")
	}
	if len(e.buf) != editorBudget-1 || e.buf[len(e.buf)-1] != '\n' {
		t.Fatalf("normalized boundary draft length=%d", len(e.buf))
	}
	if !e.insert([]byte("y")) {
		t.Fatal("insert at the budget boundary was rejected")
	}
	if len(e.buf) != editorBudget {
		t.Fatalf("boundary draft length=%d", len(e.buf))
	}
	if e.insert([]byte("z")) || !e.rejectedInsert {
		t.Fatal("over-budget insert was accepted")
	}
	if e.insert(bytes.Repeat([]byte("z"), editorBudget)) || len(e.buf) != editorBudget {
		t.Fatal("oversized insert changed the draft")
	}
}

func TestSecretEditorMatchesEditorForGraphemeEditing(t *testing.T) {
	text := "a👍🏽b🇯🇵c1\u20e3d"
	for pos := 0; pos <= len([]rune(text)); pos++ {
		editor := &Editor{buf: []rune(text), pos: pos, prefCol: -1}
		secret := &secretEditor{buf: []byte(text), pos: runeToByte(editor.buf, pos), prefCol: -1}

		editor.Backspace()
		secret.backspace()
		assertSecretEditorState(t, "backspace", editor, secret)

		editor = &Editor{buf: []rune(text), pos: pos, prefCol: -1}
		secret = &secretEditor{buf: []byte(text), pos: runeToByte(editor.buf, pos), prefCol: -1}
		editor.Delete()
		secret.delete()
		assertSecretEditorState(t, "delete", editor, secret)

		editor = &Editor{buf: []rune(text), pos: pos, prefCol: -1}
		secret = &secretEditor{buf: []byte(text), pos: runeToByte(editor.buf, pos), prefCol: -1}
		editor.MoveLeft()
		secret.moveLeft()
		assertSecretEditorState(t, "left", editor, secret)

		editor = &Editor{buf: []rune(text), pos: pos, prefCol: -1}
		secret = &secretEditor{buf: []byte(text), pos: runeToByte(editor.buf, pos), prefCol: -1}
		editor.MoveRight()
		secret.moveRight()
		assertSecretEditorState(t, "right", editor, secret)
	}
}

func assertSecretEditorState(t *testing.T, label string, editor *Editor, secret *secretEditor) {
	t.Helper()
	if !bytes.Equal(secret.buf, []byte(editor.Text())) {
		t.Fatalf("%s text = %q, want %q", label, secret.buf, editor.Text())
	}
	wantPos := runeToByte(editor.buf, editor.pos)
	if secret.pos != wantPos {
		t.Fatalf("%s cursor = %d, want %d", label, secret.pos, wantPos)
	}
}

func TestSecretNavigationMatchesEditorLayout(t *testing.T) {
	cases := []string{
		"short",
		"a\tb\tc",
		"wide 👍🏽 and 1\u20e3 keycap",
		strings.Repeat("x", secretNavigationLimit-1) + "1\u20e3z",
		strings.Repeat("alpha ", 14) + "omega",
		strings.Repeat("x", 73) + "   def ghi",
		strings.Repeat("x", 68) + " abcdefghi\tX",
		strings.Repeat("x", 65) + " abc\t，，，，",
		strings.Repeat("甲乙丙丁", 12) + "（版本）说明，。",
		// U+3000 IDEOGRAPHIC SPACE is trimmed only at a soft cut.
		strings.Repeat("x", 76) + "\u3000乙丙",
		// Nonbreaking spaces stay visible at an emergency cut:
		// NO-BREAK SPACE, FIGURE SPACE, and NARROW NO-BREAK SPACE.
		strings.Repeat("x", 78) + "\u00a0b",
		strings.Repeat("x", 78) + "\u2007b",
		strings.Repeat("x", 78) + "\u202fb",
		"line one\nline two\n",
		strings.Repeat("0123456789", 50),
	}
	for _, text := range cases {
		runes := []rune(text)
		layout := LayoutDraft(runes, 0, secretNavigationWidth)
		secret := layoutSecret([]byte(text), 0)
		if len(layout.Rows) != len(secret.rows) {
			t.Fatalf("%q: rows = %d, want %d", text, len(secret.rows), len(layout.Rows))
		}
		// Row geometry and visual-column mapping do not depend on the cursor.
		for row := range layout.Rows {
			if layout.RowWidth(row) != secret.rows[row].width {
				t.Fatalf("%q row %d: width = %d, want %d", text, row, secret.rows[row].width, layout.RowWidth(row))
			}
			wantStart := runeToByte(runes, layout.RowRunes[row][0])
			wantEnd := runeToByte(runes, layout.RowRunes[row][1])
			if secret.rows[row].start != wantStart || secret.rows[row].end != wantEnd {
				t.Fatalf("%q row %d: range = [%d,%d), want [%d,%d)",
					text, row, secret.rows[row].start, secret.rows[row].end, wantStart, wantEnd)
			}
			for col := 0; col <= layout.RowWidth(row); col++ {
				want := layout.VisualRunePos(runes, row, col)
				got := visualSecretPos([]byte(text), secret.rows[row], col)
				if byteToRune(runes, got) != want {
					t.Fatalf("%q row %d col %d: visual pos = %d, want %d", text, row, col, byteToRune(runes, got), want)
				}
			}
		}
		for pos := 0; pos <= len(runes); pos++ {
			layout := LayoutDraft(runes, pos, secretNavigationWidth)
			secret := layoutSecret([]byte(text), runeToByte(runes, pos))
			if layout.CursorRow != secret.cursorRow || layout.CursorCol != secret.cursorCol {
				t.Fatalf("%q pos %d: cursor = (%d,%d), want (%d,%d)", text, pos,
					secret.cursorRow, secret.cursorCol, layout.CursorRow, layout.CursorCol)
			}

			editor := &Editor{buf: runes, pos: pos, prefCol: -1}
			secretNav := &secretEditor{buf: []byte(text), pos: runeToByte(runes, pos), prefCol: -1}
			for i := 0; i < 3; i++ {
				editor.MoveUp(secretNavigationWidth)
				secretNav.moveUp()
				assertSecretEditorState(t, "up", editor, secretNav)
				if editor.prefCol != secretNav.prefCol {
					t.Fatalf("%q pos %d: up prefCol = %d, want %d", text, pos, secretNav.prefCol, editor.prefCol)
				}
			}
			editor = &Editor{buf: runes, pos: pos, prefCol: -1}
			secretNav = &secretEditor{buf: []byte(text), pos: runeToByte(runes, pos), prefCol: -1}
			for i := 0; i < 3; i++ {
				editor.MoveDown(secretNavigationWidth)
				secretNav.moveDown()
				assertSecretEditorState(t, "down", editor, secretNav)
				if editor.prefCol != secretNav.prefCol {
					t.Fatalf("%q pos %d: down prefCol = %d, want %d", text, pos, secretNav.prefCol, editor.prefCol)
				}
			}
		}
	}
}

func TestSecretWordBackwardMatchesEditor(t *testing.T) {
	cases := []string{
		"hello world",
		"hello   ",
		"foo/bar",
		"a…b",
		"你好，世界",
		"a💥b",
		"a_b c-d",
		"!sk-test-$-!",
	}
	for _, text := range cases {
		runes := []rune(text)
		for pos := 0; pos <= len(runes); pos++ {
			off := runeToByte(runes, pos)
			want := wordBackward(text, off)
			got := secretWordBackward([]byte(text), off)
			if got != want {
				t.Fatalf("%q pos %d: word backward = %d, want %d", text, pos, got, want)
			}
		}
	}
}

func TestSecretEditorDeletionWipesRetiredBytes(t *testing.T) {
	e := newSecretEditor()
	if !e.insert([]byte("a🔑b")) {
		t.Fatal("insert rejected")
	}
	backing := e.buf[:cap(e.buf)]
	e.moveLeft()
	e.backspace()
	if !bytes.Equal(e.buf, []byte("ab")) || e.pos != 1 {
		t.Fatal("deleting a grapheme changed the live prefix or suffix")
	}
	if !bytes.Equal(backing[len(e.buf):], make([]byte, cap(e.buf)-len(e.buf))) {
		t.Fatal("deleted secret bytes survived beyond the live draft")
	}
	e.discard()
}

func TestBeginSecretWipesPreviousDraftAndPaste(t *testing.T) {
	c, _, _ := newTestConsole(80, 24)
	c.BeginSecret()
	if !c.secret.insert([]byte("old-secret")) {
		t.Fatal("insert rejected")
	}
	oldDraft := c.secret.buf[:cap(c.secret.buf)]
	c.appendSecretPaste([]byte("old-paste"))
	oldPaste := c.secretPaste[:cap(c.secretPaste)]
	c.BeginSecret()
	for i, b := range oldDraft {
		if b != 0 {
			t.Fatalf("prior draft byte %d survived BeginSecret", i)
		}
	}
	for i, b := range oldPaste {
		if b != 0 {
			t.Fatalf("prior paste byte %d survived BeginSecret", i)
		}
	}
	if len(c.secret.buf) != 0 || len(c.secretPaste) != 0 {
		t.Fatal("BeginSecret retained prior secret state")
	}
}

func TestDiscardSecretWipesOwnedDraft(t *testing.T) {
	c, _, _ := newTestConsole(80, 24)
	c.BeginSecret()
	if !c.secret.insert([]byte("discard-secret")) {
		t.Fatal("insert rejected")
	}
	old := c.secret.buf[:cap(c.secret.buf)]
	c.DiscardSecret()
	for i, b := range old {
		if b != 0 {
			t.Fatalf("draft byte %d survived DiscardSecret", i)
		}
	}
	if c.secret != nil {
		t.Fatal("DiscardSecret left a secret editor")
	}
}

func TestSecretInsertionSnapsAcrossJoinedGraphemes(t *testing.T) {
	const flag = "\U0001F1EF\U0001F1F5" // regional indicators J and P
	editor := &Editor{buf: []rune("\U0001F1EFx\U0001F1F5"), pos: 2, prefCol: -1}
	secret := &secretEditor{buf: []byte("\U0001F1EFx\U0001F1F5"), pos: runeToByte(editor.buf, editor.pos), prefCol: -1}
	editor.Backspace()
	secret.backspace()
	if !editor.Insert("!") {
		t.Fatal("legacy insert rejected")
	}
	if !secret.insert([]byte("!")) {
		t.Fatal("secret insert rejected")
	}
	if !bytes.Equal(secret.buf, []byte(flag+"!")) {
		t.Fatalf("joined flag insertion = %q", secret.buf)
	}
	assertSecretEditorState(t, "joined flag", editor, secret)

	editor = &Editor{buf: []rune("e\u0301"), pos: 1, prefCol: -1}
	secret = &secretEditor{buf: []byte("e\u0301"), pos: runeToByte(editor.buf, editor.pos), prefCol: -1}
	if !editor.Insert("x") || !secret.insert([]byte("x")) {
		t.Fatal("combining insertion rejected")
	}
	assertSecretEditorState(t, "combining snap", editor, secret)
}

func TestSecretNavigationWrapBoundary(t *testing.T) {
	atLimit := bytes.Repeat([]byte("x"), secretNavigationLimit)
	if got := len(layoutSecret(atLimit, 0).rows); got != 1 {
		t.Fatalf("79-cell row count = %d, want 1", got)
	}
	overLimit := bytes.Repeat([]byte("x"), secretNavigationLimit+1)
	if got := len(layoutSecret(overLimit, 0).rows); got != 2 {
		t.Fatalf("80-cell row count = %d, want 2", got)
	}
}

func TestSecretDocumentKeysMatchEditor(t *testing.T) {
	const text = "first\nsecond line\nthird"
	keys := [][]byte{
		[]byte("\x1b[1;5H"),
		[]byte("\x1b[1;5F"),
	}
	for _, key := range keys {
		editor := &Editor{buf: []rune(text), pos: 8, prefCol: -1}
		secret := &secretEditor{buf: []byte(text), pos: runeToByte(editor.buf, editor.pos), prefCol: -1}
		editor.HandleKey(key, false)
		secret.handleKey(key)
		assertSecretEditorState(t, string(key), editor, secret)
	}
}
