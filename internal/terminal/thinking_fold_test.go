package terminal

import (
	"fmt"
	"strings"
	"testing"

	"psi/internal/testutil"
	pitheme "psi/internal/theme"
)

// thinkingVisible returns every row the terminal holds: native scrollback plus
// the current screen. A reasoning row must appear once across both.
func thinkingVisible(vt *testutil.VTModel) string {
	return strings.Join(vt.Scrollback, "\n") + "\n" + vt.Text()
}

func TestThinkingLongProseStreamsIntoScrollback(t *testing.T) {
	c, vt, _ := newTestConsole(80, 12)
	c.SetPresentation(PresentationSnapshot{NoColor: true, Model: "test-model"})
	for i := 1; i <= 30; i++ {
		line := fmt.Sprintf("reasoning line %02d with enough words to wrap", i)
		if _, err := c.ThinkingWrite([]byte(line + "\n")); err != nil {
			t.Fatal(err)
		}
	}
	live := thinkingVisible(vt)
	for i := 1; i <= 30; i++ {
		needle := fmt.Sprintf("reasoning line %02d", i)
		if got := strings.Count(live, needle); got != 1 {
			t.Fatalf("live %q count = %d, want 1\n%s", needle, got, live)
		}
	}
	screen := vt.Text()
	if !strings.Contains(screen, "test-model") {
		t.Fatalf("footer is not visible on the live screen:\n%s", screen)
	}
	borderRows := 0
	for _, row := range strings.Split(screen, "\n") {
		if strings.Contains(row, "─") && strings.Trim(row, "─ ") == "" {
			borderRows++
		}
	}
	if borderRows != 2 {
		t.Fatalf("editor borders = %d rows, want 2:\n%s", borderRows, screen)
	}
	if err := c.CloseThinking(); err != nil {
		t.Fatal(err)
	}
	after := thinkingVisible(vt)
	for i := 1; i <= 30; i++ {
		needle := fmt.Sprintf("reasoning line %02d", i)
		if got := strings.Count(after, needle); got != 1 {
			t.Fatalf("after %q count = %d, want 1\n%s", needle, got, after)
		}
	}
}

func TestThinkingLongSingleLineStreamsIntoScrollback(t *testing.T) {
	c, vt, _ := newTestConsole(40, 12)
	c.SetPresentation(PresentationSnapshot{NoColor: true})
	words := make([]string, 40)
	for i := range words {
		words[i] = fmt.Sprintf("word%02d", i)
	}
	if _, err := c.ThinkingWrite([]byte(strings.Join(words, " "))); err != nil {
		t.Fatal(err)
	}
	live := thinkingVisible(vt)
	for _, word := range words {
		if got := strings.Count(live, word); got != 1 {
			t.Fatalf("live %q count = %d, want 1\n%s", word, got, live)
		}
	}
}

func TestThinkingListsStreamIntoScrollback(t *testing.T) {
	t.Run("flat", func(t *testing.T) {
		c, vt, _ := newTestConsole(80, 12)
		c.SetPresentation(PresentationSnapshot{NoColor: true})
		for i := 1; i <= 20; i++ {
			if _, err := c.ThinkingWrite([]byte(fmt.Sprintf("- list item %02d with enough words to wrap\n", i))); err != nil {
				t.Fatal(err)
			}
		}
		live := thinkingVisible(vt)
		for i := 1; i <= 20; i++ {
			needle := fmt.Sprintf("list item %02d", i)
			if got := strings.Count(live, needle); got != 1 {
				t.Fatalf("live %q count = %d, want 1\n%s", needle, got, live)
			}
		}
	})
	t.Run("ordered", func(t *testing.T) {
		c, vt, _ := newTestConsole(80, 12)
		c.SetPresentation(PresentationSnapshot{NoColor: true})
		for i := 1; i <= 12; i++ {
			if _, err := c.ThinkingWrite([]byte(fmt.Sprintf("%d. ordered item %02d\n", i, i))); err != nil {
				t.Fatal(err)
			}
		}
		live := thinkingVisible(vt)
		for i := 1; i <= 12; i++ {
			needle := fmt.Sprintf("ordered item %02d", i)
			if got := strings.Count(live, needle); got != 1 {
				t.Fatalf("live %q count = %d, want 1\n%s", needle, got, live)
			}
		}
	})
	t.Run("nested", func(t *testing.T) {
		c, vt, _ := newTestConsole(80, 12)
		c.SetPresentation(PresentationSnapshot{NoColor: true})
		for i := 1; i <= 20; i++ {
			text := fmt.Sprintf("- parent %02d\n  - child %02d\n", i, i)
			if _, err := c.ThinkingWrite([]byte(text)); err != nil {
				t.Fatal(err)
			}
		}
		live := thinkingVisible(vt)
		for i := 1; i <= 20; i++ {
			for _, kind := range []string{"parent", "child"} {
				needle := fmt.Sprintf("%s %02d", kind, i)
				if got := strings.Count(live, needle); got != 1 {
					t.Fatalf("live %q count = %d, want 1\n%s", needle, got, live)
				}
			}
		}
		if err := c.CloseThinking(); err != nil {
			t.Fatal(err)
		}
		after := thinkingVisible(vt)
		for i := 1; i <= 20; i++ {
			for _, kind := range []string{"parent", "child"} {
				needle := fmt.Sprintf("%s %02d", kind, i)
				if got := strings.Count(after, needle); got != 1 {
					t.Fatalf("after %q count = %d, want 1\n%s", needle, got, after)
				}
			}
		}
	})
}

func TestThinkingBoldSpanNeverCommitsMarkers(t *testing.T) {
	t.Run("unterminated span on one line", func(t *testing.T) {
		c, vt, _ := newTestConsole(80, 12)
		c.SetPresentation(PresentationSnapshot{NoColor: true})
		for i := 1; i <= 20; i++ {
			if _, err := c.ThinkingWrite([]byte(fmt.Sprintf("plain line %02d\n", i))); err != nil {
				t.Fatal(err)
			}
		}
		// The span closes only in the second delta, on the same source line.
		if _, err := c.ThinkingWrite([]byte("intro **bold")); err != nil {
			t.Fatal(err)
		}
		if live := strings.Join(vt.Scrollback, "\n"); strings.Contains(live, "**") {
			t.Fatalf("scrollback holds an unterminated span:\n%s", live)
		}
		if _, err := c.ThinkingWrite([]byte("text** tail\n")); err != nil {
			t.Fatal(err)
		}
		if err := c.CloseThinking(); err != nil {
			t.Fatal(err)
		}
		visible := thinkingVisible(vt)
		if strings.Contains(visible, "**") {
			t.Fatalf("literal bold markers remain:\n%s", visible)
		}
		if got := strings.Count(visible, "intro"); got != 1 {
			t.Fatalf("intro count = %d, want 1\n%s", got, visible)
		}
	})
	t.Run("span never closes", func(t *testing.T) {
		c, vt, _ := newTestConsole(80, 12)
		c.SetPresentation(PresentationSnapshot{NoColor: true})
		for i := 1; i <= 20; i++ {
			line := fmt.Sprintf("bold line %02d **still bold", i)
			if _, err := c.ThinkingWrite([]byte(line + "\n")); err != nil {
				t.Fatal(err)
			}
		}
		if live := strings.Join(vt.Scrollback, "\n"); strings.Contains(live, "**") {
			t.Fatalf("scrollback holds an unclosed span:\n%s", live)
		}
		if err := c.CloseThinking(); err != nil {
			t.Fatal(err)
		}
		visible := thinkingVisible(vt)
		for i := 1; i <= 20; i++ {
			needle := fmt.Sprintf("bold line %02d", i)
			if got := strings.Count(visible, needle); got != 1 {
				t.Fatalf("%q count = %d, want 1\n%s", needle, got, visible)
			}
		}
	})
}

// TestThinkingTableDoesNotCorruptScrollback covers the case that cannot be
// repaired after the fact: a table already drawn is rebuilt at a new column
// width, so appending only the new rows would leave two mismatched shapes.
func TestThinkingTableDoesNotCorruptScrollback(t *testing.T) {
	c, vt, _ := newTestConsole(80, 12)
	c.SetPresentation(PresentationSnapshot{NoColor: true})
	lines := []string{
		"| h1 | h2 | h3 |",
		"|---|---|---|",
		"| r01c1 | r01c2 | r01c3 |",
		"| r02c1 | r02c2 | r02c3 |",
		"| r03c1 | r03c2 | r03c3 |",
		"| r04c1 | r04c2 | r04c3 |",
	}
	for _, line := range lines {
		if _, err := c.ThinkingWrite([]byte(line + "\n")); err != nil {
			t.Fatal(err)
		}
	}
	c.RequestFullRedraw()
	c.Draw()
	// A much wider cell forces every earlier row to be padded differently.
	for _, line := range []string{
		"| r05c1 | r05c2 | r05c3 |",
		"| r06c1-very-wide-cell | r06c2 | r06c3 |",
		"| r07c1 | r07c2 | r07c3 |",
	} {
		if _, err := c.ThinkingWrite([]byte(line + "\n")); err != nil {
			t.Fatal(err)
		}
	}
	if err := c.CloseThinking(); err != nil {
		t.Fatal(err)
	}
	after := thinkingVisible(vt)
	for _, needle := range []string{"r01c1", "r02c1", "r03c1", "r04c1", "r05c1", "r06c1", "r07c1"} {
		if got := strings.Count(after, needle); got != 1 {
			t.Fatalf("after %q count = %d, want 1\n%s", needle, got, after)
		}
	}
	// Every rendered table row must share one column width.
	width := -1
	rows := 0
	for _, row := range strings.Split(after, "\n") {
		if !strings.Contains(row, "c1") {
			continue
		}
		rows++
		if w := CellWidth(row); width < 0 {
			width = w
		} else if w != width {
			t.Fatalf("table rows have different widths %d and %d:\n%s", width, w, after)
		}
	}
	if rows < 7 {
		t.Fatalf("table rows found = %d, want at least 7:\n%s", rows, after)
	}
}

func TestThinkingQuoteAndIndentedCodeStream(t *testing.T) {
	t.Run("quote", func(t *testing.T) {
		c, vt, _ := newTestConsole(80, 12)
		c.SetPresentation(PresentationSnapshot{NoColor: true})
		for i := 1; i <= 20; i++ {
			if _, err := c.ThinkingWrite([]byte(fmt.Sprintf("> quote line %02d with enough words to wrap\n", i))); err != nil {
				t.Fatal(err)
			}
		}
		live := thinkingVisible(vt)
		for i := 1; i <= 20; i++ {
			needle := fmt.Sprintf("quote line %02d", i)
			if got := strings.Count(live, needle); got != 1 {
				t.Fatalf("live %q count = %d, want 1\n%s", needle, got, live)
			}
		}
	})
	t.Run("indented code", func(t *testing.T) {
		c, vt, _ := newTestConsole(80, 12)
		c.SetPresentation(PresentationSnapshot{NoColor: true})
		for i := 1; i <= 20; i++ {
			if _, err := c.ThinkingWrite([]byte(fmt.Sprintf("    code line %02d with enough words to wrap\n", i))); err != nil {
				t.Fatal(err)
			}
		}
		live := thinkingVisible(vt)
		for i := 1; i <= 20; i++ {
			needle := fmt.Sprintf("code line %02d", i)
			if got := strings.Count(live, needle); got != 1 {
				t.Fatalf("live %q count = %d, want 1\n%s", needle, got, live)
			}
		}
	})
}

func TestThinkingResizeRebuildsOnce(t *testing.T) {
	c, vt, _ := newTestConsole(80, 12)
	c.SetPresentation(PresentationSnapshot{NoColor: true})
	for i := 1; i <= 30; i++ {
		if _, err := c.ThinkingWrite([]byte(fmt.Sprintf("resize line %02d with enough words to wrap\n", i))); err != nil {
			t.Fatal(err)
		}
	}
	vt.Resize(60, 12)
	c.SizeFunc = func() (int, int) { return 60, 12 }
	c.RequestFullRedraw()
	c.Draw()
	live := thinkingVisible(vt)
	for i := 1; i <= 30; i++ {
		needle := fmt.Sprintf("resize line %02d", i)
		if got := strings.Count(live, needle); got != 1 {
			t.Fatalf("after resize %q count = %d, want 1\n%s", needle, got, live)
		}
	}
}

func TestThinkingHideShowMidRun(t *testing.T) {
	c, vt, _ := newTestConsole(80, 12)
	c.SetPresentation(PresentationSnapshot{NoColor: true})
	for i := 1; i <= 20; i++ {
		if _, err := c.ThinkingWrite([]byte(fmt.Sprintf("toggle line %02d with enough words to wrap\n", i))); err != nil {
			t.Fatal(err)
		}
	}
	c.SetPresentation(PresentationSnapshot{NoColor: true, Display: DisplaySettings{HideThinking: true}})
	c.RequestFullRedraw()
	c.Draw()
	c.SetPresentation(PresentationSnapshot{NoColor: true})
	c.RequestFullRedraw()
	c.Draw()
	c.SetDeferDraw(false)
	c.Draw()
	for i := 21; i <= 25; i++ {
		if _, err := c.ThinkingWrite([]byte(fmt.Sprintf("toggle line %02d with enough words to wrap\n", i))); err != nil {
			t.Fatal(err)
		}
	}
	live := thinkingVisible(vt)
	for i := 1; i <= 25; i++ {
		needle := fmt.Sprintf("toggle line %02d", i)
		if got := strings.Count(live, needle); got != 1 {
			t.Fatalf("after toggle %q count = %d, want 1\n%s", needle, got, live)
		}
	}
}

func TestThinkingMultiParagraphRedraw(t *testing.T) {
	c, vt, _ := newTestConsole(80, 12)
	c.SetPresentation(PresentationSnapshot{NoColor: true})
	for i := 1; i <= 10; i++ {
		if _, err := c.ThinkingWrite([]byte(fmt.Sprintf("para one line %02d\n", i))); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := c.ThinkingWrite([]byte("\n")); err != nil {
		t.Fatal(err)
	}
	for i := 11; i <= 40; i++ {
		if _, err := c.ThinkingWrite([]byte(fmt.Sprintf("para two line %02d\n", i))); err != nil {
			t.Fatal(err)
		}
	}
	c.RequestFullRedraw()
	c.Draw()
	for i := 41; i <= 45; i++ {
		if _, err := c.ThinkingWrite([]byte(fmt.Sprintf("para two line %02d\n", i))); err != nil {
			t.Fatal(err)
		}
	}
	live := thinkingVisible(vt)
	for i := 1; i <= 45; i++ {
		needle := fmt.Sprintf("line %02d", i)
		if got := strings.Count(live, needle); got != 1 {
			t.Fatalf("multi-para redraw %q count = %d, want 1\n%s", needle, got, live)
		}
	}
	rows := append(append([]string(nil), vt.Scrollback...), strings.Split(vt.Text(), "\n")...)
	lastFirst, firstSecond := -1, -1
	for i, row := range rows {
		if strings.Contains(row, "para one line 10") {
			lastFirst = i
		}
		if firstSecond < 0 && strings.Contains(row, "para two line 11") {
			firstSecond = i
		}
	}
	if lastFirst < 0 || firstSecond < 0 {
		t.Fatalf("paragraph rows not found: lastFirst=%d firstSecond=%d\n%q", lastFirst, firstSecond, rows)
	}
	if got := firstSecond - lastFirst; got != 2 {
		t.Fatalf("paragraph gap = %d rows, want 2\n%q", got, rows)
	}
}

func TestStreamingThinkingBlockRendersItalic(t *testing.T) {
	doc, err := pitheme.BuiltinDocument("dark")
	if err != nil {
		t.Fatal(err)
	}
	palette, err := doc.Compile(pitheme.ColorModeTruecolor)
	if err != nil {
		t.Fatal(err)
	}
	rows := FormatDocumentBlock(DocumentBlock{Kind: DocumentThinking, Text: "weigh options", Streaming: true},
		80, PresentationSnapshot{Display: DefaultDisplaySettings(), Theme: palette})
	joined := strings.Join(rows, "\n")
	if !strings.Contains(joined, "\x1b[3m") {
		t.Fatalf("streaming thinking block is not italic: %q", joined)
	}
	if !strings.Contains(joined, "weigh options") {
		t.Fatalf("streaming thinking block lost its text: %q", joined)
	}
}

// TestThinkingByteAtATimeDeltas covers fragmented delivery: a provider or a
// lossy link can split the reasoning into one-byte deltas.
func TestThinkingByteAtATimeDeltas(t *testing.T) {
	c, vt, _ := newTestConsole(80, 12)
	c.SetPresentation(PresentationSnapshot{NoColor: true})
	var source strings.Builder
	for i := 1; i <= 30; i++ {
		source.WriteString(fmt.Sprintf("fragmented line %02d with enough words to wrap\n", i))
	}
	for i := 0; i < source.Len(); i++ {
		if _, err := c.ThinkingWrite([]byte{source.String()[i]}); err != nil {
			t.Fatal(err)
		}
	}
	live := thinkingVisible(vt)
	for i := 1; i <= 30; i++ {
		needle := fmt.Sprintf("fragmented line %02d", i)
		if got := strings.Count(live, needle); got != 1 {
			t.Fatalf("live %q count = %d, want 1\n%s", needle, got, live)
		}
	}
	if err := c.CloseThinking(); err != nil {
		t.Fatal(err)
	}
	after := thinkingVisible(vt)
	for i := 1; i <= 30; i++ {
		needle := fmt.Sprintf("fragmented line %02d", i)
		if got := strings.Count(after, needle); got != 1 {
			t.Fatalf("after %q count = %d, want 1\n%s", needle, got, after)
		}
	}
}

// TestThinkingCRLFAndLoneCR covers a provider that sends CRLF, and a CR that
// arrives at the end of one delta with its LF at the start of the next.
func TestThinkingCRLFAndLoneCR(t *testing.T) {
	c, vt, _ := newTestConsole(80, 12)
	c.SetPresentation(PresentationSnapshot{NoColor: true})
	for i := 1; i <= 12; i++ {
		if _, err := c.ThinkingWrite([]byte(fmt.Sprintf("crlf line %02d with enough words to wrap\r\n", i))); err != nil {
			t.Fatal(err)
		}
	}
	for i := 13; i <= 20; i++ {
		if _, err := c.ThinkingWrite([]byte(fmt.Sprintf("crlf line %02d with enough words to wrap\r", i))); err != nil {
			t.Fatal(err)
		}
		if _, err := c.ThinkingWrite([]byte("\n")); err != nil {
			t.Fatal(err)
		}
	}
	live := thinkingVisible(vt)
	for i := 1; i <= 20; i++ {
		needle := fmt.Sprintf("crlf line %02d", i)
		if got := strings.Count(live, needle); got != 1 {
			t.Fatalf("live %q count = %d, want 1\n%s", needle, got, live)
		}
	}
	if err := c.CloseThinking(); err != nil {
		t.Fatal(err)
	}
	after := thinkingVisible(vt)
	for i := 1; i <= 20; i++ {
		needle := fmt.Sprintf("crlf line %02d", i)
		if got := strings.Count(after, needle); got != 1 {
			t.Fatalf("after %q count = %d, want 1\n%s", needle, got, after)
		}
	}
}

// TestThinkingWideChars covers double-width graphemes, which wrap at a
// different column count than their byte length suggests.
func TestThinkingWideChars(t *testing.T) {
	c, vt, _ := newTestConsole(40, 12)
	c.SetPresentation(PresentationSnapshot{NoColor: true})
	for i := 1; i <= 15; i++ {
		line := fmt.Sprintf("思考行 w%02d 带有足够多的词来换行 😀", i)
		if _, err := c.ThinkingWrite([]byte(line + "\n")); err != nil {
			t.Fatal(err)
		}
	}
	for _, phase := range []string{"live", "closed"} {
		if phase == "closed" {
			if err := c.CloseThinking(); err != nil {
				t.Fatal(err)
			}
		}
		visible := thinkingVisible(vt)
		for i := 1; i <= 15; i++ {
			needle := fmt.Sprintf("w%02d", i)
			if got := strings.Count(visible, needle); got != 1 {
				t.Fatalf("%s %q count = %d, want 1\n%s", phase, needle, got, visible)
			}
		}
		// VT text inserts spaces between wide cells, so count the glyphs too.
		for _, glyph := range []string{"思", "换", "😀"} {
			if got := strings.Count(visible, glyph); got != 15 {
				t.Fatalf("%s %q count = %d, want 15\n%s", phase, glyph, got, visible)
			}
		}
	}
}

// TestThinkingUnbreakableOversizeLine covers a line larger than the active
// source budget: the stream switches to sanitized fallback rows.
func TestThinkingUnbreakableOversizeLine(t *testing.T) {
	c, vt, _ := newTestConsole(40, 12)
	c.SetPresentation(PresentationSnapshot{NoColor: true})
	line := strings.Repeat("x", maxMarkdownActiveSource+128)
	if _, err := c.ThinkingWrite([]byte(line)); err != nil {
		t.Fatal(err)
	}
	if err := c.CloseThinking(); err != nil {
		t.Fatal(err)
	}
	visible := thinkingVisible(vt)
	if got := strings.Count(visible, "x"); got != len(line) {
		t.Fatalf("x count = %d, want %d", got, len(line))
	}
}
