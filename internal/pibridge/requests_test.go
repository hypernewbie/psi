//go:build !windows

package pibridge

import (
	"context"
	"errors"
	"testing"
	"time"

	"google.golang.org/grpc/metadata"
	"google.golang.org/protobuf/proto"

	pibridgepb "psi/internal/pibridge/pb"
)

// startCapabilityPhi starts the shared fake Phi with request injection enabled.
func startCapabilityPhi(t *testing.T) *fakePhi {
	t.Helper()
	f := startFakePhi(t)
	f.inject = make(chan *pibridgepb.PhiToPi, 8)
	return f
}

func modelsRequest(id string) *pibridgepb.PhiToPi {
	return &pibridgepb.PhiToPi{Payload: &pibridgepb.PhiToPi_ModelsRequest{
		ModelsRequest: &pibridgepb.ModelsRequest{RequestId: id},
	}}
}

// waitFrame waits for a client frame matching pred.
func (f *fakePhi) waitFrame(t *testing.T, what string, pred func(*pibridgepb.PiToPhi) bool) *pibridgepb.PiToPhi {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		_, _, frames, _ := f.snapshot()
		for _, frame := range frames {
			if pred(frame) {
				return frame
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
	return nil
}

// A capability request reaches the host once, and the host's answer is
// correlated by request id and bound to the stream's identity.
func TestModelsRequestRoundTrip(t *testing.T) {
	f := startCapabilityPhi(t)
	c := newTestClient(t, f, Snapshot{Generation: 2, SessionID: "session-m"})
	defer c.Close()

	f.inject <- modelsRequest("req-models")

	var req Request
	select {
	case req = <-c.Requests():
	case <-time.After(5 * time.Second):
		t.Fatal("models request was not forwarded to the host")
	}
	if req.Kind() != RequestModels || req.RequestID() != "req-models" {
		t.Fatalf("unexpected request: %s %q", req.Kind(), req.RequestID())
	}
	if !req.ReplyModels([]ModelRef{{Provider: "anthropic", Model: "a"}}, false) {
		t.Fatal("reply rejected")
	}

	frame := f.waitFrame(t, "models response", func(frame *pibridgepb.PiToPhi) bool {
		return frame.GetModelsResponse() != nil
	})
	res := frame.GetModelsResponse()
	if res.GetRequestId() != "req-models" || res.GetUnavailable() || len(res.GetModels()) != 1 {
		t.Fatalf("response mismatch: %+v", res)
	}
	if got := res.GetModels()[0]; got.GetProvider() != "anthropic" || got.GetModel() != "a" {
		t.Fatalf("entry mismatch: %+v", got)
	}
	if frame.GetRuntimeGeneration() != 2 || frame.GetPaneId() == "" || frame.GetProcessId() == "" {
		t.Fatalf("reply lost its stream identity: %+v", frame)
	}
}

// Phi can accept a subset of the offered capabilities. A request outside the
// negotiated set must answer unavailable instead of invoking host work.
func TestUnnegotiatedCapabilityAnswersUnavailable(t *testing.T) {
	f := startCapabilityPhi(t)
	f.caps = []string{capLifecycle, capState}
	c := newTestClient(t, f, Snapshot{Generation: 1, SessionID: "session-c"})
	defer c.Close()

	f.inject <- modelsRequest("req-off")
	frame := f.waitFrame(t, "unavailable models response", func(frame *pibridgepb.PiToPhi) bool {
		return frame.GetModelsResponse() != nil
	})
	if res := frame.GetModelsResponse(); res.GetRequestId() != "req-off" || !res.GetUnavailable() {
		t.Fatalf("unnegotiated request was not refused: %+v", res)
	}
	select {
	case req := <-c.Requests():
		t.Fatalf("unnegotiated request reached the host: %s", req.Kind())
	case <-time.After(100 * time.Millisecond):
	}
}

// The host queue is bounded: a flood is refused, never buffered without
// limit, and the refusal does not disturb lifecycle traffic.
func TestRequestQueueBoundedAndLifecycleSurvives(t *testing.T) {
	f := startCapabilityPhi(t)
	c := newTestClient(t, f, Snapshot{Generation: 1, SessionID: "session-q"})
	defer c.Close()

	const flood = requestQueue + 4
	for i := 0; i < flood; i++ {
		f.inject <- modelsRequest("req-" + string(rune('a'+i)))
	}
	// Overflow answers arrive without host work: exactly the excess.
	deadline := time.Now().Add(5 * time.Second)
	for {
		_, _, frames, _ := f.snapshot()
		refusals := 0
		for _, frame := range frames {
			if res := frame.GetModelsResponse(); res != nil && res.GetUnavailable() {
				refusals++
			}
		}
		if refusals >= flood-requestQueue {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("only %d refusals for %d requests", refusals, flood)
		}
		time.Sleep(10 * time.Millisecond)
	}

	// A settlement still reaches Phi while the read queue is saturated.
	c.RunStarted()
	c.RunSettled(OutcomeCompleted)
	f.waitFrame(t, "settlement during queue pressure", func(frame *pibridgepb.PiToPhi) bool {
		return frame.GetAgentSettled() != nil
	})

	// Every queued request is still answerable. Replies are bounded too, so a
	// host that answers faster than Phi reads waits for room instead of
	// blocking the loop or growing without limit.
	for i := 0; i < requestQueue; i++ {
		var req Request
		select {
		case req = <-c.Requests():
		case <-time.After(5 * time.Second):
			t.Fatalf("queued request %d was lost", i)
		}
		deadline := time.Now().Add(5 * time.Second)
		for !req.ReplyModels(nil, false) {
			if time.Now().After(deadline) {
				t.Fatalf("reply %d never found room", i)
			}
			time.Sleep(2 * time.Millisecond)
		}
	}
}

// A handshake that binds another runtime must stop the stream: answering or
// retrying would publish this pane's state under a foreign identity.
func TestAcceptedIdentityMismatchStopsAndForwardsNothing(t *testing.T) {
	f := startCapabilityPhi(t)
	f.acceptRuntime = "someone-else"
	c := newTestClient(t, f, Snapshot{Generation: 1, SessionID: "session-i"})
	defer c.Close()

	f.waitFor(t, "a rejected handshake", func(connects int, hellos, _ []*pibridgepb.PiToPhi) bool {
		return connects >= 1 && len(hellos) >= 1
	})
	select {
	case req := <-c.Requests():
		t.Fatalf("request forwarded after a foreign handshake: %s", req.Kind())
	case <-time.After(300 * time.Millisecond):
	}
	connects, _, _, _ := f.snapshot()
	if connects != 1 {
		t.Fatalf("foreign handshake retried %d times instead of stopping", connects)
	}
}

// blockingStream never completes a Send until its stream context is cancelled.
type blockingStream struct {
	ctx context.Context
}

func (b *blockingStream) Send(*pibridgepb.PiToPhi) error {
	<-b.ctx.Done()
	return b.ctx.Err()
}
func (b *blockingStream) Recv() (*pibridgepb.PhiToPi, error) {
	<-b.ctx.Done()
	return nil, b.ctx.Err()
}
func (b *blockingStream) Header() (metadata.MD, error) { return nil, nil }
func (b *blockingStream) Trailer() metadata.MD         { return nil }
func (b *blockingStream) CloseSend() error             { return nil }
func (b *blockingStream) Context() context.Context     { return b.ctx }
func (b *blockingStream) SendMsg(any) error            { return nil }
func (b *blockingStream) RecvMsg(any) error            { return nil }

// A blocked write must fail locally and cancel the stream instead of hanging
// the loop forever.
func TestSendFrameTimeoutCancelsStream(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	stream := &blockingStream{ctx: ctx}
	start := time.Now()
	err := sendFrame(context.Background(), cancel, stream, &pibridgepb.PiToPhi{}, 50*time.Millisecond)
	if !errors.Is(err, errWriteTimeout) {
		t.Fatalf("got %v, want errWriteTimeout", err)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("blocked write took %s", elapsed)
	}
	if ctx.Err() == nil {
		t.Fatal("stream context was not cancelled")
	}
}

// A conversation that loses the ack must not replay the mutation: the next
// handshake reports the state it already installed.
func TestLostReplyDoesNotReplayRequest(t *testing.T) {
	f := startCapabilityPhi(t)
	c := newTestClient(t, f, Snapshot{Generation: 1, SessionID: "session-r"})
	defer c.Close()

	// The stream dies on the reply frame itself, so the receipt is lost
	// after the host already did the work.
	f.mu.Lock()
	f.cutAfter = 1
	f.mu.Unlock()
	f.inject <- modelsRequest("req-lost")

	select {
	case req := <-c.Requests():
		req.ReplyModels([]ModelRef{{Provider: "p", Model: "m"}}, false)
	case <-time.After(5 * time.Second):
		t.Fatal("request was not forwarded")
	}

	f.waitFor(t, "a reconnect", func(connects int, hellos, _ []*pibridgepb.PiToPhi) bool {
		return connects >= 2 && len(hellos) >= 2
	})
	// The reconnect carries state only; the request is not re-sent.
	_, _, frames, _ := f.snapshot()
	for _, frame := range frames {
		if res := frame.GetModelsResponse(); res != nil && res.GetRequestId() == "req-lost" {
			t.Fatal("lost receipt was replayed after reconnect")
		}
	}
	select {
	case req := <-c.Requests():
		t.Fatalf("request replayed to the host: %s", req.Kind())
	case <-time.After(300 * time.Millisecond):
	}
}

// Context telemetry is a measurement, not a guess: unknown stays absent, a
// known zero stays zero, and a percent needs both numbers.
func TestContextUsagePresence(t *testing.T) {
	cases := []struct {
		name      string
		snap      Snapshot
		wantUsage bool
		tokens    *float64
		window    *float64
		percent   *float64
	}{
		{name: "absent", snap: Snapshot{}},
		{name: "window only", snap: Snapshot{ContextWindow: 1000, HasContextWindow: true}, wantUsage: true, window: proto.Float64(1000)},
		{name: "known zero", snap: Snapshot{HasContextTokens: true, ContextWindow: 1000, HasContextWindow: true}, wantUsage: true,
			tokens: proto.Float64(0), window: proto.Float64(1000), percent: proto.Float64(0)},
		{name: "both", snap: Snapshot{ContextTokens: 250, HasContextTokens: true, ContextWindow: 1000, HasContextWindow: true}, wantUsage: true,
			tokens: proto.Float64(250), window: proto.Float64(1000), percent: proto.Float64(25)},
	}
	for _, tc := range cases {
		c := &Client{snap: tc.snap}
		got := c.snapshotLocked("").GetContextUsage()
		if !tc.wantUsage {
			if got != nil {
				t.Fatalf("%s: usage published without a measurement: %+v", tc.name, got)
			}
			continue
		}
		if got == nil {
			t.Fatalf("%s: usage missing", tc.name)
		}
		if !equalFloatPointer(got.Tokens, tc.tokens) ||
			!equalFloatPointer(got.ContextWindow, tc.window) ||
			!equalFloatPointer(got.Percent, tc.percent) {
			t.Fatalf("%s: got tokens=%v window=%v percent=%v", tc.name,
				got.Tokens, got.ContextWindow, got.Percent)
		}
	}
}

func equalFloatPointer(a, b *float64) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}
