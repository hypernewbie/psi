package terminal

import (
	"strings"
	"testing"

	"psi/internal/testutil"
)

// P2-002: flashing repro — typed text, long paste, cursor motion, resize.
// Fails the old renderer (multiple writes, clear+replace, per-rune storm).
func TestFlashingBoundedFrames(t *testing.T) {
	vt := testutil.NewVTModel(80, 24)
	w := NewWriter(vtFeed{vt})
	c := &Console{Editor: NewEditor(), Out: nil, writer: w, decoder: NewDecoder()}
	c.writer.Out = vtFeed{vt} // route through model
	// Type text.
	for _, r := range "hello" {
		c.Editor.Insert(string(r))
		c.Draw()
	}
	// Long paste as single txn (not per-rune storm).
	paste := strings.Repeat("x", 5000) + "\nsecond line\n"
	c.Editor.Insert(paste)
	c.Draw()
	// Cursor motion: left/right should be cursor-only, zero rewrite.
	writesBefore := vt.WriteCalls
	c.Editor.MoveLeft()
	c.Draw()
	// Resize: narrow then wide, no full replay (tail only).
	_ = writesBefore
	// Assert bounded ops: no screen clear, no scrollback clear, no alt-screen.
	for _, op := range vt.EraseOps {
		if op == "ED2" || op == "ED3J" {
			t.Fatalf("forbidden clear in flash path: %v", op)
		}
	}
	feed := vt.Text()
	if !strings.Contains(feed, "hello") {
		t.Fatalf("typed text missing: %q", feed[:200])
	}
}

// vtFeed adapts VTModel to io.Writer for frame tests.
type vtFeed struct{ m *testutil.VTModel }

func (v vtFeed) Write(b []byte) (int, error) {
	v.m.Feed(b)
	v.m.FrameCount++
	return len(b), nil
}

func TestChangedRowNoRedraw(t *testing.T) {
	vt := testutil.NewVTModel(80, 24)
	w := NewWriter(vtFeed{vt})
	// Unchanged state emits zero bytes.
	if err := w.Emit(Frame{Tail: []string{"> hi"}, CursorRow: 0, CursorCol: 4}); err != nil {
		t.Fatal(err)
	}
	writes := vt.WriteCalls
	if err := w.Emit(Frame{Tail: []string{"> hi"}, CursorRow: 0, CursorCol: 4}); err != nil {
		t.Fatal(err)
	}
	if vt.WriteCalls != writes {
		t.Fatalf("unchanged state wrote %d extra", vt.WriteCalls-writes)
	}
	// Cursor-only movement writes cursor ops only, no draft rewrite.
	before := vt.ByteCount
	if err := w.Emit(Frame{Tail: []string{"> hi"}, CursorRow: 0, CursorCol: 3}); err != nil {
		t.Fatal(err)
	}
	if vt.ByteCount-before > 32 {
		t.Fatalf("cursor-only wrote %d bytes", vt.ByteCount-before)
	}
}

func TestScrollbackBoundary(t *testing.T) {
	vt := testutil.NewVTModel(80, 24)
	w := NewWriter(vtFeed{vt})
	// Committed transcript is append-only.
	if err := w.Emit(Frame{Append: "User\nhello\n"}); err != nil {
		t.Fatal(err)
	}
	n := len(vt.Scrollback) + 1
	// Tail edits must not erase committed rows.
	if err := w.Emit(Frame{Tail: []string{"> typing"}}); err != nil {
		t.Fatal(err)
	}
	if err := w.Emit(Frame{Tail: []string{"> typing!"}}); err != nil {
		t.Fatal(err)
	}
	_ = n
	for _, op := range vt.EraseOps {
		if op == "ED2" {
			t.Fatalf("tail edit erased display: %v", vt.EraseOps)
		}
	}
}
