package terminal

import (
	"fmt"
	"strings"
	"testing"
)

func TestThinkingSanitizesAcrossDeltas(t *testing.T) {
	c, vt, _ := newTestConsole(80, 12)
	c.SetPresentation(PresentationSnapshot{NoColor: true})
	chunks := []string{"safe \x1b]52;c;", "hidden-payload", "\aafter "}
	for _, b := range []byte("思考 😀") {
		chunks = append(chunks, string([]byte{b}))
	}
	for _, chunk := range chunks {
		if _, err := c.ThinkingWrite([]byte(chunk)); err != nil {
			t.Fatal(err)
		}
	}
	if err := c.CloseThinking(); err != nil {
		t.Fatal(err)
	}
	if got := c.documentBlocks[0].Text; got != "safe after 思考 😀" {
		t.Fatalf("retained reasoning = %q, want safe after 思考 😀", got)
	}
	visible := thinkingVisible(vt)
	if strings.Contains(visible, "hidden-payload") || strings.Count(visible, "😀") != 1 {
		t.Fatalf("split deltas leaked control payload or lost Unicode:\n%s", visible)
	}
}

func TestThinkingFenceRedrawDoesNotSettleOpenFence(t *testing.T) {
	c, vt, _ := newTestConsole(80, 12)
	c.SetPresentation(PresentationSnapshot{NoColor: true})
	if _, err := c.ThinkingWrite([]byte("```go\npartial")); err != nil {
		t.Fatal(err)
	}
	c.RequestFullRedraw()
	c.Draw()
	if got := strings.Count(thinkingVisible(vt), "```"); got != 1 {
		t.Fatalf("open fence has %d fence markers after redraw, want 1:\n%s", got, thinkingVisible(vt))
	}
	if _, err := c.ThinkingWrite([]byte(" continued\nnext line\n```\n")); err != nil {
		t.Fatal(err)
	}
	if err := c.CloseThinking(); err != nil {
		t.Fatal(err)
	}
	visible := thinkingVisible(vt)
	if strings.Count(visible, "partial continued") != 1 || strings.Count(visible, "next line") != 1 || strings.Count(visible, "```") != 2 {
		t.Fatalf("fenced reasoning duplicated or lost rows after redraw:\n%s", visible)
	}
}

func TestThinkingCloseAfterRedrawSettlesPendingFenceMarker(t *testing.T) {
	for _, source := range []string{"```text\n```", "```text\ncode line\n```"} {
		t.Run(source, func(t *testing.T) {
			c, vt, _ := newTestConsole(80, 12)
			display := DefaultDisplaySettings()
			c.SetPresentation(PresentationSnapshot{NoColor: true, Display: display})
			if _, err := c.ThinkingWrite([]byte(source)); err != nil {
				t.Fatal(err)
			}
			c.RequestFullRedraw()
			c.Draw()
			if err := c.CloseThinking(); err != nil {
				t.Fatal(err)
			}
			var got, want []string
			for _, row := range strings.Split(thinkingVisible(vt), "\n") {
				if strings.Contains(row, "```") {
					got = append(got, strings.TrimRight(row, " "))
				}
			}
			for _, row := range FormatThinkingBlock(source, 80, true, display) {
				if strings.Contains(row, "```") {
					want = append(want, row)
				}
			}
			if len(got) != 2 || strings.Join(got, "\n") != strings.Join(want, "\n") {
				t.Fatalf("settled fence markers = %q, want %q", got, want)
			}
		})
	}
}

func TestThinkingSettledLineAfterRedrawKeepsSourceOrder(t *testing.T) {
	for _, tc := range []struct {
		name   string
		prefix string
	}{
		{name: "fence", prefix: "```text\n"},
		{name: "fallback", prefix: strings.Repeat("x", maxMarkdownActiveSource+1) + "\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, vt, _ := newTestConsole(80, 12)
			c.SetPresentation(PresentationSnapshot{NoColor: true})
			if _, err := c.ThinkingWrite([]byte(tc.prefix + "sam")); err != nil {
				t.Fatal(err)
			}
			c.RequestFullRedraw()
			c.Draw()
			// The new partial line matches the old preview, masking its change.
			if _, err := c.ThinkingWrite([]byte("e\nsam")); err != nil {
				t.Fatal(err)
			}
			for _, phase := range []string{"live", "closed"} {
				if phase == "closed" {
					if err := c.CloseThinking(); err != nil {
						t.Fatal(err)
					}
				}
				var lines []string
				for _, row := range strings.Split(thinkingVisible(vt), "\n") {
					if text := strings.TrimSpace(row); text == "sam" || text == "same" {
						lines = append(lines, text)
					}
				}
				if got := strings.Join(lines, "\n"); got != "same\nsam" {
					t.Fatalf("%s line order = %q, want same followed by sam", phase, got)
				}
			}
		})
	}
}

func TestThinkingTableSettlesWithWiderCellsInOneDelta(t *testing.T) {
	c, vt, _ := newTestConsole(80, 12)
	c.SetPresentation(PresentationSnapshot{NoColor: true})
	source := "| h1 | h2 |\n|---|---|\n"
	for i := 1; i <= 8; i++ {
		source += fmt.Sprintf("| r%02dc1 | r%02dc2 |\n", i, i)
	}
	if _, err := c.ThinkingWrite([]byte(source)); err != nil {
		t.Fatal(err)
	}
	c.RequestFullRedraw()
	c.Draw()
	// Both the widening row and the next block can arrive in one provider delta.
	if _, err := c.ThinkingWrite([]byte("| wider-cell-value | last-cell |\n\nafter table\n")); err != nil {
		t.Fatal(err)
	}
	if err := c.CloseThinking(); err != nil {
		t.Fatal(err)
	}
	visible := thinkingVisible(vt)
	width := -1
	for _, row := range strings.Split(visible, "\n") {
		if !strings.Contains(row, "c1") && !strings.Contains(row, "wider-cell-value") {
			continue
		}
		if width < 0 {
			width = CellWidth(row)
		} else if got := CellWidth(row); got != width {
			t.Fatalf("settled table has mixed row widths %d and %d:\n%s", width, got, visible)
		}
	}
	for i := 1; i <= 8; i++ {
		if needle := fmt.Sprintf("r%02dc1", i); strings.Count(visible, needle) != 1 {
			t.Fatalf("table cell %s is missing or duplicated:\n%s", needle, visible)
		}
	}
	if strings.Count(visible, "wider-cell-value") != 1 || strings.Count(visible, "after table") != 1 {
		t.Fatalf("new table row or next block is missing or duplicated:\n%s", visible)
	}
}
