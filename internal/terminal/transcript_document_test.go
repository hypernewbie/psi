package terminal

import (
	"bytes"
	"strings"
	"testing"

	"psi/internal/event"
)

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
