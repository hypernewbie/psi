package terminal

import (
	"fmt"
	"strings"

	"psi/internal/event"
	pitheme "psi/internal/theme"
)

// FormatToolRow renders the one-line live/replay summary. The result and
// command output are deliberately absent; only a bounded call preview and the
// terminal-measured outcome label are visible.
func FormatToolRow(summary event.ToolSummary, name, state string, width int, noColor bool, palette ...*pitheme.Theme) string {
	return FormatToolRowWithDisplay(summary, name, state, width, noColor, DefaultDisplaySettings(), palette...)
}

func FormatToolRowWithDisplay(summary event.ToolSummary, name, state string, width int, noColor bool, display DisplaySettings, palette ...*pitheme.Theme) string {
	display = display.normalized()
	baseIndent := display.transcriptIndent()
	name = SanitizeStatusRow(name)
	if name == "" {
		name = "tool"
	}
	name = TruncateToWidth(name, 16)

	marker, markerToken, surface, outcome, outcomeToken := "?", TokenWarning, ThemeToken(""), "outcome unknown", TokenWarning
	switch state {
	case "running":
		marker, markerToken, surface, outcome, outcomeToken = "◌", TokenMuted, TokenToolPendingBg, "running", TokenMuted
	case "done":
		marker, markerToken, surface, outcome, outcomeToken = "•", TokenSuccess, TokenToolSuccessBg, "", TokenSuccess
	case "failed":
		marker, markerToken, surface, outcome, outcomeToken = "×", TokenError, TokenToolErrorBg, "failed", TokenError
		if summary.ExitCode != nil {
			outcome += fmt.Sprintf(" (exit %d)", *summary.ExitCode)
		}
	case "cancelled", "canceled":
		marker, markerToken, surface, outcome, outcomeToken = "–", TokenWarning, TokenToolPendingBg, "cancelled", TokenWarning
	case "not started":
		marker, markerToken, surface, outcome, outcomeToken = "○", TokenWarning, TokenToolPendingBg, "not started", TokenWarning
	case "interrupted":
		marker, markerToken, surface, outcome, outcomeToken = "?", TokenWarning, TokenToolPendingBg, "outcome unknown", TokenWarning
	}

	body, bodyToken := "", TokenToolOutput
	if summary.Available {
		switch summary.Kind {
		case "bash":
			body = firstCommandDisplayLine(summary.Command)
		case "read":
			body = summary.Path + summary.LineRange
		case "write", "edit":
			body = summary.Path
		}
		if summary.Truncated && body != "" && !strings.Contains(body, "…") {
			body += "…"
		}
	}
	if body == "" && !summary.Available {
		body = "summary unavailable"
		if summary.Note != "" {
			body = summary.Note
		}
		bodyToken = TokenMuted
	}
	body = SanitizeStatusRow(body)

	fixed := baseIndent + " " + marker + " "
	nameToken := TokenToolTitle
	if summary.Kind == "bash" {
		name, nameToken = "$", TokenToolTitle
	}
	prefix := fixed + name
	separator := ""
	if body != "" {
		separator = "  "
	}
	suffix := ""
	if outcome != "" {
		if body != "" {
			suffix = " · " + outcome
		} else {
			suffix = "  " + outcome
		}
	}
	limit := max(0, width-1)
	fixedWidth := CellWidth(fixed)
	nameBudget := max(0, limit-fixedWidth-CellWidth(suffix))
	if CellWidth(name) > nameBudget {
		name = TruncateToWidth(name, nameBudget)
	}
	prefix = fixed + name
	bodyBudget := max(0, limit-CellWidth(prefix)-CellWidth(separator)-CellWidth(suffix))
	if body != "" && bodyBudget <= 0 {
		separator = ""
		body = ""
		bodyBudget = 0
	}
	if body != "" {
		originalBody := body
		if CellWidth(body) > bodyBudget {
			if summary.Kind == "bash" {
				body = truncatePrefixPreview(body, bodyBudget)
			} else {
				body = truncateSuffixPreview(body, bodyBudget)
			}
		}
		if body == "" {
			separator = ""
		} else if bodyBudget > 0 && CellWidth(originalBody) > bodyBudget && !strings.Contains(body, "…") {
			body = truncatePrefixPreview(body, bodyBudget)
		}
	}

	spans := []StyledSpan{
		{Text: baseIndent + " ", Token: TokenMuted},
		{Text: marker, Token: markerToken, Bold: state == "failed"},
		{Text: " ", Token: TokenMuted},
		{Text: name, Token: nameToken, Bold: true},
	}
	if body != "" {
		spans = append(spans, StyledSpan{Text: separator, Token: TokenMuted}, StyledSpan{Text: body, Token: bodyToken})
	}
	if suffix != "" {
		spans = append(spans, StyledSpan{Text: suffix, Token: outcomeToken})
	}
	return FormatStyledRow(StyledRow{Spans: spans, Surface: surface}, width, noColor, palette...)
}

func truncatePrefixPreview(text string, maxCells int) string {
	if maxCells <= 0 {
		return ""
	}
	if CellWidth(text) <= maxCells {
		return text
	}
	if maxCells == 1 {
		return "…"
	}
	return TruncateToWidth(text, maxCells-1) + "…"
}

func truncateSuffixPreview(text string, maxCells int) string {
	if maxCells <= 0 {
		return ""
	}
	if CellWidth(text) <= maxCells {
		return text
	}
	if maxCells == 1 {
		return "…"
	}
	graphemes := Graphemes(text)
	var suffix []string
	used := 0
	for i := len(graphemes) - 1; i >= 0; i-- {
		width := GraphemeWidth(graphemes[i])
		if used+width > maxCells-1 {
			break
		}
		suffix = append(suffix, graphemes[i])
		used += width
	}
	var b strings.Builder
	b.WriteRune('…')
	for i := len(suffix) - 1; i >= 0; i-- {
		b.WriteString(suffix[i])
	}
	return b.String()
}

func firstCommandDisplayLine(command string) string {
	command = strings.ReplaceAll(strings.ReplaceAll(command, "\r\n", "\n"), "\r", "\n")
	for _, line := range strings.Split(command, "\n") {
		line = strings.TrimSpace(Sanitize(line))
		if line != "" {
			return SanitizeStatusRow(line)
		}
	}
	return ""
}

func toolStateFromEvent(state string) ToolState {
	switch state {
	case "done":
		return ToolDone
	case "failed":
		return ToolFailed
	case "cancelled", "canceled":
		return ToolCancelled
	case "not started":
		return ToolNotStarted
	case "interrupted":
		return ToolInterrupted
	default:
		return ToolRunning
	}
}

func toolOutcomeState(state string) ToolState {
	if state == "running" {
		return ToolRunning
	}
	return toolStateFromEvent(state)
}

// New live and replay tool events use FormatToolRow.
