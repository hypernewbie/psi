package marked

// Block-Level Grammar, ported from marked/src/rules.ts.
//
// marked composes its grammar by replacing placeholders inside regular
// expression sources, so the port keeps the sources as strings and only
// compiles them at the end.

const (
	// newline is marked's `newline`.
	newline = `^(?:[ \t]*(?:\n|$))+`
	// blockCode is marked's `blockCode`.
	blockCode = `^((?: {4}| {0,3}\t)[^\n]+(?:\n(?:[ \t]*(?:\n|$))*)?)+`
	// fences is marked's `fences`.
	fences = "^ {0,3}(`{3,}(?=[^`\\n]*(?:\\n|$))|~{3,})([^\\n]*)(?:\\n|$)(?:|([\\s\\S]*?)(?:\\n|$))(?: {0,3}\\1[~`]* *(?=\\n|$)|$)"
	// hr is marked's `hr`.
	hr = `^ {0,3}((?:-[\t ]*){3,}|(?:_[ \t]*){3,}|(?:\*[ \t]*){3,})(?:\n+|$)`
	// heading is marked's `heading`.
	heading = `^ {0,3}(#{1,6})(?=\s|$)(.*)(?:\n+|$)`
	// bullet is marked's `bullet`.
	bullet = ` {0,3}(?:[*+-]|\d{1,9}[.)])`
	// lheadingCore is marked's `lheadingCore`.
	lheadingCore = `^(?!bull |blockCode|fences|blockquote|heading|html|table)((?:.|\n(?!\s*?\n|bull |blockCode|fences|blockquote|heading|html|table))+?)\n {0,3}(=+|-+) *(?:\n+|$)`
	// paragraphCore is marked's `_paragraph`.
	paragraphCore = `^([^\n]+(?:\n(?!hr|heading|lheading|blockquote|fences|list|html|table|[ \t]+\n)[^\n]+)*)`
	// blockText is marked's `blockText`.
	blockText = `^[^\n]+`
	// blockLabel is marked's `_blockLabel`.
	blockLabel = `(?!\s*\])(?:\\[\s\S]|[^\[\]\\])+`
	// listTag is marked's `_tag`.
	listTag = "address|article|aside|base|basefont|blockquote|body|caption" +
		"|center|col|colgroup|dd|details|dialog|dir|div|dl|dt|fieldset|figcaption" +
		"|figure|footer|form|frame|frameset|h[1-6]|head|header|hr|html|iframe" +
		"|legend|li|link|main|menu|menuitem|meta|nav|noframes|ol|optgroup|option" +
		"|p|param|search|section|summary|table|tbody|td|tfoot|th|thead|title" +
		"|tr|track|ul"
	// comment is marked's `_comment`.
	comment = `<!--(?:-?>|[\s\S]*?(?:-->|$))`
	// backtick is one backtick, spelled out so the sources can stay in raw
	// string literals.
	backtick = "\x60"
)

// htmlBlockSource is marked's `html` expression before its placeholders are
// replaced.
func htmlBlockSource() string {
	return "^ {0,3}(?:" + // optional indentation
		"<(script|pre|style|textarea)[\\s>][\\s\\S]*?(?:</\\1>[^\\n]*\\n*|$)" + // (1)
		"|comment[^\\n]*(\\n+|$)" + // (2)
		"|<\\?[\\s\\S]*?(?:\\?>[^\\n]*\\n*|$)" + // (3)
		"|<![A-Z][\\s\\S]*?(?:>[^\\n]*\\n*|$)" + // (4)
		"|<!\\[CDATA\\[[\\s\\S]*?(?:\\]\\]>[^\\n]*\\n*|$)" + // (5)
		"|</?(tag)(?: +|\\n|/?>)[\\s\\S]*?(?:(?:\\n[ \\t]*)+\\n|$)" + // (6)
		"|<(?!script|pre|style|textarea)([a-z][\\w-]*)(?:attribute)*? */?>(?=[ \\t]*(?:\\n|$))[\\s\\S]*?(?:(?:\\n[ \\t]*)+\\n|$)" + // (7) open tag
		"|</(?!script|pre|style|textarea)[a-z][\\w-]*\\s*>(?=[ \\t]*(?:\\n|$))[\\s\\S]*?(?:(?:\\n[ \\t]*)+\\n|$)" + // (7) closing tag
		")"
}

// htmlSource is marked's `html`.
func htmlSource() string {
	return edit(htmlBlockSource(), flagIgnore).
		replace("comment", comment).
		replace("tag", listTag).
		replace("attribute", " +[a-zA-Z:_][\\w.:-]*(?: *= *\"[^\"\\n]*\"| *= *'[^'\\n]*'| *= *[^\\s\"'=<>`]+)?").
		source()
}

// listSource is marked's `list`.
func listSource() string {
	return edit(`^(bull)([ \t][^\n]*?)?(?:\n|$)`, flagNone).
		replaceAll("bull", bullet).
		source()
}

// defSource is marked's `def`.
func defSource() string {
	return edit(`^ {0,3}\[(label)\]: *(?:\n[ \t]*)?([^<\s][^\s]*|<.*?>)(?:(?: +(?:\n[ \t]*)?| *\n[ \t]*)(title))? *(?:\n+|$)`, flagNone).
		replace("label", blockLabel).
		replace("title", `(?:"(?:\\"?|[^"\\])*"|'[^'\n]*(?:\n[^'\n]+)*\n?'|\([^()]*\))`).
		source()
}

// lheadingSource is marked's `lheading`.
func lheadingSource() string {
	return edit(lheadingCore, flagNone).
		replaceAll("bull", bullet).                                  // lists can interrupt
		replaceAll("blockCode", `(?: {4}| {0,3}\t)`).                // indented code blocks can interrupt
		replaceAll("fences", ` {0,3}(?:`+backtickFence()+`|~{3,})`). // fenced code blocks can interrupt
		replaceAll("blockquote", ` {0,3}>`).                         // blockquote can interrupt
		replaceAll("heading", ` {0,3}#{1,6}(?:\s|$)`).               // ATX heading can interrupt
		replaceAll("html", ` {0,3}<[^\n>]+>\n`).                     // block html can interrupt
		replaceAll("|table", "").                                    // table not in commonmark
		source()
}

// lheadingGfmSource is marked's `lheadingGfm`.
func lheadingGfmSource() string {
	return edit(lheadingCore, flagNone).
		replaceAll("bull", bullet).                                  // lists can interrupt
		replaceAll("blockCode", `(?: {4}| {0,3}\t)`).                // indented code blocks can interrupt
		replaceAll("fences", ` {0,3}(?:`+backtickFence()+`|~{3,})`). // fenced code blocks can interrupt
		replaceAll("blockquote", ` {0,3}>`).                         // blockquote can interrupt
		replaceAll("heading", ` {0,3}#{1,6}(?:\s|$)`).               // ATX heading can interrupt
		replaceAll("html", ` {0,3}<[^\n>]+>\n`).                     // block html can interrupt
		replaceAll("table", ` {0,3}\|?(?:[:\- ]*\|)+[\:\- ]*\n`).    // table can interrupt
		source()
}

// gfmTableSource is marked's `gfmTable` expression before its placeholders
// are replaced.
func gfmTableSource() string {
	return "^ *([^\\n ].*)\\n" + // Header
		" {0,3}((?:\\| *)?:?-+:? *(?:\\| *:?-+:? *)*(?:\\| *)?)" + // Align
		"(?:\\n((?:(?! *\\n|hr|heading|blockquote|code|fences|list|html).*(?:\\n|$))*)\\n*|$)"
}

// gfmTableExpression is marked's `gfmTable`.
func gfmTableExpression() string {
	return edit(gfmTableSource(), flagNone).
		replace("hr", hr).
		replace("heading", ` {0,3}#{1,6}(?:\s|$)`).
		replace("blockquote", ` {0,3}>`).
		replace("code", `(?: {4}| {0,3}\t)[^\n]`).
		replace("fences", " {0,3}(?:"+backtickFence()+"(?=[^`\\n]*(?:\\n|$))|~~~)[^\\n]*(?:\\n|$)").
		replace("list", ` {0,3}(?:[*+-]|1[.)])[ \t]`). // any bullet ends the table rows
		replace("html", `</?(?:`+listTag+`)(?: +|\n|/?>)|<(?:script|pre|style|textarea|!--)`).
		replace("tag", listTag). // tables can be interrupted by type (6) html blocks
		source()
}

// paragraphSource is marked's `createParagraph`.
func paragraphSource(listInterrupt string) string {
	return edit(paragraphCore, flagNone).
		replace("hr", hr).
		replace("heading", ` {0,3}#{1,6}(?:\s|$)`).
		replace("|lheading", ""). // setext headings don't interrupt commonmark paragraphs
		replace("|table", "").
		replace("blockquote", ` {0,3}>`).
		replace("fences", " {0,3}(?:"+backtickFence()+"(?=[^`\\n]*(?:\\n|$))|~~~)[^\\n]*(?:\\n|$)").
		replace("list", listInterrupt).
		replace("html", `</?(?:`+listTag+`)(?: +|\n|/?>)|<(?:script|pre|style|textarea|!--)`).
		replace("tag", listTag). // pars can be interrupted by type (6) html blocks
		source()
}

// blockquoteSource is marked's `blockquote`.
func blockquoteSource() string {
	// inside a blockquote a bare list marker (any number) starts a sibling
	// list, so it must not be lazily continued as paragraph text (unlike a
	// top level paragraph, where an empty list cannot interrupt)
	blockquoteParagraph := paragraphSource(` {0,3}(?:[*+-]|\d{1,9}[.)])(?:[ \t]|\n|$)`)
	return edit(`^( {0,3}> ?(paragraph|[^\n]*)(?:\n|$))+`, flagNone).
		replace("paragraph", blockquoteParagraph).
		source()
}

// backtickFence is marked's fence of three or more backticks.
func backtickFence() string {
	return backtick + "{3,}"
}

// blockNormal is marked's `blockNormal`.
var blockNormal = BlockRules{
	Newline:    mustRegex(newline, flagNone),
	Code:       mustRegex(blockCode, flagNone),
	Fences:     mustRegex(fences, flagNone),
	Hr:         mustRegex(hr, flagNone),
	Heading:    mustRegex(heading, flagNone),
	Blockquote: mustRegex(blockquoteSource(), flagNone),
	List:       mustRegex(listSource(), flagNone),
	HTML:       mustRegex(htmlSource(), flagIgnore),
	Def:        mustRegex(defSource(), flagNone),
	Table:      noopTest(),
	LHeading:   mustRegex(lheadingSource(), flagNone),
	Paragraph:  mustRegex(paragraphSource(` {0,3}(?:[*+-]|1[.)])[ \t]+[^ \t\n]`), flagNone),
	Text:       mustRegex(blockText, flagNone),
}

// blockGfm is marked's `blockGfm`.
var blockGfm = BlockRules{
	Newline:    blockNormal.Newline,
	Code:       blockNormal.Code,
	Fences:     blockNormal.Fences,
	Hr:         blockNormal.Hr,
	Heading:    blockNormal.Heading,
	Blockquote: blockNormal.Blockquote,
	List:       blockNormal.List,
	HTML:       blockNormal.HTML,
	Def:        blockNormal.Def,
	Table:      mustRegex(gfmTableExpression(), flagNone),
	LHeading:   mustRegex(lheadingGfmSource(), flagNone),
	Paragraph: mustRegex(edit(paragraphCore, flagNone).
		replace("hr", hr).
		replace("heading", ` {0,3}#{1,6}(?:\s|$)`).
		replace("|lheading", "").               // setext headings don't interrupt commonmark paragraphs
		replace("table", gfmTableExpression()). // interrupt paragraphs with table
		replace("blockquote", ` {0,3}>`).
		replace("fences", " {0,3}(?:"+backtickFence()+"(?=[^`\\n]*(?:\\n|$))|~~~)[^\\n]*(?:\\n|$)").
		replace("list", ` {0,3}(?:[*+-]|1[.)])[ \t]+[^ \t\n]`). // only non-empty lists starting from 1 can interrupt
		replace("html", `</?(?:`+listTag+`)(?: +|\n|/?>)|<(?:script|pre|style|textarea|!--)`).
		replace("tag", listTag). // pars can be interrupted by type (6) html blocks
		source(), flagNone),
	Text: blockNormal.Text,
}

// blockPedantic is marked's `blockPedantic`.
var blockPedantic = BlockRules{
	Newline:    blockNormal.Newline,
	Code:       blockNormal.Code,
	Fences:     noopTest(), // fences not supported
	Hr:         blockNormal.Hr,
	Heading:    mustRegex(`^(#{1,6})(.*)(?:\n+|$)`, flagNone),
	Blockquote: blockNormal.Blockquote,
	List:       blockNormal.List,
	HTML: mustRegex(edit(`^ *(?:comment *(?:\n|\s*$)`+
		`|<(tag)[\s\S]+?</\1> *(?:\n{2,}|\s*$)`+ // closed tag
		`|<tag(?:"[^"]*"|'[^']*'|\s[^'"/>\s]*)*?/?> *(?:\n{2,}|\s*$))`, flagNone).
		replace("comment", comment).
		replaceAll("tag", "(?!(?:a|em|strong|small|s|cite|q|dfn|abbr|data|time|code|var|samp|kbd|sub"+
			"|sup|i|b|u|mark|ruby|rt|rp|bdi|bdo|span|br|wbr|ins|del|img)\\b)\\w+(?!:|[^\\w\\s@]*@)\\b").
		source(), flagNone),
	Def:      mustRegex(`^ *\[([^\]]+)\]: *<?([^\s>]+)>?(?: +(["(][^\n]+[")]))? *(?:\n+|$)`, flagNone),
	Table:    noopTest(),
	LHeading: mustRegex(`^(.+?)\n {0,3}(=+|-+) *(?:\n+|$)`, flagNone),
	Paragraph: mustRegex(edit(paragraphCore, flagNone).
		replace("hr", hr).
		replace("heading", ` *#{1,6} *[^\n]`).
		replace("lheading", lheadingSource()).
		replace("|table", "").
		replace("blockquote", ` {0,3}>`).
		replace("|fences", "").
		replace("|list", "").
		replace("|html", "").
		replace("|tag", "").
		source(), flagNone),
	Text: blockNormal.Text,
}

// Inline-Level Grammar, ported from marked/src/rules.ts.

const (
	// escape is marked's `escape`.
	escape = `^\\([!"#$%&'()*+,\-./:;<=>?@\[\]\\^_` + "\x60" + `{|}~])`
	// inlineCode is marked's `inlineCode`.
	inlineCode = "^(`+)([^`]|[^`][\\s\\S]*?[^`])\\1(?!`)"
	// br is marked's `br`.
	br = `^( {2,}|\\)\n(?!\s*$)`
	// inlineText is marked's `inlineText`.
	inlineText = "^(`+|[^`])(?:(?= {2,}\\n)|[\\s\\S]*?(?:(?=[\\\\<!\\[`*_]|\\b_|$)|[^ ](?= {2,}\\n)))"
	// punctuation is marked's `_punctuation`.
	punctuation = `[\p{P}\p{S}]`
	// punctuationOrSpace is marked's `_punctuationOrSpace`.
	punctuationOrSpace = `[\s\p{P}\p{S}]`
	// notPunctuationOrSpace is marked's `_notPunctuationOrSpace`.
	notPunctuationOrSpace = `[^\s\p{P}\p{S}]`
	// openQuote is marked's `_openQuote`.
	openQuote = `[\p{Pi}\p{Ps}"']`
	// emStrongLDelimCore is marked's `emStrongLDelimCore`.
	emStrongLDelimCore = `^(?:\*+(?:((?!\*)punct)|([^\s*]))?)|^_+(?:((?!_)punct)|([^\s_]))?`
	// emStrongLDelimPedanticCore is marked's `emStrongLDelimPedanticCore`.
	emStrongLDelimPedanticCore = `^(?:\*+(?:((?!\*)(?!openQuote)punct)|([^\s*]))?)|^_+(?:((?!_)(?!openQuote)punct)|([^\s_]))?`
	// caseInsensitiveProtocol is marked's `_caseInsensitiveProtocol`.
	caseInsensitiveProtocol = `[hH][tT][tT][pP][sS]?|[fF][tT][pP]`
)

// inlineLabelSource is marked's `_inlineLabel`.
func inlineLabelSource() string {
	return "(?:\\[(?:\\\\[\\s\\S]|[^\\[\\]\\\\])*\\]|\\\\[\\s\\S]|\x60+(?!\x60)[^\x60]*?\x60+(?!\x60)|\x60\x60+(?=\\])|[^\\[\\]\\\\\x60])*?"
}

// inlineNormal is marked's `inlineNormal`.
var inlineNormal = InlineRules{
	Backpedal:         noopTest(), // only used for GFM url
	AnyPunctuation:    mustRegex(anyPunctuationSource(), flagNone),
	Autolink:          mustRegex(autolinkSource(), flagNone),
	BlockSkip:         mustRegex(blockSkipSource(), flagNone),
	Br:                mustRegex(br, flagNone),
	Code:              mustRegex(inlineCode, flagNone),
	Del:               noopTest(),
	DelLDelim:         noopTest(),
	DelRDelim:         noopTest(),
	EmStrongLDelim:    mustRegex(emStrongLDelimSource(), flagNone),
	EmStrongRDelimAst: mustRegex(emStrongRDelimAstSource(), flagNone),
	EmStrongRDelimUnd: mustRegex(emStrongRDelimUndSource(), flagNone),
	Escape:            mustRegex(escape, flagNone),
	Link:              mustRegex(linkSource(), flagNone),
	Nolink:            mustRegex(nolinkSource(), flagNone),
	Punctuation: mustRegex(edit(`^((?![*_])punctSpace)`, flagNone).
		replaceAll("punctSpace", punctuationOrSpace).
		source(), flagNone),
	Reflink:       mustRegex(reflinkSource(), flagNone),
	ReflinkSearch: mustRegex(reflinkSearchSource(), flagNone),
	Tag:           mustRegex(tagSource(), flagNone),
	Text:          mustRegex(inlineText, flagNone),
	URL:           noopTest(),
}

// inlinePedantic is marked's `inlinePedantic`.
var inlinePedantic = InlineRules{
	Backpedal:         inlineNormal.Backpedal,
	AnyPunctuation:    inlineNormal.AnyPunctuation,
	Autolink:          inlineNormal.Autolink,
	BlockSkip:         inlineNormal.BlockSkip,
	Br:                inlineNormal.Br,
	Code:              inlineNormal.Code,
	Del:               inlineNormal.Del,
	DelLDelim:         inlineNormal.DelLDelim,
	DelRDelim:         inlineNormal.DelRDelim,
	EmStrongLDelim:    mustRegex(emStrongLDelimPedanticSource(), flagNone),
	EmStrongRDelimAst: mustRegex(emStrongRDelimAstPedanticSource(), flagNone),
	EmStrongRDelimUnd: mustRegex(emStrongRDelimUndPedanticSource(), flagNone),
	Escape:            inlineNormal.Escape,
	Link: mustRegex(edit(`^!?\[(label)\]\((.*?)\)`, flagNone).
		replace("label", inlineLabelSource()).
		source(), flagNone),
	Nolink:        inlineNormal.Nolink,
	Punctuation:   inlineNormal.Punctuation,
	Reflink:       mustRegex(edit(`^!?\[(label)\]\s*\[([^\]]*)\]`, flagNone).replace("label", inlineLabelSource()).source(), flagNone),
	ReflinkSearch: inlineNormal.ReflinkSearch,
	Tag:           inlineNormal.Tag,
	Text:          inlineNormal.Text,
	URL:           inlineNormal.URL,
}

// inlineGfm is marked's `inlineGfm`.
var inlineGfm = InlineRules{
	Backpedal:         mustRegex(`(?:[^?!.,:;*_'"~()&]+|\([^)]*\)|&(?![a-zA-Z0-9]+;$)|[?!.,:;*_'"~)]+(?!$))+`, flagNone),
	AnyPunctuation:    inlineNormal.AnyPunctuation,
	Autolink:          inlineNormal.Autolink,
	BlockSkip:         inlineNormal.BlockSkip,
	Br:                inlineNormal.Br,
	Code:              inlineNormal.Code,
	Del:               mustRegex(`^(~~?)(?=[^\s~])((?:\\[\s\S]|[^\\])*?(?:\\[\s\S]|[^\s~\\]))\1(?=[^~]|$)`, flagNone),
	DelLDelim:         mustRegex(delLDelimSource(), flagNone),
	DelRDelim:         mustRegex(delRDelimSource(), flagNone),
	EmStrongLDelim:    mustRegex(emStrongLDelimGfmSource(), flagNone),
	EmStrongRDelimAst: mustRegex(emStrongRDelimAstGfmSource(), flagNone),
	EmStrongRDelimUnd: inlineNormal.EmStrongRDelimUnd,
	Escape:            inlineNormal.Escape,
	Link:              inlineNormal.Link,
	Nolink:            inlineNormal.Nolink,
	Punctuation:       inlineNormal.Punctuation,
	Reflink:           inlineNormal.Reflink,
	ReflinkSearch:     inlineNormal.ReflinkSearch,
	Tag:               inlineNormal.Tag,
	Text:              mustRegex(inlineGfmTextExpression(), flagNone),
	URL: mustRegex(edit(`^((?:protocol):\/\/|www\.)(?:[a-zA-Z0-9\-]+\.?)+[^\s<]*|^email`, flagNone).
		replace("protocol", caseInsensitiveProtocol).
		replace("email", `[A-Za-z0-9._+-]+(@)[a-zA-Z0-9-_]+(?:\.[a-zA-Z0-9-_]*[a-zA-Z0-9])+(?![-_])`).
		source(), flagNone),
}

// inlineBreaks is marked's `inlineBreaks`.
var inlineBreaks = InlineRules{
	Backpedal:         inlineGfm.Backpedal,
	AnyPunctuation:    inlineGfm.AnyPunctuation,
	Autolink:          inlineGfm.Autolink,
	BlockSkip:         inlineGfm.BlockSkip,
	Br:                mustRegex(edit(br, flagNone).replace("{2,}", "*").source(), flagNone),
	Code:              inlineGfm.Code,
	Del:               inlineGfm.Del,
	DelLDelim:         inlineGfm.DelLDelim,
	DelRDelim:         inlineGfm.DelRDelim,
	EmStrongLDelim:    inlineGfm.EmStrongLDelim,
	EmStrongRDelimAst: inlineGfm.EmStrongRDelimAst,
	EmStrongRDelimUnd: inlineGfm.EmStrongRDelimUnd,
	Escape:            inlineGfm.Escape,
	Link:              inlineGfm.Link,
	Nolink:            inlineGfm.Nolink,
	Punctuation:       inlineGfm.Punctuation,
	Reflink:           inlineGfm.Reflink,
	ReflinkSearch:     inlineGfm.ReflinkSearch,
	Tag:               inlineGfm.Tag,
	Text: mustRegex(edit(inlineGfmTextExpression(), flagNone).
		replace(`\b_`, `\b_| {2,}\n`).
		replaceAll("{2,}", "*").
		source(), flagNone),
	URL: inlineGfm.URL,
}

// inlineGfmTextExpression is marked's gfm inline text expression after its
// placeholder is replaced.
func inlineGfmTextExpression() string {
	return edit(inlineGfmTextSource(), flagNone).
		replace("protocol", caseInsensitiveProtocol).
		source()
}

// inlineGfmTextSource is marked's gfm inline text expression before its
// placeholder is replaced.
func inlineGfmTextSource() string {
	return "^(`+|~+|[^`~])(?:(?=[`~])|(?= {2,}\\n)|(?=[a-zA-Z0-9.!#$%&'*+\\/=?_`{\\|}~-]+@)|[\\s\\S]*?(?:(?=[\\\\<!\\[`*~_]|\\b_|protocol:\\/\\/|www\\.|$)|[^ ](?= {2,}\\n)|[^a-zA-Z0-9.!#$%&'*+\\/=?_`{\\|}~-](?=[a-zA-Z0-9.!#$%&'*+\\/=?_`{\\|}~-]+@)))"
}

// anyPunctuationSource is marked's `anyPunctuation`.
func anyPunctuationSource() string {
	return edit(`\\(punct)`, flagNone).
		replaceAll("punct", punctuation).
		source()
}

// autolinkSource is marked's `autolink`.
func autolinkSource() string {
	return edit(`^<(scheme:[^\s\x00-\x1f<>]*|email)>`, flagNone).
		replace("scheme", `[a-zA-Z][a-zA-Z0-9+.-]{1,31}`).
		replace("email", "[a-zA-Z0-9.!#$%&'*+/=?^_`{|}~-]+(@)[a-zA-Z0-9](?:[a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?(?:\\.[a-zA-Z0-9](?:[a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?)+(?![-_])").
		source()
}

// blockSkipSource is marked's `blockSkip`.
func blockSkipSource() string {
	return edit(`link|precode-code|html`, flagNone).
		replace("link", `\[(?:[^\[\]`+btMark+`]|(?<a>`+btMark+`+)[^`+btMark+`]+\k<a>(?!`+btMark+`))*?\]\((?:\\[\s\S]|[^\\\(\)]|\((?:\\[\s\S]|[^\\\(\)])*\))*\)`).
		replace("precode-", "(?<!`)"+"()").
		replace("code", "(?<b>\x60+)[^\x60]+\\k<b>(?!\x60)").
		replace("html", `<(?! )[^<>]*?>`).
		source()
}

// emStrongLDelimSource is marked's `emStrongLDelim`.
func emStrongLDelimSource() string {
	return edit(emStrongLDelimCore, flagNone).
		replaceAll("punct", punctuation).
		source()
}

// emStrongLDelimGfmSource is marked's `emStrongLDelimGfm`.
func emStrongLDelimGfmSource() string {
	return edit(emStrongLDelimCore, flagNone).
		replaceAll("punct", `(?!~)`+punctuation).
		source()
}

// emStrongLDelimPedanticSource is marked's `emStrongLDelimPedantic`.
func emStrongLDelimPedanticSource() string {
	return edit(emStrongLDelimPedanticCore, flagNone).
		replaceAll("openQuote", openQuote).
		replaceAll("punct", punctuation).
		source()
}

// emStrongRDelimAstCore is marked's `emStrongRDelimAstCore`.
const emStrongRDelimAstCore = `^[^_*]*?__[^_*]*?\*[^_*]*?(?=__)` + // Skip orphan inside strong
	`|[^*]+(?=[^*])` + // Consume to delim
	`|(?!\*)punct(\*+)(?=[\s]|$)` + // (1) #*** can only be a Right Delimiter
	`|notPunctSpace(\*+)(?!\*)(?=punctSpace|$)` + // (2) a***#, a*** can only be a Right Delimiter
	`|(?!\*)punctSpace(\*+)(?=notPunctSpace)` + // (3) #***a, ***a can only be Left Delimiter
	`|[\s](\*+)(?!\*)(?=punct)` + // (4) ***# can only be Left Delimiter
	`|(?!\*)punct(\*+)(?!\*)(?=punct)` + // (5) #***# can be either Left or Right Delimiter
	`|notPunctSpace(\*+)(?=notPunctSpace)` // (6) a***a can be either Left or Right Delimiter

// emStrongRDelimAstSource is marked's `emStrongRDelimAst`.
func emStrongRDelimAstSource() string {
	return edit(emStrongRDelimAstCore, flagNone).
		replaceAll("notPunctSpace", notPunctuationOrSpace).
		replaceAll("punctSpace", punctuationOrSpace).
		replaceAll("punct", punctuation).
		source()
}

// emStrongRDelimAstGfmSource is marked's `emStrongRDelimAstGfm`.
func emStrongRDelimAstGfmSource() string {
	return edit(emStrongRDelimAstCore, flagNone).
		replaceAll("notPunctSpace", `(?:[^\s\p{P}\p{S}]|~)`).
		replaceAll("punctSpace", `(?!~)[\s\p{P}\p{S}]`).
		replaceAll("punct", `(?!~)[\p{P}\p{S}]`).
		source()
}

// emStrongRDelimAstPedanticCore is marked's `emStrongRDelimAstPedanticCore`.
const emStrongRDelimAstPedanticCore = `^[^_*]*?__[^_*]*?\*[^_*]*?(?=__)` + // Skip orphan inside strong
	`|[^*]+(?=[^*])` + // Consume to delim
	`|(?!\*)punct(\*+)(?=[\s]|$)` + // (1) #*** can only be a Right Delimiter
	`|notPunctSpace(\*+)(?!\*)(?=punctSpace|$)` + // (2) a***#, a*** can only be a Right Delimiter
	`|(?!\*)[\s](\*+)(?=notPunctSpace)` + // (3) ***a can only be Left Delimiter (whitespace required; not #***a)
	`|[\s](\*+)(?!\*)(?=punct)` + // (4) ***# can only be Left Delimiter
	`|(?!\*)punct(\*+)(?!\*)(?=punct)` + // (5) #***# can be either Left or Right Delimiter
	`|(?:(?!\*)punct|notPunctSpace)(\*+)(?!\*)(?=notPunctSpace)` // (6) a***a, #***a, and :** (e.g. **foo:**bar)

// emStrongRDelimAstPedanticSource is marked's `emStrongRDelimAstPedantic`.
func emStrongRDelimAstPedanticSource() string {
	return edit(emStrongRDelimAstPedanticCore, flagNone).
		replaceAll("notPunctSpace", notPunctuationOrSpace).
		replaceAll("punctSpace", punctuationOrSpace).
		replaceAll("punct", punctuation).
		source()
}

// emStrongRDelimUndCore is marked's `emStrongRDelimUnd`.
const emStrongRDelimUndCore = `^[^_*]*?\*\*[^_*]*?_[^_*]*?(?=\*\*)` + // Skip orphan inside strong
	`|[^_]+(?=[^_])` + // Consume to delim
	`|(?!_)punct(_+)(?=[\s]|$)` + // (1) #___ can only be a Right Delimiter
	`|notPunctSpace(_+)(?!_)(?=punctSpace|$)` + // (2) a___#, a___ can only be a Right Delimiter
	`|(?!_)punctSpace(_+)(?=notPunctSpace)` + // (3) #___a, ___a can only be Left Delimiter
	`|[\s](_+)(?!_)(?=punct)` + // (4) ___# can only be Left Delimiter
	`|(?!_)punct(_+)(?!_)(?=punct)` // (5) #___# can be either Left or Right Delimiter

// emStrongRDelimUndSource is marked's `emStrongRDelimUnd`.
func emStrongRDelimUndSource() string {
	return edit(emStrongRDelimUndCore, flagNone).
		replaceAll("notPunctSpace", notPunctuationOrSpace).
		replaceAll("punctSpace", punctuationOrSpace).
		replaceAll("punct", punctuation).
		source()
}

// emStrongRDelimUndPedanticCore is marked's `emStrongRDelimUndPedanticCore`.
const emStrongRDelimUndPedanticCore = `^[^_*]*?\*\*[^_*]*?_[^_*]*?(?=\*\*)` + // Skip orphan inside strong
	`|[^_]+(?=[^_])` + // Consume to delim
	`|(?!_)punct(_+)(?=[\s]|$)` + // (1) #___ can only be a Right Delimiter
	`|notPunctSpace(_+)(?!_)(?=punctSpace|$)` + // (2) a___#, a___ can only be a Right Delimiter
	`|(?!_)[\s](_+)(?=notPunctSpace)` + // (3) ___a can only be Left Delimiter (whitespace required; not #___a)
	`|[\s](_+)(?!_)(?=punct)` + // (4) ___# can only be Left Delimiter
	`|(?!_)punct(_+)(?!_)(?=punct)` + // (5) #___# can be either Left or Right Delimiter
	`|(?:(?!_)punct|notPunctSpace)(_+)(?!_)(?=notPunctSpace)` // (6) a___a, #___a, and :__ (e.g. _foo:_bar)

// emStrongRDelimUndPedanticSource is marked's `emStrongRDelimUndPedantic`.
func emStrongRDelimUndPedanticSource() string {
	return edit(emStrongRDelimUndPedanticCore, flagNone).
		replaceAll("notPunctSpace", notPunctuationOrSpace).
		replaceAll("punctSpace", punctuationOrSpace).
		replaceAll("punct", punctuation).
		source()
}

// delLDelimSource is marked's `delLDelim`.
func delLDelimSource() string {
	return edit(`^~~?(?:((?!~)punct)|[^\s~])`, flagNone).
		replaceAll("punct", punctuation).
		source()
}

// delRDelimCore is marked's `delRDelimCore`.
const delRDelimCore = `^[^~]+(?=[^~])` + // Consume to delim
	`|(?!~)punct(~~?)(?=[\s]|$)` + // (1) #~~ can only be a Right Delimiter
	`|notPunctSpace(~~?)(?!~)(?=punctSpace|$)` + // (2) a~~#, a~~ can only be a Right Delimiter
	`|(?!~)punctSpace(~~?)(?=notPunctSpace)` + // (3) #~~a, ~~a can only be Left Delimiter
	`|[\s](~~?)(?!~)(?=punct)` + // (4) ~~# can only be Left Delimiter
	`|(?!~)punct(~~?)(?!~)(?=punct)` + // (5) #~~# can be either Left or Right Delimiter
	`|notPunctSpace(~~?)(?=notPunctSpace)` // (6) a~~a can be either Left or Right Delimiter

// delRDelimSource is marked's `delRDelim`.
func delRDelimSource() string {
	return edit(delRDelimCore, flagNone).
		replaceAll("notPunctSpace", notPunctuationOrSpace).
		replaceAll("punctSpace", punctuationOrSpace).
		replaceAll("punct", punctuation).
		source()
}

// tagSource is marked's `tag`.
func tagSource() string {
	inlineComment := edit(comment, flagNone).replace("(?:-->|$)", "-->").source()
	return edit(`^comment`+
		`|^</[a-zA-Z][\w:-]*\s*>`+ // self-closing tag
		`|^<[a-zA-Z][\w-]*(?:attribute)*?\s*/?>`+ // open tag
		`|^<\?[\s\S]*?\?>`+ // processing instruction, e.g. <?php ?>
		`|^<![a-zA-Z]+\s[\s\S]*?>`+ // declaration, e.g. <!DOCTYPE html>
		`|^<!\[CDATA\[[\s\S]*?\]\]>`, flagNone). // CDATA section
		replace("comment", inlineComment).
		replace("attribute", "\\s+[a-zA-Z:_][\\w.:-]*(?:\\s*=\\s*\"[^\"]*\"|\\s*=\\s*'[^']*'|\\s*=\\s*[^\\s\"'=<>`]+)?").
		source()
}

// linkSource is marked's `link`.
func linkSource() string {
	return edit(`^!?\[(label)\]\(\s*(href)(?:(?:[ \t]+(?:\n[ \t]*)?|\n[ \t]*)(title))?\s*\)`, flagNone).
		replace("label", inlineLabelSource()).
		replace("href", `<(?:\\.|[^\n<>\\])+>|[^ \t\n\x00-\x1f]+|(?=\))`).
		replace("title", `"(?:\\"?|[^"\\])*"|'(?:\\'?|[^'\\])*'|\((?:\\\)?|[^)\\])*\)`).
		source()
}

// reflinkSource is marked's `reflink`.
func reflinkSource() string {
	return edit(`^!?\[(label)\]\[(ref)\]`, flagNone).
		replace("label", inlineLabelSource()).
		replace("ref", blockLabel).
		source()
}

// nolinkSource is marked's `nolink`.
func nolinkSource() string {
	return edit(`^!?\[(ref)\](?:\[\])?`, flagNone).
		replace("ref", blockLabel).
		source()
}

// reflinkSearchSource is marked's `reflinkSearch`.
func reflinkSearchSource() string {
	return edit(`reflink|nolink(?!\()`, flagNone).
		replace("reflink", reflinkSource()).
		replace("nolink", nolinkSource()).
		source()
}

// block and inline hold the grammars of every mode, like marked's `block` and
// `inline` exports.
var (
	block = map[string]BlockRules{
		"normal":   blockNormal,
		"gfm":      blockGfm,
		"pedantic": blockPedantic,
	}
	inline = map[string]InlineRules{
		"normal":   inlineNormal,
		"gfm":      inlineGfm,
		"breaks":   inlineBreaks,
		"pedantic": inlinePedantic,
	}
)
