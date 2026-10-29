package terminal

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"psi/internal/testutil"
)

func TestStreamFragmentKeepsEditorFooterAndStableScrollback(t *testing.T) {
	vt, raw := testutil.NewVTModel(80, 24), &strings.Builder{}
	out := ioMultiWriter{vt: vt, raw: raw}
	c := &Console{Editor: NewEditor(), Out: out, writer: NewWriter(out), decoder: NewDecoder()}
	c.SetPresentation(PresentationSnapshot{
		WorkingDirectory: filepath.FromSlash("/home/operator/work"), HomeDirectory: filepath.FromSlash("/home/operator"),
		Provider: "anthropic", ProviderCount: 2, Model: "example-model", Thinking: "high", NoColor: true,
	})
	c.Status("⠋ Working… (Esc/Ctrl+C cancel, input paused)")
	if n, err := c.StreamWrite([]byte("A partial answer")); err != nil || n == 0 {
		t.Fatalf("partial write n=%d err=%v", n, err)
	}
	assertComposed := func(where string) {
		t.Helper()
		visible := strings.Join(vt.Scrollback, "\n") + "\n" + vt.Text()
		for _, want := range []string{"A partial answer", "Working…", "~" + string(filepath.Separator) + "work", "example-model"} {
			if !strings.Contains(visible, want) {
				t.Fatalf("%s lost %q:\n%s", where, want, visible)
			}
		}
	}
	assertComposed("partial stream")
	if len(vt.Scrollback) != 0 {
		t.Fatalf("incomplete assistant fragment committed early: %q", vt.Scrollback)
	}
	// A notice append replaces only the composed tail; it cannot erase the
	// active text fragment, editor, or footer.
	if _, err := c.Write([]byte("local notice\r\n")); err != nil {
		t.Fatal(err)
	}
	assertComposed("notice append")
	if _, err := c.StreamWrite([]byte(" and it continues\n")); err != nil {
		t.Fatal(err)
	}
	visible := strings.Join(vt.Scrollback, "\n") + "\n" + vt.Text()
	if strings.Count(visible, "A partial answer and it continues") != 1 {
		t.Fatalf("completed line missing or duplicated:\n%s", visible)
	}
	if len(c.streamFragment) != 0 {
		t.Fatalf("newline left %d active bytes", len(c.streamFragment))
	}
	if !strings.Contains(vt.Text(), "example-model") {
		t.Fatalf("footer not retained after line commit:\n%s", vt.Text())
	}
	if strings.Contains(raw.String(), "\x1b[2J") || strings.Contains(raw.String(), "\x1b[3J") {
		t.Fatal("stream composition cleared screen or scrollback")
	}
}

func TestActiveFragmentSourceBudgetPreservesVisibleBytes(t *testing.T) {
	vt := testutil.NewVTModel(80, 24)
	c := &Console{Editor: NewEditor(), Out: vtFeed{vt}, writer: NewWriter(vtFeed{vt})}
	input := strings.Repeat("x", maxActiveFragmentBytes+200)
	if n, err := c.StreamWrite([]byte(input)); err != nil || n != len(input) {
		t.Fatalf("stream write n=%d err=%v", n, err)
	}
	if len(c.streamFragment) > maxActiveFragmentBytes {
		t.Fatalf("active fragment retained %d bytes", len(c.streamFragment))
	}
	// End the line so the retained active source becomes committed output.
	if _, err := c.StreamWrite([]byte("\n")); err != nil {
		t.Fatal(err)
	}
	all := strings.Join(vt.Scrollback, "\n") + "\n" + vt.Text()
	if got := strings.Count(all, "x"); got != len(input) {
		t.Fatalf("bounded fallback lost/duplicated bytes: got=%d want=%d", got, len(input))
	}
	if !strings.Contains(all, "──") {
		t.Fatal("editor borders disappeared during bounded fallback")
	}
}

func TestStreamCompositionIsChunkIndependentAtBoundaries(t *testing.T) {
	text := "A wide 世界 phrase and an emoji 👩‍👩‍👧‍👦\nsecond line\n"
	run := func(chunks []string) string {
		vt := testutil.NewVTModel(40, 12)
		c := &Console{Editor: NewEditor(), Out: vtFeed{vt}, writer: NewWriter(vtFeed{vt}), SizeFunc: func() (int, int) { return 40, 12 }}
		c.SetPresentation(PresentationSnapshot{WorkingDirectory: "/work", Model: "model", NoColor: true})
		c.Status("⠋ Working…")
		stream := NewStreamer(StreamOutput{Console: c}, true)
		for _, chunk := range chunks {
			stream.Delta(chunk)
			stream.Flush()
		}
		stream.Close()
		return strings.Join(vt.Scrollback, "\n") + "\n" + vt.Text()
	}
	whole := run([]string{text})
	parts := []string{"A wide 世", "界 phrase and an emoji 👩‍", "👩‍👧‍👦\nsecond", " line\n"}
	chunked := run(parts)
	if whole != chunked {
		t.Fatalf("stream chunking changed final view\nwhole:\n%s\nchunked:\n%s", whole, chunked)
	}
	for _, want := range []string{"A wide", "phrase and an emoji", "second line", "model"} {
		if !strings.Contains(chunked, want) {
			t.Fatalf("chunked output lost %q:\n%s", want, chunked)
		}
	}
}

func TestStreamToolBoundaryKeepsFragmentAndOneSettledRow(t *testing.T) {
	vt := testutil.NewVTModel(80, 24)
	c := &Console{Editor: NewEditor(), Out: vtFeed{vt}, writer: NewWriter(vtFeed{vt}), SizeFunc: func() (int, int) { return 80, 24 }}
	c.SetPresentation(PresentationSnapshot{WorkingDirectory: "/work", Model: "model", NoColor: true})
	c.Status("⠋ Working…")
	stream := NewStreamer(StreamOutput{Console: c}, true)
	stream.Delta("partial answer before tool")
	stream.Flush()
	if !strings.Contains(vt.Text(), "partial answer before tool") || !strings.Contains(vt.Text(), "model") {
		t.Fatalf("partial output hid footer: %s", vt.Text())
	}
	stream.Close() // Pi tool boundary settles the partial line exactly once.
	c.Status("⠋ bash: running")
	status := NewStatusWriter(RawOutput{W: c})
	status.Update("bash", "call-1", ToolRunning)
	status.Update("bash", "call-1", ToolDone)
	c.Status("⠋ Working…")
	visible := strings.Join(vt.Scrollback, "\n") + "\n" + vt.Text()
	if strings.Count(visible, "partial answer before tool") != 1 || strings.Count(visible, "bash: done") != 1 {
		t.Fatalf("tool boundary duplicated or lost output:\n%s", visible)
	}
	if strings.Contains(strings.Join(vt.Scrollback, "\n"), "bash: running") {
		t.Fatal("transient running row was committed to scrollback")
	}
	if !strings.Contains(vt.Text(), "model") {
		t.Fatalf("footer disappeared after tool settlement:\n%s", vt.Text())
	}
}

func TestBusyEscapeCancelsButPastedEscapeDoesNot(t *testing.T) {
	c, _, _ := reviewConsole()
	if _, _, quit, cancel := c.Handle(Input{Key: []byte{27}}, true); quit || !cancel {
		t.Fatalf("busy Escape: quit=%v cancel=%v", quit, cancel)
	}
	c.Handle(Input{Paste: true, PasteStart: true}, true)
	c.Handle(Input{Paste: true, Key: []byte{27}}, true)
	if _, _, quit, cancel := c.Handle(Input{Paste: true, PasteEnd: true}, true); quit || cancel {
		t.Fatalf("pasted Escape changed active run: quit=%v cancel=%v", quit, cancel)
	}
}

func TestConsoleWriteKeepsTailWithAppend(t *testing.T) {
	vt := testutil.NewVTModel(50, 12)
	c := &Console{Editor: NewEditor(), Out: vtFeed{vt}, writer: NewWriter(vtFeed{vt}), SizeFunc: func() (int, int) { return 50, 12 }}
	c.SetPresentation(PresentationSnapshot{WorkingDirectory: "/work", Model: "model", NoColor: true})
	c.Draw()
	if _, err := fmt.Fprint(RawOutput{W: c}, "notice\n"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(vt.Text(), "notice") || !strings.Contains(vt.Text(), "model") || !strings.Contains(vt.Text(), "──") {
		t.Fatalf("append failed to preserve composed tail:\n%s", vt.Text())
	}
}

type ioMultiWriter struct {
	vt  *testutil.VTModel
	raw *strings.Builder
}

func (w ioMultiWriter) Write(p []byte) (int, error) {
	w.vt.Feed(p)
	return w.raw.Write(p)
}

// Pi keeps the active tool row above the editor. The row stays in the owned
// tail and never reaches native scrollback.
func TestActiveToolRowStaysInTheOwnedTail(t *testing.T) {
	vt := testutil.NewVTModel(80, 24)
	c := &Console{Editor: NewEditor(), Out: vtFeed{vt}, writer: NewWriter(vtFeed{vt}), SizeFunc: func() (int, int) { return 80, 24 }}
	c.SetPresentation(PresentationSnapshot{WorkingDirectory: "/work", Model: "model", NoColor: true})
	c.Draw()
	c.SetToolRow("bash: running")
	rows := strings.Split(vt.Text(), "\n")
	toolRow, borderRow := -1, -1
	for i, row := range rows {
		if toolRow < 0 && strings.Contains(row, "bash: running") {
			toolRow = i
		}
		if borderRow < 0 && strings.Contains(row, "─") && strings.TrimSpace(row) != "" {
			borderRow = i
		}
	}
	if toolRow < 0 {
		t.Fatalf("active tool row missing: %q", vt.Text())
	}
	if borderRow < 0 || toolRow > borderRow {
		t.Fatalf("active tool row is not above the editor: %q", vt.Text())
	}
	if len(vt.Scrollback) != 0 {
		t.Fatalf("active tool row reached native scrollback: %q", vt.Scrollback)
	}
	c.ClearToolRow()
	c.Draw()
	if strings.Contains(vt.Text(), "bash: running") {
		t.Fatalf("active tool row survived the settlement: %q", vt.Text())
	}
}

// The active tool row keeps the shared message margin and stays on one
// physical row even when the tool name is longer than the terminal.
func TestActiveToolRowKeepsMarginAndOnePhysicalRow(t *testing.T) {
	vt := testutil.NewVTModel(40, 12)
	c := &Console{Editor: NewEditor(), Out: vtFeed{vt}, writer: NewWriter(vtFeed{vt}), SizeFunc: func() (int, int) { return 40, 12 }}
	c.SetPresentation(PresentationSnapshot{WorkingDirectory: "/work", Model: "model", NoColor: true})
	c.Draw()
	c.SetToolRow("bash-with-a-very-long-name-that-does-not-fit: running")
	for _, row := range strings.Split(vt.Text(), "\n") {
		if strings.Contains(row, "bash-with-a-very-long-name") {
			if !strings.HasPrefix(row, MessageIndent) {
				t.Fatalf("active tool row lost the message margin: %q", row)
			}
			if CellWidth(row) > 39 {
				t.Fatalf("active tool row exceeds one physical row: %q", row)
			}
			if strings.Contains(row, ": running") {
				t.Fatalf("clipped tool row dropped its state: %q", row)
			}
			return
		}
	}
	t.Fatalf("active tool row missing: %q", vt.Text())
}
