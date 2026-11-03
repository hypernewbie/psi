package pibridge

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	mrand "math/rand/v2"
	"os"
	"runtime"
	"strings"
	"sync"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	pibridgepb "psi/internal/pibridge/pb"
)

// Environment keys provided by Phi at spawn time. They are read once and
// cleared so child processes never inherit the token.
const (
	envEndpoint       = "PHI_PI_BRIDGE_ENDPOINT"
	envPaneID         = "PHI_PI_BRIDGE_PANE_ID"
	envProcessID      = "PHI_PI_BRIDGE_PROCESS_ID"
	envToken          = "PHI_PI_BRIDGE_TOKEN"
	envAttachmentRoot = "PHI_PI_ATTACHMENT_ROOT"

	metadataToken   = "phi-pi-token"
	metadataPane    = "phi-pi-pane"
	metadataProcess = "phi-pi-process"
)

const (
	protocolVersion = 1
	capLifecycle    = "pi.lifecycle.v1"
	capState        = "pi.state.v2"

	// attachDeadline must stay below Phi's own 5s hello deadline: that status
	// is terminal, so the local timeout has to fire first for the retry to
	// happen at all.
	attachDeadline = 4 * time.Second
	minBackoff     = 250 * time.Millisecond
	maxBackoff     = 5 * time.Second

	// pendingLimit bounds lifecycle edges queued while no stream is up.
	pendingLimit = 128
	// recvQueue bounds messages buffered between the reader and the owner.
	recvQueue = 16
)

var (
	errAcceptTimeout = errors.New("pibridge: attach not accepted")
	errProtocol      = errors.New("pibridge: protocol error")
	errNotReady      = errors.New("pibridge: session identity not ready")
	errRegeneration  = errors.New("pibridge: runtime generation changed")
)

// Outcome is the logical run outcome at settlement.
type Outcome uint8

const (
	OutcomeUnspecified Outcome = iota
	OutcomeCompleted
	OutcomeError
	OutcomeAborted
)

func (o Outcome) proto() pibridgepb.RunOutcome {
	switch o {
	case OutcomeCompleted:
		return pibridgepb.RunOutcome_RUN_OUTCOME_COMPLETED
	case OutcomeError:
		return pibridgepb.RunOutcome_RUN_OUTCOME_ERROR
	case OutcomeAborted:
		return pibridgepb.RunOutcome_RUN_OUTCOME_ABORTED
	default:
		return pibridgepb.RunOutcome_RUN_OUTCOME_UNSPECIFIED
	}
}

// Snapshot is the identity the host publishes. The client owns the runtime
// id, the running state, and the snapshot revision.
type Snapshot struct {
	Generation uint64
	SessionID  string
	LeafID     string
	Provider   string
	Model      string
	Thinking   string
}

type edge struct {
	seq     uint64
	runID   uint64
	settled bool
	outcome pibridgepb.RunOutcome
}

// Client publishes one pane's lifecycle and state to Phi. Every exported
// method is nil-receiver safe.
type Client struct {
	endpoint  string
	paneID    string
	processID string
	token     string
	runtimeID string

	startMu sync.Mutex
	started bool
	cancel  context.CancelFunc
	done    chan struct{}

	mu      sync.Mutex
	snap    Snapshot
	rev     uint64
	dirty   bool
	running bool
	runID   uint64
	jrn     journal
	pending []edge
	wake    chan struct{}
}

// FromEnv builds a client when Phi provisioned this process. It returns nil
// when the environment is absent, partial, or not a local unix endpoint.
// All five keys are cleared before returning, including the attachment root
// this MVP does not use.
func FromEnv(initial Snapshot) *Client {
	endpoint := os.Getenv(envEndpoint)
	paneID := os.Getenv(envPaneID)
	processID := os.Getenv(envProcessID)
	token := os.Getenv(envToken)
	for _, key := range []string{envEndpoint, envPaneID, envProcessID, envToken, envAttachmentRoot} {
		_ = os.Unsetenv(key)
	}
	if runtime.GOOS == "windows" {
		// Phi provisions a Windows named pipe; this client has no pipe
		// dialer, so it stays plain instead of retrying forever.
		return nil
	}
	if endpoint == "" || paneID == "" || processID == "" || token == "" {
		return nil
	}
	// Only the local IPC socket Phi creates is legitimate. Refuse any
	// other target so a stray value cannot send the token off-host.
	if !strings.HasPrefix(endpoint, "unix:/") {
		return nil
	}
	c := &Client{
		endpoint:  endpoint,
		paneID:    paneID,
		processID: processID,
		token:     token,
		runtimeID: newRuntimeID(),
		snap:      initial,
		wake:      make(chan struct{}, 1),
	}
	return c
}

func newRuntimeID() string {
	var buf [16]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return fmt.Sprintf("%d-%d", os.Getpid(), time.Now().UnixNano())
	}
	return hex.EncodeToString(buf[:])
}

// Start launches the only goroutine. It is safe to call once; later calls
// do nothing. Call Close to stop it.
func (c *Client) Start() {
	if c == nil {
		return
	}
	c.startMu.Lock()
	defer c.startMu.Unlock()
	if c.started {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	c.started = true
	c.cancel = cancel
	c.done = make(chan struct{})
	go c.run(ctx)
}

// Close cancels the stream and waits for the goroutine. It is idempotent
// and returns at once when Start was never called.
func (c *Client) Close() {
	if c == nil {
		return
	}
	c.startMu.Lock()
	cancel, done := c.cancel, c.done
	c.startMu.Unlock()
	if cancel == nil {
		return
	}
	cancel()
	<-done
}

// Sync publishes identity changes. Unchanged snapshots are dropped here so
// the host may call it every frame.
func (c *Client) Sync(s Snapshot) {
	if c == nil {
		return
	}
	c.mu.Lock()
	if c.snap == s {
		c.mu.Unlock()
		return
	}
	if s.Generation != c.snap.Generation {
		// A new generation is a new runtime on Phi's side: it resets its
		// journal, so ours starts clean too. Queued edges belong to the
		// old runtime and must not land on the new stream, where their
		// sequences would be ahead of the fresh journal.
		c.jrn.reset()
		c.running = false
		c.pending = nil
		c.runtimeID = newRuntimeID()
	}
	c.snap = s
	c.rev++
	c.dirty = true
	c.mu.Unlock()
	c.poke()
}

// RunStarted opens a logical run. A second call before settlement keeps the
// current run id: retries and queued continuations belong to the same run.
func (c *Client) RunStarted() {
	if c == nil {
		return
	}
	c.mu.Lock()
	if !c.running {
		c.running = true
		c.runID++
		rec := c.jrn.open(c.runID)
		c.pushEdge(edge{seq: rec.EventSequence, runID: c.runID})
		c.rev++
		c.dirty = true
	}
	c.mu.Unlock()
	c.poke()
}

// RunSettled closes the logical run. It is ignored when no run is open or
// when the run already settled.
func (c *Client) RunSettled(outcome Outcome) {
	if c == nil {
		return
	}
	c.mu.Lock()
	if c.running {
		c.running = false
		rec := c.jrn.settle(c.runID, outcome.proto())
		c.pushEdge(edge{seq: rec.EventSequence, runID: c.runID, settled: true, outcome: outcome.proto()})
		c.rev++
		c.dirty = true
	}
	c.mu.Unlock()
	c.poke()
}

func (c *Client) pushEdge(e edge) {
	if len(c.pending) == pendingLimit {
		c.pending = c.pending[1:]
	}
	c.pending = append(c.pending, e)
}

func (c *Client) poke() {
	select {
	case c.wake <- struct{}{}:
	default:
	}
}

func (c *Client) run(ctx context.Context) {
	defer close(c.done)
	backoff := minBackoff
	for {
		served, err := c.runOnce(ctx)
		if ctx.Err() != nil {
			return
		}
		if served {
			backoff = minBackoff
		}
		if errors.Is(err, errRegeneration) {
			continue
		}
		if !retryable(err) {
			return
		}
		if !sleep(ctx, jitter(backoff)) {
			return
		}
		if backoff < maxBackoff {
			backoff *= 2
			if backoff > maxBackoff {
				backoff = maxBackoff
			}
		}
	}
}

func sleep(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

// jitter returns d plus up to 20 percent, never less.
func jitter(d time.Duration) time.Duration {
	return d + time.Duration(mrand.Int64N(int64(d)/5+1))
}

func retryable(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, errProtocol) ||
		errors.Is(err, context.Canceled) ||
		errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	if errors.Is(err, errAcceptTimeout) || errors.Is(err, errNotReady) {
		return true
	}
	switch status.Code(err) {
	case codes.Unavailable, codes.ResourceExhausted, codes.Internal, codes.Unknown:
		return true
	}
	return false
}

// runOnce dials, performs the Hello handshake, and serves until the stream
// ends. It never sleeps; the caller owns backoff. served reports whether Phi
// accepted this attempt, so a fresh failure after a healthy stream starts
// again from the minimum delay.
func (c *Client) runOnce(parent context.Context) (served bool, err error) {
	hello, gen, ok := c.hello()
	if !ok {
		return false, errNotReady
	}
	conn, err := grpc.NewClient(c.endpoint, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return false, err
	}
	defer conn.Close()

	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	ctx = metadata.AppendToOutgoingContext(ctx,
		metadataToken, c.token,
		metadataPane, c.paneID,
		metadataProcess, c.processID,
	)
	stream, err := pibridgepb.NewIntegrationClient(conn).Connect(ctx)
	if err != nil {
		return false, err
	}
	if err := stream.Send(hello); err != nil {
		return false, err
	}

	msgs := make(chan *pibridgepb.PhiToPi, recvQueue)
	recvErr := make(chan error, 1)
	done := make(chan struct{})
	defer close(done)
	go func() {
		for {
			msg, err := stream.Recv()
			if err != nil {
				select {
				case recvErr <- err:
				default:
				}
				return
			}
			select {
			case msgs <- msg:
			case <-done:
				return
			case <-ctx.Done():
				return
			}
		}
	}()

	if err := c.awaitAccepted(ctx, msgs, recvErr); err != nil {
		return false, err
	}
	served = true
	c.dropReplayed(hello.GetHello().GetJournalLastSequence())
	if err := c.flush(stream, gen); err != nil {
		return served, err
	}
	for {
		select {
		case <-ctx.Done():
			return served, ctx.Err()
		case err := <-recvErr:
			return served, err
		case msg := <-msgs:
			switch payload := msg.Payload.(type) {
			case *pibridgepb.PhiToPi_StateRequest:
				if err := stream.Send(c.stateMessage(payload.StateRequest.GetRequestId(), gen)); err != nil {
					return served, err
				}
			case *pibridgepb.PhiToPi_ProtocolError:
				return served, fmt.Errorf("%w: %s", errProtocol, payload.ProtocolError.GetSafeMessage())
			}
		case <-c.wake:
			if err := c.flush(stream, gen); err != nil {
				return served, err
			}
		}
	}
}

func (c *Client) awaitAccepted(ctx context.Context, msgs <-chan *pibridgepb.PhiToPi, recvErr <-chan error) error {
	timer := time.NewTimer(attachDeadline)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
			return errAcceptTimeout
		case err := <-recvErr:
			return err
		case msg := <-msgs:
			switch payload := msg.Payload.(type) {
			case *pibridgepb.PhiToPi_Accepted:
				return nil
			case *pibridgepb.PhiToPi_ProtocolError:
				return fmt.Errorf("%w: %s", errProtocol, payload.ProtocolError.GetSafeMessage())
			}
		}
	}
}

// hello builds the attach message and the generation it binds.
func (c *Client) hello() (*pibridgepb.PiToPhi, uint64, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.snap.SessionID == "" {
		return nil, 0, false
	}
	gen := c.snap.Generation
	snap := c.snapshotLocked("")
	first, last := c.jrn.bounds()
	hello := &pibridgepb.Hello{
		ProtocolVersion:      protocolVersion,
		SessionId:            c.snap.SessionID,
		RuntimeId:            c.runtimeID,
		Capabilities:         []string{capLifecycle, capState},
		CurrentState:         snap,
		JournalFirstSequence: first,
		JournalLastSequence:  last,
	}
	return &pibridgepb.PiToPhi{
		PaneId:            c.paneID,
		ProcessId:         c.processID,
		RuntimeGeneration: gen,
		Payload:           &pibridgepb.PiToPhi_Hello{Hello: hello},
	}, gen, true
}

func (c *Client) snapshotLocked(requestID string) *pibridgepb.StateSnapshot {
	state := pibridgepb.LogicalState_LOGICAL_STATE_IDLE
	if c.running {
		state = pibridgepb.LogicalState_LOGICAL_STATE_RUNNING
	}
	snap := &pibridgepb.StateSnapshot{
		State:         state,
		SessionId:     c.snap.SessionID,
		LeafId:        c.snap.LeafID,
		Journal:       c.jrn.snapshot(),
		JournalGap:    c.jrn.gap,
		StateRevision: c.rev,
		ThinkingLevel: c.snap.Thinking,
		RequestId:     requestID,
	}
	if c.snap.Provider != "" || c.snap.Model != "" {
		snap.Model = &pibridgepb.ModelSnapshot{Provider: c.snap.Provider, Model: c.snap.Model}
	}
	return snap
}

func (c *Client) stateMessage(requestID string, gen uint64) *pibridgepb.PiToPhi {
	c.mu.Lock()
	snap := c.snapshotLocked(requestID)
	c.mu.Unlock()
	return &pibridgepb.PiToPhi{
		PaneId:            c.paneID,
		ProcessId:         c.processID,
		RuntimeGeneration: gen,
		Payload:           &pibridgepb.PiToPhi_StateSnapshot{StateSnapshot: snap},
	}
}

// dropReplayed discards queued edges the attach message already carried, so
// a reconnect publishes each sequence once. last is the bound in that Hello,
// not the current journal: an edge created while the stream attached is not
// in it and must still be sent. Older dropped edges stay queued; Phi ignores
// them by sequence.
func (c *Client) dropReplayed(last uint64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	i := 0
	for i < len(c.pending) && c.pending[i].seq <= last {
		i++
	}
	if i > 0 {
		c.pending = c.pending[i:]
	}
}

// flush sends queued edges, then the current snapshot when it changed. It
// stops early when the host's generation moved on.
func (c *Client) flush(stream pibridgepb.Integration_ConnectClient, gen uint64) error {
	for {
		c.mu.Lock()
		if len(c.pending) == 0 && !c.dirty {
			c.mu.Unlock()
			return nil
		}
		edges := c.pending
		c.pending = nil
		rev := c.rev
		if c.snap.Generation != gen {
			// The generation moved under us: the edges just taken belong
			// to the old runtime. Restore them; Sync already reset the
			// journal for the new generation, and the stale peek would
			// otherwise drop edges Phi has not merged yet.
			c.pending = append(edges, c.pending...)
			c.mu.Unlock()
			return errRegeneration
		}
		var snap *pibridgepb.StateSnapshot
		if c.dirty {
			snap = c.snapshotLocked("")
		}
		c.mu.Unlock()

		for _, e := range edges {
			if err := stream.Send(c.edgeMessage(e, gen)); err != nil {
				return err
			}
		}
		if snap != nil {
			if err := stream.Send(&pibridgepb.PiToPhi{
				PaneId:            c.paneID,
				ProcessId:         c.processID,
				RuntimeGeneration: gen,
				Payload:           &pibridgepb.PiToPhi_StateSnapshot{StateSnapshot: snap},
			}); err != nil {
				return err
			}
		}

		c.mu.Lock()
		if c.rev == rev {
			c.dirty = false
		}
		c.mu.Unlock()
	}
}

func (c *Client) edgeMessage(e edge, gen uint64) *pibridgepb.PiToPhi {
	msg := &pibridgepb.PiToPhi{
		PaneId:            c.paneID,
		ProcessId:         c.processID,
		RuntimeGeneration: gen,
	}
	if e.settled {
		msg.Payload = &pibridgepb.PiToPhi_AgentSettled{AgentSettled: &pibridgepb.AgentSettled{
			EventSequence: e.seq,
			RunId:         e.runID,
			Outcome:       e.outcome,
		}}
		return msg
	}
	msg.Payload = &pibridgepb.PiToPhi_AgentStarted{AgentStarted: &pibridgepb.AgentStarted{
		EventSequence: e.seq,
		RunId:         e.runID,
	}}
	return msg
}
