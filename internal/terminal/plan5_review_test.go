package terminal

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"psi/internal/event"
	pitheme "psi/internal/theme"
)

func TestReviewUserMarkerDoesNotDiscardWrappedPromptText(t *testing.T) {
	const source = "abcdefghijklmnopqrstuvwxyz0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZ"
	for _, width := range []int{8, 20, 40, 80} {
		rows := FormatUserMessage(source, width, true)
		var recovered strings.Builder
		for _, row := range rows {
			if CellWidth(row) > width-1 {
				t.Fatalf("width %d: overflowing row %q", width, row)
			}
			recovered.WriteString(strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(row), "›")))
		}
		if recovered.String() != source {
			t.Errorf("width %d: prompt text lost: got %q, want %q", width, recovered.String(), source)
		}
		if len(rows) > 2 && !strings.HasPrefix(rows[1], "    ") {
			t.Errorf("width %d: continuation does not align with prompt text: %q", width, rows[1])
		}
	}
}

func TestReviewUserMarkerPreservesLeadingCodeFenceAndQuote(t *testing.T) {
	for _, source := range []string{"```go\nabcdef\n```", "> requested change"} {
		rows := FormatUserMessage(source, 40, true)
		output := strings.Join(rows, "\n")
		if !strings.Contains(output, "›") {
			t.Fatalf("missing user marker: %q", output)
		}
		if strings.HasPrefix(source, "```") {
			if !strings.Contains(output, "```go") || !strings.Contains(output, "\n  abcdef\n") {
				t.Errorf("user marker changed code: %q", output)
			}
		} else if !strings.Contains(output, "│") || !strings.Contains(output, "requested change") {
			t.Errorf("user marker changed quote: %q", output)
		}
	}
}

func TestReviewOversizedOSCPreservesSplitSTTerminator(t *testing.T) {
	for _, extra := range []int{0, 1, 10} {
		payload := append([]byte("\x1b]"), bytes.Repeat([]byte{'x'}, maxPendingEscape-2+extra)...)
		payload = append(payload, '\x1b', '\\', 'z')
		for split := 0; split <= len(payload); split++ {
			decoder := NewDecoder()
			decoded := append(decoder.Feed(payload[:split]), decoder.Feed(payload[split:])...)
			if len(decoded) != 1 || decoded[0].Reply || string(decoded[0].Key) != "z" {
				t.Fatalf("extra=%d split=%d: terminator swallowed following key: %+v", extra, split, decoded)
			}
		}
	}
}

func TestReviewTimedOutOSCPreservesPendingSTPrefix(t *testing.T) {
	decoder := NewDecoder()
	now := time.Unix(0, 0)
	decoder.SetClock(func() time.Time { return now })
	if decoded := decoder.Feed([]byte("\x1b]10;broken\x1b")); len(decoded) != 0 {
		t.Fatal(decoded)
	}
	now = now.Add(escapeTimeout + time.Millisecond)
	if decoded := decoder.Feed(nil); len(decoded) != 0 {
		t.Fatal(decoded)
	}
	decoded := decoder.Feed([]byte("\\z"))
	if len(decoded) != 1 || string(decoded[0].Key) != "z" {
		t.Fatalf("timed-out OSC swallowed input: %+v", decoded)
	}
}

func TestReviewToolBackgroundDoesNotColorIndent(t *testing.T) {
	doc, err := pitheme.BuiltinDocument("dark")
	if err != nil {
		t.Fatal(err)
	}
	palette, err := doc.Compile(pitheme.ColorModeTruecolor)
	if err != nil {
		t.Fatal(err)
	}
	row := FormatToolRow(event.ToolSummary{Kind: "read", Path: "main.go", Available: true}, "read", "done", 40, false, palette)
	const gutter = "   "
	if !strings.HasPrefix(row, gutter+"\x1b[") {
		t.Fatalf("tool background covers its inset: %q", row)
	}
	background, _ := palette.BackgroundANSI("toolSuccessBg")
	if !strings.Contains(row[len(gutter):], background) {
		t.Fatalf("missing tool surface: %q", row)
	}
	if CellWidth(Sanitize(row)) > 39 {
		t.Fatalf("tool row overflow: %q", row)
	}
}
