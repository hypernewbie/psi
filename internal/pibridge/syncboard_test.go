//go:build !windows

package pibridge

import (
	"context"
	"testing"
	"time"

	pibridgepb "psi/internal/pibridge/pb"
)

// One reverse-direction call reaches Phi and its answer resolves the caller,
// correlated by the client-allocated id and bound to the stream identity.
func TestSyncboardCallRoundTrip(t *testing.T) {
	f := startCapabilityPhi(t)
	c := newTestClient(t, f, Snapshot{Generation: 1, SessionID: "session-sb"})
	defer c.Close()
	waitSyncboardAccepted(t, c)

	type outcome struct {
		res SyncboardResult
		err error
	}
	done := make(chan outcome, 1)
	go func() {
		res, err := c.CallSyncboard(context.Background(), pibridgepb.SyncboardOperation_SYNCBOARD_OPERATION_GET, "k", nil)
		done <- outcome{res, err}
	}()

	frame := f.waitFrame(t, "syncboard request", func(frame *pibridgepb.PiToPhi) bool {
		return frame.GetSyncboardRequest() != nil
	})
	req := frame.GetSyncboardRequest()
	if req.GetOperation() != pibridgepb.SyncboardOperation_SYNCBOARD_OPERATION_GET || req.GetKey() != "k" || len(req.GetValueJson()) != 0 {
		t.Fatalf("request mismatch: %+v", req)
	}
	if frame.GetRuntimeGeneration() != 1 || frame.GetPaneId() == "" || frame.GetProcessId() == "" {
		t.Fatalf("request lost its stream identity: %+v", frame)
	}

	f.inject <- &pibridgepb.PhiToPi{Payload: &pibridgepb.PhiToPi_SyncboardResponse{
		SyncboardResponse: &pibridgepb.SyncboardResponse{
			RequestId:  req.GetRequestId(),
			Outcome:    pibridgepb.SyncboardOutcome_SYNCBOARD_OUTCOME_HTTP_RESPONSE,
			HttpStatus: 200,
			BodyJson:   []byte(`{"key":"k","value":"v","created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-01T00:00:00Z"}`),
		},
	}}

	select {
	case out := <-done:
		if out.err != nil {
			t.Fatalf("call failed: %v", out.err)
		}
		if out.res.Outcome != pibridgepb.SyncboardOutcome_SYNCBOARD_OUTCOME_HTTP_RESPONSE || out.res.HTTPStatus != 200 {
			t.Fatalf("result mismatch: %+v", out.res)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("call was not resolved")
	}
}

// Unknown, duplicate, and late responses are dropped: only the matching
// waiter consumes a receipt.
func TestSyncboardUnknownResponseDropped(t *testing.T) {
	f := startCapabilityPhi(t)
	c := newTestClient(t, f, Snapshot{Generation: 1, SessionID: "session-sb-drop"})
	defer c.Close()
	waitSyncboardAccepted(t, c)

	f.inject <- &pibridgepb.PhiToPi{Payload: &pibridgepb.PhiToPi_SyncboardResponse{
		SyncboardResponse: &pibridgepb.SyncboardResponse{
			RequestId: "999",
			Outcome:   pibridgepb.SyncboardOutcome_SYNCBOARD_OUTCOME_UNKNOWN,
			Reason:    "timeout",
		},
	}}
	// A bogus receipt must not break the next real call.
	done := make(chan error, 1)
	go func() {
		_, err := c.CallSyncboard(context.Background(), pibridgepb.SyncboardOperation_SYNCBOARD_OPERATION_LIST, "", nil)
		done <- err
	}()
	frame := f.waitFrame(t, "syncboard list request", func(frame *pibridgepb.PiToPhi) bool {
		return frame.GetSyncboardRequest() != nil
	})
	f.inject <- &pibridgepb.PhiToPi{Payload: &pibridgepb.PhiToPi_SyncboardResponse{
		SyncboardResponse: &pibridgepb.SyncboardResponse{
			RequestId:  frame.GetSyncboardRequest().GetRequestId(),
			Outcome:    pibridgepb.SyncboardOutcome_SYNCBOARD_OUTCOME_HTTP_RESPONSE,
			HttpStatus: 200,
			BodyJson:   []byte(`[]`),
		},
	}}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("call after bogus receipt failed: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("call after bogus receipt was not resolved")
	}
}

// Stream loss fails every waiter as unknown: requests are never replayed,
// and the caller must read before acting, never retry blindly.
func TestSyncboardStreamLossFailsWaiters(t *testing.T) {
	f := startCapabilityPhi(t)
	c := newTestClient(t, f, Snapshot{Generation: 1, SessionID: "session-sb-loss"})
	defer c.Close()
	waitSyncboardAccepted(t, c)

	done := make(chan error, 1)
	go func() {
		_, err := c.CallSyncboard(context.Background(), pibridgepb.SyncboardOperation_SYNCBOARD_OPERATION_GET, "k", nil)
		done <- err
	}()
	f.waitFrame(t, "syncboard request", func(frame *pibridgepb.PiToPhi) bool {
		return frame.GetSyncboardRequest() != nil
	})
	// Cut the stream after one server message. The injected request is
	// harmless; the cut is what the waiter must observe.
	f.cutAfterSend = 1
	f.inject <- &pibridgepb.PhiToPi{Payload: &pibridgepb.PhiToPi_StateRequest{
		StateRequest: &pibridgepb.StateRequest{RequestId: "state-after-cut"},
	}}
	select {
	case err := <-done:
		sbErr, ok := err.(*SyncboardError)
		if !ok || sbErr.Outcome != "unknown" {
			t.Fatalf("loss was not an unknown failure: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("waiter survived stream loss")
	}
}

// The fifth concurrent call is refused while four are in flight. Closing the
// client releases every waiter as unknown.
func TestSyncboardPendingBound(t *testing.T) {
	f := startCapabilityPhi(t)
	c := newTestClient(t, f, Snapshot{Generation: 1, SessionID: "session-sb-busy"})
	defer c.Close()
	waitSyncboardAccepted(t, c)

	release := make(chan error, syncboardPendingMax)
	for i := 0; i < syncboardPendingMax; i++ {
		go func() {
			_, err := c.CallSyncboard(context.Background(), pibridgepb.SyncboardOperation_SYNCBOARD_OPERATION_GET, "k", nil)
			release <- err
		}()
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		c.sbMu.Lock()
		n := len(c.sbWaiters)
		c.sbMu.Unlock()
		if n == syncboardPendingMax {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	c.sbMu.Lock()
	n := len(c.sbWaiters)
	c.sbMu.Unlock()
	if n != syncboardPendingMax {
		t.Fatalf("in-flight calls = %d, want %d", n, syncboardPendingMax)
	}
	if _, err := c.CallSyncboard(context.Background(), pibridgepb.SyncboardOperation_SYNCBOARD_OPERATION_GET, "k", nil); err == nil {
		t.Fatal("fifth concurrent call was admitted")
	} else if sbErr, ok := err.(*SyncboardError); !ok || sbErr.Outcome != "not-sent" || sbErr.Reason != "busy" {
		t.Fatalf("fifth call was not a busy refusal: %v", err)
	}
}

// Without the negotiated capability the tool path fails before any wire
// work: nothing is sent and no id is burned.
func TestSyncboardUnnegotiatedFailsFast(t *testing.T) {
	f := startCapabilityPhi(t)
	f.caps = []string{capLifecycle, capState}
	c := newTestClient(t, f, Snapshot{Generation: 1, SessionID: "session-sb-off"})
	defer c.Close()

	// The handshake completes (an unnegotiated request is refused without
	// host work), but syncboard stays off.
	f.inject <- modelsRequest("models-probe")
	f.waitFrame(t, "unavailable models response", func(frame *pibridgepb.PiToPhi) bool {
		return frame.GetModelsResponse() != nil
	})
	if c.SyncboardAccepted() {
		t.Fatal("unnegotiated capability reads accepted")
	}
	if _, err := c.CallSyncboard(context.Background(), pibridgepb.SyncboardOperation_SYNCBOARD_OPERATION_LIST, "", nil); err == nil {
		t.Fatal("unnegotiated call was admitted")
	} else if sbErr, ok := err.(*SyncboardError); !ok || sbErr.Outcome != "not-sent" {
		t.Fatalf("unnegotiated call was not a not-sent failure: %v", err)
	}
	select {
	case req := <-c.Requests():
		t.Fatalf("reverse-direction call reached the host queue: %s", req.Kind())
	case <-time.After(100 * time.Millisecond):
	}
	_, _, frames, _ := f.snapshot()
	for _, frame := range frames {
		if frame.GetSyncboardRequest() != nil {
			t.Fatal("unnegotiated call reached the wire")
		}
	}
}

func waitSyncboardAccepted(t *testing.T, c *Client) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if c.SyncboardAccepted() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("syncboard capability was not negotiated")
}
