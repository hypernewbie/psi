package terminal

import (
	"strings"
	"testing"

	"psi/internal/event"
)

func TestToolRowKeepsPreviewAndOutcomeOnOneBoundedLine(t *testing.T) {
	code := 127
	summary := event.ToolSummary{Kind: "bash", Command: "make test", Available: true, ExitCode: &code}
	row := FormatToolRow(summary, "bash", "failed", 28, true)
	plain := Sanitize(row)
	if CellWidth(plain) > 27 || strings.ContainsAny(plain, "\r\n") {
		t.Fatalf("tool row is not one bounded line: width=%d %q", CellWidth(plain), plain)
	}
	if !strings.Contains(plain, "failed (exit 127)") {
		t.Fatalf("outcome was clipped: %q", plain)
	}
	if strings.Contains(plain, "make test") {
		t.Fatalf("preview should yield to the reserved failure state at this width: %q", plain)
	}
}

func TestToolRowUsesInsetPreviewAndCompactSuccess(t *testing.T) {
	summary := event.ToolSummary{Kind: "read", Path: "internal/app/app.go", LineRange: ":10-14", Available: true}
	running := Sanitize(FormatToolRow(summary, "read", "running", 80, true))
	if !strings.Contains(running, "◌ read  internal/app/app.go:10-14 · running") {
		t.Fatalf("unexpected running row %q", running)
	}
	done := Sanitize(FormatToolRow(summary, "read", "done", 80, true))
	if strings.Contains(done, "done") || !strings.Contains(done, "• read  internal/app/app.go:10-14") {
		t.Fatalf("unexpected success row %q", done)
	}
}

func TestToolRowSanitizesUntrustedPreview(t *testing.T) {
	summary := event.ToolSummary{Kind: "bash", Command: "echo safe\x1b[2J\rsecond", Available: true}
	row := Sanitize(FormatToolRow(summary, "bash", "running", 80, true))
	if strings.Contains(row, "\x1b") || strings.Contains(row, "second") || strings.ContainsAny(row, "\r\n") {
		t.Fatalf("unsafe row %q", row)
	}
}
