package terminal

import (
	"reflect"
	"strings"
	"testing"
)

func TestFormatMessageUsesSharedPaddingWithoutRoleHeading(t *testing.T) {
	got := FormatMessage("hello", 80, true)
	want := []string{"  hello", ""}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("message rows=%#v want %#v", got, want)
	}
}

func TestRenderMarkdownRetainedSubset(t *testing.T) {
	input := "# Title\n\nA **bold** and *soft* with `code` and [docs](https://example.test/docs).\n\n- first item\n- second item\n  - nested\n\n> quoted line\n> next line\n\n```go\nfmt.Println(\"hi\")\n```\n\n---\n"
	got := RenderMarkdown(input, 60, true)
	want := []string{
		"  Title",
		"",
		"  A bold and soft with code and docs",
		"  (https://example.test/docs).",
		"",
		"  - first item",
		"  - second item",
		"      - nested",
		"",
		"│   quoted line next line",
		"",
		"```go",
		"  fmt.Println(\"hi\")",
		"```",
		"",
		strings.Repeat("─", 59),
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Markdown output mismatch\n got: %#v\nwant: %#v", got, want)
	}
}

func TestRenderMarkdownStylesAreSemanticAndNoColorSafe(t *testing.T) {
	colored := strings.Join(RenderMarkdown("## Heading\n\n**bold** *italic* `code` [link](https://example.test)", 80, false), "\n")
	if !strings.Contains(colored, "\x1b[33;1mHeading\x1b[0m") || !strings.Contains(strings.Join(RenderMarkdown("# Title", 80, false), "\n"), "\x1b[33;1;4mTitle\x1b[0m") || !strings.Contains(colored, "\x1b[1mbold\x1b[0m") || !strings.Contains(colored, "\x1b[3mitalic\x1b[0m") || !strings.Contains(colored, "\x1b[35mcode\x1b[0m") || !strings.Contains(colored, "\x1b[34;4mlink\x1b[0m") {
		t.Fatalf("missing Pi semantic Markdown styling: %q", colored)
	}
	plain := strings.Join(RenderMarkdown("## Heading\n\n**bold** *italic* `code` [link](https://example.test)", 80, true), "\n")
	if strings.Contains(plain, "\x1b[") || !strings.Contains(plain, "link (https://example.test)") {
		t.Fatalf("NO_COLOR or visible URL fallback failed: %q", plain)
	}
}

func TestRenderMarkdownMatchesPiParagraphListSpacing(t *testing.T) {
	joined := strings.Join(RenderMarkdown("Paragraph\n- list item", 80, true), "\n")
	if strings.Contains(joined, "Paragraph\n\n  - list item") {
		t.Fatalf("Pi paragraph/list adjacency gained a blank row: %q", joined)
	}
	separated := strings.Join(RenderMarkdown("Paragraph\n\n- list item", 80, true), "\n")
	if !strings.Contains(separated, "Paragraph\n\n  - list item") {
		t.Fatalf("explicit blank line was lost: %q", separated)
	}
}

func TestRenderMarkdownReadableUnsupportedAndSafe(t *testing.T) {
	input := "<script>not executed</script>\n\n| a | b |\n|---|---|\n| x | y |\n\n![diagram](https://example.test/image)\n\nlink \x1b]52;c;secret\x07"
	got := strings.Join(RenderMarkdown(input, 80, true), "\n")
	for _, want := range []string{"<script>not executed</script>", "| a | b |", "[image: diagram]", "link"} {
		if !strings.Contains(got, want) {
			t.Errorf("Markdown fallback missing %q in %q", want, got)
		}
	}
	if strings.Contains(got, "\x1b") || strings.Contains(got, "\x07") || strings.Contains(got, "https://example.test/image") {
		t.Fatalf("unsupported source was executed or image destination exposed: %q", got)
	}
}

func BenchmarkRenderMarkdown1MiB(b *testing.B) {
	unit := "A paragraph with Unicode 世界, **bold**, and `inline code`. "
	input := strings.Repeat(unit, (1<<20)/len(unit)+1)
	b.SetBytes(int64(len(input)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = RenderMarkdown(input, 80, true)
	}
}

func BenchmarkRenderMarkdownPlain1MiB(b *testing.B) {
	unit := "A plain paragraph with Unicode 世界 and ordinary words. "
	input := strings.Repeat(unit, (1<<20)/len(unit)+1)
	b.SetBytes(int64(len(input)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = RenderMarkdown(input, 80, true)
	}
}

func TestRenderMarkdownWidthAndLongCode(t *testing.T) {
	input := "A long paragraph with **split bold**, *split italic*, `inline code`, and a [visible link](https://example.test) that wraps across several terminal rows.\n\n3. ordered item with a long description\n4. second item\n\n```text\n0123456789abcdefghij\nsecond code line\n```"
	for _, width := range []int{1, 2, 4, 10, 24} {
		rows := RenderMarkdown(input, width, true)
		limit := max(1, width-1)
		for i, row := range rows {
			if CellWidth(row) > limit {
				t.Errorf("terminal width %d row %d exceeds safe width: %d cells: %q", width, i, CellWidth(row), row)
			}
		}
	}
	rows := RenderMarkdown(input, 24, true)
	if !strings.Contains(strings.Join(rows, "\n"), "3.") {
		t.Fatalf("ordered-list start was not preserved: %#v", rows)
	}
}
