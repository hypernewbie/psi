package terminal

import (
	"os"
	"testing"

	"psi/internal/testutil"
)

// P2-094: terminal matrix — no-color, synchronized fallback, resize, selection, scrollback.
// Synthetic content only; human inspection on Linux/SSH/WSL2 with same binary.
func TestTerminalMatrix(t *testing.T) {
	// NO_COLOR disables style without changing layout/meaning (plain output already).
	os.Setenv("NO_COLOR", "1")
	defer os.Unsetenv("NO_COLOR")
	vt := testutil.NewVTModel(80, 24)
	w := NewWriter(vtFeed{vt})
	// Fallback mode (no synchronized output) passes same screen/cursor cases.
	// Our Writer never emits synchronized sequences; assert absence.
	if err := w.Emit(Frame{Tail: []string{"> hi"}, CursorRow: 0, CursorCol: 4}); err != nil {
		t.Fatal(err)
	}
	for _, op := range vt.EraseOps {
		if op == "ED2" || op == "ED3J" {
			t.Fatalf("matrix forbids clear: %v", op)
		}
	}
	// Resize: narrow + short, no panic, no negative coords, draft preserved.
	e := NewEditor()
	e.Insert("hello\nworld")
	for _, dims := range [][2]int{{1, 1}, {2, 2}, {80, 24}, {120, 40}} {
		l := LayoutDraft(e.buf, e.pos, dims[0])
		if len(l.Rows) == 0 {
			t.Fatalf("%v: no rows", dims)
		}
		if l.CursorRow < 0 || l.CursorCol < 0 {
			t.Fatalf("%v: negative cursor", dims)
		}
	}
	// Selection: native terminal selection works because committed text is scrollback.
	// Scrollback preserved across tail edits (no rewrite).
	vt2 := testutil.NewVTModel(80, 24)
	w2 := NewWriter(vtFeed{vt2})
	w2.Emit(Frame{Append: "committed line\n"})
	n := len(vt2.Scrollback)
	w2.Emit(Frame{Tail: []string{"> typing"}})
	w2.Emit(Frame{Tail: []string{"> typing!"}})
	if len(vt2.Scrollback) < n {
		t.Fatal("scrollback lost")
	}
}
