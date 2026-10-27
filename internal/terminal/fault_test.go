package terminal

// PLAN3 P3-053: bounded terminal writes with error propagation. Short
// writes loop without losing or duplicating the suffix; zero progress and
// invalid counts fail instead of spinning; EPIPE and other errors propagate
// with correct byte order.

import (
	"errors"
	"io"
	"strings"
	"testing"
)

type faultWriter struct {
	calls int
	mode  string // short, zero, badcount, epipe, block-then-eof(fail)
	buf   []byte
}

func (w *faultWriter) Write(p []byte) (int, error) {
	w.calls++
	switch w.mode {
	case "short":
		n := len(p) / 2
		if n == 0 && len(p) > 0 {
			n = 1
		}
		w.buf = append(w.buf, p[:n]...)
		return n, nil
	case "zero":
		return 0, nil
	case "badcount":
		return len(p) + 1, nil
	case "epipe":
		n := len(p) / 2
		w.buf = append(w.buf, p[:n]...)
		return n, errors.New("broken pipe")
	}
	w.buf = append(w.buf, p...)
	return len(p), nil
}

func TestF53WriterFaults(t *testing.T) {
	payload := strings.Repeat("abcdefgh", 9000) // > 64KiB forces multiple units
	if err := WriteBounded(&faultWriter{mode: "ok"}, []byte(payload)); err != nil {
		t.Fatalf("ok writer: %v", err)
	}
	// Short writes: exact bytes, correct order, no duplication.
	sw := &faultWriter{mode: "short"}
	if err := WriteBounded(sw, []byte(payload)); err != nil {
		t.Fatalf("short writes: %v", err)
	}
	if string(sw.buf) != payload {
		t.Fatal("short-write suffix lost or duplicated")
	}
	// Zero progress: fail, do not spin.
	if err := WriteBounded(&faultWriter{mode: "zero"}, []byte("x")); !errors.Is(err, io.ErrShortWrite) {
		t.Fatalf("zero progress: %v", err)
	}
	// Invalid counts: fail.
	if err := WriteBounded(&faultWriter{mode: "badcount"}, []byte("x")); !errors.Is(err, io.ErrShortWrite) {
		t.Fatalf("bad count: %v", err)
	}
	// EPIPE propagates after partial bytes.
	ew := &faultWriter{mode: "epipe"}
	if err := WriteBounded(ew, []byte("hello")); err == nil || !strings.Contains(err.Error(), "broken pipe") {
		t.Fatalf("epipe: %v", err)
	}
	// Partial bytes already accepted stay; the error still propagates.
	if string(ew.buf) != "he" {
		t.Fatalf("epipe prefix wrong: %q", ew.buf)
	}
}
