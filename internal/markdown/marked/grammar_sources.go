package marked

import (
	"sort"

	"github.com/dlclark/regexp2"
)

// The port keeps the JavaScript source of every expression it compiles, so
// the grammar can be compared with upstream marked source by source. The
// registry maps a compiled expression back to the source it was built from.

var ruleSources = map[*regexp2.Regexp]string{}

// rememberSource records the JavaScript source of a compiled expression.
func rememberSource(re *regexp2.Regexp, source string) {
	if re == nil {
		return
	}
	ruleSources[re] = source
}

// RuleSource returns the JavaScript source of a compiled expression, the way
// marked's `RegExp.source` returns it.
func RuleSource(re *regexp2.Regexp) string {
	if re == nil {
		return ""
	}
	if source, ok := ruleSources[re]; ok {
		return source
	}
	return re.String()
}

// GrammarSources returns the JavaScript source of every block and inline
// expression of every mode, keyed the way the sources are keyed in the
// upstream test data.
func GrammarSources() map[string]map[string]string {
	out := map[string]map[string]string{}
	for mode, rules := range block {
		out["block."+mode] = blockSources(rules)
	}
	for mode, rules := range inline {
		out["inline."+mode] = inlineSources(rules)
	}
	return out
}

// OtherSources returns the JavaScript source of every helper expression of
// marked's `other` export.
func OtherSources() map[string]string {
	return map[string]string{
		"codeRemoveIndent":         RuleSource(other.CodeRemoveIndent),
		"outputLinkReplace":        RuleSource(other.OutputLinkReplace),
		"indentCodeCompensation":   RuleSource(other.IndentCodeCompensation),
		"beginningSpace":           RuleSource(other.BeginningSpace),
		"endingHash":               RuleSource(other.EndingHash),
		"startingSpaceChar":        RuleSource(other.StartingSpaceChar),
		"endingSpaceChar":          RuleSource(other.EndingSpaceChar),
		"nonSpaceChar":             RuleSource(other.NonSpaceChar),
		"newLineCharGlobal":        RuleSource(other.NewLineCharGlobal),
		"tabCharGlobal":            RuleSource(other.TabCharGlobal),
		"multipleSpaceGlobal":      RuleSource(other.MultipleSpaceGlobal),
		"blankLine":                RuleSource(other.BlankLine),
		"doubleBlankLine":          RuleSource(other.DoubleBlankLine),
		"blockquoteStart":          RuleSource(other.BlockquoteStart),
		"blockquoteSetextReplace":  RuleSource(other.BlockquoteSetextReplace),
		"blockquoteSetextReplace2": RuleSource(other.BlockquoteSetextReplace2),
		"listReplaceNesting":       RuleSource(other.ListReplaceNesting),
		"listIsTask":               RuleSource(other.ListIsTask),
		"listReplaceTask":          RuleSource(other.ListReplaceTask),
		"listTaskCheckbox":         RuleSource(other.ListTaskCheckbox),
		"anyLine":                  RuleSource(other.AnyLine),
		"hrefBrackets":             RuleSource(other.HrefBrackets),
		"tableDelimiter":           RuleSource(other.TableDelimiter),
		"tableAlignChars":          RuleSource(other.TableAlignChars),
		"tableRowBlankLine":        RuleSource(other.TableRowBlankLine),
		"tableAlignRight":          RuleSource(other.TableAlignRight),
		"tableAlignCenter":         RuleSource(other.TableAlignCenter),
		"tableAlignLeft":           RuleSource(other.TableAlignLeft),
		"startATag":                RuleSource(other.StartATag),
		"endATag":                  RuleSource(other.EndATag),
		"startPreScriptTag":        RuleSource(other.StartPreScriptTag),
		"endPreScriptTag":          RuleSource(other.EndPreScriptTag),
		"startAngleBracket":        RuleSource(other.StartAngleBracket),
		"endAngleBracket":          RuleSource(other.EndAngleBracket),
		"pedanticHrefTitle":        RuleSource(other.PedanticHrefTitle),
		"unicodeAlphaNumeric":      RuleSource(other.UnicodeAlphaNumeric),
		"escapeTest":               RuleSource(other.EscapeTest),
		"escapeReplace":            RuleSource(other.EscapeReplace),
		"escapeTestNoEncode":       RuleSource(other.EscapeTestNoEncode),
		"escapeReplaceNoEncode":    RuleSource(other.EscapeReplaceNoEncode),
		"caret":                    RuleSource(other.Caret),
		"percentDecode":            RuleSource(other.PercentDecode),
		"findPipe":                 RuleSource(other.FindPipe),
		"splitPipe":                RuleSource(other.SplitPipe),
		"slashPipe":                RuleSource(other.SlashPipe),
		"carriageReturn":           RuleSource(other.CarriageReturn),
		"spaceLine":                RuleSource(other.SpaceLine),
		"notSpaceStart":            RuleSource(other.NotSpaceStart),
		"endingNewline":            RuleSource(other.EndingNewline),
	}
}

// blockSources returns the source of every block expression of one mode.
func blockSources(rules BlockRules) map[string]string {
	return map[string]string{
		"newline":    RuleSource(rules.Newline),
		"code":       RuleSource(rules.Code),
		"fences":     RuleSource(rules.Fences),
		"hr":         RuleSource(rules.Hr),
		"heading":    RuleSource(rules.Heading),
		"blockquote": RuleSource(rules.Blockquote),
		"list":       RuleSource(rules.List),
		"html":       RuleSource(rules.HTML),
		"def":        RuleSource(rules.Def),
		"table":      RuleSource(rules.Table),
		"lheading":   RuleSource(rules.LHeading),
		"paragraph":  RuleSource(rules.Paragraph),
		"text":       RuleSource(rules.Text),
	}
}

// inlineSources returns the source of every inline expression of one mode.
func inlineSources(rules InlineRules) map[string]string {
	return map[string]string{
		"_backpedal":        RuleSource(rules.Backpedal),
		"anyPunctuation":    RuleSource(rules.AnyPunctuation),
		"autolink":          RuleSource(rules.Autolink),
		"blockSkip":         RuleSource(rules.BlockSkip),
		"br":                RuleSource(rules.Br),
		"code":              RuleSource(rules.Code),
		"del":               RuleSource(rules.Del),
		"delLDelim":         RuleSource(rules.DelLDelim),
		"delRDelim":         RuleSource(rules.DelRDelim),
		"emStrongLDelim":    RuleSource(rules.EmStrongLDelim),
		"emStrongRDelimAst": RuleSource(rules.EmStrongRDelimAst),
		"emStrongRDelimUnd": RuleSource(rules.EmStrongRDelimUnd),
		"escape":            RuleSource(rules.Escape),
		"link":              RuleSource(rules.Link),
		"nolink":            RuleSource(rules.Nolink),
		"punctuation":       RuleSource(rules.Punctuation),
		"reflink":           RuleSource(rules.Reflink),
		"reflinkSearch":     RuleSource(rules.ReflinkSearch),
		"tag":               RuleSource(rules.Tag),
		"text":              RuleSource(rules.Text),
		"url":               RuleSource(rules.URL),
	}
}

// SortedKeys returns the keys of a map in a stable order.
func SortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
