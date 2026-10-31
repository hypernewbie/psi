package terminal

import (
	"bytes"
	"strings"
	"testing"

	"psi/internal/testutil"
	"psi/internal/testutil/leakcheck"
)

func newTestConsole(w, h int) (*Console, *testutil.VTModel, *bytes.Buffer) {
	vt := testutil.NewVTModel(w, h)
	var buf bytes.Buffer
	c := &Console{
		Editor:   NewEditor(),
		Out:      &buf,
		writer:   NewWriter(vtFeed{vt}),
		decoder:  NewDecoder(),
		SizeFunc: func() (int, int) { return w, h },
	}
	return c, vt, &buf
}

func TestConsolePresentationAndDeferDraw(t *testing.T) {
	defer leakcheck.Verify(t)
	c, _, _ := newTestConsole(80, 24)

	snap := PresentationSnapshot{
		WorkingDirectory: "/home/user/project",
		Model:            "test-model",
		NoColor:          true,
	}
	c.SetPresentation(snap)
	got := c.GetPresentation()
	if got.WorkingDirectory != snap.WorkingDirectory || got.Model != snap.Model || !got.NoColor {
		t.Fatalf("presentation mismatch: got %+v, want %+v", got, snap)
	}

	// DeferDraw toggles
	c.SetDeferDraw(true)
	if !c.deferDraw {
		t.Fatal("SetDeferDraw(true) failed")
	}
	c.SetDeferDraw(false)
	if c.deferDraw {
		t.Fatal("SetDeferDraw(false) failed")
	}

	// SetPresentation updates markdown stream and completion items if present
	c.markdownStream = NewMarkdownStream(80, false)
	c.completionItems = []string{"/help", "/model"}
	c.completionSelected = 0
	c.SetPresentation(snap)
	if !c.markdownStream.noColor {
		t.Fatal("markdownStream noColor should be true")
	}
	if len(c.completionRows) == 0 {
		t.Fatal("completion rows should be rendered")
	}
}

func TestConsoleHeldInputs(t *testing.T) {
	defer leakcheck.Verify(t)
	c, _, _ := newTestConsole(80, 24)

	if len(c.TakeHeldInputs()) != 0 {
		t.Fatal("expected empty held inputs initially")
	}

	i1 := Input{Key: []byte("first")}
	i2 := Input{Key: []byte("second")}
	c.HoldInput(i1)
	c.HoldInput(i2)

	held := c.TakeHeldInputs()
	if len(held) != 2 || string(held[0].Key) != "first" || string(held[1].Key) != "second" {
		t.Fatalf("unexpected held inputs: %+v", held)
	}

	// TakeHeldInputs must clear the queue
	if len(c.TakeHeldInputs()) != 0 {
		t.Fatal("expected held inputs to be cleared after take")
	}
}

func TestConsolePickerManagement(t *testing.T) {
	defer leakcheck.Verify(t)
	c, _, _ := newTestConsole(80, 24)

	pw, pr := c.PickerSize()
	if pw != 80 || pr <= 0 {
		t.Fatalf("PickerSize w=%d r=%d", pw, pr)
	}

	c.SetPickerRows([]string{"item 1", "item 2"})
	if c.picker == nil || len(c.picker.rows) != 2 || c.picker.mode != PickerSearch {
		t.Fatalf("SetPickerRows failed: %+v", c.picker)
	}

	c.ClearPickerRows()
	if c.picker != nil {
		t.Fatal("ClearPickerRows did not clear picker")
	}

	c.SetPickerChoiceRows([]string{"choice 1"})
	if c.picker == nil || c.picker.mode != PickerChoice {
		t.Fatalf("SetPickerChoiceRows failed: %+v", c.picker)
	}
	c.ClearPickerRows()
}

func TestConsoleTerminalColorSchemeNotificationsAndQuery(t *testing.T) {
	defer leakcheck.Verify(t)
	c, _, buf := newTestConsole(80, 24)

	// In inactive console (stop == nil), sets state without emitting escape
	c.SetTerminalColorSchemeNotifications(true)
	if !c.themeNotifications {
		t.Fatal("themeNotifications should be true")
	}
	c.SetTerminalColorSchemeNotifications(true) // no-op duplicate

	// In active console (stop != nil), emits mode sequences
	c.stop = func() {}
	c.SetTerminalColorSchemeNotifications(false)
	if c.themeNotifications {
		t.Fatal("themeNotifications should be false")
	}
	if !strings.Contains(buf.String(), "\x1b[?2031l") {
		t.Fatalf("expected disable mode escape in output: %q", buf.String())
	}
	buf.Reset()

	c.SetTerminalColorSchemeNotifications(true)
	if !strings.Contains(buf.String(), "\x1b[?2031h") {
		t.Fatalf("expected enable mode escape in output: %q", buf.String())
	}
	buf.Reset()

	// QueryTerminalColors emits color query escape
	if err := c.QueryTerminalColors(); err != nil {
		t.Fatalf("QueryTerminalColors err: %v", err)
	}
	if buf.Len() == 0 {
		t.Fatal("QueryTerminalColors emitted no bytes")
	}
}

func TestConsoleRecallLimitAndNeedsDraw(t *testing.T) {
	defer leakcheck.Verify(t)
	c, vt, _ := newTestConsole(80, 24)

	// When rejectedRecall is false, recallLimit does nothing
	c.recallLimit()
	if c.inputLimitShown || len(vt.Scrollback) > 0 {
		t.Fatal("recallLimit should do nothing when rejectedRecall=false")
	}

	// When rejectedRecall is true, recallLimit prints notice once
	c.Editor.rejectedRecall = true
	c.recallLimit()
	if c.Editor.rejectedRecall {
		t.Fatal("recallLimit should clear rejectedRecall")
	}
	if !c.inputLimitShown {
		t.Fatal("inputLimitShown should be true")
	}
	vtOutput := strings.Join(vt.Scrollback, "\n") + "\n" + vt.Text()
	if !strings.Contains(vtOutput, "Input exceeds 256 KiB") {
		t.Fatalf("notice missing from output: %q", vtOutput)
	}

	// Repeated call is guarded by inputLimitShown
	vt.Scrollback = nil
	c.Editor.rejectedRecall = true
	c.recallLimit()
	if len(vt.Scrollback) > 0 {
		t.Fatal("recallLimit notice should not repeat while inputLimitShown is true")
	}

	// NeedsDraw and Flush
	if c.NeedsDraw() {
		t.Fatal("NeedsDraw should be false initially")
	}
	c.dirty = true
	if !c.NeedsDraw() {
		t.Fatal("NeedsDraw should be true when dirty")
	}
	c.Flush()
	if c.NeedsDraw() {
		t.Fatal("Flush should clear dirty state after draw")
	}

	// FrameStats
	writes, frames, bytesCount, unchanged := c.FrameStats()
	if writes < 0 || frames < 0 || bytesCount < 0 || unchanged < 0 {
		t.Fatalf("invalid FrameStats: %d, %d, %d, %d", writes, frames, bytesCount, unchanged)
	}
}

func TestConsoleApplicationKeypadSequences(t *testing.T) {
	defer leakcheck.Verify(t)

	t.Run("completion navigation with SS3 sequences", func(t *testing.T) {
		c, _, _ := newTestConsole(80, 24)
		c.completionItems = []string{"/help", "/model", "/new"}
		c.completionSelected = 0

		// Down with \x1bOB
		c.Handle(Input{Key: []byte("\x1bOB")}, false)
		if c.completionSelected != 1 {
			t.Fatalf("want completionSelected 1, got %d", c.completionSelected)
		}

		// Down with \x1bOB
		c.Handle(Input{Key: []byte("\x1bOB")}, false)
		if c.completionSelected != 2 {
			t.Fatalf("want completionSelected 2, got %d", c.completionSelected)
		}

		// Up with \x1bOA
		c.Handle(Input{Key: []byte("\x1bOA")}, false)
		if c.completionSelected != 1 {
			t.Fatalf("want completionSelected 1, got %d", c.completionSelected)
		}

		// Up with \x1bOA
		c.Handle(Input{Key: []byte("\x1bOA")}, false)
		if c.completionSelected != 0 {
			t.Fatalf("want completionSelected 0, got %d", c.completionSelected)
		}
	})

	t.Run("editor visual row and history movement with SS3 sequences", func(t *testing.T) {
		c, _, _ := newTestConsole(80, 24)

		// Multi-line editor visual movement
		c.Editor.Insert("line 1\nline 2")
		// Cursor is at end of "line 2"
		c.Handle(Input{Key: []byte("\x1bOA")}, false)
		// Should have moved up to line 1
		if c.Editor.Pos() > len([]rune("line 1")) {
			t.Fatalf("expected cursor in line 1, pos=%d", c.Editor.Pos())
		}
		c.Handle(Input{Key: []byte("\x1bOB")}, false)
		// Should have moved down to line 2
		if c.Editor.Pos() <= len([]rune("line 1")) {
			t.Fatalf("expected cursor in line 2, pos=%d", c.Editor.Pos())
		}

		// History movement
		c.Editor.Clear()
		c.Editor.SetHistory([]string{"hist1", "hist2"})
		c.Handle(Input{Key: []byte("\x1bOA")}, false)
		if c.Editor.Text() != "hist2" {
			t.Fatalf("expected hist2, got %q", c.Editor.Text())
		}
		c.Handle(Input{Key: []byte("\x1bOA")}, false)
		if c.Editor.Text() != "hist1" {
			t.Fatalf("expected hist1, got %q", c.Editor.Text())
		}
		c.Handle(Input{Key: []byte("\x1bOB")}, false)
		if c.Editor.Text() != "hist2" {
			t.Fatalf("expected hist2, got %q", c.Editor.Text())
		}
		c.Handle(Input{Key: []byte("\x1bOB")}, false)
		if c.Editor.Text() != "" {
			t.Fatalf("expected empty draft, got %q", c.Editor.Text())
		}
	})
}
