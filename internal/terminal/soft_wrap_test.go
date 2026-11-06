package terminal

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/vt"

	pitheme "psi/internal/theme"
)

func softDisplay() DisplaySettings {
	display := DefaultDisplaySettings()
	display.SoftWrap = true
	return display
}

func TestSoftWrapCommittedParagraphIsOneLogicalRow(t *testing.T) {
	display := softDisplay()
	source := "alpha beta gamma delta"
	soft := FormatAssistantMessageWithDisplay(source, 12, true, display)
	if len(soft) != 2 {
		t.Fatalf("soft rows = %d, want one row and one separator: %q", len(soft), soft)
	}
	if soft[0] != "  alpha beta gamma delta" {
		t.Fatalf("soft row = %q", soft[0])
	}

	hard := FormatAssistantMessageWithDisplay(source, 12, true, DefaultDisplaySettings())
	if len(hard) <= 2 {
		t.Fatalf("hard mode stopped wrapping: %q", hard)
	}
}

func TestSoftWrapJoinsSourceSoftBreaks(t *testing.T) {
	display := softDisplay()
	rows := FormatAssistantMessageWithDisplay("alpha\nbeta", 40, true, display)
	if len(rows) == 0 || rows[0] != "  alpha beta" {
		t.Fatalf("soft source break was not joined: %q", rows)
	}
}

func TestSoftWrapKeepsHardBreaks(t *testing.T) {
	display := softDisplay()
	rows := FormatAssistantMessageWithDisplay("alpha  \nbeta", 40, true, display)
	if len(rows) != 3 || rows[0] != "  alpha" || rows[1] != "  beta" {
		t.Fatalf("hard break rows = %q", rows)
	}
}

func TestSoftWrapExactWidthBoundaryDoesNotAddBlankRow(t *testing.T) {
	const width = 20
	model := vt.NewEmulator(width, 6)
	defer model.Close()

	// Two prefix cells plus eighteen text cells make one full terminal row.
	display := softDisplay()
	rows := FormatAssistantMessageWithDisplay(strings.Repeat("x", 18), width, true, display)
	payload := rows[0] + "\r\nMARK\r\n"
	if _, err := model.Write([]byte(payload)); err != nil {
		t.Fatal(err)
	}
	if got := vtRow(model, width, 1); got != "MARK" {
		t.Fatalf("row after an exact-width soft row = %q, want MARK", got)
	}
	if got := vtRow(model, width, 2); got != "" {
		t.Fatalf("exact-width soft row added a blank row: %q", got)
	}
}

func TestSoftWrapStreamedParagraphCommitsAsOneRow(t *testing.T) {
	stream := NewMarkdownStream(12, true)
	stream.SetDisplay(softDisplay())
	stable, _ := stream.Feed("alpha beta gamma delta\n\nsecond block")
	stable = append(stable, stream.Finish()...)
	if len(stable) == 0 || stable[0] != "  alpha beta gamma delta" {
		t.Fatalf("streamed soft commit = %q", stable)
	}

	hard := NewMarkdownStream(12, true)
	hard.SetDisplay(DefaultDisplaySettings())
	stable, _ = hard.Feed("alpha beta gamma delta\n\nsecond block")
	stable = append(stable, hard.Finish()...)
	if len(stable) < 3 {
		t.Fatalf("hard stream did not wrap: %q", stable)
	}
}

func vtRow(model *vt.Emulator, width, y int) string {
	var row strings.Builder
	for x := 0; x < width; x++ {
		if cell := model.CellAt(x, y); cell != nil {
			row.WriteString(cell.Content)
		}
	}
	return strings.TrimRight(row.String(), " ")
}

func TestSoftWrapUserMessageKeepsOneRowWithMarker(t *testing.T) {
	display := softDisplay()
	source := "user request that is much wider than the terminal"
	rows := FormatUserMessageWithDisplay(source, 20, true, display)
	if len(rows) != 2 || rows[0] != "  › "+source {
		t.Fatalf("soft user rows = %q", rows)
	}

	hard := FormatUserMessageWithDisplay(source, 20, true, DefaultDisplaySettings())
	if len(hard) <= 2 {
		t.Fatalf("hard user message stopped wrapping: %q", hard)
	}
	if !strings.HasPrefix(hard[0], "  › ") {
		t.Fatalf("hard user marker = %q", hard[0])
	}
}

func TestSoftWrapMetadataIsOneRow(t *testing.T) {
	display := softDisplay()
	text := "a metadata line that is wider than the terminal width"
	rows := FormatMetadataMessage("[compaction] ", text, 24, true, display)
	if len(rows) != 2 || rows[0] != "  [compaction] "+text {
		t.Fatalf("soft metadata rows = %q", rows)
	}

	hard := FormatMetadataMessage("[compaction] ", text, 24, true, DefaultDisplaySettings())
	if len(hard) <= 2 {
		t.Fatalf("hard metadata stopped wrapping: %q", hard)
	}
}

func TestSoftWrapNoticeBreaksOnlyOnSourceNewlines(t *testing.T) {
	display := softDisplay()
	rows := formatNoticeWithDisplay(NoticeWarning, "first line\nsecond line", 12, true, display)
	want := []string{" Warning: first line", " second line"}
	if strings.Join(rows, "|") != strings.Join(want, "|") {
		t.Fatalf("soft notice rows = %q", rows)
	}

	hard := formatNoticeWithDisplay(NoticeWarning, "first line\nsecond line", 12, true, DefaultDisplaySettings())
	if len(hard) <= 2 {
		t.Fatalf("hard notice stopped wrapping: %q", hard)
	}
}

func TestSoftWrapRawDocumentLines(t *testing.T) {
	display := softDisplay()
	long := strings.Repeat("r", 40)
	rows := formatRawDocument("short\n"+long, 10, display)
	if len(rows) != 2 || rows[1] != long {
		t.Fatalf("soft raw rows = %q", rows)
	}

	hard := formatRawDocument("short\n"+long, 10, DefaultDisplaySettings())
	if len(hard) <= 2 {
		t.Fatalf("hard raw document stopped wrapping: %q", hard)
	}
}

func TestSoftWrapCodeLineLongerThanWidth(t *testing.T) {
	display := softDisplay()
	code := "fmt.Println(\"" + strings.Repeat("x", 40) + "\")"
	source := "```go\n" + code + "\n```"
	rows := FormatAssistantMessageWithDisplay(source, 20, true, display)
	found := false
	for _, row := range rows {
		if row == "    "+code {
			found = true
		}
	}
	if !found {
		t.Fatalf("soft code line was wrapped: %q", rows)
	}

	hard := FormatAssistantMessageWithDisplay(source, 20, true, DefaultDisplaySettings())
	if strings.Contains(strings.Join(hard, "\n"), code) {
		t.Fatalf("hard code line stayed contiguous: %q", hard)
	}
}

func TestSoftWrapLeavesTablesHard(t *testing.T) {
	source := "| alpha | beta |\n|---|---|\n| one | two |"
	soft := FormatAssistantMessageWithDisplay(source, 40, true, softDisplay())
	hard := FormatAssistantMessageWithDisplay(source, 40, true, DefaultDisplaySettings())
	if strings.Join(soft, "|") != strings.Join(hard, "|") {
		t.Fatalf("soft mode changed a table:\nsoft=%q\nhard=%q", soft, hard)
	}
}

func TestSoftWrapThinkingBlockIsOneRow(t *testing.T) {
	display := softDisplay()
	source := "reasoning that is much wider than the terminal width"
	rows := FormatThinkingBlock(source, 20, true, display)
	if len(rows) != 2 || rows[0] != "  "+source {
		t.Fatalf("soft thinking rows = %q", rows)
	}
}

func TestSoftWrapLiveTailStaysHardUntilCommit(t *testing.T) {
	for _, prefix := range []string{"", "```go\n"} {
		t.Run(prefix, func(t *testing.T) {
			const text = "alpha beta gamma delta epsilon"
			stream := NewMarkdownStream(12, true)
			stream.SetDisplay(softDisplay())
			stable, _ := stream.Feed(prefix + text)
			tail := stream.DisplayTail()
			if len(tail) < 2 {
				t.Fatalf("live tail did not wrap: %q", tail)
			}
			for _, row := range tail {
				if cells := CellWidth(Sanitize(row)); cells > 11 {
					t.Fatalf("live tail exceeds the cursor-addressed row width: %q", row)
				}
			}
			stable = append(stable, stream.Finish()...)
			if !strings.Contains(strings.Join(stable, "\n"), text) {
				t.Fatalf("settled tail did not become a soft row: %q", stable)
			}
		})
	}
}

func TestSoftWrapOversizedFallbackStaysBoundedAndHard(t *testing.T) {
	const width = 20
	stream := NewMarkdownStream(width, true)
	stream.SetDisplay(softDisplay())
	stable, _ := stream.Feed(strings.Repeat("x", maxMarkdownActiveSource+4096))
	if stream.BufferedBytes() > maxMarkdownActiveSource {
		t.Fatalf("fallback buffer = %d, want <= %d", stream.BufferedBytes(), maxMarkdownActiveSource)
	}
	stable = append(stable, stream.Finish()...)
	if len(stable) == 0 {
		t.Fatal("oversized fallback emitted no rows")
	}
	for _, row := range stable {
		if width := CellWidth(Sanitize(row)); width > 20 {
			t.Fatalf("fallback row width = %d, want <= 20: %q", width, row)
		}
	}
}

func softTheme(t *testing.T) *pitheme.Theme {
	t.Helper()
	data, err := pitheme.BuiltinJSON("dark")
	if err != nil {
		t.Fatal(err)
	}
	doc, err := pitheme.Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	theme, err := doc.Compile(pitheme.ColorMode256)
	if err != nil {
		t.Fatal(err)
	}
	return theme
}

func TestSoftWrapSurfaceRowsUseEraseNotPadding(t *testing.T) {
	theme := softTheme(t)
	background, err := theme.BackgroundANSI("userMessageBg")
	if err != nil {
		t.Fatal(err)
	}
	display := softDisplay()

	row := FormatUserMessageWithDisplay("hello", 20, false, display, theme)[0]
	if !strings.Contains(row, background) || !strings.Contains(row, "\x1b[K") {
		t.Fatalf("soft surface row lacks background erase: %q", row)
	}
	if strings.HasSuffix(Sanitize(row), " ") {
		t.Fatalf("soft surface row has padding: %q", Sanitize(row))
	}

	hard := FormatUserMessageWithDisplay("hello", 20, false, DefaultDisplaySettings(), theme)[0]
	if !strings.Contains(hard, "\x1b[K") || strings.HasSuffix(Sanitize(hard), " ") {
		t.Fatalf("hard surface row did not switch to erase: %q", hard)
	}

	plain := FormatUserMessageWithDisplay("hello", 20, true, display, theme)[0]
	if strings.Contains(plain, "\x1b[K") || strings.HasSuffix(Sanitize(plain), " ") {
		t.Fatalf("NO_COLOR row gained fill: %q", plain)
	}

	noTheme := FormatUserMessageWithDisplay("hello", 20, false, display)[0]
	if strings.Contains(noTheme, "\x1b[K") || strings.HasSuffix(Sanitize(noTheme), " ") {
		t.Fatalf("nil-theme row gained fill: %q", noTheme)
	}
}

func TestSoftWrapWriterKeepsSurfaceFill(t *testing.T) {
	theme := softTheme(t)
	display := softDisplay()
	model := vt.NewEmulator(20, 5)
	defer model.Close()
	w := NewWriter(model)

	row := FormatUserMessageWithDisplay("hello", 20, false, display, theme)[0]
	if err := w.Emit(Frame{Tail: []string{row, "next"}, TailCellWidth: []int{CellWidth(Sanitize(row)), 4}, CursorRow: 1, CursorCol: 4, Width: 20, Height: 5}); err != nil {
		t.Fatal(err)
	}
	updated := FormatUserMessageWithDisplay("hello!", 20, false, display, theme)[0]
	if err := w.Emit(Frame{Tail: []string{updated, "next"}, TailCellWidth: []int{CellWidth(Sanitize(updated)), 4}, CursorRow: 1, CursorCol: 4, Width: 20, Height: 5}); err != nil {
		t.Fatal(err)
	}
	want := model.CellAt(5, 0).Style.Bg
	for _, x := range []int{18, 19} {
		cell := model.CellAt(x, 0)
		if want == nil || cell == nil || cell.Style.Bg != want {
			t.Fatalf("surface fill at (%d,0) = %#v, want background %v", x, cell, want)
		}
	}
}

func TestSoftWrapAppendKeepsSurfaceFill(t *testing.T) {
	theme := softTheme(t)
	for _, invalidated := range []bool{false, true} {
		model := vt.NewEmulator(20, 8)
		defer model.Close()
		w := NewWriter(model)
		if err := w.Emit(Frame{Tail: []string{"stale row", "stale footer"}, Width: 20, Height: 8}); err != nil {
			t.Fatal(err)
		}
		if invalidated {
			w.InvalidateTail()
		}
		row := FormatUserMessageWithDisplay("hello", 20, false, softDisplay(), theme)[0]
		if err := w.Emit(Frame{Append: row + "\r\n", Tail: []string{"TAIL"}, Width: 20, Height: 8}); err != nil {
			t.Fatal(err)
		}
		if got := vtRow(model, 20, 0); got != Sanitize(row) {
			t.Fatalf("invalidated=%t: committed row = %q, want %q", invalidated, got, Sanitize(row))
		}
		if got := vtRow(model, 20, 1); got != "TAIL" {
			t.Fatalf("invalidated=%t: tail row = %q, want TAIL", invalidated, got)
		}
		want := model.CellAt(5, 0).Style.Bg
		if got := model.CellAt(18, 0).Style.Bg; want == nil || got != want {
			t.Fatalf("invalidated=%t: append erased surface fill: got %v, want %v", invalidated, got, want)
		}
	}
}

func TestSoftWrapFullRedrawKeepsTailAdjacent(t *testing.T) {
	model := vt.NewEmulator(20, 8)
	defer model.Close()
	w := NewWriter(model)
	long := strings.Repeat("x", 40)
	if err := w.Emit(Frame{FullRedraw: true, DocumentRows: []string{long, "MARK"}, Tail: []string{"TAIL"}, Width: 20, Height: 8}); err != nil {
		t.Fatal(err)
	}
	if got := vtRow(model, 20, 2); got != "MARK" {
		t.Fatalf("row after a two-visual-row document row = %q, want MARK", got)
	}
	if got := vtRow(model, 20, 3); got != "TAIL" {
		t.Fatalf("tail row = %q, want TAIL", got)
	}
}

func TestSoftWrapSurfaceEraseAtExactWidthDoesNotWrap(t *testing.T) {
	theme := softTheme(t)
	for _, tc := range []struct {
		name string
		text string
		want []string
	}{
		{"one row", strings.Repeat("y", 16), []string{"  › " + strings.Repeat("y", 16)}},
		{"two rows", strings.Repeat("y", 36), []string{"  › " + strings.Repeat("y", 16), strings.Repeat("y", 20)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			row := FormatUserMessageWithDisplay(tc.text, 20, false, softDisplay(), theme)[0]
			model := vt.NewEmulator(20, 5)
			defer model.Close()
			if _, err := model.Write([]byte(row + "\r\nMARK\r\n")); err != nil {
				t.Fatal(err)
			}
			for i, want := range append(tc.want, "MARK") {
				if got := vtRow(model, 20, i); got != want {
					t.Fatalf("surface row %d: got %q, want %q", i, got, want)
				}
			}
		})
	}
}

func TestSoftWrapSurfaceFullRowAfterWideWrapDoesNotErase(t *testing.T) {
	// The wide glyph moves to the next row, leaving one unused cell behind.
	// Its final visual row is full even though the total cell width is 39.
	text := strings.Repeat("y", 15) + "界" + strings.Repeat("y", 18)
	row := FormatUserMessageWithDisplay(text, 20, false, softDisplay(), softTheme(t))[0]
	if strings.Contains(row, "\x1b[K") {
		t.Fatalf("erase would remove the last character after a wide wrap: %q", row)
	}
}

func TestSoftWrapResizedTailKeepsSurfaceFill(t *testing.T) {
	theme := softTheme(t)
	model := vt.NewEmulator(20, 5)
	defer model.Close()
	if _, err := model.WriteString("COMMITTED_SENTINEL\r\n"); err != nil {
		t.Fatal(err)
	}
	w := NewWriter(model)
	row := FormatUserMessageWithDisplay("hello", 20, false, DefaultDisplaySettings(), theme)[0]
	frame := Frame{Tail: []string{row, "next"}, CursorRow: 1, CursorCol: 4, Width: 20, Height: 5}
	if err := w.Emit(frame); err != nil {
		t.Fatal(err)
	}
	before := model.CellAt(18, 1).Style.Bg
	if before == nil {
		t.Fatal("initial surface fill missing")
	}
	model.Resize(24, 6)
	cursor := model.CursorPosition()
	frame.Width, frame.Height = 24, 6
	w.InvalidateTail()
	w.SetResizeCursorPosition(cursor.Y, cursor.X)
	if err := w.Emit(frame); err != nil {
		t.Fatal(err)
	}
	if got := vtRow(model, 24, 0); got != "COMMITTED_SENTINEL" {
		t.Fatalf("resize erased committed history: %q", got)
	}
	if got := model.CellAt(18, 1).Style.Bg; got != before {
		t.Fatalf("resize erased surface fill: got %v, want %v", got, before)
	}
}

func TestSoftWrapListHardBreakKeepsContinuationMarker(t *testing.T) {
	rows := FormatAssistantMessageWithDisplay("- first  \n  second", 20, true, softDisplay())
	if got, want := strings.Join(rows, "\n"), "  - first\n    second\n"; got != want {
		t.Fatalf("list hard break changed the marker: got %q, want %q", got, want)
	}
}

func TestFrameTailWidthIgnoresSGR(t *testing.T) {
	if got := frameTailWidth(Frame{Tail: []string{"\x1b[31mred\x1b[0m"}}, 0); got != 3 {
		t.Fatalf("frameTailWidth counted SGR bytes: %d", got)
	}
}

func TestSoftWrapKeepsContainerMarkersAtNarrowWidths(t *testing.T) {
	for _, tc := range []struct {
		name   string
		source string
		want   string
	}{
		{"nested list", "- outer\n    - inner\n        - nested", "          - nested"},
		{"nested quote", "> > > > quoted", "  │ │ │ │ quoted"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rows := FormatAssistantMessageWithDisplay(tc.source, 10, true, softDisplay())
			if !strings.Contains(strings.Join(rows, "\n"), tc.want) {
				t.Fatalf("soft mode lost container markers: got %q, want row %q", rows, tc.want)
			}
		})
	}
}

func TestSoftWrapHeadingListAndQuoteRows(t *testing.T) {
	display := softDisplay()
	source := "# heading that is much wider than the terminal width\n\n" +
		"- first item that is much wider than the terminal width\n\n" +
		"> quoted line that is much wider than the terminal width"
	rows := FormatAssistantMessageWithDisplay(source, 20, true, display)
	joined := strings.Join(rows, "\n")
	for _, want := range []string{
		"  heading that is much wider than the terminal width",
		"  - first item that is much wider than the terminal width",
		"  │ quoted line that is much wider than the terminal width",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("soft row %q missing from %q", want, rows)
		}
	}

	hard := FormatAssistantMessageWithDisplay(source, 20, true, DefaultDisplaySettings())
	for _, want := range []string{
		"  heading that is much wider than the terminal width",
		"  - first item that is much wider than the terminal width",
		"  │ quoted line that is much wider than the terminal width",
	} {
		if strings.Contains(strings.Join(hard, "\n"), want) {
			t.Fatalf("hard mode kept %q contiguous: %q", want, hard)
		}
	}
}
