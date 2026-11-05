package tools

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type reviewObservedContext struct {
	context.Context
	once    sync.Once
	checked chan struct{}
}

func (c *reviewObservedContext) Err() error {
	err := c.Context.Err()
	c.once.Do(func() { close(c.checked) })
	return err
}

func TestReviewCancelledEditWaitDoesNotRead(t *testing.T) {
	base, cancel := context.WithCancel(context.Background())
	defer cancel()
	ctx := &reviewObservedContext{Context: base, checked: make(chan struct{})}
	reads := 0
	queue := NewMutationQueue()
	tool := &EditTool{Cwd: t.TempDir(), Mutations: queue, ReadFile: func(string) ([]byte, error) {
		reads++
		return []byte("original"), nil
	}}
	// Hold the file's mutation slot so the edit below queues behind it.
	held := make(chan struct{})
	release := make(chan struct{})
	go queue.withSlot(tool.Cwd, "synthetic", func() Result {
		close(held)
		<-release
		return textResult("holder")
	})
	<-held
	done := make(chan Result, 1)
	go func() {
		done <- tool.ExecuteCtx(ctx, Invocation{Name: Edit, Arguments: map[string]any{
			"path": "synthetic", "oldText": "original", "newText": "changed",
		}})
	}()
	<-ctx.checked // the entry check passed while the slot was still held
	cancel()
	close(release)
	res := <-done
	if reads != 0 || !res.IsError || !strings.Contains(res.Content[0].Text, "cancel") {
		t.Fatalf("cancelled queue waiter started a read: reads=%d result=%+v", reads, res)
	}
}

func TestReviewDrainGraceStartsAtLeaderExit(t *testing.T) {
	// Jump the drain clock an hour past the leader's last chunk while the
	// leader is still alive. The second clock call is the leader-exit touch;
	// it releases the descendant, which guarantees its write happens after
	// leader exit. Without that touch, drain expires immediately instead.
	dir := t.TempDir()
	reg := NewRegistry(dir)
	var fake atomic.Int64
	fake.Store(time.Now().UnixNano())
	drainReady := filepath.Join(dir, "drain.ready")
	var clockCalls atomic.Int64
	var releaseErr error
	reg.Bash.now = func() time.Time {
		if clockCalls.Add(1) == 2 {
			releaseErr = os.WriteFile(drainReady, nil, 0o600)
		}
		return time.Unix(0, fake.Load())
	}
	gate := filepath.Join(dir, "go")
	done := make(chan Result, 1)
	go func() {
		done <- reg.Dispatch(Invocation{Name: Bash, Arguments: map[string]any{
			"command": "echo $$ > leader.pid; " +
				"(echo ready > descendant.ready; while [ ! -e drain.ready ]; do sleep 0.01; done; printf after-exit) & " +
				"while [ ! -e go ]; do sleep 0.01; done",
		}})
	}()
	t.Cleanup(func() {
		_ = os.WriteFile(gate, nil, 0o600)
		_ = os.WriteFile(drainReady, nil, 0o600)
	})
	reviewWaitPID(t, filepath.Join(dir, "leader.pid"))
	reviewWaitFile(t, filepath.Join(dir, "descendant.ready"))
	fake.Add(int64(time.Hour))
	if err := os.WriteFile(gate, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	select {
	case res := <-done:
		if releaseErr != nil {
			t.Fatalf("release descendant after leader exit: %v", releaseErr)
		}
		if res.IsError || !strings.Contains(res.Content[0].Text, "after-exit") {
			t.Fatalf("quiet leader consumed descendant drain grace: %+v", res)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("post-exit descendant output was not released")
	}
}

func reviewWaitFile(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("fixture did not publish %s", path)
}

func reviewWaitPID(t *testing.T, path string) int {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		b, err := os.ReadFile(path)
		if err == nil {
			pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
			if err == nil && pid > 0 {
				return pid
			}
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("fixture did not publish %s", path)
	return 0
}
