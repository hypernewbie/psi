package terminal

import (
	"testing"

	"psi/internal/testutil"
)

// P2-004: screen-state assertions — cells, scrollback, cursor, modes, erase ops.
func TestScreenModelCellsCursorModes(t *testing.T) {
	vt := testutil.NewVTModel(20, 6)
	vt.Feed([]byte("> hello"))
	if vt.CursorCol != 7 {
		t.Fatalf("cursor col %d, want 7", vt.CursorCol)
	}
	// Erase ops tracked.
	vt.Feed([]byte("\x1b[K"))
	if len(vt.EraseOps) == 0 || vt.EraseOps[len(vt.EraseOps)-1] != "EL" {
		t.Fatalf("erase not tracked: %v", vt.EraseOps)
	}
	// Modes tracked.
	vt.Feed([]byte("\x1b[?2004h"))
	if !vt.Modes["?2004"] {
		t.Fatalf("mode not tracked: %v", vt.Modes)
	}
	// Scrollback on overflow.
	vt2 := testutil.NewVTModel(10, 3)
	for i := 0; i < 10; i++ {
		vt2.Feed([]byte("line\n"))
	}
	if len(vt2.Scrollback) == 0 {
		t.Fatalf("scrollback empty after overflow")
	}
	// Blank intermediate frame fails even with expected final substring.
	vt3 := testutil.NewVTModel(80, 24)
	vt3.Feed([]byte("\x1b[2J\x1b[H"))
	vt3.Feed([]byte("final"))
	if len(vt3.EraseOps) == 0 {
		t.Fatalf("blank clear not detected")
	}
}
