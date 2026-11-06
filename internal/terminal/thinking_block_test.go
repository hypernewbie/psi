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
