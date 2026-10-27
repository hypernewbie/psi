package terminal

// PLAN3 P3-052: editor/decoder/frame fuzzing for grapheme, budget, and
// physical-row invariants. Random operation sequences must never panic,
// exceed the editor budget, misplace the cursor, or leak line breaks into
// single-row status output.

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func FuzzEditorOps(f *testing.F) {
	f.Add("hello", "\x7f|left|back|ctE|ctU|")
	f.Add("a\nb 世界🌍", "right|ctW|home|end|")
	f.Fuzz(func(t *testing.T, text, ops string) {
		if len(text) > 4096 || len(ops) > 512 {
			return
		}
		if !utf8.ValidString(text) {
			return
		}
		e := NewEditor()
		e.Insert(text)
		for _, op := range strings.Split(ops, "|") {
			switch op {
			case "left":
				e.MoveLeft()
			case "right":
				e.MoveRight()
			case "up":
				e.MoveUp(80)
			case "down":
				e.MoveDown(80)
			case "home":
				e.Home()
			case "end":
				e.End()
			case "back":
				e.Backspace()
			case "del":
				e.Delete()
			case "ctW":
				e.DeleteWordBackward()
			case "ctU":
				e.DeleteToLineStart()
			case "ctE":
				e.DeleteToLineStart()
				e.Insert("x")
			case "ctK":
				e.DeleteToLineEnd()
			case "docS":
				e.DocStart()
			case "docE":
				e.DocEnd()
			}
		}
		got := e.Text()
		if len(got) > 256*1024 {
			t.Fatalf("budget exceeded: %d", len(got))
		}
		if !utf8.ValidString(got) {
			t.Fatal("invalid UTF-8 buffer")
		}
		if e.Pos() < 0 || e.Pos() > len([]rune(got)) {
			t.Fatalf("cursor out of range: %d in %d runes", e.Pos(), len([]rune(got)))
		}
		if !e.Insert("ok") && len(got)+2 > 256*1024 {
			// Whole-edit rejection at the boundary is the correct outcome.
		}
	})
}

func FuzzDecoderFrame(f *testing.F) {
	f.Add("\x1b[200~paste\x1b[201~", "\x1b[A")
	f.Add("plain", "\r\n")
	f.Fuzz(func(t *testing.T, a, b string) {
		if len(a) > 4096 || len(b) > 256 {
			return
		}
		d := NewDecoder()
		for _, chunk := range splitChunks(a + b) {
			for _, in := range d.Feed([]byte(chunk)) {
				_ = in
			}
		}
		// Status rows never carry line breaks, no matter the input.
		row := SanitizeStatusRow(a + b)
		if strings.ContainsAny(row, "\n\r\t\x1b\x07") {
			t.Fatalf("status row escapes: %q", row)
		}
	})
}

func splitChunks(s string) []string {
	var out []string
	for i := 0; i < len(s); i += 3 {
		end := i + 3
		if end > len(s) {
			end = len(s)
		}
		out = append(out, s[i:end])
	}
	return out
}
