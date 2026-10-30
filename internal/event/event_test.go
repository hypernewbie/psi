package event

import (
	"testing"
)

func TestEventConstants(t *testing.T) {
	expectedTypes := map[Type]string{
		TextDelta:         "text_delta",
		AssistantStart:    "assistant_start",
		RunStart:          "run_start",
		RunEnd:            "run_end",
		SteeringDelivered: "steering_delivered",
		ToolStart:         "tool_start",
		ToolEnd:           "tool_end",
		Retry:             "retry",
		CompactionStart:   "compaction_start",
		CompactionEnd:     "compaction_end",
		Usage:             "usage",
		Error:             "error",
	}

	for typ, expected := range expectedTypes {
		if string(typ) != expected {
			t.Errorf("expected Type %v == %q, got %q", typ, expected, string(typ))
		}
	}

	if CompactionManual != "manual" || CompactionThreshold != "threshold" || CompactionOverflow != "overflow" {
		t.Errorf("compaction constants mismatch")
	}

	if OutcomeTransient != "transient" || OutcomeDurable != "durable" || OutcomeInterrupted != "interrupted" || OutcomeUnsaved != "unsaved" {
		t.Errorf("outcome constants mismatch")
	}
}

func TestEventStructFields(t *testing.T) {
	ev := Event{
		Type:          TextDelta,
		Text:          "sample",
		ToolName:      "read",
		ToolCallID:    "call_1",
		State:         "running",
		Summary:       ToolSummary{Kind: "read", Path: "file.txt"},
		RetryAttempt:  1,
		RetryMaximum:  3,
		RetryDelayMs:  500,
		RetryClass:    "overloaded",
		SessionID:     "s1",
		Generation:    2,
		RunID:         "r1",
		AttemptID:     "a1",
		Seq:           42,
		ProvisionalID: "p1",
		CommittedID:   "c1",
		Outcome:       OutcomeDurable,
	}

	if ev.Type != TextDelta || ev.Summary.Path != "file.txt" || ev.Seq != 42 {
		t.Errorf("event fields corrupted: %+v", ev)
	}
}
