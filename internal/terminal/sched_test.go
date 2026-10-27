package terminal

import (
	"testing"
	"time"
)

// P2-005: fake-clock scheduler, counters, no transcript payload logging.
func TestSchedulerCoalesces(t *testing.T) {
	s := NewScheduler()
	cur := time.Now()
	s.SetClock(func() time.Time { return cur })
	flushed := 0
	for i := 0; i < 100; i++ {
		s.Push("delta", func() { flushed++ })
	}
	// First Ready (same tick) should coalesce, not emit 100 frames.
	ready := s.Ready()
	if len(ready) > 2 {
		t.Fatalf("no coalescing: %d ready", len(ready))
	}
	// Advance past 1/30s, drain all.
	cur = cur.Add(100 * time.Millisecond)
	ready = s.Ready()
	for _, r := range ready {
		r.Flush()
	}
	// Boundaries flush immediately.
	s.Push("boundary", func() { flushed++ })
	if got := s.Ready(); len(got) != 1 {
		t.Fatalf("boundary not immediate: %d", len(got))
	}
}

func TestWriteBounded64KiB(t *testing.T) {
	big := make([]byte, 200*1024)
	for i := range big {
		big[i] = 'a'
	}
	batches := Batches(string(big), 64*1024)
	if len(batches) < 3 {
		t.Fatalf("expected bounded batches, got %d", len(batches))
	}
	for _, b := range batches {
		if len(b) > 64*1024 {
			t.Fatalf("batch exceeds 64KiB: %d", len(b))
		}
	}
}
