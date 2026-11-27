package agent

import (
	"testing"
)

// TestCompactionQueueHoldsMessagesUntilTheCheckpoint covers Pi's compaction
// queue: a message typed while a compaction runs waits for the checkpoint.
// The queue keeps it out of the run's own queues, so the summary is
// generated without it, and the checkpoint releases it into the run.
func TestCompactionQueueHoldsMessagesUntilTheCheckpoint(t *testing.T) {
	_, _, _, loop, _ := testSetup(t)
	loop.QueueDuringCompaction(UserInput{Content: "typed during the compaction", Display: "typed during the compaction"}, false)
	loop.QueueDuringCompaction(UserInput{Content: "follow-up during the compaction", Display: "follow-up during the compaction"}, true)

	if got := loop.PendingSteering(); len(got) != 0 {
		t.Fatalf("the compaction message reached the steering queue: %q", got)
	}
	if got := loop.PendingFollowUp(); len(got) != 0 {
		t.Fatalf("the compaction message reached the follow-up queue: %q", got)
	}
	if got := loop.PendingCompactionSteering(); len(got) != 1 || got[0] != "typed during the compaction" {
		t.Fatalf("compaction steering = %q", got)
	}
	if got := loop.PendingCompactionFollowUp(); len(got) != 1 || got[0] != "follow-up during the compaction" {
		t.Fatalf("compaction follow-up = %q", got)
	}

	loop.flushCompactionQueue()

	if got := loop.PendingCompactionSteering(); len(got) != 0 {
		t.Fatalf("the compaction queue was not emptied: %q", got)
	}
	if got := loop.PendingSteering(); len(got) != 1 || got[0] != "typed during the compaction" {
		t.Fatalf("steering after the checkpoint = %q", got)
	}
	if got := loop.PendingFollowUp(); len(got) != 1 || got[0] != "follow-up during the compaction" {
		t.Fatalf("follow-up after the checkpoint = %q", got)
	}
}

// TestClearCompactionQueueReturnsTheMessages covers the dequeue action: the
// messages leave the compaction queue and come back as text.
func TestClearCompactionQueueReturnsTheMessages(t *testing.T) {
	_, _, _, loop, _ := testSetup(t)
	loop.QueueDuringCompaction(UserInput{Content: "first", Display: "first"}, false)
	loop.QueueDuringCompaction(UserInput{Content: "second", Display: "second"}, true)
	got := loop.ClearCompactionQueue()
	if len(got) != 2 || got[0] != "first" || got[1] != "second" {
		t.Fatalf("cleared compaction queue = %q", got)
	}
	if pending := loop.PendingCompactionSteering(); len(pending) != 0 {
		t.Fatalf("the compaction queue was not emptied: %q", pending)
	}
	if got := loop.ClearCompactionQueue(); len(got) != 0 {
		t.Fatalf("a second clear returned %q", got)
	}
}
