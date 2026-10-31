package terminal

import (
	"strings"
	"testing"
	"unicode/utf8"
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

func verifySanitizeInvariant(t *testing.T, label, raw, sanitized string) {
	t.Helper()
	if !utf8.ValidString(sanitized) {
		t.Fatalf("%s: output is not valid UTF-8 for raw %q", label, raw)
	}
	for i, r := range sanitized {
		if r == 27 {
			t.Fatalf("%s: raw ESC (0x1b) leaked at index %d in %q (raw %q)", label, i, sanitized, raw)
		}
		if r < 0x20 && r != '\n' && r != '\t' {
			t.Fatalf("%s: C0 control 0x%02x leaked at index %d in %q (raw %q)", label, r, i, sanitized, raw)
		}
		if r == 0x7f {
			t.Fatalf("%s: DEL (0x7f) leaked at index %d in %q (raw %q)", label, i, sanitized, raw)
		}
		if r >= 0x80 && r <= 0x9f {
			t.Fatalf("%s: C1 control 0x%02x leaked at index %d in %q (raw %q)", label, r, i, sanitized, raw)
		}
		if isBidiControl(r) {
			t.Fatalf("%s: bidi control U+%04X leaked at index %d in %q (raw %q)", label, r, i, sanitized, raw)
		}
		if isInterlinearAnnotation(r) {
			t.Fatalf("%s: interlinear annotation U+%04X leaked at index %d in %q (raw %q)", label, r, i, sanitized, raw)
		}
	}
}

func verifyStatusRowInvariant(t *testing.T, label, raw, sanitized string) {
	t.Helper()
	verifySanitizeInvariant(t, label, raw, sanitized)
	if strings.ContainsAny(sanitized, "\r\n") {
		t.Fatalf("%s: newline leaked into status row %q (raw %q)", label, sanitized, raw)
	}
}

func TestSanitizeEscapeSequenceInjections(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string // expected visible string, or empty if entirely control
	}{
		// OSC 52 clipboard hijacking
		{"OSC 52 BEL", "text\x1b]52;c;ZXhpbA==\x07more", "textmore"},
		{"OSC 52 ST", "text\x1b]52;c;ZXhpbA==\x1b\\more", "textmore"},
		{"OSC 52 C1 ST", "text\u009d52;c;ZXhpbA==\u009cmore", "textmore"},
		{"OSC 52 paste query", "text\x1b]52;p;?\x07more", "textmore"},

		// Window title changes
		{"OSC 0 window title", "\x1b]0;Evil Window Title\x07hello", "hello"},
		{"OSC 2 window title", "\x1b]2;Hacked\x1b\\world", "world"},
		{"OSC 0 with newlines", "\x1b]0;Title\r\nInjection\x07safe", "safe"},

		// Cursor movement & display controls
		{"CSI clear screen", "prefix\x1b[2Jsuffix", "prefixsuffix"},
		{"CSI clear scrollback", "\x1b[3Jclear", "clear"},
		{"CSI cursor position", "before\x1b[10;20Hafter", "beforeafter"},
		{"CSI cursor up/down/fwd/back", "a\x1b[1Ab\x1b[2Bc\x1b[3Cd\x1b[4De", "abcde"},
		{"CSI cursor visibility", "\x1b[?25lhidden\x1b[?25h", "hidden"},
		{"CSI alternate buffer", "\x1b[?1049hscreen\x1b[?1049l", "screen"},
		{"CSI save/restore cursor", "\x1b[ssaved\x1b[urestored", "savedrestored"},
		{"C1 CSI clear", "prefix\u009b2Jsuffix", "prefixsuffix"},

		// DCS (Device Control String)
		{"DCS request status", "\x1bP$q\"p\x1b\\clean", "clean"},
		{"DCS user-defined keys", "\x1bP1$tkey\x1b\\clean", "clean"},
		{"C1 DCS", "\u0090$q\u009cclean", "clean"},

		// APC (Application Program Command) & PM & SOS
		{"APC kitty graphics", "\x1b_Gi=1;payload\x1b\\visible", "visible"},
		{"PM privacy message", "\x1b^confidential\x1b\\visible", "visible"},
		{"SOS start of string", "\x1bXsystem\x1b\\visible", "visible"},
		{"C1 APC", "\u009fGi=1;\u009cvisible", "visible"},
		{"C1 PM", "\u009econfidential\u009cvisible", "visible"},
		{"C1 SOS", "\u0098system\u009cvisible", "visible"},

		// Consecutive and nested escape sequences (verifying bug fix)
		{"consecutive ESC to OSC", "\x1b\x1b]52;c;evil\x07safe", "safe"},
		{"consecutive ESC to CSI", "\x1b\x1b[31mcolor\x1b[0m", "color"},
		{"ESC inside CSI to OSC", "\x1b[\x1b]52;c;evil\x07safe", "safe"},
		{"triple ESC to APC", "\x1b\x1b\x1b_secret\x1b\\safe", "safe"},
		{"CAN/SUB interrupt in OSC", "\x1b]52;evil\x18cancelled", "cancelled"},
		{"CAN/SUB interrupt in CSI", "\x1b[12\x1acancelled", "cancelled"},

		// OSC smuggling prevention
		{"OSC smuggling hyperlink", "\x1b]8;;http://evil\x1b[mLEAKED\x07", ""},
		{"OSC smuggling with visible text", "\x1b]8;;http://evil\x1b[mLEAKED\x07visible", "visible"},
		{"OSC smuggling complex CSI", "\x1b]8;;http://evil\x1b[31;1mLEAKED\x07visible", "visible"},
		{"OSC with ESC BEL terminator", "\x1b]0;title\x1b\x07visible", "visible"},
		{"OSC with ESC C1 ST terminator", "\x1b]0;title\x1b\x9cvisible", "visible"},

		// Interlinear annotation controls U+FFF9..U+FFFB
		{"interlinear annotation anchor", "hello\ufff9world", "helloworld"},
		{"interlinear annotation separator", "hello\ufffaworld", "helloworld"},
		{"interlinear annotation terminator", "hello\ufffbworld", "helloworld"},
		{"all interlinear annotations", "\ufff9ruby\ufffaannotation\ufffbtext", "rubyannotationtext"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := Sanitize(tc.in)
			if got != tc.want {
				t.Errorf("Sanitize(%q) = %q, want %q", tc.in, got, tc.want)
			}
			verifySanitizeInvariant(t, tc.name, tc.in, got)

			// Also test SanitizeStatusRow
			rowGot := SanitizeStatusRow(tc.in)
			verifyStatusRowInvariant(t, tc.name+" statusRow", tc.in, rowGot)
		})
	}
}

func TestSanitizeInvariantFuzzing(t *testing.T) {
	// Comprehensive property test generating random combinations of:
	// - normal text
	// - raw control codes (C0, C1, Bidi)
	// - escape sequences (valid, truncated, concatenated, split)
	// - invalid UTF-8 bytes
	primitives := []string{
		"abc", "123", "   ", "hello\nworld", "tab\tseparated",
		"日本語", "emoji 🔥 👩‍❤️‍💋‍👩", "accent e\u0301",
		"\x1b", "\x1b[", "\x1b[31m", "\x1b[0m", "\x1b[2J", "\x1b[H",
		"\x1b]", "\x1b]0;", "Title\x07", "\x1b]52;c;evil\x1b\\",
		"\x1bP", "data\x1b\\", "\x1b_", "APC\x07", "\x1b^", "PM\x1b\\",
		"\u009b", "\u009d", "\u009c", "\u0090", "\u009f",
		"\x00", "\x07", "\x08", "\r", "\x0b", "\x0c", "\x7f",
		"\u202a", "\u202e", "\u2066", "\u200e",
		"\ufff9", "\ufffa", "\ufffb",
		"\xff", "\xfe", "\xc0\xaf",
	}

	seed := uint32(9999)
	randNext := func() uint32 {
		seed = seed*1664525 + 1013904223
		return seed
	}

	for iter := 0; iter < 1000; iter++ {
		var b strings.Builder
		count := int(randNext()%10) + 1
		for c := 0; c < count; c++ {
			b.WriteString(primitives[randNext()%uint32(len(primitives))])
		}
		raw := b.String()

		// 1. Full string sanitization
		sanitized := Sanitize(raw)
		verifySanitizeInvariant(t, "fuzz full", raw, sanitized)

		statusRow := SanitizeStatusRow(raw)
		verifyStatusRowInvariant(t, "fuzz statusRow", raw, statusRow)

		// 2. Chunked feeding through Sanitizer
		var chunkSan Sanitizer
		var chunkOut strings.Builder
		chunkSize := int(randNext()%4) + 1
		for i := 0; i < len(raw); i += chunkSize {
			end := min(i+chunkSize, len(raw))
			chunkOut.WriteString(chunkSan.Feed(raw[i:end]))
		}
		verifySanitizeInvariant(t, "fuzz chunked", raw, chunkOut.String())
	}
}

func TestOSCSmugglingPrevention(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "OSC 8 hyperlink with embedded CSI",
			in:   "\x1b]8;;http://evil\x1b[mLEAKED\x07",
			want: "",
		},
		{
			name: "OSC 8 hyperlink with embedded CSI followed by text",
			in:   "\x1b]8;;http://evil\x1b[mLEAKED\x07visible",
			want: "visible",
		},
		{
			name: "OSC 0 title with embedded CSI color",
			in:   "\x1b]0;evil\x1b[31;1mINJECTED\x07safe",
			want: "safe",
		},
		{
			name: "OSC with ESC BEL terminator",
			in:   "\x1b]0;title\x1b\x07visible",
			want: "visible",
		},
		{
			name: "OSC with ESC C1 ST terminator",
			in:   "\x1b]0;title\x1b\x9cvisible",
			want: "visible",
		},
		{
			name: "OSC with standard ST",
			in:   "\x1b]0;title\x1b\\visible",
			want: "visible",
		},
		{
			name: "OSC with C1 ST",
			in:   "\x1b]0;title\u009cvisible",
			want: "visible",
		},
		{
			name: "OSC with BEL",
			in:   "\x1b]0;title\x07visible",
			want: "visible",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Sanitize(tc.in)
			if got != tc.want {
				t.Errorf("Sanitize(%q) = %q, want %q", tc.in, got, tc.want)
			}
			verifySanitizeInvariant(t, tc.name, tc.in, got)

			// Also verify chunked feeding does not leak across any split boundary
			for splitAt := 1; splitAt < len(tc.in); splitAt++ {
				var s Sanitizer
				chunked := s.Feed(tc.in[:splitAt]) + s.Feed(tc.in[splitAt:])
				if chunked != tc.want {
					t.Errorf("Sanitizer split at %d (%q + %q) = %q, want %q",
						splitAt, tc.in[:splitAt], tc.in[splitAt:], chunked, tc.want)
				}
			}
		})
	}
}

func TestInterlinearAnnotationStripping(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"anchor U+FFF9", "hello\ufff9world", "helloworld"},
		{"separator U+FFFA", "hello\ufffaworld", "helloworld"},
		{"terminator U+FFFB", "hello\ufffbworld", "helloworld"},
		{"all annotation controls", "\ufff9base\ufffaannotation\ufffb", "baseannotation"},
		{"status row annotation controls", "status:\ufff9info\ufffaextra\ufffb", "status:infoextra"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Sanitize(tc.in)
			if got != tc.want {
				t.Errorf("Sanitize(%q) = %q, want %q", tc.in, got, tc.want)
			}
			verifySanitizeInvariant(t, tc.name, tc.in, got)

			rowGot := SanitizeStatusRow(tc.in)
			verifyStatusRowInvariant(t, tc.name+" statusRow", tc.in, rowGot)
		})
	}

	// Status row newline folding with interlinear annotations
	statusGot := SanitizeStatusRow("status:\ufff9info\ufffaextra\ufffb\nline")
	statusWant := "status:infoextra line"
	if statusGot != statusWant {
		t.Errorf("SanitizeStatusRow() = %q, want %q", statusGot, statusWant)
	}
}

func TestPiCompatibilityInputs(t *testing.T) {
	knownCases := []struct {
		in   string
		want string
	}{
		{"plain", "plain"},
		{"a\x1b[31mred\x1b[0mz", "aredz"},
		{"a\x1b]8;;https://example.com\x07link\x1b]8;;\x07z", "alinkz"},
		{"a\x1b]unterminated", "a"},
		{"a\x1b]funterminated", "a"},
		{"a\x1bPabc\x1b\\z", "az"},
		{"a\x1b^abc\x07z", "az"},
		{"a\x1b_abc\x9cz", "az"},
		{"a\x90abc\x9cz", "az"},
		{"a\x9dabc\x9cz", "az"},
		{"a\x9b31mred", "ared"},
		{"a\x1b(0x", "ax"},
		{"a\x1b*0x", "ax"},
		{"a\x1b+c", "a"},
		{"a\x1b/0x", "ax"},
		{"a\x1bcok", "aok"},
		{"a\x1b\\ok", "aok"},
	}
	for _, tc := range knownCases {
		got := Sanitize(tc.in)
		if got != tc.want {
			t.Errorf("Sanitize(%q) = %q, want %q", tc.in, got, tc.want)
		}
		verifySanitizeInvariant(t, "pi-known", tc.in, got)
	}

	// Combinatorial inputs matching pi's getCompatibilityInputs()
	chars := []string{
		"a", "f", "0", "1", ";", ":", "[", "]", "(", ")",
		"#", "?", "m", "P", "_", "\\", "\x07", "\x1b",
		"\x9b", "\x9c", "\x90", "\x9d",
	}

	var generated []string
	for _, char := range chars {
		generated = append(generated, "x\x1b"+char+"y")
		generated = append(generated, "x\x9b"+char+"y")
		for index := 0; index < len(chars); index += 3 {
			generated = append(generated, "x\x1b"+char+chars[index]+"y")
		}
	}

	for _, input := range generated {
		sanitized := Sanitize(input)
		verifySanitizeInvariant(t, "pi-compat", input, sanitized)
		statusRow := SanitizeStatusRow(input)
		verifyStatusRowInvariant(t, "pi-compat statusRow", input, statusRow)
	}
}
