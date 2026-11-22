//go:build linux || darwin

package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestRunExecCancellationKillsTheProcessGroup proves that a cancelled run
// leaves no child behind. The command publishes its process id and then waits
// forever; after the cancellation the process must be gone.
func TestRunExecCancellationKillsTheProcessGroup(t *testing.T) {
	workspace := t.TempDir()
	pidFile := filepath.Join(workspace, "pid")
	command := "echo $$ > " + pidFile + "; sleep 30"
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		waitForPath(t, pidFile)
		cancel()
	}()
	result := runExec(ctx, workspace, command, resolveExecLimits(&execSettings{TimeoutMs: ptr(int64(60000))}))
	if !errors.Is(result.err, context.Canceled) {
		t.Fatalf("expected cancellation, got %+v", result)
	}
	if result.status != "cancelled" {
		t.Fatalf("status = %q", result.status)
	}
	pid, err := readPID(t, pidFile)
	if err != nil {
		t.Fatal(err)
	}
	if err := syscall.Kill(pid, 0); err == nil {
		t.Fatalf("process %d survived the cancellation", pid)
	} else if !errors.Is(err, syscall.ESRCH) {
		t.Fatalf("process check failed: %v", err)
	}
}

// waitForPath waits until the child has published its process id. The wait is
// a synchronization point with the child, not a performance budget: the
// deadline only bounds a failing test.
func waitForPath(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("the command never published %s", filepath.Base(path))
}

func readPID(t *testing.T, path string) (int, error) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		return 0, err
	}
	return pid, nil
}
