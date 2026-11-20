package terminal

import (
	"fmt"
	"math"
	"strings"

	"github.com/hypernewbie/psi/internal/event"
	pitheme "github.com/hypernewbie/psi/internal/theme"
)

// FormatToolRowsWithDisplay renders one tool block at the current width. Web
// tools own a multi-row card; every other tool keeps the existing one-row
// layout unchanged.
func FormatToolRowsWithDisplay(summary event.ToolSummary, name, state string, width int, noColor bool, display DisplaySettings, palette ...*pitheme.Theme) []string {
	kind := summary.Kind
	if kind == "" {
		kind = name
	}
	if event.IsWebTool(kind) || event.IsWebTool(name) {
		call, result := event.WebCall{}, (*event.WebResult)(nil)
		if summary.Web != nil {
			call, result = summary.Web.Call, summary.Web.Result
		}
		// The call title needs a tool identity even when a caller supplies
		// only the kind, as a restored summary may.
		titleName := name
		if titleName == "" {
			titleName = kind
		}
		return formatWebCardRows(call, result, titleName, state, nil, width, noColor, display, palette...)
	}
	return []string{FormatToolRowWithDisplay(summary, name, state, width, noColor, display, palette...)}
}

// FormatWebCardRows renders a live web card from structured data. The caller
// keeps the source data, so a resize reflows the card instead of a string that
// was truncated at an older width. progress is transient and never persisted.
func FormatWebCardRows(name, state string, web event.WebDisplay, progress *event.WebProgress, width int, noColor bool, display DisplaySettings, palette ...*pitheme.Theme) []string {
	return formatWebCardRows(web.Call, web.Result, name, state, progress, width, noColor, display, palette...)
}

// formatWebCardRows lays out Pi's call-above-result card. The card owns its
// leading spacer and one-cell box padding; the shared gutter stays outside the
// surface like the one-row tool layout. Every field is folded to a single row
// before styling, so untrusted data cannot add a row or a control sequence.
func formatWebCardRows(call event.WebCall, result *event.WebResult, name, state string, progress *event.WebProgress, width int, noColor bool, display DisplaySettings, palette ...*pitheme.Theme) []string {
	display = display.normalized()
	width = max(1, width)
	gutter := TruncateToWidth(display.transcriptIndent(), max(0, width-1))
	boxWidth := width - 1 - CellWidth(gutter)
	if boxWidth < 4 {
		// No usable column remains: hide the card rather than print a broken
		// box. The settled block stays in the transcript document.
		return nil
	}
	surface := TokenToolPendingBg
	switch state {
	case "done":
		surface = TokenToolSuccessBg
	case "failed":
		surface = TokenToolErrorBg
	}
	rows := []string{""}
	// Pi reduces the box padding when the available width is tight; below a
	// few usable columns the card is hidden instead of printing a broken box.
	boxPad := " "
	if boxWidth < 10 {
		boxPad = ""
	}
	render := func(text string, token ThemeToken, bold, keepPreviewLayout bool) []string {
		text = SanitizeStatusRow(text)
		if text == "" && !keepPreviewLayout {
			return nil
		}
		var prefix []StyledSpan
		if boxPad != "" {
			prefix = []StyledSpan{{Text: boxPad, Token: token}}
		}
		wrapped := wrapMarkdownWithOptions([]StyledSpan{{Text: text, Token: token, Bold: bold}}, boxWidth, prefix, prefix, keepPreviewLayout)
		out := make([]string, 0, len(wrapped))
		for _, row := range wrapped {
			row.Surface = surface
			out = append(out, gutter+FormatStyledRow(row, width-CellWidth(gutter), noColor, palette...))
		}
		return out
	}
	add := func(text string, token ThemeToken, bold bool) {
		rows = append(rows, render(text, token, bold, false)...)
	}
	// addPreview clips lines to a UTF-16 unit budget and returns the number of
	// terminal rows the lines occupy at this width.
	addPreview := func(lines []string, budget int) int {
		before := len(rows)
		remaining := budget
		for _, line := range lines {
			if remaining <= 0 {
				break
			}
			line = utf16PrefixUnits(line, remaining)
			if line == "" {
				break
			}
			add(line, TokenToolOutput, false)
			remaining -= utf16UnitsIn(line)
		}
		return len(rows) - before
	}
	countPreview := func(lines []string, budget int) int {
		count, remaining := 0, budget
		for _, line := range lines {
			if remaining <= 0 {
				break
			}
			line = utf16PrefixUnits(line, remaining)
			if line == "" {
				break
			}
			count += len(render(line, TokenToolOutput, false, false))
			remaining -= utf16UnitsIn(line)
		}
		return count
	}

	add(webCallTitle(call, name), TokenToolTitle, true)
	for _, entry := range call.Entries {
		add("  "+entry, TokenToolTitle, false)
	}
	if call.Overflow > 0 {
		add(fmt.Sprintf("  ... and %d more", call.Overflow), TokenMuted, false)
	}
	if call.Mode != "" {
		add("  mode: "+call.Mode, TokenMuted, false)
	}
	if state == "running" && progress != nil {
		add(webProgressLine(*progress), TokenMuted, false)
	}
	switch state {
	case "failed":
		// A failed request never shows success data. The diagnostic stays a
		// fixed label; producers may extend it with safe structured facts.
		add("Error: request failed", TokenError, false)
	case "cancelled", "canceled":
		add("Cancelled", TokenWarning, false)
	case "not started":
		add("Not started", TokenWarning, false)
	case "interrupted":
		add("Outcome unknown", TokenWarning, false)
	}
	if result != nil && state == "done" {
		title := webResultTitle(*result)
		if result.HasDuration {
			title += " · " + webDuration(result.DurationMs) + " total"
		}
		add(title, TokenToolTitle, false)
		if result.PagePreview != "" {
			budget := webCollapsedPreviewUnits
			if display.ExpandWeb {
				budget = webExpandedPreviewUnits
			}
			preview := utf16PrefixUnits(result.PagePreview, budget)
			if int64(utf16UnitsIn(preview)) < result.Chars {
				preview += "..."
			}
			// Pi's Text expands each tab to three spaces after clipping.
			for _, line := range strings.Split(strings.ReplaceAll(preview, "\t", "   "), "\n") {
				rows = append(rows, render(line, TokenToolOutput, false, true)...)
			}
		} else if display.ExpandWeb {
			addPreview(webExpandedPreview(*result), webExpandedPreviewUnits)
		} else if result.Kind == event.WebResultPage || result.Kind == event.WebResultSearch || result.Kind == "" {
			collapsedUnits := webCollapsedPreviewUnits
			if result.Kind == event.WebResultSearch {
				// Pi's search preview is shorter than the page preview.
				collapsedUnits = webSearchCollapsedPreviewUnits
			}
			addPreview(result.Preview, collapsedUnits)
			if result.Kind == event.WebResultSearch {
				// The hint counts rows the expanded view would add at the
				// current width; it is omitted when expansion adds nothing.
				full := webExpandedPreview(*result)
				total := countPreview(full, webExpandedPreviewUnits)
				shown := countPreview(result.Preview, collapsedUnits)
				if total > shown {
					add(fmt.Sprintf("... (%d more lines, %d total, ctrl+o to expand)", total-shown, total), TokenMuted, false)
				}
			}
		}
	}
	return rows
}

// webExpandedPreview returns the stored rows the expanded view renders.
func webExpandedPreview(result event.WebResult) []string {
	if len(result.Expanded) > 0 {
		return result.Expanded
	}
	return result.Preview
}

// webProgressLine renders Pi's ten-cell bar from completed attempts, never
// from elapsed time or byte estimates. The ratio is clamped, and a zero total
// leaves the bar empty.
func webProgressLine(progress event.WebProgress) string {
	filled := 0
	if progress.Total > 0 {
		ratio := float64(progress.Completed) / float64(progress.Total)
		ratio = math.Max(0, math.Min(1, ratio))
		filled = int(math.Round(ratio * 10))
	}
	filled = max(0, min(10, filled))
	parts := []string{"[" + strings.Repeat("█", filled) + strings.Repeat("░", 10-filled) + "]"}
	if progress.Phase != "" {
		parts = append(parts, progress.Phase)
	}
	if progress.Label != "" {
		parts = append(parts, progress.Label)
	}
	if progress.Total > 0 {
		parts = append(parts, fmt.Sprintf("%d/%d", progress.Completed, progress.Total))
	}
	return strings.Join(parts, " ")
}

// webDuration formats a measured fetch duration like Pi's compact suffix.
func webDuration(ms int64) string {
	ms = max(0, ms)
	if ms < 1000 {
		return fmt.Sprintf("%dms", ms)
	}
	return fmt.Sprintf("%.1fs", float64(ms)/1000)
}

const (
	// webCollapsedPreviewUnits is Pi's collapsed page preview budget.
	webCollapsedPreviewUnits = 200
	// webSearchCollapsedPreviewUnits is Pi's shorter collapsed search budget.
	webSearchCollapsedPreviewUnits = 120
	// webExpandedPreviewUnits is the shared preview bound retained on disk.
	webExpandedPreviewUnits = event.WebMaxPreviewUnits
)

// utf16PrefixUnits returns the prefix that fits in max UTF-16 units without
// splitting a scalar value.
func utf16PrefixUnits(s string, max int) string {
	units := 0
	for i, r := range s {
		width := 1
		if r > 0xFFFF {
			width = 2
		}
		if units+width > max {
			return s[:i]
		}
		units += width
	}
	return s
}

func utf16UnitsIn(s string) int {
	units := 0
	for _, r := range s {
		units++
		if r > 0xFFFF {
			units++
		}
	}
	return units
}

func webCallTitle(call event.WebCall, name string) string {
	title := event.WebTitle(name)
	if title == "" {
		title = "tool"
	}
	switch {
	case call.Invalid:
		return title + " (invalid parameters)"
	case call.NoURL:
		return title + " (no URL)"
	case call.NoQuery:
		return title + " (no query)"
	case call.Title != "":
		return call.Title
	}
	return title
}

// webResultTitle composes the collapsed status row for each supported result
// shape. Every value is a settled producer fact; the renderer never derives
// counts or ranges from result prose.
func webResultTitle(result event.WebResult) string {
	switch result.Kind {
	case event.WebResultBatch:
		label := fmt.Sprintf("%d/%d URLs", result.OK, result.Total)
		if result.Stored {
			return label + " (content stored)"
		}
		return label + " (content fetched)"
	case event.WebResultSearch:
		var parts []string
		if result.Total > 1 {
			parts = append(parts, fmt.Sprintf("%d/%d queries", result.OK, result.Total))
		}
		if result.HasSources {
			parts = append(parts, fmt.Sprintf("%d sources", result.Sources))
		}
		line := strings.Join(parts, ", ")
		if line == "" {
			line = "search"
		}
		if result.Stored {
			line += " (content ready)"
		}
		return line
	case event.WebResultContent:
		title := strings.TrimSpace(result.Title)
		if title == "" {
			title = "Content"
		}
		if result.HasChars {
			title = fmt.Sprintf("%s (%d chars", title, result.Chars)
			if result.HasRange {
				title += fmt.Sprintf(", showing %d-%d", result.RangeStart, result.RangeEnd)
			}
			return title + ")"
		}
		if result.HasRange {
			return fmt.Sprintf("%s (showing %d-%d)", title, result.RangeStart, result.RangeEnd)
		}
		return title
	}
	title := strings.TrimSpace(result.Title)
	if title == "" {
		title = "Untitled"
	}
	if result.HasChars {
		title = fmt.Sprintf("%s (%d chars)", title, result.Chars)
	}
	if result.Truncated {
		title += " [truncated]"
	}
	return title
}

// clipWebCardRows keeps a live card inside the fragment budget. Leading and
// trailing blank rows go first, then continuation rows from the end, so the
// call title survives while any row fits.
func clipWebCardRows(rows []string, budget int) []string {
	if budget <= 0 || len(rows) == 0 {
		return nil
	}
	for len(rows) > budget && strings.TrimSpace(rows[0]) == "" {
		rows = rows[1:]
	}
	for len(rows) > budget && strings.TrimSpace(rows[len(rows)-1]) == "" {
		rows = rows[:len(rows)-1]
	}
	if len(rows) > budget {
		rows = rows[:budget]
	}
	return rows
}
