package marked

import (
	"strings"

	"github.com/dlclark/regexp2"
)

// Rules, ported from marked/src/rules.ts.
//
// marked builds its grammar by replacing named placeholders inside regular
// expression sources. The port keeps that construction so the expressions stay
// comparable with the upstream file. The sources below are JavaScript
// expressions; regex.go translates them into the dialect that the port's
// engine understands.

// Flags of the marked expressions.
const (
	flagNone      = regexp2.None
	flagIgnore    = regexp2.IgnoreCase | regexp2.Unicode
	flagMultiline = regexp2.Multiline
)

// ruleBuilder mirrors marked's `edit` helper.
type ruleBuilder struct {
	pattern string
	flags   regexp2.RegexOptions
}

// edit starts a rule expression, like marked's `edit`.
func edit(source string, flags regexp2.RegexOptions) *ruleBuilder {
	return &ruleBuilder{pattern: source, flags: flags}
}

// replace substitutes a placeholder inside the expression source. marked
// replaces the first occurrence when the placeholder is a string.
func (b *ruleBuilder) replace(name string, value string) *ruleBuilder {
	b.pattern = strings.Replace(b.pattern, name, applyCaret(value), 1)
	return b
}

// replaceAll substitutes every occurrence of a placeholder.
func (b *ruleBuilder) replaceAll(name string, value string) *ruleBuilder {
	b.pattern = strings.ReplaceAll(b.pattern, name, applyCaret(value))
	return b
}

// btMark marks a backtick inside a source that cannot hold a raw string
// literal. The rule builder replaces the mark before the expression is
// compiled.
const btMark = "\ue000"

// source returns the composed expression source.
func (b *ruleBuilder) source() string {
	return strings.ReplaceAll(b.pattern, btMark, "\x60")
}

// getRegex compiles the expression.
func (b *ruleBuilder) getRegex() *regexp2.Regexp {
	multiline := b.flags&regexp2.Multiline != 0
	re, err := regexp2.Compile(jsToDotNet(b.pattern, multiline), b.flags)
	if err != nil {
		panic("marked: invalid rule expression " + b.pattern + ": " + err.Error())
	}
	rememberSource(re, b.source())
	return re
}

// mustRegex compiles a plain expression.
func mustRegex(source string, flags regexp2.RegexOptions) *regexp2.Regexp {
	multiline := flags&regexp2.Multiline != 0
	re, err := regexp2.Compile(jsToDotNet(source, multiline), flags)
	if err != nil {
		panic("marked: invalid expression " + source + ": " + err.Error())
	}
	rememberSource(re, source)
	return re
}

// noopTest never matches, like marked's `noopTest`.
func noopTest() *regexp2.Regexp {
	return nil
}

// OtherRules holds the helper expressions of marked's `other` export.
type OtherRules struct {
	CodeRemoveIndent         *regexp2.Regexp
	OutputLinkReplace        *regexp2.Regexp
	IndentCodeCompensation   *regexp2.Regexp
	BeginningSpace           *regexp2.Regexp
	EndingHash               *regexp2.Regexp
	StartingSpaceChar        *regexp2.Regexp
	EndingSpaceChar          *regexp2.Regexp
	NonSpaceChar             *regexp2.Regexp
	NewLineCharGlobal        *regexp2.Regexp
	TabCharGlobal            *regexp2.Regexp
	MultipleSpaceGlobal      *regexp2.Regexp
	BlankLine                *regexp2.Regexp
	DoubleBlankLine          *regexp2.Regexp
	BlockquoteStart          *regexp2.Regexp
	BlockquoteSetextReplace  *regexp2.Regexp
	BlockquoteSetextReplace2 *regexp2.Regexp
	ListReplaceNesting       *regexp2.Regexp
	ListIsTask               *regexp2.Regexp
	ListReplaceTask          *regexp2.Regexp
	ListTaskCheckbox         *regexp2.Regexp
	AnyLine                  *regexp2.Regexp
	HrefBrackets             *regexp2.Regexp
	TableDelimiter           *regexp2.Regexp
	TableAlignChars          *regexp2.Regexp
	TableRowBlankLine        *regexp2.Regexp
	TableAlignRight          *regexp2.Regexp
	TableAlignCenter         *regexp2.Regexp
	TableAlignLeft           *regexp2.Regexp
	StartATag                *regexp2.Regexp
	EndATag                  *regexp2.Regexp
	StartPreScriptTag        *regexp2.Regexp
	EndPreScriptTag          *regexp2.Regexp
	StartAngleBracket        *regexp2.Regexp
	EndAngleBracket          *regexp2.Regexp
	PedanticHrefTitle        *regexp2.Regexp
	UnicodeAlphaNumeric      *regexp2.Regexp
	EscapeTest               *regexp2.Regexp
	EscapeReplace            *regexp2.Regexp
	EscapeTestNoEncode       *regexp2.Regexp
	EscapeReplaceNoEncode    *regexp2.Regexp
	Caret                    *regexp2.Regexp
	PercentDecode            *regexp2.Regexp
	FindPipe                 *regexp2.Regexp
	SplitPipe                *regexp2.Regexp
	SlashPipe                *regexp2.Regexp
	CarriageReturn           *regexp2.Regexp
	SpaceLine                *regexp2.Regexp
	NotSpaceStart            *regexp2.Regexp
	EndingNewline            *regexp2.Regexp
	ListItemRegex            func(bull string) *regexp2.Regexp
	NextBulletRegex          func(indent int) *regexp2.Regexp
	HrRegex                  func(indent int) *regexp2.Regexp
	FencesBeginRegex         func(indent int) *regexp2.Regexp
	HeadingBeginRegex        func(indent int) *regexp2.Regexp
	HtmlBeginRegex           func(indent int) *regexp2.Regexp
	BlockquoteBeginRegex     func(indent int) *regexp2.Regexp
}

// BlockRules holds the block grammar of one marked mode.
type BlockRules struct {
	Newline    *regexp2.Regexp
	Code       *regexp2.Regexp
	Fences     *regexp2.Regexp
	Hr         *regexp2.Regexp
	Heading    *regexp2.Regexp
	Blockquote *regexp2.Regexp
	List       *regexp2.Regexp
	HTML       *regexp2.Regexp
	Def        *regexp2.Regexp
	Table      *regexp2.Regexp
	LHeading   *regexp2.Regexp
	Paragraph  *regexp2.Regexp
	Text       *regexp2.Regexp
}

// InlineRules holds the inline grammar of one marked mode.
type InlineRules struct {
	Backpedal         *regexp2.Regexp
	AnyPunctuation    *regexp2.Regexp
	Autolink          *regexp2.Regexp
	BlockSkip         *regexp2.Regexp
	Br                *regexp2.Regexp
	Code              *regexp2.Regexp
	Del               *regexp2.Regexp
	DelLDelim         *regexp2.Regexp
	DelRDelim         *regexp2.Regexp
	EmStrongLDelim    *regexp2.Regexp
	EmStrongRDelimAst *regexp2.Regexp
	EmStrongRDelimUnd *regexp2.Regexp
	Escape            *regexp2.Regexp
	Link              *regexp2.Regexp
	Nolink            *regexp2.Regexp
	Punctuation       *regexp2.Regexp
	Reflink           *regexp2.Regexp
	ReflinkSearch     *regexp2.Regexp
	Tag               *regexp2.Regexp
	Text              *regexp2.Regexp
	URL               *regexp2.Regexp
}

// Rules is marked's `Rules` interface. It holds the grammar of one mode;
// marked's `block` and `inline` exports hold the grammars of every mode.
type Rules struct {
	Other  OtherRules
	Block  BlockRules
	Inline InlineRules
}

// cachedIndentRegex mirrors marked's `cachedIndentRegex`.
func cachedIndentRegex(create func(indent int) *regexp2.Regexp) func(indent int) *regexp2.Regexp {
	var cache [4]*regexp2.Regexp
	return func(indent int) *regexp2.Regexp {
		cacheIndex := max(0, min(3, indent-1))
		if cache[cacheIndex] == nil {
			cache[cacheIndex] = create(cacheIndex)
		}
		return cache[cacheIndex]
	}
}

// other is marked's `other` export.
var other = OtherRules{
	CodeRemoveIndent:         mustRegex(`^(?: {1,4}| {0,3}\t)`, flagMultiline),
	OutputLinkReplace:        mustRegex(`\\([\[\]])`, flagNone),
	IndentCodeCompensation:   mustRegex("^(\\s+)(?:"+backtick+backtick+backtick+")", flagNone),
	BeginningSpace:           mustRegex(`^\s+`, flagNone),
	EndingHash:               mustRegex(`#$`, flagNone),
	StartingSpaceChar:        mustRegex(`^ `, flagNone),
	EndingSpaceChar:          mustRegex(` $`, flagNone),
	NonSpaceChar:             mustRegex(`[^ ]`, flagNone),
	NewLineCharGlobal:        mustRegex(`\n`, flagNone),
	TabCharGlobal:            mustRegex(`\t`, flagNone),
	MultipleSpaceGlobal:      mustRegex(`\s+`, flagNone),
	BlankLine:                mustRegex(`^[ \t]*$`, flagNone),
	DoubleBlankLine:          mustRegex(`\n[ \t]*\n[ \t]*$`, flagNone),
	BlockquoteStart:          mustRegex(`^ {0,3}>`, flagNone),
	BlockquoteSetextReplace:  mustRegex(`\n {0,3}((?:=+|-+) *)(?=\n|$)`, flagNone),
	BlockquoteSetextReplace2: mustRegex(`^ {0,3}>[ \t]?`, flagMultiline),
	ListReplaceNesting:       mustRegex(`^ {1,4}(?=( {4})*[^ ])`, flagNone),
	ListIsTask:               mustRegex(`^\[[ xX]\] +\S`, flagNone),
	ListReplaceTask:          mustRegex(`^\[[ xX]\] +`, flagNone),
	ListTaskCheckbox:         mustRegex(`\[[ xX]\]`, flagNone),
	AnyLine:                  mustRegex(`\n.*\n`, flagNone),
	HrefBrackets:             mustRegex(`^<(.*)>$`, flagNone),
	TableDelimiter:           mustRegex(`[:|]`, flagNone),
	TableAlignChars:          mustRegex(`^\||\| *$`, flagMultiline),
	TableRowBlankLine:        mustRegex(`\n[ \t]*$`, flagNone),
	TableAlignRight:          mustRegex(`^ *-+: *$`, flagNone),
	TableAlignCenter:         mustRegex(`^ *:-+: *$`, flagNone),
	TableAlignLeft:           mustRegex(`^ *:-+ *$`, flagNone),
	StartATag:                mustRegex(`^<a `, flagIgnore),
	EndATag:                  mustRegex(`^</a>`, flagIgnore),
	StartPreScriptTag:        mustRegex(`^<(pre|code|kbd|script)(\s|>)`, flagIgnore),
	EndPreScriptTag:          mustRegex(`^</(pre|code|kbd|script)(\s|>)`, flagIgnore),
	StartAngleBracket:        mustRegex(`^<`, flagNone),
	EndAngleBracket:          mustRegex(`>$`, flagNone),
	PedanticHrefTitle:        mustRegex(`^([^'"]*[^\s])\s+(['"])(.*)\2`, flagNone),
	UnicodeAlphaNumeric:      mustRegex(`[\p{L}\p{N}]`, flagNone),
	EscapeTest:               mustRegex(`[&<>"']`, flagNone),
	EscapeReplace:            mustRegex(`[&<>"']`, flagNone),
	EscapeTestNoEncode:       mustRegex(`[<>"']|&(?!(#\d{1,7}|#[Xx][a-fA-F0-9]{1,6}|\w+);)`, flagNone),
	EscapeReplaceNoEncode:    mustRegex(`[<>"']|&(?!(#\d{1,7}|#[Xx][a-fA-F0-9]{1,6}|\w+);)`, flagNone),
	Caret:                    mustRegex(`(^|[^\[])\^`, flagNone),
	PercentDecode:            mustRegex(`%25`, flagNone),
	FindPipe:                 mustRegex(`\|`, flagNone),
	SplitPipe:                mustRegex(` \|`, flagNone),
	SlashPipe:                mustRegex(`\\\|`, flagNone),
	CarriageReturn:           mustRegex(`\r\n|\r`, flagNone),
	SpaceLine:                mustRegex(`^ +$`, flagMultiline),
	NotSpaceStart:            mustRegex(`^\S*`, flagNone),
	EndingNewline:            mustRegex(`\n$`, flagNone),
}

func init() {
	other.ListItemRegex = func(bull string) *regexp2.Regexp {
		return mustRegex(`^( {0,3}`+bull+`)((?:[\t ][^\n]*)?(?:\n|$))`, flagNone)
	}
	other.NextBulletRegex = cachedIndentRegex(func(indent int) *regexp2.Regexp {
		return mustRegex(`^ {0,`+itoa(indent)+`}(?:[*+-]|\d{1,9}[.)])((?:[ \t][^\n]*)?(?:\n|$))`, flagNone)
	})
	other.HrRegex = cachedIndentRegex(func(indent int) *regexp2.Regexp {
		return mustRegex(`^ {0,`+itoa(indent)+`}((?:- *){3,}|(?:_ *){3,}|(?:\* *){3,})(?:\n+|$)`, flagNone)
	})
	other.FencesBeginRegex = cachedIndentRegex(func(indent int) *regexp2.Regexp {
		return mustRegex(`^ {0,`+itoa(indent)+`}(?:\x60\x60\x60|~~~)`, flagNone)
	})
	other.HeadingBeginRegex = cachedIndentRegex(func(indent int) *regexp2.Regexp {
		return mustRegex(`^ {0,`+itoa(indent)+`}#`, flagNone)
	})
	other.HtmlBeginRegex = cachedIndentRegex(func(indent int) *regexp2.Regexp {
		return mustRegex(`^ {0,`+itoa(indent)+`}<(?:[a-z].*>|!--)`, flagIgnore)
	})
	other.BlockquoteBeginRegex = cachedIndentRegex(func(indent int) *regexp2.Regexp {
		return mustRegex(`^ {0,`+itoa(indent)+`}>`, flagNone)
	})
}

// itoa converts an indent to its decimal representation.
func itoa(value int) string {
	if value == 0 {
		return "0"
	}
	negative := value < 0
	if negative {
		value = -value
	}
	var digits []byte
	for value > 0 {
		digits = append([]byte{byte('0' + value%10)}, digits...)
		value /= 10
	}
	if negative {
		return "-" + string(digits)
	}
	return string(digits)
}
