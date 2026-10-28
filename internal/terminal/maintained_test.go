package terminal

import (
	"fmt"
	"strings"
	"testing"

	"github.com/charmbracelet/x/vt"
)

type checkedTerminal struct {
	t      *testing.T
	model  *vt.Emulator
	frames int
}

func (w *checkedTerminal) Write(p []byte) (int, error) {
	n, err := w.model.Write(p)
	w.frames++
	// Check each completed write, not only the final screenshot. The writer is
	// allowed to change its tail but cannot erase the committed marker.
	if !strings.Contains(w.model.String(), "COMMITTED_SENTINEL") && w.model.ScrollbackLen() == 0 {
		w.t.Fatal("committed conversation disappeared in an intermediate frame")
	}
	if w.model.IsAltScreen() {
		w.t.Fatal("application entered alternate screen")
	}
	return n, err
}
func TestMaintainedVTIntermediateFramesAndScrollback(t *testing.T) {
	model := vt.NewEmulator(40, 8)
	defer model.Close()
	model.WriteString("COMMITTED_SENTINEL\r\n")
	out := &checkedTerminal{t: t, model: model}
	c := &Console{Editor: NewEditor(), Out: out, writer: NewWriter(out), decoder: NewDecoder()}
	c.Draw()
	c.Editor.Insert("hello e\u0301 界 🌍")
	c.Draw()
	c.Editor.MoveLeft()
	c.Editor.Backspace()
	c.Draw()
	c.Editor.Insert("\nsecond\nthird")
	c.Draw()
	c.Editor.DocStart()
	c.Draw()
	c.Handle(Input{Key: []byte{'\r'}}, false)
	c.Draw()
	for i := 0; i < 30; i++ {
		fmt.Fprintf(RawOutput{W: c}, "committed line %02d\n", i)
		c.Draw()
	}
	if model.ScrollbackLen() == 0 {
		t.Fatal("no native scrollback")
	}
	found := false
	for y := 0; y < model.ScrollbackLen(); y++ {
		var line strings.Builder
		for x := 0; x < 40; x++ {
			if cell := model.ScrollbackCellAt(x, y); cell != nil {
				line.WriteString(cell.Content)
			}
		}
		if strings.Contains(line.String(), "COMMITTED_SENTINEL") {
			found = true
		}
	}
	if !found {
		t.Fatal("committed sentinel lost from scrollback")
	}
	model.Resize(20, 5)
	c.Draw()
	model.Resize(40, 8)
	c.Draw()
	if out.frames > 100 {
		t.Fatal("unexpected frame storm")
	}
}

func TestMaintainedVTResizeKeepsActiveDraftAndCommittedHistory(t *testing.T) {
	model := vt.NewEmulator(80, 24)
	defer model.Close()
	width, height := 80, 24
	c := &Console{Editor: NewEditor(), Out: model, writer: NewWriter(model), SizeFunc: func() (int, int) { return width, height }}
	c.SetPresentation(PresentationSnapshot{WorkingDirectory: "/work", Model: "model", NoColor: true})
	fmt.Fprintln(RawOutput{W: c}, "COMMITTED_SENTINEL")
	c.Editor.Insert("resize-draft")
	c.Draw()
	for _, size := range [][2]int{{120, 40}, {80, 24}, {60, 16}, {40, 12}, {20, 6}, {80, 24}} {
		width, height = size[0], size[1]
		model.Resize(width, height)
		c.Draw()
		if !strings.Contains(model.String(), "resize-draft") {
			t.Fatalf("resize to %dx%d hid the active draft: %q", width, height, model.String())
		}
		sentinel := strings.Contains(model.String(), "COMMITTED_SENTINEL")
		for y := 0; y < model.ScrollbackLen() && !sentinel; y++ {
			var line strings.Builder
			for x := 0; x < width; x++ {
				if cell := model.ScrollbackCellAt(x, y); cell != nil {
					line.WriteString(cell.Content)
				}
			}
			sentinel = strings.Contains(line.String(), "COMMITTED_SENTINEL")
		}
		if !sentinel {
			t.Fatalf("resize to %dx%d lost the committed sentinel", width, height)
		}
	}
}

func TestMaintainedVTOverwriteEraseWrapAndCursor(t *testing.T) {
	model := vt.NewEmulator(8, 3)
	defer model.Close()
	model.WriteString("abcde\rXY\x1b[K")
	if !strings.HasPrefix(model.String(), "XY") {
		t.Fatal(model.String())
	}
	model.WriteString("\x1b[2J\x1b[H123456789")
	if model.CellAt(0, 1).Content != "9" {
		t.Fatal("reference VT did not wrap")
	}
	pos := model.CursorPosition()
	if pos.X != 1 || pos.Y != 1 {
		t.Fatalf("cursor: %+v", pos)
	}
	model.WriteString("\x1b[?25l")
	if !model.CursorHidden() {
		t.Fatal("hide cursor ignored")
	}
	model.WriteString("\x1b[?25h")
	if model.CursorHidden() {
		t.Fatal("show cursor ignored")
	}
}
