package terminal

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"

	"psi/internal/event"
)

// fullClear is the retained-document rebuild sequence: clear viewport, home,
// then erase native scrollback before retyping the source.
const fullClear = "\x1b[2J\x1b[H\x1b[3J"

func TestConsoleResizeRebuildsCommittedConversationFromSource(t *testing.T) {
	width, height := 24, 12
	var output bytes.Buffer
	c := &Console{
		Out:          &output,
		Editor:       NewEditor(),
		writer:       NewWriter(&output),
		SizeFunc:     func() (int, int) { return width, height },
		presentation: PresentationSnapshot{NoColor: true},
	}
	source := "The quick brown fox jumps over the lazy dog, then returns home."
	if err := c.AppendTranscriptBlock(DocumentBlock{Kind: DocumentUser, Text: source}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "\r\n") {
		t.Fatal("initial narrow layout did not emit rows")
	}

	output.Reset()
	width = 80
	c.RequestFullRedraw()
	c.Draw()
	got := output.String()
	if !strings.Contains(got, "\x1b[2J\x1b[H\x1b[3J") {
		t.Fatalf("resize did not replace the main-screen document: %q", got)
	}
	if strings.Contains(got, "\x1b[6n") {
		t.Fatal("full-document reflow must not depend on CPR")
	}
	wantRows := FormatUserMessageWithDisplay(source, width, true, DefaultDisplaySettings())
	want := rowsAppend(wantRows)
	if !strings.Contains(got, want) {
		t.Fatalf("resized output does not match fresh source layout\nwant %q\ngot %q", want, got)
	}
}

// resizeConsole is the shared fixture for the resize regressions. Every case
// proves the same invariants through the real console paths instead of a bare
// request method.
type resizeConsole struct {
	t       *testing.T
	console *Console
	width   int
	height  int
}

func newResizeConsole(t *testing.T, out io.Writer, width, height int) *resizeConsole {
	t.Helper()
	f := &resizeConsole{t: t, width: width, height: height}
	f.console = &Console{
		Out:          out,
		Editor:       NewEditor(),
		writer:       NewWriter(out),
		SizeFunc:     func() (int, int) { return f.width, f.height },
		presentation: PresentationSnapshot{NoColor: true},
	}
	return f
}

// handleResize drives the real input branch. A resize must never submit, quit,
// or cancel, whatever the run state.
func (f *resizeConsole) handleResize(busy bool) {
	f.t.Helper()
	text, submitted, quit, cancel := f.console.Handle(Input{Resize: true}, busy)
	if text != "" || submitted || quit || cancel {
		f.t.Fatalf("resize input returned text=%q submitted=%v quit=%v cancel=%v", text, submitted, quit, cancel)
	}
}

func (f *resizeConsole) requireEditor(text string, pos int) {
	f.t.Helper()
	if got, gotPos := f.console.Editor.Text(), f.console.Editor.Pos(); got != text || gotPos != pos {
		f.t.Fatalf("resize changed the draft to %q at %d, want %q at %d", got, gotPos, text, pos)
	}
}

func (f *resizeConsole) requireRebuild(out *bytes.Buffer, wantRows ...string) {
	f.t.Helper()
	got := out.String()
	if !strings.Contains(got, fullClear) || strings.Count(got, "\x1b[2J") != 1 || strings.Count(got, "\x1b[3J") != 1 {
		f.t.Fatalf("geometry change did not rebuild the document exactly once: %q", got)
	}
	for _, want := range wantRows {
		if !strings.Contains(got, want) {
			f.t.Fatalf("rebuild is missing %q: %q", want, got)
		}
	}
}

func TestConsoleResizeInputTracksGeometry(t *testing.T) {
	const source = "The quick brown fox jumps over the lazy dog, then returns home."
	for _, busy := range []bool{false, true} {
		t.Run(fmt.Sprintf("busy=%t", busy), func(t *testing.T) {
			var output bytes.Buffer
			f := newResizeConsole(t, &output, 40, 12)
			if err := f.console.AppendTranscriptBlock(DocumentBlock{Kind: DocumentUser, Text: source}); err != nil {
				t.Fatal(err)
			}
			if !f.console.Editor.Insert("draft text") {
				t.Fatal("fixture could not insert the draft")
			}
			f.console.Editor.MoveLeft()
			f.console.Draw()
			draft, pos := f.console.Editor.Text(), f.console.Editor.Pos()
			output.Reset()

			// A notification with unchanged geometry must not paint at all.
			for i := 0; i < 4; i++ {
				f.handleResize(busy)
				f.console.Draw()
			}
			if got := output.String(); got != "" {
				t.Fatalf("four identical resize notifications emitted %q", got)
			}
			f.requireEditor(draft, pos)

			// Change width alone, then height alone. Each must rebuild source
			// once and consume the pending resize without changing the draft.
			for _, size := range [][2]int{{80, 12}, {80, 14}} {
				f.width, f.height = size[0], size[1]
				f.handleResize(busy)
				f.console.Draw()
				f.requireRebuild(&output, rowsAppend(FormatUserMessageWithDisplay(source, f.width, true, DefaultDisplaySettings())))
				f.requireEditor(draft, pos)
				output.Reset()
				f.handleResize(busy)
				f.console.Draw()
				if got := output.String(); got != "" {
					t.Fatalf("repeated notification at %dx%d emitted %q", f.width, f.height, got)
				}
				f.requireEditor(draft, pos)
			}
		})
	}

	// Before the first emission there is no retained geometry to replace, so a
	// resize may render the initial editor but must never clear the screen.
	t.Run("first draw without prior geometry", func(t *testing.T) {
		var output bytes.Buffer
		f := newResizeConsole(t, &output, 80, 24)
		f.handleResize(false)
		f.console.Draw()
		got := output.String()
		if strings.Contains(got, "\x1b[2J") || strings.Contains(got, "\x1b[3J") {
			t.Fatalf("first draw cleared the screen on an unchanged render: %q", got)
		}
	})
}

func TestConsoleDeferredDrawTracksResizeGeometry(t *testing.T) {
	const sentinel = "deferred sentinel"
	var output bytes.Buffer
	f := newResizeConsole(t, &output, 40, 12)
	f.console.SetDeferDraw(true)
	if err := f.console.AppendTranscriptBlock(DocumentBlock{Kind: DocumentUser, Text: sentinel}); err != nil {
		t.Fatal(err)
	}
	output.Reset()

	// The successful deferred append recorded the geometry, so a same-size
	// notification is silent even before the replay finishes.
	f.handleResize(false)
	f.console.Draw()
	if got := output.String(); got != "" {
		t.Fatalf("same-size notification during deferred replay emitted %q", got)
	}

	f.width = 80
	f.handleResize(false)
	f.console.Draw()
	f.requireRebuild(&output, sentinel)
	output.Reset()

	// Resuming normal drawing paints the editor tail once. The deferred rebuild
	// never emitted it, so bytes are expected here; a second document replay is
	// not.
	f.console.SetDeferDraw(false)
	f.console.Draw()
	if got := output.String(); strings.Contains(got, "\x1b[2J") || strings.Contains(got, "\x1b[3J") || strings.Contains(got, sentinel) {
		t.Fatalf("first normal draw after deferred replay rebuilt the document: %q", got)
	}
}

func TestConsoleResizeKeepsPendingFullRedraw(t *testing.T) {
	const sentinel = "pending sentinel"
	var output bytes.Buffer
	f := newResizeConsole(t, &output, 40, 12)
	if err := f.console.AppendTranscriptBlock(DocumentBlock{Kind: DocumentUser, Text: sentinel}); err != nil {
		t.Fatal(err)
	}
	f.console.Draw()
	output.Reset()

	// An explicit repaint stays armed across a redundant notification.
	f.console.RequestFullRedraw()
	f.handleResize(false)
	f.console.Draw()
	f.requireRebuild(&output, sentinel)
	output.Reset()
	f.handleResize(false)
	f.console.Draw()
	if got := output.String(); got != "" {
		t.Fatalf("repeated notification after an explicit repaint emitted %q", got)
	}

	// An observed resize away and back keeps the rebuild armed until it draws.
	f.width = 80
	f.handleResize(false)
	f.width = 40
	f.handleResize(false)
	f.console.Draw()
	f.requireRebuild(&output, sentinel)
	output.Reset()
	f.handleResize(false)
	f.console.Draw()
	if got := output.String(); got != "" {
		t.Fatalf("repeated notification after a latched rebuild emitted %q", got)
	}
}

func TestConsoleResizeOutputFailureKeepsGeometry(t *testing.T) {
	for _, test := range []struct {
		name        string
		deferred    bool
		firstAppend bool
	}{
		{name: "normal rebuild"},
		{name: "deferred rebuild", deferred: true},
		{name: "first deferred append", deferred: true, firstAppend: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			out := &recoverableWriter{}
			f := newResizeConsole(t, out, 40, 12)
			f.console.SetDeferDraw(test.deferred)
			block := DocumentBlock{Kind: DocumentUser, Text: "fault sentinel"}
			if !test.firstAppend {
				if err := f.console.AppendTranscriptBlock(block); err != nil {
					t.Fatal(err)
				}
				f.console.Draw()
				if f.console.lastW != 40 || f.console.lastH != 12 {
					t.Fatalf("successful output did not record geometry: %dx%d", f.console.lastW, f.console.lastH)
				}
			}
			lastW, lastH := f.console.lastW, f.console.lastH
			f.width = 80
			out.fail = true
			if test.firstAppend {
				if err := f.console.AppendTranscriptBlock(block); !errors.Is(err, io.ErrClosedPipe) {
					t.Fatalf("failed deferred append returned %v", err)
				}
			} else {
				f.handleResize(false)
				f.console.Draw()
			}
			if !errors.Is(f.console.Err(), io.ErrClosedPipe) {
				t.Fatalf("output failure was not sticky: %v", f.console.Err())
			}
			if f.console.lastW != lastW || f.console.lastH != lastH {
				t.Fatalf("failed output advanced geometry to %dx%d, want %dx%d", f.console.lastW, f.console.lastH, lastW, lastH)
			}

			// The failed rebuild may end synchronized output. Only later calls
			// must be silent, even though this writer can now succeed again.
			before := out.Len()
			f.console.Draw()
			if err := f.console.AppendTranscriptBlock(block); !errors.Is(err, io.ErrClosedPipe) {
				t.Fatalf("append lost the sticky error: %v", err)
			}
			if out.Len() != before {
				t.Fatalf("sticky output error still emitted %q", out.String()[before:])
			}
		})
	}
}

func TestDocumentReflowRestoresClippedToolPreview(t *testing.T) {
	block := DocumentBlock{Kind: DocumentTool, ToolName: "read", ToolState: "done", CallID: "call-1",
		Summary: event.ToolSummary{Kind: "read", Path: "internal/terminal/transcript_document.go", LineRange: ":10-20", Available: true}}
	presentation := PresentationSnapshot{NoColor: true}
	narrow := strings.Join(FormatDocument([]DocumentBlock{block}, 20, presentation), "\n")
	wide := strings.Join(FormatDocument([]DocumentBlock{block}, 100, presentation), "\n")
	if strings.Contains(narrow, "transcript_document.go") {
		t.Fatalf("narrow tool row should clip its preview: %q", narrow)
	}
	if !strings.Contains(wide, "internal/terminal/transcript_document.go:10-20") {
		t.Fatalf("wide layout did not restore the retained tool summary: %q", wide)
	}
}
