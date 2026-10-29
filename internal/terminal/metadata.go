package terminal

import (
	"strings"

	pitheme "psi/internal/theme"
)

// FormatMetadataMessage gives session and compaction markers Pi's subordinate
// custom-message roles without treating them as user or assistant prose.
func FormatMetadataMessage(label, text string, width int, noColor bool, display DisplaySettings, palette ...*pitheme.Theme) []string {
	display = display.normalized()
	label = SanitizeStatusRow(label)
	text = Sanitize(text)
	indent := display.transcriptIndent()
	var out []string
	lines := strings.Split(text, "\n")
	for i, line := range lines {
		prefix := mdIndent(indent, TokenCustomMessageText)
		if i == 0 {
			prefix = []StyledSpan{{Text: indent, Token: TokenDim}, {Text: label, Token: TokenCustomMessageLabel}}
		}
		spans := []StyledSpan{{Text: line, Token: TokenCustomMessageText}}
		rows := wrapMarkdown(spans, max(1, width-1), prefix, mdIndent(indent, TokenCustomMessageText))
		for _, row := range rows {
			row.Surface = TokenCustomMessageBg
			out = append(out, FormatStyledRow(row, width, noColor, palette...))
		}
	}
	if len(out) > 0 {
		out = append(out, "")
	}
	return out
}
