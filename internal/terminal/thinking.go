package terminal

import pitheme "psi/internal/theme"

// FormatThinkingRow is a quiet activity label, not provider reasoning content.
func FormatThinkingRow(width int, noColor bool, display DisplaySettings, palette *pitheme.Theme) string {
	text := TruncateToWidth(display.transcriptIndent()+"Thinking…", max(0, width-1))
	if text == "" || colorDisabled(PresentationSnapshot{NoColor: noColor}) {
		return text
	}
	if palette != nil {
		if styled, err := palette.Style(text, "dim", "", pitheme.TextStyle{Dim: true}); err == nil {
			return styled
		}
	}
	return (StyledRow{Spans: []StyledSpan{{Text: text, Token: TokenDim}}}).Serialize(false)
}
