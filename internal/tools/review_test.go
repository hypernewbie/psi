package tools

import (
	"context"
	"os"
	"strconv"
	"strings"
	"sync"
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
	tool := &EditTool{Cwd: t.TempDir(), ReadFile: func(string) ([]byte, error) {
		reads++
		return []byte("original"), nil
	}}
	tool.mu.Lock()
	done := make(chan Result, 1)
	go func() {
		done <- tool.ExecuteCtx(ctx, Invocation{Name: Edit, Arguments: map[string]any{
			"path": "synthetic", "oldText": "original", "newText": "changed",
		}})
	}()
	<-ctx.checked // the entry check passed while the mutex was still held
	cancel()
	tool.mu.Unlock()
	res := <-done
	if reads != 0 || !res.IsError || !strings.Contains(res.Content[0].Text, "cancel") {
		t.Fatalf("cancelled queue waiter started a read: reads=%d result=%+v", reads, res)
	}
}

func TestReviewDrainGraceStartsAtLeaderExit(t *testing.T) {
	res := NewRegistry(t.TempDir()).Dispatch(Invocation{Name: Bash, Arguments: map[string]any{
		"command": "sleep 0.25; (sleep 0.04; printf after-exit) &",
	}})
	if res.IsError || !strings.Contains(res.Content[0].Text, "after-exit") {
		t.Fatalf("quiet leader consumed descendant drain grace: %+v", res)
	}
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
	t.Fatal("fixture did not publish its PID")
	return 0
}
