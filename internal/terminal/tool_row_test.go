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

func TestTruncatePrefixAndSuffixPreview(t *testing.T) {
	// truncatePrefixPreview
	if got := truncatePrefixPreview("hello", 0); got != "" {
		t.Errorf("truncatePrefixPreview(0) = %q, want empty", got)
	}
	if got := truncatePrefixPreview("hello", -3); got != "" {
		t.Errorf("truncatePrefixPreview(-3) = %q, want empty", got)
	}
	if got := truncatePrefixPreview("hello", 1); got != "…" {
		t.Errorf("truncatePrefixPreview(1) = %q, want …", got)
	}
	if got := truncatePrefixPreview("hello", 5); got != "hello" {
		t.Errorf("truncatePrefixPreview(5) = %q, want hello", got)
	}
	if got := truncatePrefixPreview("hello", 10); got != "hello" {
		t.Errorf("truncatePrefixPreview(10) = %q, want hello", got)
	}
	if got := truncatePrefixPreview("hello world", 6); got != "hello…" {
		t.Errorf("truncatePrefixPreview(6) = %q, want hello…", got)
	}
	// Wide characters
	if got := truncatePrefixPreview("あいうえお", 5); got != "あい…" {
		t.Errorf("truncatePrefixPreview(wide 5) = %q, want あい…", got)
	}

	// truncateSuffixPreview
	if got := truncateSuffixPreview("hello", 0); got != "" {
		t.Errorf("truncateSuffixPreview(0) = %q, want empty", got)
	}
	if got := truncateSuffixPreview("hello", 1); got != "…" {
		t.Errorf("truncateSuffixPreview(1) = %q, want …", got)
	}
	if got := truncateSuffixPreview("hello", 5); got != "hello" {
		t.Errorf("truncateSuffixPreview(5) = %q, want hello", got)
	}
	if got := truncateSuffixPreview("path/to/long/file.go", 11); got != "…ng/file.go" {
		t.Errorf("truncateSuffixPreview(11) = %q, want …ng/file.go", got)
	}
}

func TestToolStateMappings(t *testing.T) {
	cases := []struct {
		in          string
		wantEvent   ToolState
		wantOutcome ToolState
	}{
		{"done", ToolDone, ToolDone},
		{"failed", ToolFailed, ToolFailed},
		{"cancelled", ToolCancelled, ToolCancelled},
		{"canceled", ToolCancelled, ToolCancelled},
		{"not started", ToolNotStarted, ToolNotStarted},
		{"interrupted", ToolInterrupted, ToolInterrupted},
		{"running", ToolRunning, ToolRunning},
		{"unknown", ToolRunning, ToolRunning},
	}

	for _, tc := range cases {
		if got := toolStateFromEvent(tc.in); got != tc.wantEvent {
			t.Errorf("toolStateFromEvent(%q) = %q, want %q", tc.in, got, tc.wantEvent)
		}
		if got := toolOutcomeState(tc.in); got != tc.wantOutcome {
			t.Errorf("toolOutcomeState(%q) = %q, want %q", tc.in, got, tc.wantOutcome)
		}
	}
}
