package marked

import (
	"strings"
)

// Pi's markdown extensions, ported from
// packages/tui/src/components/markdown.ts of the pi repository.
//
// pi runs marked with three additions of its own on top of marked itself:
// a strict strikethrough tokenizer, latex tokenizers for the block and the
// inline level, and a fix-up that trims partial closing fences of streamed
// code blocks. The file keeps the order of the source file.

// The token types of pi's latex extensions.
const (
	TypeLatex      = "latex"
	TypeLatexBlock = "latexBlock"
)

// strictStrikethroughRegex is pi's `STRICT_STRIKETHROUGH_REGEX`.
var strictStrikethroughRegex = mustRegex(`^(~~)(?=[^\s~])((?:\\.|[^\\])*?(?:\\.|[^\s~\\]))\1(?=[^~]|$)`, flagNone)

// StrictStrikethrough returns pi's `StrictStrikethroughTokenizer`. It makes a
// strikethrough span close only at an exact `~~` delimiter, so `~~~x~~~` does
// not parse as a strikethrough span.
func StrictStrikethrough() *TokenizerOverrides {
	return &TokenizerOverrides{
		Del: strictStrikethroughDel,
	}
}

// strictStrikethroughDel is pi's `del` override.
func strictStrikethroughDel(t *Tokenizer, src string, maskedSrc string, prevChar string) (*Token, bool) {
	match := exec(strictStrikethroughRegex, src)
	if match == nil {
		return nil, true
	}

	text := match.group(2)
	return &Token{
		Type:   TypeDel,
		Raw:    match.whole(),
		Text:   text,
		Tokens: t.Lexer.inlineTokens(text, nil),
	}, true
}

// isEscaped reports whether the offset in the source is escaped, like pi's
// `isEscaped`.
func piIsEscaped(source string, index int) bool {
	backslashes := 0
	for position := index - 1; position >= 0 && charAt(source, position) == "\\"; position-- {
		backslashes++
	}
	return backslashes%2 == 1
}

// findClosingDelimiter finds the closing delimiter of a latex span, like pi's
// `findClosingDelimiter`. Escaped delimiters do not count.
func findClosingDelimiter(source string, closing string, start int) int {
	index := indexOfUnitFrom(source, closing, start)
	for index >= 0 && piIsEscaped(source, index) {
		index = indexOfUnitFrom(source, closing, index+len(closing))
	}
	return index
}

// indexOfUnitFrom returns the UTF-16 offset of a substring at or after an
// offset, like JavaScript's `str.indexOf(needle, fromIndex)` in code units.
func indexOfUnitFrom(src string, needle string, from int) int {
	if from < 0 {
		from = 0
	}
	total := utf16Len(src)
	if from >= total {
		return -1
	}
	rest := substring(src, from)
	index := indexOfUnit(rest, needle)
	if index < 0 {
		return -1
	}
	return from + index
}

// looksLikePendingDollarMath reports whether a pending dollar span looks like
// math, like pi's `looksLikePendingDollarMath`.
var looksLikePendingDollarMathRegex = mustRegex(`\\[A-Za-z]+|[_^=+*/<>()[\]|±≤≥≠≈∈→⇒∞∫∑√-]`, flagNone)

func looksLikePendingDollarMath(source string) bool {
	return matches(looksLikePendingDollarMathRegex, source)
}

// pendingDollarOpening matches a dollar opening that is followed by
// whitespace, like pi's `/^\$\s/`.
var pendingDollarOpening = mustRegex(`^\$\s`, flagNone)

// dollarNoSpaceBeforeClosing matches a closing dollar that follows
// whitespace, like pi's `/\s$/`.
var trailingSpace = mustRegex(`\s$`, flagNone)

// digitAfterClosing matches a digit right after the closing delimiter, like
// pi's `/^\d/`.
var digitStart = mustRegex(`^\d`, flagNone)

// constantCaseText matches a constant-case span, like pi's
// `/^[A-Z_][A-Z0-9_]*(?:[^A-Za-z0-9_\s])?$/`.
var constantCaseText = mustRegex(`^[A-Z_][A-Z0-9_]*(?:[^A-Za-z0-9_\s])?$`, flagNone)

// identifierStart matches the start of an identifier, like pi's
// `/^[A-Za-z_][A-Za-z0-9_]*/`.
var identifierStart = mustRegex(`^[A-Za-z_][A-Za-z0-9_]*`, flagNone)

// tokenizeInlineLatex is pi's `tokenizeInlineLatex`.
func tokenizeInlineLatex(lexer *Lexer, src string, tokens []Token) *Token {
	opening := ""
	closing := ""
	switch {
	case strings.HasPrefix(src, "$$"):
		opening = "$$"
		closing = "$$"
	case strings.HasPrefix(src, "\\("):
		opening = "\\("
		closing = "\\)"
	case strings.HasPrefix(src, "\\["):
		opening = "\\["
		closing = "\\]"
	case strings.HasPrefix(src, "$") && !matches(pendingDollarOpening, src):
		opening = "$"
		closing = "$"
	default:
		return nil
	}

	closingIndex := findClosingDelimiter(src, closing, utf16Len(opening))
	if closingIndex >= 0 && opening == "$" {
		middle := jsSlice(src, utf16Len(opening), closingIndex)
		after := substring(src, closingIndex+1)
		if matches(trailingSpace, middle) ||
			matches(digitStart, after) ||
			(matches(constantCaseText, middle) && matches(identifierStart, after)) ||
			strings.Contains(middle, "`") {
			return nil
		}
	}

	if closingIndex < 0 {
		pendingSource := substring(src, utf16Len(opening))
		if strings.HasPrefix(opening, "\\") || looksLikePendingDollarMath(pendingSource) {
			return &Token{Type: TypeLatex, Raw: src, Text: pendingSource, Pending: true}
		}
		return nil
	}

	text := jsSlice(src, utf16Len(opening), closingIndex)
	if text == "" || strings.Contains(text, "\n") {
		return nil
	}

	raw := cutPrefix(src, closingIndex+len(closing))
	return &Token{Type: TypeLatex, Raw: raw, Text: text}
}

// tableDelimiterRow is pi's `TABLE_DELIMITER_ROW_REGEX`.
var tableDelimiterRow = mustRegex(`^ {0,3}\|?[ \t]*:?-+:?[ \t]*(?:\|[ \t]*:?-+:?[ \t]*)*\|?[ \t]*$`, flagNone)

// containsTableStart reports whether the source holds the start of a GFM
// table, like pi's `containsTableStart`.
//
// A GFM table starts with a header row followed by a delimiter row such as
// "|---|---|" or "| --- |". The header needs a "|" only when the delimiter
// has none: a pipe-free delimiter without a pipe in the header is a thematic
// break, not a table.
func containsTableStart(source string) bool {
	lines := strings.Split(source, "\n")
	for index := 0; index < len(lines)-1; index++ {
		delimiterRow := lines[index+1]
		if (strings.Contains(delimiterRow, "|") || strings.Contains(lines[index], "|")) && matches(tableDelimiterRow, delimiterRow) {
			return true
		}
	}
	return false
}

// The expressions of pi's `tokenizeBlockLatex`.
var (
	blockDollarLatex        = mustRegex(`^ {0,3}\$\$[ \t]*(?:\n)?([\s\S]*?)\$\$[ \t]*(?:\n|$)`, flagNone)
	blockBracketLatex       = mustRegex(`^ {0,3}\\\[[ \t]*(?:\n)?([\s\S]*?)\\\][ \t]*(?:\n|$)`, flagNone)
	blockPendingBracket     = mustRegex(`^ {0,3}\\\[[ \t]*(?:\n)?([\s\S]*)$`, flagNone)
	blockPendingDollarLatex = mustRegex(`^ {0,3}\$\$[ \t]*(?:\n)?([\s\S]*)$`, flagNone)
)

// tokenizeBlockLatex is pi's `tokenizeBlockLatex`.
func tokenizeBlockLatex(lexer *Lexer, src string, tokens []Token) *Token {
	// Display math never spans a table. Without this check, an unclosed "$$"
	// or "\[" pairs with a later opener or stays pending, swallowing the
	// table.
	if match := exec(blockDollarLatex, src); match != nil && match.group(1) != "" && !containsTableStart(match.group(1)) {
		return &Token{Type: TypeLatexBlock, Raw: match.whole(), Text: jsTrim(match.group(1))}
	}

	if match := exec(blockBracketLatex, src); match != nil && match.group(1) != "" && !containsTableStart(match.group(1)) {
		return &Token{Type: TypeLatexBlock, Raw: match.whole(), Text: jsTrim(match.group(1))}
	}

	if match := exec(blockPendingBracket, src); match != nil && match.group(1) != "" && !containsTableStart(match.group(1)) {
		return &Token{Type: TypeLatexBlock, Raw: match.whole(), Text: match.group(1), Pending: true}
	}
	if match := exec(blockPendingDollarLatex, src); match != nil && match.group(1) != "" && looksLikePendingDollarMath(match.group(1)) && !containsTableStart(match.group(1)) {
		return &Token{Type: TypeLatexBlock, Raw: match.whole(), Text: match.group(1), Pending: true}
	}
	return nil
}

// latexBlockStart is the `start` function of the block latex extension, pi's
// inline arrow function.
var latexBlockStartRegex = mustRegex(`(?:^|\n) {0,3}(?:\$\$|\\\[)`, flagNone)

func latexBlockStart(lexer *Lexer, src string) int {
	match := exec(latexBlockStartRegex, src)
	if match == nil {
		return -1
	}
	start := match.index
	if firstUnit(match.whole()) == "\n" {
		start++
	}
	return start
}

// latexInlineStart is the `start` function of the inline latex extension,
// pi's inline arrow function.
func latexInlineStart(lexer *Lexer, src string) int {
	best := -1
	for _, needle := range []string{"$", "\\(", "\\["} {
		index := indexOfUnit(src, needle)
		if index >= 0 && (best == -1 || index < best) {
			best = index
		}
	}
	return best
}

// LatexExtensions returns pi's `LATEX_MARKDOWN_EXTENSIONS`, so that they can
// be registered on a marked instance.
func LatexExtensions() []Extension {
	return []Extension{
		{
			Name:      TypeLatexBlock,
			Level:     "block",
			Tokenizer: tokenizeBlockLatex,
			Start:     latexBlockStart,
		},
		{
			Name:      TypeLatex,
			Level:     "inline",
			Tokenizer: tokenizeInlineLatex,
			Start:     latexInlineStart,
		},
	}
}

// TrimPartialClosingFences trims streamed partial closing fences, like pi's
// `trimPartialClosingFences`.
//
// It keeps code blocks from shrinking or flickering while a closing fence is
// streamed character by character.
func TrimPartialClosingFences(tokens []Token) {
	if len(tokens) == 0 {
		return
	}
	token := &tokens[len(tokens)-1]
	switch token.Type {
	case TypeList:
		if len(token.Items) > 0 {
			TrimPartialClosingFences(token.Items[len(token.Items)-1].Tokens)
		}
		return
	case TypeBlockquote:
		TrimPartialClosingFences(token.Tokens)
		return
	case TypeCode:
		// fall through to the fence trim below
	default:
		return
	}

	// Trim streamed partial closing fences so code blocks do not
	// shrink/flicker when the final fence character arrives.
	marker := ""
	if match := exec(fenceMarkerRegex, token.Raw); match != nil {
		marker = match.group(1)
	}
	lastLine := lastLine(token.Raw)
	if marker == "" || lastLine == "" || utf16Len(lastLine) >= utf16Len(marker) || lastLine != strings.Repeat(firstUnit(marker), utf16Len(lastLine)) {
		return
	}

	token.Text = strings.TrimSuffix(dropLastUnits(token.Text, utf16Len(lastLine)), "\n")
}

// fenceMarkerRegex finds the fence of a code block.
var fenceMarkerRegex = mustRegex(`^(`+"`{3,}"+`|~{3,})`, flagNone)

// lastLine returns the last line of a string.
func lastLine(src string) string {
	if index := strings.LastIndexByte(src, '\n'); index >= 0 {
		return src[index+1:]
	}
	return src
}

// NewPiMarked returns a marked instance wired the way pi wires its markdown
// parser:
//
//	markdownParser.setOptions({ tokenizer: new StrictStrikethroughTokenizer() });
//	markdownParser.use({
//	  tokenizer: { emStrong: markedCjkFriendly().tokenizer?.emStrong },
//	  extensions: [...LATEX_MARKDOWN_EXTENSIONS],
//	});
//
// The instance uses marked's defaults otherwise, which turn GFM on.
func NewPiMarked() *Marked {
	instance := New()
	instance.SetOptions(&MarkedOptions{Tokenizer: &Tokenizer{Overrides: StrictStrikethrough()}})
	// pi registers only the emStrong tokenizer of marked-cjk-friendly, not
	// its del tokenizer, so the strict strikethrough tokenizer keeps the
	// strikethrough spans.
	instance.Use(MarkedExtension{
		Tokenizer:  &TokenizerOverrides{EmStrong: cjkEmStrong},
		Extensions: LatexExtensions(),
	})
	return instance
}
