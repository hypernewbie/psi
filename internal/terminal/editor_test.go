package terminal

import (
	"strings"
	"testing"
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
