package terminal

import (
	"bytes"
	"strings"
	"unicode/utf8"
)

const maxMarkdownActiveSource = 64 * 1024

// MarkdownStream commits complete, stable blocks and keeps only the unresolved
// block in its active tail. Oversized blocks switch to sanitized source rows
// until a safe block boundary. RenderMarkdown uses this same path for replay.
type MarkdownStream struct {
	width   int
	noColor bool
	filter  Sanitizer

	pending          []byte
	activePreviewLen int
	blockGeneration  uint64
	tailGeneration   uint64
	lineStart        int
	fenceByte        byte
	fenceLen         int
	fenceLanguage    string
	fenceHasLine     bool
	listBase         int
	inQuote          bool
	indentedCode     bool
	candidateStart   int
	candidateEnd     int

	fallback          bool
	fallbackLine      []byte
	fallbackLineStart []byte
	fallbackCandidate bool
	fallbackNoResume  bool

	pendingCR      bool
	needsSeparator bool
	seenOutput     bool
	tailRows       []string
	tailCells      int
}

func NewMarkdownStream(width int, noColor bool) *MarkdownStream {
	return &MarkdownStream{width: max(1, width), noColor: noColor, listBase: -1, candidateStart: -1}
}

// Feed returns stable rows and a borrowed active-tail slice. The tail remains
// valid only until the next call that changes this stream.
func (m *MarkdownStream) Feed(chunk string) (stable, tail []string) {
	generation := m.blockGeneration
	text := m.normalizeNewlines(m.filter.Feed(chunk))
	m.consume([]byte(text), &stable)
	m.updateTail(generation)
	return stable, m.tailRows
}

// Finish commits the remaining active block and resets the stream for the next
// assistant message or tool-separated response segment.
func (m *MarkdownStream) Finish() []string {
	var rows []string
	if m.pendingCR {
		m.pendingCR = false
		m.consume([]byte{'\n'}, &rows)
	}
	m.filter.pendingUTF8 = ""
	m.filter.state = 0
	if m.fallback {
		if len(m.fallbackLine) > 0 {
			m.appendRawFallbackLine(string(m.fallbackLine), &rows)
		}
	} else if m.fenceByte != 0 {
		if len(m.pending) > 0 && !isMarkdownFenceClose(m.pending, m.fenceByte, m.fenceLen) {
			m.commitCodeLine(m.pending, &rows)
		}
		m.commitFenceEnd(&rows)
	} else if len(m.pending) > 0 {
		if m.needsSeparator {
			rows = append(rows, "")
			m.needsSeparator = false
		}
		rows = append(rows, renderMarkdownCore(string(m.pending), m.width, m.noColor)...)
		m.seenOutput = m.seenOutput || len(rows) > 0
	}
	if m.fallback && m.fenceByte != 0 {
		m.commitFenceEnd(&rows)
	}
	m.reset()
	return rows
}

// SetWidth re-renders only the bounded active source region. Stable scrollback
// rows are left to the terminal's native resize/reflow behavior.
func (m *MarkdownStream) SetWidth(width int) []string {
	m.width = max(1, width)
	m.renderTail()
	return append([]string(nil), m.tailRows...)
}

func (m *MarkdownStream) BufferedBytes() int {
	return len(m.pending) + len(m.fallbackLine) + len(m.fallbackLineStart)
}

func (m *MarkdownStream) HasOutput() bool { return m.seenOutput }

func (m *MarkdownStream) Tail() []string { return append([]string(nil), m.tailRows...) }

// DisplayTail formats the current unresolved block once per scheduled draw.
// Feed itself only appends a plain active preview, so tiny provider deltas do
// not repeatedly parse the growing block.
func (m *MarkdownStream) DisplayTail() []string {
	source := m.previewSource()
	if len(source) == 0 {
		return nil
	}
	var rows []string
	switch {
	case m.fallback:
		rows = wrapAssistantFragment(Sanitize(string(source)), m.width)
	case m.fenceByte != 0:
		rows = renderCodeRows(source, m.width, m.noColor)
	default:
		rows = renderMarkdownCore(string(source), m.width, m.noColor)
	}
	if m.needsSeparator && len(rows) > 0 {
		rows = append([]string{""}, rows...)
	}
	return rows
}

func (m *MarkdownStream) normalizeNewlines(text string) string {
	if text == "" && !m.pendingCR {
		return ""
	}
	var out strings.Builder
	out.Grow(len(text) + 1)
	if m.pendingCR {
		out.WriteByte('\n')
		m.pendingCR = false
		if strings.HasPrefix(text, "\n") {
			text = text[1:]
		}
	}
	for i := 0; i < len(text); i++ {
		switch text[i] {
		case '\r':
			if i+1 == len(text) {
				m.pendingCR = true
				continue
			}
			out.WriteByte('\n')
			if text[i+1] == '\n' {
				i++
			}
		case '\n':
			out.WriteByte('\n')
		default:
			out.WriteByte(text[i])
		}
	}
	return out.String()
}

func (m *MarkdownStream) consume(text []byte, stable *[]string) {
	for len(text) > 0 {
		if m.fallback {
			text = m.consumeFallback(text, stable)
			continue
		}
		newline := bytes.IndexByte(text, '\n')
		if newline < 0 {
			room := maxMarkdownActiveSource - len(m.pending)
			if len(text) <= room {
				m.pending = append(m.pending, text...)
				m.commitPartialBoundary(stable)
				return
			}
			if room > 0 {
				m.pending = append(m.pending, text[:room]...)
				text = text[room:]
			}
			m.enterFallback(stable)
			continue
		}
		lineBytes := text[:newline+1]
		room := maxMarkdownActiveSource - len(m.pending)
		if len(lineBytes) > room {
			lineTail := lineBytes[max(0, room):]
			rest := text[newline+1:]
			if room > 0 {
				m.pending = append(m.pending, lineBytes[:room]...)
			}
			m.enterFallback(stable)
			text = make([]byte, 0, len(lineTail)+len(rest))
			text = append(text, lineTail...)
			text = append(text, rest...)
			continue
		}
		m.pending = append(m.pending, lineBytes...)
		text = text[newline+1:]
		m.processNormalLine(stable)
	}
}

func (m *MarkdownStream) processNormalLine(stable *[]string) {
	for {
		if m.lineStart >= len(m.pending) || m.pending[len(m.pending)-1] != '\n' {
			return
		}
		start, end := m.lineStart, len(m.pending)
		lineBytes := m.pending[start : end-1]
		line := string(lineBytes)
		m.lineStart = end

		if m.fenceByte != 0 {
			if isMarkdownFenceClose(lineBytes, m.fenceByte, m.fenceLen) {
				m.commitFenceEnd(stable)
				m.clearPending()
				m.resetBlockState()
				m.needsSeparator = true
				return
			}
			m.commitCodeLine(lineBytes, stable)
			m.clearPending()
			return
		}
		if marker, count, ok := markdownFenceStart(lineBytes); ok {
			if m.candidateStart >= 0 && m.safeNormalBoundary(m.pending, m.candidateEnd, lineBytes) {
				block := m.pending[:m.candidateStart]
				rest := append([]byte(nil), m.pending[m.candidateEnd:]...)
				m.commitBlock(block, stable)
				m.clearPending()
				m.pending = append(m.pending, rest...)
				m.resetBlockState()
				m.processNormalLine(stable)
				return
			}
			m.fenceByte, m.fenceLen = marker, count
			m.fenceHasLine = false
			info := strings.Fields(string(lineBytes[count:]))
			if len(info) > 0 {
				m.fenceLanguage = info[0]
			}
			m.commitFenceStart(stable)
			m.clearPending()
			return
		}
		if markdownBlank(lineBytes) {
			if m.candidateStart < 0 {
				m.candidateStart = start
			}
			m.candidateEnd = end
			return
		}
		if m.candidateStart >= 0 {
			if m.safeNormalBoundary(m.pending, m.candidateEnd, lineBytes) {
				block := m.pending[:m.candidateStart]
				rest := append([]byte(nil), m.pending[m.candidateEnd:]...)
				m.commitBlock(block, stable)
				m.clearPending()
				m.pending = append(m.pending, rest...)
				m.resetBlockState()
				m.processNormalLine(stable)
				return
			}
			m.candidateStart, m.candidateEnd = -1, -1
		}
		m.noteBlockShape(line)
		return
	}
}

func (m *MarkdownStream) commitPartialBoundary(stable *[]string) {
	if m.candidateStart < 0 || m.lineStart >= len(m.pending) {
		return
	}
	nextLine := m.pending[m.lineStart:]
	if markdownBlank(nextLine) || !m.safeNormalBoundary(m.pending, m.candidateEnd, nextLine) {
		return
	}
	block := m.pending[:m.candidateStart]
	rest := append([]byte(nil), m.pending[m.candidateEnd:]...)
	m.commitBlock(block, stable)
	m.clearPending()
	m.pending = append(m.pending, rest...)
	m.resetBlockState()
}

func (m *MarkdownStream) safeNormalBoundary(_ []byte, _ int, nextLine []byte) bool {
	if m.fenceByte != 0 || m.fallbackNoResume {
		return false
	}
	if m.indentedCode && markdownContentIndent(nextLine) >= 4 {
		return false
	}
	if m.listBase >= 0 {
		indent, marker := markdownListIndent(nextLine)
		if marker && indent >= m.listBase {
			return false
		}
		if markdownLineIndent(nextLine) > m.listBase {
			return false
		}
	}
	if m.inQuote && markdownQuoteLine(nextLine) {
		return false
	}
	// Ambiguous containers are kept active above. A blank line is therefore a
	// safe boundary for the remaining CommonMark block types.
	return true
}

func (m *MarkdownStream) commitFenceStart(stable *[]string) {
	if m.needsSeparator {
		*stable = append(*stable, "")
		m.needsSeparator = false
	}
	label := "```" + SanitizeStatusRow(m.fenceLanguage)
	row := StyledRow{Spans: []StyledSpan{{Text: TruncateToWidth(label, max(0, m.width-1)), Token: TokenMuted}}}
	*stable = append(*stable, row.Serialize(colorDisabled(PresentationSnapshot{NoColor: m.noColor})))
	m.seenOutput = true
}

func (m *MarkdownStream) commitCodeLine(line []byte, stable *[]string) {
	m.fenceHasLine = true
	*stable = append(*stable, renderCodeRows(line, m.width, m.noColor)...)
	m.seenOutput = true
}

func renderCodeRows(line []byte, width int, noColor bool) []string {
	text := strings.TrimSuffix(string(line), "\r")
	spans := []StyledSpan{{Text: Sanitize(text), Token: TokenCodeBlock}}
	rows := wrapMarkdown(spans, max(1, width-1), mdIndent(markdownIndent), mdIndent(markdownIndent))
	out := make([]string, 0, len(rows))
	for _, row := range rows {
		out = append(out, row.Serialize(colorDisabled(PresentationSnapshot{NoColor: noColor})))
	}
	return out
}

func (m *MarkdownStream) commitFenceEnd(stable *[]string) {
	if !m.fenceHasLine {
		m.commitCodeLine(nil, stable)
	}
	row := StyledRow{Spans: []StyledSpan{{Text: TruncateToWidth("```", max(0, m.width-1)), Token: TokenMuted}}}
	*stable = append(*stable, row.Serialize(colorDisabled(PresentationSnapshot{NoColor: m.noColor})))
	m.seenOutput = true
}

func (m *MarkdownStream) commitBlock(source []byte, stable *[]string) {
	if strings.TrimSpace(string(source)) == "" {
		return
	}
	rows := renderMarkdownCore(string(source), m.width, m.noColor)
	if len(rows) == 0 {
		return
	}
	if m.needsSeparator {
		*stable = append(*stable, "")
	}
	*stable = append(*stable, rows...)
	m.needsSeparator = true
	m.seenOutput = true
}

func (m *MarkdownStream) enterFallback(stable *[]string) {
	m.fallback = true
	if m.needsSeparator && len(m.pending) > 0 {
		*stable = append(*stable, "")
		m.needsSeparator = false
	}
	if m.lineStart > 0 {
		complete := m.pending[:m.lineStart]
		for len(complete) > 0 {
			n := bytes.IndexByte(complete, '\n')
			if n < 0 {
				break
			}
			m.appendRawFallbackLine(string(complete[:n]), stable)
			complete = complete[n+1:]
		}
	}
	m.fallbackCandidate = m.candidateStart >= 0
	m.fallbackNoResume = m.fallbackNoResume || markdownHasReferenceCandidate(m.pending)
	m.clearFallbackLine()
	m.fallbackLine = append(m.fallbackLine, m.pending[m.lineStart:]...)
	m.fallbackLineStart = append(m.fallbackLineStart, m.fallbackLine[:min(len(m.fallbackLine), 256)]...)
	m.clearPending()
	m.flushOversizedFallbackLine(stable)
}

func (m *MarkdownStream) consumeFallback(text []byte, stable *[]string) []byte {
	for len(text) > 0 {
		newline := bytes.IndexByte(text, '\n')
		if newline < 0 {
			m.appendFallbackBytes(text, stable)
			return nil
		}
		m.appendFallbackBytes(text[:newline], stable)
		line := string(m.fallbackLine)
		classLine := line
		if len(m.fallbackLineStart) > 0 {
			classLine = string(m.fallbackLineStart)
		}
		text = text[newline+1:]
		if m.fallbackCandidate && m.fallbackSafeBoundary(classLine) {
			m.fallback = false
			m.fallbackCandidate = false
			m.fallbackNoResume = false
			m.needsSeparator = false // the fallback already emitted the source blank line
			m.clearFallbackLine()
			m.resetBlockState()
			m.consume([]byte(line+"\n"), stable)
			if len(text) > 0 {
				m.consume(text, stable)
			}
			return nil
		}
		m.appendRawFallbackLine(line, stable)
		m.clearFallbackLine()
		if m.fenceByte != 0 {
			if isMarkdownFenceClose([]byte(classLine), m.fenceByte, m.fenceLen) {
				m.fallback = false
				m.needsSeparator = true
				m.fallbackCandidate = false
				m.fallbackNoResume = false
				m.resetBlockState()
				if len(text) > 0 {
					m.consume(text, stable)
				}
				return nil
			}
			continue
		}
		if markdownBlank([]byte(classLine)) {
			m.fallbackCandidate = true
			continue
		}
		m.fallbackCandidate = false
		m.noteBlockShape(classLine)
		if marker, count, ok := markdownFenceStart([]byte(classLine)); ok {
			m.fenceByte, m.fenceLen = marker, count
		}
		if markdownHasReferenceCandidate([]byte(classLine)) || markdownRawHTMLStart([]byte(classLine)) {
			m.fallbackNoResume = true
		}
	}
	return nil
}

func (m *MarkdownStream) fallbackSafeBoundary(nextLine string) bool {
	if m.fallbackNoResume || m.fenceByte != 0 {
		return false
	}
	if m.indentedCode && markdownContentIndent([]byte(nextLine)) >= 4 {
		return false
	}
	if m.listBase >= 0 {
		indent, marker := markdownListIndent([]byte(nextLine))
		if marker && indent >= m.listBase {
			return false
		}
		if markdownLineIndent([]byte(nextLine)) > m.listBase {
			return false
		}
	}
	return !(m.inQuote && markdownQuoteLine([]byte(nextLine)))
}

func (m *MarkdownStream) appendFallbackBytes(text []byte, stable *[]string) {
	for len(text) > 0 {
		if len(m.fallbackLineStart) < 256 {
			prefix := min(len(text), 256-len(m.fallbackLineStart))
			m.fallbackLineStart = append(m.fallbackLineStart, text[:prefix]...)
		}
		room := maxMarkdownActiveSource - len(m.fallbackLine) - len(m.fallbackLineStart)
		if room <= 0 {
			m.flushOversizedFallbackLine(stable)
			room = maxMarkdownActiveSource - len(m.fallbackLine) - len(m.fallbackLineStart)
		}
		n := min(len(text), max(1, room))
		m.fallbackLine = append(m.fallbackLine, text[:n]...)
		text = text[n:]
		m.flushOversizedFallbackLine(stable)
	}
}

func (m *MarkdownStream) flushOversizedFallbackLine(stable *[]string) {
	for len(m.fallbackLine)+len(m.fallbackLineStart) > maxMarkdownActiveSource {
		end := streamPrefixLen(m.fallbackLine, m.width)
		if end <= 0 || end > len(m.fallbackLine) {
			_, size := utf8.DecodeRune(m.fallbackLine)
			if size <= 0 {
				size = 1
			}
			end = size
		}
		m.appendRawFallbackFragment(string(m.fallbackLine[:end]), stable)
		clear(m.fallbackLine[:end])
		m.fallbackLine = m.fallbackLine[end:]
	}
}

func (m *MarkdownStream) appendRawFallbackLine(line string, stable *[]string) {
	if m.needsSeparator {
		*stable = append(*stable, "")
		m.needsSeparator = false
	}
	if line == "" {
		*stable = append(*stable, "")
	} else {
		*stable = append(*stable, wrapAssistantFragment(Sanitize(line), m.width)...)
	}
	m.seenOutput = true
}

func (m *MarkdownStream) appendRawFallbackFragment(fragment string, stable *[]string) {
	if m.needsSeparator {
		*stable = append(*stable, "")
		m.needsSeparator = false
	}
	*stable = append(*stable, wrapAssistantFragment(Sanitize(fragment), m.width)...)
	m.seenOutput = true
}

func (m *MarkdownStream) previewSource() []byte {
	if m.fallback {
		return m.fallbackLine
	}
	return m.pending
}

func (m *MarkdownStream) updateTail(previousGeneration uint64) {
	source := m.previewSource()
	incremental := previousGeneration == m.blockGeneration && m.tailGeneration == m.blockGeneration && len(source) >= m.activePreviewLen
	if incremental {
		suffix := source[m.activePreviewLen:]
		if previewASCII(suffix) {
			m.appendPreviewASCII(suffix)
		} else if len(suffix) > 0 {
			incremental = false
		}
	}
	if !incremental {
		m.renderTail()
	}
	m.activePreviewLen = len(source)
	m.tailGeneration = m.blockGeneration
}

func previewASCII(text []byte) bool {
	for _, b := range text {
		if b != '\n' && (b < 0x20 || b > 0x7e) {
			return false
		}
	}
	return true
}

func (m *MarkdownStream) appendPreviewASCII(text []byte) {
	if len(text) == 0 {
		return
	}
	if len(m.tailRows) == 0 {
		if m.needsSeparator {
			m.tailRows = append(m.tailRows, "")
		}
		m.tailRows = append(m.tailRows, "  ")
		m.tailCells = 2
	}
	limit := max(1, m.width-1)
	for _, b := range text {
		if b == '\n' {
			m.tailRows = append(m.tailRows, "  ")
			m.tailCells = 2
			continue
		}
		last := len(m.tailRows) - 1
		row := m.tailRows[last]
		if m.tailCells > 2 && m.tailCells+1 > limit {
			m.tailRows = append(m.tailRows, "  ")
			last++
			row = m.tailRows[last]
			m.tailCells = 2
		}
		m.tailRows[last] = row + string(b)
		m.tailCells++
	}
}

func (m *MarkdownStream) renderTail() {
	clear(m.tailRows)
	m.tailRows = m.tailRows[:0]
	m.tailCells = 0
	source := m.previewSource()
	if len(source) == 0 {
		return
	}
	rows := wrapAssistantFragment(Sanitize(string(source)), m.width)
	if m.needsSeparator && len(rows) > 0 {
		m.tailRows = append(m.tailRows, "")
	}
	m.tailRows = append(m.tailRows, rows...)
	m.tailCells = CellWidth(m.tailRows[len(m.tailRows)-1])
}

func (m *MarkdownStream) reset() {
	m.clearPending()
	m.clearFallbackLine()
	clear(m.tailRows)
	m.tailRows = m.tailRows[:0]
	m.lineStart = 0
	m.fallback = false
	m.fallbackCandidate = false
	m.fallbackNoResume = false
	m.pendingCR = false
	m.needsSeparator = false
	m.seenOutput = false
	m.activePreviewLen = 0
	m.blockGeneration++
	m.tailGeneration = m.blockGeneration
	m.resetBlockState()
}

func (m *MarkdownStream) clearPending() {
	if len(m.pending) > 0 {
		m.blockGeneration++
	}
	clear(m.pending)
	m.pending = m.pending[:0]
	m.lineStart = 0
}

func (m *MarkdownStream) clearFallbackLine() {
	if len(m.fallbackLine) > 0 || len(m.fallbackLineStart) > 0 {
		m.blockGeneration++
	}
	clear(m.fallbackLine)
	clear(m.fallbackLineStart)
	m.fallbackLine = m.fallbackLine[:0]
	m.fallbackLineStart = m.fallbackLineStart[:0]
}

func (m *MarkdownStream) resetBlockState() {
	m.fenceByte, m.fenceLen, m.fenceLanguage = 0, 0, ""
	m.fenceHasLine = false
	m.listBase, m.inQuote, m.indentedCode = -1, false, false
	m.candidateStart, m.candidateEnd = -1, -1
	m.fallbackNoResume = false
}

func (m *MarkdownStream) noteBlockShape(line string) {
	if indent, ok := markdownListIndent([]byte(line)); ok && m.listBase < 0 {
		m.listBase = indent
	}
	if markdownQuoteLine([]byte(line)) {
		m.inQuote = true
	}
	if m.listBase < 0 && markdownContentIndent([]byte(line)) >= 4 {
		m.indentedCode = true
	}
	if markdownHasReferenceCandidate([]byte(line)) || markdownRawHTMLStart([]byte(line)) {
		m.fallbackNoResume = true
	}
}

func markdownBlank(line []byte) bool {
	for _, b := range line {
		if b != ' ' && b != '\t' {
			return false
		}
	}
	return true
}

func markdownLineIndent(line []byte) int {
	indent := 0
	for indent < len(line) && line[indent] == ' ' {
		indent++
	}
	return indent
}

func markdownListIndent(line []byte) (int, bool) {
	indent := markdownLineIndent(line)
	if indent > 3 || indent >= len(line) {
		return indent, false
	}
	i := indent
	if line[i] == '-' || line[i] == '+' || line[i] == '*' {
		return indent, i+1 < len(line) && (line[i+1] == ' ' || line[i+1] == '\t')
	}
	start := i
	for i < len(line) && line[i] >= '0' && line[i] <= '9' && i-start < 9 {
		i++
	}
	return indent, i > start && i+1 < len(line) && (line[i] == '.' || line[i] == ')') && (line[i+1] == ' ' || line[i+1] == '\t')
}

func markdownQuoteLine(line []byte) bool {
	line = bytes.TrimLeft(line, " ")
	return len(line) > 0 && line[0] == '>'
}

func markdownContentIndent(line []byte) int {
	for {
		trimmed := bytes.TrimLeft(line, " ")
		spaces := len(line) - len(trimmed)
		if len(trimmed) == 0 || trimmed[0] != '>' {
			return spaces
		}
		line = trimmed[1:]
		if len(line) > 0 && line[0] == ' ' {
			line = line[1:]
		}
	}
}

func markdownFenceContent(line []byte) []byte {
	for {
		trimmed := bytes.TrimLeft(line, " ")
		spaces := len(line) - len(trimmed)
		if len(trimmed) == 0 || trimmed[0] != '>' {
			return line
		}
		line = trimmed[1:]
		if len(line) > 0 && line[0] == ' ' {
			line = line[1:]
		}
		if spaces > 3 {
			return nil
		}
	}
}

func markdownFenceStart(line []byte) (byte, int, bool) {
	line = markdownFenceContent(line)
	if line == nil {
		return 0, 0, false
	}
	trimmed := bytes.TrimLeft(line, " ")
	if len(line)-len(trimmed) > 3 || len(trimmed) < 3 || (trimmed[0] != '`' && trimmed[0] != '~') {
		return 0, 0, false
	}
	line = trimmed
	if len(line) < 3 || (line[0] != '`' && line[0] != '~') {
		return 0, 0, false
	}
	marker := line[0]
	count := 0
	for count < len(line) && line[count] == marker {
		count++
	}
	return marker, count, count >= 3
}

func isMarkdownFenceClose(line []byte, marker byte, minimum int) bool {
	line = markdownFenceContent(line)
	if line == nil {
		return false
	}
	trimmed := bytes.TrimLeft(line, " ")
	if len(line)-len(trimmed) > 3 || len(trimmed) == 0 || trimmed[0] != marker {
		return false
	}
	line = trimmed
	if len(line) == 0 || line[0] != marker {
		return false
	}
	count := 0
	for count < len(line) && line[count] == marker {
		count++
	}
	if count < minimum {
		return false
	}
	return markdownBlank(line[count:])
}

func markdownHasReferenceCandidate(source []byte) bool {
	for offset := 0; offset < len(source); {
		open := bytes.IndexByte(source[offset:], '[')
		if open < 0 {
			return false
		}
		open += offset
		close := bytes.IndexByte(source[open+1:], ']')
		if close < 0 {
			return true
		}
		close += open + 1
		after := close + 1
		for after < len(source) && (source[after] == ' ' || source[after] == '\t') {
			after++
		}
		if after < len(source) && (source[after] == '(' || source[after] == '[') {
			offset = close + 1
			continue
		}
		return true
	}
	return false
}

func markdownRawHTMLStart(line []byte) bool {
	line = bytes.TrimLeft(line, " >\t")
	if len(line) < 2 || line[0] != '<' {
		return false
	}
	if line[1] == '!' || line[1] == '/' || line[1] == '?' {
		return true
	}
	for i := 1; i < len(line); i++ {
		if line[i] == '>' || line[i] == ' ' || line[i] == '\t' {
			return true
		}
		if line[i] == ':' || line[i] == '/' {
			return false // do not treat autolinks as raw HTML blocks
		}
	}
	return false
}
