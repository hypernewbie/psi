//go:build !windows

package pibridge

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"

	pibridgepb "psi/internal/pibridge/pb"
)

type fakePhi struct {
	pibridgepb.UnimplementedIntegrationServer
	target string

	mu        sync.Mutex
	connects  int
	hellos    []*pibridgepb.PiToPhi
	frames    []*pibridgepb.PiToPhi
	md        []metadata.MD
	drops     int
	mutes     int
	reject    codes.Code
	cutAfter  int
	onFrame   func(srv pibridgepb.Integration_ConnectServer, frame *pibridgepb.PiToPhi)
	requestID string
	// caps overrides the accepted capability set; nil accepts everything the
	// client offered.
	caps []string
	// inject carries server messages to send after the handshake.
	inject chan *pibridgepb.PhiToPi
	// acceptRuntime overrides the runtime id echoed in Accepted.
	acceptRuntime string
	// cutAfterSend drops the stream after that many injected messages.
	cutAfterSend int
}

func startFakePhi(t *testing.T) *fakePhi {
	t.Helper()
	dir, err := os.MkdirTemp("", "pb")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	path := filepath.Join(dir, "bridge.sock")
	if len(path) >= 100 {
		t.Skipf("socket path too long for this host: %d bytes", len(path))
	}
	lis, err := net.Listen("unix", path)
	if err != nil {
		t.Skipf("unix listener unavailable: %v", err)
	}
	f := &fakePhi{target: "unix:" + path}
	gs := grpc.NewServer()
	pibridgepb.RegisterIntegrationServer(gs, f)
	go func() { _ = gs.Serve(lis) }()
	t.Cleanup(gs.Stop)
	return f
}

func (f *fakePhi) Connect(stream pibridgepb.Integration_ConnectServer) error {
	f.mu.Lock()
	f.connects++
	drops, mutes, reject := f.drops, f.mutes, f.reject
	if drops > 0 {
		f.drops--
	}
	if mutes > 0 {
		f.mutes--
	}
	md, _ := metadata.FromIncomingContext(stream.Context())
	f.md = append(f.md, md)
	requestID := f.requestID
	f.mu.Unlock()

	if reject != codes.OK {
		return status.Error(reject, "rejected")
	}
	if drops > 0 {
		return status.Error(codes.Unavailable, "drop")
	}
	first, err := stream.Recv()
	if err != nil {
		return err
	}
	f.mu.Lock()
	f.hellos = append(f.hellos, first)
	f.mu.Unlock()
	if mutes > 0 {
		return nil
	}
	f.mu.Lock()
	caps, runtimeID := f.caps, f.acceptRuntime
	f.mu.Unlock()
	if caps == nil {
		caps = first.GetHello().GetCapabilities()
	}
	if runtimeID == "" {
		runtimeID = first.GetHello().GetRuntimeId()
	}
	if err := stream.Send(&pibridgepb.PhiToPi{
		PaneId: first.GetPaneId(), ProcessId: first.GetProcessId(),
		RuntimeGeneration: first.GetRuntimeGeneration(),
		Payload: &pibridgepb.PhiToPi_Accepted{Accepted: &pibridgepb.Accepted{
			ProtocolVersion:   protocolVersion,
			Capabilities:      caps,
			RuntimeGeneration: first.GetRuntimeGeneration(),
			RuntimeId:         runtimeID,
		}},
	}); err != nil {
		return err
	}
	if requestID != "" {
		req := &pibridgepb.PhiToPi{
			PaneId: first.GetPaneId(), ProcessId: first.GetProcessId(), RuntimeGeneration: first.GetRuntimeGeneration(),
			Payload: &pibridgepb.PhiToPi_StateRequest{StateRequest: &pibridgepb.StateRequest{RequestId: requestID}},
		}
		if err := stream.Send(req); err != nil {
			return err
		}
	}
	frames := make(chan *pibridgepb.PiToPhi, 4)
	recvErr := make(chan error, 1)
	done := make(chan struct{})
	defer close(done)
	go func() {
		for {
			frame, err := stream.Recv()
			if err != nil {
				select {
				case recvErr <- err:
				default:
				}
				return
			}
			select {
			case frames <- frame:
			case <-done:
				return
			}
		}
	}()
	for {
		select {
		case err := <-recvErr:
			return err
		case frame := <-frames:
			f.mu.Lock()
			hook := f.onFrame
			cutNow := false
			if f.cutAfter > 0 {
				f.cutAfter--
				cutNow = f.cutAfter == 0
			}
			// A cut frame is not recorded: the stream died before Phi
			// could use it, which is what a lost receipt looks like.
			if !cutNow {
				f.frames = append(f.frames, frame)
			}
			f.mu.Unlock()
			if hook != nil {
				hook(stream, frame)
			}
			if cutNow {
				return status.Error(codes.Unavailable, "cut")
			}
		case msg := <-f.inject:
			msg.PaneId, msg.ProcessId = first.GetPaneId(), first.GetProcessId()
			msg.RuntimeGeneration = first.GetRuntimeGeneration()
			if err := stream.Send(msg); err != nil {
				return err
			}
			f.mu.Lock()
			cutSent := false
			if f.cutAfterSend > 0 {
				f.cutAfterSend--
				cutSent = f.cutAfterSend == 0
			}
			f.mu.Unlock()
			if cutSent {
				return status.Error(codes.Unavailable, "cut")
			}
		}
	}
}

func (f *fakePhi) snapshot() (connects int, hellos, frames []*pibridgepb.PiToPhi, mds []metadata.MD) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.connects, append([]*pibridgepb.PiToPhi(nil), f.hellos...), append([]*pibridgepb.PiToPhi(nil), f.frames...), append([]metadata.MD(nil), f.md...)
}

func (f *fakePhi) waitFor(t *testing.T, what string, ok func(connects int, hellos, frames []*pibridgepb.PiToPhi) bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if connects, hellos, frames, _ := f.snapshot(); ok(connects, hellos, frames) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	connects, hellos, frames, _ := f.snapshot()
	t.Fatalf("timed out waiting for %s (connects=%d hellos=%d frames=%d)", what, connects, len(hellos), len(frames))
}

func TestFromEnvReadsClearsAndRejectsPartial(t *testing.T) {
	full := map[string]string{
		envEndpoint:       "unix:/tmp/pb-test.sock",
		envPaneID:         "pane-1",
		envProcessID:      "proc-1",
		envToken:          "secret-token",
		envAttachmentRoot: "/tmp/attachments",
	}
	for k, v := range full {
		t.Setenv(k, v)
	}
	c := FromEnv(Snapshot{Generation: 1, SessionID: "s1"})
	if c == nil {
		t.Fatal("full environment produced no client")
	}
	for k := range full {
		if got := os.Getenv(k); got != "" {
			t.Errorf("%s not cleared: %q", k, got)
		}
	}

	for k, v := range full {
		t.Setenv(k, v)
	}
	_ = os.Unsetenv(envToken)
	if c := FromEnv(Snapshot{}); c != nil {
		t.Fatal("partial environment produced a client")
	}
	for k := range full {
		if got := os.Getenv(k); got != "" {
			t.Errorf("%s not cleared after partial env: %q", k, got)
		}
	}

	if c := FromEnv(Snapshot{}); c != nil {
		t.Fatal("empty environment produced a client")
	}
}

func TestFromEnvRejectsRemoteTargets(t *testing.T) {
	t.Setenv(envEndpoint, "dns:example.com:443")
	t.Setenv(envPaneID, "p")
	t.Setenv(envProcessID, "q")
	t.Setenv(envToken, "t")
	if c := FromEnv(Snapshot{}); c != nil {
		t.Fatal("non-unix target produced a client")
	}
}

func TestHelloCarriesIdentityMetadataAndCapabilities(t *testing.T) {
	f := startFakePhi(t)
	t.Setenv(envEndpoint, f.target)
	t.Setenv(envPaneID, "pane-7")
	t.Setenv(envProcessID, "proc-7")
	t.Setenv(envToken, "token-7")
	c := FromEnv(Snapshot{Generation: 3, SessionID: "session-7", LeafID: "leaf-7", Provider: "anthropic", Model: "m", Thinking: "high"})
	if c == nil {
		t.Fatal("no client")
	}
	c.Start()
	defer c.Close()

	f.waitFor(t, "hello", func(_ int, hellos, _ []*pibridgepb.PiToPhi) bool { return len(hellos) == 1 })
	_, hellos, _, mds := f.snapshot()
	hello := hellos[0]
	if hello.GetPaneId() != "pane-7" || hello.GetProcessId() != "proc-7" || hello.GetRuntimeGeneration() != 3 {
		t.Fatalf("envelope mismatch: %+v", hello)
	}
	h := hello.GetHello()
	if h.GetProtocolVersion() != 1 || h.GetRuntimeId() == "" || h.GetSessionId() != "session-7" {
		t.Fatalf("hello mismatch: %+v", h)
	}
	if got := h.GetCapabilities(); len(got) != len(baseCapabilities) {
		t.Fatalf("capabilities: %v", got)
	}
	for i, name := range baseCapabilities {
		if h.GetCapabilities()[i] != name {
			t.Fatalf("capabilities: %v", h.GetCapabilities())
		}
	}
	state := h.GetCurrentState()
	if state.GetState() != pibridgepb.LogicalState_LOGICAL_STATE_IDLE || state.GetLeafId() != "leaf-7" ||
		state.GetModel().GetProvider() != "anthropic" || state.GetThinkingLevel() != "high" {
		t.Fatalf("snapshot mismatch: %+v", state)
	}
	md := mds[0]
	for key, want := range map[string]string{metadataToken: "token-7", metadataPane: "pane-7", metadataProcess: "proc-7"} {
		if got := md.Get(key); len(got) != 1 || got[0] != want {
			t.Errorf("metadata %s = %v, want %q", key, got, want)
		}
	}
	if proto.Size(hello) >= 16*1024 {
		t.Errorf("hello too large: %d", proto.Size(hello))
	}
	raw, err := proto.Marshal(hello)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte("token-7")) {
		t.Error("token leaked into the message payload")
	}
}

func TestServesEdgesSnapshotsAndStateRequests(t *testing.T) {
	f := startFakePhi(t)
	f.requestID = "req-1"
	c := newTestClient(t, f, Snapshot{Generation: 1, SessionID: "session-1"})
	f.waitFor(t, "hello", func(_ int, hellos, _ []*pibridgepb.PiToPhi) bool { return len(hellos) == 1 })
	c.RunStarted()
	f.waitFor(t, "running snapshot", func(_ int, _, frames []*pibridgepb.PiToPhi) bool {
		for _, fr := range frames {
			if s := fr.GetStateSnapshot(); s != nil && s.GetState() == pibridgepb.LogicalState_LOGICAL_STATE_RUNNING {
				return true
			}
		}
		return false
	})
	c.Sync(Snapshot{Generation: 1, SessionID: "session-1", LeafID: "leaf-2", Provider: "p", Model: "m", Thinking: "low"})
	c.RunSettled(OutcomeCompleted)

	f.waitFor(t, "settled edge", func(_ int, _, frames []*pibridgepb.PiToPhi) bool {
		for _, fr := range frames {
			if s := fr.GetAgentSettled(); s != nil && s.GetOutcome() == pibridgepb.RunOutcome_RUN_OUTCOME_COMPLETED {
				return true
			}
		}
		return false
	})
	// State refreshes are served on the host loop, because only the host
	// knows the current identity. A client that answered them itself would
	// risk publishing a stale snapshot under a fresh request id.
	select {
	case req := <-c.Requests():
		if req.Kind() != RequestState || req.RequestID() != "req-1" {
			t.Fatalf("unexpected request: %s %q", req.Kind(), req.RequestID())
		}
		if !req.ReplyState() {
			t.Fatal("state reply rejected")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("state request was not forwarded to the host")
	}
	f.waitFor(t, "state request reply", func(_ int, _, frames []*pibridgepb.PiToPhi) bool {
		for _, fr := range frames {
			if s := fr.GetStateSnapshot(); s != nil && s.GetRequestId() == "req-1" {
				return true
			}
		}
		return false
	})

	_, _, frames, _ := f.snapshot()
	var sawRunning, sawIdle bool
	var seqs []uint64
	for _, fr := range frames {
		if s := fr.GetAgentStarted(); s != nil {
			seqs = append(seqs, s.GetEventSequence())
		}
		if s := fr.GetAgentSettled(); s != nil {
			seqs = append(seqs, s.GetEventSequence())
		}
		if s := fr.GetStateSnapshot(); s != nil {
			switch s.GetState() {
			case pibridgepb.LogicalState_LOGICAL_STATE_RUNNING:
				sawRunning = true
			case pibridgepb.LogicalState_LOGICAL_STATE_IDLE:
				sawIdle = true
			}
		}
	}
	if !sawRunning || !sawIdle {
		t.Fatalf("snapshots did not cover running and idle (running=%v idle=%v)", sawRunning, sawIdle)
	}
	if len(seqs) != 2 || seqs[0] >= seqs[1] || seqs[0] == 0 || seqs[1] == 0 {
		t.Fatalf("edge sequences not monotonic: %v", seqs)
	}
}

func TestCloseDuringBackoffReturns(t *testing.T) {
	f := startFakePhi(t)
	f.drops = 1000
	c := newTestClient(t, f, Snapshot{Generation: 1, SessionID: "s"})
	f.waitFor(t, "a dropped attempt", func(connects int, _, _ []*pibridgepb.PiToPhi) bool { return connects >= 1 })
	start := time.Now()
	c.Close()
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("Close took %s", d)
	}
}

func TestTerminalCodeStopsAfterOneAttempt(t *testing.T) {
	f := startFakePhi(t)
	f.reject = codes.Unauthenticated
	c := newTestClient(t, f, Snapshot{Generation: 1, SessionID: "s"})
	select {
	case <-c.done:
	case <-time.After(5 * time.Second):
		t.Fatal("client did not stop after terminal rejection")
	}
	connects, _, _, _ := f.snapshot()
	if connects != 1 {
		t.Fatalf("connects = %d, want 1", connects)
	}
	c.Close()
}

type helloEOFStream struct {
	pibridgepb.Integration_ConnectClient
	rejection error
}

func (s *helloEOFStream) Send(*pibridgepb.PiToPhi) error { return io.EOF }
func (s *helloEOFStream) Recv() (*pibridgepb.PhiToPi, error) {
	return nil, s.rejection
}

func TestHelloWriteEOFPreservesRejectionStatus(t *testing.T) {
	for _, code := range []codes.Code{codes.Unauthenticated, codes.Unavailable} {
		t.Run(code.String(), func(t *testing.T) {
			c := &Client{runtimeID: "runtime", snap: Snapshot{Generation: 1, SessionID: "s"}}
			hello, _, _, _ := c.hello()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			stream := &helloEOFStream{rejection: status.Error(code, "rejected")}
			served, err := c.serveStream(ctx, cancel, stream, hello)
			if served || status.Code(err) != code {
				t.Fatalf("served = %v, err = %v; want false and %s", served, err, code)
			}
		})
	}
}

func TestUnavailableRetriesThenAccepts(t *testing.T) {
	f := startFakePhi(t)
	f.drops = 2
	newTestClient(t, f, Snapshot{Generation: 1, SessionID: "s"})
	f.waitFor(t, "three attempts", func(connects int, _, _ []*pibridgepb.PiToPhi) bool { return connects == 3 })
	f.waitFor(t, "hello after retries", func(_ int, hellos, _ []*pibridgepb.PiToPhi) bool { return len(hellos) == 1 })
	_, hellos, _, _ := f.snapshot()
	if hellos[0].GetRuntimeGeneration() != 1 {
		t.Fatalf("generation changed across retries: %d", hellos[0].GetRuntimeGeneration())
	}
}

func TestDuplicateEdgesAreSuppressed(t *testing.T) {
	f := startFakePhi(t)
	c := newTestClient(t, f, Snapshot{Generation: 1, SessionID: "s"})
	f.waitFor(t, "hello", func(_ int, hellos, _ []*pibridgepb.PiToPhi) bool { return len(hellos) == 1 })
	c.RunStarted()
	c.RunStarted()
	c.RunSettled(OutcomeCompleted)
	c.RunSettled(OutcomeError)

	f.waitFor(t, "settled edge", func(_ int, _, frames []*pibridgepb.PiToPhi) bool {
		for _, fr := range frames {
			if fr.GetAgentSettled() != nil {
				return true
			}
		}
		return false
	})
	_, _, frames, _ := f.snapshot()
	var started, settled int
	for _, fr := range frames {
		if fr.GetAgentStarted() != nil {
			started++
		}
		if fr.GetAgentSettled() != nil {
			settled++
		}
	}
	if started != 1 || settled != 1 {
		t.Fatalf("edges started=%d settled=%d, want 1/1", started, settled)
	}
}

func TestSettlementWithoutOpenRunProducesNoEdge(t *testing.T) {
	f := startFakePhi(t)
	c := newTestClient(t, f, Snapshot{Generation: 1, SessionID: "s"})
	f.waitFor(t, "hello", func(_ int, hellos, _ []*pibridgepb.PiToPhi) bool { return len(hellos) == 1 })
	c.RunSettled(OutcomeCompleted)
	time.Sleep(200 * time.Millisecond)
	_, _, frames, _ := f.snapshot()
	for _, fr := range frames {
		if fr.GetAgentSettled() != nil {
			t.Fatal("settlement without an open run produced an edge")
		}
	}
}

func TestGenerationRebindsStream(t *testing.T) {
	f := startFakePhi(t)
	c := newTestClient(t, f, Snapshot{Generation: 1, SessionID: "s1"})
	f.waitFor(t, "first hello", func(_ int, hellos, _ []*pibridgepb.PiToPhi) bool { return len(hellos) == 1 })
	c.Sync(Snapshot{Generation: 2, SessionID: "s2"})
	f.waitFor(t, "second hello", func(_ int, hellos, _ []*pibridgepb.PiToPhi) bool { return len(hellos) == 2 })
	_, hellos, _, _ := f.snapshot()
	first, second := hellos[0].GetHello(), hellos[1].GetHello()
	if second.GetSessionId() != "s2" || hellos[1].GetRuntimeGeneration() != 2 {
		t.Fatalf("second hello did not rebind: %+v gen=%d", second, hellos[1].GetRuntimeGeneration())
	}
	if first.GetRuntimeId() == second.GetRuntimeId() {
		t.Fatal("runtime id was reused across generations")
	}
	if second.GetJournalLastSequence() != 0 {
		t.Fatalf("journal not reset: last=%d", second.GetJournalLastSequence())
	}
}

func TestJournalRetainsBoundAndLatchesGap(t *testing.T) {
	var j journal
	for i := 1; i <= 100; i++ {
		j.open(uint64(i))
	}
	recs := j.snapshot()
	if len(recs) != journalLimit {
		t.Fatalf("retained %d, want %d", len(recs), journalLimit)
	}
	if !j.gap {
		t.Fatal("gap not latched")
	}
	first, last := j.bounds()
	if first != recs[0].GetEventSequence() || last != recs[len(recs)-1].GetEventSequence() || last-first != journalLimit-1 {
		t.Fatalf("bounds %d..%d", first, last)
	}
	for i := 1; i < len(recs); i++ {
		if recs[i].GetEventSequence() <= recs[i-1].GetEventSequence() {
			t.Fatalf("sequences not monotonic at %d", i)
		}
	}
	edge := j.settle(100, pibridgepb.RunOutcome_RUN_OUTCOME_COMPLETED)
	if edge.GetEventSequence() != last+1 || !edge.GetSettled() {
		t.Fatalf("settlement edge %+v", edge)
	}
}

func TestReconnectReplaysRetainedJournal(t *testing.T) {
	f := startFakePhi(t)
	c := newTestClient(t, f, Snapshot{Generation: 1, SessionID: "s"})
	f.waitFor(t, "hello", func(_ int, hellos, _ []*pibridgepb.PiToPhi) bool { return len(hellos) == 1 })
	for i := 0; i < 70; i++ {
		c.RunStarted()
		c.RunSettled(OutcomeCompleted)
	}
	f.waitFor(t, "some edges", func(_ int, _, frames []*pibridgepb.PiToPhi) bool { return len(frames) > 0 })

	// Cut the stream once, then let the client reconnect with the same
	// generation and runtime identity.
	f.mu.Lock()
	f.cutAfter = 1
	f.mu.Unlock()
	c.RunStarted()

	f.waitFor(t, "second hello", func(_ int, hellos, _ []*pibridgepb.PiToPhi) bool { return len(hellos) == 2 })
	_, hellos, _, _ := f.snapshot()
	first, second := hellos[0], hellos[1]
	if second.GetRuntimeGeneration() != first.GetRuntimeGeneration() {
		t.Fatalf("generation changed on reconnect: %d -> %d", first.GetRuntimeGeneration(), second.GetRuntimeGeneration())
	}
	if second.GetHello().GetRuntimeId() != first.GetHello().GetRuntimeId() {
		t.Fatal("runtime id changed on reconnect")
	}
	state := second.GetHello().GetCurrentState()
	if !state.GetJournalGap() {
		t.Fatal("journal gap missing on replay")
	}
	recs := state.GetJournal()
	if len(recs) != journalLimit {
		t.Fatalf("replayed %d records, want %d", len(recs), journalLimit)
	}
	lo, hi := second.GetHello().GetJournalFirstSequence(), second.GetHello().GetJournalLastSequence()
	if lo != recs[0].GetEventSequence() || hi != recs[len(recs)-1].GetEventSequence() {
		t.Fatalf("replayed bounds %d..%d do not match the retained records", lo, hi)
	}
	seen := map[uint64]bool{}
	for _, r := range state.GetJournal() {
		if seen[r.GetEventSequence()] {
			t.Fatalf("duplicate sequence %d", r.GetEventSequence())
		}
		seen[r.GetEventSequence()] = true
	}
}

func TestMessageBounds(t *testing.T) {
	c := &Client{snap: Snapshot{Generation: 1, SessionID: "s", LeafID: "l", Provider: "provider-name", Model: "model-name", Thinking: "high"}}
	for i := 0; i < journalLimit; i++ {
		c.jrn.open(uint64(i + 1))
	}
	hello, _, _, ok := c.hello()
	if !ok {
		t.Fatal("no hello")
	}
	if size := proto.Size(hello); size >= 16*1024 {
		t.Fatalf("hello %d bytes", size)
	}
	snap := c.stateMessage("", 1)
	if size := proto.Size(snap); size >= 256*1024 {
		t.Fatalf("snapshot %d bytes", size)
	}
}

func TestRetryableTable(t *testing.T) {
	cases := []struct {
		err  error
		want bool
	}{
		{status.Error(codes.Unauthenticated, ""), false},
		{status.Error(codes.PermissionDenied, ""), false},
		{status.Error(codes.FailedPrecondition, ""), false},
		{status.Error(codes.Canceled, ""), false},
		{status.Error(codes.DeadlineExceeded, ""), false},
		{status.Error(codes.Unavailable, ""), true},
		{status.Error(codes.ResourceExhausted, ""), true},
		{status.Error(codes.Internal, ""), true},
		{status.Error(codes.Unknown, ""), true},
		{errAcceptTimeout, true},
		{context.Canceled, false},
		{nil, false},
	}
	for _, tc := range cases {
		if got := retryable(tc.err); got != tc.want {
			t.Errorf("retryable(%v) = %v, want %v", tc.err, got, tc.want)
		}
	}
}

func TestNilClientIsInert(t *testing.T) {
	var c *Client
	c.Start()
	c.Sync(Snapshot{Generation: 1})
	c.RunStarted()
	c.RunSettled(OutcomeAborted)
	c.Close()
}

func newTestClient(t *testing.T, f *fakePhi, snap Snapshot) *Client {
	t.Helper()
	t.Setenv(envEndpoint, f.target)
	t.Setenv(envPaneID, "pane")
	t.Setenv(envProcessID, "proc")
	t.Setenv(envToken, "token")
	c := FromEnv(snap)
	if c == nil {
		t.Fatal("no client")
	}
	c.Start()
	t.Cleanup(c.Close)
	return c
}

func TestGenerationChangeDropsStaleEdges(t *testing.T) {
	c := &Client{wake: make(chan struct{}, 1)}
	c.RunStarted()
	if len(c.pending) != 1 {
		t.Fatalf("queued edges = %d, want 1", len(c.pending))
	}
	c.Sync(Snapshot{Generation: 2, SessionID: "s2"})
	if len(c.pending) != 0 {
		t.Fatalf("stale edges survived the generation change: %d", len(c.pending))
	}
	if c.running || c.jrn.seq != 0 || len(c.jrn.records) != 0 {
		t.Fatal("run state or journal survived the generation change")
	}
}

// TestReconnectDropsReplayedEdges pins the reconnect contract: the attach
// journal carries every queued edge, so flushing them again would publish
// each sequence twice.
func TestReconnectDropsOnlyEdgesTheHelloCarried(t *testing.T) {
	c := &Client{wake: make(chan struct{}, 1)}
	c.RunStarted()
	c.RunSettled(OutcomeCompleted)
	c.RunStarted()
	if len(c.pending) != 3 {
		t.Fatalf("queued edges = %d, want 3", len(c.pending))
	}
	if _, last := c.jrn.bounds(); last != 3 {
		t.Fatalf("journal last sequence = %d, want 3", last)
	}
	// The attach carried 1..2; the edge opened while attaching must survive.
	c.dropReplayed(2)
	if len(c.pending) != 1 || c.pending[0].seq != 3 {
		t.Fatalf("pending after replay = %+v, want only seq 3", c.pending)
	}
	c.dropReplayed(3)
	if len(c.pending) != 0 {
		t.Fatalf("replayed edges stayed queued: %d", len(c.pending))
	}
}

func TestFlushRestoresEdgesOnGenerationChange(t *testing.T) {
	c := &Client{wake: make(chan struct{}, 1)}
	c.RunStarted()
	c.Sync(Snapshot{Generation: 1, SessionID: "s"})
	// A generation bump clears pending via Sync; simulate the race the
	// guard covers: an edge queued before the bump, not after it.
	c.pending = []edge{{seq: 1, runID: 1}}
	fake := &restoringStream{}
	err := c.flush(fake, 2)
	if !errors.Is(err, errRegeneration) {
		t.Fatalf("err = %v, want errRegeneration", err)
	}
	if len(c.pending) != 1 || c.pending[0].seq != 1 {
		t.Fatalf("edges lost on regeneration: %+v", c.pending)
	}
}

type restoringStream struct {
	pibridgepb.Integration_ConnectClient
}

func (s *restoringStream) Send(*pibridgepb.PiToPhi) error { return nil }
