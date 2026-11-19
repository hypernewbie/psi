package marked

import (
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/dlclark/regexp2"
)

// The CJK friendly tokenizer of marked-cjk-friendly 0.1.2, ported from
// marked-cjk-friendly/src/index.ts.
//
// The extension makes the emphasis and strikethrough delimiters work next to
// CJK characters, which CommonMark does not treat as punctuation.

// cjkConstant is the CJK character class of marked-cjk-friendly 0.1.2.
const cjkConstant = `\u1100-\u11ff\u20a9\u2329-\u232a\u2630-\u2637\u268a-\u268f\u2e80-\u2e99\u2e9b-\u2ef3\u2f00-\u2fd5\u2ff0-\u303e\u3041-\u3096\u3099-\u30ff\u3105-\u312f\u3131-\u318e\u3190-\u31e5\u31ef-\u321e\u3220-\u3247\u3250-\ua48c\ua490-\ua4c6\ua960-\ua97c\uac00-\ud7a3\ud7b0-\ud7c6\ud7cb-\ud7fb\uf900-\ufaff\ufe10-\ufe19\ufe30-\ufe52\ufe54-\ufe66\ufe68-\ufe6b\uff01-\uffbe\uffc2-\uffc7\uffca-\uffcf\uffd2-\uffd7\uffda-\uffdc\uffe0-\uffe6\uffe8-\uffee\u{16fe0}-\u{16fe4}\u{16ff0}-\u{16ff6}\u{17000}-\u{18cda}\u{18cff}-\u{18d20}\u{18d80}-\u{18df2}\u{18e00}-\u{19191}\u{191a0}-\u{191d2}\u{1aff0}-\u{1aff3}\u{1aff5}-\u{1affb}\u{1affd}-\u{1affe}\u{1b000}-\u{1b128}\u{1b132}\u{1b150}-\u{1b152}\u{1b155}\u{1b164}-\u{1b168}\u{1b170}-\u{1b2fb}\u{1d300}-\u{1d356}\u{1d360}-\u{1d376}\u{1f1ae}\u{1f200}\u{1f202}\u{1f210}-\u{1f219}\u{1f21b}-\u{1f22e}\u{1f230}-\u{1f231}\u{1f237}\u{1f23b}\u{1f240}-\u{1f248}\u{1f260}-\u{1f265}\u{1f7da}\u{20000}-\u{3fffd}`

// The character classes that the extension builds from the CJK constant.
const (
	// cjkPunctSource matches punctuation, symbols and CJK characters.
	cjkPunctSource = `[` + cjkConstant + `\p{P}\p{S}]`
	// cjkPunctSpaceSource matches whitespace, punctuation, symbols and CJK
	// characters.
	cjkPunctSpaceSource = `[` + cjkConstant + `\s\p{P}\p{S}]`
	// cjkNotPunctSpaceSource matches everything that is not whitespace, not
	// punctuation, not a symbol and not a CJK character.
	cjkNotPunctSpaceSource = `[^` + cjkConstant + `\s\p{P}\p{S}]`
	// cjkDelPunctSource matches punctuation, symbols and CJK characters, but
	// not the emphasis markers "*" and "_".
	cjkDelPunctSource = `(?![*_])[` + cjkConstant + `\p{P}\p{S}]`
	// cjkDelPunctSpaceSource matches whitespace, punctuation, symbols and CJK
	// characters, but not the emphasis markers "*" and "_".
	cjkDelPunctSpaceSource = `(?![*_])[` + cjkConstant + `\s\p{P}\p{S}]`
	// cjkDelNotPunctSpaceSource matches everything that is not whitespace,
	// not punctuation, not a symbol and not a CJK character, plus the
	// emphasis markers "*" and "_".
	cjkDelNotPunctSpaceSource = `(?:[^` + cjkConstant + `\s\p{P}\p{S}]|[*_])`
	// cjkPunctGfmSource matches punctuation, symbols and CJK characters, but
	// not the strikethrough marker "~".
	cjkPunctGfmSource = `(?!~)[` + cjkConstant + `\p{P}\p{S}]`
	// cjkPunctSpaceGfmSource matches whitespace, punctuation, symbols and CJK
	// characters, but not the strikethrough marker "~".
	cjkPunctSpaceGfmSource = `(?!~)[` + cjkConstant + `\s\p{P}\p{S}]`
	// cjkNotPunctSpaceGfmSource matches everything that is not whitespace,
	// not punctuation, not a symbol and not a CJK character, plus the
	// strikethrough marker "~".
	cjkNotPunctSpaceGfmSource = `(?:[^` + cjkConstant + `\s\p{P}\p{S}]|~)`
	// punctuationCjkSource matches the first character of a string when it is
	// whitespace, punctuation, a symbol or a CJK character, and is neither
	// "*" nor "_".
	punctuationCjkSource = `^((?![*_])[` + cjkConstant + `\s\p{P}\p{S}])`
)

// punctuationCjk matches the first character of a string, like the
// `punctuationCjk` expression of the extension.
var punctuationCjk = mustRegex(punctuationCjkSource, flagNone)

// The right delimiter expressions of the extension.
var (
	// emStrongRDelimAstCjk finds the right delimiter of an emphasis span
	// that is opened with "*".
	emStrongRDelimAstCjk = buildRDelimAst(cjkPunctSource, cjkPunctSpaceSource, cjkNotPunctSpaceSource)
	// emStrongRDelimAstCjkGfm finds the right delimiter of an emphasis span
	// that is opened with "*", in the GFM flavour.
	emStrongRDelimAstCjkGfm = buildRDelimAst(cjkPunctGfmSource, cjkPunctSpaceGfmSource, cjkNotPunctSpaceGfmSource)
	// emStrongRDelimUndCjk finds the right delimiter of an emphasis span
	// that is opened with "_".
	emStrongRDelimUndCjk = buildRDelimUnd(cjkPunctSource, cjkPunctSpaceSource, cjkNotPunctSpaceSource)
	// delRDelimCjk finds the right delimiter of a strikethrough span.
	delRDelimCjk = buildRDelimDel(cjkDelPunctSource, cjkDelPunctSpaceSource, cjkDelNotPunctSpaceSource)
)

// buildRDelimAst builds the right delimiter expression of an emphasis span
// that is opened with "*", like marked-cjk-friendly's `buildRDelimAst`.
func buildRDelimAst(punct string, punctSpace string, notPunctSpace string) *regexp2.Regexp {
	source := "^[^_*]*?__[^_*]*?\\*[^_*]*?(?=__)" +
		"|[^*]+(?=[^*])" +
		"|(?!\\*)" + punct + `(\*+)(?=[\s]|$)` +
		"|" + notPunctSpace + `(\*+)(?!\*)(?=` + punctSpace + "|$)" +
		"|(?!\\*)" + punctSpace + `(\*+)(?=` + notPunctSpace + ")" +
		"|[\\s](\\*+)(?!\\*)(?=" + punct + ")" +
		"|(?!\\*)" + punct + `(\*+)(?!\*)(?=` + punct + ")" +
		"|" + notPunctSpace + `(\*+)(?=` + notPunctSpace + ")"
	return mustRegex(source, flagNone)
}

// buildRDelimUnd builds the right delimiter expression of an emphasis span
// that is opened with "_", like marked-cjk-friendly's `buildRDelimUnd`.
func buildRDelimUnd(punct string, punctSpace string, notPunctSpace string) *regexp2.Regexp {
	source := "^[^_*]*?\\*\\*[^_*]*?_[^_*]*?(?=\\*\\*)" +
		"|[^_]+(?=[^_])" +
		"|(?!_)" + punct + `(_+)(?=[\s]|$)` +
		"|" + notPunctSpace + `(_+)(?!_)(?=` + punctSpace + "|$)" +
		"|(?!_)" + punctSpace + `(_+)(?=` + notPunctSpace + ")" +
		"|[\\s](_+)(?!_)(?=" + punct + ")" +
		"|(?!_)" + punct + `(_+)(?!_)(?=` + punct + ")"
	return mustRegex(source, flagNone)
}

// buildRDelimDel builds the right delimiter expression of a strikethrough
// span, like marked-cjk-friendly's `buildRDelimDel`.
func buildRDelimDel(punct string, punctSpace string, notPunctSpace string) *regexp2.Regexp {
	source := "^[^~]+(?=[^~])" +
		"|(?!~)" + punct + `(~~?)(?=[\s]|$)` +
		"|" + notPunctSpace + `(~~?)(?!~)(?=` + punctSpace + "|$)" +
		"|(?!~)" + punctSpace + `(~~?)(?=` + notPunctSpace + ")" +
		"|[\\s](~~?)(?!~)(?=" + punct + ")" +
		"|(?!~)" + punct + `(~~?)(?!~)(?=` + punct + ")" +
		"|" + notPunctSpace + `(~~?)(?=` + notPunctSpace + ")"
	return mustRegex(source, flagNone)
}

// CjkFriendly returns the tokenizer methods of marked-cjk-friendly 0.1.2, so
// that they can be registered on a marked instance.
func CjkFriendly() *TokenizerOverrides {
	return &TokenizerOverrides{
		EmStrong: cjkEmStrong,
		Del:      cjkDel,
	}
}

// cjkEmStrong is the CJK friendly `emStrong` tokenizer of
// marked-cjk-friendly 0.1.2.
func cjkEmStrong(t *Tokenizer, src string, maskedSrc string, prevChar string) (*Token, bool) {
	rules := t.Rules
	match := exec(rules.Inline.EmStrongLDelim, src)
	if match == nil {
		return nil, false
	}
	modernEmStrongLayout := usesModernEmStrongLayout(match)
	if modernEmStrongLayout && !hasEmStrongLeadContent(match) {
		return nil, true
	}
	if isEmStrongUnderscoreWordChar(match) && matches(rules.Other.UnicodeAlphaNumeric, prevChar) {
		return nil, false
	}
	nextChar := getEmStrongOpeningPunctuation(match)
	prevIsCjk := isPrevCharCjk(prevChar, maskedSrc, src)
	if nextChar == "" || prevChar == "" || exec(rules.Inline.Punctuation, prevChar) != nil || prevIsCjk || isCjk(nextChar) {
		lLength := runeLen(match.whole()) - 1
		rLength := 0
		delimTotal := lLength
		midDelimTotal := 0
		prevCharIsSameDelimiter := modernEmStrongLayout && prevChar == firstUnit(match.whole())
		isGfm := strings.Contains(RuleSource(rules.Inline.EmStrongRDelimAst), "(?!~)")
		var endReg *regexp2.Regexp
		if firstUnit(match.whole()) == "*" {
			if isGfm {
				endReg = emStrongRDelimAstCjkGfm
			} else {
				endReg = emStrongRDelimAstCjk
			}
		} else {
			endReg = emStrongRDelimUndCjk
		}
		clippedMaskedSrc := cutSuffix(maskedSrc, utf16Len(src)-lLength)
		for _, rMatch := range execAll(endReg, clippedMaskedSrc) {
			rDelim := firstGroup(rMatch, 1, 2, 3, 4, 5, 6)
			if rDelim == "" {
				continue
			}
			rLength = runeLen(rDelim)
			isCjkAdjacent := isCjkAdjacentToDelimiter(rMatch, clippedMaskedSrc)
			isLeftOnly := rMatch.group(3) != "" || rMatch.group(4) != ""
			isBoth := rMatch.group(5) != "" || rMatch.group(6) != ""
			if isLeftOnly && !isCjkAdjacent {
				delimTotal += rLength
				continue
			}
			if isBoth || isCjkAdjacent {
				if lLength%3 != 0 && (lLength+rLength)%3 == 0 {
					midDelimTotal += rLength
					continue
				}
				if prevCharIsSameDelimiter && isBoth {
					break
				}
			}
			delimTotal -= rLength
			if delimTotal > 0 {
				continue
			}
			rLength = min(rLength, rLength+delimTotal+midDelimTotal)
			lastCharLength := firstCodePointLen(rMatch.whole())
			raw := cutPrefix(src, lLength+rMatch.index+lastCharLength+rLength)
			if min(lLength, rLength)%2 == 1 {
				text := jsSlice(raw, 1, -1)
				return &Token{
					Type:   TypeEm,
					Raw:    raw,
					Text:   text,
					Tokens: t.Lexer.inlineTokens(text, nil),
				}, true
			}
			text := jsSlice(raw, 2, -2)
			return &Token{
				Type:   TypeStrong,
				Raw:    raw,
				Text:   text,
				Tokens: t.Lexer.inlineTokens(text, nil),
			}, true
		}
	}
	return nil, true
}

// cjkDel is the CJK friendly `del` tokenizer of marked-cjk-friendly 0.1.2.
func cjkDel(t *Tokenizer, src string, maskedSrc string, prevChar string) (*Token, bool) {
	rules := t.Rules
	match := exec(rules.Inline.DelLDelim, src)
	if match == nil {
		return nil, false
	}
	nextChar := match.group(1)
	prevIsCjk := isPrevCharCjk(prevChar, maskedSrc, src)
	if nextChar == "" || prevChar == "" || exec(rules.Inline.Punctuation, prevChar) != nil || prevIsCjk || isCjk(nextChar) {
		lLength := runeLen(match.whole()) - 1
		rLength := 0
		delimTotal := lLength
		clippedMaskedSrc := cutSuffix(maskedSrc, utf16Len(src)-lLength)
		for _, rMatch := range execAll(delRDelimCjk, clippedMaskedSrc) {
			rDelim := firstGroup(rMatch, 1, 2, 3, 4, 5, 6)
			if rDelim == "" {
				continue
			}
			rLength = runeLen(rDelim)
			if rLength != lLength {
				continue
			}
			isCjkAdjacent := isCjkAdjacentToDelimiter(rMatch, clippedMaskedSrc)
			if (rMatch.group(3) != "" || rMatch.group(4) != "") && !isCjkAdjacent {
				delimTotal += rLength
				continue
			}
			delimTotal -= rLength
			if delimTotal > 0 {
				continue
			}
			rLength = min(rLength, rLength+delimTotal)
			lastCharLength := firstCodePointLen(rMatch.whole())
			raw := cutPrefix(src, lLength+rMatch.index+lastCharLength+rLength)
			text := jsSlice(raw, lLength, -lLength)
			return &Token{
				Type:   TypeDel,
				Raw:    raw,
				Text:   text,
				Tokens: t.Lexer.inlineTokens(text, nil),
			}, true
		}
	}
	return nil, true
}

// firstGroup returns the first group of a match that is not empty, like
// JavaScript's `a || b || c`.
func firstGroup(found *match, groups ...int) string {
	for _, group := range groups {
		if value := found.group(group); value != "" {
			return value
		}
	}
	return ""
}

// usesModernEmStrongLayout reports whether the match holds the groups of the
// modern em and strong layout.
func usesModernEmStrongLayout(found *match) bool {
	return len(found.groups) >= 5
}

// hasEmStrongLeadContent reports whether the match holds content in front of
// the delimiter.
func hasEmStrongLeadContent(found *match) bool {
	return found.group(1) != "" || found.group(2) != "" ||
		found.group(3) != "" || found.group(4) != ""
}

// isEmStrongUnderscoreWordChar reports whether the match holds a word
// character behind an underscore delimiter.
func isEmStrongUnderscoreWordChar(found *match) bool {
	if usesModernEmStrongLayout(found) {
		return found.group(4) != ""
	}
	return found.group(3) != ""
}

// getEmStrongOpeningPunctuation returns the punctuation that opens the
// delimiter.
func getEmStrongOpeningPunctuation(found *match) string {
	if usesModernEmStrongLayout(found) {
		return firstGroup(found, 1, 3)
	}
	return firstGroup(found, 1, 2)
}

// isPrevCharCjk reports whether the character in front of the source is CJK.
//
// Marked passes `prevChar` as a single UTF-16 code unit, so supplementary
// plane characters may arrive as a lone surrogate and variation sequences may
// point at the variation selector instead of the base character. The port
// recovers the character from `maskedSrc` the same way the extension does.
func isPrevCharCjk(prevChar string, maskedSrc string, src string) bool {
	prevIsCjk := matches(punctuationCjk, prevChar)
	if !prevIsCjk && prevChar != "" {
		prevIdx := utf16Len(maskedSrc) - utf16Len(src)
		if prevIdx >= 1 {
			idx := prevIdx - 1
			code := codeUnitAt(maskedSrc, idx)
			if code >= 65024 && code <= 65038 && idx >= 1 {
				idx--
				code = codeUnitAt(maskedSrc, idx)
			}
			if (code&64512) == 56320 && idx >= 1 {
				cp := codePointAt(maskedSrc, idx-1)
				if cp > 65535 {
					if cp >= 917760 && cp <= 917999 {
						prevIsCjk = true
					} else {
						prevIsCjk = isCjkCodePoint(cp)
					}
				}
			} else {
				prevIsCjk = isCjkCodePoint(code)
			}
		}
	}
	return prevIsCjk
}

// isCjkAdjacentToDelimiter reports whether a matched right delimiter stands
// next to CJK text.
func isCjkAdjacentToDelimiter(rightMatch *match, clippedMaskedSrc string) bool {
	if rightMatch.group(1) == "" && rightMatch.group(2) == "" &&
		rightMatch.group(3) == "" && rightMatch.group(4) == "" {
		return false
	}
	charBefore := firstCodePoint(rightMatch.whole())
	afterPos := rightMatch.index + utf16Len(rightMatch.whole())
	charAfter := 0
	if afterPos < utf16Len(clippedMaskedSrc) {
		charAfter = codePointAt(clippedMaskedSrc, afterPos)
	}
	return isCjkCodePoint(charBefore) || isCjkCodePoint(charAfter)
}

// codeUnitAt returns the UTF-16 code unit of a string at an offset, like
// JavaScript's `str.charCodeAt`.
func codeUnitAt(src string, index int) int {
	if index < 0 {
		return 0
	}
	units := 0
	for _, r := range src {
		if units == index {
			if r > 0xFFFF {
				return int(0xD800 + ((r - 0x10000) >> 10))
			}
			return int(r)
		}
		units++
		if r > 0xFFFF {
			units++
		}
	}
	return 0
}

// codePointAt returns the code point of a string that starts at a UTF-16
// offset, like JavaScript's `str.codePointAt`.
func codePointAt(src string, index int) int {
	if index < 0 {
		return 0
	}
	units := 0
	for _, r := range src {
		if units == index {
			return int(r)
		}
		units++
		if r > 0xFFFF {
			units++
		}
	}
	return 0
}

// firstCodePoint returns the first code point of a string, like JavaScript's
// `String.fromCodePoint(str.codePointAt(0))`.
func firstCodePoint(src string) int {
	if src == "" {
		return 0
	}
	r, _ := utf8.DecodeRuneInString(src)
	return int(r)
}

// cjkRange is a range of CJK code points.
type cjkRange struct {
	lo int
	hi int
}

// cjkRanges holds the code point ranges of the CJK constant.
var cjkRanges = []cjkRange{
	{0x1100, 0x11FF},
	{0x20A9, 0x20A9},
	{0x2329, 0x232A},
	{0x2630, 0x2637},
	{0x268A, 0x268F},
	{0x2E80, 0x2E99},
	{0x2E9B, 0x2EF3},
	{0x2F00, 0x2FD5},
	{0x2FF0, 0x303E},
	{0x3041, 0x3096},
	{0x3099, 0x30FF},
	{0x3105, 0x312F},
	{0x3131, 0x318E},
	{0x3190, 0x31E5},
	{0x31EF, 0x321E},
	{0x3220, 0x3247},
	{0x3250, 0xA48C},
	{0xA490, 0xA4C6},
	{0xA960, 0xA97C},
	{0xAC00, 0xD7A3},
	{0xD7B0, 0xD7C6},
	{0xD7CB, 0xD7FB},
	{0xF900, 0xFAFF},
	{0xFE10, 0xFE19},
	{0xFE30, 0xFE52},
	{0xFE54, 0xFE66},
	{0xFE68, 0xFE6B},
	{0xFF01, 0xFFBE},
	{0xFFC2, 0xFFC7},
	{0xFFCA, 0xFFCF},
	{0xFFD2, 0xFFD7},
	{0xFFDA, 0xFFDC},
	{0xFFE0, 0xFFE6},
	{0xFFE8, 0xFFEE},
	{0x16FE0, 0x16FE4},
	{0x16FF0, 0x16FF6},
	{0x17000, 0x18CDA},
	{0x18CFF, 0x18D20},
	{0x18D80, 0x18DF2},
	{0x18E00, 0x19191},
	{0x191A0, 0x191D2},
	{0x1AFF0, 0x1AFF3},
	{0x1AFF5, 0x1AFFB},
	{0x1AFFD, 0x1AFFE},
	{0x1B000, 0x1B128},
	{0x1B132, 0x1B132},
	{0x1B150, 0x1B152},
	{0x1B155, 0x1B155},
	{0x1B164, 0x1B168},
	{0x1B170, 0x1B2FB},
	{0x1D300, 0x1D356},
	{0x1D360, 0x1D376},
	{0x1F1AE, 0x1F1AE},
	{0x1F200, 0x1F200},
	{0x1F202, 0x1F202},
	{0x1F210, 0x1F219},
	{0x1F21B, 0x1F22E},
	{0x1F230, 0x1F231},
	{0x1F237, 0x1F237},
	{0x1F23B, 0x1F23B},
	{0x1F240, 0x1F248},
	{0x1F260, 0x1F265},
	{0x1F7DA, 0x1F7DA},
	{0x20000, 0x3FFFD},
}

// isCjkCodePoint reports whether a code point is a CJK character.
func isCjkCodePoint(code int) bool {
	if code <= 0 {
		return false
	}
	for _, r := range cjkRanges {
		if code < r.lo {
			return false
		}
		if code <= r.hi {
			return true
		}
	}
	return false
}

// isCjk reports whether the first character of a string is a CJK character.
func isCjk(src string) bool {
	if src == "" {
		return false
	}
	r, _ := utf8.DecodeRuneInString(src)
	return isCjkCodePoint(int(r))
}

// parseCjkConstant parses the code point ranges of the CJK constant.
func parseCjkConstant(source string) []cjkRange {
	var out []cjkRange
	for i := 0; i < len(source); {
		if source[i] != '\\' || i+1 >= len(source) || source[i+1] != 'u' {
			i++
			continue
		}
		i += 2
		lo, width, ok := parseCjkCodePoint(source[i:])
		if !ok {
			break
		}
		i += width
		if i < len(source) && source[i] == '-' {
			i++
			// the end of the range repeats the escape of its start
			if i+1 < len(source) && source[i] == '\\' && source[i+1] == 'u' {
				i += 2
			}
			hi, width, ok := parseCjkCodePoint(source[i:])
			if !ok {
				break
			}
			i += width
			out = append(out, cjkRange{lo: lo, hi: hi})
			continue
		}
		out = append(out, cjkRange{lo: lo, hi: lo})
	}
	return out
}

// parseCjkCodePoint parses one code point of the CJK constant and returns the
// number of characters it holds.
func parseCjkCodePoint(source string) (int, int, bool) {
	if len(source) > 0 && source[0] == '{' {
		end := strings.IndexByte(source, '}')
		if end < 0 {
			return 0, 0, false
		}
		value, err := strconv.ParseInt(source[1:end], 16, 32)
		if err != nil {
			return 0, 0, false
		}
		return int(value), end + 1, true
	}
	if len(source) < 4 {
		return 0, 0, false
	}
	value, err := strconv.ParseInt(source[:4], 16, 32)
	if err != nil {
		return 0, 0, false
	}
	return int(value), 4, true
}
