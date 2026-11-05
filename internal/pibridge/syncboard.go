package pibridge

import (
	"context"
	"strconv"
	"time"

	pibridgepb "psi/internal/pibridge/pb"
)

// Reverse-direction native Syncboard calls. Psi is the requester here: the
// model invokes the syncboard tool, the client sends one SyncboardRequest,
// and Phi answers with one SyncboardResponse. Requests are never replayed:
// stream loss fails every waiter, and only an explicit response resolves one.
//
// This mirrors the TypeScript runtime's requestSyncboard: canonical decimal
// ids from a client-owned saturating counter, at most four calls in flight,
// a 15s caller wait, and duplicate or late responses dropped.
const syncboardPendingMax = 4

// syncboardCallTimeout bounds one caller wait. It is a var so tests can
// shrink the wait without real timers.
var syncboardCallTimeout = 15 * time.Second

// SyncboardOutcome names which side of the call failed. not-sent means Phi
// never saw the request and a retry may be safe; unknown means the request
// may have run and the caller must read-before-acting, never replay blindly.
type SyncboardError struct {
	Outcome string
	Reason  string
}

func (e *SyncboardError) Error() string {
	return "pibridge: syncboard " + e.Outcome + ": " + e.Reason
}

// SyncboardResult is one complete coordinator answer. A nil error from
// CallSyncboard does not mean success: the outcome still needs parsing,
// because HTTP errors arrive as results, not transport faults.
type SyncboardResult struct {
	Outcome    pibridgepb.SyncboardOutcome
	HTTPStatus uint32
	Body       []byte
	Reason     string
}

type sbQueued struct {
	id    string
	op    pibridgepb.SyncboardOperation
	key   string
	value []byte
}

type sbDelivery struct {
	result SyncboardResult
	err    *SyncboardError
}

// Accepted reports whether Phi negotiated this capability on the live
// stream. A nil client accepts nothing.
func (c *Client) Accepted(name string) bool {
	if c == nil {
		return false
	}
	c.capsMu.RLock()
	defer c.capsMu.RUnlock()
	return c.caps[name]
}

// SyncboardAccepted reports whether the live stream negotiated the native
// Syncboard capability. The tool is advertised only while this holds.
func (c *Client) SyncboardAccepted() bool {
	return c.Accepted(capSyncboard)
}

func (c *Client) setCaps(caps map[string]bool) {
	c.capsMu.Lock()
	c.caps = caps
	c.capsMu.Unlock()
}

// dropCaps clears the negotiated set and fails every waiter: the stream
// that carried their requests is gone, and requests are never replayed.
func (c *Client) dropCaps() {
	c.capsMu.Lock()
	c.caps = nil
	c.failSyncboard(&SyncboardError{Outcome: "unknown", Reason: "transport-error"})
	c.capsMu.Unlock()
}

// CallSyncboard performs one bounded Syncboard operation against Phi. The
// id is allocated before admission so a refused call still burns its number,
// exactly like the reference runtime: ids are never reused.
func (c *Client) CallSyncboard(ctx context.Context, op pibridgepb.SyncboardOperation, key string, value []byte) (SyncboardResult, error) {
	if c == nil {
		return SyncboardResult{}, &SyncboardError{Outcome: "not-sent", Reason: "unavailable"}
	}
	if err := ctx.Err(); err != nil {
		return SyncboardResult{}, &SyncboardError{Outcome: "not-sent", Reason: "cancelled"}
	}
	// Admission and disconnect share capsMu -> sbMu ordering. Otherwise a
	// caller can enqueue after the old stream's waiters have been failed.
	c.capsMu.RLock()
	if !c.caps[capSyncboard] {
		c.capsMu.RUnlock()
		return SyncboardResult{}, &SyncboardError{Outcome: "not-sent", Reason: "unavailable"}
	}
	c.sbMu.Lock()
	if c.sbSeq == ^uint64(0) {
		c.sbMu.Unlock()
		c.capsMu.RUnlock()
		return SyncboardResult{}, &SyncboardError{Outcome: "not-sent", Reason: "unavailable"}
	}
	if len(c.sbWaiters) >= syncboardPendingMax {
		c.sbMu.Unlock()
		c.capsMu.RUnlock()
		return SyncboardResult{}, &SyncboardError{Outcome: "not-sent", Reason: "busy"}
	}
	c.sbSeq++
	id := strconv.FormatUint(c.sbSeq, 10)
	ch := make(chan sbDelivery, 1)
	if c.sbWaiters == nil {
		c.sbWaiters = map[string]chan sbDelivery{}
	}
	c.sbWaiters[id] = ch
	c.sbQueue = append(c.sbQueue, sbQueued{id: id, op: op, key: key, value: append([]byte(nil), value...)})
	c.sbMu.Unlock()
	c.capsMu.RUnlock()
	c.poke()

	timer := time.NewTimer(syncboardCallTimeout)
	defer timer.Stop()
	select {
	case delivery := <-ch:
		if delivery.err != nil {
			return SyncboardResult{}, delivery.err
		}
		return delivery.result, nil
	case <-timer.C:
		c.dropSyncboardWaiter(id)
		return SyncboardResult{}, &SyncboardError{Outcome: "unknown", Reason: "timeout"}
	case <-ctx.Done():
		c.dropSyncboardWaiter(id)
		return SyncboardResult{}, &SyncboardError{Outcome: "unknown", Reason: "cancelled"}
	}
}

func (c *Client) dropSyncboardWaiter(id string) {
	c.sbMu.Lock()
	delete(c.sbWaiters, id)
	for i, q := range c.sbQueue {
		if q.id == id {
			c.sbQueue = append(c.sbQueue[:i], c.sbQueue[i+1:]...)
			break
		}
	}
	c.sbMu.Unlock()
}

// takeSyncboard removes queued native calls for this flush. The queue is
// drained under the run's generation, so a reconnect never resends them:
// waiters were already failed when the old stream died.
func (c *Client) takeSyncboard() []sbQueued {
	c.sbMu.Lock()
	defer c.sbMu.Unlock()
	queued := c.sbQueue
	c.sbQueue = nil
	return queued
}

func (c *Client) syncboardMessage(q sbQueued, gen uint64) *pibridgepb.PiToPhi {
	return &pibridgepb.PiToPhi{
		PaneId:            c.paneID,
		ProcessId:         c.processID,
		RuntimeGeneration: gen,
		Payload: &pibridgepb.PiToPhi_SyncboardRequest{SyncboardRequest: &pibridgepb.SyncboardRequest{
			RequestId: q.id,
			Operation: q.op,
			Key:       q.key,
			ValueJson: q.value,
		}},
	}
}

// resolveSyncboard delivers one response to its waiter. Unknown, duplicate,
// and late ids are dropped: they belong to a timed-out or cancelled caller.
func (c *Client) resolveSyncboard(res *pibridgepb.SyncboardResponse) {
	if res == nil {
		return
	}
	c.sbMu.Lock()
	ch := c.sbWaiters[res.GetRequestId()]
	if ch != nil {
		delete(c.sbWaiters, res.GetRequestId())
	}
	c.sbMu.Unlock()
	if ch == nil {
		return
	}
	select {
	case ch <- sbDelivery{result: SyncboardResult{
		Outcome:    res.GetOutcome(),
		HTTPStatus: res.GetHttpStatus(),
		Body:       res.GetBodyJson(),
		Reason:     res.GetReason(),
	}}:
	default:
	}
}

// failSyncboard rejects every waiter. It runs when the stream dies, so the
// reason is always transport-error and the outcome always unknown.
func (c *Client) failSyncboard(err *SyncboardError) {
	c.sbMu.Lock()
	waiters := c.sbWaiters
	c.sbWaiters = nil
	c.sbQueue = nil
	c.sbMu.Unlock()
	for _, ch := range waiters {
		select {
		case ch <- sbDelivery{err: err}:
		default:
		}
	}
}
