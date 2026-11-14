package terminal

import (
	"strings"
	"testing"

	"psi/internal/testutil"
	pitheme "psi/internal/theme"
)

// TestThinkingBlockIsItalicWithTheme covers Pi's reasoning style: the block
// and the collapsed label are italic.
func TestThinkingBlockIsItalicWithTheme(t *testing.T) {
	doc, err := pitheme.BuiltinDocument("dark")
	if err != nil {
		t.Fatal(err)
	}
	palette, err := doc.Compile(pitheme.ColorModeTruecolor)
	if err != nil {
		t.Fatal(err)
	}
	block := strings.Join(FormatThinkingBlock("weigh options", 80, false, DefaultDisplaySettings(), palette), "\n")
	if !strings.Contains(block, "\x1b[3m") {
		t.Fatalf("thinking block is not italic: %q", block)
	}
	label := strings.Join(FormatThinkingLabel(80, false, DefaultDisplaySettings(), palette), "\n")
	if !strings.Contains(label, "\x1b[3m") {
		t.Fatalf("thinking label is not italic: %q", label)
	}
}

// TestThinkingBlockKeepsSourceAndRendersReasoning covers the visible path:
// the reasoning text reaches the screen and the ledger keeps the source so a
// later repaint can show it again.
func TestThinkingBlockKeepsSourceAndRendersReasoning(t *testing.T) {
	c, vt, _ := newTestConsole(80, 24)
	c.SetPresentation(PresentationSnapshot{NoColor: true})
	if _, err := c.ThinkingWrite([]byte("weigh options")); err != nil {
		t.Fatal(err)
	}
	if err := c.CloseThinking(); err != nil {
		t.Fatal(err)
	}
	visible := strings.Join(vt.Scrollback, "\n") + "\n" + vt.Text()
	if !strings.Contains(visible, "weigh options") {
		t.Fatalf("reasoning missing:\n%s", visible)
	}
	if len(c.documentBlocks) != 1 || c.documentBlocks[0].Kind != DocumentThinking {
		t.Fatalf("document blocks = %+v", c.documentBlocks)
	}
	if c.documentBlocks[0].Text != "weigh options" {
		t.Fatalf("ledger source = %q", c.documentBlocks[0].Text)
	}
	if c.documentBlocks[0].Streaming {
		t.Fatal("thinking block still marked as streaming")
	}
}

// TestHiddenThinkingShowsOnlyTheLabel covers Pi's collapsed reasoning block:
// the source stays in the ledger, the screen shows one label, and the
// reasoning text never reaches the screen.
func TestHiddenThinkingShowsOnlyTheLabel(t *testing.T) {
	c, vt, _ := newTestConsole(80, 24)
	c.SetPresentation(PresentationSnapshot{NoColor: true, Display: DisplaySettings{HideThinking: true}})
	if _, err := c.ThinkingWrite([]byte("weigh options")); err != nil {
		t.Fatal(err)
	}
	if _, err := c.ThinkingWrite([]byte(" carefully")); err != nil {
		t.Fatal(err)
	}
	if err := c.CloseThinking(); err != nil {
		t.Fatal(err)
	}
	visible := strings.Join(vt.Scrollback, "\n") + "\n" + vt.Text()
	if strings.Contains(visible, "weigh options") || strings.Contains(visible, "carefully") {
		t.Fatalf("hidden reasoning leaked:\n%s", visible)
	}
	if strings.Count(visible, "Thinking...") != 1 {
		t.Fatalf("label count != 1:\n%s", visible)
	}
	if len(c.documentBlocks) != 1 || c.documentBlocks[0].Text == "" {
		t.Fatalf("hidden source was not kept: %+v", c.documentBlocks)
	}
}

// TestThinkingVisibilityToggleRepaintsTheLedger covers the Ctrl+T path: a
// repaint after the toggle shows the label instead of the reasoning, and
// toggling back restores the reasoning.
func TestThinkingVisibilityToggleRepaintsTheLedger(t *testing.T) {
	c, vt, _ := newTestConsole(80, 24)
	c.SetPresentation(PresentationSnapshot{NoColor: true})
	if _, err := c.ThinkingWrite([]byte("weigh options")); err != nil {
		t.Fatal(err)
	}
	if err := c.CloseThinking(); err != nil {
		t.Fatal(err)
	}
	c.SetPresentation(PresentationSnapshot{NoColor: true, Display: DisplaySettings{HideThinking: true}})
	c.RequestFullRedraw()
	c.Draw()
	visible := strings.Join(vt.Scrollback, "\n") + "\n" + vt.Text()
	if strings.Contains(visible, "weigh options") {
		t.Fatalf("hidden repaint kept the reasoning:\n%s", visible)
	}
	if !strings.Contains(visible, "Thinking...") {
		t.Fatalf("hidden repaint lost the label:\n%s", visible)
	}
	c.SetPresentation(PresentationSnapshot{NoColor: true})
	c.RequestFullRedraw()
	c.Draw()
	visible = strings.Join(vt.Scrollback, "\n") + "\n" + vt.Text()
	if !strings.Contains(visible, "weigh options") {
		t.Fatalf("visible repaint lost the reasoning:\n%s", visible)
	}
}

// TestTextAfterThinkingStartsANewBlock covers stream switching: reasoning and
// assistant text are separate ledger blocks in arrival order.
func TestTextAfterThinkingStartsANewBlock(t *testing.T) {
	c, vt, _ := newTestConsole(80, 24)
	c.SetPresentation(PresentationSnapshot{NoColor: true})
	if _, err := c.ThinkingWrite([]byte("weigh options")); err != nil {
		t.Fatal(err)
	}
	if _, err := c.MarkdownWrite([]byte("the answer")); err != nil {
		t.Fatal(err)
	}
	if err := c.CloseMarkdown(); err != nil {
		t.Fatal(err)
	}
	if len(c.documentBlocks) != 2 {
		t.Fatalf("document blocks = %+v", c.documentBlocks)
	}
	if c.documentBlocks[0].Kind != DocumentThinking || c.documentBlocks[1].Kind != DocumentAssistant {
		t.Fatalf("block order = %+v", c.documentBlocks)
	}
	visible := strings.Join(vt.Scrollback, "\n") + "\n" + vt.Text()
	if !strings.Contains(visible, "weigh options") || !strings.Contains(visible, "the answer") {
		t.Fatalf("missing output:\n%s", visible)
	}
	if strings.Index(visible, "weigh options") > strings.Index(visible, "the answer") {
		t.Fatalf("reasoning must precede the answer:\n%s", visible)
	}
}

// TestThinkingWhitespaceDeltaKeepsWordSpacing covers a provider that splits
// a word boundary into its own delta: the space must survive.
func TestThinkingWhitespaceDeltaKeepsWordSpacing(t *testing.T) {
	c, vt, _ := newTestConsole(80, 24)
	c.SetPresentation(PresentationSnapshot{NoColor: true})
	for _, part := range []string{"weigh", " ", "options"} {
		if _, err := c.ThinkingWrite([]byte(part)); err != nil {
			t.Fatal(err)
		}
	}
	if err := c.CloseThinking(); err != nil {
		t.Fatal(err)
	}
	visible := strings.Join(vt.Scrollback, "\n") + "\n" + vt.Text()
	if !strings.Contains(visible, "weigh options") {
		t.Fatalf("reasoning lost its spacing:\n%s", visible)
	}
}

// TestWhitespaceOnlyThinkingCreatesNoBlock covers an empty reasoning run: it
// must not leave a block in the ledger.
func TestWhitespaceOnlyThinkingCreatesNoBlock(t *testing.T) {
	c, _, _ := newTestConsole(80, 24)
	c.SetPresentation(PresentationSnapshot{NoColor: true})
	if _, err := c.ThinkingWrite([]byte("  \n\t ")); err != nil {
		t.Fatal(err)
	}
	if err := c.CloseThinking(); err != nil {
		t.Fatal(err)
	}
	if len(c.documentBlocks) != 0 {
		t.Fatalf("empty reasoning left %d blocks", len(c.documentBlocks))
	}
}

// TestThinkingReflowsAfterResize covers the live reasoning run: a resize
// must re-wrap the active reasoning at the new width.
func TestThinkingReflowsAfterResize(t *testing.T) {
	vt := testutil.NewVTModel(50, 12)
	width := 50
	c := &Console{Editor: NewEditor(), Out: vtFeed{vt}, writer: NewWriter(vtFeed{vt}), SizeFunc: func() (int, int) { return width, 12 }}
	c.SetPresentation(PresentationSnapshot{NoColor: true})
	source := "An active reasoning paragraph with enough words to wrap after resize."
	if _, err := c.ThinkingWrite([]byte(source)); err != nil {
		t.Fatal(err)
	}
	width = 24
	c.Draw()
	visible := strings.Join(append(append([]string(nil), vt.Scrollback...), strings.Split(vt.Text(), "\n")...), " ")
	if !strings.Contains(strings.Join(strings.Fields(visible), " "), "An active reasoning paragraph with enough words to wrap after resize.") {
		t.Fatalf("resized reasoning is missing:\n%s", visible)
	}
	for _, row := range strings.Split(vt.Text(), "\n") {
		if !strings.Contains(row, "reasoning") && !strings.Contains(row, "paragraph") {
			continue
		}
		if len(row) > width {
			t.Fatalf("reasoning row is wider than the terminal: %q", row)
		}
	}
}

// TestMidRunHideToggleShowsOneLabel covers the Ctrl+T path during a live
// run: the redraw renders the ledger label, and a later delta must not
// append a second label for the same reasoning run.
func TestMidRunHideToggleShowsOneLabel(t *testing.T) {
	c, vt, _ := newTestConsole(80, 24)
	c.SetPresentation(PresentationSnapshot{NoColor: true})
	if _, err := c.ThinkingWrite([]byte("weigh options")); err != nil {
		t.Fatal(err)
	}
	// Toggle hidden mid-run and force the redraw the app performs.
	c.SetPresentation(PresentationSnapshot{NoColor: true, Display: DisplaySettings{HideThinking: true}})
	c.RequestFullRedraw()
	c.Draw()
	// A further delta in the same run must not add a second label.
	if _, err := c.ThinkingWrite([]byte(" carefully")); err != nil {
		t.Fatal(err)
	}
	if err := c.CloseThinking(); err != nil {
		t.Fatal(err)
	}
	all := strings.Join(append(append([]string(nil), vt.Scrollback...), strings.Split(vt.Text(), "\n")...), "\n")
	if got := strings.Count(all, "Thinking..."); got != 1 {
		t.Fatalf("label count = %d, want 1:\n%s", got, all)
	}
	if strings.Contains(all, "carefully") {
		t.Fatalf("hidden reasoning leaked after the mid-run toggle:\n%s", all)
	}
	// The ledger keeps the full source for the toggle back.
	if len(c.documentBlocks) != 1 || c.documentBlocks[0].Text != "weigh options carefully" {
		t.Fatalf("ledger lost the reasoning source: %+v", c.documentBlocks)
	}
}

// TestMidRunShowToggleDoesNotDuplicateReasoning covers the reverse toggle:
// restoring visibility mid-run renders the committed prefix from the ledger
// and the remainder from the fragment tail, exactly once.
func TestMidRunShowToggleDoesNotDuplicateReasoning(t *testing.T) {
	c, vt, _ := newTestConsole(80, 24)
	c.SetPresentation(PresentationSnapshot{NoColor: true, Display: DisplaySettings{HideThinking: true}})
	if _, err := c.ThinkingWrite([]byte("weigh options")); err != nil {
		t.Fatal(err)
	}
	// Toggle visible mid-run and force the redraw the app performs.
	c.SetPresentation(PresentationSnapshot{NoColor: true})
	c.RequestFullRedraw()
	c.Draw()
	if _, err := c.ThinkingWrite([]byte(" carefully")); err != nil {
		t.Fatal(err)
	}
	if err := c.CloseThinking(); err != nil {
		t.Fatal(err)
	}
	all := strings.Join(append(append([]string(nil), vt.Scrollback...), strings.Split(vt.Text(), "\n")...), " ")
	fields := strings.Join(strings.Fields(all), " ")
	if got := strings.Count(fields, "weigh options carefully"); got != 1 {
		t.Fatalf("reasoning rendered %d times after the show toggle, want 1:\n%s", got, all)
	}
	if strings.Count(fields, "Thinking...") > 1 {
		t.Fatalf("label duplicated:\n%s", all)
	}
}

// TestThinkingLedgerRepaintKeepsTheDocumentOrder covers the resize path: the
// rebuilt document lists the reasoning block before the assistant block.
func TestThinkingLedgerRepaintKeepsTheDocumentOrder(t *testing.T) {
	c, _, _ := newTestConsole(80, 24)
	c.SetPresentation(PresentationSnapshot{NoColor: true})
	if _, err := c.ThinkingWrite([]byte("weigh options")); err != nil {
		t.Fatal(err)
	}
	if err := c.CloseThinking(); err != nil {
		t.Fatal(err)
	}
	if _, err := c.MarkdownWrite([]byte("the answer")); err != nil {
		t.Fatal(err)
	}
	if err := c.CloseMarkdown(); err != nil {
		t.Fatal(err)
	}
	rows := FormatDocument(c.documentBlocks, 80, c.presentation)
	joined := strings.Join(rows, "\n")
	if strings.Index(joined, "weigh options") > strings.Index(joined, "the answer") {
		t.Fatalf("document order is wrong:\n%s", joined)
	}
	if !strings.Contains(joined, "weigh options") {
		t.Fatalf("reasoning missing from the document:\n%s", joined)
	}
}

// Invisible reasoning must not finalize the live answer, even when a
// multi-byte whitespace rune or an escape sequence arrives in fragments.
func TestInvisibleThinkingBetweenTextDoesNotSplit(t *testing.T) {
	for _, hide := range []bool{false, true} {
		name := "visible"
		if hide {
			name = "hidden"
		}
		t.Run(name, func(t *testing.T) {
			for _, tc := range []struct {
				name   string
				chunks []string
			}{
				{name: "whitespace", chunks: []string{"\n\n"}},
				{name: "fragmented_unicode_whitespace", chunks: []string{"\xe2", "\x80", "\x83"}},
				{name: "fragmented_controls", chunks: []string{"\x1b]52;c;", "hidden-payload", "\a"}},
			} {
				t.Run(tc.name, func(t *testing.T) {
					c, vt, _ := newTestConsole(80, 12)
					c.SetPresentation(PresentationSnapshot{NoColor: true, Display: DisplaySettings{HideThinking: hide}})
					if _, err := c.MarkdownWrite([]byte("The answer ends here")); err != nil {
						t.Fatal(err)
					}
					for _, chunk := range tc.chunks {
						if _, err := c.ThinkingWrite([]byte(chunk)); err != nil {
							t.Fatal(err)
						}
					}
					if _, err := c.MarkdownWrite([]byte(".")); err != nil {
						t.Fatal(err)
					}
					if err := c.CloseMarkdown(); err != nil {
						t.Fatal(err)
					}
					visible := thinkingVisible(vt)
					if !strings.Contains(visible, "The answer ends here.") {
						t.Fatalf("answer was split by invisible reasoning:\n%s", visible)
					}
					if len(c.documentBlocks) != 1 || c.documentBlocks[0].Kind != DocumentAssistant || c.documentBlocks[0].Text != "The answer ends here." {
						t.Fatalf("answer must stay joined after replay: %+v", c.documentBlocks)
					}
				})
			}
		})
	}
}

// Closing the app's reasoning Streamer can write a trailing newline after the
// answer has started. That synthetic newline must not detach pending text.
func TestThinkingStreamerCloseDoesNotSplitAnswer(t *testing.T) {
	c, vt, _ := newTestConsole(80, 16)
	c.SetPresentation(PresentationSnapshot{NoColor: true})
	text := NewStreamer(StreamOutput{Console: c}, true)
	thinking := NewStreamer(ThinkingOutput{Console: c}, true)
	text.Delta("Before reasoning")
	text.Flush()
	c.RequestFullRedraw()
	thinking.Delta("weigh options")
	thinking.Flush()
	text.Delta("The answer ends here")
	text.Flush()
	text.Delta(".")
	thinking.Close()
	text.Close()
	if err := c.Err(); err != nil {
		t.Fatal(err)
	}
	visible := thinkingVisible(vt)
	if !strings.Contains(visible, "The answer ends here.") || strings.Count(visible, "weigh options") != 1 {
		t.Fatalf("stream finalization split the answer or lost reasoning:\n%s", visible)
	}
	want := []DocumentBlock{
		{Kind: DocumentAssistant, Text: "Before reasoning"},
		{Kind: DocumentThinking, Text: "weigh options"},
		{Kind: DocumentAssistant, Text: "The answer ends here.\n"},
	}
	if len(c.documentBlocks) != len(want) {
		t.Fatalf("visible reasoning must separate text blocks: %+v", c.documentBlocks)
	}
	for i, block := range c.documentBlocks {
		if block.Kind != want[i].Kind || block.Text != want[i].Text {
			t.Fatalf("block %d = %+v, want %+v", i, block, want[i])
		}
	}
}

func TestInvisibleThinkingPreservesOutputError(t *testing.T) {
	c, _, _ := newTestConsole(80, 12)
	c.writer = NewWriter(&recoverableWriter{fail: true})
	_, wantErr := c.MarkdownWrite([]byte("The answer ends here"))
	if wantErr == nil {
		t.Fatal("expected a frame output failure")
	}
	if n, err := c.ThinkingWrite([]byte("\n\n")); n != 0 || err != wantErr {
		t.Fatalf("ThinkingWrite = (%d, %v), want (0, %v)", n, err, wantErr)
	}
}
