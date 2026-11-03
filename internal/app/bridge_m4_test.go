//go:build !windows

package app

import (
	"context"
	"errors"
	"testing"
	"time"

	"psi/internal/agent"
	"psi/internal/pibridge"
	pibridgepb "psi/internal/pibridge/pb"
)

func compactRequest(id, session, leaf, provider, model string) *pibridgepb.PhiToPi {
	req := &pibridgepb.CompactRequest{
		RequestId:         id,
		ExpectedSessionId: session,
		ExpectedLeafId:    leaf,
	}
	if provider != "" || model != "" {
		req.ExpectedModel = &pibridgepb.ModelSnapshot{Provider: provider, Model: model}
	}
	return &pibridgepb.PhiToPi{Payload: &pibridgepb.PhiToPi_CompactRequest{CompactRequest: req}}
}

// nextCompactResult waits for the next unread terminal answer. The cursor makes
// each answer readable exactly once, the way Phi consumes the stream.
func nextCompactResult(t *testing.T, f *bridgePhi, cursor *int) *pibridgepb.CompactResult {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		f.mu.Lock()
		for *cursor < len(f.frames) {
			frame := f.frames[*cursor]
			*cursor++
			if frame.GetCompactResult() != nil {
				f.mu.Unlock()
				return frame.GetCompactResult()
			}
		}
		f.mu.Unlock()
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("compact result did not arrive")
	return nil
}

// admitCompact injects one request and returns the admitted operation, or the
// terminal result when admission refused.
func admitCompact(t *testing.T, r *runtime, f *bridgePhi, h *bridgeHandler, frame *pibridgepb.PhiToPi, cursor *int) (*bridgeCompact, *pibridgepb.CompactResult) {
	t.Helper()
	id, _ := canonicalDecimal(frame.GetCompactRequest().GetRequestId())
	f.inject <- frame
	h.handle(nextRequest(t, r.bridge), false)
	if h.compact != nil && h.compact.id == id {
		return h.compact, nil
	}
	return nil, nextCompactResult(t, f, cursor)
}

func compactFixture(t *testing.T) (*runtime, *bridgePhi, *bridgeHandler, *int) {
	t.Helper()
	r := testRuntime(t)
	r.generation = 1
	r.state = StateIdle
	f, h := startBridgeRuntime(t, r)
	dispatched := new(int)
	h.compactRun = func(string) error {
		*dispatched++
		return nil
	}
	return r, f, h, dispatched
}

func TestBridgeCompactRefusesDuplicateAndStale(t *testing.T) {
	r, f, h, dispatched := compactFixture(t)
	session, leaf := r.store.Header().ID, r.store.LeafID()
	cursor := 0
	request := func(id, expectSession, expectLeaf, provider, model string) *pibridgepb.PhiToPi {
		return compactRequest(id, expectSession, expectLeaf, provider, model)
	}
	frame := func(id string) *pibridgepb.PhiToPi {
		return compactRequest(id, session, leaf, r.ov.Provider, r.ov.Model)
	}

	op, refused := admitCompact(t, r, f, h, frame("2"), &cursor)
	if refused != nil || op == nil {
		t.Fatalf("first compact = %+v refused %+v, want admitted", op, refused)
	}
	if *dispatched != 1 {
		t.Fatalf("dispatched = %d, want 1", *dispatched)
	}
	h.resolveCompactRun(nil)
	if got := nextCompactResult(t, f, &cursor); got.GetOutcome() != pibridgepb.CompactOutcome_COMPACT_OUTCOME_COMPLETED {
		t.Fatalf("first result = %+v, want completed", got)
	}

	// The same ID may never dispatch twice, and older IDs never revive.
	if _, refused := admitCompact(t, r, f, h, frame("2"), &cursor); refused.GetReason() != "duplicate" {
		t.Fatalf("replayed id = %+v, want duplicate", refused)
	}
	if _, refused := admitCompact(t, r, f, h, frame("1"), &cursor); refused.GetReason() != "duplicate" {
		t.Fatalf("older id = %+v, want duplicate", refused)
	}
	if *dispatched != 1 {
		t.Fatalf("dispatched = %d after refusals, want 1", *dispatched)
	}

	// A non-canonical spelling is never a different operation.
	for _, id := range []string{"03", "0", "+4", " 4", "4 ", "x"} {
		if _, refused := admitCompact(t, r, f, h, frame(id), &cursor); refused.GetReason() != "stale" {
			t.Fatalf("id %q = %+v, want stale", id, refused)
		}
	}
	// A foreign identity is stale, never guessed at.
	for i, expect := range [][2]string{{"other", leaf}, {session, "other"}} {
		id := string(rune('3' + i))
		if _, refused := admitCompact(t, r, f, h, request(id, expect[0], expect[1], r.ov.Provider, r.ov.Model), &cursor); refused.GetReason() != "stale" {
			t.Fatalf("identity %v = %+v, want stale", expect, refused)
		}
	}
	if _, refused := admitCompact(t, r, f, h, request("6", session, leaf, "other", r.ov.Model), &cursor); refused.GetReason() != "stale" {
		t.Fatalf("foreign model = %+v, want stale", refused)
	}
}

func TestBridgeCompactRefusesBusyQueuedAndActive(t *testing.T) {
	r, f, h, dispatched := compactFixture(t)
	session, leaf := r.store.Header().ID, r.store.LeafID()
	cursor := 0
	frame := func(id string) *pibridgepb.PhiToPi {
		return compactRequest(id, session, leaf, r.ov.Provider, r.ov.Model)
	}

	// Busy: the loop owns a run right now.
	r.state = StateWaiting
	f.inject <- frame("5")
	h.handle(nextRequest(t, r.bridge), true)
	if got := nextCompactResult(t, f, &cursor); got.GetReason() != "busy" {
		t.Fatalf("busy = %+v, want busy", got)
	}
	r.state = StateIdle

	// Queued input must be delivered before a compaction rewrites context.
	loop := &agent.Loop{}
	loop.FollowUp("later")
	h.activeLoop = loop
	f.inject <- frame("6")
	h.handle(nextRequest(t, r.bridge), false)
	if got := nextCompactResult(t, f, &cursor); got.GetReason() != "queued" {
		t.Fatalf("queued = %+v, want queued", got)
	}
	h.activeLoop = nil

	// An active native compaction excludes another one.
	r.compactActive = true
	f.inject <- frame("7")
	h.handle(nextRequest(t, r.bridge), false)
	if got := nextCompactResult(t, f, &cursor); got.GetReason() != "compaction-active" {
		t.Fatalf("active = %+v, want compaction-active", got)
	}
	r.compactActive = false

	// In-flight bridge preparation can still install a configuration; a
	// compaction started beside it would summarize the wrong context.
	h.prep = &bridgePrep{request: pibridge.Request{}, done: make(chan struct{})}
	f.inject <- frame("8")
	h.handle(nextRequest(t, r.bridge), false)
	if got := nextCompactResult(t, f, &cursor); got.GetReason() != "busy" {
		t.Fatalf("prep = %+v, want busy", got)
	}
	close(h.prep.done)
	h.prep = nil

	if *dispatched != 0 {
		t.Fatalf("dispatched = %d after refusals, want 0", *dispatched)
	}
}

func TestBridgeCompactPendingBlocksSecondUntilResolved(t *testing.T) {
	r, f, h, dispatched := compactFixture(t)
	session, leaf := r.store.Header().ID, r.store.LeafID()
	cursor := 0
	frame := func(id string) *pibridgepb.PhiToPi {
		return compactRequest(id, session, leaf, r.ov.Provider, r.ov.Model)
	}
	op, refused := admitCompact(t, r, f, h, frame("10"), &cursor)
	if op == nil || refused != nil || !r.compactPending {
		t.Fatalf("admit = %+v %+v pending=%v", op, refused, r.compactPending)
	}
	// The second request is the next frame on the stream: the first is still
	// unresolved, so it is refused as pending instead of waiting.
	second, refused := admitCompact(t, r, f, h, frame("11"), &cursor)
	if refused.GetReason() != "pending" {
		t.Fatalf("second op=%+v refused=%+v, want pending", second, refused)
	}
	h.resolveCompactRun(nil)
	if r.compactPending {
		t.Fatal("latch survived a terminal outcome")
	}
	if got := nextCompactResult(t, f, &cursor); got.GetOutcome() != pibridgepb.CompactOutcome_COMPACT_OUTCOME_COMPLETED {
		t.Fatalf("result = %+v, want completed", got)
	}
	if *dispatched != 1 {
		t.Fatalf("dispatched = %d, want 1", *dispatched)
	}
}

func TestBridgeCompactOutcomesFollowTheRunResult(t *testing.T) {
	cases := []struct {
		name   string
		err    error
		want   pibridgepb.CompactOutcome
		reason string
	}{
		{"success", nil, pibridgepb.CompactOutcome_COMPACT_OUTCOME_COMPLETED, ""},
		{"cancelled", context.Canceled, pibridgepb.CompactOutcome_COMPACT_OUTCOME_CANCELLED, "cancelled"},
		{"no-work", &agent.CompactionNoWorkError{}, pibridgepb.CompactOutcome_COMPACT_OUTCOME_FAILED, "already-compacted"},
		{"error", errors.New("boom"), pibridgepb.CompactOutcome_COMPACT_OUTCOME_FAILED, "error"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r, f, h, _ := compactFixture(t)
			cursor := 0
			op, refused := admitCompact(t, r, f, h, compactRequest("20", r.store.Header().ID, r.store.LeafID(), r.ov.Provider, r.ov.Model), &cursor)
			if op == nil || refused != nil {
				t.Fatalf("admit = %+v %+v", op, refused)
			}
			h.resolveCompactRun(tc.err)
			got := nextCompactResult(t, f, &cursor)
			if got.GetOutcome() != tc.want || got.GetReason() != tc.reason {
				t.Fatalf("outcome = %v/%q, want %v/%q", got.GetOutcome(), got.GetReason(), tc.want, tc.reason)
			}
		})
	}
}

func TestBridgeCompactSynchronousFailureIsTerminal(t *testing.T) {
	r, f, h, _ := compactFixture(t)
	h.compactRun = func(string) error { return &agent.CompactionNoWorkError{} }
	cursor := 0

	f.inject <- compactRequest("30", r.store.Header().ID, r.store.LeafID(), r.ov.Provider, r.ov.Model)
	h.handle(nextRequest(t, r.bridge), false)

	got := nextCompactResult(t, f, &cursor)
	if got.GetOutcome() != pibridgepb.CompactOutcome_COMPACT_OUTCOME_FAILED || got.GetReason() != "already-compacted" {
		t.Fatalf("result = %+v, want failed/already-compacted", got)
	}
	if h.compact != nil || r.compactPending {
		t.Fatal("a refused dispatch must not leave the process latch pending")
	}
}
