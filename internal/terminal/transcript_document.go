package terminal

import (
	"strings"

	"psi/internal/event"
	pitheme "psi/internal/theme"
)

// DocumentKind identifies display-safe source retained for resize relayout.
type DocumentKind string

const (
	DocumentRaw       DocumentKind = "raw"
	DocumentUser      DocumentKind = "user"
	DocumentAssistant DocumentKind = "assistant"
	DocumentThinking  DocumentKind = "thinking"
	DocumentTool      DocumentKind = "tool"
	DocumentMetadata  DocumentKind = "metadata"
	DocumentBoundary  DocumentKind = "boundary"
	DocumentNotice    DocumentKind = "notice"
	DocumentGap       DocumentKind = "gap"
)

// DocumentBlock stores display source, not provider context. Each value is one
// displayed occurrence, so callers can intentionally show the same source twice.
type DocumentBlock struct {
	Kind       DocumentKind
	Text       string
	Label      string
	ToolName   string
	ToolState  string
	CallID     string
	Summary    event.ToolSummary
	Historical bool
	NoticeKind NoticeKind
	Streaming  bool
}

// FormatDocumentBlock lays out one retained block at the current terminal width.
func FormatDocumentBlock(block DocumentBlock, width int, presentation PresentationSnapshot) []string {
	width = max(1, width)
	noColor, display, theme := presentation.NoColor, presentation.Display, presentation.Theme
	switch block.Kind {
	case DocumentUser:
		return FormatUserMessageWithDisplay(block.Text, width, noColor, display, theme)
	case DocumentAssistant:
		if block.Streaming {
			return RenderMarkdownWithDisplay(block.Text, width, noColor, display, theme)
		}
		return FormatAssistantMessageWithDisplay(block.Text, width, noColor, display, theme)
	case DocumentThinking:
		if block.Streaming {
			// A live run exists only after non-whitespace reasoning arrived.
			// Hidden mode has no fragment tail, so the ledger owns the label.
			if display.HideThinking {
				return FormatThinkingLabel(width, noColor, display, theme)
			}
			return RenderMarkdownWithDisplay(block.Text, width, noColor, display, theme)
		}
		return FormatThinkingBlock(block.Text, width, noColor, display, theme)
	case DocumentTool:
		name, state := block.ToolName, block.ToolState
		if name == "" {
			name, state, _ = strings.Cut(block.Text, ": ")
		}
		summary := block.Summary
		if summary.Kind == "" {
			summary.Kind = name
		}
		return []string{FormatToolRowWithDisplay(summary, name, state, width, noColor, display, theme)}
	case DocumentMetadata:
		return FormatMetadataMessage(block.Label, block.Text, width, noColor, display, theme)
	case DocumentBoundary:
		return []string{"--- " + Sanitize(block.Text) + " ---", ""}
	case DocumentNotice:
		return FormatNotice(block.NoticeKind, block.Text, width, noColor, theme)
	case DocumentGap:
		return []string{""}
	case DocumentRaw:
		return formatRawDocument(block.Text, width)
	default:
		return nil
	}
}

// FormatThinkingLabel renders Pi's collapsed reasoning label.
func FormatThinkingLabel(width int, noColor bool, display DisplaySettings, palette ...*pitheme.Theme) []string {
	row := StyledRow{Spans: []StyledSpan{{Text: display.transcriptIndent() + "Thinking...", Token: TokenThinkingText, Italic: true}}}
	var selected *pitheme.Theme
	if len(palette) > 0 {
		selected = palette[0]
	}
	return []string{row.SerializeWithTheme(selected, colorDisabled(PresentationSnapshot{NoColor: noColor}))}
}

// FormatThinkingBlock renders one reasoning run. An empty run renders
// nothing, like Pi. A hidden run collapses to the label.
func FormatThinkingBlock(source string, width int, noColor bool, display DisplaySettings, palette ...*pitheme.Theme) []string {
	if strings.TrimSpace(source) == "" {
		return nil
	}
	if display.HideThinking {
		return FormatThinkingLabel(width, noColor, display, palette...)
	}
	stream := NewMarkdownStream(max(1, width), noColor)
	stream.SetDisplay(display)
	stream.SetStyle(mdStyle{token: TokenThinkingText, italic: true})
	if len(palette) > 0 {
		stream.SetTheme(palette[0])
	}
	stable, _ := stream.Feed(source)
	rows := append(stable, stream.Finish()...)
	if len(rows) > 0 {
		rows = append(rows, "")
	}
	return rows
}

// FormatDocument lays out the complete displayed ledger. Tool rows stay grouped
// until a non-tool block or an explicit gap ends the group.
func FormatDocument(blocks []DocumentBlock, width int, presentation PresentationSnapshot) []string {
	var rows []string
	toolPending := false
	for _, block := range blocks {
		if block.Kind != DocumentTool && block.Kind != DocumentGap && toolPending {
			rows = append(rows, "")
			toolPending = false
		}
		rows = append(rows, FormatDocumentBlock(block, width, presentation)...)
		if block.Kind == DocumentTool {
			toolPending = true
		}
		if block.Kind == DocumentGap {
			toolPending = false
		}
	}
	return rows
}

func formatRawDocument(text string, width int) []string {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	if text == "" {
		return nil
	}
	text = strings.TrimSuffix(text, "\n")
	lines := strings.Split(text, "\n")
	rows := make([]string, 0, len(lines))
	for _, line := range lines {
		plain := Sanitize(line)
		if plain == "" {
			rows = append(rows, "")
			continue
		}
		wrapped := wrapMarkdown([]StyledSpan{{Text: plain, Token: TokenText}}, max(1, width-1), nil, nil)
		for _, row := range wrapped {
			rows = append(rows, Sanitize(row.Plain()))
		}
	}
	return rows
}
