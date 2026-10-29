package app

import (
	"reflect"
	"strings"
	"testing"

	"psi/internal/session"
	"psi/internal/terminal"
)

func TestTimelineSeparatesToolBatchesAndAssistantReplies(t *testing.T) {
	items := []session.TranscriptItem{
		{Kind: session.KindAssistant, Text: "Before"},
		{Kind: session.KindTool, ToolName: "read", ToolState: "done"},
		{Kind: session.KindTool, ToolName: "write", ToolState: "done"},
		{Kind: session.KindAssistant, Text: "After"},
	}
	var lines []string
	if !renderTimelineAtWidthWithDisplayAndColor(func(line string) bool { lines = append(lines, line); return true }, items, 80, terminal.DefaultDisplaySettings(), true) {
		t.Fatal("render stopped")
	}
	if len(lines) != 7 || !strings.Contains(lines[0], "Before") || lines[1] != "" || !strings.Contains(lines[2], "read") || !strings.Contains(lines[3], "write") || lines[4] != "" || !strings.Contains(lines[5], "After") || lines[6] != "" {
		t.Fatalf("unexpected spacing: %#v", lines)
	}
	var streamed []string
	var spacing toolBatchSpacing
	emit := func(line string) bool { streamed = append(streamed, line); return true }
	for _, item := range items {
		if !renderTimelineItems(emit, []session.TranscriptItem{item}, 80, terminal.DefaultDisplaySettings(), true, &spacing) {
			t.Fatal("streaming render stopped")
		}
	}
	spacing.finish(emit)
	if !reflect.DeepEqual(lines, streamed) {
		t.Fatalf("streamed timeline differs: %#v vs %#v", streamed, lines)
	}
}
