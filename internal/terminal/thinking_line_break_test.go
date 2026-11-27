package terminal

import (
	"strings"
	"testing"
)

// TestThinkingBlockKeepsSourceLineBreaks covers Pi's reasoning rendering:
// a reasoning block keeps the line breaks of its source. psi's softWrap
// setting joins a paragraph's source lines so the terminal can reflow
// committed prose, but a reasoning line structure is part of the text, and
// Pi's Markdown keeps it. A joined reasoning block also ran straight into
// the tool row that followed it.
func TestThinkingBlockKeepsSourceLineBreaks(t *testing.T) {
	display := DefaultDisplaySettings()
	display.SoftWrap = true
	source := "first reasoning line\nsecond reasoning line"
	rows := FormatThinkingBlock(source, 80, true, display)
	if len(rows) != 3 {
		t.Fatalf("thinking rows = %q, want the two source lines and a trailing blank", rows)
	}
	if rows[0] != "  first reasoning line" || rows[1] != "  second reasoning line" {
		t.Fatalf("thinking rows = %q, want the source line breaks kept", rows)
	}
}

// TestThinkingStreamKeepsSourceLineBreaks covers the live reasoning tail:
// the streamed rows keep the source line breaks too, so the block does not
// change shape when it settles.
func TestThinkingStreamKeepsSourceLineBreaks(t *testing.T) {
	display := DefaultDisplaySettings()
	display.SoftWrap = true
	stream := NewMarkdownStream(80, true)
	stream.SetDisplay(display)
	stream.SetStyle(thinkingStyle())
	stable, _ := stream.Feed("first reasoning line\nsecond reasoning line")
	rows := append(stable, stream.Finish()...)
	joined := strings.Join(rows, "\n")
	if !strings.Contains(joined, "first reasoning line\n") {
		t.Fatalf("streamed reasoning rows = %q, want the source line break kept", rows)
	}
	if strings.Contains(joined, "first reasoning line second reasoning line") {
		t.Fatalf("streamed reasoning joined the source lines: %q", rows)
	}
}
