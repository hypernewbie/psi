package terminal

import (
	"strings"

	"psi/internal/skills"
	pitheme "psi/internal/theme"
)

// formatSkillInvocation renders Pi's skill block as its custom message group,
// followed by the argument tail on the ordinary user surface. The collapsed
// group never parses the hidden Markdown body.
func formatSkillInvocation(block skills.Block, width int, noColor bool, display DisplaySettings, palette ...*pitheme.Theme) []string {
	display = display.normalized()
	indent := display.transcriptIndent()
	label := StyledSpan{Text: "[skill]", Token: TokenCustomMessageLabel, Bold: true}
	var rows []StyledRow
	if display.ExpandSkills {
		first := StyledRow{Spans: []StyledSpan{{Text: indent, Token: TokenDim}, label}}
		first.Surface = TokenCustomMessageBg
		rows = append(rows, first)
		body := "**" + block.Name + "**"
		if strings.TrimSpace(block.Body) != "" {
			body += "\n\n" + block.Body
		}
		for _, row := range renderMarkdownRowsWithIndent(body, width, mdStyle{token: TokenCustomMessageText}, display, indent) {
			row.Surface = TokenCustomMessageBg
			rows = append(rows, row)
		}
	} else {
		spans := []StyledSpan{
			label,
			{Text: " " + block.Name, Token: TokenCustomMessageText},
			{Text: " (ctrl+o to expand)", Token: TokenDim},
		}
		for _, row := range wrapMarkdown(spans, max(1, width-1), mdIndent(indent, TokenCustomMessageText), mdIndent(indent, TokenCustomMessageText)) {
			row.Surface = TokenCustomMessageBg
			rows = append(rows, row)
		}
	}
	out := make([]string, 0, len(rows)+2)
	for _, row := range rows {
		out = append(out, FormatStyledRow(row, width, noColor, palette...))
	}
	out = append(out, "")
	if block.Args != "" {
		out = append(out, formatPlainUserMessage(block.Args, width, noColor, display, palette...)...)
	}
	return out
}
