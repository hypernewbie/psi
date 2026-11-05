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
	// The drain clock is injected and jumps an hour past the leader's last
	// chunk before the leader exits. A clock that measured from the last
	// chunk would kill the group at once; the exit touch restarts it, so the
	// descendant's post-exit write is captured without depending on how long
	// a loaded host takes to schedule that write inside 100ms.
	dir := t.TempDir()
	reg := NewRegistry(dir)
	var fake atomic.Int64
	fake.Store(time.Now().UnixNano())
	reg.Bash.now = func() time.Time { return time.Unix(0, fake.Load()) }
	gate := filepath.Join(dir, "go")
	done := make(chan Result, 1)
	go func() {
		done <- reg.Dispatch(Invocation{Name: Bash, Arguments: map[string]any{
			"command": "echo $$ > leader.pid; " +
				"(echo ready > descendant.ready; while [ ! -e go ]; do sleep 0.01; done; sleep 0.03; printf after-exit) & " +
				"while [ ! -e go ]; do sleep 0.01; done",
		}})
	}()
	t.Cleanup(func() { _ = os.WriteFile(gate, nil, 0o600) })
	reviewWaitPID(t, filepath.Join(dir, "leader.pid"))
	reviewWaitFile(t, filepath.Join(dir, "descendant.ready"))
	fake.Add(int64(time.Hour))
	if err := os.WriteFile(gate, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	select {
	case res := <-done:
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
