package marked

import (
	"fmt"
	"regexp"
	"strings"
	"unicode/utf16"

	"github.com/dlclark/regexp2"
)

// The port keeps marked's regular expressions verbatim, but the engine that
// runs them is not JavaScript. Go's standard regexp package cannot express
// the lookahead that marked's grammar needs, so the port uses
// github.com/dlclark/regexp2, which implements the .NET dialect. This file
// holds the small translation layer that makes a JavaScript expression mean the
// same thing in that dialect, plus the helpers that replace the JavaScript
// RegExp API.
//
// The differences that the translation layer covers:
//
//   - JavaScript's \s matches a wider set of spaces than .NET's \s, so \s and
//     \S are replaced by their JavaScript equivalents.
//   - JavaScript's "." excludes \r, U+2028 and U+2029, while .NET's "." only
//     excludes \n.
//   - JavaScript's "$" without the multiline flag matches only at the very
//     end of the input, while .NET's "$" also matches before a final newline,
//     so "$" becomes "\z" for rules that are not multiline.
//   - JavaScript writes code points above U+FFFF as \u{10FFFF}, which .NET
//     does not understand, so those escapes become surrogate pairs.
//   - JavaScript's RegExp.exec returns an array of the match and its groups.
//     The helpers below return the same shape.

// jsWhitespace is the set of code points JavaScript's \s matches.
const jsWhitespace = `\f\n\r\t\v\x20\u00a0\u1680\u2000-\u200a\u2028\u2029\u202f\u205f\u3000\ufeff`

// jsToDotNet translates a JavaScript regular expression source into the .NET
// dialect. The multiline flag mirrors JavaScript's "m" flag.
func jsToDotNet(src string, multiline bool) string {
	src = renameNamedGroups(src)
	var out strings.Builder
	inClass := false
	for i := 0; i < len(src); i++ {
		c := src[i]
		switch {
		case c == '\\' && i+1 < len(src):
			next := src[i+1]
			switch next {
			case 's':
				if inClass {
					out.WriteString(jsWhitespace)
				} else {
					out.WriteString("[" + jsWhitespace + "]")
				}
			case 'S':
				if inClass {
					// A character class cannot hold the complement of a set,
					// so the engine's own escape is used inside a class. The
					// engine and JavaScript agree on the whitespace of a
					// character class for the cases marked uses.
					out.WriteString("\\S")
				} else {
					out.WriteString("[^" + jsWhitespace + "]")
				}
			case 'u':
				if i+2 < len(src) && src[i+2] == '{' {
					end := strings.IndexByte(src[i:], '}')
					if end > 0 {
						code := parseCodePoint(src[i+3 : i+end])
						out.WriteString(surrogatePair(code))
						i += end
						break
					}
				}
				out.WriteByte(c)
				out.WriteByte(next)
			default:
				out.WriteByte(c)
				out.WriteByte(next)
			}
			i++
		case c == '[' && !inClass:
			inClass = true
			out.WriteByte(c)
		case c == ']' && inClass:
			inClass = false
			out.WriteByte(c)
		case c == '.' && !inClass:
			out.WriteString("[^\n\r\u2028\u2029]")
		case c == '$' && !inClass && !multiline:
			out.WriteString(`\z`)
		default:
			out.WriteByte(c)
		}
	}
	return out.String()
}

// renameNamedGroups rewrites the named capture groups of an expression into
// unnamed ones and turns the named backreferences into numbered ones.
//
// JavaScript numbers the groups of an expression by the order of their
// opening parenthesis, so a named group counts towards the number of the
// groups that follow it. The .NET dialect numbers the unnamed groups first
// and the named groups after them. Renaming every group makes both dialects
// number the groups the same way, so the port can read the groups of a match
// with the numbers marked uses.
func renameNamedGroups(src string) string {
	names := map[string]int{}
	count := 0
	inClass := false
	for i := 0; i < len(src); i++ {
		switch c := src[i]; {
		case c == '\\':
			i++
		case inClass:
			if c == ']' {
				inClass = false
			}
		case c == '[':
			inClass = true
		case c == '(':
			name, ok := groupName(src, i)
			if ok {
				count++
				names[name] = count
			} else if !isGroupStart(src, i) {
				count++
			}
		}
	}
	if len(names) == 0 {
		return src
	}

	var out strings.Builder
	inClass = false
	for i := 0; i < len(src); i++ {
		c := src[i]
		switch {
		case c == '\\':
			if reference, ok := namedReference(src, i); ok {
				out.WriteString(`\` + itoa(names[reference]))
				i += len(reference) + 3
				continue
			}
			out.WriteByte(c)
			if i+1 < len(src) {
				out.WriteByte(src[i+1])
				i++
			}
		case inClass:
			out.WriteByte(c)
			if c == ']' {
				inClass = false
			}
		case c == '[':
			inClass = true
			out.WriteByte(c)
		case c == '(':
			if name, ok := groupName(src, i); ok {
				out.WriteByte('(')
				i += len(name) + 3
				continue
			}
			out.WriteByte(c)
		default:
			out.WriteByte(c)
		}
	}
	return out.String()
}

// isGroupStart reports whether the parenthesis at index i opens a group that
// does not capture.
func isGroupStart(src string, i int) bool {
	return i+1 < len(src) && src[i+1] == '?'
}

// groupName returns the name of the named capture group that starts at index
// i, when the parenthesis opens one.
func groupName(src string, i int) (string, bool) {
	if i+3 >= len(src) || src[i+1] != '?' || src[i+2] != '<' {
		return "", false
	}
	if src[i+3] == '=' || src[i+3] == '!' {
		return "", false
	}
	end := i + 3
	for end < len(src) && isGroupNameChar(src[end]) {
		end++
	}
	if end == i+3 || end >= len(src) || src[end] != '>' {
		return "", false
	}
	return src[i+3 : end], true
}

// namedReference returns the name of the backreference that starts at index
// i, when the escape is one.
func namedReference(src string, i int) (string, bool) {
	if i+2 >= len(src) || src[i+1] != 'k' || src[i+2] != '<' {
		return "", false
	}
	end := i + 3
	for end < len(src) && isGroupNameChar(src[end]) {
		end++
	}
	if end == i+3 || end >= len(src) || src[end] != '>' {
		return "", false
	}
	return src[i+3 : end], true
}

// isGroupNameChar reports whether a byte may be part of a group name.
func isGroupNameChar(c byte) bool {
	return c == '_' || c == '$' ||
		(c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')
}

// parseCodePoint parses a hexadecimal code point.
func parseCodePoint(s string) int {
	var value int
	for _, c := range s {
		value <<= 4
		switch {
		case c >= '0' && c <= '9':
			value |= int(c - '0')
		case c >= 'a' && c <= 'f':
			value |= int(c-'a') + 10
		case c >= 'A' && c <= 'F':
			value |= int(c-'A') + 10
		default:
			return value
		}
	}
	return value
}

// surrogatePair writes a code point as the UTF-16 surrogate pair that .NET
// expects inside a character class.
func surrogatePair(code int) string {
	r := rune(code)
	if r > 0xFFFF {
		high, low := utf16.EncodeRune(r)
		return fmt.Sprintf(`\x%04x\x%04x`, high, low)
	}
	return fmt.Sprintf(`\x%04x`, code)
}

// match mirrors the array that JavaScript's RegExp.exec returns. The first
// element holds the whole match, the following elements hold the groups.
type match struct {
	groups []string
	// start and end are the rune offsets of the match in its source.
	start int
	end   int
	// index is the offset of the match in UTF-16 code units, the unit
	// JavaScript counts string offsets in.
	index int
}

// group returns the text of a group, or an empty string when the group did
// not take part in the match.
func (m *match) group(index int) string {
	if m == nil || index >= len(m.groups) {
		return ""
	}
	return m.groups[index]
}

// setGroup replaces the text of a group, the way marked replaces an entry of
// the array that RegExp.exec returns.
func (m *match) setGroup(index int, value string) {
	if m == nil || index >= len(m.groups) {
		return
	}
	m.groups[index] = value
}

// whole returns the text of the whole match.
func (m *match) whole() string {
	return m.group(0)
}

// exec runs an expression the way JavaScript's RegExp.exec does and returns
// the match, or nil when the expression does not match.
func exec(re *regexp2.Regexp, src string) *match {
	if re == nil {
		return nil
	}
	found, err := re.FindStringMatch(src)
	if err != nil || found == nil {
		return nil
	}
	result := &match{groups: matchGroups(*found), start: -1, end: -1, index: -1}
	if groups := found.Groups(); len(groups) > 0 {
		result.start = groups[0].Index
		result.end = groups[0].Index + groups[0].Length
		result.index = utf16Index(src, groups[0].Index)
	}
	return result
}

// matchGroups converts a regexp2 match into the group list of a JavaScript
// match array.
func matchGroups(found regexp2.Match) []string {
	groups := found.Groups()
	out := make([]string, 0, len(groups))
	for _, group := range groups {
		out = append(out, group.String())
	}
	return out
}

// execAll runs an expression the way JavaScript's String.matchAll does.
func execAll(re *regexp2.Regexp, src string) []*match {
	if re == nil {
		return nil
	}
	found, err := re.FindStringMatch(src)
	if err != nil || found == nil {
		return nil
	}
	var out []*match
	for found != nil {
		result := &match{groups: matchGroups(*found), start: -1, end: -1, index: -1}
		if groups := found.Groups(); len(groups) > 0 {
			result.start = groups[0].Index
			result.end = groups[0].Index + groups[0].Length
			result.index = utf16Index(src, groups[0].Index)
		}
		out = append(out, result)
		next, err := re.FindNextMatch(found)
		if err != nil {
			break
		}
		found = next
	}
	return out
}

// matches reports whether an expression matches, the way JavaScript's
// RegExp.test does.
func matches(re *regexp2.Regexp, src string) bool {
	if re == nil {
		return false
	}
	found, err := re.MatchString(src)
	return err == nil && found
}

// replaceAllRegex replaces every match of an expression, the way JavaScript's
// String.replace does with a global expression. The callback receives the
// match and the offset of the match in the source.
func replaceAllRegex(re *regexp2.Regexp, src string, repl func(*match, int) string) string {
	if re == nil {
		return src
	}
	var out strings.Builder
	last := 0
	for _, found := range execAll(re, src) {
		start, end := byteOffsets(src, found)
		out.WriteString(src[last:start])
		out.WriteString(repl(found, start))
		last = end
	}
	out.WriteString(src[last:])
	return out.String()
}

// byteOffsets returns the byte offsets of a match inside its source.
func byteOffsets(src string, found *match) (int, int) {
	if found.start < 0 {
		return 0, 0
	}
	start := runeIndexToByte(src, found.start)
	end := runeIndexToByte(src, found.end)
	return start, end
}

// runeIndexToByte converts a rune offset into a byte offset.
func runeIndexToByte(s string, index int) int {
	if index <= 0 {
		return 0
	}
	count := 0
	for byteIndex := range s {
		if count == index {
			return byteIndex
		}
		count++
	}
	return len(s)
}

// utf16Len returns the length of s in UTF-16 code units, the unit JavaScript
// counts string lengths in.
func utf16Len(s string) int {
	length := 0
	for _, r := range s {
		length++
		if r > 0xFFFF {
			length++
		}
	}
	return length
}

// cutPrefix returns the prefix of s that holds the first n UTF-16 code units.
// JavaScript's String.substring counts UTF-16 code units, so the port has to
// cut at the same place.
func cutPrefix(s string, n int) string {
	if n <= 0 {
		return ""
	}
	units := 0
	for byteIndex, r := range s {
		if units >= n {
			return s[:byteIndex]
		}
		units++
		if r > 0xFFFF {
			units++
		}
	}
	return s
}

// cutSuffix returns the suffix of s that holds its last n UTF-16 code units,
// like JavaScript's `str.slice(-n)`.
func cutSuffix(s string, n int) string {
	if n <= 0 {
		return ""
	}
	total := utf16Len(s)
	if n >= total {
		return s
	}
	return substring(s, total-n)
}

// jsCaret strips the leading "^" of an expression the way marked's edit
// helper does.
var jsCaret = regexp.MustCompile(`(^|[^\[])\^`)

// applyCaret removes the caret of a value that is inserted into a rule.
func applyCaret(value string) string {
	return jsCaret.ReplaceAllString(value, "$1")
}

// utf16Index returns the offset of a rune offset in UTF-16 code units.
func utf16Index(src string, index int) int {
	if index <= 0 {
		return 0
	}
	units := 0
	count := 0
	for _, r := range src {
		if count == index {
			break
		}
		count++
		units++
		if r > 0xFFFF {
			units++
		}
	}
	return units
}
