package terminal

import (
	"strings"

	"github.com/yuin/goldmark/ast"
	east "github.com/yuin/goldmark/extension/ast"
)

// GitHub-flavored Markdown parity with Pi's marked-based renderer.
//
// Pi parses Markdown with marked (CommonMark + GFM) and renders the token
// tree with theme callbacks. psi parses with goldmark and renders the AST with
// semantic theme tokens, so the same source produces the same visible layout
// while the theme stays the single source of color.
//
// Reference: temp/pi-mono/packages/tui/src/components/markdown.ts at the
// pinned Pi revision.

// mdTableCell is one rendered table cell.
type mdTableCell struct {
	spans []StyledSpan
	text  string
	width int
	// minWord is the width of the longest unbreakable word in the cell. Pi
	// keeps a column at least this wide so words do not break mid-word.
	minWord int
}

// markdownTableCells returns the header cells and the body rows of a GFM table.
func markdownTableCells(table *east.Table) ([]*east.TableCell, [][]*east.TableCell) {
	var header []*east.TableCell
	var rows [][]*east.TableCell
	for child := table.FirstChild(); child != nil; child = child.NextSibling() {
		switch node := child.(type) {
		case *east.TableHeader:
			for cell := node.FirstChild(); cell != nil; cell = cell.NextSibling() {
				if tableCell, ok := cell.(*east.TableCell); ok {
					header = append(header, tableCell)
				}
			}
		case *east.TableRow:
			var row []*east.TableCell
			for cell := node.FirstChild(); cell != nil; cell = cell.NextSibling() {
				if tableCell, ok := cell.(*east.TableCell); ok {
					row = append(row, tableCell)
				}
			}
			rows = append(rows, row)
		}
	}
	return header, rows
}

// markdownTableCell renders one cell into styled spans.
func markdownTableCell(cell *east.TableCell, source []byte, style mdStyle) mdTableCell {
	spans := renderInlineChildren(cell, source, style)
	text := ""
	for _, span := range spans {
		text += span.Text
	}
	return mdTableCell{
		spans:   spans,
		text:    text,
		width:   CellWidth(text),
		minWord: markdownLongestWordWidth(text, markdownMaxUnbrokenWordWidth),
	}
}

// markdownMaxUnbrokenWordWidth matches Pi's table column minimum.
const markdownMaxUnbrokenWordWidth = 30

// markdownLongestWordWidth returns the width of the longest whitespace
// separated word, capped at maxWidth.
func markdownLongestWordWidth(text string, maxWidth int) int {
	longest := 0
	for _, word := range strings.Fields(text) {
		if width := CellWidth(word); width > longest {
			longest = width
		}
	}
	if maxWidth > 0 && longest > maxWidth {
		return maxWidth
	}
	return longest
}

// markdownTableColumnWidths mirrors Pi's column width allocation: columns keep
// their natural width when the table fits, otherwise the widest growable
// columns absorb the remaining space.
func markdownTableColumnWidths(natural, minimum []int, availableForCells int) []int {
	numCols := len(natural)
	minWidths := append([]int(nil), minimum...)
	minCellsWidth := 0
	for _, width := range minWidths {
		minCellsWidth += width
	}
	if minCellsWidth > availableForCells {
		minWidths = make([]int, numCols)
		for i := range minWidths {
			minWidths[i] = 1
		}
		remaining := availableForCells - numCols
		if remaining > 0 {
			totalWeight := 0
			for _, width := range minimum {
				if width > 1 {
					totalWeight += width - 1
				}
			}
			growth := make([]int, numCols)
			allocated := 0
			if totalWeight > 0 {
				for i, width := range minimum {
					weight := 0
					if width > 1 {
						weight = width - 1
					}
					growth[i] = weight * remaining / totalWeight
					allocated += growth[i]
				}
			}
			for i := range minWidths {
				minWidths[i] += growth[i]
			}
			for leftover := remaining - allocated; leftover > 0; leftover-- {
				for i := 0; i < numCols && leftover > 0; i++ {
					minWidths[i]++
					leftover--
				}
			}
		}
		minCellsWidth = 0
		for _, width := range minWidths {
			minCellsWidth += width
		}
	}

	totalNatural := 0
	for _, width := range natural {
		totalNatural += width
	}
	totalGrowPotential := 0
	for i, width := range natural {
		if delta := width - minWidths[i]; delta > 0 {
			totalGrowPotential += delta
		}
	}
	widths := make([]int, numCols)
	if totalNatural <= availableForCells {
		for i, width := range natural {
			widths[i] = max(width, minWidths[i])
		}
		return widths
	}
	extraWidth := max(0, availableForCells-minCellsWidth)
	for i, width := range natural {
		delta := max(0, width-minWidths[i])
		grow := 0
		if totalGrowPotential > 0 {
			grow = delta * extraWidth / totalGrowPotential
		}
		widths[i] = minWidths[i] + grow
	}
	allocated := 0
	for _, width := range widths {
		allocated += width
	}
	for remaining := availableForCells - allocated; remaining > 0; remaining-- {
		grew := false
		for i := range widths {
			if remaining <= 0 {
				break
			}
			if widths[i] < natural[i] {
				widths[i]++
				remaining--
				grew = true
			}
		}
		if !grew {
			break
		}
	}
	// Keep the table inside the available width. A column that claims more
	// space than the table may use is reduced, so the table never wraps the
	// terminal line.
	for {
		total := 0
		for _, width := range widths {
			total += width
		}
		if total <= availableForCells {
			break
		}
		widest := -1
		for index, width := range widths {
			if width <= 1 {
				continue
			}
			if widest < 0 || width > widths[widest] {
				widest = index
			}
		}
		if widest < 0 {
			break
		}
		widths[widest]--
	}
	return widths
}

// markdownIsDelimiterRow reports whether the line is a GFM delimiter row.
// Pi's marked table tokenizer accepts the row with or without outer pipes.
func markdownIsDelimiterRow(line string) bool {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" {
		return false
	}
	if strings.HasPrefix(trimmed, "|") {
		trimmed = strings.TrimPrefix(trimmed, "|")
	}
	if strings.HasSuffix(trimmed, "|") {
		trimmed = strings.TrimSuffix(trimmed, "|")
	}
	cells := strings.Split(trimmed, "|")
	for _, cell := range cells {
		cell = strings.TrimSpace(cell)
		if cell == "" {
			return false
		}
		colon := strings.Index(cell, ":")
		if colon >= 0 {
			cell = cell[:colon] + cell[colon+1:]
		}
		if cell == "" || strings.Trim(cell, "-") != "" {
			return false
		}
	}
	return true
}

// markdownSplitTableCells splits a table row into cells. Pi's marked
// splitCells trims every cell and drops the empty cells next to the outer
// pipes.
func markdownSplitTableCells(line string) []string {
	var builder strings.Builder
	runes := []rune(line)
	for index, r := range runes {
		if r != '|' {
			builder.WriteRune(r)
			continue
		}
		escaped := false
		for back := index - 1; back >= 0 && runes[back] == '\\'; back-- {
			escaped = !escaped
		}
		if escaped {
			builder.WriteRune('|')
			continue
		}
		builder.WriteString(" | ")
	}
	parts := strings.Split(builder.String(), " | ")
	if len(parts) > 0 && strings.TrimSpace(parts[0]) == "" {
		parts = parts[1:]
	}
	if len(parts) > 0 && strings.TrimSpace(parts[len(parts)-1]) == "" {
		parts = parts[:len(parts)-1]
	}
	for index := range parts {
		parts[index] = strings.ReplaceAll(strings.TrimSpace(parts[index]), "\\|", "|")
	}
	return parts
}

// markdownDelimiterCellCount counts the cells of a delimiter row. Pi counts
// the pipes of the row without its outer pipes.
func markdownDelimiterCellCount(line string) int {
	trimmed := strings.TrimSpace(line)
	trimmed = strings.TrimPrefix(trimmed, "|")
	trimmed = strings.TrimRight(trimmed, "| ")
	return len(strings.Split(trimmed, "|"))
}

// markdownNormalizeTableDelimiters adds the outer pipes to a GFM delimiter row
// that Pi's marked table tokenizer accepts without them. goldmark requires
// the outer pipes, marked does not, so the delimiter row is rewritten before
// the document is parsed. Fenced code blocks keep their content unchanged.
func markdownNormalizeTableDelimiters(source string) string {
	lines := strings.Split(source, "\n")
	inFence := false
	fenceMarker := byte(0)
	fenceLen := 0
	changed := false
	for index := 0; index < len(lines); index++ {
		line := lines[index]
		marker, count, ok := markdownFenceStart([]byte(line))
		if inFence {
			if ok && marker == fenceMarker && count >= fenceLen && strings.TrimSpace(line) == strings.Repeat(string(marker), count) {
				inFence = false
			}
			continue
		}
		if ok {
			inFence = true
			fenceMarker, fenceLen = marker, count
			continue
		}
		if index+1 >= len(lines) {
			continue
		}
		delimiterIndent, delimiter := markdownSplitIndent(lines[index+1])
		if delimiterIndent > 3 || !markdownIsDelimiterRow(delimiter) || !strings.ContainsAny(delimiter, ":|") {
			continue
		}
		if strings.HasPrefix(strings.TrimSpace(delimiter), "|") {
			continue
		}
		headerIndent, header := markdownSplitIndent(lines[index])
		if headerIndent != delimiterIndent || !strings.Contains(header, "|") {
			continue
		}
		if len(markdownSplitTableCells(header)) != markdownDelimiterCellCount(delimiter) {
			continue
		}
		lines[index+1] = strings.Repeat(" ", delimiterIndent) + "| " + strings.TrimSpace(delimiter) + " |"
		changed = true
	}
	if !changed {
		return source
	}
	return strings.Join(lines, "\n")
}

// markdownSplitIndent splits a line into its leading spaces and its content.
func markdownSplitIndent(line string) (int, string) {
	indent := 0
	for indent < len(line) && line[indent] == ' ' {
		indent++
	}
	return indent, line[indent:]
}

// markdownStripContainerPrefix removes the blockquote markers, the list marker,
// and the container indentation from a source line. Pi's raw table text keeps
// only the table content.
func markdownStripContainerPrefix(line string) string {
	rest := strings.TrimLeft(line, " \t")
	for strings.HasPrefix(rest, ">") {
		rest = strings.TrimLeft(rest[1:], " \t")
	}
	if len(rest) == 0 {
		return rest
	}
	if strings.ContainsRune("-*+", rune(rest[0])) {
		if len(rest) == 1 || rest[1] == ' ' || rest[1] == '\t' {
			rest = strings.TrimLeft(rest[1:], " \t")
		}
		return rest
	}
	digits := 0
	for digits < len(rest) && rest[digits] >= '0' && rest[digits] <= '9' {
		digits++
	}
	if digits > 0 && digits+1 < len(rest) && rest[digits] == '.' && (rest[digits+1] == ' ' || rest[digits+1] == '\t') {
		rest = strings.TrimLeft(rest[digits+1:], " \t")
	}
	return rest
}

// markdownTableSourceRows returns the header row and the delimiter row that
// follows it in the source.
func markdownTableSourceRows(table *east.Table, source []byte) (string, string) {
	headerCells, _ := markdownTableCells(table)
	if len(headerCells) == 0 {
		return "", ""
	}
	lines := headerCells[0].Lines()
	if lines == nil || lines.Len() == 0 {
		return "", ""
	}
	segment := lines.At(0)
	start := segment.Start
	for start > 0 && source[start-1] != '\n' {
		start--
	}
	end := segment.Stop
	for end < len(source) && source[end] != '\n' {
		end++
	}
	header := strings.TrimSpace(string(source[start:end]))
	if end >= len(source) {
		return header, ""
	}
	rest := source[end+1:]
	if index := strings.IndexByte(string(rest), '\n'); index >= 0 {
		rest = rest[:index]
	}
	return header, strings.TrimSpace(string(rest))
}

// markdownTableMatchesPi reports whether the table header and delimiter rows
// have the same number of cells. Pi's marked table tokenizer rejects a table
// with a mismatched row and renders the source as text.
func markdownTableMatchesPi(table *east.Table, source []byte) bool {
	header, delimiter := markdownTableSourceRows(table, source)
	if delimiter == "" || !markdownIsDelimiterRow(delimiter) {
		return true
	}
	return len(markdownSplitTableCells(header)) == markdownDelimiterCellCount(delimiter)
}

// markdownBlockPrefix clips a block prefix to the available width and returns
// the prefix with the remaining content width. A prefix that fills the line is
// dropped, like Pi's narrow-width fallbacks.
func markdownBlockPrefix(prefix []StyledSpan, width int) ([]StyledSpan, int) {
	limit := max(0, width)
	clipped := ClipStyledRow(StyledRow{Spans: prefix}, limit)
	if clipped.Width() >= limit {
		return nil, max(1, limit)
	}
	return clipped.Spans, limit - clipped.Width()
}

// markdownClipPrefix clips a prefix to the available width and drops it when
// it fills the line.
func markdownClipPrefix(prefix []StyledSpan, width int) []StyledSpan {
	limit := max(0, width)
	clipped := ClipStyledRow(StyledRow{Spans: prefix}, limit)
	if clipped.Width() >= limit {
		return nil
	}
	return clipped.Spans
}

// renderMarkdownTable renders a GFM table the way Pi does: box drawing
// borders, a bold header, wrapped cells, and a raw Markdown fallback when the
// terminal is too narrow for a stable table.
func renderMarkdownTable(table *east.Table, source []byte, width int, style mdStyle, indent string) []StyledRow {
	headerCells, bodyRows := markdownTableCells(table)
	numCols := len(headerCells)
	if numCols == 0 {
		return nil
	}
	prefix, contentWidth := markdownBlockPrefix(mdIndent(indent, style.token), width)
	// Border overhead: "│ " + (n-1) * " │ " + " │".
	borderOverhead := 3*numCols + 1
	availableForCells := contentWidth - borderOverhead
	if availableForCells < numCols {
		return renderMarkdownTableFallback(table, source, width, style, indent)
	}
	if !markdownTableMatchesPi(table, source) {
		// Pi keeps a table whose header and delimiter rows disagree as text.
		return renderMarkdownTableFallback(table, source, width, style, indent)
	}

	header := make([]mdTableCell, 0, numCols)
	for _, cell := range headerCells {
		header = append(header, markdownTableCell(cell, source, style))
	}
	body := make([][]mdTableCell, 0, len(bodyRows))
	for _, row := range bodyRows {
		cells := make([]mdTableCell, 0, numCols)
		for i := 0; i < numCols; i++ {
			if i < len(row) {
				cells = append(cells, markdownTableCell(row[i], source, style))
				continue
			}
			cells = append(cells, mdTableCell{})
		}
		body = append(body, cells)
	}

	natural := make([]int, numCols)
	minimum := make([]int, numCols)
	for i, cell := range header {
		natural[i] = cell.width
		minimum[i] = max(1, cell.minWord)
	}
	for _, row := range body {
		for i, cell := range row {
			if cell.width > natural[i] {
				natural[i] = cell.width
			}
			minimum[i] = max(minimum[i], max(1, cell.minWord))
		}
	}
	widths := markdownTableColumnWidths(natural, minimum, availableForCells)

	rows := []StyledRow{}
	rows = append(rows, markdownTableBorderRow("┌", "┬", "┐", widths, style, prefix))
	rows = append(rows, markdownTableContentRows(header, widths, style, prefix, true)...)
	separator := markdownTableBorderRow("├", "┼", "┤", widths, style, prefix)
	rows = append(rows, separator)
	for index, row := range body {
		if index > 0 {
			rows = append(rows, separator)
		}
		rows = append(rows, markdownTableContentRows(row, widths, style, prefix, false)...)
	}
	rows = append(rows, markdownTableBorderRow("└", "┴", "┘", widths, style, prefix))
	return rows
}

// markdownTableBorderRow renders one horizontal table border. Pi draws two
// border cells around every column, so the border matches the content rows.
func markdownTableBorderRow(left, middle, right string, widths []int, style mdStyle, prefix []StyledSpan) StyledRow {
	cells := make([]string, 0, len(widths))
	for _, width := range widths {
		cells = append(cells, strings.Repeat("─", width+2))
	}
	text := left + strings.Join(cells, middle) + right
	spans := append([]StyledSpan(nil), prefix...)
	spans = append(spans, StyledSpan{Text: text, Token: style.token})
	return StyledRow{Spans: spans}
}

// markdownTableContentRows renders one table row, wrapping every cell to its
// column width.
func markdownTableContentRows(cells []mdTableCell, widths []int, style mdStyle, prefix []StyledSpan, header bool) []StyledRow {
	wrapped := make([][]StyledRow, len(cells))
	height := 1
	for i, cell := range cells {
		columnWidth := widths[i]
		if columnWidth < 1 {
			columnWidth = 1
		}
		wrapped[i] = wrapMarkdown(cell.spans, columnWidth, nil, nil)
		if len(wrapped[i]) > height {
			height = len(wrapped[i])
		}
	}
	rows := make([]StyledRow, 0, height)
	for line := 0; line < height; line++ {
		spans := append([]StyledSpan(nil), prefix...)
		spans = append(spans, StyledSpan{Text: "│ ", Token: style.token})
		for i := range cells {
			var cellSpans []StyledSpan
			if line < len(wrapped[i]) {
				cellSpans = wrapped[i][line].Spans
			}
			cellWidth := 0
			for _, span := range cellSpans {
				cellWidth += CellWidth(span.Text)
			}
			for _, span := range cellSpans {
				if header {
					span.Bold = true
				}
				spans = append(spans, span)
			}
			if pad := widths[i] - cellWidth; pad > 0 {
				spans = append(spans, StyledSpan{Text: strings.Repeat(" ", pad), Token: style.token, Bold: header})
			}
			if i < len(cells)-1 {
				spans = append(spans, StyledSpan{Text: " │ ", Token: style.token})
			}
		}
		spans = append(spans, StyledSpan{Text: " │", Token: style.token})
		rows = append(rows, StyledRow{Spans: spans})
	}
	return rows
}

// renderMarkdownTableFallback shows the raw Markdown source when the table
// cannot be drawn at the available width.
func renderMarkdownTableFallback(table *east.Table, source []byte, width int, style mdStyle, indent string) []StyledRow {
	raw := markdownTableRawText(table, source)
	if raw == "" {
		return nil
	}
	spans := []StyledSpan{{Text: raw, Token: style.token}}
	return wrapMarkdown(spans, width, mdIndent(indent, style.token), mdIndent(indent, style.token))
}

// markdownTableRawText recovers the source lines of a table.
func markdownTableRawText(table *east.Table, source []byte) string {
	start, end := -1, -1
	collect := func(cell *east.TableCell) {
		lines := cell.Lines()
		for i := 0; i < lines.Len(); i++ {
			segment := lines.At(i)
			if start < 0 || segment.Start < start {
				start = segment.Start
			}
			if segment.Stop > end {
				end = segment.Stop
			}
		}
	}
	headerCells, bodyRows := markdownTableCells(table)
	for _, cell := range headerCells {
		collect(cell)
	}
	for _, row := range bodyRows {
		for _, cell := range row {
			collect(cell)
		}
	}
	if start < 0 || end <= start || end > len(source) {
		return ""
	}
	for start > 0 && source[start-1] != '\n' {
		start--
	}
	for end < len(source) && source[end] != '\n' {
		end++
	}
	lines := strings.Split(string(source[start:end]), "\n")
	for index, line := range lines {
		lines[index] = markdownStripContainerPrefix(line)
	}
	return strings.Join(lines, "\n")
}

// mdMark is one edit applied to rendered inline spans.
type mdMark struct {
	start  int
	end    int
	remove bool
}

// markdownStrictStrikeMarks finds Pi's strict strikethrough spans: "~~" must be
// followed by a non-space character, the closing "~~" must not be preceded by
// a tilde, and the content may not end with a lone backslash.
func markdownStrictStrikeMarks(text string) []mdMark {
	var marks []mdMark
	for i := 0; i+1 < len(text); {
		index := strings.Index(text[i:], "~~")
		if index < 0 {
			break
		}
		open := i + index
		if open+2 >= len(text) {
			break
		}
		next := text[open+2]
		if next == '~' || next == ' ' || next == '\t' || next == '\n' || next == '\r' {
			i = open + 1
			continue
		}
		closer := markdownStrikeCloser(text, open+2)
		if closer < 0 {
			i = open + 1
			continue
		}
		marks = append(marks,
			mdMark{start: open, end: open + 2, remove: true},
			mdMark{start: open + 2, end: closer},
			mdMark{start: closer, end: closer + 2, remove: true},
		)
		i = closer + 2
	}
	return marks
}

// markdownStrikeCloser finds the closing "~~" of a strict strikethrough run.
func markdownStrikeCloser(text string, from int) int {
	for j := from; j+1 < len(text); j++ {
		if text[j] != '~' || text[j+1] != '~' {
			continue
		}
		if j > 0 && text[j-1] == '~' {
			continue
		}
		if j+2 < len(text) && text[j+2] == '~' {
			continue
		}
		content := text[from:j]
		if markdownEndsWithEscape(content) {
			continue
		}
		return j
	}
	return -1
}

// markdownEndsWithEscape reports whether text ends with an odd number of
// backslashes, which cannot end a strikethrough run.
func markdownEndsWithEscape(text string) bool {
	backslashes := 0
	for i := len(text) - 1; i >= 0 && text[i] == '\\'; i-- {
		backslashes++
	}
	return backslashes%2 == 1
}

// applyMarkdownMarks applies strikethrough edits to rendered spans. Code spans
// and visible link destinations keep their literal text.
func applyMarkdownMarks(spans []StyledSpan) []StyledSpan {
	if len(spans) == 0 {
		return spans
	}
	text := ""
	offsets := make([][2]int, 0, len(spans))
	for _, span := range spans {
		if span.Token == TokenCode || span.Token == TokenLinkURL {
			offsets = append(offsets, [2]int{len(text), len(text)})
			continue
		}
		start := len(text)
		text += span.Text
		offsets = append(offsets, [2]int{start, len(text)})
	}
	marks := markdownStrictStrikeMarks(text)
	if len(marks) == 0 {
		return spans
	}
	out := make([]StyledSpan, 0, len(spans)+len(marks))
	for index, span := range spans {
		start, end := offsets[index][0], offsets[index][1]
		if start == end {
			out = append(out, span)
			continue
		}
		pieces := markdownSplitSpan(span, text, start, end, marks)
		out = append(out, pieces...)
	}
	return mergeStyledSpans(out)
}

// markdownSplitSpan splits one span at the mark boundaries that touch it.
func markdownSplitSpan(span StyledSpan, text string, start, end int, marks []mdMark) []StyledSpan {
	type boundary struct {
		offset int
		remove bool
	}
	var boundaries []boundary
	for _, mark := range marks {
		if mark.end <= start || mark.start >= end {
			continue
		}
		boundaries = append(boundaries, boundary{offset: mark.start, remove: mark.remove})
		boundaries = append(boundaries, boundary{offset: mark.end, remove: mark.remove})
	}
	if len(boundaries) == 0 {
		return []StyledSpan{span}
	}
	// Keep the boundaries ordered and deduplicated.
	for i := 1; i < len(boundaries); i++ {
		for j := i; j > 0 && boundaries[j].offset < boundaries[j-1].offset; j-- {
			boundaries[j], boundaries[j-1] = boundaries[j-1], boundaries[j]
		}
	}
	offsets := []int{start}
	for _, b := range boundaries {
		if b.offset > start && b.offset < end {
			offsets = append(offsets, b.offset)
		}
	}
	offsets = append(offsets, end)
	pieces := []StyledSpan{}
	for i := 0; i+1 < len(offsets); i++ {
		from, to := offsets[i], offsets[i+1]
		if from >= to {
			continue
		}
		removed := false
		struck := false
		for _, mark := range marks {
			if mark.start <= from && to <= mark.end {
				if mark.remove {
					removed = true
				} else {
					struck = true
				}
			}
		}
		if removed {
			continue
		}
		piece := span
		piece.Text = text[from:to]
		piece.Strikethrough = span.Strikethrough || struck
		pieces = append(pieces, piece)
	}
	return pieces
}

// markdownTaskMarker renders a task list checkbox like Pi does.
func markdownTaskMarker(checked bool) string {
	if checked {
		return "[x] "
	}
	return "[ ] "
}

// markdownEscapablePunctuation lists the characters CommonMark allows to be
// escaped with a backslash.
const markdownEscapablePunctuation = "!\"#$%&'()*+,-./:;<=>?@[\\]^_`{|}~"

// markdownUnescape removes backslash escapes from Markdown text. Pi's
// tokenizer drops the backslash and keeps the escaped character.
func markdownUnescape(text string) string {
	if !strings.ContainsRune(text, '\\') {
		return text
	}
	var out strings.Builder
	out.Grow(len(text))
	for i := 0; i < len(text); i++ {
		if text[i] == '\\' && i+1 < len(text) && strings.IndexByte(markdownEscapablePunctuation, text[i+1]) >= 0 {
			i++
			out.WriteByte(text[i])
			continue
		}
		out.WriteByte(text[i])
	}
	return out.String()
}

// markdownFenceInfo returns the full info string of a fenced code block. Pi
// shows the whole info string after the fence marker.
func markdownFenceInfo(block *ast.FencedCodeBlock, source []byte) string {
	if block.Info == nil {
		return ""
	}
	return strings.TrimSpace(string(block.Info.Segment.Value(source)))
}
