package marked

import (
	"fmt"
	"strings"
)

// Lexer is marked's `_Lexer`, ported from marked/src/Lexer.ts.
type Lexer struct {
	// Tokens holds the tokens of the document.
	Tokens []Token
	// Links holds the link reference definitions of the document.
	Links Links
	// Options holds the options of the lexer.
	Options *MarkedOptions
	// State holds the state of the lexer while it runs.
	State LexerState
	// InlineQueue holds the inline sources that still have to be lexed.
	InlineQueue []InlineQueueItem

	tokenizer *Tokenizer
}

// LexerState is the state that marked's lexer keeps while it runs.
type LexerState struct {
	// InLink reports whether the lexer is inside a link.
	InLink bool
	// InRawBlock reports whether the lexer is inside a raw block.
	InRawBlock bool
	// LinkEmitted reports whether a link was produced in the inline run that
	// is currently being scanned.
	LinkEmitted bool
	// Top reports whether the lexer is at the top level.
	Top bool
}

// InlineQueueItem is one entry of the lexer's inline queue.
type InlineQueueItem struct {
	src string
}

// NewLexer creates a lexer, like marked's `_Lexer` constructor.
func NewLexer(options *MarkedOptions) *Lexer {
	if options == nil {
		options = DefaultOptions()
	}
	lexer := &Lexer{
		Tokens:      []Token{},
		Links:       Links{},
		Options:     options,
		InlineQueue: []InlineQueueItem{},
	}
	lexer.State = LexerState{Top: true}

	tokenizer := options.Tokenizer
	if tokenizer == nil {
		tokenizer = newTokenizer(options)
	}
	options.Tokenizer = tokenizer
	tokenizer.Options = options
	tokenizer.Lexer = lexer

	rules := Rules{Other: other, Block: block["normal"], Inline: inline["normal"]}
	switch {
	case options.PedanticEnabled():
		rules.Block = block["pedantic"]
		rules.Inline = inline["pedantic"]
	case options.GFMEnabled():
		rules.Block = block["gfm"]
		if options.BreaksEnabled() {
			rules.Inline = inline["breaks"]
		} else {
			rules.Inline = inline["gfm"]
		}
	}
	tokenizer.Rules = rules
	lexer.tokenizer = tokenizer

	return lexer
}

// Lex parses a markdown document into tokens, like marked's `Lexer.lex`.
func (l *Lexer) Lex(src string) []Token {
	src = replaceAll(l.tokenizer.Rules.Other.CarriageReturn, src, func(string) string { return "\n" })

	l.Tokens = l.blockTokens(src, l.Tokens, false)

	// marked lexes the inline runs after the block tokens, so that the link
	// definitions of the document are known.
	results := make([][]Token, len(l.InlineQueue))
	for i := range l.InlineQueue {
		results[i] = l.inlineTokens(l.InlineQueue[i].src, nil)
	}
	l.InlineQueue = nil

	l.attachInline(l.Tokens, results)

	return l.Tokens
}

// attachInline gives every token the inline tokens of its inline run.
func (l *Lexer) attachInline(tokens []Token, results [][]Token) {
	for i := range tokens {
		if tokens[i].HasInline && tokens[i].InlineIndex < len(results) {
			tokens[i].Tokens = results[tokens[i].InlineIndex]
			if len(tokens[i].InlinePrepend) > 0 {
				prepend := make([]Token, 0, len(tokens[i].InlinePrepend)+len(tokens[i].Tokens))
				prepend = append(prepend, tokens[i].InlinePrepend...)
				tokens[i].Tokens = append(prepend, tokens[i].Tokens...)
			}
		}
		l.attachInline(tokens[i].Tokens, results)
		for j := range tokens[i].Items {
			l.attachInline(tokens[i].Items[j].Tokens, results)
		}
		for j := range tokens[i].Header {
			l.attachInlineCell(&tokens[i].Header[j], results)
		}
		for j := range tokens[i].Rows {
			for k := range tokens[i].Rows[j] {
				l.attachInlineCell(&tokens[i].Rows[j][k], results)
			}
		}
	}
}

// attachInlineCell gives a table cell the inline tokens of its inline run.
func (l *Lexer) attachInlineCell(cell *TableCell, results [][]Token) {
	if cell.HasInline && cell.InlineIndex < len(results) {
		cell.Tokens = results[cell.InlineIndex]
	}
	l.attachInline(cell.Tokens, results)
}

// blockTokens lexes a block of markdown, like marked's `blockTokens`.
func (l *Lexer) blockTokens(src string, tokens []Token, lastParagraphClipped bool) []Token {
	l.tokenizer.Lexer = l
	if l.Options.PedanticEnabled() {
		src = replaceAll(l.tokenizer.Rules.Other.TabCharGlobal, src, func(string) string { return "    " })
		src = replaceAll(l.tokenizer.Rules.Other.SpaceLine, src, func(string) string { return "" })
	}

	srcLength := -1
	for src != "" {
		if srcLength != -1 && utf16Len(src) >= srcLength {
			l.infiniteLoopError(src)
			break
		}
		srcLength = utf16Len(src)

		var token *Token

		// extensions
		if l.Options.Extensions != nil {
			matched := false
			for _, extension := range l.Options.Extensions.Block {
				token = extension(l, src, tokens)
				if token == nil {
					continue
				}
				src = substring(src, utf16Len(token.Raw))
				tokens = append(tokens, *token)
				matched = true
				break
			}
			if matched {
				continue
			}
		}

		// newline
		if token = l.tokenizer.Space(src); token != nil {
			src = substring(src, utf16Len(token.Raw))
			lastToken := lastTokenOf(tokens)
			if utf16Len(token.Raw) == 1 && lastToken != nil {
				// if there's a single \n as a spacer, it's terminating the last
				// line, so move it there so that we don't get unnecessary
				// paragraph tags
				lastToken.Raw += "\n"
			} else {
				tokens = append(tokens, *token)
			}
			continue
		}

		// code
		if token = l.tokenizer.Code(src); token != nil {
			src = substring(src, utf16Len(token.Raw))
			lastToken := lastTokenOf(tokens)
			// An indented code block cannot interrupt a paragraph.
			if lastToken != nil && (lastToken.Type == TypeParagraph || lastToken.Type == TypeText) {
				if !strings.HasSuffix(lastToken.Raw, "\n") {
					lastToken.Raw += "\n"
				}
				lastToken.Raw += token.Raw
				lastToken.Text += "\n" + token.Text
				l.setInlineQueueLastSrc(lastToken.Text)
			} else {
				tokens = append(tokens, *token)
			}
			continue
		}

		// fences
		if token = l.tokenizer.Fences(src); token != nil {
			src = substring(src, utf16Len(token.Raw))
			tokens = append(tokens, *token)
			continue
		}

		// heading
		if token = l.tokenizer.Heading(src); token != nil {
			src = substring(src, utf16Len(token.Raw))
			tokens = append(tokens, *token)
			continue
		}

		// hr
		if token = l.tokenizer.Hr(src); token != nil {
			src = substring(src, utf16Len(token.Raw))
			tokens = append(tokens, *token)
			continue
		}

		// blockquote
		if token = l.tokenizer.Blockquote(src); token != nil {
			src = substring(src, utf16Len(token.Raw))
			tokens = append(tokens, *token)
			continue
		}

		// list
		if token = l.tokenizer.List(src); token != nil {
			src = substring(src, utf16Len(token.Raw))
			tokens = append(tokens, *token)
			continue
		}

		// html
		if token = l.tokenizer.Html(src); token != nil {
			src = substring(src, utf16Len(token.Raw))
			tokens = append(tokens, *token)
			continue
		}

		// def
		if token = l.tokenizer.Def(src); token != nil {
			src = substring(src, utf16Len(token.Raw))
			lastToken := lastTokenOf(tokens)
			if lastToken != nil && (lastToken.Type == TypeParagraph || lastToken.Type == TypeText) {
				if !strings.HasSuffix(lastToken.Raw, "\n") {
					lastToken.Raw += "\n"
				}
				lastToken.Raw += token.Raw
				lastToken.Text += "\n" + token.Raw
				l.setInlineQueueLastSrc(lastToken.Text)
			} else if _, ok := l.Links[token.Tag]; !ok {
				l.Links[token.Tag] = Link{Href: token.Href, Title: derefString(token.Title)}
				tokens = append(tokens, *token)
			}
			continue
		}

		// table (gfm)
		if token = l.tokenizer.Table(src); token != nil {
			src = substring(src, utf16Len(token.Raw))
			tokens = append(tokens, *token)
			continue
		}

		// lheading
		if token = l.tokenizer.Lheading(src); token != nil {
			src = substring(src, utf16Len(token.Raw))
			tokens = append(tokens, *token)
			continue
		}

		// top-level paragraph
		// prevent paragraph consuming extensions by clipping 'src' to extension
		// start
		cutSrc := src
		if l.Options.Extensions != nil && len(l.Options.Extensions.StartBlock) > 0 {
			startIndex := -1
			tempSrc := substring(src, 1)
			for _, start := range l.Options.Extensions.StartBlock {
				tempStart := start(l, tempSrc)
				if tempStart >= 0 && (startIndex == -1 || tempStart < startIndex) {
					startIndex = tempStart
				}
			}
			if startIndex >= 0 {
				cutSrc = substring(src, startIndex+1)
			}
		}
		if l.State.Top {
			if token = l.tokenizer.Paragraph(cutSrc); token != nil {
				lastToken := lastTokenOf(tokens)
				if lastParagraphClipped && lastToken != nil && lastToken.Type == TypeParagraph {
					if !strings.HasSuffix(lastToken.Raw, "\n") {
						lastToken.Raw += "\n"
					}
					lastToken.Raw += token.Raw
					lastToken.Text += "\n" + token.Text
					l.popInlineQueue()
					l.setInlineQueueLastSrc(lastToken.Text)
				} else {
					tokens = append(tokens, *token)
				}
				lastParagraphClipped = utf16Len(cutSrc) != utf16Len(src)
				src = substring(src, utf16Len(token.Raw))
				continue
			}
		}

		// text
		if token = l.tokenizer.Text(src); token != nil {
			src = substring(src, utf16Len(token.Raw))
			lastToken := lastTokenOf(tokens)
			if lastToken != nil && lastToken.Type == TypeText {
				if !strings.HasSuffix(lastToken.Raw, "\n") {
					lastToken.Raw += "\n"
				}
				lastToken.Raw += token.Raw
				lastToken.Text += "\n" + token.Text
				l.popInlineQueue()
				l.setInlineQueueLastSrc(lastToken.Text)
			} else {
				tokens = append(tokens, *token)
			}
			continue
		}

		if src != "" {
			l.infiniteLoopError(src)
			break
		}
	}

	l.State.Top = true
	return tokens
}

// inline queues an inline source, like marked's `inline`. It returns the
// index of the run in the inline queue.
func (l *Lexer) inline(src string) int {
	l.InlineQueue = append(l.InlineQueue, InlineQueueItem{src: src})
	return len(l.InlineQueue) - 1
}

// setInlineQueueLastSrc sets the source of the last inline queue entry.
func (l *Lexer) setInlineQueueLastSrc(src string) {
	if len(l.InlineQueue) == 0 {
		return
	}
	l.InlineQueue[len(l.InlineQueue)-1].src = src
}

// popInlineQueue removes the last inline queue entry.
func (l *Lexer) popInlineQueue() {
	if len(l.InlineQueue) == 0 {
		return
	}
	l.InlineQueue = l.InlineQueue[:len(l.InlineQueue)-1]
}

// linkInText reports whether the text holds a link already. An image does not
// count: an image may hold a link, a link may not.
func (l *Lexer) linkInText(text string) bool {
	if !strings.Contains(text, "[") {
		return false
	}

	linkRule := l.tokenizer.Rules.Inline.Link
	for _, found := range execAll(l.tokenizer.Rules.Inline.BlockSkip, text) {
		// blockSkip also matches code spans and html, and the `!` of an image
		// is left out of the match, so read the character before it.
		if matches(linkRule, found.whole()) && charAt(text, found.index-1) != "!" {
			return true
		}
	}

	for _, found := range execAll(l.tokenizer.Rules.Inline.ReflinkSearch, text) {
		match0 := found.whole()
		refStart := lastIndexOfUnit(match0, "[")
		if charAt(match0, 0) == "!" {
			continue
		}
		if _, ok := l.Links[jsSlice(match0, refStart+1, -1)]; !ok {
			continue
		}
		// a candidate holding a link is not a link either, so it does not count
		if refStart > 1 && l.linkInText(jsSlice(match0, 1, refStart-1)) {
			continue
		}
		return true
	}

	return false
}

// inlineTokens lexes inline markdown, like marked's `inlineTokens`.
func (l *Lexer) inlineTokens(src string, tokens []Token) []Token {
	l.tokenizer.Lexer = l
	// String with links masked to avoid interference with em and strong
	maskedSrc := src

	// Mask out reflinks
	if len(l.Links) > 0 && strings.Contains(src, "[") {
		reflinkSearch := l.tokenizer.Rules.Inline.ReflinkSearch
		// maskReflink is marked's `maskReflink`. It masks the links a
		// candidate holds by calling itself, so the port keeps it recursive.
		var maskReflink func(match0 string) string
		maskReflink = func(match0 string) string {
			refStart := lastIndexOfUnit(match0, "[")
			if _, ok := l.Links[jsSlice(match0, refStart+1, -1)]; !ok {
				return match0
			}
			// CommonMark: "Links may not contain other links, at any level of
			// nesting." A candidate whose text already holds one never becomes
			// a link, so flattening the whole span would hide the emphasis
			// that does still apply inside it. Mask the links it holds
			// instead. Images are exempt: their text is flattened into an alt
			// attribute.
			if refStart > 1 && charAt(match0, 0) != "!" {
				text := jsSlice(match0, 1, refStart-1)
				if l.linkInText(text) {
					masked := replaceAllWithIndex(reflinkSearch, text, func(match0 string, _ int, _ string) string {
						return maskReflink(match0)
					})
					return "[" + masked + "][" + strings.Repeat("a", utf16Len(match0)-refStart-2) + "]"
				}
			}
			return "[" + strings.Repeat("a", utf16Len(match0)-2) + "]"
		}
		maskedSrc = replaceAllWithIndex(reflinkSearch, maskedSrc, func(match0 string, _ int, _ string) string {
			return maskReflink(match0)
		})
	}

	// Mask out escaped characters.
	// Every mask must keep the length it replaces: emStrong and del line
	// maskedSrc up with src by slicing from the end. `anyPunctuation` matches
	// unicode punctuation, so an escaped astral character is 3 code units.
	maskedSrc = replaceAll(l.tokenizer.Rules.Inline.AnyPunctuation, maskedSrc, func(match0 string) string {
		return strings.Repeat("+", utf16Len(match0))
	})

	// Mask out other blocks
	// marked masks a match with a run of "a" of the same length, so the
	// masked source keeps the length of the source it replaces. The second
	// group of the expression holds the characters in front of a code span.
	maskedSrc = replaceAllMatches(l.tokenizer.Rules.Inline.BlockSkip, maskedSrc, func(found *match) string {
		match0 := found.whole()
		context := found.group(2)
		offset := 0
		if context != "" {
			offset = utf16Len(context)
		}
		return cutPrefix(match0, offset) + "[" + strings.Repeat("a", utf16Len(match0)-offset-2) + "]"
	})

	// Mask out blocks from extensions
	if l.Options.Hooks != nil && l.Options.Hooks.EmStrongMask != nil {
		maskedSrc = l.Options.Hooks.EmStrongMask(maskedSrc)
	}

	keepPrevChar := false
	prevChar := ""
	srcLength := -1
	for src != "" {
		if srcLength != -1 && utf16Len(src) >= srcLength {
			l.infiniteLoopError(src)
			break
		}
		srcLength = utf16Len(src)

		if !keepPrevChar {
			prevChar = ""
		}
		keepPrevChar = false

		var token *Token

		// extensions
		if l.Options.Extensions != nil {
			matched := false
			for _, extension := range l.Options.Extensions.Inline {
				token = extension(l, src, tokens)
				if token == nil {
					continue
				}
				src = substring(src, utf16Len(token.Raw))
				tokens = append(tokens, *token)
				matched = true
				break
			}
			if matched {
				continue
			}
		}

		// escape
		if token = l.tokenizer.Escape(src); token != nil {
			src = substring(src, utf16Len(token.Raw))
			tokens = append(tokens, *token)
			continue
		}

		// tag
		if token = l.tokenizer.Tag(src); token != nil {
			src = substring(src, utf16Len(token.Raw))
			tokens = append(tokens, *token)
			continue
		}

		// link
		if token = l.tokenizer.Link(src); token != nil {
			src = substring(src, utf16Len(token.Raw))
			tokens = append(tokens, *token)
			continue
		}

		// reflink, nolink
		if token = l.tokenizer.Reflink(src, l.Links); token != nil {
			src = substring(src, utf16Len(token.Raw))
			lastToken := lastTokenOf(tokens)
			if token.Type == TypeText && lastToken != nil && lastToken.Type == TypeText {
				lastToken.Raw += token.Raw
				lastToken.Text += token.Text
			} else {
				tokens = append(tokens, *token)
			}
			continue
		}

		// em & strong
		if token = l.tokenizer.EmStrong(src, maskedSrc, prevChar); token != nil {
			src = substring(src, utf16Len(token.Raw))
			tokens = append(tokens, *token)
			continue
		}

		// code
		if token = l.tokenizer.Codespan(src); token != nil {
			src = substring(src, utf16Len(token.Raw))
			tokens = append(tokens, *token)
			continue
		}

		// br
		if token = l.tokenizer.Br(src); token != nil {
			src = substring(src, utf16Len(token.Raw))
			tokens = append(tokens, *token)
			continue
		}

		// del (gfm)
		if token = l.tokenizer.Del(src, maskedSrc, prevChar); token != nil {
			src = substring(src, utf16Len(token.Raw))
			tokens = append(tokens, *token)
			continue
		}

		// autolink
		if token = l.tokenizer.Autolink(src); token != nil {
			src = substring(src, utf16Len(token.Raw))
			tokens = append(tokens, *token)
			continue
		}

		// url (gfm)
		if !l.State.InLink {
			if token = l.tokenizer.Url(src); token != nil {
				src = substring(src, utf16Len(token.Raw))
				tokens = append(tokens, *token)
				continue
			}
		}

		// text
		// prevent inlineText consuming extensions by clipping 'src' to
		// extension start
		cutSrc := src
		if l.Options.Extensions != nil && len(l.Options.Extensions.StartInline) > 0 {
			startIndex := -1
			tempSrc := substring(src, 1)
			for _, start := range l.Options.Extensions.StartInline {
				tempStart := start(l, tempSrc)
				if tempStart >= 0 && (startIndex == -1 || tempStart < startIndex) {
					startIndex = tempStart
				}
			}
			if startIndex >= 0 {
				cutSrc = substring(src, startIndex+1)
			}
		}
		if token = l.tokenizer.InlineText(cutSrc); token != nil {
			src = substring(src, utf16Len(token.Raw))
			if !strings.HasSuffix(token.Raw, "_") { // Track prevChar before string of ____ started
				prevChar = lastUnit(token.Raw)
			}
			keepPrevChar = true
			lastToken := lastTokenOf(tokens)
			if lastToken != nil && lastToken.Type == TypeText {
				lastToken.Raw += token.Raw
				lastToken.Text += token.Text
			} else {
				tokens = append(tokens, *token)
			}
			continue
		}

		if src != "" {
			l.infiniteLoopError(src)
			break
		}
	}

	return tokens
}

// infiniteLoopError reports a lexer that does not consume its input.
func (l *Lexer) infiniteLoopError(src string) {
	if l.Options.SilentEnabled() {
		return
	}
	panic(fmt.Sprintf("marked: infinite loop on byte: %d", src[0]))
}

// charAt returns the UTF-16 code unit at an offset, like JavaScript's
// `str.charAt(index)`.
func charAt(src string, index int) string {
	if index < 0 || index >= utf16Len(src) {
		return ""
	}
	units := 0
	for _, r := range src {
		if units == index {
			if r > 0xFFFF {
				return string(rune(0xD800 + ((r - 0x10000) >> 10)))
			}
			return string(r)
		}
		units++
		if r > 0xFFFF {
			units++
		}
	}
	return ""
}

// lastUnit returns the last UTF-16 code unit of a string, like JavaScript's
// `str.slice(-1)`.
func lastUnit(src string) string {
	if src == "" {
		return ""
	}
	units := utf16Len(src)
	return charAt(src, units-1)
}

// derefString returns the text of an optional string.
func derefString(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
