//go:build !windows

package app

import (
	"testing"
	"time"

	"psi/internal/agent"
	"psi/internal/pibridge"
	pibridgepb "psi/internal/pibridge/pb"
)

func reloadRequest(id, session, leaf, provider, model string, runID, ctxRev *uint64) *pibridgepb.PhiToPi {
	req := &pibridgepb.ReloadRequest{
		RequestId:         id,
		ExpectedSessionId: session,
		ExpectedLeafId:    leaf,
	}
	if provider != "" || model != "" {
		req.ExpectedModel = &pibridgepb.ModelSnapshot{Provider: provider, Model: model}
	}
	req.ExpectedRunId, req.ExpectedContextRevision = runID, ctxRev
	return &pibridgepb.PhiToPi{Payload: &pibridgepb.PhiToPi_ReloadRequest{ReloadRequest: req}}
}

// nextReloadResult waits for the next unread dispatch acknowledgment.
func nextReloadResult(t *testing.T, f *bridgePhi, cursor *int) *pibridgepb.ReloadAck {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		f.mu.Lock()
		for *cursor < len(f.frames) {
			frame := f.frames[*cursor]
			*cursor++
			if frame.GetReloadAck() != nil {
				f.mu.Unlock()
				return frame.GetReloadAck()
			}
		}
		f.mu.Unlock()
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("reload ack did not arrive")
	return nil
}

func reloadFixture(t *testing.T) (*runtime, *bridgePhi, *bridgeHandler, *int) {
	t.Helper()
	r := testRuntime(t)
	r.generation = 1
	r.state = StateIdle
	f, h := startBridgeRuntime(t, r)
	dispatched := new(int)
	h.reloadRun = func() error {
		*dispatched++
		return nil
	}
	return r, f, h, dispatched
}

func liveReloadFrame(r *runtime, id string) *pibridgepb.PhiToPi {
	return reloadRequest(id, r.store.Header().ID, r.store.LeafID(), r.ov.Provider, r.ov.Model, nil, nil)
}

func TestBridgeReloadDispatchesNatively(t *testing.T) {
	r, f, h, dispatched := reloadFixture(t)
	cursor := 0
	f.inject <- liveReloadFrame(r, "2")
	h.handle(nextRequest(t, r.bridge), false)
	if got := nextReloadResult(t, f, &cursor); got.GetRequestId() != "2" ||
		got.GetOutcome() != pibridgepb.ReloadDispatchOutcome_RELOAD_DISPATCH_OUTCOME_DISPATCHED {
		t.Fatalf("ack = %+v, want dispatched 2", got)
	}
	if *dispatched != 1 {
		t.Fatalf("native runs = %d, want 1", *dispatched)
	}
	// The latch releases on completion and the high-water mark sticks, so a
	// later readiness sample retires the operation server-side.
	if h.reload != nil || r.reloadPending {
		t.Fatal("latch outlived the synchronous reload")
	}
	if r.reloadHigh != 2 {
		t.Fatalf("high water = %d, want 2", r.reloadHigh)
	}
}

func TestBridgeReloadRefusesDuplicateAndStale(t *testing.T) {
	r, f, h, dispatched := reloadFixture(t)
	cursor := 0
	refuse := func(frame *pibridgepb.PhiToPi, runBusy bool) *pibridgepb.ReloadAck {
		f.inject <- frame
		h.handle(nextRequest(t, r.bridge), runBusy)
		return nextReloadResult(t, f, &cursor)
	}
	if got := refuse(liveReloadFrame(r, "2"), false); got.GetOutcome() != pibridgepb.ReloadDispatchOutcome_RELOAD_DISPATCH_OUTCOME_DISPATCHED {
		t.Fatalf("first reload = %+v, want dispatched", got)
	}
	// The same ID may never dispatch twice, and older IDs never revive.
	for _, id := range []string{"2", "1"} {
		if got := refuse(liveReloadFrame(r, id), false); got.GetReason() != "duplicate" {
			t.Fatalf("id %s = %+v, want duplicate", id, got)
		}
	}
	// A non-canonical spelling is never a different operation.
	for _, id := range []string{"03", "0", "+4", "x", ""} {
		if got := refuse(liveReloadFrame(r, id), false); got.GetReason() != "stale" {
			t.Fatalf("id %q = %+v, want stale", id, got)
		}
	}
	// A foreign identity is stale, never guessed at.
	session, leaf := r.store.Header().ID, r.store.LeafID()
	for _, frame := range []*pibridgepb.PhiToPi{
		reloadRequest("3", "other", leaf, r.ov.Provider, r.ov.Model, nil, nil),
		reloadRequest("4", session, "other", r.ov.Provider, r.ov.Model, nil, nil),
		reloadRequest("5", session, leaf, "other", r.ov.Model, nil, nil),
	} {
		if got := refuse(frame, false); got.GetReason() != "stale" {
			t.Fatalf("foreign identity = %+v, want stale", got)
		}
	}
	// Pinned counters that moved on read stale; absent counters pin nothing.
	moved, behind := r.bridge.RunID()+1, r.contextRev+1
	for _, frame := range []*pibridgepb.PhiToPi{
		reloadRequest("6", session, leaf, r.ov.Provider, r.ov.Model, &moved, nil),
		reloadRequest("7", session, leaf, r.ov.Provider, r.ov.Model, nil, &behind),
	} {
		if got := refuse(frame, false); got.GetReason() != "stale" {
			t.Fatalf("moved counter = %+v, want stale", got)
		}
	}
	// Matching pinned counters dispatch.
	runID, ctxRev := r.bridge.RunID(), r.contextRev
	if got := refuse(reloadRequest("8", session, leaf, r.ov.Provider, r.ov.Model, &runID, &ctxRev), false); got.GetOutcome() != pibridgepb.ReloadDispatchOutcome_RELOAD_DISPATCH_OUTCOME_DISPATCHED {
		t.Fatalf("pinned reload = %+v, want dispatched", got)
	}
	if *dispatched != 2 {
		t.Fatalf("native runs = %d, want 2", *dispatched)
	}
}

func TestBridgeReloadRefusesBusyQueuedPendingAndActive(t *testing.T) {
	r, f, h, dispatched := reloadFixture(t)
	cursor := 0
	refuse := func(frame *pibridgepb.PhiToPi, runBusy bool) *pibridgepb.ReloadAck {
		f.inject <- frame
		h.handle(nextRequest(t, r.bridge), runBusy)
		return nextReloadResult(t, f, &cursor)
	}

	r.state = StateWaiting
	if got := refuse(liveReloadFrame(r, "5"), true); got.GetReason() != "busy" {
		t.Fatalf("busy = %+v, want busy", got)
	}
	r.state = StateIdle

	loop := &agent.Loop{}
	loop.FollowUp("later")
	h.activeLoop = loop
	if got := refuse(liveReloadFrame(r, "6"), false); got.GetReason() != "queued" {
		t.Fatalf("queued = %+v, want queued", got)
	}
	h.activeLoop = nil

	r.compactActive = true
	if got := refuse(liveReloadFrame(r, "7"), false); got.GetReason() != "compaction-active" {
		t.Fatalf("active = %+v, want compaction-active", got)
	}
	r.compactActive = false

	h.prep = &bridgePrep{request: pibridge.Request{}, done: make(chan struct{})}
	if got := refuse(liveReloadFrame(r, "8"), false); got.GetReason() != "busy" {
		t.Fatalf("prep = %+v, want busy", got)
	}
	close(h.prep.done)
	h.prep = nil

	h.compact = &bridgeCompact{id: 9}
	if got := refuse(liveReloadFrame(r, "9"), false); got.GetReason() != "pending" {
		t.Fatalf("compact pending = %+v, want pending", got)
	}
	h.compact = nil

	h.reload = &bridgeReload{id: 10}
	if got := refuse(liveReloadFrame(r, "11"), false); got.GetReason() != "pending" {
		t.Fatalf("reload pending = %+v, want pending", got)
	}
	h.reload = nil

	r.selector = NewSelector(nil, "")
	if got := refuse(liveReloadFrame(r, "12"), false); got.GetReason() != "unavailable" {
		t.Fatalf("selector = %+v, want unavailable", got)
	}
	r.selector = nil

	h.reloadRun = nil
	if got := refuse(liveReloadFrame(r, "13"), false); got.GetReason() != "unavailable" {
		t.Fatalf("missing runner = %+v, want unavailable", got)
	}
	if *dispatched != 0 {
		t.Fatalf("native runs = %d after refusals, want 0", *dispatched)
	}
}

// The hello snapshot carries reload readiness with explicit counters, so
// Phi's Reload button enables against a live pane.
func TestBridgeReloadReadinessPublished(t *testing.T) {
	r := testRuntime(t)
	r.generation = 1
	r.state = StateIdle
	f, _ := startBridgeRuntime(t, r)
	deadline := time.Now().Add(5 * time.Second)
	for {
		f.mu.Lock()
		hellos := append([]*pibridgepb.PiToPhi(nil), f.hellos...)
		f.mu.Unlock()
		if len(hellos) > 0 {
			ready := hellos[0].GetHello().GetCurrentState().GetReloadReadiness()
			if ready == nil {
				t.Fatal("hello carries no reload readiness")
			}
			if ready.RunId == nil || ready.ContextRevision == nil || ready.HighWater == nil {
				t.Fatalf("counters not present: %+v", ready)
			}
			if ready.GetReloadPending() {
				t.Fatal("fresh pane reads reload-pending")
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("hello was not sent")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// Every user-intent funnel and every compaction boundary moves the fence.
func TestContextRevMovesWithContext(t *testing.T) {
	r := testRuntime(t)
	if r.contextRev != 0 {
		t.Fatalf("fresh rev = %d", r.contextRev)
	}
	r.bumpContextRev()
	if r.contextRev != 1 {
		t.Fatalf("rev = %d, want 1", r.contextRev)
	}
}
