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
// leaves no child behind. The command starts a background child, publishes its
// process id, and waits for the child. The child is in the process group before
// the test cancels, so the cancellation must kill an already populated group.
func TestRunExecCancellationKillsTheProcessGroup(t *testing.T) {
	workspace := t.TempDir()
	pidFile := filepath.Join(workspace, "pid")
	// The shell creates and truncates a redirection target before it writes the
	// number. Write the process id to a staging file and rename it into place,
	// so the watched path appears only after the complete value is published.
	stagingFile := pidFile + ".tmp"
	command := "sleep 30 & echo $$ > " + stagingFile + " && mv " + stagingFile + " " + pidFile + "; wait"

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan execResult, 1)
	go func() {
		done <- runExec(ctx, workspace, command, resolveExecLimits(&execSettings{TimeoutMs: ptr(int64(60000))}))
	}()

	waitForPath(t, pidFile)
	pid, err := readPID(t, pidFile)
	if err != nil {
		t.Fatal(err)
	}
	cancel()

	result := <-done
	if !errors.Is(result.err, context.Canceled) {
		t.Fatalf("expected cancellation, got %+v", result)
	}
	if result.status != "cancelled" {
		t.Fatalf("status = %q", result.status)
	}
	if err := syscall.Kill(pid, 0); err == nil {
		t.Fatalf("process %d survived the cancellation", pid)
	} else if !errors.Is(err, syscall.ESRCH) {
		t.Fatalf("process check failed: %v", err)
	}
}

// waitForPath waits until the child has published its process id. The command
// renames the file into place, so an existing path holds the complete value.
// The wait is a synchronization point with the child, not a performance
// budget: the deadline only bounds a failing test.
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
