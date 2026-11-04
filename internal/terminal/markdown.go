package terminal

import (
	"fmt"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"

	"github.com/rivo/uniseg"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	east "github.com/yuin/goldmark/extension/ast"
	gmtext "github.com/yuin/goldmark/text"

	pitheme "psi/internal/theme"
)

const (
	// MessageIndent is the shared left margin for message prose and tool
	// rows. Pi pads its transcript content, so the one-line tool status
	// stays aligned with the surrounding prose.
	MessageIndent  = "  "
	markdownIndent = MessageIndent
)

type NoticeKind string

const (
	NoticeInfo    NoticeKind = "info"
	NoticeSuccess NoticeKind = "success"
	NoticeWarning NoticeKind = "warning"
	NoticeError   NoticeKind = "error"
)

// markdownParserPool holds parsers with Pi's Markdown dialect: CommonMark plus
// GFM tables, task lists, and bare URL autolinks. Strikethrough stays out of
// the parser because Pi applies a strict "~~" rule that CommonMark parsers
// implement loosely.
var markdownParserPool = sync.Pool{New: func() any {
	return goldmark.New(goldmark.WithExtensions(
		extension.Table,
		extension.TaskList,
		extension.Linkify,
	))
}}

// FormatMessage applies the shared Pi-style message padding and separation.
// It is used for both accepted live submissions and history replay.
func FormatMessage(source string, width int, noColor bool, palette ...*pitheme.Theme) []string {
	return FormatAssistantMessage(source, width, noColor, palette...)
}

func FormatAssistantMessage(source string, width int, noColor bool, palette ...*pitheme.Theme) []string {
	return FormatAssistantMessageWithDisplay(source, width, noColor, DefaultDisplaySettings(), palette...)
}

func FormatAssistantMessageWithDisplay(source string, width int, noColor bool, display DisplaySettings, palette ...*pitheme.Theme) []string {
	if strings.TrimSpace(source) == "" {
		return nil
	}
	rows := RenderMarkdownWithDisplay(source, width, noColor, display, palette...)
	if len(rows) > 0 {
		rows = append(rows, "")
	}
	return rows
}

// FormatUserMessage keeps the request distinguishable in monochrome and uses
// Pi's user surface when the resolved theme defines one.
func FormatUserMessage(source string, width int, noColor bool, palette ...*pitheme.Theme) []string {
	return FormatUserMessageWithDisplay(source, width, noColor, DefaultDisplaySettings(), palette...)
}

func FormatUserMessageWithDisplay(source string, width int, noColor bool, display DisplaySettings, palette ...*pitheme.Theme) []string {
	if strings.TrimSpace(source) == "" {
		return nil
	}
	display = display.normalized()
	indent := display.transcriptIndent()
	// Reserve the marker cells before wrapping, not by clipping an already
	// full first row. Continuations use the same content column.
	contentIndent := indent + "  "
	rows := renderMarkdownRowsWithIndent(source, width, mdStyle{token: TokenUserMessageText}, display, contentIndent)
	for i := range rows {
		if rows[i].Width() == 0 {
			continue
		}
		marker := StyledSpan{Text: indent + "› ", Token: TokenUserMessageText}
		if strings.HasPrefix(rows[i].Plain(), contentIndent) {
			removeMarkdownPrefix(&rows[i], contentIndent)
			rows[i].Spans = append([]StyledSpan{marker}, rows[i].Spans...)
		} else {
			// Fences, quotes, rules, and tiny-width fallbacks need their own
			// intact first row. Never remove bytes that are not indentation.
			rows = append(rows[:i], append([]StyledRow{{Spans: []StyledSpan{marker}}}, rows[i:]...)...)
		}
		break
	}
	for i := range rows {
		rows[i].Surface = TokenUserMessageBg
	}
	var selected *pitheme.Theme
	if len(palette) > 0 {
		selected = palette[0]
	}
	out := make([]string, 0, len(rows)+1)
	for _, row := range rows {
		out = append(out, FormatStyledRow(row, width, noColor, selected))
	}
	out = append(out, "")
	return out
}

func removeMarkdownPrefix(row *StyledRow, prefix string) {
	remaining := len(prefix)
	for i := range row.Spans {
		if remaining == 0 {
			return
		}
		text := row.Spans[i].Text
		if len(text) <= remaining {
			remaining -= len(text)
			row.Spans[i].Text = ""
			continue
		}
		row.Spans[i].Text = text[remaining:]
		remaining = 0
	}
}

// RenderMarkdown applies the retained regular-mode Markdown subset from
// temp/pi-mono/packages/tui/src/components/markdown.ts. It deliberately uses
// the no-highlighter code path and visible link destinations (no OSC 8).
// Unsupported block content remains readable source text.
func RenderMarkdown(source string, width int, noColor bool, palette ...*pitheme.Theme) []string {
	return RenderMarkdownWithDisplay(source, width, noColor, DefaultDisplaySettings(), palette...)
}

func RenderMarkdownWithDisplay(source string, width int, noColor bool, display DisplaySettings, palette ...*pitheme.Theme) []string {
	stream := NewMarkdownStream(width, noColor)
	stream.SetDisplay(display)
	if len(palette) > 0 {
		stream.SetTheme(palette[0])
	}
	stable, _ := stream.Feed(source)
	return append(stable, stream.Finish()...)
}

// FormatNotice renders one Pi-style semantic status notice. User/provider text
// stays untrusted; only the fixed semantic style is serialized as SGR.
func FormatNotice(kind NoticeKind, text string, width int, noColor bool, palette ...*pitheme.Theme) []string {
	if strings.TrimSpace(text) == "" {
		return nil
	}
	prefix, token := "", TokenMuted
	switch kind {
	case NoticeSuccess:
		prefix, token = "Success: ", TokenSuccess
	case NoticeWarning:
		prefix, token = "Warning: ", TokenWarning
	case NoticeError:
		prefix, token = "Error: ", TokenError
	}
	rows := wrapMarkdown([]StyledSpan{{Text: prefix + Sanitize(text), Token: token}}, max(1, width-1),
		[]StyledSpan{{Text: " ", Token: TokenMuted}}, []StyledSpan{{Text: " ", Token: TokenMuted}})
	out := make([]string, 0, len(rows))
	var selected *pitheme.Theme
	if len(palette) > 0 {
		selected = palette[0]
	}
	for _, row := range rows {
		out = append(out, row.SerializeWithTheme(selected, colorDisabled(PresentationSnapshot{NoColor: noColor})))
	}
	return out
}

func renderMarkdownCore(source string, width int, noColor bool, palette ...*pitheme.Theme) []string {
	return renderMarkdownCoreWithDisplay(source, width, noColor, DefaultDisplaySettings(), palette...)
}

func renderMarkdownCoreWithDisplay(source string, width int, noColor bool, display DisplaySettings, palette ...*pitheme.Theme) []string {
	if strings.TrimSpace(source) == "" {
		return nil
	}
	rows := renderMarkdownRowsWithDisplay(source, width, mdStyle{token: TokenText}, display)
	var selected *pitheme.Theme
	if len(palette) > 0 {
		selected = palette[0]
	}
	out := make([]string, 0, len(rows))
	for _, row := range rows {
		out = append(out, row.SerializeWithTheme(selected, colorDisabled(PresentationSnapshot{NoColor: noColor})))
	}
	return out
}

// normalizeMarkdownTabs replaces tabs with three spaces. Pi's Markdown
// component normalizes tabs the same way before it parses the document.
func normalizeMarkdownTabs(source string) string {
	if !strings.ContainsRune(source, '\t') {
		return source
	}
	return strings.ReplaceAll(source, "\t", "   ")
}

func renderMarkdownRows(source string, width int, style mdStyle) []StyledRow {
	return renderMarkdownRowsWithDisplay(source, width, style, DefaultDisplaySettings())
}

func renderMarkdownRowsWithDisplay(source string, width int, style mdStyle, display DisplaySettings) []StyledRow {
	return renderMarkdownRowsWithIndent(source, width, style, display, display.transcriptIndent())
}

func renderMarkdownRowsWithIndent(source string, width int, style mdStyle, display DisplaySettings, indent string) []StyledRow {
	if strings.TrimSpace(source) == "" {
		return nil
	}
	width = max(1, width-1) // keep the final cell clear of terminal autowrap
	input := []byte(markdownNormalizeTableDelimiters(normalizeMarkdownTabs(source)))
	parser := markdownParserPool.Get().(goldmark.Markdown)
	doc := parser.Parser().Parse(gmtext.NewReader(input))
	markdownParserPool.Put(parser)
	display = display.normalized()
	return renderMarkdownBlocks(doc, input, width, style, indent, display.CodeBlockIndent)
}

type mdStyle struct {
	token     ThemeToken
	bold      bool
	italic    bool
	underline bool
}

func (s mdStyle) span(text string) StyledSpan {
	return StyledSpan{Text: text, Token: s.token, Bold: s.bold, Italic: s.italic, Underline: s.underline}
}

func (s mdStyle) withToken(token ThemeToken) mdStyle { s.token = token; return s }

func (s mdStyle) withBold() mdStyle { s.bold = true; return s }

func (s mdStyle) withItalic() mdStyle { s.italic = true; return s }

func (s mdStyle) withUnderline() mdStyle { s.underline = true; return s }

type mdTextRun struct {
	text  []byte
	style StyledSpan
}

type mdTextBuffer struct {
	firstText  []byte
	firstStyle StyledSpan
	more       []mdTextRun
	width      int
}

func mdGraphemeWidth(gr *uniseg.Graphemes, cluster string) int {
	if strings.HasSuffix(cluster, "\u20e3") && len(cluster) > 0 && strings.ContainsRune("#*0123456789", rune(cluster[0])) {
		return 2
	}
	return gr.Width()
}

func sameMarkdownStyle(a, b StyledSpan) bool {
	return a.Token == b.Token && a.Background == b.Background && a.Bold == b.Bold && a.Italic == b.Italic && a.Underline == b.Underline && a.Strikethrough == b.Strikethrough
}

func (b *mdTextBuffer) write(text string, style StyledSpan, width int) {
	style.Text = ""
	if len(b.firstText) == 0 {
		b.firstStyle = style
		b.firstText = append(b.firstText, text...)
	} else if sameMarkdownStyle(b.firstStyle, style) && len(b.more) == 0 {
		b.firstText = append(b.firstText, text...)
	} else if len(b.more) > 0 && sameMarkdownStyle(b.more[len(b.more)-1].style, style) {
		last := &b.more[len(b.more)-1]
		last.text = append(last.text, text...)
	} else {
		b.more = append(b.more, mdTextRun{text: append([]byte(nil), text...), style: style})
	}
	b.width += width
}

func (b *mdTextBuffer) each(fn func([]byte, StyledSpan)) {
	if len(b.firstText) > 0 {
		fn(b.firstText, b.firstStyle)
	}
	for _, run := range b.more {
		fn(run.text, run.style)
	}
}

func (b *mdTextBuffer) empty() bool { return len(b.firstText) == 0 && len(b.more) == 0 }

func (b *mdTextBuffer) reset() {
	b.firstText = b.firstText[:0]
	for i := range b.more {
		b.more[i].text = b.more[i].text[:0]
	}
	b.more = b.more[:0]
	b.firstStyle = StyledSpan{}
	b.width = 0
}

type markdownInlineRenderer struct {
	source []byte
	spans  []StyledSpan
}

func (r *markdownInlineRenderer) add(text string, style mdStyle) {
	if text == "" {
		return
	}
	text = strings.ReplaceAll(text, "\r\n", " ")
	text = strings.ReplaceAll(text, "\r", " ")
	r.spans = append(r.spans, style.span(text))
}

func (r *markdownInlineRenderer) children(node ast.Node, style mdStyle) {
	for child := node.FirstChild(); child != nil; child = child.NextSibling() {
		r.walk(child, style)
	}
}

func (r *markdownInlineRenderer) walk(node ast.Node, style mdStyle) {
	switch x := node.(type) {
	case *ast.Text:
		r.add(markdownUnescape(string(x.Segment.Value(r.source))), style)
		if x.HardLineBreak() {
			r.add("\n", style)
		} else if x.SoftLineBreak() {
			// Pi keeps soft line breaks as line breaks in the terminal.
			r.add("\n", style)
		}
	case *ast.String:
		r.add(markdownUnescape(string(x.Value)), style)
	case *ast.CodeSpan:
		code := inlinePlain(x, r.source)
		code = strings.ReplaceAll(code, "\n", " ")
		if len(code) >= 2 && code[0] == ' ' && code[len(code)-1] == ' ' && strings.TrimSpace(code) != "" {
			code = code[1 : len(code)-1]
		}
		r.add(code, style.withToken(TokenCode))
	case *ast.Emphasis:
		if x.Level >= 2 {
			style = style.withBold()
		} else {
			style = style.withItalic()
		}
		r.children(x, style)
	case *ast.Link:
		r.children(x, style.withToken(TokenLink).withUnderline())
		text := inlinePlain(x, r.source)
		href := SanitizeStatusRow(string(x.Destination))
		comparison := strings.TrimPrefix(href, "mailto:")
		if text != href && text != comparison && href != "" {
			r.add(" ("+href+")", style.withToken(TokenLinkURL))
		}
	case *ast.AutoLink:
		label := string(x.Label(r.source))
		href := string(x.URL(r.source))
		r.add(label, style.withToken(TokenLink).withUnderline())
		comparison := strings.TrimPrefix(href, "mailto:")
		if label != href && label != comparison {
			r.add(" ("+SanitizeStatusRow(href)+")", style.withToken(TokenLinkURL))
		}
	case *ast.Image:
		// Pi renders an image as its alt text. It has no inline image
		// support in the Markdown component.
		alt := strings.TrimSpace(inlinePlain(x, r.source))
		if alt != "" {
			r.add(alt, style)
		}
	case *ast.RawHTML:
		for i := 0; i < x.Segments.Len(); i++ {
			segment := x.Segments.At(i)
			r.add(string(segment.Value(r.source)), style)
		}
	case *east.TaskCheckBox:
		r.add(markdownTaskMarker(x.IsChecked), style)
	default:
		r.children(node, style)
	}
}

func renderInline(node ast.Node, source []byte, style mdStyle) []StyledSpan {
	renderer := markdownInlineRenderer{source: source}
	renderer.walk(node, style)
	return applyMarkdownMarks(mergeStyledSpans(renderer.spans))
}

func renderInlineChildren(node ast.Node, source []byte, style mdStyle) []StyledSpan {
	renderer := markdownInlineRenderer{source: source}
	renderer.children(node, style)
	return applyMarkdownMarks(mergeStyledSpans(renderer.spans))
}

func inlinePlain(node ast.Node, source []byte) string {
	var b strings.Builder
	var walk func(ast.Node)
	walk = func(n ast.Node) {
		switch x := n.(type) {
		case *ast.Text:
			b.Write(x.Segment.Value(source))
			if x.HardLineBreak() || x.SoftLineBreak() {
				b.WriteByte(' ')
			}
		case *ast.String:
			b.Write(x.Value)
		case *ast.CodeSpan:
			for c := x.FirstChild(); c != nil; c = c.NextSibling() {
				if t, ok := c.(*ast.Text); ok {
					b.Write(t.Segment.Value(source))
				}
			}
		case *ast.AutoLink:
			b.Write(x.Label(source))
		case *ast.RawHTML:
			for i := 0; i < x.Segments.Len(); i++ {
				segment := x.Segments.At(i)
				b.Write(segment.Value(source))
			}
		default:
			for c := n.FirstChild(); c != nil; c = c.NextSibling() {
				walk(c)
			}
		}
	}
	walk(node)
	return b.String()
}

func mergeStyledSpans(spans []StyledSpan) []StyledSpan {
	out := make([]StyledSpan, 0, len(spans))
	var text strings.Builder
	var active StyledSpan
	activeSet := false
	flush := func() {
		if text.Len() == 0 {
			return
		}
		active.Text = text.String()
		out = append(out, active)
		text.Reset()
	}
	for _, span := range spans {
		if span.Text == "" {
			continue
		}
		if activeSet && (active.Token != span.Token || active.Bold != span.Bold || active.Italic != span.Italic || active.Underline != span.Underline || active.Strikethrough != span.Strikethrough) {
			flush()
			activeSet = false
		}
		if !activeSet {
			active, activeSet = span, true
		}
		text.WriteString(span.Text)
	}
	flush()
	return out
}

func appendRowSpans(row *StyledRow, spans ...StyledSpan) {
	for _, span := range spans {
		if span.Text != "" {
			row.Spans = append(row.Spans, span)
		}
	}
}

func wrapMarkdown(spans []StyledSpan, width int, firstPrefix, continuationPrefix []StyledSpan) []StyledRow {
	return wrapMarkdownWithOptions(spans, width, firstPrefix, continuationPrefix, false)
}

// wrapMarkdownWithOptions wraps inline spans. keepLeadingSpace keeps the
// leading whitespace of the content, like Pi's raw block rendering.
func wrapMarkdownWithOptions(spans []StyledSpan, width int, firstPrefix, continuationPrefix []StyledSpan, keepLeadingSpace bool) []StyledRow {
	width = max(1, width)
	firstPrefix = ClipStyledRow(StyledRow{Spans: firstPrefix}, width).Spans
	continuationPrefix = ClipStyledRow(StyledRow{Spans: continuationPrefix}, width).Spans
	firstPrefixWidth := (StyledRow{Spans: firstPrefix}).Width()
	if firstPrefixWidth >= width {
		firstPrefix, firstPrefixWidth = nil, 0
	}
	continuationPrefixWidth := (StyledRow{Spans: continuationPrefix}).Width()
	if continuationPrefixWidth >= width {
		continuationPrefix, continuationPrefixWidth = nil, 0
	}

	var row StyledRow
	var rowText strings.Builder
	var rowStyle StyledSpan
	rowStyleSet := false
	rowWidth := 0
	currentPrefixWidth := firstPrefixWidth
	rows := []StyledRow{}
	flushRowText := func() {
		if rowText.Len() == 0 {
			return
		}
		rowStyle.Text = rowText.String()
		row.Spans = append(row.Spans, rowStyle)
		rowText.Reset()
		rowStyleSet = false
	}
	writeRow := func(text string, style StyledSpan, cells int) {
		style.Text = ""
		if rowStyleSet && !sameMarkdownStyle(rowStyle, style) {
			flushRowText()
		}
		if !rowStyleSet {
			rowStyle, rowStyleSet = style, true
		}
		rowText.WriteString(text)
		rowWidth += cells
	}
	writeRowBytes := func(text []byte, style StyledSpan) {
		style.Text = ""
		if rowStyleSet && !sameMarkdownStyle(rowStyle, style) {
			flushRowText()
		}
		if !rowStyleSet {
			rowStyle, rowStyleSet = style, true
		}
		rowText.Write(text)
	}
	for _, prefix := range firstPrefix {
		writeRow(prefix.Text, prefix, CellWidth(prefix.Text))
	}
	flushLine := func(force bool) {
		flushRowText()
		if force || rowWidth > currentPrefixWidth || len(rows) == 0 {
			rows = append(rows, row)
		}
		row = StyledRow{}
		rowText.Reset()
		rowStyleSet = false
		currentPrefixWidth = continuationPrefixWidth
		rowWidth = 0
		for _, prefix := range continuationPrefix {
			writeRow(prefix.Text, prefix, CellWidth(prefix.Text))
		}
	}
	appendBuffer := func(buffer *mdTextBuffer) {
		if buffer.empty() {
			return
		}
		if rowWidth > currentPrefixWidth && rowWidth+buffer.width > width {
			flushLine(false)
		}
		if rowWidth+buffer.width <= width {
			buffer.each(func(text []byte, style StyledSpan) { writeRowBytes(text, style) })
			rowWidth += buffer.width
			return
		}
		buffer.each(func(text []byte, style StyledSpan) {
			gr := uniseg.NewGraphemes(string(text))
			for gr.Next() {
				cluster := gr.Str()
				cells := mdGraphemeWidth(gr, cluster)
				if cells > width {
					cluster, cells = "?", 1
				}
				if rowWidth+cells > width && rowWidth > currentPrefixWidth {
					flushLine(false)
				}
				writeRow(cluster, style, cells)
			}
		})
	}

	var word, spaces mdTextBuffer
	commitWord := func() {
		if word.empty() {
			return
		}
		hasText := rowWidth > currentPrefixWidth
		if hasText && rowWidth+spaces.width+word.width > width {
			flushLine(false)
			spaces.reset()
		} else if !hasText {
			// Discard leading Markdown indentation and trailing continuation spaces.
			if keepLeadingSpace {
				appendBuffer(&spaces)
			}
			spaces.reset()
		} else {
			appendBuffer(&spaces)
			spaces.reset()
		}
		appendBuffer(&word)
		word.reset()
	}
	for _, span := range spans {
		gr := uniseg.NewGraphemes(Sanitize(span.Text))
		style := span
		style.Text = ""
		for gr.Next() {
			cluster := gr.Str()
			if cluster == "\n" {
				commitWord()
				spaces.reset()
				flushLine(true)
				continue
			}
			firstRune, _ := utf8.DecodeRuneInString(cluster)
			if cluster == "\t" || (cluster != "" && unicode.IsSpace(firstRune)) {
				commitWord()
				if cluster == "\t" {
					// A tab renders as one space cell.
					spaces.write(" ", style, 1)
					continue
				}
				spaces.write(cluster, style, mdGraphemeWidth(gr, cluster))
				continue
			}
			word.write(cluster, style, mdGraphemeWidth(gr, cluster))
		}
	}
	commitWord()
	if rowWidth > currentPrefixWidth || len(rows) == 0 {
		flushLine(true)
	}
	return rows
}

func mdIndent(text string, token ...ThemeToken) []StyledSpan {
	if text == "" {
		return nil
	}
	selected := TokenText
	if len(token) > 0 {
		selected = token[0]
	}
	return []StyledSpan{{Text: text, Token: selected}}
}

func renderMarkdownBlocks(parent ast.Node, source []byte, width int, style mdStyle, indent, codeIndent string) []StyledRow {
	var rows []StyledRow
	var previous ast.Node
	for child := parent.FirstChild(); child != nil; child = child.NextSibling() {
		block := renderMarkdownBlock(child, source, width, style, 0, indent, codeIndent)
		if len(block) > 0 {
			if len(rows) > 0 && (child.HasBlankPreviousLines() || markdownAddsSpacingAfter(previous, child)) {
				rows = append(rows, StyledRow{})
			}
			rows = append(rows, block...)
		}
		previous = child
	}
	return rows
}

// markdownPreserveLineIndentation restores the source indentation of a
// paragraph's continuation lines. Pi keeps the raw paragraph text, so an
// indented continuation line stays indented.
func markdownPreserveLineIndentation(node ast.Node, source []byte, spans []StyledSpan) []StyledSpan {
	lines := node.Lines()
	if lines == nil || lines.Len() < 2 {
		return spans
	}
	gaps := make([]string, 0, lines.Len()-1)
	for index := 0; index+1 < lines.Len(); index++ {
		segment := lines.At(index)
		next := lines.At(index + 1)
		if next.Start <= segment.Stop || next.Start > len(source) {
			gaps = append(gaps, "")
			continue
		}
		gap := string(source[segment.Stop:next.Start])
		gap = strings.TrimLeft(gap, "\r\n")
		gaps = append(gaps, markdownStripQuoteMarkers(gap))
	}
	if len(gaps) == 0 {
		return spans
	}
	out := make([]StyledSpan, 0, len(spans))
	gapIndex := 0
	for _, span := range spans {
		if gapIndex >= len(gaps) || !strings.Contains(span.Text, "\n") {
			out = append(out, span)
			continue
		}
		parts := strings.Split(span.Text, "\n")
		var builder strings.Builder
		builder.WriteString(parts[0])
		for index := 1; index < len(parts); index++ {
			builder.WriteString("\n")
			if gapIndex < len(gaps) {
				builder.WriteString(gaps[gapIndex])
				gapIndex++
			}
			builder.WriteString(parts[index])
		}
		restyled := span
		restyled.Text = builder.String()
		out = append(out, restyled)
	}
	return out
}

// markdownStripQuoteMarkers removes the blockquote markers from a source gap.
// The content indentation of the quoted block stays.
func markdownStripQuoteMarkers(gap string) string {
	for {
		trimmed := strings.TrimLeft(gap, " \t")
		if !strings.HasPrefix(trimmed, ">") {
			return gap
		}
		gap = strings.TrimLeft(trimmed[1:], " \t")
	}
}

func markdownAddsSpacingAfter(previous, next ast.Node) bool {
	if previous == nil {
		return false
	}
	switch previous.(type) {
	case *ast.Paragraph, *ast.TextBlock:
		_, nextIsList := next.(*ast.List)
		return !nextIsList
	case *ast.Heading, *ast.FencedCodeBlock, *ast.CodeBlock, *ast.Blockquote, *ast.ThematicBreak:
		return true
	default:
		return false
	}
}

func renderMarkdownBlock(node ast.Node, source []byte, width int, style mdStyle, depth int, indent, codeIndent string) []StyledRow {
	switch x := node.(type) {
	case *ast.Paragraph, *ast.TextBlock:
		spans := renderInlineChildren(x, source, style)
		spans = markdownPreserveLineIndentation(x, source, spans)
		return wrapMarkdownWithOptions(spans, width, mdIndent(indent, style.token), mdIndent(indent, style.token), true)
	case *ast.Heading:
		heading := style.withToken(TokenHeading).withBold()
		if x.Level == 1 {
			heading = heading.withUnderline()
		}
		prefix := ""
		if x.Level >= 3 {
			prefix = strings.Repeat("#", x.Level) + " "
		}
		spans := renderInlineChildren(x, source, heading)
		return wrapMarkdown(spans, width, append(mdIndent(indent, style.token), heading.span(prefix)), mdIndent(indent, style.token))
	case *ast.FencedCodeBlock:
		lang := TruncateToWidth(SanitizeStatusRow(markdownFenceInfo(x, source)), max(0, width-1))
		return renderCodeLines(x, source, width, lang, codeIndent, indent, style.token)
	case *ast.CodeBlock:
		return renderCodeLines(x, source, width, "", codeIndent, indent, style.token)
	case *ast.Blockquote:
		// Pi draws the quote border at the message margin and keeps the quoted
		// content directly behind it.
		prefix := mdIndent(indent, style.token)
		if width >= 3 {
			prefix = append(prefix, StyledSpan{Text: "│ ", Token: TokenQuoteBorder})
		}
		prefix, contentWidth := markdownBlockPrefix(prefix, width)
		quoted := renderMarkdownBlocks(x, source, contentWidth, style.withToken(TokenQuote).withItalic(), "", codeIndent)
		for i := range quoted {
			quoted[i] = prependStyled(prefix, quoted[i])
		}
		return quoted
	case *ast.List:
		return renderMarkdownList(x, source, width, style, depth, indent, codeIndent)
	case *ast.ThematicBreak:
		prefix, contentWidth := markdownBlockPrefix(mdIndent(indent, style.token), width)
		line := strings.Repeat("─", min(contentWidth, 80))
		return []StyledRow{{Spans: append(prefix, StyledSpan{Text: line, Token: TokenHorizontalRule})}}
	case *east.Table:
		return renderMarkdownTable(x, source, width, style, indent)
	case *ast.HTMLBlock:
		return renderRawBlock(x, source, width, style, indent)
	default:
		if node.Type() == ast.TypeBlock {
			return renderRawBlock(node, source, width, style, indent)
		}
		return nil
	}
}

func renderCodeLines(node ast.Node, source []byte, width int, lang, codeIndent, indent string, base ...ThemeToken) []StyledRow {
	indentToken := TokenText
	if len(base) > 0 {
		indentToken = base[0]
	}
	label := "```" + lang
	// Pi keeps the code block at the message margin and indents the code
	// itself by the configured code block indent.
	fencePrefix := mdIndent(indent, indentToken)
	codePrefix := mdIndent(indent+codeIndent, indentToken)
	rows := []StyledRow{markdownFenceRow(label, fencePrefix, width, indentToken)}
	lines := node.Lines()
	lineCount := 0
	for i := 0; lines != nil && i < lines.Len(); i++ {
		lineCount++
		segment := lines.At(i)
		text := string(segment.Value(source))
		text = strings.TrimSuffix(text, "\n")
		text = strings.TrimSuffix(text, "\r")
		spans := []StyledSpan{{Text: Sanitize(text), Token: TokenCodeBlock}}
		rows = append(rows, wrapMarkdown(spans, width, codePrefix, codePrefix)...)
	}
	if lineCount == 0 {
		rows = append(rows, wrapMarkdown(nil, width, codePrefix, codePrefix)...)
	}
	rows = append(rows, markdownFenceRow("```", fencePrefix, width, indentToken))
	return rows
}

// markdownFenceRow renders one code fence line inside the message margin.
func markdownFenceRow(label string, prefix []StyledSpan, width int, token ThemeToken) StyledRow {
	limit := max(0, width)
	clipped := ClipStyledRow(StyledRow{Spans: prefix}, limit)
	spans := clipped.Spans
	spans = append(spans, StyledSpan{Text: TruncateToWidth(label, max(0, limit-clipped.Width())), Token: TokenCodeFence})
	return StyledRow{Spans: spans}
}

func renderRawBlock(node ast.Node, source []byte, width int, style mdStyle, indent string) []StyledRow {
	var raw []byte
	if lines := node.Lines(); lines != nil {
		for i := 0; i < lines.Len(); i++ {
			segment := lines.At(i)
			raw = append(raw, segment.Value(source)...)
		}
	}
	if x, ok := node.(*ast.HTMLBlock); ok && x.HasClosure() {
		raw = append(raw, x.ClosureLine.Value(source)...)
	}
	text := strings.TrimRight(string(raw), "\r\n")
	if text == "" {
		text = inlinePlain(node, source)
	}
	text = strings.TrimSpace(text)
	var rows []StyledRow
	for _, line := range strings.Split(text, "\n") {
		spans := []StyledSpan{style.span(strings.TrimSuffix(line, "\r"))}
		rows = append(rows, wrapMarkdownWithOptions(spans, width, mdIndent(indent, style.token), mdIndent(indent, style.token), true)...)
	}
	return rows
}

func prependStyled(prefix []StyledSpan, row StyledRow) StyledRow {
	out := StyledRow{}
	appendRowSpans(&out, prefix...)
	appendRowSpans(&out, row.Spans...)
	return out
}

// markdownListItemWidth reports the width left for a list item body after the
// marker and the message margin.
func markdownListItemWidth(first, continuation []StyledSpan, width int) int {
	used := (StyledRow{Spans: first}).Width()
	if used == 0 {
		used = (StyledRow{Spans: continuation}).Width()
	}
	return max(1, width-used)
}

func renderMarkdownList(list *ast.List, source []byte, width int, style mdStyle, depth int, indentBase, codeIndent string) []StyledRow {
	var rows []StyledRow
	itemIndex := 0
	for item := list.FirstChild(); item != nil; item = item.NextSibling() {
		listItem, ok := item.(*ast.ListItem)
		if !ok {
			continue
		}
		marker := "- "
		if list.IsOrdered() {
			start := list.Start
			if start < 1 {
				start = 1
			}
			marker = fmt.Sprintf("%d. ", start+itemIndex)
		}
		itemIndex++
		indent := indentBase + strings.Repeat("    ", depth)
		first := markdownClipPrefix(append(mdIndent(indent, style.token), StyledSpan{Text: marker, Token: TokenListBullet}), width)
		continuation := markdownClipPrefix(mdIndent(indent+strings.Repeat(" ", CellWidth(marker)), style.token), width)
		visible := false
		var previous ast.Node
		for child := listItem.FirstChild(); child != nil; child = child.NextSibling() {
			if visible && (child.HasBlankPreviousLines() || markdownAddsSpacingAfter(previous, child)) {
				rows = append(rows, StyledRow{Spans: continuation})
			}
			previous = child
			if _, ok := child.(*ast.Paragraph); ok {
				spans := renderInlineChildren(child, source, style)
				prefix := first
				if visible {
					prefix = continuation
				}
				part := wrapMarkdown(spans, width, prefix, continuation)
				rows = append(rows, part...)
				visible = visible || len(part) > 0
				continue
			}
			if _, ok := child.(*ast.TextBlock); ok {
				spans := renderInlineChildren(child, source, style)
				prefix := first
				if visible {
					prefix = continuation
				}
				part := wrapMarkdown(spans, width, prefix, continuation)
				rows = append(rows, part...)
				visible = visible || len(part) > 0
				continue
			}
			if nested, ok := child.(*ast.List); ok {
				if visible && len(rows) > 0 {
					// Pi increases nested list depth by one four-space level.
				}
				part := renderMarkdownList(nested, source, width, style, depth+1, indentBase, codeIndent)
				rows = append(rows, part...)
				visible = visible || len(part) > 0
				continue
			}
			part := renderMarkdownBlock(child, source, markdownListItemWidth(first, continuation, width), style, depth, "", codeIndent)
			if len(part) > 0 {
				prefix := first
				if visible {
					prefix = continuation
				}
				for i := range part {
					part[i] = prependStyled(prefix, part[i])
					prefix = continuation
				}
				rows = append(rows, part...)
				visible = true
			}
		}
		if !visible {
			rows = append(rows, StyledRow{Spans: first})
		}
		if !list.IsTight && item.NextSibling() != nil {
			rows = append(rows, StyledRow{})
		}
	}
	return rows
}
