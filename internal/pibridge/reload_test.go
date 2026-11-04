//go:build !windows

package pibridge

import (
	"context"
	"testing"

	pibridgepb "psi/internal/pibridge/pb"
)

// A reload request dispatches by request id and carries the pinned identity,
// with presence for the optional counters.
func TestReloadRequestDispatch(t *testing.T) {
	c := &Client{}
	runID, ctxRev := uint64(3), uint64(9)
	msg := &pibridgepb.PhiToPi{Payload: &pibridgepb.PhiToPi_ReloadRequest{
		ReloadRequest: &pibridgepb.ReloadRequest{
			RequestId:               "7",
			ExpectedSessionId:       "s",
			ExpectedLeafId:          "l",
			ExpectedModel:           &pibridgepb.ModelSnapshot{Provider: "p", Model: "m"},
			ExpectedRunId:           &runID,
			ExpectedContextRevision: &ctxRev,
		},
	}}
	out := newOutbox()
	defer out.close()
	req, ok := c.dispatch(msg, context.Background(), out, 1)
	if !ok || req.Kind() != RequestReload || req.RequestID() != "7" {
		t.Fatalf("dispatch = %v %q, want reload 7", ok, req.Kind())
	}
	expect := req.ReloadExpectation()
	if expect.Session != "s" || expect.Leaf != "l" || !expect.HasModel ||
		expect.Model.Provider != "p" || expect.Model.Model != "m" {
		t.Fatalf("identity = %+v", expect)
	}
	if !expect.HasRunID || expect.RunID != 3 || !expect.HasContextRev || expect.ContextRev != 9 {
		t.Fatalf("counters = %+v", expect)
	}
	// Absent counters pin nothing.
	bare, ok := c.dispatch(&pibridgepb.PhiToPi{Payload: &pibridgepb.PhiToPi_ReloadRequest{
		ReloadRequest: &pibridgepb.ReloadRequest{RequestId: "8"},
	}}, context.Background(), out, 1)
	if !ok {
		t.Fatal("bare reload did not dispatch")
	}
	if bareExpect := bare.ReloadExpectation(); bareExpect.HasRunID || bareExpect.HasContextRev || bareExpect.HasModel {
		t.Fatalf("absent counters read present: %+v", bareExpect)
	}
}

// The acknowledgment reports entry or refusal with the contract vocabulary:
// unknown reasons collapse to empty, never an error.
func TestReloadAckShapes(t *testing.T) {
	c := &Client{}
	out := newOutbox()
	defer out.close()
	req, _ := c.dispatch(&pibridgepb.PhiToPi{Payload: &pibridgepb.PhiToPi_ReloadRequest{
		ReloadRequest: &pibridgepb.ReloadRequest{RequestId: "7"},
	}}, context.Background(), out, 1)
	if !req.ReplyReload(true, "anything") {
		t.Fatal("dispatch ack rejected")
	}
	select {
	case frame := <-out.ch:
		ack := frame.GetReloadAck()
		if ack.GetRequestId() != "7" ||
			ack.GetOutcome() != pibridgepb.ReloadDispatchOutcome_RELOAD_DISPATCH_OUTCOME_DISPATCHED ||
			ack.GetReason() != "" {
			t.Fatalf("dispatch ack = %+v", ack)
		}
	default:
		t.Fatal("dispatch ack never queued")
	}
	if !req.ReplyReload(false, "bogus-reason") {
		t.Fatal("refusal rejected")
	}
	select {
	case frame := <-out.ch:
		ack := frame.GetReloadAck()
		if ack.GetOutcome() != pibridgepb.ReloadDispatchOutcome_RELOAD_DISPATCH_OUTCOME_REFUSED || ack.GetReason() != "" {
			t.Fatalf("refusal = %+v", ack)
		}
	default:
		t.Fatal("refusal never queued")
	}
	// An unnegotiated capability answers unavailable without host work.
	if !req.ReplyUnavailable() {
		t.Fatal("unavailable refused")
	}
}

// The snapshot carries reload readiness with explicit counter presence, so
// Phi never reads an absent sample as idle.
func TestReloadReadinessPresence(t *testing.T) {
	c := &Client{}
	c.mu.Lock()
	empty := c.snapshotLocked("")
	c.mu.Unlock()
	if empty.GetReloadReadiness() != nil {
		t.Fatal("absent readiness serialized as present")
	}
	c.mu.Lock()
	c.snap.HasReload = true
	c.snap.Reload = ReloadReadiness{Idle: true, HasHighWater: true, HighWater: 12}
	ready := c.snapshotLocked("")
	c.mu.Unlock()
	got := ready.GetReloadReadiness()
	if got == nil || !got.GetIdle() || got.RunId != nil || got.ContextRevision != nil {
		t.Fatalf("readiness = %+v", got)
	}
	if got.HighWater == nil || *got.HighWater != 12 {
		t.Fatalf("high water = %+v", got.HighWater)
	}
}

// RunID is safe on a nil client and tracks the logical run counter.
func TestClientRunID(t *testing.T) {
	var nilClient *Client
	if nilClient.RunID() != 0 {
		t.Fatal("nil client run id is not zero")
	}
	c := &Client{wake: make(chan struct{}, 1)}
	c.RunStarted()
	c.RunStarted()
	if c.RunID() != 1 {
		t.Fatalf("run id = %d, want 1", c.RunID())
	}
	c.RunSettled(OutcomeCompleted)
	c.RunStarted()
	if c.RunID() != 2 {
		t.Fatalf("run id after settle = %d, want 2", c.RunID())
	}
}
