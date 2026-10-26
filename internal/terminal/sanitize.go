package terminal

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// Sanitize untrusted output for terminal display (Step 82/91).
// Strips ANSI CSI (...m, ...K etc.), OSC sequences (terminated by BEL or ST),
// and C0 controls except \n and \t. Respects NO_COLOR at the render layer
// (color is never emitted by psi in the first release).

func Sanitize(s string) string {
	var sb strings.Builder
	sb.Grow(len(s))
	i := 0
	for i < len(s) {
		c := s[i]
		if c == 0x1b { // ESC
			if i+1 < len(s) && s[i+1] == '[' {
				// CSI: ESC [ params final
				j := i + 2
				for j < len(s) && !isCSIFinal(s[j]) {
					j++
				}
				if j < len(s) {
					j++ // consume final
				}
				i = j
				continue
			}
			if i+1 < len(s) && s[i+1] == ']' {
				// OSC: ESC ] ... (BEL | ESC \)
				j := i + 2
				for j < len(s) {
					if s[j] == 0x07 {
						j++
						break
					}
					if s[j] == 0x1b && j+1 < len(s) && s[j+1] == '\\' {
						j += 2
						break
					}
					j++
				}
				i = j
				continue
			}
			// Lone ESC or other sequence: drop.
			i++
			continue
		}
		if c < 0x20 && c != '\n' && c != '\t' {
			i++
			continue
		}
		// Copy one rune.
		_, size := utf8.DecodeRuneInString(s[i:])
		if size <= 0 {
			i++
			continue
		}
		// Strip unicode bidi isolates and other invisible formatting that can
		// hide malicious text (keep ZWJ for emoji sequences, keep variation selectors off? conservative: drop bidi controls).
		r, _ := utf8.DecodeRuneInString(s[i:])
		if isBidiControl(r) {
			i += size
			continue
		}
		sb.WriteString(s[i : i+size])
		i += size
	}
	return sb.String()
}

func isCSIFinal(b byte) bool { return b >= 0x40 && b <= 0x7E }

func isBidiControl(r rune) bool {
	switch r {
	case 0x202A, 0x202B, 0x202C, 0x202D, 0x202E, 0x2066, 0x2067, 0x2068, 0x2069, 0x200E, 0x200F, 0x061C:
		return true
	}
	return false
}

// HasEscape reports leftover control sequences (acceptance assertion).
func HasEscape(s string) bool {
	if strings.Contains(s, "\x1b[") || strings.Contains(s, "\x1b]") {
		return true
	}
	for _, r := range s {
		if r < 0x20 && r != '\n' && r != '\t' {
			return true
		}
		if unicode.Is(unicode.Cf, r) && isBidiControl(r) {
			return true
		}
	}
	return false
}

// ForbiddenSequences for M6 acceptance: alternate screen, mouse, inline images, diffs.
var ForbiddenSequences = []string{
	"\x1b[?1049h", "\x1b[?1049l", // alternate screen
	"\x1b[?1000h", "\x1b[?1002h", "\x1b[?1006h", // mouse
	"\x1b]1337;", "\x1b]136976;", // inline images (iTerm/kitty)
	"\x1b[?2026h", // synchronized output (redraw framework; psi streams without it)
}
