package agent

import (
	"context"
	"os"
	"strings"
	"testing"

	"psi/internal/provider"
	"psi/internal/session"
)

func TestClearFollowUpDrainsQueueInOrder(t *testing.T) {
	_, _, _, loop, _ := testSetup(t)
	loop.FollowUp(" first ")
	loop.FollowUp("   ")
	loop.FollowUp("second")
	if got := loop.ClearFollowUp(); strings.Join(got, "|") != " first |second" {
		t.Fatalf("cleared follow-ups = %q", got)
	}
	if got := loop.ClearFollowUp(); len(got) != 0 {
		t.Fatalf("second clear returned %q", got)
	}
	if got := loop.PendingFollowUp(); len(got) != 0 {
		t.Fatalf("cleared queue still has %q", got)
	}
}

func TestSteeringQueueRestoredWhenPersistenceFails(t *testing.T) {
	store, _, _, loop, _ := testSetup(t)
	if _, err := store.AppendMessage(userMsg("initial")); err != nil {
		t.Fatal(err)
	}
	appendBrokenSessionTail(t, store.File())
	loop.Steer("preserve this")
	if delivered, err := loop.deliverQueuedSteering(); delivered || err == nil {
		t.Fatalf("delivery = %v, %v; want a reported storage failure", delivered, err)
	}
	if got := loop.PendingSteering(); len(got) != 1 || got[0] != "preserve this" {
		t.Fatalf("failed steering delivery lost message: %q", got)
	}
}

func TestFollowUpQueueRestoredWhenPersistenceFails(t *testing.T) {
	store, _, _, loop, _ := testSetup(t)
	if _, err := store.AppendMessage(userMsg("initial")); err != nil {
		t.Fatal(err)
	}
	appendBrokenSessionTail(t, store.File())
	loop.FollowUp("preserve this")
	if delivered, err := loop.deliverFollowUp(); delivered || err == nil {
		t.Fatalf("delivery = (%v, %v), want storage failure", delivered, err)
	}
	if got := loop.PendingFollowUp(); len(got) != 1 || got[0] != "preserve this" {
		t.Fatalf("failed follow-up delivery lost message: %q", got)
	}
}

func appendBrokenSessionTail(t *testing.T, path string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(`{"broken":`); err != nil {
		_ = f.Close()
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestContinuePendingIsANoOpWithoutQueuedInput(t *testing.T) {
	store, _, fake, loop, _ := testSetup(t)
	before := len(store.Entries())
	if err := loop.ContinuePending(context.Background(), Config{Model: modelMeta(), Tools: toolDefs()}); err != nil {
		t.Fatal(err)
	}
	if fake.CallCount() != 0 || len(store.Entries()) != before || loop.Running() {
		t.Fatalf("empty continuation had effects: requests=%d entries=%d running=%v", fake.CallCount(), len(store.Entries()), loop.Running())
	}
}

func TestContinuePendingDeliversQueuedFollowUp(t *testing.T) {
	store, _, fake, loop, _ := testSetup(t)
	fake.Responses = []*provider.Response{{Text: "follow-up answer", StopReason: "stop"}}
	loop.FollowUp("queued after settlement")

	if err := loop.ContinuePending(context.Background(), Config{Model: modelMeta(), Tools: toolDefs()}); err != nil {
		t.Fatal(err)
	}
	requests := fake.Requests()
	if len(requests) != 1 || len(requests[0].Messages) == 0 {
		t.Fatalf("continuation requests = %+v", requests)
	}
	last := requests[0].Messages[len(requests[0].Messages)-1]
	if last.Role != "user" || len(last.Content) != 1 || last.Content[0].Text != "queued after settlement" {
		t.Fatalf("queued input was not delivered first: %+v", requests[0].Messages)
	}
	if pending := loop.PendingFollowUp(); len(pending) != 0 {
		t.Fatalf("queued input remains pending: %q", pending)
	}
	if loop.Running() {
		t.Fatal("run remained active after completion")
	}
	if len(store.Entries()) == 0 {
		t.Fatal("queued follow-up was not persisted")
	}
}

func TestCompactAndResumeSummarizesBeforeResuming(t *testing.T) {
	store, _, fake, loop, _ := testSetup(t)
	if _, err := store.AppendMessage(userMsg("original task")); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AppendMessage(session.AgentMessage{Role: "assistant", Assistant: &session.AssistantMessage{
		Role: "assistant", Content: []session.ContentBlock{{Type: "text", Text: &session.TextContent{Type: "text", Text: "completed work"}}},
	}}); err != nil {
		t.Fatal(err)
	}
	fake.Responses = []*provider.Response{
		{Text: "checkpoint", StopReason: "stop"},
		{Text: "continued", StopReason: "stop"},
	}
	cfg := Config{Model: modelMeta(), Tools: toolDefs(), KeepRecentTokens: 0}
	if err := loop.CompactAndResume(context.Background(), cfg, ""); err != nil {
		t.Fatal(err)
	}
	requests := fake.Requests()
	if len(requests) != 2 {
		t.Fatalf("provider requests = %d, want summary then continuation", len(requests))
	}
	if !strings.Contains(requests[0].Messages[0].Content[0].Text, "original task") {
		t.Fatalf("summary request omitted prior task: %+v", requests[0].Messages)
	}
	last := requests[1].Messages[len(requests[1].Messages)-1]
	if last.Role != "user" || !strings.Contains(last.Content[0].Text, "Continue the current task from the compacted context") {
		t.Fatalf("continuation request did not follow compaction: %+v", requests[1].Messages)
	}
	compactions := 0
	for _, entry := range store.Entries() {
		if entry.Kind == "compaction" {
			compactions++
		}
	}
	if compactions != 1 {
		t.Fatalf("compaction entries = %d, want 1", compactions)
	}
}
