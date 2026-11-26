package terminal

import (
	"fmt"
	"strings"
	"unicode/utf8"

	pitheme "github.com/hypernewbie/psi/internal/theme"
)

const (
	// Retained shell output matches the application collector: the latest
	// 50 KiB and 2000 logical lines. Imported entries can exceed these limits.
	shellDisplayBytes = 50 * 1024
	shellDisplayLines = 2000
	// The collapsed shell view starts with the last 20 logical lines and then
	// keeps at most 20 wrapped rows. Expanded output keeps the retained tail.
	shellCollapsedLogicalLines = 20
	shellCollapsedWrappedRows  = 20
)

// ShellDisplay is the display-safe source for an active, settled, or replayed
// shell block. Command and Output are untrusted; formatting sanitizes both.
type ShellDisplay struct {
	Command        string
	Output         string
	State          string
	ExitCode       *int
	Excluded       bool
	Truncated      bool
	FullOutputPath string
	Expanded       bool
}

// FormatShellBlock renders one shell block. Active, settled, and replayed
// views use this formatter so their rows stay consistent.
func FormatShellBlock(display ShellDisplay, width int, noColor bool, palette ...*pitheme.Theme) []string {
	width = max(1, width)
	command := SanitizeStatusRow(display.Command)
	header := "$ " + command
	headerToken := TokenAccent
	if display.Excluded {
		headerToken = TokenDim
		// Color dimming is the ordinary excluded cue. A plain terminal keeps
		// the same distinction as text so the block does not look included.
		if noColor {
			header = "!! " + header
		}
	}
	rows := []string{shellStyledRow(header, headerToken, width, noColor, palette...)}

	output, boundCut := boundedShellOutput(display.Output)
	truncated := display.Truncated || boundCut
	lines := strings.Split(strings.TrimRight(output, "\n"), "\n")
	if strings.TrimSpace(output) == "" {
		lines = nil
	}
	hidden := max(0, len(lines)-shellCollapsedLogicalLines)
	if !display.Expanded {
		lines = lines[hidden:]
	}
	if hidden > 0 {
		notice := "… " + itoa(hidden) + " earlier lines (Ctrl+O to expand)"
		rows = append(rows, shellStyledRow(notice, TokenDim, width, noColor, palette...))
	}
	if len(lines) > 0 {
		body := wrapShellLines(strings.Join(lines, "\n"), width)
		if !display.Expanded && len(body) > shellCollapsedWrappedRows {
			body = body[len(body)-shellCollapsedWrappedRows:]
		}
		for _, line := range body {
			rows = append(rows, shellStyledRow(line, TokenMuted, width, noColor, palette...))
		}
	}

	switch display.State {
	case "running":
		rows = append(rows, shellStyledRow("Running... (Esc to cancel)", TokenAccent, width, noColor, palette...))
	case "cancelled":
		rows = append(rows, shellStyledRow("(cancelled)", TokenMuted, width, noColor, palette...))
	case "interrupted":
		rows = append(rows, shellStyledRow("(interrupted)", TokenWarning, width, noColor, palette...))
	case "failed":
		marker := "(failed)"
		if display.ExitCode != nil {
			marker = fmt.Sprintf("(exit %d)", *display.ExitCode)
		}
		rows = append(rows, shellStyledRow(marker, TokenWarning, width, noColor, palette...))
	default:
		if len(lines) == 0 && !truncated {
			rows = append(rows, shellStyledRow("(no output)", TokenMuted, width, noColor, palette...))
		}
	}
	if truncated {
		notice := "[Output truncated]"
		if path := SanitizeStatusRow(display.FullOutputPath); path != "" {
			notice = "[Output truncated. Full output: " + path + "]"
		}
		rows = append(rows, shellStyledRow(notice, TokenWarning, width, noColor, palette...))
	}
	return rows
}

func shellStyledRow(text string, token ThemeToken, width int, noColor bool, palette ...*pitheme.Theme) string {
	return FormatStyledRow(StyledRow{Spans: []StyledSpan{{Text: text, Token: token}}}, width, noColor, palette...)
}

// boundedShellOutput keeps the newest display-safe bytes and logical lines.
// It reports a cut so the formatter can show a truncation notice even for an
// imported entry that the current collector did not produce.
func boundedShellOutput(output string) (string, bool) {
	text := Sanitize(strings.ReplaceAll(strings.ReplaceAll(output, "\r\n", "\n"), "\r", "\n"))
	cut := false
	if len(text) > shellDisplayBytes {
		start := len(text) - shellDisplayBytes
		for start < len(text) && !utf8.RuneStart(text[start]) {
			start++
		}
		text, cut = text[start:], true
	}
	lines := strings.Split(text, "\n")
	if len(lines) > shellDisplayLines {
		text = strings.Join(lines[len(lines)-shellDisplayLines:], "\n")
		cut = true
	}
	return text, cut
}

// wrapShellLines wraps plain shell output at the current inner width. Tabs
// expand at eight-column stops and wide graphemes are measured by cell width.
func wrapShellLines(text string, width int) []string {
	if text == "" {
		return nil
	}
	return wrapAssistantFragment(text, width)
}
