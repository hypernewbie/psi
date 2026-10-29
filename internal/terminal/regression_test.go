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
	c := &Console{Editor: NewEditor(), Out: &strings.Builder{}, writer: NewWriter(&strings.Builder{}), decoder: NewDecoder()}
	_, _, _, cancel := c.Handle(Input{Paste: true, PasteStart: true}, true)
	if cancel {
		t.Fatal("paste delimiter cancelled run")
	}
	_, submitted, _, cancel := c.Handle(Input{Key: []byte{3}, Paste: true}, true)
	if submitted || cancel {
		t.Fatal("pasted Ctrl+C was executed")
	}
	c.Handle(Input{Paste: true, PasteEnd: true}, true)
	_, _, _, cancel = c.Handle(Input{Key: []byte{3}}, true)
	if !cancel {
		t.Fatal("typed Ctrl+C did not cancel")
	}
}

// A4-15: ordinary keys during an active run are ignored. They never submit,
// never cancel, and never become a queued prompt.
func TestBusyRunIgnoresOrdinaryKeysWithoutQueueingThem(t *testing.T) {
	c := &Console{Editor: NewEditor(), Out: &strings.Builder{}, writer: NewWriter(&strings.Builder{}), decoder: NewDecoder()}
	for _, key := range [][]byte{[]byte("x"), []byte("\r"), []byte("hello"), []byte{9}} {
		text, submitted, quit, cancel := c.Handle(Input{Key: key}, true)
		if text != "" || submitted || quit || cancel {
			t.Fatalf("busy key %q was acted on: text=%q submitted=%v quit=%v cancel=%v", key, text, submitted, quit, cancel)
		}
	}
	if c.Editor.Text() != "" {
		t.Fatalf("busy run queued ordinary input: %q", c.Editor.Text())
	}
	if len(c.completionItems) != 0 {
		t.Fatalf("busy run opened a completion list: %q", c.completionItems)
	}
}
