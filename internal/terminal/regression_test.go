package terminal

import (
	"strings"
	"testing"
)

func TestStreamingSanitizerKeepsEscapeState(t *testing.T) {
	var out strings.Builder
	s := NewStreamer(&out, true)
	for _, part := range []string{"hello", "\x1b]52;", "secret-payload", "\x1b", "\\", " world", "\u009b31m", "!"} {
		s.Delta(part)
	}
	s.Close()
	if out.String() != "hello world!\n" {
		t.Fatalf("escape fragments leaked: %q", out.String())
	}
	if Sanitize("a\x7fb\u009cc") != "abc" {
		t.Fatal("DEL/C1 controls survived")
	}
}

func TestBusyInputCannotSubmitOrExecutePastedControl(t *testing.T) {
	c := &Console{Editor: NewEditor(), Out: &strings.Builder{}}
	_, _, _, cancel := c.Handle(Input{Key: []byte("\x1b[200~")}, true)
	if cancel {
		t.Fatal("paste delimiter cancelled run")
	}
	_, submitted, _, cancel := c.Handle(Input{Key: []byte{3}}, true)
	if submitted || cancel {
		t.Fatal("pasted Ctrl+C was executed")
	}
	c.Handle(Input{Key: []byte("\x1b[201~")}, true)
	_, _, _, cancel = c.Handle(Input{Key: []byte{3}}, true)
	if !cancel {
		t.Fatal("typed Ctrl+C did not cancel")
	}
}
