package terminal

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// FuzzRenderMarkdown checks that the Markdown renderer never panics and never
// emits a row wider than the requested width.
func FuzzRenderMarkdown(f *testing.F) {
	seeds := []string{
		"| A | B |\n| --- | --- |\n| 1 | 2 |",
		"| A |\n| --- |\n| a \\| b |",
		"~~struck~~ and ~single~ and unmatched ~~ here",
		"- [ ] task\n- [x] done",
		"Visit https://example.com/docs now",
		"> | A | B |\n> | --- | --- |\n> | 1 | 2 |",
		"# Heading\n\n```go\nfmt.Println(1)\n```",
		"a \\*escaped\\* text",
		"line one\nline two",
		"|",
		"|||\n|||\n|||",
		"~~",
		"~~~",
		"\x1b[31mred\x1b[0m | a | b |",
	}
	for _, seed := range seeds {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, source string) {
		if len(source) > 8192 || !utf8.ValidString(source) {
			return
		}
		for _, width := range []int{1, 2, 5, 24, 80} {
			rows := RenderMarkdown(source, width, true)
			limit := max(1, width-1)
			for i, row := range rows {
				if CellWidth(row) > limit && !markdownRowOverflowsByCluster(row, limit) {
					t.Fatalf("width %d row %d exceeds the safe width: %d cells: %q", width, i, CellWidth(row), row)
				}
				if strings.ContainsRune(row, '\x1b') {
					t.Fatalf("NO_COLOR output contains SGR: %q", row)
				}
			}
		}
	})
}

// markdownRowOverflowsByCluster reports whether a row exceeds the limit only
// because its last grapheme cluster does not fit. psi keeps a cluster intact
// instead of breaking it like Pi breaks long tokens.
func markdownRowOverflowsByCluster(row string, limit int) bool {
	clusters := Graphemes(row)
	if len(clusters) == 0 {
		return false
	}
	last := clusters[len(clusters)-1]
	rest := strings.Join(clusters[:len(clusters)-1], "")
	return CellWidth(rest) <= limit && CellWidth(rest)+CellWidth(last) > limit
}
