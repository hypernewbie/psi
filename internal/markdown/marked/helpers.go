package marked

import (
	"strings"
	"unicode/utf8"

	"github.com/dlclark/regexp2"
)

// Helpers, ported from marked/src/helpers.ts.

// escapeReplacements is marked's HTML entity table.
var escapeReplacements = map[string]string{
	"&":  "&amp;",
	"<":  "&lt;",
	">":  "&gt;",
	"\"": "&quot;",
	"'":  "&#39;",
}

// EscapeHtmlEntities escapes HTML entities the way marked does.
func EscapeHtmlEntities(html string, encode bool) string {
	if encode {
		if matches(other.EscapeTest, html) {
			return replaceAll(other.EscapeReplace, html, func(match string) string {
				return escapeReplacements[match]
			})
		}
	} else {
		if matches(other.EscapeTestNoEncode, html) {
			return replaceAll(other.EscapeReplaceNoEncode, html, func(match string) string {
				return escapeReplacements[match]
			})
		}
	}
	return html
}

// SplitCells splits a table row into cells, the way marked does.
func SplitCells(tableRow string, count int) []string {
	// ensure that every cell-delimiting pipe has a space
	// before it to distinguish it from an escaped pipe
	row := replaceAllWithIndex(other.FindPipe, tableRow, func(match string, offset int, str string) string {
		escaped := false
		curr := offset
		for {
			curr--
			if curr < 0 || str[curr] != '\\' {
				break
			}
			escaped = !escaped
		}
		if escaped {
			// odd number of slashes means | is escaped
			// so we leave it alone
			return "|"
		}
		// add space before unescaped |
		return " |"
	})
	cells := strings.Split(row, " |")

	// First/last cell in a row cannot be empty if it has no leading/trailing pipe
	if len(cells) > 0 && strings.TrimSpace(cells[0]) == "" {
		cells = cells[1:]
	}
	if len(cells) > 0 && strings.TrimSpace(cells[len(cells)-1]) == "" {
		cells = cells[:len(cells)-1]
	}

	if count > 0 {
		if len(cells) > count {
			cells = cells[:count]
		} else {
			for len(cells) < count {
				cells = append(cells, "")
			}
		}
	}

	for i := range cells {
		// leading or trailing whitespace is ignored per the gfm spec
		cells[i] = strings.TrimSpace(cells[i])
		cells[i] = replaceAll(other.SlashPipe, cells[i], func(string) string { return "|" })
	}
	return cells
}

// Rtrim removes trailing 'c's. Equivalent to str.replace(/c*$/, ”).
//
// marked counts the suffix in UTF-16 code units, so the port does the same.
func Rtrim(str string, c string, invert bool) string {
	if str == "" {
		return ""
	}

	cut := len(str)
	for cut > 0 {
		r, size := utf8.DecodeLastRuneInString(str[:cut])
		cut -= size
		matched := string(r) == c
		if (matched && !invert) || (!matched && invert) {
			continue
		}
		return str[:cut+size]
	}

	return ""
}

// TrimTrailingBlankLines removes trailing blank lines, keeping a single one.
func TrimTrailingBlankLines(str string) string {
	lines := strings.Split(str, "\n")
	end := len(lines) - 1
	for end >= 0 && matches(other.BlankLine, lines[end]) {
		end--
	}
	if len(lines)-end <= 2 {
		// we want to keep single trailing blank lines
		return str
	}
	return strings.Join(lines[:end+1], "\n")
}

// FindClosingBracket finds the matching closing bracket of str.
func FindClosingBracket(str string, b string) int {
	if !strings.Contains(str, b[1:2]) {
		return -1
	}

	level := 0
	for i := 0; i < len(str); i++ {
		if str[i] == '\\' {
			i++
		} else if str[i] == b[0] {
			level++
		} else if str[i] == b[1] {
			level--
			if level < 0 {
				return i
			}
		}
	}
	if level > 0 {
		return -2
	}
	return -1
}

// ExpandTabs expands the tabs of a line to four column stops.
func ExpandTabs(line string, indent int) string {
	col := indent
	var expanded strings.Builder
	for _, char := range line {
		if char == '\t' {
			added := 4 - (col % 4)
			expanded.WriteString(strings.Repeat(" ", added))
			col += added
		} else {
			expanded.WriteRune(char)
			col++
		}
	}
	return expanded.String()
}

// replaceAll replaces every match of re with the result of repl.
func replaceAll(re *regexp2.Regexp, src string, repl func(string) string) string {
	return replaceAllRegex(re, src, func(found *match, _ int) string {
		return repl(found.whole())
	})
}

// replaceAllWithIndex replaces every match of re with the result of repl.
// The repl callback receives the match and its offset in the source.
func replaceAllWithIndex(re *regexp2.Regexp, src string, repl func(string, int, string) string) string {
	return replaceAllRegex(re, src, func(found *match, offset int) string {
		return repl(found.whole(), offset, src)
	})
}

// replaceAllMatches replaces every match of re with the result of repl. The
// repl callback receives the whole match, so it can read the groups of the
// match the way marked does.
func replaceAllMatches(re *regexp2.Regexp, src string, repl func(*match) string) string {
	return replaceAllRegex(re, src, func(found *match, _ int) string {
		return repl(found)
	})
}
