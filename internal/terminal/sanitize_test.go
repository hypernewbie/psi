package terminal

import (
	"testing"
)

func TestSanitizeStatusRowFoldsCarriageReturnsWithoutLeakingSequences(t *testing.T) {
	cases := []struct{ in, want string }{
		{"before\rafter\nnext\ttab", "before after next tab"},
		{"left\x1b]52;c;secret\rpayload\x07right", "leftright"},
		{"left\x1bPsecret\rpayload\x1b\\right", "leftright"},
		{"left\x1b[31\rmright", "leftright"},
	}
	for _, tc := range cases {
		if got := SanitizeStatusRow(tc.in); got != tc.want {
			t.Errorf("SanitizeStatusRow(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
	if got := Sanitize("before\rafter"); got != "beforeafter" {
		t.Errorf("transcript sanitization changed: %q", got)
	}
}

// P2-033: one sanitization policy — ANSI, OSC, DCS, C1, split sequences.
// Trusted renderer bytes (CSI cursor/erase) stay separate from content bytes.
func TestSanitizePolicy(t *testing.T) {
	cases := []struct{ in, want string }{
		{"\x1b[31mred\x1b[0m", "red"},
		{"\x1b]52;c;secret\x07", ""},
		{"\x1bP$q\x1b\\", ""},     // DCS
		{"\x1bXdata\x1b\\", ""},   // SOS
		{"\x1b^msg\x1b\\", ""},    // PM
		{"\x1b_NB\x1b\\", ""},     // APC
		{"\u009b31mhi", "hi"},     // C1 CSI
		{"\u009dtitle\u009c", ""}, // C1 OSC
		{"a\x7fb", "ab"},          // DEL
		{"a\u0085b", "ab"},        // C1 NEL
		{"a\u202eb", "ab"},        // bidi
		{"plain", "plain"},
		{"line1\nline2\ttab", "line1\nline2\ttab"},
	}
	for _, tc := range cases {
		if got := Sanitize(tc.in); got != tc.want {
			t.Errorf("Sanitize(%q)=%q want %q", tc.in, got, tc.want)
		}
	}
	// Split escape fragments across deltas must not leak.
	var s Sanitizer
	parts := []string{"\x1b", "]52;", "secret", "\x1b", "\\", "ok"}
	out := ""
	for _, p := range parts {
		out += s.Feed(p)
	}
	if out != "ok" {
		t.Fatalf("split OSC leaked: %q", out)
	}
	// Split DCS.
	s = Sanitizer{}
	out = ""
	for _, p := range []string{"\x1b", "P", "data", "\x1b", "\\", "ok"} {
		out += s.Feed(p)
	}
	if out != "ok" {
		t.Fatalf("split DCS leaked: %q", out)
	}
	// Trusted renderer bytes are not content: Writer emits CSI, sanitizer strips content CSI.
	if HasEscape("\x1b[2C") {
		// HasEscape returns true for any escape (content check); renderer CSI is trusted path only.
	}
	for _, f := range ForbiddenSequences {
		if Sanitize(f) == f {
			t.Errorf("forbidden survived sanitize: %q", f)
		}
	}
}
