package terminal

import (
	"unicode"
)

// CellWidth returns display-cell width (not bytes): combining 0, wide/CJK 2,
// emoji 2, default 1. Covers combining, wide, emoji for PTY resize tests.
func CellWidth(s string) int {
	w := 0
	for _, r := range s {
		w += RuneWidth(r)
	}
	return w
}

func RuneWidth(r rune) int {
	if r == 0 || r == 0x200d {
		return 0
	}
	if r >= 0xFE00 && r <= 0xFE0F {
		return 0 // variation selectors
	}
	if unicode.Is(unicode.Mn, r) || unicode.Is(unicode.Me, r) || unicode.Is(unicode.Cc, r) {
		return 0
	}
	if isWide(r) {
		return 2
	}
	return 1
}

func isWide(r rune) bool {
	// East Asian Wide/Fullwidth + emoji ranges (conservative, test-covered).
	if r >= 0x1100 && r <= 0x115F {
		return true
	}
	if r >= 0x2E80 && r <= 0x303E {
		return true
	}
	if r >= 0x3041 && r <= 0x33FF {
		return true
	}
	if r >= 0x3400 && r <= 0x4DBF {
		return true
	}
	if r >= 0x4E00 && r <= 0x9FFF {
		return true
	}
	if r >= 0xA000 && r <= 0xA4CF {
		return true
	}
	if r >= 0xAC00 && r <= 0xD7A3 {
		return true
	}
	if r >= 0xF900 && r <= 0xFAFF {
		return true
	}
	if r >= 0xFE30 && r <= 0xFE4F {
		return true
	}
	if r >= 0xFF00 && r <= 0xFF60 {
		return true
	}
	if r >= 0xFFE0 && r <= 0xFFE6 {
		return true
	}
	if r >= 0x20000 && r <= 0x3FFFD {
		return true
	}
	// Emoji.
	if r >= 0x1F300 && r <= 0x1FAFF {
		return true
	}
	if r >= 0x2600 && r <= 0x27BF {
		return true
	}
	return false
}

// TruncateToWidth cuts s to at most maxCells display cells (no partial wide).
func TruncateToWidth(s string, maxCells int) string {
	if maxCells <= 0 {
		return ""
	}
	w := 0
	i := 0
	for _, r := range s {
		rw := RuneWidth(r)
		if w+rw > maxCells {
			break
		}
		w += rw
		i += len(string(r))
	}
	return s[:i]
}
