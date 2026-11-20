//go:build !windows

package app

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/hypernewbie/psi/internal/agent"
	"github.com/hypernewbie/psi/internal/pibridge"
	pibridgepb "github.com/hypernewbie/psi/internal/pibridge/pb"
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
	f, h := startBridgeRuntimeV1(t, r)
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

func compactRequestV2(id, session, leaf, provider, model string, runID uint64, instructions string) *pibridgepb.PhiToPi {
	req := compactRequest(id, session, leaf, provider, model).GetCompactRequest()
	req.ExpectedRunId = &runID
	req.CustomInstructions = instructions
	return &pibridgepb.PhiToPi{Payload: &pibridgepb.PhiToPi_CompactRequest{CompactRequest: req}}
}

func compactRequestV2NoRun(id, session, leaf, provider, model string, instructions string) *pibridgepb.PhiToPi {
	req := compactRequest(id, session, leaf, provider, model).GetCompactRequest()
	req.CustomInstructions = instructions
	return &pibridgepb.PhiToPi{Payload: &pibridgepb.PhiToPi_CompactRequest{CompactRequest: req}}
}

func compactFixtureV2(t *testing.T) (*runtime, *bridgePhi, *bridgeHandler, *int) {
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

func TestBridgeCompactV2AdmitsActiveRunAndQueuedInput(t *testing.T) {
	r, f, h, dispatched := compactFixtureV2(t)
	r.bridge.RunStarted()
	r.state = StateWaiting
	r.queuedInputs = func() int { return 1 }
	session, leaf := r.store.Header().ID, r.store.LeafID()
	frame := compactRequestV2("1", session, leaf, r.ov.Provider, r.ov.Model, r.bridge.RunID(), "FOCUS_TEXT")
	f.inject <- frame
	h.handle(nextRequest(t, r.bridge), true)
	if h.compact == nil || !r.compactPending {
		t.Fatalf("v2 active compact was not admitted: %+v", h.compact)
	}
	if *dispatched != 1 {
		t.Fatalf("dispatched = %d, want 1", *dispatched)
	}
	h.resolveCompactRun(nil)
	if got := nextCompactResult(t, f, new(int)); got.GetOutcome() != pibridgepb.CompactOutcome_COMPACT_OUTCOME_COMPLETED {
		t.Fatalf("result = %+v", got)
	}
}

func TestBridgeCompactV2RefusesStaleRunAndIdentity(t *testing.T) {
	r, f, h, dispatched := compactFixtureV2(t)
	r.bridge.RunStarted()
	r.state = StateWaiting
	session, leaf := r.store.Header().ID, r.store.LeafID()
	cursor := 0

	// Wrong run id is stale and never reaches compactRun.
	f.inject <- compactRequestV2("1", session, leaf, r.ov.Provider, r.ov.Model, r.bridge.RunID()+1, "")
	h.handle(nextRequest(t, r.bridge), true)
	if got := nextCompactResult(t, f, &cursor); got.GetReason() != "stale" {
		t.Fatalf("wrong run = %+v, want stale", got)
	}
	// Wrong session, leaf, and model are stale under the correct run fence.
	for i, frame := range []*pibridgepb.PhiToPi{
		compactRequestV2("2", "other", leaf, r.ov.Provider, r.ov.Model, r.bridge.RunID(), ""),
		compactRequestV2("3", session, "other", r.ov.Provider, r.ov.Model, r.bridge.RunID(), ""),
		compactRequestV2("4", session, leaf, "other", r.ov.Model, r.bridge.RunID(), ""),
	} {
		f.inject <- frame
		h.handle(nextRequest(t, r.bridge), true)
		if got := nextCompactResult(t, f, &cursor); got.GetReason() != "stale" {
			t.Fatalf("identity %d = %+v, want stale", i, got)
		}
	}
	// A successor can own the worker slot before its RunStart updates the
	// bridge counter. An unchanged leaf is possible for bump, or before
	// the successor's first append. The old fence must still refuse.
	r.bridge.RunSettled(pibridge.OutcomeCompleted)
	h.runStarting = true
	f.inject <- compactRequestV2("5", session, leaf, r.ov.Provider, r.ov.Model, r.bridge.RunID(), "")
	h.handle(nextRequest(t, r.bridge), true)
	if got := nextCompactResult(t, f, &cursor); got.GetReason() != "stale" {
		t.Fatalf("request from previous run = %+v, want stale", got)
	}
	// A request delivered by the old stream can remain in the host queue
	// after the REPL replaces the runtime. The same session can be reopened
	// at the same leaf, with the same model and no intervening run.
	h.runStarting = false
	r.state = StateIdle
	f.inject <- compactRequestV2("6", session, leaf, r.ov.Provider, r.ov.Model, r.bridge.RunID(), "")
	queued := nextRequest(t, r.bridge)
	r.generation++
	h.handle(queued, false)
	if *dispatched != 0 {
		t.Fatalf("stale request reached compact admission: %d dispatches", *dispatched)
	}
	if got := nextCompactResult(t, f, &cursor); got.GetReason() != "stale" {
		t.Fatalf("previous generation = %+v, want stale", got)
	}
}

func TestBridgeCompactV2ClientRejectsMissingRunFence(t *testing.T) {
	r, f, h, dispatched := compactFixtureV2(t)
	session, leaf := r.store.Header().ID, r.store.LeafID()
	cursor := 0
	f.inject <- compactRequestV2NoRun("1", session, leaf, r.ov.Provider, r.ov.Model, "FOCUS_TEXT")
	if got := nextCompactResult(t, f, &cursor); got.GetReason() != "unavailable" {
		t.Fatalf("missing run fence = %+v, want unavailable", got)
	}
	if h.compact != nil || r.compactPending || *dispatched != 0 {
		t.Fatal("missing run fence reached the host")
	}
}

func TestBridgeCompactReceiptBinding(t *testing.T) {
	r, f, h, _ := compactFixtureV2(t)
	session, leaf := r.store.Header().ID, r.store.LeafID()
	cursor := 0
	f.inject <- compactRequestV2("10", session, leaf, r.ov.Provider, r.ov.Model, r.bridge.RunID(), "")
	h.handle(nextRequest(t, r.bridge), false)
	if h.compact == nil || !r.compactPending {
		t.Fatal("compact was not admitted")
	}
	// A second request during the held reservation is refused pending and
	// does not disturb the receipt.
	f.inject <- compactRequestV2("11", session, leaf, r.ov.Provider, r.ov.Model, r.bridge.RunID(), "")
	h.handle(nextRequest(t, r.bridge), false)
	if got := nextCompactResult(t, f, &cursor); got.GetReason() != "pending" {
		t.Fatalf("second request = %+v, want pending", got)
	}
	if h.compact == nil || !r.compactPending {
		t.Fatal("second request released the held receipt")
	}
	// A model error is one terminal failure and the owner can be reused.
	h.resolveCompactRun(errors.New("model failed"))
	if got := nextCompactResult(t, f, &cursor); got.GetOutcome() != pibridgepb.CompactOutcome_COMPACT_OUTCOME_FAILED || got.GetReason() != "error" {
		t.Fatalf("failure = %+v", got)
	}
	if h.compact != nil || r.compactPending {
		t.Fatal("failure left the receipt bound")
	}
	f.inject <- compactRequestV2("12", session, leaf, r.ov.Provider, r.ov.Model, r.bridge.RunID(), "")
	h.handle(nextRequest(t, r.bridge), false)
	if h.compact == nil || !r.compactPending {
		t.Fatal("owner was not reusable after failure")
	}
	h.resolveCompactRun(nil)
	if got := nextCompactResult(t, f, &cursor); got.GetOutcome() != pibridgepb.CompactOutcome_COMPACT_OUTCOME_COMPLETED {
		t.Fatalf("reused owner = %+v", got)
	}
}
