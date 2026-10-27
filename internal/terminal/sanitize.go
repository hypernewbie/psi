package terminal

import (
	"strings"
	"unicode/utf8"
)

// Sanitizer retains escape-sequence state across provider deltas. In particular,
// an OSC split across multiple chunks must not leak its payload into the UI.
type Sanitizer struct{ state byte }

func (s *Sanitizer) Feed(text string) string {
	var out strings.Builder
	for len(text) > 0 {
		r, size := utf8.DecodeRuneInString(text)
		text = text[size:]
		switch s.state {
		case 'e':
			switch r {
			case '[':
				s.state = 'c'
			case ']':
				s.state = 'o'
			case 'P', 'X', '^', '_':
				s.state = 's'
			default:
				if r >= 0x20 && r <= 0x2f {
					s.state = 'i'
				} else {
					s.state = 0
				}
			}
			continue
		case 'i':
			if r >= 0x30 && r <= 0x7e {
				s.state = 0
			}
			continue
		case 'c':
			if r >= 0x40 && r <= 0x7e {
				s.state = 0
			}
			continue
		case 'o', 's':
			if r == 0x07 || r == 0x9c {
				s.state = 0
			} else if r == 27 {
				s.state = 't'
			}
			continue
		case 't':
			if r == '\\' {
				s.state = 0
			} else {
				s.state = 's'
			}
			continue
		}
		switch r {
		case 27:
			s.state = 'e'
			continue
		case 0x9b:
			s.state = 'c'
			continue
		case 0x9d:
			s.state = 'o'
			continue
		case 0x90, 0x98, 0x9e, 0x9f:
			s.state = 's'
			continue
		}
		if (r < 0x20 && r != '\n' && r != '\t') || (r >= 0x7f && r <= 0x9f) || isBidiControl(r) {
			continue
		}
		if r == utf8.RuneError && size == 1 {
			continue
		}
		out.WriteRune(r)
	}
	return out.String()
}

func Sanitize(text string) string { var s Sanitizer; return s.Feed(text) }

func isBidiControl(r rune) bool {
	switch r {
	case 0x202a, 0x202b, 0x202c, 0x202d, 0x202e, 0x2066, 0x2067, 0x2068, 0x2069, 0x200e, 0x200f, 0x061c:
		return true
	}
	return false
}

func HasEscape(s string) bool { return Sanitize(s) != s }

var ForbiddenSequences = []string{
	"\x1b[?1049h", "\x1b[?1049l",
	"\x1b[?1000h", "\x1b[?1002h", "\x1b[?1006h",
	"\x1b]1337;", "\x1b]136976;", "\x1b[?2026h",
}
