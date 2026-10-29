package terminal

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/rivo/uniseg"
)

// ThemeToken is the small semantic style vocabulary used by psi's Pi-derived
// regular-mode components. It is not a user-configurable theme engine.
type ThemeToken string

const (
	TokenText            ThemeToken = "text"
	TokenMuted           ThemeToken = "muted"
	TokenBorder          ThemeToken = "border"
	TokenAccent          ThemeToken = "accent"
	TokenSuccess         ThemeToken = "success"
	TokenWarning         ThemeToken = "warning"
	TokenError           ThemeToken = "error"
	TokenHeading         ThemeToken = "mdHeading"
	TokenCode            ThemeToken = "mdCode"
	TokenCodeBlock       ThemeToken = "mdCodeBlock"
	TokenLink            ThemeToken = "mdLink"
	TokenQuote           ThemeToken = "mdQuote"
	TokenThinkingOff     ThemeToken = "thinkingOff"
	TokenThinkingMinimal ThemeToken = "thinkingMinimal"
	TokenThinkingLow     ThemeToken = "thinkingLow"
	TokenThinkingMedium  ThemeToken = "thinkingMedium"
	TokenThinkingHigh    ThemeToken = "thinkingHigh"
	TokenThinkingXhigh   ThemeToken = "thinkingXhigh"
	TokenThinkingMax     ThemeToken = "thinkingMax"
)

// Pi's indexed fallback maps semantic families to terminal palette slots. The
// neutral family is terminal-default; secondary neutral text is faint (SGR 2).
// Thinking levels use Pi's indexed slate/blue/periwinkle/violet/magenta/red map.
func tokenSGR(token ThemeToken) string {
	switch token {
	case TokenMuted, TokenQuote:
		return "2"
	case TokenBorder, TokenThinkingMinimal, TokenThinkingLow:
		return "34" // palette slot 4, blue
	case TokenAccent, TokenCode, TokenThinkingHigh:
		return "35" // palette slot 5, violet
	case TokenCodeBlock, TokenSuccess:
		return "32" // palette slot 2, green
	case TokenWarning, TokenHeading:
		return "33" // palette slot 3, yellow
	case TokenError:
		return "31" // palette slot 1, red
	case TokenLink:
		return "34" // Pi's mdLink uses the blue family, palette slot 4
	case TokenThinkingMedium:
		return "36" // palette slot 6, calamine
	case TokenThinkingXhigh:
		return "95" // palette slot 13, bright magenta
	case TokenThinkingMax:
		return "31"
	default:
		return ""
	}
}

func thinkingToken(level string) ThemeToken {
	switch level {
	case "minimal":
		return TokenThinkingMinimal
	case "low":
		return TokenThinkingLow
	case "medium":
		return TokenThinkingMedium
	case "high":
		return TokenThinkingHigh
	case "xhigh":
		return TokenThinkingXhigh
	case "max":
		return TokenThinkingMax
	default:
		return TokenThinkingOff
	}
}

type StyledSpan struct {
	Text      string
	Token     ThemeToken
	Bold      bool
	Italic    bool
	Underline bool
}

// StyledRow contains untrusted, plain-text spans. Only Serialize generates
// SGR. Cell width is always measured from Plain, never from ANSI bytes.
type StyledRow struct{ Spans []StyledSpan }

func (r StyledRow) Plain() string {
	var b strings.Builder
	for _, span := range r.Spans {
		b.WriteString(SanitizeStatusRow(span.Text))
	}
	return b.String()
}

func (r StyledRow) Width() int { return CellWidth(r.Plain()) }

func (r StyledRow) Serialize(noColor bool) string {
	var b strings.Builder
	var text strings.Builder
	var active StyledSpan
	activeSet := false
	flush := func() {
		if text.Len() == 0 {
			return
		}
		var codes []string
		if !noColor {
			if sgr := tokenSGR(active.Token); sgr != "" {
				codes = append(codes, sgr)
			}
			if active.Bold {
				codes = append(codes, "1")
			}
			if active.Italic {
				codes = append(codes, "3")
			}
			if active.Underline {
				codes = append(codes, "4")
			}
		}
		if len(codes) > 0 {
			b.WriteString("\x1b[")
			b.WriteString(strings.Join(codes, ";"))
			b.WriteByte('m')
		}
		b.WriteString(text.String())
		if len(codes) > 0 {
			b.WriteString("\x1b[0m")
		}
		text.Reset()
	}
	for _, span := range r.Spans {
		plain := SanitizeStatusRow(span.Text)
		if plain == "" {
			continue
		}
		if activeSet && (active.Token != span.Token || active.Bold != span.Bold || active.Italic != span.Italic || active.Underline != span.Underline) {
			flush()
			activeSet = false
		}
		if !activeSet {
			active, activeSet = span, true
		}
		text.WriteString(plain)
	}
	flush()
	return b.String()
}

// ClipStyledRow clips at grapheme boundaries using the plain-cell width policy.
// FormatStyledRow applies the plain-cell width boundary, sanitizes text, and
// serializes only the small semantic token vocabulary.
func FormatStyledRow(row StyledRow, width int, noColor bool) string {
	return ClipStyledRow(row, max(0, width-1)).Serialize(colorDisabled(PresentationSnapshot{NoColor: noColor}))
}

func ClipStyledRow(row StyledRow, maxCells int) StyledRow {
	maxCells = max(0, maxCells)
	left := maxCells
	out := StyledRow{}
	for _, span := range row.Spans {
		text := SanitizeStatusRow(span.Text)
		clipped := TruncateToWidth(text, left)
		if clipped != "" {
			out.Spans = append(out.Spans, StyledSpan{Text: clipped, Token: span.Token, Bold: span.Bold, Italic: span.Italic, Underline: span.Underline})
			left -= CellWidth(clipped)
		}
		if CellWidth(clipped) < CellWidth(text) || left == 0 {
			break
		}
	}
	return out
}

// PresentationSnapshot is computed by the application at state boundaries.
// Rendering it never reads files, credentials, history, or provider state.
type PresentationSnapshot struct {
	WorkingDirectory string
	HomeDirectory    string
	SessionName      string
	Usage            string // only locally stored counters; empty means unavailable
	ContextEstimate  string // explicitly marked approximation, e.g. ~8%/100k
	Provider         string
	ProviderCount    int
	Model            string
	Thinking         string
	NoColor          bool
}

type EditorView struct {
	Rows      []string
	CellWidth []int
	CursorRow int
	CursorCol int
}

func homeAbbreviated(path, home string) string {
	if path == "" {
		return ""
	}
	path = filepath.Clean(path)
	if home == "" {
		return SanitizeStatusRow(path)
	}
	home = filepath.Clean(home)
	rel, err := filepath.Rel(home, path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return SanitizeStatusRow(path)
	}
	if rel == "." {
		return "~"
	}
	return "~" + string(filepath.Separator) + SanitizeStatusRow(rel)
}

// truncateFooterText mirrors Pi's truncateToWidth for sanitized footer text.
func truncateFooterText(text string, width int, ellipsis string) string {
	if width <= 0 {
		return ""
	}
	if CellWidth(text) <= width {
		return text
	}
	ellipsisWidth := CellWidth(ellipsis)
	if ellipsisWidth >= width {
		return TruncateToWidth(ellipsis, width)
	}
	return TruncateToWidth(text, width-ellipsisWidth) + ellipsis
}

func formatFooter(snapshot PresentationSnapshot, width int, height int, noColor bool) ([]string, []int) {
	if height <= 0 || width <= 0 {
		return nil, nil
	}
	width = max(1, width-1) // reserve the final cell against terminal autowrap
	path := homeAbbreviated(snapshot.WorkingDirectory, snapshot.HomeDirectory)
	if snapshot.SessionName != "" {
		path += " • " + SanitizeStatusRow(snapshot.SessionName)
	}
	path = truncateFooterText(path, width, "...")
	secondLeft := truncateFooterText(strings.TrimSpace(snapshot.Usage+" "+snapshot.ContextEstimate), width, "...")
	modelSide := SanitizeStatusRow(snapshot.Model)
	if snapshot.Thinking != "" {
		modelSide += " • " + SanitizeStatusRow(snapshot.Thinking)
	}
	right := modelSide
	if snapshot.ProviderCount > 1 && snapshot.Provider != "" && snapshot.Model != "" {
		withProvider := "(" + SanitizeStatusRow(snapshot.Provider) + ") " + modelSide
		// Pi includes the provider only when the complete label fits alongside
		// the existing stats and its minimum two-cell separator.
		if CellWidth(secondLeft)+2+CellWidth(withProvider) <= width {
			right = withProvider
		}
	}
	pathRow := ClipStyledRow(StyledRow{Spans: []StyledSpan{{Text: path, Token: TokenMuted}}}, width)
	leftRow := ClipStyledRow(StyledRow{Spans: []StyledSpan{{Text: secondLeft, Token: TokenMuted}}}, width)
	statsWidth := leftRow.Width()
	rightWidth := CellWidth(right)
	gap := 0
	// Pi keeps the stats left and truncates the model side when the minimum gap cannot fit.
	if statsWidth+2+rightWidth <= width {
		gap = width - statsWidth - rightWidth
	} else {
		availableForRight := width - statsWidth - 2
		if availableForRight > 0 {
			right = TruncateToWidth(right, availableForRight)
		} else {
			right = ""
		}
		gap = max(0, width-statsWidth-CellWidth(right))
	}
	second := StyledRow{Spans: append(append([]StyledSpan(nil), leftRow.Spans...), StyledSpan{Text: strings.Repeat(" ", gap), Token: TokenText}, StyledSpan{Text: right, Token: TokenText})}
	second = ClipStyledRow(second, width)
	rows := []string{pathRow.Serialize(noColor), second.Serialize(noColor)}
	cells := []int{pathRow.Width(), second.Width()}
	if height == 1 {
		return rows[:0], cells[:0]
	}
	if height == 2 {
		return rows[1:], cells[1:]
	}
	return rows, cells
}

func colorDisabled(snapshot PresentationSnapshot) bool {
	if snapshot.NoColor {
		return true
	}
	_, set := os.LookupEnv("NO_COLOR")
	return set
}

// ComposeEditorView builds Pi's horizontal-border editor and compact footer.
func ComposeEditorView(width, height int, buf []rune, pos int, snapshot PresentationSnapshot, activity string) EditorView {
	return ComposeConsoleView(width, height, buf, pos, snapshot, activity, "")
}

// ComposeConsoleView composes the active assistant fragment above the editor,
// then the persistent footer. The fragment is bounded by the console owner.
func ComposeConsoleView(width, height int, buf []rune, pos int, snapshot PresentationSnapshot, activity, fragment string) EditorView {
	width = max(1, width)
	fragmentRows := []string{}
	if fragment != "" && width > 3 {
		fragmentRows = wrapAssistantFragment(fragment, width)
	}
	return ComposeConsoleViewComponents(width, height, buf, pos, snapshot, activity, fragmentRows, nil)
}

// ComposeConsoleViewWithRows composes already-rendered Markdown rows above the
// editor and footer. Rows carry generated SGR only; their visible width is plain.
func ComposeConsoleViewWithRows(width, height int, buf []rune, pos int, snapshot PresentationSnapshot, activity string, fragmentRows []string) EditorView {
	return ComposeConsoleViewComponents(width, height, buf, pos, snapshot, activity, fragmentRows, nil)
}

// EditorComponentRowBudget leaves room for the focused input row, borders,
// and footer. Picker layout and composition must use the same height budget.
func EditorComponentRowBudget(height int) int {
	if height <= 2 {
		return 0
	}
	footer := 2
	if height < 8 {
		footer = 1
	}
	return max(0, height-footer-3)
}

// ComposeConsoleViewComponents places an active fragment above the editor,
// then editor-owned suggestions, then the persistent footer.
func ComposeConsoleViewComponents(width, height int, buf []rune, pos int, snapshot PresentationSnapshot, activity string, fragmentRows, completionRows []string) EditorView {
	width, height = max(1, width), max(1, height)
	noColor := colorDisabled(snapshot)
	layout := LayoutDraft(buf, pos, width)
	footerRows, footerWidths := formatFooter(snapshot, width, height, noColor)

	// At the smallest sizes keep the focused input usable before chrome.
	if height <= 2 {
		row := ""
		if len(layout.Rows) > 0 {
			row = TruncateToWidth(layout.Rows[layout.CursorRow], max(1, width-1))
		}
		col := min(layout.CursorCol, max(0, width-1))
		return EditorView{Rows: []string{row}, CellWidth: []int{CellWidth(row)}, CursorCol: col}
	}

	footerCount := len(footerRows)
	if height < 8 && footerCount > 0 {
		// Preserve model/status line before the optional folder line.
		footerRows, footerWidths = footerRows[len(footerRows)-1:], footerWidths[len(footerWidths)-1:]
		footerCount = 1
	}
	borderCount := 2
	preferred := max(5, int(float64(height)*0.3))
	preferred = min(preferred, len(layout.Rows))
	// Preserve the focused editor row, then allocate remaining height to the
	// active picker/completion before expanding the draft viewport.
	componentBudget := EditorComponentRowBudget(height)
	if len(completionRows) > componentBudget {
		completionRows = completionRows[:componentBudget]
	}
	fragmentBudget := max(0, componentBudget-len(completionRows))
	if len(fragmentRows) > fragmentBudget {
		fragmentRows = fragmentRows[len(fragmentRows)-fragmentBudget:]
	}
	bodyBudget := max(1, height-footerCount-borderCount-len(fragmentRows)-len(completionRows))
	bodyBudget = min(bodyBudget, preferred)
	bodyBudget = min(bodyBudget, len(layout.Rows))
	start := max(0, layout.CursorRow-bodyBudget+1)
	end := min(len(layout.Rows), start+bodyBudget)
	if end-start < bodyBudget {
		start = max(0, end-bodyBudget)
	}
	above, below := start, len(layout.Rows)-end
	borderToken := thinkingToken(snapshot.Thinking)
	borderRow := func(top bool) (string, int) {
		borderWidth := max(1, width-1)
		overflow := ""
		status := ""
		if top {
			if activity != "" {
				reserve := 4
				if above > 0 {
					reserve += CellWidth(" ↑ " + itoa(above) + " more ")
				}
				status = compactActivity(SanitizeStatusRow(activity), max(1, borderWidth-reserve))
			}
			if above > 0 {
				overflow = " ↑ " + itoa(above) + " more "
			}
		} else if below > 0 {
			overflow = " ↓ " + itoa(below) + " more "
		}
		text := piBorderRow(borderWidth, status, overflow)
		row := StyledRow{Spans: []StyledSpan{{Text: text, Token: borderToken}}}
		return row.Serialize(noColor), row.Width()
	}
	rows := make([]string, 0, len(fragmentRows)+borderCount+bodyBudget+footerCount)
	cells := make([]int, 0, cap(rows))
	for _, row := range fragmentRows {
		rows, cells = append(rows, row), append(cells, CellWidth(Sanitize(row)))
	}
	top, topWidth := borderRow(true)
	rows, cells = append(rows, top), append(cells, topWidth)
	cursorRow := len(fragmentRows) + layout.CursorRow - start + 1
	for i := start; i < end; i++ {
		plain := SanitizeStatusRow(layout.Rows[i])
		clipped := TruncateToWidth(plain, max(0, width-1))
		rows, cells = append(rows, clipped), append(cells, CellWidth(clipped))
	}
	bottom, bottomWidth := borderRow(false)
	rows, cells = append(rows, bottom), append(cells, bottomWidth)
	for _, row := range completionRows {
		plain := Sanitize(row)
		if CellWidth(plain) > width-1 {
			row = TruncateToWidth(plain, max(0, width-1))
		}
		rows, cells = append(rows, row), append(cells, CellWidth(Sanitize(row)))
	}
	rows = append(rows, footerRows...)
	cells = append(cells, footerWidths...)
	if len(rows) > height {
		// Height fallback must not discard the focused row or cancellation signal.
		rows = rows[len(rows)-height:]
		cells = cells[len(cells)-height:]
		cursorRow = min(len(rows)-1, max(0, cursorRow-(len(rows)-(height))))
	}
	return EditorView{Rows: rows, CellWidth: cells, CursorRow: cursorRow, CursorCol: min(layout.CursorCol, max(0, width-1))}
}

// ComposeConsoleViewPicker renders an application-owned picker in place of the
// editor region. Pi's showSelector clears the editor container and installs a
// bordered selector that owns its search query, so psi renders the picker
// border, the query row, the bounded list, and the closing border instead of
// the editor block. The committed transcript above stays untouched.
func ComposeConsoleViewPicker(width, height int, buf []rune, pos int, snapshot PresentationSnapshot, activity string, fragmentRows, pickerRows []string, mode PickerMode) EditorView {
	width, height = max(1, width), max(1, height)
	noColor := colorDisabled(snapshot)
	footerRows, footerWidths := formatFooter(snapshot, width, height, noColor)
	footerCount := len(footerRows)
	if height < 8 && footerCount > 0 {
		// Preserve model/status line before the optional folder line.
		footerRows, footerWidths = footerRows[len(footerRows)-1:], footerWidths[len(footerWidths)-1:]
		footerCount = 1
	}
	withQuery := mode == PickerSearch
	queryRows := 0
	if withQuery {
		queryRows = 1
	}

	// At the smallest sizes keep the focused query usable before chrome.
	if height <= 2 {
		row, col := "", 0
		if withQuery {
			row, col = pickerQueryRow(buf, pos, width)
		} else if len(pickerRows) > 0 {
			row = TruncateToWidth(Sanitize(pickerRows[0]), max(0, width-1))
		}
		return EditorView{Rows: []string{row}, CellWidth: []int{CellWidth(row)}, CursorRow: 0, CursorCol: col}
	}

	// The picker owns its top border, the optional search input, and its
	// bottom border. The query and one list row outrank the chrome on a very
	// short terminal, so the footer and borders give way first.
	minBody := 0
	if len(pickerRows) > 0 {
		minBody = 1
	}
	keepTop, keepBottom := true, true
	if height < queryRows+minBody+2+footerCount {
		// The footer is the first thing to give way.
		footerRows, footerWidths = nil, nil
		footerCount = 0
	}
	if height < queryRows+minBody+2 {
		keepBottom = false
	}
	if height < queryRows+minBody+1 {
		keepTop = false
	}
	chromeRows := 0
	if keepTop {
		chromeRows++
	}
	if keepBottom {
		chromeRows++
	}
	bodySpace := max(0, height-queryRows-chromeRows-footerCount)
	bodyCount := min(len(pickerRows), bodySpace)

	fragment := fragmentRows
	fragmentCount := max(0, bodySpace-bodyCount)
	if len(fragment) > fragmentCount {
		fragment = fragment[len(fragment)-fragmentCount:]
	}

	border := StyledRow{Spans: []StyledSpan{{Text: strings.Repeat("─", max(1, width-1)), Token: thinkingToken(snapshot.Thinking)}}}
	if activity != "" && keepTop {
		// The busy indicator stays visible even when a picker owns the region.
		status := compactActivity(SanitizeStatusRow(activity), max(1, width-1-4))
		if status != "" {
			label := "── " + status + " "
			fill := max(0, max(1, width-1)-CellWidth(label))
			border = StyledRow{Spans: []StyledSpan{
				{Text: "── ", Token: thinkingToken(snapshot.Thinking)},
				{Text: status, Token: TokenMuted},
				{Text: " ", Token: TokenMuted},
				{Text: strings.Repeat("─", fill), Token: thinkingToken(snapshot.Thinking)},
			}}
		}
	}
	border = ClipStyledRow(border, max(1, width-1))
	borderText := border.Serialize(noColor)
	borderCells := border.Width()

	rows := make([]string, 0, len(fragment)+queryRows+chromeRows+bodyCount+footerCount)
	cells := make([]int, 0, cap(rows))
	for _, row := range fragment {
		rows, cells = append(rows, row), append(cells, CellWidth(Sanitize(row)))
	}
	cursorRow, cursorCol := len(rows), 0
	if keepTop {
		rows, cells = append(rows, borderText), append(cells, borderCells)
	}
	if withQuery {
		queryRow, col := pickerQueryRow(buf, pos, width)
		rows, cells = append(rows, queryRow), append(cells, CellWidth(queryRow))
		cursorRow, cursorCol = len(rows)-1, col
	}
	for _, row := range pickerRows[:bodyCount] {
		plain := Sanitize(row)
		if CellWidth(plain) > width-1 {
			row = TruncateToWidth(plain, max(0, width-1))
		}
		rows, cells = append(rows, row), append(cells, CellWidth(Sanitize(row)))
	}
	if keepBottom {
		rows, cells = append(rows, borderText), append(cells, borderCells)
	}
	rows = append(rows, footerRows...)
	cells = append(cells, footerWidths...)
	if len(rows) > height {
		// Height fallback must not discard the focused row or cancellation signal.
		rows = rows[len(rows)-height:]
		cells = cells[len(cells)-height:]
		cursorRow = min(len(rows)-1, max(0, cursorRow-(len(rows)-(height))))
	}
	return EditorView{Rows: rows, CellWidth: cells, CursorRow: cursorRow, CursorCol: min(cursorCol, max(0, width-1))}
}

// piBorderRow builds Pi's editor border row. The working status sits at the
// left of the top border, the overflow count is centered like Pi's
// createScrollBorder, and the rest is horizontal fill.
func piBorderRow(width int, status string, overflow string) string {
	if width <= 0 {
		return ""
	}
	status = TruncateToWidth(status, width)
	overflowWidth := CellWidth(overflow)
	overflowStart := max(0, (width-overflowWidth)/2)
	statusWidth := CellWidth(status)
	canFitOverflow := overflow != "" && overflowWidth+2 <= width && overflowStart-(3+statusWidth+1) >= 1
	switch {
	case statusWidth > 0 && canFitOverflow:
		left := 3 + statusWidth + 1
		return "── " + status + " " + strings.Repeat("─", max(0, overflowStart-left)) + overflow + strings.Repeat("─", max(0, width-overflowStart-overflowWidth))
	case statusWidth > 0:
		if width >= statusWidth+5 {
			return "── " + status + " " + strings.Repeat("─", max(0, width-statusWidth-4))
		}
		return TruncateToWidth(status, width)
	case overflow != "" && overflowWidth+2 <= width:
		return strings.Repeat("─", overflowStart) + overflow + strings.Repeat("─", max(0, width-overflowStart-overflowWidth))
	default:
		return strings.Repeat("─", width)
	}
}

// pickerQueryRow renders Pi's selector search input: the "> " prompt, the
// bounded query text, and the cursor column of the query inside that row.
func pickerQueryRow(buf []rune, pos int, width int) (string, int) {
	const prompt = "> "
	promptCells := CellWidth(prompt)
	available := max(1, width-1-promptCells)
	pos = max(0, min(pos, len(buf)))
	query := SanitizeStatusRow(string(buf))
	cursorCells := CellWidth(SanitizeStatusRow(string(buf[:pos])))
	total := CellWidth(query)
	start := 0
	if total > available {
		half := available / 2
		switch {
		case cursorCells <= half:
			start = 0
		case cursorCells >= total-half:
			start = max(0, total-available)
		default:
			start = cursorCells - half
		}
		start = max(0, min(start, max(0, total-available)))
	}
	visible := SliceCells(query, start, available)
	cursorCol := promptCells + cursorCells - start
	return prompt + visible, max(0, min(cursorCol, width-1))
}

func compactActivity(status string, maxWidth int) string {
	if maxWidth <= 0 {
		return ""
	}
	if CellWidth(status) <= maxWidth {
		return status
	}
	if keyStart := strings.Index(status, "Esc/Ctrl+C"); keyStart >= 0 {
		keyEnd := keyStart + len("Esc/Ctrl+C")
		if CellWidth(status[:keyEnd]) <= maxWidth {
			return TruncateToWidth(status, maxWidth)
		}
		if clusters := Graphemes(status); len(clusters) > 0 {
			compact := clusters[0] + " Esc/Ctrl+C"
			if CellWidth(compact) <= maxWidth {
				return compact
			}
			return TruncateToWidth(clusters[0], maxWidth)
		}
	}
	return TruncateToWidth(status, maxWidth)
}

func wrapAssistantFragment(text string, width int) []string {
	const indent = "  "
	limit := max(1, width-1)
	cells := CellWidth(indent)
	var rows []string
	var line strings.Builder
	line.WriteString(indent)
	flush := func() {
		rows = append(rows, line.String())
		line.Reset()
		line.WriteString(indent)
		cells = CellWidth(indent)
	}
	gr := uniseg.NewGraphemes(text)
	for gr.Next() {
		cluster := gr.Str()
		if cluster == "\n" {
			flush()
			continue
		}
		widthOf := GraphemeWidth(cluster)
		if cluster == "\t" {
			widthOf = 8 - cells%8
		}
		if cells > CellWidth(indent) && cells+widthOf > limit {
			flush()
			if cluster == "\t" {
				widthOf = 8 - cells%8
			}
		}
		if cluster == "\t" {
			line.WriteString(strings.Repeat(" ", min(widthOf, max(0, limit-cells))))
			cells += min(widthOf, max(0, limit-cells))
		} else if widthOf > limit-CellWidth(indent) {
			line.WriteByte('?')
			cells++
		} else {
			line.WriteString(cluster)
			cells += widthOf
		}
	}
	rows = append(rows, line.String())
	return rows
}

func itoa(n int) string {
	if n <= 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}
