package terminal

// Phase G regressions for PLAN3 F10: untrusted status text cannot escape the
// single owned row. Provider-supplied tool names reach status formatting
// before enablement checks (runBatch ToolStart), so the status boundary
// folds line breaks and drops controls. Verified with the VT emulator plus
// a committed-scrollback sentinel.

import (
	"fmt"
	"strings"
	"testing"

	"psi/internal/event"
	"psi/internal/testutil"
)

func TestF10StatusRowAccounting(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
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

func TestSettledToolStatusKeepsStateWithinPhysicalWidth(t *testing.T) {
	var out strings.Builder
	w := &fixedStatusWidthWriter{out: &out, width: 14}
	sw := NewStatusWriter(w)
	sw.Update("tool-very-long\nsecond", "call-1", ToolFailed)
	plain := Sanitize(out.String())
	line := strings.TrimSuffix(plain, "\n")
	if strings.ContainsAny(line, "\r\n") || CellWidth(line) > w.width-1 {
		t.Fatalf("settled row is multiline or too wide: %q (%d cells)", line, CellWidth(line))
	}
	if !strings.HasSuffix(line, ": failed") {
		t.Fatalf("tool outcome lost to name clipping: %q", line)
	}
	if !strings.Contains(out.String(), "\x1b[31m") || !strings.HasSuffix(out.String(), "\x1b[0m\n") {
		t.Fatalf("Pi error token or row reset missing: %q", out.String())
	}
}

type fixedStatusWidthWriter struct {
	out   *strings.Builder
	width int
}

func (w *fixedStatusWidthWriter) Write(p []byte) (int, error) { return w.out.Write(p) }
func (w *fixedStatusWidthWriter) StatusWidth() int            { return w.width }

func TestF10UnsupportedNameThroughConsole(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	// Helper coverage only. This test does not run the provider or app loop.
	c := &Console{Editor: NewEditor(), Out: &strings.Builder{}, writer: NewWriter(&strings.Builder{}), decoder: NewDecoder()}
	evil := "pwn\nsecond: running\x1b[2J"
	c.Status(FormatStatus(evil, ToolRunning))
	for _, row := range c.visibleTail {
		if strings.ContainsAny(row, "\n\r\x1b") {
			t.Fatalf("owned row escapes: %q", row)
		}
	}
	if len(c.visibleTail) < 3 || !strings.Contains(Sanitize(c.visibleTail[1]), "pwn second: running") {
		t.Fatalf("activity missing from bordered editor: %q", c.visibleTail)
	}
	// A running status emits nothing, then settlement commits one line.
	var sb strings.Builder
	sw := NewStatusWriter(&sb)
	sw.Update("tool\nname", "c1", ToolRunning)
	sw.Update("tool\nname", "c1", ToolDone)
	if out := sb.String(); out != MessageIndent+"tool name: done\n" {
		t.Fatalf("wrong settled status: %q", out)
	}
}

func TestStatusWriterUpdateSummary(t *testing.T) {
	var sb strings.Builder
	sw := NewStatusWriter(&sb)

	code := 1
	sumRunning := event.ToolSummary{Kind: "bash", Command: "make build", Available: true}
	sumDone := event.ToolSummary{Kind: "bash", Command: "make build", Available: true}
	sumFailed := event.ToolSummary{Kind: "bash", Command: "make test", Available: true, ExitCode: &code}

	// 1. Running state does not write settled lines
	sw.UpdateSummary("bash", "call-1", sumRunning, "running", DefaultDisplaySettings(), nil, true)
	if sb.Len() > 0 {
		t.Fatalf("running summary should not write to output, got %q", sb.String())
	}

	// 2. Duplicate state is suppressed
	sw.UpdateSummary("bash", "call-1", sumRunning, "running", DefaultDisplaySettings(), nil, true)
	if sb.Len() > 0 {
		t.Fatal("duplicate running state should be suppressed")
	}

	// 3. Settled state "done" writes formatted line
	sw.UpdateSummary("bash", "call-1", sumDone, "done", DefaultDisplaySettings(), nil, true)
	if sb.Len() == 0 || !strings.Contains(sb.String(), "make build") || !strings.HasSuffix(sb.String(), "\n") {
		t.Fatalf("done summary missing or malformed: %q", sb.String())
	}

	// 4. Settled state duplicate suppressed
	beforeLen := sb.Len()
	sw.UpdateSummary("bash", "call-1", sumDone, "done", DefaultDisplaySettings(), nil, true)
	if sb.Len() != beforeLen {
		t.Fatal("duplicate done summary should be suppressed")
	}

	// 5. Failed state writes exit code
	sw.UpdateSummary("bash", "call-2", sumFailed, "failed", DefaultDisplaySettings(), nil, true)
	if !strings.Contains(sb.String(), "failed (exit 1)") {
		t.Fatalf("failed summary missing exit code: %q", sb.String())
	}

	// 6. Sized writer uses StatusWidth
	var sizedSb strings.Builder
	wSized := &fixedStatusWidthWriter{out: &sizedSb, width: 30}
	swSized := NewStatusWriter(wSized)
	swSized.UpdateSummary("bash", "call-3", sumFailed, "failed", DefaultDisplaySettings(), nil, true)
	if CellWidth(Sanitize(sizedSb.String())) > 30 {
		t.Fatalf("sized summary exceeded width 30: %q", sizedSb.String())
	}

	// 7. Cache eviction when reaching 1024 keys
	for i := 0; i < 1030; i++ {
		sw.UpdateSummary("bash", fmt.Sprintf("call-bulk-%d", i), sumDone, "done", DefaultDisplaySettings(), nil, true)
	}
	if len(sw.last) >= 1024 {
		t.Fatalf("cache should clear when reaching 1024 entries, got %d", len(sw.last))
	}
}
