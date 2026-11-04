package marked

import (
	"strings"
	"unicode/utf8"
)

// Tokenizer is marked's `_Tokenizer`, ported from marked/src/Tokenizer.ts.
type Tokenizer struct {
	// Options holds the options of the tokenizer.
	Options *MarkedOptions
	// Rules holds the grammar of the tokenizer.
	Rules Rules
	// Lexer holds the lexer that runs the tokenizer.
	Lexer *Lexer
	// Overrides holds the tokenizer methods that an extension replaced, like
	// marked's "overwrite" tokenizer extensions.
	Overrides *TokenizerOverrides
}

// TokenizerOverrides holds the tokenizer methods that an extension replaced.
// A method that is not set keeps the built in behaviour.
type TokenizerOverrides struct {
	// EmStrong replaces the em and strong tokenizer.
	EmStrong func(t *Tokenizer, src, maskedSrc, prevChar string) (*Token, bool)
	// Del replaces the strikethrough tokenizer.
	Del func(t *Tokenizer, src, maskedSrc, prevChar string) (*Token, bool)
}

// newTokenizer creates a tokenizer, like marked's `_Tokenizer` constructor.
func newTokenizer(options *MarkedOptions) *Tokenizer {
	return &Tokenizer{Options: options}
}

// outputLink is marked's `outputLink`.
func outputLink(cap *match, link Link, raw string, lexer *Lexer, rules Rules) *Token {
	href := link.Href
	title := link.Title
	text := replaceGroup(rules.Other.OutputLinkReplace, cap.group(1), 1)
	isImage := strings.HasPrefix(cap.group(0), "!")

	lexer.State.InLink = true
	outerLinkEmitted := lexer.State.LinkEmitted
	outerInRawBlock := lexer.State.InRawBlock
	lexer.State.LinkEmitted = false
	tokens := lexer.inlineTokens(text, nil)
	textHasLink := lexer.State.LinkEmitted
	lexer.State.LinkEmitted = outerLinkEmitted
	lexer.State.InLink = false

	if !isImage {
		// CommonMark: "Links may not contain other links, at any level of
		// nesting." Bail so the caller falls through to text and the inner link
		// is the one kept. Images are exempt: their text is flattened into an
		// alt attribute.
		if textHasLink {
			// these tokens are discarded, so undo the raw-block state they
			// opened; leaving it set would suppress escaping for the text that
			// is re-scanned
			lexer.State.InRawBlock = outerInRawBlock
			return nil
		}
		lexer.State.LinkEmitted = true
	}

	token := &Token{
		Type:   TypeLink,
		Raw:    raw,
		Href:   href,
		Text:   text,
		Tokens: tokens,
	}
	if title != "" {
		titleCopy := title
		token.Title = &titleCopy
	}
	if isImage {
		token.Type = TypeImage
	}
	return token
}

// indentCodeCompensation is marked's `indentCodeCompensation`.
func indentCodeCompensation(raw string, text string, rules Rules) string {
	cap := exec(rules.Other.IndentCodeCompensation, raw)
	if cap == nil {
		return text
	}

	indentToCode := cap.group(1)

	lines := strings.Split(text, "\n")
	for i, node := range lines {
		matchIndentInNode := exec(rules.Other.BeginningSpace, node)
		if matchIndentInNode == nil {
			continue
		}

		indentInNode := matchIndentInNode.whole()
		if utf16Len(indentInNode) >= utf16Len(indentToCode) {
			lines[i] = substring(node, utf16Len(indentToCode))
		}
	}
	return strings.Join(lines, "\n")
}

// Space is marked's `space`.
func (t *Tokenizer) Space(src string) *Token {
	cap := exec(t.Rules.Block.Newline, src)
	if cap != nil && utf16Len(cap.whole()) > 0 {
		return &Token{Type: TypeSpace, Raw: cap.whole()}
	}
	return nil
}

// Code is marked's `code`.
func (t *Tokenizer) Code(src string) *Token {
	cap := exec(t.Rules.Block.Code, src)
	if cap == nil {
		return nil
	}
	raw := cap.whole()
	if !t.Options.PedanticEnabled() {
		raw = TrimTrailingBlankLines(raw)
	}
	text := replaceAll(t.Rules.Other.CodeRemoveIndent, raw, func(string) string { return "" })
	return &Token{
		Type:           TypeCode,
		Raw:            raw,
		CodeBlockStyle: "indented",
		Text:           text,
	}
}

// Fences is marked's `fences`.
func (t *Tokenizer) Fences(src string) *Token {
	cap := exec(t.Rules.Block.Fences, src)
	if cap == nil {
		return nil
	}
	raw := cap.whole()
	text := indentCodeCompensation(raw, cap.group(3), t.Rules)
	lang := cap.group(2)
	if lang != "" {
		lang = strings.TrimSpace(lang)
		lang = replaceGroup(t.Rules.Inline.AnyPunctuation, lang, 1)
	}
	return &Token{
		Type: TypeCode,
		Raw:  raw,
		Lang: lang,
		Text: text,
	}
}

// Heading is marked's `heading`.
func (t *Tokenizer) Heading(src string) *Token {
	cap := exec(t.Rules.Block.Heading, src)
	if cap == nil {
		return nil
	}
	text := strings.TrimSpace(cap.group(2))

	// remove trailing #s
	if matches(t.Rules.Other.EndingHash, text) {
		trimmed := Rtrim(text, "#", false)
		if t.Options.PedanticEnabled() {
			text = strings.TrimSpace(trimmed)
		} else if trimmed == "" || matches(t.Rules.Other.EndingSpaceChar, trimmed) {
			// CommonMark requires space before trailing #s
			text = strings.TrimSpace(trimmed)
		}
	}

	return &Token{
		Type:        TypeHeading,
		Raw:         Rtrim(cap.whole(), "\n", false),
		Depth:       utf16Len(cap.group(1)),
		Text:        text,
		HasInline:   true,
		InlineIndex: t.Lexer.inline(text),
	}
}

// Hr is marked's `hr`.
func (t *Tokenizer) Hr(src string) *Token {
	cap := exec(t.Rules.Block.Hr, src)
	if cap == nil {
		return nil
	}
	return &Token{Type: TypeHR, Raw: Rtrim(cap.whole(), "\n", false)}
}

// Blockquote is marked's `blockquote`.
func (t *Tokenizer) Blockquote(src string) *Token {
	cap := exec(t.Rules.Block.Blockquote, src)
	if cap == nil {
		return nil
	}
	lines := strings.Split(Rtrim(cap.whole(), "\n", false), "\n")
	raw := ""
	text := ""
	var tokens []Token

	for len(lines) > 0 {
		inBlockquote := false
		var currentLines []string

		i := 0
		for ; i < len(lines); i++ {
			// get lines up to a continuation
			if matches(t.Rules.Other.BlockquoteStart, lines[i]) {
				currentLines = append(currentLines, lines[i])
				inBlockquote = true
			} else if !inBlockquote {
				currentLines = append(currentLines, lines[i])
			} else {
				break
			}
		}
		lines = lines[i:]

		currentRaw := strings.Join(currentLines, "\n")
		currentText := replaceAll(t.Rules.Other.BlockquoteSetextReplace, currentRaw, func(match string) string {
			return "\n    " + match[1:]
		})
		currentText = replaceAll(t.Rules.Other.BlockquoteSetextReplace2, currentText, func(string) string { return "" })
		if raw != "" {
			raw = raw + "\n" + currentRaw
		} else {
			raw = currentRaw
		}
		if text != "" {
			text = text + "\n" + currentText
		} else {
			text = currentText
		}

		// parse blockquote lines as top level tokens
		// merge paragraphs if this is a continuation
		top := t.Lexer.State.Top
		t.Lexer.State.Top = true
		tokens = t.Lexer.blockTokens(currentText, tokens, true)
		t.Lexer.State.Top = top

		// if there is no continuation then we are done
		if len(lines) == 0 {
			break
		}

		lastToken := lastTokenOf(tokens)

		switch {
		case lastToken != nil && lastToken.Type == TypeCode:
			// blockquote continuation cannot be preceded by a code block
			return &Token{Type: TypeBlockquote, Raw: raw, Text: text, Tokens: tokens}
		case lastToken != nil && lastToken.Type == TypeBlockquote:
			// include continuation in nested blockquote
			// The old token is copied because the slot it occupies in `tokens`
			// is replaced below, while marked keeps the old token object alive.
			oldToken := *lastToken
			// The continuation lines belong to the same nesting frame as the
			// nested blockquote, which already had one '>' marker stripped, so
			// strip one marker from them too. Otherwise a restated marker after
			// a lazy line is re-parsed as a spurious deeper blockquote.
			continuation := strings.Join(lines, "\n")
			newText := oldToken.Raw + "\n" + replaceAll(t.Rules.Other.BlockquoteSetextReplace2, continuation, func(string) string { return "" })
			newToken := t.Blockquote(newText)
			if newToken == nil {
				return &Token{Type: TypeBlockquote, Raw: raw, Text: text, Tokens: tokens}
			}
			tokens[len(tokens)-1] = *newToken

			raw = raw + "\n" + continuation
			text = cutPrefix(text, utf16Len(text)-utf16Len(oldToken.Text)) + newToken.Text
			return &Token{Type: TypeBlockquote, Raw: raw, Text: text, Tokens: tokens}
		case lastToken != nil && lastToken.Type == TypeList:
			// include continuation in nested list
			oldToken := *lastToken
			newText := oldToken.Raw + "\n" + strings.Join(lines, "\n")
			newToken := t.List(newText)
			if newToken == nil {
				return &Token{Type: TypeBlockquote, Raw: raw, Text: text, Tokens: tokens}
			}
			tokens[len(tokens)-1] = *newToken

			raw = cutPrefix(raw, utf16Len(raw)-utf16Len(oldToken.Raw)) + newToken.Raw
			text = cutPrefix(text, utf16Len(text)-utf16Len(oldToken.Raw)) + newToken.Raw
			lines = strings.Split(cutPrefix(newText, utf16Len(newText)-utf16Len(tokens[len(tokens)-1].Raw)), "\n")
			continue
		}
	}

	return &Token{Type: TypeBlockquote, Raw: raw, Text: text, Tokens: tokens}
}

// List is marked's `list`.
func (t *Tokenizer) List(src string) *Token {
	cap := exec(t.Rules.Block.List, src)
	if cap == nil {
		return nil
	}
	bull := strings.TrimSpace(cap.group(1))
	isordered := utf16Len(bull) > 1

	list := &Token{
		Type:    TypeList,
		Raw:     "",
		Ordered: isordered,
		Loose:   false,
		Items:   []ListItem{},
	}
	if isordered {
		value, ok := parseLeadingNumber(bull[:len(bull)-1])
		if ok {
			list.Start = value
			list.HasStart = true
		}
	}

	if isordered {
		bull = `\d{1,9}\` + bull[len(bull)-1:]
	} else {
		bull = `\` + bull
	}

	if t.Options.PedanticEnabled() && !isordered {
		bull = "[*+-]"
	}

	// Get next list item
	itemRegex := t.Rules.Other.ListItemRegex(bull)
	endsWithBlankLine := false
	// Check if current bullet point can start a new List Item
	for src != "" {
		endEarly := false
		raw := ""
		itemContents := ""

		cap = exec(itemRegex, src)
		if cap == nil {
			break
		}

		if matches(t.Rules.Block.Hr, src) { // End list if bullet was actually HR
			break
		}

		raw = cap.whole()
		src = substring(src, utf16Len(raw))

		line := ExpandTabs(firstLine(cap.group(2)), utf16Len(cap.group(1)))
		nextLine := firstLine(src)
		blankLine := strings.TrimSpace(line) == ""

		indent := 0
		switch {
		case t.Options.PedanticEnabled():
			indent = 2
			itemContents = jsTrimStart(line)
		case blankLine:
			indent = utf16Len(cap.group(1)) + 1
		default:
			indent = indexOfNonSpace(line)
			// Treat indented code blocks (> 4 spaces) as having only 1 indent
			if indent > 4 {
				indent = 1
			}
			itemContents = substring(line, indent)
			indent += utf16Len(cap.group(1))
		}

		if blankLine && matches(t.Rules.Other.BlankLine, nextLine) { // Items begin with at most one blank line
			raw += nextLine + "\n"
			src = substring(src, utf16Len(nextLine)+1)
			endEarly = true
		}

		if !endEarly {
			nextBulletRegex := t.Rules.Other.NextBulletRegex(indent)
			hrRegex := t.Rules.Other.HrRegex(indent)
			fencesBeginRegex := t.Rules.Other.FencesBeginRegex(indent)
			headingBeginRegex := t.Rules.Other.HeadingBeginRegex(indent)
			htmlBeginRegex := t.Rules.Other.HtmlBeginRegex(indent)
			blockquoteBeginRegex := t.Rules.Other.BlockquoteBeginRegex(indent)

			// Check if following lines should be included in List Item
			for src != "" {
				rawLine := firstLine(src)
				nextLine := rawLine
				var nextLineWithoutTabs string

				// Re-align to follow commonmark nesting rules
				if t.Options.PedanticEnabled() {
					nextLine = replaceAll(t.Rules.Other.ListReplaceNesting, nextLine, func(string) string { return "  " })
					nextLineWithoutTabs = nextLine
				} else {
					nextLineWithoutTabs = replaceAll(t.Rules.Other.TabCharGlobal, nextLine, func(string) string { return "    " })
				}

				// End list item if found code fences
				if matches(fencesBeginRegex, nextLine) {
					break
				}

				// End list item if found start of new heading
				if matches(headingBeginRegex, nextLine) {
					break
				}

				// End list item if found start of html block
				if matches(htmlBeginRegex, nextLine) {
					break
				}

				// End list item if found start of blockquote
				if matches(blockquoteBeginRegex, nextLine) {
					break
				}

				// End list item if found start of new bullet
				if matches(nextBulletRegex, nextLine) {
					break
				}

				// Horizontal rule found
				if matches(hrRegex, nextLine) {
					break
				}

				if indexOfNonSpace(nextLineWithoutTabs) >= indent || strings.TrimSpace(nextLine) == "" { // Dedent if possible
					itemContents += "\n" + substring(nextLineWithoutTabs, indent)
				} else {
					// not enough indentation
					if blankLine {
						break
					}

					// paragraph continuation unless last line was a different block level element
					if indexOfNonSpace(replaceAll(t.Rules.Other.TabCharGlobal, line, func(string) string { return "    " })) >= 4 { // indented code block
						break
					}
					if matches(fencesBeginRegex, line) {
						break
					}
					if matches(headingBeginRegex, line) {
						break
					}
					if matches(hrRegex, line) {
						break
					}

					itemContents += "\n" + nextLine
				}

				blankLine = strings.TrimSpace(nextLine) == ""

				raw += rawLine + "\n"
				src = substring(src, utf16Len(rawLine)+1)
				line = substring(nextLineWithoutTabs, indent)
			}
		}

		if !list.Loose {
			// If the previous item ended with a blank line, the list is loose
			if endsWithBlankLine {
				list.Loose = true
			} else if matches(t.Rules.Other.DoubleBlankLine, raw) {
				endsWithBlankLine = true
			}
		}

		list.Items = append(list.Items, ListItem{
			Raw:    raw,
			Task:   t.Options.GFMEnabled() && matches(t.Rules.Other.ListIsTask, itemContents),
			Loose:  false,
			Text:   itemContents,
			Tokens: []Token{},
		})

		list.Raw += raw
	}

	// Do not consume newlines at end of final item.
	if len(list.Items) == 0 {
		// not a list since there were no items
		return nil
	}
	lastItem := &list.Items[len(list.Items)-1]
	lastItem.Raw = jsTrimEnd(lastItem.Raw)
	lastItem.Text = jsTrimEnd(lastItem.Text)
	list.Raw = jsTrimEnd(list.Raw)

	// Item child tokens handled here at end because we needed to have the final
	// item to trim it first
	// First pass: tokenize items and finalize list.loose from spacers before
	// placing checkboxes
	for i := range list.Items {
		t.Lexer.State.Top = false
		list.Items[i].Tokens = t.Lexer.blockTokens(list.Items[i].Text, nil, false)

		if !list.Loose {
			// Check if list should be loose
			hasMultipleLineBreaks := false
			for _, token := range list.Items[i].Tokens {
				if token.Type == TypeSpace && matches(t.Rules.Other.AnyLine, token.Raw) {
					hasMultipleLineBreaks = true
					break
				}
			}
			list.Loose = hasMultipleLineBreaks
		}
	}

	// Second pass: place task checkboxes using the final list.loose
	for i := range list.Items {
		item := &list.Items[i]
		var itemToken *Token
		if len(item.Tokens) > 0 {
			itemToken = &item.Tokens[0]
		}
		if item.Task && itemToken != nil && (itemToken.Type == TypeText || itemToken.Type == TypeParagraph) {
			// Remove checkbox markdown from item tokens
			item.Text = replaceAll(t.Rules.Other.ListReplaceTask, item.Text, func(string) string { return "" })
			itemToken.Raw = replaceAll(t.Rules.Other.ListReplaceTask, itemToken.Raw, func(string) string { return "" })
			itemToken.Text = replaceAll(t.Rules.Other.ListReplaceTask, itemToken.Text, func(string) string { return "" })
			for j := len(t.Lexer.InlineQueue) - 1; j >= 0; j-- {
				if matches(t.Rules.Other.ListIsTask, t.Lexer.InlineQueue[j].src) {
					t.Lexer.InlineQueue[j].src = replaceAll(t.Rules.Other.ListReplaceTask, t.Lexer.InlineQueue[j].src, func(string) string { return "" })
					break
				}
			}

			taskRaw := exec(t.Rules.Other.ListTaskCheckbox, item.Raw)
			if taskRaw != nil {
				checked := taskRaw.whole() != "[ ]"
				checkboxToken := Token{
					Type:    TypeCheckbox,
					Raw:     taskRaw.whole() + " ",
					Checked: checked,
				}
				item.Checked = checked
				if list.Loose {
					if len(item.Tokens) > 0 && (item.Tokens[0].Type == TypeParagraph || item.Tokens[0].Type == TypeText) && item.Tokens[0].HasInline {
						item.Tokens[0].Raw = checkboxToken.Raw + item.Tokens[0].Raw
						item.Tokens[0].Text = checkboxToken.Raw + item.Tokens[0].Text
						item.Tokens[0].InlinePrepend = append([]Token{checkboxToken}, item.Tokens[0].InlinePrepend...)
					} else {
						item.Tokens = append([]Token{{
							Type:   TypeParagraph,
							Raw:    checkboxToken.Raw,
							Text:   checkboxToken.Raw,
							Tokens: []Token{checkboxToken},
						}}, item.Tokens...)
					}
				} else {
					item.Tokens = append([]Token{checkboxToken}, item.Tokens...)
				}
			}
		} else if item.Task {
			item.Task = false
		}
	}

	// Set all items to loose if list is loose
	if list.Loose {
		for i := range list.Items {
			list.Items[i].Loose = true
			for j := range list.Items[i].Tokens {
				if list.Items[i].Tokens[j].Type == TypeText {
					list.Items[i].Tokens[j].Type = TypeParagraph
				}
			}
		}
	}

	return list
}

// Html is marked's `html`.
func (t *Tokenizer) Html(src string) *Token {
	cap := exec(t.Rules.Block.HTML, src)
	if cap == nil {
		return nil
	}
	raw := TrimTrailingBlankLines(cap.whole())
	pre := cap.group(1) == "pre" || cap.group(1) == "script" || cap.group(1) == "style"
	return &Token{
		Type:  TypeHTML,
		Block: true,
		Raw:   raw,
		Pre:   pre,
		Text:  raw,
	}
}

// Def is marked's `def`.
func (t *Tokenizer) Def(src string) *Token {
	cap := exec(t.Rules.Block.Def, src)
	if cap == nil {
		return nil
	}
	tag := strings.ToLower(cap.group(1))
	tag = replaceAll(t.Rules.Other.MultipleSpaceGlobal, tag, func(string) string { return " " })
	href := ""
	if cap.group(2) != "" {
		href = replaceGroup(t.Rules.Other.HrefBrackets, cap.group(2), 1)
		href = replaceGroup(t.Rules.Inline.AnyPunctuation, href, 1)
	}
	title := cap.group(3)
	if title != "" {
		title = cutRange(title, 1, utf16Len(title)-1)
		title = replaceGroup(t.Rules.Inline.AnyPunctuation, title, 1)
	}
	return &Token{
		Type:  TypeDef,
		Tag:   tag,
		Raw:   Rtrim(cap.whole(), "\n", false),
		Href:  href,
		Title: &title,
	}
}

// Table is marked's `table`.
func (t *Tokenizer) Table(src string) *Token {
	cap := exec(t.Rules.Block.Table, src)
	if cap == nil {
		return nil
	}

	if !matches(t.Rules.Other.TableDelimiter, cap.group(2)) {
		// delimiter row must have a pipe (|) or colon (:) otherwise it is a
		// setext heading
		return nil
	}

	headers := SplitCells(cap.group(1), 0)
	aligns := strings.Split(replaceAll(t.Rules.Other.TableAlignChars, cap.group(2), func(string) string { return "" }), "|")
	var rows []string
	if strings.TrimSpace(cap.group(3)) != "" {
		rows = strings.Split(replaceAll(t.Rules.Other.TableRowBlankLine, cap.group(3), func(string) string { return "" }), "\n")
	}

	item := &Token{
		Type:   TypeTable,
		Raw:    Rtrim(cap.whole(), "\n", false),
		Header: []TableCell{},
		Align:  []string{},
		Rows:   [][]TableCell{},
	}

	if len(headers) != len(aligns) {
		// header and align columns must be equal, rows can be different.
		return nil
	}

	for _, align := range aligns {
		switch {
		case matches(t.Rules.Other.TableAlignRight, align):
			item.Align = append(item.Align, "right")
		case matches(t.Rules.Other.TableAlignCenter, align):
			item.Align = append(item.Align, "center")
		case matches(t.Rules.Other.TableAlignLeft, align):
			item.Align = append(item.Align, "left")
		default:
			item.Align = append(item.Align, "")
		}
	}

	for i, header := range headers {
		item.Header = append(item.Header, TableCell{
			Text:   header,
			Header: true,
			Align:  item.Align[i],
		})
		headerIndex := t.Lexer.inline(header)
		item.Header[len(item.Header)-1].InlineIndex = headerIndex
		item.Header[len(item.Header)-1].HasInline = true
	}

	for _, row := range rows {
		cells := SplitCells(row, len(item.Header))
		rowCells := make([]TableCell, 0, len(cells))
		for i, cell := range cells {
			rowCells = append(rowCells, TableCell{
				Text:   cell,
				Header: false,
				Align:  item.Align[i],
			})
			rowCells[len(rowCells)-1].InlineIndex = t.Lexer.inline(cell)
			rowCells[len(rowCells)-1].HasInline = true
		}
		item.Rows = append(item.Rows, rowCells)
	}

	return item
}

// Lheading is marked's `lheading`.
func (t *Tokenizer) Lheading(src string) *Token {
	cap := exec(t.Rules.Block.LHeading, src)
	if cap == nil {
		return nil
	}
	text := strings.TrimSpace(cap.group(1))
	depth := 2
	if strings.HasPrefix(cap.group(2), "=") {
		depth = 1
	}
	return &Token{
		Type:        TypeHeading,
		Raw:         Rtrim(cap.whole(), "\n", false),
		Depth:       depth,
		Text:        text,
		HasInline:   true,
		InlineIndex: t.Lexer.inline(text),
	}
}

// Paragraph is marked's `paragraph`.
func (t *Tokenizer) Paragraph(src string) *Token {
	cap := exec(t.Rules.Block.Paragraph, src)
	if cap == nil {
		return nil
	}
	text := cap.group(1)
	if strings.HasSuffix(text, "\n") {
		text = cutPrefix(text, utf16Len(text)-1)
	}
	return &Token{
		Type:        TypeParagraph,
		Raw:         cap.whole(),
		Text:        text,
		HasInline:   true,
		InlineIndex: t.Lexer.inline(text),
	}
}

// Text is marked's `text`.
func (t *Tokenizer) Text(src string) *Token {
	cap := exec(t.Rules.Block.Text, src)
	if cap == nil {
		return nil
	}
	return &Token{
		Type:        TypeText,
		Raw:         cap.whole(),
		Text:        cap.whole(),
		HasInline:   true,
		InlineIndex: t.Lexer.inline(cap.whole()),
	}
}

// Escape is marked's `escape`.
func (t *Tokenizer) Escape(src string) *Token {
	cap := exec(t.Rules.Inline.Escape, src)
	if cap == nil {
		return nil
	}
	return &Token{Type: TypeEscape, Raw: cap.whole(), Text: cap.group(1)}
}

// Tag is marked's `tag`.
func (t *Tokenizer) Tag(src string) *Token {
	cap := exec(t.Rules.Inline.Tag, src)
	if cap == nil {
		return nil
	}
	if !t.Lexer.State.InLink && matches(t.Rules.Other.StartATag, cap.whole()) {
		t.Lexer.State.InLink = true
	} else if t.Lexer.State.InLink && matches(t.Rules.Other.EndATag, cap.whole()) {
		t.Lexer.State.InLink = false
	}
	if !t.Lexer.State.InRawBlock && matches(t.Rules.Other.StartPreScriptTag, cap.whole()) {
		t.Lexer.State.InRawBlock = true
	} else if t.Lexer.State.InRawBlock && matches(t.Rules.Other.EndPreScriptTag, cap.whole()) {
		t.Lexer.State.InRawBlock = false
	}

	return &Token{
		Type:       TypeHTML,
		Raw:        cap.whole(),
		InLink:     t.Lexer.State.InLink,
		InRawBlock: t.Lexer.State.InRawBlock,
		Block:      false,
		Text:       cap.whole(),
	}
}

// Link is marked's `link`.
func (t *Tokenizer) Link(src string) *Token {
	cap := exec(t.Rules.Inline.Link, src)
	if cap == nil {
		return nil
	}
	trimmedURL := strings.TrimSpace(cap.group(2))
	if !t.Options.PedanticEnabled() && matches(t.Rules.Other.StartAngleBracket, trimmedURL) {
		// commonmark requires matching angle brackets
		if !matches(t.Rules.Other.EndAngleBracket, trimmedURL) {
			return nil
		}

		// ending angle bracket cannot be escaped
		rtrimSlash := Rtrim(cutPrefix(trimmedURL, utf16Len(trimmedURL)-1), "\\", false)
		if (utf16Len(trimmedURL)-utf16Len(rtrimSlash))%2 == 0 {
			return nil
		}
	} else {
		// find closing parenthesis
		lastParenIndex := FindClosingBracket(cap.group(2), "()")
		if lastParenIndex == -2 {
			// more open parens than closed
			return nil
		}

		if lastParenIndex > -1 {
			start := 4
			if strings.HasPrefix(cap.whole(), "!") {
				start = 5
			}
			linkLen := start + utf16Len(cap.group(1)) + lastParenIndex
			cap.setGroup(2, cutPrefix(cap.group(2), lastParenIndex))
			cap.setGroup(0, jsTrim(cutPrefix(cap.whole(), linkLen)))
			cap.setGroup(3, "")
		}
	}
	href := cap.group(2)
	title := ""
	if t.Options.PedanticEnabled() {
		// split pedantic href and title
		link := exec(t.Rules.Other.PedanticHrefTitle, href)
		if link != nil {
			href = link.group(1)
			title = link.group(3)
		}
	} else if cap.group(3) != "" {
		title = cutRange(cap.group(3), 1, utf16Len(cap.group(3))-1)
	}

	href = strings.TrimSpace(href)
	if matches(t.Rules.Other.StartAngleBracket, href) {
		if t.Options.PedanticEnabled() && !matches(t.Rules.Other.EndAngleBracket, trimmedURL) {
			// pedantic allows starting angle bracket without ending angle bracket
			href = substring(href, 1)
		} else {
			href = cutRange(href, 1, utf16Len(href)-1)
		}
	}
	if href != "" {
		href = replaceGroup(t.Rules.Inline.AnyPunctuation, href, 1)
	}
	if title != "" {
		title = replaceGroup(t.Rules.Inline.AnyPunctuation, title, 1)
	}
	return outputLink(&match{groups: []string{cap.group(0), cap.group(1), href, title}}, Link{Href: href, Title: title}, cap.group(0), t.Lexer, t.Rules)
}

// Reflink is marked's `reflink`.
func (t *Tokenizer) Reflink(src string, links Links) *Token {
	cap := exec(t.Rules.Inline.Reflink, src)
	if cap == nil {
		cap = exec(t.Rules.Inline.Nolink, src)
	}
	if cap == nil {
		return nil
	}
	linkString := cap.group(2)
	if linkString == "" {
		linkString = cap.group(1)
	}
	linkString = replaceAll(t.Rules.Other.MultipleSpaceGlobal, linkString, func(string) string { return " " })
	link, ok := links[strings.ToLower(linkString)]
	if !ok {
		text := firstUnit(cap.whole())
		return &Token{Type: TypeText, Raw: text, Text: text}
	}
	return outputLink(cap, link, cap.whole(), t.Lexer, t.Rules)
}

// EmStrong is marked's `emStrong`. An extension may replace the method, in
// which case the built in method runs only when the extension reports that
// it did not handle the source.
func (t *Tokenizer) EmStrong(src string, maskedSrc string, prevChar string) *Token {
	if t.Overrides != nil && t.Overrides.EmStrong != nil {
		if token, handled := t.Overrides.EmStrong(t, src, maskedSrc, prevChar); handled {
			return token
		}
	}
	return t.emStrong(src, maskedSrc, prevChar)
}

// emStrong is marked's `emStrong`.
func (t *Tokenizer) emStrong(src string, maskedSrc string, prevChar string) *Token {
	m := exec(t.Rules.Inline.EmStrongLDelim, src)
	if m == nil {
		return nil
	}
	if m.group(1) == "" && m.group(2) == "" && m.group(3) == "" && m.group(4) == "" {
		return nil
	}

	// _ can't be between two alphanumerics. \p{L}\p{N} includes non-english
	// alphabet/numbers as well
	if m.group(4) != "" && matches(t.Rules.Other.UnicodeAlphaNumeric, prevChar) {
		return nil
	}

	nextChar := m.group(1)
	if nextChar == "" {
		nextChar = m.group(3)
	}

	if nextChar == "" || prevChar == "" || matches(t.Rules.Inline.Punctuation, prevChar) {
		// unicode Regex counts emoji as 1 char; spread into array for proper
		// count (used multiple times below)
		lLength := runeLen(m.whole()) - 1
		var rDelim string
		rLength := 0
		delimTotal := lLength
		midDelimTotal := 0

		delimChar := firstUnit(m.whole())
		// A mid-run opener (for example the second star of an unmatched `**`)
		// must only pair with a delimiter that can only close, otherwise it
		// steals the opener of a later span (`**a*b*c` must be
		// `**a<em>b</em>c`).
		midRun := prevChar == delimChar
		endReg := t.Rules.Inline.EmStrongRDelimAst
		if delimChar != "*" {
			endReg = t.Rules.Inline.EmStrongRDelimUnd
		}

		// Clip maskedSrc to same section of string as src (move to lexer?)
		maskedSrc = cutSuffix(maskedSrc, utf16Len(src)-lLength)

		for _, m := range execAll(endReg, maskedSrc) {
			rDelim = m.group(1)
			for i := 2; i <= 6 && rDelim == ""; i++ {
				rDelim = m.group(i)
			}

			if rDelim == "" {
				continue // skip single * in __abc*abc__
			}

			rLength = runeLen(rDelim)

			if m.group(3) != "" || m.group(4) != "" { // found another Left Delim
				delimTotal += rLength
				continue
			} else if m.group(5) != "" || m.group(6) != "" { // either Left or Right Delim
				if lLength%3 != 0 && (lLength+rLength)%3 == 0 {
					midDelimTotal += rLength
					continue // CommonMark Emphasis Rules 9-10
				}
				if midRun {
					// A mid-run opener cannot close against an ambiguous
					// delimiter that can also open; that delimiter opens its own
					// emphasis span instead.
					break
				}
			}

			delimTotal -= rLength

			if delimTotal > 0 {
				continue // Haven't found enough closing delimiters
			}

			// Remove extra characters. *a*** -> *a*
			rLength = min(rLength, rLength+delimTotal+midDelimTotal)
			// char length can be >1 for unicode characters;
			lastCharLength := firstCodePointLen(m.whole())
			raw := cutPrefix(src, lLength+m.index+lastCharLength+rLength)

			// Create `em` if smallest delimiter has odd char count. *a***
			if min(lLength, rLength)%2 == 1 {
				text := cutRange(raw, 1, utf16Len(raw)-1)
				return &Token{
					Type:   TypeEm,
					Raw:    raw,
					Text:   text,
					Tokens: t.Lexer.inlineTokens(text, nil),
				}
			}

			// Create 'strong' if smallest delimiter has even char count. **a***
			text := cutRange(raw, 2, utf16Len(raw)-2)
			return &Token{
				Type:   TypeStrong,
				Raw:    raw,
				Text:   text,
				Tokens: t.Lexer.inlineTokens(text, nil),
			}
		}
	}
	return nil
}

// Codespan is marked's `codespan`.
func (t *Tokenizer) Codespan(src string) *Token {
	cap := exec(t.Rules.Inline.Code, src)
	if cap == nil {
		return nil
	}
	text := replaceAll(t.Rules.Other.NewLineCharGlobal, cap.group(2), func(string) string { return " " })
	hasNonSpaceChars := matches(t.Rules.Other.NonSpaceChar, text)
	hasSpaceCharsOnBothEnds := matches(t.Rules.Other.StartingSpaceChar, text) && matches(t.Rules.Other.EndingSpaceChar, text)
	if hasNonSpaceChars && hasSpaceCharsOnBothEnds {
		text = cutRange(text, 1, utf16Len(text)-1)
	}
	return &Token{
		Type: TypeCodespan,
		Raw:  cap.whole(),
		Text: text,
	}
}

// Br is marked's `br`.
func (t *Tokenizer) Br(src string) *Token {
	cap := exec(t.Rules.Inline.Br, src)
	if cap == nil {
		return nil
	}
	return &Token{Type: TypeBr, Raw: cap.whole()}
}

// Del is marked's `del`. An extension may replace the method, in which case
// the built in method runs only when the extension reports that it did not
// handle the source.
func (t *Tokenizer) Del(src string, maskedSrc string, prevChar string) *Token {
	if t.Overrides != nil && t.Overrides.Del != nil {
		if token, handled := t.Overrides.Del(t, src, maskedSrc, prevChar); handled {
			return token
		}
	}
	return t.del(src, maskedSrc, prevChar)
}

// del is marked's `del`.
func (t *Tokenizer) del(src string, maskedSrc string, prevChar string) *Token {
	m := exec(t.Rules.Inline.DelLDelim, src)
	if m == nil {
		return nil
	}

	nextChar := m.group(1)

	if nextChar == "" || prevChar == "" || matches(t.Rules.Inline.Punctuation, prevChar) {
		// unicode Regex counts emoji as 1 char; spread into array for proper
		// count
		lLength := runeLen(m.whole()) - 1
		var rDelim string
		rLength := 0
		delimTotal := lLength

		endReg := t.Rules.Inline.DelRDelim

		// Clip maskedSrc to same section of string as src
		maskedSrc = cutSuffix(maskedSrc, utf16Len(src)-lLength)

		for _, m := range execAll(endReg, maskedSrc) {
			rDelim = m.group(1)
			for i := 2; i <= 6 && rDelim == ""; i++ {
				rDelim = m.group(i)
			}

			if rDelim == "" {
				continue
			}

			rLength = runeLen(rDelim)

			if rLength != lLength {
				continue
			}

			if m.group(3) != "" || m.group(4) != "" { // found another Left Delim
				delimTotal += rLength
				continue
			}

			delimTotal -= rLength

			if delimTotal > 0 {
				continue // Haven't found enough closing delimiters
			}

			// Remove extra characters
			rLength = min(rLength, rLength+delimTotal)
			// char length can be >1 for unicode characters
			lastCharLength := firstCodePointLen(m.whole())
			raw := cutPrefix(src, lLength+m.index+lastCharLength+rLength)

			// Create del token - only single ~ or double ~~ supported
			text := cutRange(raw, lLength, utf16Len(raw)-lLength)
			return &Token{
				Type:   TypeDel,
				Raw:    raw,
				Text:   text,
				Tokens: t.Lexer.inlineTokens(text, nil),
			}
		}
	}
	return nil
}

// Autolink is marked's `autolink`.
func (t *Tokenizer) Autolink(src string) *Token {
	cap := exec(t.Rules.Inline.Autolink, src)
	if cap == nil {
		return nil
	}
	text := cap.group(1)
	href := text
	if cap.group(2) == "@" {
		href = "mailto:" + text
	}

	return &Token{
		Type: TypeLink,
		Raw:  cap.whole(),
		Text: text,
		Href: href,
		Tokens: []Token{{
			Type: TypeText,
			Raw:  text,
			Text: text,
		}},
	}
}

// Url is marked's `url`.
func (t *Tokenizer) Url(src string) *Token {
	cap := exec(t.Rules.Inline.URL, src)
	if cap == nil {
		return nil
	}
	text := cap.whole()
	href := text
	raw := cap.whole()
	if cap.group(2) == "@" {
		href = "mailto:" + text
	} else {
		// do extended autolink path validation
		for {
			prevCapZero := raw
			backpedal := exec(t.Rules.Inline.Backpedal, raw)
			if backpedal == nil {
				raw = ""
			} else {
				raw = backpedal.whole()
			}
			if prevCapZero == raw {
				break
			}
		}
		text = raw
		if cap.group(1) == "www." {
			href = "http://" + raw
		} else {
			href = raw
		}
	}

	return &Token{
		Type: TypeLink,
		Raw:  raw,
		Text: text,
		Href: href,
		Tokens: []Token{{
			Type: TypeText,
			Raw:  text,
			Text: text,
		}},
	}
}

// InlineText is marked's `inlineText`.
func (t *Tokenizer) InlineText(src string) *Token {
	cap := exec(t.Rules.Inline.Text, src)
	if cap == nil {
		return nil
	}
	escaped := t.Lexer.State.InRawBlock
	return &Token{
		Type:    TypeText,
		Raw:     cap.whole(),
		Text:    cap.whole(),
		Escaped: escaped,
	}
}

// lastTokenOf returns the last token of a list, like marked's `tokens.at(-1)`.
func lastTokenOf(tokens []Token) *Token {
	if len(tokens) == 0 {
		return nil
	}
	return &tokens[len(tokens)-1]
}

// firstLine returns the first line of a string, like JavaScript's
// `str.split('\n', 1)[0]`.
func firstLine(src string) string {
	if index := strings.IndexByte(src, '\n'); index >= 0 {
		return src[:index]
	}
	return src
}

// firstUnit returns the first UTF-16 code unit of a string, like JavaScript's
// `str.charAt(0)`.
func firstUnit(src string) string {
	if src == "" {
		return ""
	}
	r, size := utf8.DecodeRuneInString(src)
	if r > 0xFFFF {
		// the first code unit of an astral character is a lone surrogate
		return string(rune(0xD800 + ((r - 0x10000) >> 10)))
	}
	return src[:size]
}

// firstRune returns the first rune of a string.
func firstRune(src string) string {
	if src == "" {
		return ""
	}
	r, _ := utf8.DecodeRuneInString(src)
	return string(r)
}

// firstCodePointLen returns the length of the first code point of a string in
// UTF-16 code units, like JavaScript's `[...str][0].length`.
func firstCodePointLen(src string) int {
	if src == "" {
		return 0
	}
	r, _ := utf8.DecodeRuneInString(src)
	if r > 0xFFFF {
		return 2
	}
	return 1
}

// runeLen returns the number of code points in a string, like JavaScript's
// `[...str].length`.
func runeLen(src string) int {
	return utf8.RuneCountInString(src)
}

// indexOfNonSpace returns the index of the first non-space character, like
// JavaScript's `str.search(/[^ ]/)`.
func indexOfNonSpace(src string) int {
	for i, r := range src {
		if r != ' ' {
			return i
		}
	}
	return -1
}

// parseLeadingNumber parses the number of an ordered list marker.
func parseLeadingNumber(src string) (int, bool) {
	if src == "" {
		return 0, false
	}
	value := 0
	for _, c := range src {
		if c < '0' || c > '9' {
			return 0, false
		}
		value = value*10 + int(c-'0')
	}
	return value, true
}
