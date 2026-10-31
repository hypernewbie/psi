package terminal

import (
	"strings"
	"testing"

	"psi/internal/testutil"
)

func TestResizeReflowsStableAndActiveMarkdownWithoutDuplication(t *testing.T) {
	vt := testutil.NewVTModel(50, 12)
	width := 50
	c := &Console{Editor: NewEditor(), Out: vtFeed{vt}, writer: NewWriter(vtFeed{vt}), SizeFunc: func() (int, int) { return width, 12 }}
	c.SetPresentation(PresentationSnapshot{NoColor: true})
	source := "First stable paragraph.\n\nAn active paragraph with enough words to wrap after resize."
	if _, err := c.MarkdownWrite([]byte(source)); err != nil {
		t.Fatal(err)
	}
	if len(c.documentBlocks) != 1 || !c.documentBlocks[0].Streaming {
		t.Fatalf("active assistant source was not retained as a streaming block: %#v", c.documentBlocks)
	}
	if strings.Contains(c.documentBlocks[0].Text, "An active paragraph") {
		t.Fatalf("active tail was duplicated in the committed document source: %q", c.documentBlocks[0].Text)
	}

	width = 24
	c.Draw()
	visible := func() string {
		return strings.Join(append(append([]string(nil), vt.Scrollback...), strings.Split(vt.Text(), "\n")...), " ")
	}
	countActive := func() int {
		return strings.Count(strings.Join(strings.Fields(visible()), ""), "Anactiveparagraph")
	}
	if got := countActive(); got != 1 {
		t.Fatalf("resize did not show the active source exactly once: count=%d screen=%q scrollback=%q", got, vt.Text(), vt.Scrollback)
	}

	if err := c.CloseMarkdown(); err != nil {
		t.Fatal(err)
	}
	width = 80
	c.Draw()
	if got := countActive(); got != 1 {
		t.Fatalf("settled source was not reflowed exactly once: count=%d screen=%q scrollback=%q", got, vt.Text(), vt.Scrollback)
	}
}
