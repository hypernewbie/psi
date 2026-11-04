package marked

import (
	"strings"

	"github.com/dlclark/regexp2"
)

// JavaScript string semantics. marked works on JavaScript strings, which are
// sequences of UTF-16 code units, and the port has to cut and count them the
// same way.

// jsTrimSpace is the set of code points JavaScript's trim removes.
const jsTrimSpace = " \t\n\v\f\r\u00a0\u1680\u2000\u2001\u2002\u2003\u2004\u2005\u2006\u2007\u2008\u2009\u200a\u2028\u2029\u202f\u205f\u3000\ufeff"

// jsTrim removes the whitespace at both ends of a string.
func jsTrim(src string) string {
	return strings.Trim(src, jsTrimSpace)
}

// jsTrimStart removes the whitespace at the start of a string.
func jsTrimStart(src string) string {
	return strings.TrimLeft(src, jsTrimSpace)
}

// jsTrimEnd removes the whitespace at the end of a string.
func jsTrimEnd(src string) string {
	return strings.TrimRight(src, jsTrimSpace)
}

// substring returns the string after the first n UTF-16 code units, like
// JavaScript's `str.substring(n)`.
func substring(src string, n int) string {
	if n <= 0 {
		return src
	}
	units := 0
	for byteIndex, r := range src {
		if units >= n {
			return src[byteIndex:]
		}
		units++
		if r > 0xFFFF {
			units++
		}
	}
	return ""
}

// cutRange returns the part of a string between two UTF-16 offsets, like
// JavaScript's `str.slice(start, end)`.
func cutRange(src string, start int, end int) string {
	if start < 0 {
		start = 0
	}
	if end > utf16Len(src) {
		end = utf16Len(src)
	}
	if end <= start {
		return ""
	}
	return substring(cutPrefix(src, end), start)
}

// jsSlice returns the part of a string between two UTF-16 offsets, like
// JavaScript's `str.slice(start, end)`. A negative offset counts from the end
// of the string, and an offset past the end is clamped to the end.
func jsSlice(src string, start int, end int) string {
	length := utf16Len(src)
	if start < 0 {
		start += length
		if start < 0 {
			start = 0
		}
	}
	if end < 0 {
		end += length
		if end < 0 {
			end = 0
		}
	}
	if start > length {
		start = length
	}
	if end > length {
		end = length
	}
	if end <= start {
		return ""
	}
	return substring(cutPrefix(src, end), start)
}

// lastIndexOfUnit returns the UTF-16 offset of the last occurrence of a
// substring, like JavaScript's `str.lastIndexOf`.
func lastIndexOfUnit(src string, needle string) int {
	byteIndex := strings.LastIndex(src, needle)
	if byteIndex < 0 {
		return -1
	}
	return utf16Len(src[:byteIndex])
}

// indexOfUnit returns the UTF-16 offset of a substring, like JavaScript's
// `str.indexOf`.
func indexOfUnit(src string, needle string) int {
	byteIndex := strings.Index(src, needle)
	if byteIndex < 0 {
		return -1
	}
	return utf16Len(src[:byteIndex])
}

// replaceGroup replaces every match of an expression with the text of one of
// its groups, like JavaScript's `str.replace(re, '$1')`.
func replaceGroup(re *regexp2.Regexp, src string, group int) string {
	return replaceAllRegex(re, src, func(found *match, _ int) string {
		return found.group(group)
	})
}
