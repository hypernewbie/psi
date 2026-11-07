package terminal

import (
	"strings"

	pitheme "psi/internal/theme"
)

// formatStatusMessage renders Pi's showStatus output: one blank spacer row
// and one dim status line. Consecutive statuses replace, so one line remains.
func formatStatusMessage(text string, width int, noColor bool, palette ...*pitheme.Theme) []string {
	return formatColoredMessage(text, TokenMuted, width, noColor, palette...)
}

// formatColoredMessage renders one blank spacer row and one colored line, like
// Pi's plain ThemedText chat additions.
func formatColoredMessage(text string, token ThemeToken, width int, noColor bool, palette ...*pitheme.Theme) []string {
	if strings.TrimSpace(text) == "" {
		return nil
	}
	text = SanitizeStatusRow(text)
	if text == "" {
		return nil
	}
	var selected *pitheme.Theme
	if len(palette) > 0 {
		selected = palette[0]
	}
	row := StyledRow{Spans: []StyledSpan{{Text: TruncateToWidth(text, max(1, width-1)), Token: token}}}
	return []string{"", FormatStyledRow(row, width, noColor, selected)}
}

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
