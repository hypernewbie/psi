package terminal

import (
	"strings"
	"testing"
)

func renderMarkdownText(source string, width int) []string {
	rows := []string{}
	for _, row := range RenderMarkdown(source, width, true) {
		rows = append(rows, strings.TrimRight(row, " "))
	}
	return rows
}

func TestRenderMarkdownRendersGitHubTables(t *testing.T) {
	rows := renderMarkdownText("| A | B |\n| --- | --- |\n| 1 | 2 |", 40)
	want := []string{
		"  ┌───┬───┐",
		"  │ A │ B │",
		"  ├───┼───┤",
		"  │ 1 │ 2 │",
		"  └───┴───┘",
	}
	if strings.Join(rows, "\n") != strings.Join(want, "\n") {
		t.Fatalf("table layout\n got: %q\nwant: %q", rows, want)
	}
}

func TestRenderMarkdownTableSeparatesEveryRow(t *testing.T) {
	rows := renderMarkdownText("| Name | Age |\n| --- | --- |\n| Alice | 30 |\n| Bob | 25 |", 60)
	separators := 0
	for _, row := range rows {
		if strings.Contains(row, "┼") {
			separators++
		}
	}
	if separators != 2 {
		t.Fatalf("table separators = %d, want a header and one row separator: %q", separators, rows)
	}
}

func TestRenderMarkdownTableWrapsCellContent(t *testing.T) {
	rows := renderMarkdownText("| Header |\n| --- |\n| This is a very long cell content that should wrap |", 25)
	for _, row := range rows {
		if CellWidth(row) > 24 {
			t.Fatalf("table row exceeds the width: %q", row)
		}
	}
	joined := strings.Join(rows, "\n")
	for _, want := range []string{"This is a very", "long cell content", "that should wrap"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("wrapped cell lost %q: %q", want, joined)
		}
	}
}

func TestRenderMarkdownTableFallsBackToSourceWhenTooNarrow(t *testing.T) {
	rows := renderMarkdownText("| A | B | C |\n| --- | --- | --- |\n| 1 | 2 | 3 |", 12)
	joined := strings.Join(rows, "\n")
	if !strings.Contains(joined, "| A | B |") {
		t.Fatalf("narrow table did not fall back to the Markdown source: %q", joined)
	}
	if strings.Contains(joined, "┌") {
		t.Fatalf("narrow table drew borders it cannot fit: %q", joined)
	}
}

func TestRenderMarkdownTableKeepsInlineFormattingInCells(t *testing.T) {
	colored := strings.Join(RenderMarkdown("| Name |\n| --- |\n| **bold** |", 40, false), "\n")
	if !strings.Contains(colored, "\x1b[1mbold\x1b[0m") {
		t.Fatalf("table cell lost bold styling: %q", colored)
	}
	plain := strings.Join(RenderMarkdown("| Name |\n| --- |\n| [link](https://example.test) |", 40, true), "\n")
	if !strings.Contains(plain, "link (https://example.test)") {
		t.Fatalf("table cell lost the visible link destination: %q", plain)
	}
}

func TestRenderMarkdownTableIgnoresAlignmentLikePi(t *testing.T) {
	rows := renderMarkdownText("| Left | Right |\n| :--- | ---: |\n| A | B |", 40)
	joined := strings.Join(rows, "\n")
	if !strings.Contains(joined, "│ A    │ B     │") {
		t.Fatalf("table alignment changed the cell layout: %q", joined)
	}
}

func TestRenderMarkdownAppliesStrictStrikethrough(t *testing.T) {
	rows := renderMarkdownText("~~struck~~ and ~single~ and unmatched ~~ here", 60)
	joined := strings.Join(rows, "\n")
	if !strings.Contains(joined, "struck and ~single~ and unmatched ~~ here") {
		t.Fatalf("strikethrough did not follow Pi's strict rule: %q", joined)
	}
	colored := strings.Join(RenderMarkdown("~~struck~~", 40, false), "\n")
	if !strings.Contains(colored, "\x1b[9mstruck\x1b[0m") {
		t.Fatalf("strikethrough styling missing: %q", colored)
	}
}

func TestRenderMarkdownStrikethroughKeepsCodeSpansLiteral(t *testing.T) {
	joined := strings.Join(renderMarkdownText("`~~code~~`", 40), "\n")
	if !strings.Contains(joined, "~~code~~") {
		t.Fatalf("code span was struck through: %q", joined)
	}
}

func TestRenderMarkdownRendersTaskListMarkers(t *testing.T) {
	joined := strings.Join(renderMarkdownText("- [ ] beep\n- [x] boop", 40), "\n")
	if !strings.Contains(joined, "- [ ] beep") || !strings.Contains(joined, "- [x] boop") {
		t.Fatalf("task list markers missing: %q", joined)
	}
}

func TestRenderMarkdownAutolinksBareURLs(t *testing.T) {
	joined := strings.Join(renderMarkdownText("Visit https://example.com/docs now", 60), "\n")
	if !strings.Contains(joined, "https://example.com/docs") {
		t.Fatalf("bare URL was not rendered: %q", joined)
	}
	colored := strings.Join(RenderMarkdown("Visit https://example.com/docs now", 60, false), "\n")
	if !strings.Contains(colored, "\x1b[34;4mhttps://example.com/docs\x1b[0m") {
		t.Fatalf("bare URL is not styled as a link: %q", colored)
	}
	www := strings.Join(renderMarkdownText("Visit www.example.com now", 60), "\n")
	if !strings.Contains(www, "www.example.com (http://www.example.com)") {
		t.Fatalf("www link did not show its destination: %q", www)
	}
}

func TestRenderMarkdownResolvesBackslashEscapes(t *testing.T) {
	joined := strings.Join(renderMarkdownText("a \\*escaped\\* text and \\`tick\\`", 60), "\n")
	if !strings.Contains(joined, "a *escaped* text and `tick`") {
		t.Fatalf("backslash escapes were not resolved: %q", joined)
	}
}

func TestRenderMarkdownKeepsSoftLineBreaks(t *testing.T) {
	rows := renderMarkdownText("line one\nline two", 40)
	if len(rows) != 2 || rows[0] != "  line one" || rows[1] != "  line two" {
		t.Fatalf("soft line break was collapsed: %q", rows)
	}
}

func TestRenderMarkdownKeepsQuoteBorderAtMessageMargin(t *testing.T) {
	rows := renderMarkdownText("> quoted paragraph", 40)
	if len(rows) == 0 || !strings.HasPrefix(rows[0], "  │ quoted paragraph") {
		t.Fatalf("quote border did not follow the message margin: %q", rows)
	}
}

func TestRenderMarkdownKeepsCodeBlockAtMessageMargin(t *testing.T) {
	rows := renderMarkdownText("```go\nfmt.Println(1)\n```", 40)
	want := []string{"  ```go", "    fmt.Println(1)", "  ```"}
	if strings.Join(rows, "\n") != strings.Join(want, "\n") {
		t.Fatalf("code block margin\n got: %q\nwant: %q", rows, want)
	}
}

func TestMarkdownStreamRendersTablesLikeTheWholeMessage(t *testing.T) {
	source := "Intro paragraph.\n\n| A | B |\n| --- | --- |\n| 1 | 2 |\n\nAfter the table."
	want := strings.Join(RenderMarkdown(source, 48, true), "\n")
	got, _, _ := renderMarkdownChunks([]string{source}, 48)
	if strings.Join(got, "\n") != want {
		t.Fatalf("streamed table differs from the whole message renderer\nwant:\n%s\ngot:\n%s", want, strings.Join(got, "\n"))
	}
}

func TestMarkdownStrictStrikeMarksFollowPiRules(t *testing.T) {
	cases := []struct {
		text    string
		strikes int
	}{
		{text: "~~a~~", strikes: 1},
		{text: "~a~", strikes: 0},
		{text: "~~ a~~", strikes: 0},
		{text: "~~a ~~", strikes: 1},
		{text: "~~a", strikes: 0},
		{text: "a ~~ b ~~ c", strikes: 0},
		{text: "~~~a~~~", strikes: 0},
	}
	for _, tc := range cases {
		marks := markdownStrictStrikeMarks(tc.text)
		strikes := 0
		for _, mark := range marks {
			if !mark.remove {
				strikes++
			}
		}
		if strikes != tc.strikes {
			t.Errorf("markdownStrictStrikeMarks(%q) strikes = %d, want %d", tc.text, strikes, tc.strikes)
		}
	}
}

func TestMarkdownTableColumnWidthsStayInsideTheBudget(t *testing.T) {
	widths := markdownTableColumnWidths([]int{40, 12, 7}, []int{6, 4, 3}, 30)
	total := 0
	for _, width := range widths {
		if width < 1 {
			t.Fatalf("column width %d is unusable: %v", width, widths)
		}
		total += width
	}
	if total > 30 {
		t.Fatalf("column widths %v exceed the cell budget", widths)
	}
}

// TestRenderMarkdownTableDetectionKeepsPiTextForMismatchedRows documents the
// one known table detection difference. Pi rejects a table whose header and
// delimiter rows disagree and keeps the source as text. psi keeps the same
// text, but the lazy continuation of a list item loses the source space that
// Pi keeps.
func TestRenderMarkdownTableDetectionKeepsPiTextForMismatchedRows(t *testing.T) {
	got := RenderMarkdown(">* 0\n -|-\n0000000000", 24, true)
	want := []string{"  │ - 0", "  │   -|-", "  │   0000000000"}
	if len(got) != len(want) {
		t.Fatalf("mismatched table rows rendered %d lines, want %d: %q", len(got), len(want), got)
	}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("mismatched table row %d = %q, want %q", index, got[index], want[index])
		}
	}
}

// TestRenderMarkdownTableWithoutOuterPipesMatchesPi covers the GFM table form
// without the outer pipes. Pi accepts it and so does psi.
func TestRenderMarkdownTableWithoutOuterPipesMatchesPi(t *testing.T) {
	got := RenderMarkdown("Name | Age\n--- | ---\nAlice | 30", 60, true)
	joined := strings.Join(got, "\n")
	if !strings.Contains(joined, "┌") || !strings.Contains(joined, "│ Alice │ 30  │") {
		t.Fatalf("table without outer pipes was not rendered as a table: %q", joined)
	}
}
