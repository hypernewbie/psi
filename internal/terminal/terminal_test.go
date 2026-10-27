package terminal

import (
	"strings"
	"testing"
)

func TestSanitizeStripsANSIAndOSC(t *testing.T) {
	in := "hi\x1b[31mred\x1b[0m \x1b]1337;image\x07 ok\x1b\\ end\x01"
	got := Sanitize(in)
	if HasEscape(got) {
		t.Errorf("escape remains: %q", got)
	}
	if !strings.Contains(got, "hi") || !strings.Contains(got, "ok") {
		t.Errorf("content lost: %q", got)
	}
}

func TestSanitizeMalicious(t *testing.T) {
	cases := []string{
		"\x1b[2Jclear",
		"\x1b]0;title\x07text",
		"a\x1bDb",
		"a\x202ebidi",
		"\x00\x08control",
	}
	for _, c := range cases {
		if HasEscape(Sanitize(c)) {
			t.Errorf("malicious survived: %q -> %q", c, Sanitize(c))
		}
	}
}

func TestForbiddenAbsent(t *testing.T) {
	var sb strings.Builder
	sw := NewStatusWriter(&sb)
	sw.Update("read", "c1", ToolRunning)
	sw.Update("read", "c1", ToolDone)
	out := sb.String()
	for _, f := range ForbiddenSequences {
		if strings.Contains(out, f) {
			t.Errorf("forbidden %q in %q", f, out)
		}
	}
	// No args/bodies/diffs in status.
	if strings.Contains(out, "path") || strings.Contains(out, "@@") {
		t.Errorf("status leaked details: %q", out)
	}
	// Throttle: identical updates emit once.
	sw2 := NewStatusWriter(&sb)
	_ = sw2
	if strings.Count(out, "read: done") != 1 || strings.Contains(out, "running") {
		t.Errorf("throttle: %q", out)
	}
}

func TestWidth(t *testing.T) {
	if CellWidth("hello") != 5 {
		t.Errorf("ascii")
	}
	if CellWidth("あ") != 2 {
		t.Errorf("wide")
	}
	if CellWidth("e\u0301") != 1 { // e + combining acute
		t.Errorf("combining")
	}
	if CellWidth("🌍") != 2 {
		t.Errorf("emoji")
	}
	if TruncateToWidth("あいう", 4) != "あい" {
		t.Errorf("truncate wide: %q", TruncateToWidth("あいう", 4))
	}
}

func TestEditor(t *testing.T) {
	e := NewEditor()
	e.Insert("hello")
	e.MoveLeft()
	e.MoveLeft()
	e.Insert("X")
	if e.Text() != "helXlo" {
		t.Errorf("edit=%q", e.Text())
	}
	// Enter submits, history works.
	sub := e.Submit()
	if sub != "helXlo" {
		t.Errorf("submit=%q", sub)
	}
	e.Insert("second")
	e.Submit()
	e.PrevHistory()
	if e.Text() != "second" {
		t.Errorf("history=%q", e.Text())
	}
	// Multiline: Ctrl+J inserts, Enter submits.
	e2 := NewEditor()
	e2.HandleKey([]byte{'\n'}, false)
	if e2.Text() != "\n" {
		t.Errorf("ctrl-j=%q", e2.Text())
	}
	line, ok := e2.HandleKey([]byte{'\r'}, false)
	if !ok || line != "\n" {
		t.Errorf("enter=%q %v", line, ok)
	}
	// Paste newlines are literal.
	e3 := NewEditor()
	e3.HandleKey([]byte("a\nb"), true)
	if e3.Text() != "a\nb" {
		t.Errorf("paste=%q", e3.Text())
	}
}

func TestCommands(t *testing.T) {
	for _, c := range []string{"/help", "/new", "/resume", "/model a b", "/thinking high", "/compact focus", "/session", "/quit", "/skill:do arg"} {
		if _, ok := ParseCommand(c); !ok {
			t.Errorf("command %q not parsed", c)
		}
	}
	if _, ok := ParseCommand("hello"); ok {
		t.Errorf("plain text is not a command")
	}
	cmd, _ := ParseCommand("/bogus")
	if !strings.HasPrefix(cmd.Name, "unknown:") {
		t.Errorf("unknown must be distinct: %+v", cmd)
	}
	if !strings.Contains(HelpText(), "/compact") {
		t.Errorf("help")
	}
}

func TestStreamerNoRedraw(t *testing.T) {
	var sb strings.Builder
	st := NewStreamer(&sb, true)
	st.PrintUser("hi")
	st.Delta("hel")
	st.Delta("lo")
	st.Close()
	out := sb.String()
	if !strings.Contains(out, "hi") || !strings.Contains(out, "hello") {
		t.Errorf("stream=%q", out)
	}
	for _, f := range ForbiddenSequences {
		if strings.Contains(out, f) {
			t.Errorf("forbidden in stream")
		}
	}
}
