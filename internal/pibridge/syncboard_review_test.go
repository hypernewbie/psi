package pibridge

import (
	"context"
	"testing"
	"time"

	pb "psi/internal/pibridge/pb"
)

func TestSyncboardDropCapsDiscardsUnsentQueue(t *testing.T) {
	c := &Client{}
	ch := make(chan sbDelivery, 1)
	c.setCaps(map[string]bool{capSyncboard: true})
	c.sbWaiters = map[string]chan sbDelivery{"1": ch}
	c.sbQueue = []sbQueued{{id: "1", op: pb.SyncboardOperation_SYNCBOARD_OPERATION_UPSERT, key: "key"}}
	c.dropCaps()
	if q := c.takeSyncboard(); len(q) != 0 {
		t.Fatalf("queued mutation survived disconnect: %+v", q)
	}
	select {
	case result := <-ch:
		if result.err == nil || result.err.Outcome != "unknown" {
			t.Fatalf("delivery = %+v", result)
		}
	default:
		t.Fatal("waiter not failed")
	}
}

func TestSyncboardCancelledQueuedMutationIsRemoved(t *testing.T) {
	c := &Client{}
	c.setCaps(map[string]bool{capSyncboard: true})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := c.CallSyncboard(ctx, pb.SyncboardOperation_SYNCBOARD_OPERATION_UPSERT, "key", []byte(`1`))
		done <- err
	}()
	deadline := time.After(2 * time.Second)
	for {
		c.sbMu.Lock()
		n := len(c.sbQueue)
		c.sbMu.Unlock()
		if n > 0 {
			break
		}
		select {
		case <-deadline:
			t.Fatal("not queued")
		default:
			time.Sleep(time.Millisecond)
		}
	}
	cancel()
	if err := <-done; err == nil {
		t.Fatal("cancel succeeded")
	}
	if q := c.takeSyncboard(); len(q) != 0 {
		t.Fatalf("cancelled mutation still queued: %+v", q)
	}
}
