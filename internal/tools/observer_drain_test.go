package tools

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestBashObserverDrain proves the post-exit idle drain does not release a
// process group while a slow observer still owns an accepted chunk, and that
// the grace restarts after the observer returns.
func TestBashObserverDrain(t *testing.T) {
	dir := t.TempDir()
	reg := NewRegistry(dir)
	var fake atomic.Int64
	fake.Store(time.Now().UnixNano())
	reg.Bash.now = func() time.Time { return time.Unix(0, fake.Load()) }

	observed := make(chan struct{})
	afterSeen := make(chan struct{})
	release := make(chan struct{})
	var first sync.Once
	var sawAfter sync.Once
	onOutput := func(p []byte) {
		if strings.Contains(string(p), "FIRST") {
			first.Do(func() { close(observed) })
			<-release
		}
		if strings.Contains(string(p), "AFTER") {
			sawAfter.Do(func() { close(afterSeen) })
		}
	}
	done := make(chan BashRunResult, 1)
	go func() {
		done <- reg.Bash.Run(t.Context(), "echo FIRST; (echo ready > ready; while [ ! -e go ]; do sleep 0.01; done; printf AFTER) &", 0, onOutput)
	}()
	reviewWaitFile(t, filepath.Join(dir, "ready"))
	select {
	case <-observed:
	case <-time.After(2 * time.Second):
		t.Fatal("observer did not receive the first chunk")
	}
	// Advancing the clock far past the grace must not release the group while
	// the first observer call is still blocked.
	fake.Add(int64(time.Hour))
	if err := os.WriteFile(filepath.Join(dir, "go"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	close(release)
	select {
	case <-afterSeen:
	case <-time.After(2 * time.Second):
		t.Fatal("descendant output was lost while the observer blocked")
	}
	// The accepted AFTER chunk restarted the grace at the new clock.
	fake.Add(int64(time.Hour))
	select {
	case result := <-done:
		if result.InfraErr != nil {
			t.Fatalf("observer drain failed: %v", result.InfraErr)
		}
		if !strings.Contains(result.Output, "AFTER") {
			t.Fatalf("observer drain output = %q", result.Output)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("observer drain did not release the process group")
	}
}
