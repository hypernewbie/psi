package terminal

import (
	"strings"
	"unicode/utf8"
)

// Sanitizer retains escape-sequence state across provider deltas. In particular,
// an OSC split across multiple chunks must not leak its payload into the UI.
type Sanitizer struct {
	state       byte
	pendingUTF8 string
}

func (s *Sanitizer) Feed(text string) string {
	return s.feed(text, false)
}

// foldCR is used only for single-row labels, after escape sequences have been
// consumed. Transcript sanitization retains its existing control policy.
func (s *Sanitizer) feed(text string, foldCR bool) string {
	text = s.pendingUTF8 + text
	s.pendingUTF8 = ""
	var out strings.Builder
	for len(text) > 0 {
		if !utf8.FullRuneInString(text) {
			s.pendingUTF8 = text
			break
		}
		rawByte := text[0]
		r, size := utf8.DecodeRuneInString(text)
		if r == utf8.RuneError && size == 1 && rawByte >= 0x80 && rawByte <= 0x9f {
			r = rune(rawByte)
		}
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
			case 27:
				s.state = 'e'
			default:
				if r >= 0x20 && r <= 0x2f {
					s.state = 'i'
				} else {
					s.state = 0
				}
			}
			continue
		case 'i':
			if r == 27 {
				s.state = 'e'
			} else if r == 0x18 || r == 0x1a {
				s.state = 0
			} else if r >= 0x30 && r <= 0x7e {
				s.state = 0
			}
			continue
		case 'c':
			if r == 27 {
				s.state = 'e'
			} else if r == 0x18 || r == 0x1a {
				s.state = 0
			} else if r >= 0x40 && r <= 0x7e {
				s.state = 0
			}
			continue
		case 'o', 's':
			if r == 0x07 || r == 0x9c || r == 0x18 || r == 0x1a {
				s.state = 0
			} else if r == 27 {
				s.state = 't'
			}
			continue
		case 't':
			switch r {
			case '\\', 0x07, 0x9c, 0x18, 0x1a:
				s.state = 0
			case 27:
				s.state = 't'
			default:
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
		if r == '\r' && foldCR {
			out.WriteByte(' ')
			continue
		}
		if (r < 0x20 && r != '\n' && r != '\t') || (r >= 0x7f && r <= 0x9f) || isBidiControl(r) || isInterlinearAnnotation(r) {
			continue
		}
		if r == utf8.RuneError && size == 1 {
			continue
		}
		out.WriteRune(r)
	}
	return out.String()
}

func Sanitize(text string) string {
	if !requiresSanitization(text) {
		return text
	}
	var s Sanitizer
	return s.Feed(text)
}

func requiresSanitization(text string) bool {
	for i := 0; i < len(text); {
		if text[i] < utf8.RuneSelf {
			b := text[i]
			if b == 27 || b == 0x7f || (b < 0x20 && b != '\n' && b != '\t') {
				return true
			}
			i++
			continue
		}
		r, size := utf8.DecodeRuneInString(text[i:])
		if (r == utf8.RuneError && size == 1) || (r >= 0x7f && r <= 0x9f) || isBidiControl(r) || isInterlinearAnnotation(r) {
			return true
		}
		i += size
	}
	return false
}

func isBidiControl(r rune) bool {
	switch r {
	case 0x202a, 0x202b, 0x202c, 0x202d, 0x202e, 0x2066, 0x2067, 0x2068, 0x2069, 0x200e, 0x200f, 0x061c:
		return true
	}
	return false
}

func isInterlinearAnnotation(r rune) bool {
	return r >= 0xfff9 && r <= 0xfffb
}

func HasEscape(s string) bool { return Sanitize(s) != s }

var ForbiddenSequences = []string{
	"\x1b[?1049h", "\x1b[?1049l",
	"\x1b[?1000h", "\x1b[?1002h", "\x1b[?1006h",
	"\x1b]1337;", "\x1b]136976;", "\x1b[?2026h",
}

// SanitizeStatusRow collapses untrusted single-row labels (F10): tool names
// arrive from provider-supplied calls before enablement checks, so a newline
// bearing name must never become extra physical rows. Transcript
// sanitization is untouched: this applies only at the status boundary.
// Tabs, CR, and LF fold to spaces; remaining C0/C1 and bidi controls drop;
// ANSI/OSC/DCS/C1 sequences never pass through.
func SanitizeStatusRow(s string) string {
	if requiresSanitization(s) {
		var sanitizer Sanitizer
		s = sanitizer.feed(s, true)
	}
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch r {
		case '\n', '\r', '\t':
			b.WriteByte(' ')
		default:
			if r < 0x20 || (r >= 0x7f && r <= 0x9f) || isBidiControl(r) || isInterlinearAnnotation(r) {
				continue
			}
			b.WriteRune(r)
		}
	}
	return b.String()
}
