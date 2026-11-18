package terminal

import (
	"reflect"
	"strings"
	"testing"
)

func assertLayoutRows(t *testing.T, l Layout, want []string) {
	t.Helper()
	if !reflect.DeepEqual(l.Rows, want) {
		t.Fatalf("rows = %#v, want %#v", l.Rows, want)
	}
}

func TestLayoutDraftWordWrap(t *testing.T) {
	source := "this is a draft message where as the word gets chopped in half"
	buf := []rune(source)
	l := LayoutDraft(buf, len(buf), 40)
	assertLayoutRows(t, l, []string{
		"this is a draft message where as the",
		"word gets chopped in half",
	})
	if string(buf) != source {
		t.Fatalf("source changed: %q", string(buf))
	}
	if l.CursorRow != 1 || l.CursorCol != 25 {
		t.Fatalf("cursor = (%d,%d), want (1,25)", l.CursorRow, l.CursorCol)
	}
}

func TestLayoutDraftChineseWrap(t *testing.T) {
	source := "这是一个很长的中文句子，用来测试换行是否符合中文标点规则。我们需要确保标点符号不会出现在行首。"
	buf := []rune(source)
	l := LayoutDraft(buf, len(buf), 24)
	assertLayoutRows(t, l, []string{
		"这是一个很长的中文句",
		"子，用来测试换行是否符",
		"合中文标点规则。我们需",
		"要确保标点符号不会出现",
		"在行首。",
	})
	if string(buf) != source {
		t.Fatalf("source changed: %q", string(buf))
	}
	if l.CursorRow != 4 || l.CursorCol != 8 {
		t.Fatalf("cursor = (%d,%d), want (4,8)", l.CursorRow, l.CursorCol)
	}
}

func TestLayoutDraftCursorMapping(t *testing.T) {
	buf := []rune("abcdef ghij")
	for _, tc := range []struct{ pos, row, col int }{
		{8, 1, 1},
		{9, 1, 2},
		{10, 1, 3},
	} {
		l := LayoutDraft(buf, tc.pos, 11)
		if l.CursorRow != tc.row || l.CursorCol != tc.col {
			t.Fatalf("pos %d cursor = (%d,%d), want (%d,%d)", tc.pos, l.CursorRow, l.CursorCol, tc.row, tc.col)
		}
		if got := l.VisualRunePos(buf, tc.row, tc.col); got != tc.pos {
			t.Fatalf("pos %d round trip = %d", tc.pos, got)
		}
	}

	// The position immediately after the breaking space belongs to the next row.
	l := LayoutDraft(buf, 7, 11)
	assertLayoutRows(t, l, []string{"abcdef", "ghij"})
	if l.CursorRow != 1 || l.CursorCol != 0 {
		t.Fatalf("position after space = (%d,%d), want (1,0)", l.CursorRow, l.CursorCol)
	}

	// A cursor inside a trimmed run keeps its cell column past RowWidth.
	hidden := []rune("abc   de fg")
	l = LayoutDraft(hidden, 4, 8)
	if l.CursorRow != 0 || l.CursorCol != 4 {
		t.Fatalf("hidden-space cursor = (%d,%d), want (0,4)", l.CursorRow, l.CursorCol)
	}
	if l.RowWidth(0) != 3 {
		t.Fatalf("row 0 width = %d, want 3", l.RowWidth(0))
	}
	if got := l.VisualRunePos(hidden, 0, 4); got != 4 {
		t.Fatalf("hidden-space round trip = %d, want 4", got)
	}
	assertLayoutRows(t, l, []string{"abc", "de fg"})
	if !reflect.DeepEqual(l.RowRunes, [][2]int{{0, 6}, {6, 11}}) {
		t.Fatalf("ranges = %v", l.RowRunes)
	}
}

func assertLayoutSourceOwnership(t *testing.T, l Layout, buf []rune) {
	t.Helper()
	prevEnd := 0
	for i, r := range l.RowRunes {
		start, end := r[0], r[1]
		if start < 0 || end < start || end > len(buf) {
			t.Fatalf("row %d range [%d,%d) outside %d runes", i, start, end, len(buf))
		}
		if i == 0 {
			if start != 0 {
				t.Fatalf("first row starts at %d, want 0", start)
			}
		} else if start != prevEnd {
			if start != prevEnd+1 || prevEnd >= len(buf) || buf[prevEnd] != '\n' {
				t.Fatalf("row %d starts at %d after %d without a hard newline", i, start, prevEnd)
			}
		}
		prevEnd = end
	}
	if prevEnd != len(buf) {
		t.Fatalf("last row ends at %d, want %d", prevEnd, len(buf))
	}
}

func TestLayoutDraftEveryCursorPosition(t *testing.T) {
	for _, tc := range []struct {
		source string
		width  int
	}{
		{"abcdef ghij", 11},
		{"abc   de fg", 8},
		{"one  two   three four", 7},
		{"这是一个很长的中文句子，用来测试换行是否符合中文标点规则。", 10},
		{"alpha beta\n\ngamma delta epsilon", 8},
	} {
		buf := []rune(tc.source)
		assertLayoutSourceOwnership(t, LayoutDraft(buf, len(buf), tc.width), buf)
		for pos := 0; pos <= len(buf); pos++ {
			l := LayoutDraft(buf, pos, tc.width)
			if l.CursorRow < 0 || l.CursorRow >= len(l.Rows) {
				t.Fatalf("%q pos %d: cursor row %d outside %d rows", tc.source, pos, l.CursorRow, len(l.Rows))
			}
			start, end := l.RowRunes[l.CursorRow][0], l.RowRunes[l.CursorRow][1]
			if pos < start || pos > end {
				t.Fatalf("%q pos %d: not in cursor row range [%d,%d]", tc.source, pos, start, end)
			}
			if got := l.VisualRunePos(buf, l.CursorRow, l.CursorCol); got != pos {
				t.Fatalf("%q pos %d: round trip = %d at (%d,%d)", tc.source, pos, got, l.CursorRow, l.CursorCol)
			}
		}
	}
}

func TestLayoutDraftComposedCursorPastTrimmedRow(t *testing.T) {
	buf := []rune("abc   de fg")
	l := LayoutDraft(buf, 4, 8)
	if l.CursorCol != 4 || l.RowWidth(0) != 3 {
		t.Fatalf("layout cursor = %d, row width = %d", l.CursorCol, l.RowWidth(0))
	}
	view := ComposeEditorView(8, 10, buf, 4, PresentationSnapshot{NoColor: true}, "")
	if view.CursorCol != 4 {
		t.Fatalf("composed cursor column = %d, want 4", view.CursorCol)
	}
	if view.CursorRow <= 0 || view.CursorRow >= len(view.Rows) {
		t.Fatalf("composed cursor row = %d outside %d rows", view.CursorRow, len(view.Rows))
	}
	row := view.Rows[view.CursorRow]
	if row != "abc" {
		t.Fatalf("composed cursor row = %q, want %q", row, "abc")
	}
	if view.CursorCol <= CellWidth(row) {
		t.Fatalf("composed cursor column %d is not past visible width %d", view.CursorCol, CellWidth(row))
	}
}

func TestLayoutDraftBoundaryWrap(t *testing.T) {
	latin := []rune("this is a draft message where as the word gets chopped in half")
	assertLayoutRows(t, LayoutDraft(latin, len(latin), 24), []string{
		"this is a draft",
		"message where as the",
		"word gets chopped in",
		"half",
	})
	assertLayoutRows(t, LayoutDraft(latin, len(latin), 80), []string{string(latin)})

	// Exact fit does not add a row.
	assertLayoutRows(t, LayoutDraft([]rune("abcde"), 5, 6), []string{"abcde"})

	// A word wider than the row still makes progress at grapheme granularity.
	long := []rune("supercalifragilisticexpialidocious")
	longLayout := LayoutDraft(long, len(long), 10)
	if len(longLayout.Rows) < 2 {
		t.Fatalf("oversized word rows = %v", longLayout.Rows)
	}
	if got := strings.Join(longLayout.Rows, ""); got != string(long) {
		t.Fatalf("oversized word lost source: %q", got)
	}
	for _, row := range longLayout.Rows {
		if CellWidth(row) > 9 {
			t.Fatalf("oversized row %q exceeds limit 9", row)
		}
	}

	// Hyphen and slash are legal break points and stay in the source rows.
	for _, tc := range []struct {
		source string
		width  int
		want   []string
	}{
		{"well-known", 8, []string{"well-", "known"}},
		{"https://example.com/path", 12, []string{"https://", "example.com", "/path"}},
	} {
		l := LayoutDraft([]rune(tc.source), len(tc.source), tc.width)
		assertLayoutRows(t, l, tc.want)
		if got := strings.Join(l.Rows, ""); got != tc.source {
			t.Fatalf("%q rows lost source: %q", tc.source, got)
		}
	}
}

func TestLayoutDraftChinesePunctuation(t *testing.T) {
	source := "这是一个很长的中文句子，用来测试换行是否符合中文标点规则。我们需要确保标点符号不会出现在行首。"
	closing := "，。、；：！？）〕」』”’"
	opening := "（〔「『“‘"
	for _, width := range []int{20, 21, 30} {
		l := LayoutDraft([]rune(source), len(source), width)
		if got := strings.Join(l.Rows, ""); got != source {
			t.Fatalf("width %d lost source: %q", width, got)
		}
		for i, row := range l.Rows {
			runes := []rune(row)
			if len(runes) == 0 {
				continue
			}
			if i > 0 && strings.ContainsRune(closing, runes[0]) {
				t.Fatalf("width %d row %d starts with closing punctuation: %v", width, i, l.Rows)
			}
			if strings.ContainsRune(opening, runes[len(runes)-1]) {
				t.Fatalf("width %d row %d ends with opening punctuation: %v", width, i, l.Rows)
			}
			if CellWidth(row) > width-1 {
				t.Fatalf("width %d row %q exceeds limit", width, row)
			}
		}
	}

	brackets := "「Linux」文档，请参阅（版本）说明。"
	for _, width := range []int{14, 16} {
		l := LayoutDraft([]rune(brackets), len(brackets), width)
		if got := strings.Join(l.Rows, ""); got != brackets {
			t.Fatalf("width %d brackets lost source: %q", width, got)
		}
		for i, row := range l.Rows {
			runes := []rune(row)
			if len(runes) == 0 {
				continue
			}
			if i > 0 && strings.ContainsRune("」）", runes[0]) {
				t.Fatalf("width %d row %d starts with closing bracket: %v", width, i, l.Rows)
			}
			if strings.ContainsRune("「（", runes[len(runes)-1]) {
				t.Fatalf("width %d row %d ends with opening bracket: %v", width, i, l.Rows)
			}
		}
	}

	pairs := "甲乙……丙丁——戊己"
	for _, width := range []int{10, 12} {
		l := LayoutDraft([]rune(pairs), len(pairs), width)
		if got := strings.Join(l.Rows, ""); got != pairs {
			t.Fatalf("width %d pairs lost source: %q", width, got)
		}
		for _, row := range l.Rows {
			if strings.Count(row, "…")%2 != 0 {
				t.Fatalf("width %d split an ellipsis pair: %v", width, l.Rows)
			}
			if strings.Count(row, "—")%2 != 0 {
				t.Fatalf("width %d split an em dash pair: %v", width, l.Rows)
			}
		}
	}
}

func TestLayoutDraftTabCandidates(t *testing.T) {
	for _, tc := range []struct {
		source string
		width  int
		want   []string
	}{
		{"abc abcdefghi\tX", 16, []string{"abc abcdefghi", "        X"}},
		{"abcdefgh abc\t。", 18, []string{"abcdefgh", "abc     。"}},
		{"abcdefgh abc\t，。", 20, []string{"abcdefgh", "abc     ，。"}},
		{"a abc\t。", 10, []string{"a abc", "。"}},
		{"a\tb", 8, []string{"a", "", "b"}},
	} {
		l := LayoutDraft([]rune(tc.source), len(tc.source), tc.width)
		assertLayoutRows(t, l, tc.want)
	}

	// With two closing marks, the cursor after the tab is captured before
	// the second mark triggers backtracking. Its new column is 8, not the
	// old column 16 minus the 9-cell prefix. Also check both sides of it.
	for _, tc := range []struct {
		source               string
		pos, width, row, col int
	}{
		{"abcdefgh abc\t。", 13, 18, 1, 8},
		{"abcdefgh abc\t，。", 10, 20, 1, 1},
		{"abcdefgh abc\t，。", 13, 20, 1, 8},
		{"abcdefgh abc\t，。", 14, 20, 1, 10},
	} {
		buf := []rune(tc.source)
		l := LayoutDraft(buf, tc.pos, tc.width)
		if l.CursorRow != tc.row || l.CursorCol != tc.col {
			t.Fatalf("%q pos %d cursor = (%d,%d), want (%d,%d)", tc.source, tc.pos, l.CursorRow, l.CursorCol, tc.row, tc.col)
		}
		if got := l.VisualRunePos(buf, l.CursorRow, l.CursorCol); got != tc.pos {
			t.Fatalf("%q pos %d round trip = %d", tc.source, tc.pos, got)
		}
	}
}

func TestLayoutDraftSpacePreservation(t *testing.T) {
	for _, tc := range []struct {
		source string
		width  int
		want   []string
	}{
		{"a  b", 80, []string{"a  b"}},
		{"abc   ", 80, []string{"abc   "}},
		{"abc  \ndef  ", 80, []string{"abc  ", "def  "}},
	} {
		buf := []rune(tc.source)
		l := LayoutDraft(buf, len(buf), tc.width)
		assertLayoutRows(t, l, tc.want)
		if string(buf) != tc.source {
			t.Fatalf("%q source changed: %q", tc.source, string(buf))
		}
	}
}

func TestLayoutDraftUnicodeSpaces(t *testing.T) {
	source := "甲\u3000乙丙"
	buf := []rune(source)
	l := LayoutDraft(buf, len(buf), 6)
	assertLayoutRows(t, l, []string{"甲", "乙丙"})
	if !reflect.DeepEqual(l.RowRunes, [][2]int{{0, 2}, {2, 4}}) {
		t.Fatalf("ranges = %v", l.RowRunes)
	}
	if string(buf) != source {
		t.Fatalf("source changed: %q", string(buf))
	}
	for _, tc := range []struct{ pos, row, col int }{
		{1, 0, 2},
		{2, 1, 0},
	} {
		cursor := LayoutDraft(buf, tc.pos, 6)
		if cursor.CursorRow != tc.row || cursor.CursorCol != tc.col {
			t.Fatalf("pos %d cursor = (%d,%d), want (%d,%d)", tc.pos, cursor.CursorRow, cursor.CursorCol, tc.row, tc.col)
		}
	}

	// Unicode spaces at hard and final ends stay in the source-owned rows.
	hard := "甲\u3000\n乙\u3000"
	assertLayoutRows(t, LayoutDraft([]rune(hard), len([]rune(hard)), 6), []string{"甲\u3000", "乙\u3000"})

	// Nonbreaking spaces survive an emergency cut.
	for _, space := range []string{"\u00a0", "\u2007", "\u202f"} {
		source := "a" + space + "b"
		l := LayoutDraft([]rune(source), 3, 3)
		assertLayoutRows(t, l, []string{"a" + space, "b"})
		if got := strings.Join(l.Rows, ""); got != source {
			t.Fatalf("%U lost source: %q", []rune(space)[0], got)
		}
	}
}

func assertRowGraphemes(t *testing.T, l Layout, source string) {
	t.Helper()
	want := Graphemes(source)
	next := 0
	for rowIndex, row := range l.Rows {
		for _, g := range Graphemes(row) {
			if next >= len(want) || g != want[next] {
				t.Fatalf("row %d grapheme %q does not continue source at %d: %v", rowIndex, g, next, l.Rows)
			}
			next++
		}
	}
	if next != len(want) {
		t.Fatalf("rows consumed %d of %d graphemes: %v", next, len(want), l.Rows)
	}
}

func TestLayoutDraftGraphemeAtomicity(t *testing.T) {
	for _, tc := range []struct {
		source string
		width  int
	}{
		{"e\u0301e\u0301e\u0301", 2},
		{"👨‍👩‍👧‍👦👨‍👩‍👧‍👦", 4},
		{"🇩🇪🇫🇷", 4},
	} {
		l := LayoutDraft([]rune(tc.source), len([]rune(tc.source)), tc.width)
		if got := strings.Join(l.Rows, ""); got != tc.source {
			t.Fatalf("%q rows lost source: %q", tc.source, got)
		}
		assertRowGraphemes(t, l, tc.source)
	}

	assertLayoutRows(t, LayoutDraft([]rune("a1️⃣b"), 4, 4), []string{"a1️⃣", "b"})
}

func TestLayoutDraftEmptyAndTinyWidths(t *testing.T) {
	l := LayoutDraft(nil, 0, 10)
	assertLayoutRows(t, l, []string{""})
	if l.CursorRow != 0 || l.CursorCol != 0 || !reflect.DeepEqual(l.RowRunes, [][2]int{{0, 0}}) {
		t.Fatalf("empty layout: %+v", l)
	}

	buf := []rune("abc\n")
	l = LayoutDraft(buf, len(buf), 10)
	assertLayoutRows(t, l, []string{"abc", ""})
	if l.CursorRow != 1 || l.CursorCol != 0 {
		t.Fatalf("trailing newline cursor = (%d,%d), want (1,0)", l.CursorRow, l.CursorCol)
	}
	if !reflect.DeepEqual(l.RowRunes, [][2]int{{0, 3}, {4, 4}}) {
		t.Fatalf("trailing newline ranges = %v", l.RowRunes)
	}

	buf = []rune("\n\n")
	l = LayoutDraft(buf, len(buf), 10)
	assertLayoutRows(t, l, []string{"", "", ""})
	if l.CursorRow != 2 || l.CursorCol != 0 {
		t.Fatalf("blank lines cursor = (%d,%d), want (2,0)", l.CursorRow, l.CursorCol)
	}
	if !reflect.DeepEqual(l.RowRunes, [][2]int{{0, 0}, {1, 1}, {2, 2}}) {
		t.Fatalf("blank lines ranges = %v", l.RowRunes)
	}

	for _, width := range []int{0, 1, 2} {
		source := "a界b"
		buf := []rune(source)
		l := LayoutDraft(buf, len(buf), width)
		if l.CursorRow < 0 || l.CursorRow >= len(l.Rows) {
			t.Fatalf("width %d cursor row %d outside %d rows", width, l.CursorRow, len(l.Rows))
		}
		if l.CursorCol < 0 || l.CursorCol > max(0, width-1) {
			t.Fatalf("width %d cursor column %d", width, l.CursorCol)
		}
		if string(buf) != source {
			t.Fatalf("width %d source changed: %q", width, string(buf))
		}
		if !strings.Contains(strings.Join(l.Rows, ""), "?") {
			t.Fatalf("width %d lost the wide-cluster placeholder: %v", width, l.Rows)
		}
		assertLayoutSourceOwnership(t, l, buf)
	}

	buf = []rune("abcdef")
	l = LayoutDraft(buf, -5, 80)
	if l.CursorRow != 0 || l.CursorCol != 0 {
		t.Fatalf("negative cursor = (%d,%d), want (0,0)", l.CursorRow, l.CursorCol)
	}
	l = LayoutDraft(buf, 99, 80)
	if l.CursorRow != 0 || l.CursorCol != 6 {
		t.Fatalf("oversized cursor = (%d,%d), want (0,6)", l.CursorRow, l.CursorCol)
	}
}

func TestLayoutDraftInteriorCursorStaysBounded(t *testing.T) {
	// Position 3 is inside the b+combining-mark grapheme, so it is not a
	// representable cursor boundary and must not produce a negative column.
	buf := []rune("a b\u0301c")
	l := LayoutDraft(buf, 3, 4)
	if l.CursorRow < 0 || l.CursorRow >= len(l.Rows) || l.CursorCol < 0 || l.CursorCol > 3 {
		t.Fatalf("interior cursor = (%d,%d) rows=%v", l.CursorRow, l.CursorCol, l.Rows)
	}
}
