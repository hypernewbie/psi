package terminal

import (
	"strings"
	"testing"

	"psi/internal/testutil"
)

func TestStreamingSanitizerKeepsEscapeState(t *testing.T) {
	var out strings.Builder
	s := NewStreamer(&out, true)
	for _, part := range []string{"hello", "\x1b]52;", "secret-payload", "\x1b", "\\", " world", "\u009b31m", "!"} {
		s.Delta(part)
	}
	s.Close()
	if out.String() != "hello world!\n" {
		t.Fatalf("escape fragments leaked: %q", out.String())
	}
	if Sanitize("a\x7fb\u009cc") != "abc" {
		t.Fatal("DEL/C1 controls survived")
	}
}

func TestBusyInputCannotSubmitOrExecutePastedControl(t *testing.T) {
	c := &Console{Editor: NewEditor(), Out: &strings.Builder{}, writer: NewWriter(&strings.Builder{}), decoder: NewDecoder()}
	_, _, _, cancel := c.Handle(Input{Paste: true, PasteStart: true}, true)
	if cancel {
		t.Fatal("paste delimiter cancelled run")
	}
	_, submitted, _, cancel := c.Handle(Input{Key: []byte{3}, Paste: true}, true)
	if submitted || cancel {
		t.Fatal("pasted Ctrl+C was executed")
	}
	c.Handle(Input{Paste: true, PasteEnd: true}, true)
	_, _, _, cancel = c.Handle(Input{Key: []byte{3}}, true)
	if !cancel {
		t.Fatal("typed Ctrl+C did not cancel")
	}
}

// A4-15: ordinary keys during an active run are ignored. They never submit,
// never cancel, and never become a queued prompt.
func TestBusyRunKeepsEditorActiveAndSubmitsSteering(t *testing.T) {
	// Pi's editor stays fully usable while the agent runs: keys edit the
	// draft, Enter returns the text for steering, and only the cancel keys
	// interrupt the run.
	vt := testutil.NewVTModel(60, 20)
	c := &Console{Editor: NewEditor(), Out: vtFeed{vt}, writer: NewWriter(vtFeed{vt}), SizeFunc: func() (int, int) { return 60, 20 }}
	c.SetPresentation(PresentationSnapshot{NoColor: true})
	c.Draw()
	for _, key := range []string{"hel", "lo"} {
		if text, submitted, quit, cancel := c.Handle(Input{Key: []byte(key)}, true); text != "" || submitted || quit || cancel {
			t.Fatalf("busy edit key %q was intercepted: %+v", key, text)
		}
	}
	if c.Editor.Text() != "hello" {
		t.Fatalf("busy run dropped typed input: %q", c.Editor.Text())
	}
	text, submitted, quit, cancel := c.Handle(Input{Key: []byte{'\r'}}, true)
	if text != "hello" || !submitted || quit || cancel {
		t.Fatalf("busy Enter must submit for steering: text=%q submitted=%v quit=%v cancel=%v", text, submitted, quit, cancel)
	}
	for _, key := range []byte{3, 27} {
		if _, _, _, cancel := c.Handle(Input{Key: []byte{key}}, true); !cancel {
			t.Fatalf("busy cancel key %q did not cancel", key)
		}
	}
}

func TestLoopStatusRowRendersAndClears(t *testing.T) {
	vt := testutil.NewVTModel(60, 20)
	c := &Console{Editor: NewEditor(), Out: vtFeed{vt}, writer: NewWriter(vtFeed{vt}), SizeFunc: func() (int, int) { return 60, 20 }}
	c.SetPresentation(PresentationSnapshot{NoColor: true})
	c.Draw()
	c.SetLoopStatus("loop · pass 2/5 · refine the renderer")
	if !strings.Contains(vt.Text(), "loop · pass 2/5") {
		t.Fatalf("loop status missing from active tail: %q", vt.Text())
	}
	c.SetLoopStatus("")
	if strings.Contains(vt.Text(), "loop · pass 2/5") {
		t.Fatalf("cleared loop status stayed visible: %q", vt.Text())
	}
}

func TestSteeringRowsRenderAboveEditorLikePendingMessages(t *testing.T) {
	vt := testutil.NewVTModel(60, 20)
	c := &Console{Editor: NewEditor(), Out: vtFeed{vt}, writer: NewWriter(vtFeed{vt}), SizeFunc: func() (int, int) { return 60, 20 }}
	c.SetPresentation(PresentationSnapshot{NoColor: true})
	c.Draw()
	c.SetSteeringRows([]string{"first steer", "second steer"})
	rows := append(append([]string{}, vt.Scrollback...), strings.Split(vt.Text(), "\n")...)
	border := -1
	for i, row := range rows {
		if strings.Contains(Sanitize(row), "\u2500") {
			border = i
			break
		}
	}
	if border < 4 {
		t.Fatalf("steering rows or borders missing: %q", rows)
	}
	if strings.TrimSpace(rows[border-4]) != "" || !strings.Contains(rows[border-3], "Steering: first steer") || !strings.Contains(rows[border-2], "Steering: second steer") || strings.TrimSpace(rows[border-1]) != "" {
		t.Fatalf("steering block does not match Pi's pending rows: %q", rows)
	}
	c.SetSteeringRows(nil)
	rows = append(append([]string{}, vt.Scrollback...), strings.Split(vt.Text(), "\n")...)
	for _, row := range rows {
		if strings.Contains(row, "Steering:") {
			t.Fatalf("cleared steering rows stayed visible: %q", rows)
		}
	}
}
