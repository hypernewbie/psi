//go:build unix

package app

import (
	"bytes"
	"context"
	"encoding/json"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"psi/internal/provider"
)

type compactWaitProvider struct {
	calls       atomic.Int32
	entered     chan struct{}
	cancelled   chan struct{}
	release     chan struct{}
	enteredOnce sync.Once
	cancelOnce  sync.Once
}

func (p *compactWaitProvider) Send(ctx context.Context, _ provider.Request, _ func(provider.Delta)) (*provider.Response, error) {
	p.calls.Add(1)
	p.enteredOnce.Do(func() { close(p.entered) })
	<-ctx.Done()
	p.cancelOnce.Do(func() { close(p.cancelled) })
	<-p.release
	return &provider.Response{Text: "settled", StopReason: "stop"}, nil
}

func (*compactWaitProvider) CloseIdle() {}

// TestRPCAbortDuringCompactWaitPreventsSummary pins the waiting phase: abort
// cancels the reservation before the summary starts, so the provider is never
// called a second time and the terminal compact response reports the abort.
func TestRPCAbortDuringCompactWaitPreventsSummary(t *testing.T) {
	prov := &compactWaitProvider{entered: make(chan struct{}), cancelled: make(chan struct{}), release: make(chan struct{})}
	s, _, log := newRPCTestRunner(t, prov, nil)
	if resp := s.handle(rpcCommand{ID: strptr("p1"), Type: "prompt", Message: "work"}); resp == nil || !resp.Success {
		t.Fatalf("prompt = %+v", resp)
	}
	<-prov.entered
	if resp := s.handle(rpcCommand{ID: strptr("c1"), Type: "compact"}); resp != nil {
		t.Fatalf("compact admission must be asynchronous, got %+v", resp)
	}
	if resp := s.handle(rpcCommand{ID: strptr("a1"), Type: "abort"}); resp == nil || !resp.Success {
		t.Fatalf("abort = %+v", resp)
	}
	<-prov.cancelled
	close(prov.release)
	log.waitFor(t, "aborted compact response", func(obj map[string]any) bool {
		return obj["type"] == "response" && obj["id"] == "c1"
	})
	objects := log.objects(t)
	got := responseByID(objects, "c1")
	if got == nil || got["success"] != false {
		t.Fatalf("compact response = %v, want failure", got)
	}
	if hasEvent(objects, "compaction_start", nil) || hasEvent(objects, "compaction_end", nil) {
		t.Fatalf("aborted wait started a summary: %v", objects)
	}
	if n := prov.calls.Load(); n != 1 {
		t.Fatalf("provider calls = %d, want the interrupted run only", n)
	}
}

type blockingJSONWriter struct {
	entered chan struct{}
	release chan struct{}
	once    sync.Once

	mu  sync.Mutex
	buf bytes.Buffer
}

func (w *blockingJSONWriter) Write(p []byte) (int, error) {
	w.once.Do(func() { close(w.entered) })
	<-w.release
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.Write(p)
}

func (w *blockingJSONWriter) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.String()
}

// TestRPCTerminalFrameOrdersOwnershipRelease holds the compact response write
// and proves shutdown cannot release the store before that terminal frame is
// complete.
func TestRPCTerminalFrameOrdersOwnershipRelease(t *testing.T) {
	s, store, _ := newRPCTestRunner(t, &provider.Fake{}, nil)
	s.shutdown = make(chan struct{})
	writer := &blockingJSONWriter{entered: make(chan struct{}), release: make(chan struct{})}
	enc := json.NewEncoder(writer)
	enc.SetEscapeHTML(false)
	s.out = enc

	if resp := s.handle(rpcCommand{ID: strptr("c1"), Type: "compact"}); resp != nil {
		t.Fatalf("compact admission must be asynchronous, got %+v", resp)
	}
	select {
	case <-writer.entered:
	case <-time.After(8 * time.Second):
		t.Fatal("compact response never reached the writer")
	}
	if err := store.TryLock(); err == nil {
		store.Unlock()
		t.Fatal("store was released while the compact response was still being written")
	}

	shutdownStarted := make(chan struct{})
	shutdownDone := make(chan struct{})
	go func() {
		close(shutdownStarted)
		s.cancelActive()
		s.waitWorkers()
		store.Unlock()
		close(shutdownDone)
	}()
	<-shutdownStarted
	select {
	case <-shutdownDone:
		t.Fatal("shutdown released the store before the terminal frame completed")
	default:
	}
	if err := store.TryLock(); err == nil {
		store.Unlock()
		t.Fatal("store was released while shutdown waited on the terminal frame")
	}
	close(writer.release)
	select {
	case <-shutdownDone:
	case <-time.After(8 * time.Second):
		t.Fatal("shutdown did not finish after the terminal frame")
	}
	if err := store.TryLock(); err != nil {
		t.Fatalf("store stayed locked after shutdown: %v", err)
	}
	store.Unlock()
	if !bytes.Contains([]byte(writer.String()), []byte(`"id":"c1"`)) {
		t.Fatalf("terminal frame missing from writer: %s", writer.String())
	}
}
