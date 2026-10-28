package terminal

// Phase G regressions for PLAN3 F10: untrusted status text cannot escape the
// single owned row. Provider-supplied tool names reach status formatting
// before enablement checks (runBatch ToolStart), so the status boundary
// folds line breaks and drops controls. Verified with the VT emulator plus
// a committed-scrollback sentinel.

import (
	"strings"
	"testing"

	"psi/internal/testutil"
)

func TestF10StatusRowAccounting(t *testing.T) {
	vt := testutil.NewVTModel(80, 24)
	w := NewWriter(vtFeed{vt})
	const sentinel = "COMMITTED_STATUS_SENTINEL"
	if err := w.Emit(Frame{Append: sentinel + "\r\n" + strings.Repeat("committed\r\n", 24)}); err != nil {
		t.Fatal(err)
	}
	names := []string{
		"read\ninjected: done",
		"evil\r\nsecond: running",
		"tab\tseparated: running",
		"name\x1b[2J: running",
		"name\x1b]0;title\x07: running",
		"\u202eRTL: running",
		"wide-世界-word-that-is-very-long-and-keeps-going-past-width: running",
		"",
		": running",
	}
	for _, n := range names {
		row := FormatStatus(n, ToolRunning)
		if strings.ContainsAny(row, "\n\r\t\x1b\x07") {
			t.Fatalf("status row escapes: %q", row)
		}
		if err := w.Emit(Frame{Tail: []string{TruncateToWidth(row, 79)}}); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(strings.Join(vt.Scrollback, "\n")+vt.Text(), sentinel) {
			t.Fatal("status update destroyed committed scrollback")
		}
	}
	// Owned rows: one per status update; no cursor escape; scrollback keeps
	// only committed transcript (status is live tail, never scrollback).
	for _, op := range vt.EraseOps {
		if op == "ED2" || op == "ED3J" {
			t.Fatalf("status cleared scrollback: %v", op)
		}
	}
}

func TestF10UnsupportedNameThroughConsole(t *testing.T) {
	// Helper coverage only. This test does not run the provider or app loop.
	c := &Console{Editor: NewEditor(), Out: &strings.Builder{}, writer: NewWriter(&strings.Builder{}), decoder: NewDecoder()}
	evil := "pwn\nsecond: running\x1b[2J"
	c.Status(FormatStatus(evil, ToolRunning))
	for _, row := range c.visibleTail {
		if strings.ContainsAny(row, "\n\r\x1b") {
			t.Fatalf("owned row escapes: %q", row)
		}
	}
	if len(c.visibleTail) < 3 || !strings.Contains(Sanitize(c.visibleTail[0]), "pwn second: running") {
		t.Fatalf("activity missing from bordered editor: %q", c.visibleTail)
	}
	// A running status emits nothing, then settlement commits one line.
	var sb strings.Builder
	sw := NewStatusWriter(&sb)
	sw.Update("tool\nname", "c1", ToolRunning)
	sw.Update("tool\nname", "c1", ToolDone)
	if out := sb.String(); out != "tool name: done\n" {
		t.Fatalf("wrong settled status: %q", out)
	}
}
