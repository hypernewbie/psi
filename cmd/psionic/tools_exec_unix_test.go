//go:build linux || darwin

package main

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestRunExecCancellationKillsTheProcessGroup(t *testing.T) {
	const watchdog = 10 * time.Second
	workspace := t.TempDir()
	ready, readyGuard := openExecTestFIFO(t, filepath.Join(workspace, "ready"))
	_, release := openExecTestFIFO(t, filepath.Join(workspace, "release"))

	// The background child opens both pipes, signals readiness, then waits for
	// cleanup to release it. Canceling only the outer shell cannot close the
	// child's write end of ready.
	const command = `(printf R && IFS= read -r line) < release > ready & wait "$!"`
	ctx, cancel := context.WithCancel(context.Background())
	var result execResult
	done := make(chan struct{})
	go func() {
		defer close(done)
		result = runExec(ctx, workspace, command, resolveExecLimits(&execSettings{TimeoutMs: ptr(int64(60000))}))
	}()
	t.Cleanup(func() {
		cancel()
		// Keep the release pipe open and queue a line even if the child has not
		// opened it yet. A failed group kill must not leave the fixture running.
		if _, err := release.WriteString("\n"); err != nil {
			t.Errorf("release background child: %v", err)
		}
		select {
		case <-done:
		case <-time.After(watchdog):
			t.Error("exec did not return during cleanup")
		}
	})

	signal, err := readExecTestFIFO(ready, watchdog)
	if err != nil {
		t.Fatalf("wait for background child: %v", err)
	}
	if signal != 'R' {
		t.Fatalf("unexpected readiness signal: %q", signal)
	}
	if err := readyGuard.Close(); err != nil {
		t.Fatal(err)
	}

	cancel()
	if signal, err := readExecTestFIFO(ready, watchdog); err != io.EOF {
		t.Fatalf("expected EOF from the background child on cancellation, got byte %q, error %v", signal, err)
	}
	select {
	case <-done:
	case <-time.After(watchdog):
		t.Fatal("exec did not return after cancellation")
	}
	if !errors.Is(result.err, context.Canceled) || result.status != "cancelled" {
		t.Fatalf("expected cancellation, got status %q, error %v", result.status, result.err)
	}
}

// Open a temporary nonblocking reader so opening the writer cannot block.
// The writer then lets us open a blocking reader and prevents EOF until the
// subprocess opens its write end of the pipe.
func openExecTestFIFO(t *testing.T, path string) (reader, writer *os.File) {
	t.Helper()
	if err := unix.Mkfifo(path, 0o600); err != nil {
		t.Fatal(err)
	}
	guard, err := os.OpenFile(path, os.O_RDONLY|unix.O_NONBLOCK, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer guard.Close()
	writer, err = os.OpenFile(path, os.O_WRONLY|unix.O_NONBLOCK, 0)
	if err != nil {
		t.Fatal(err)
	}
	reader, err = os.Open(path)
	if err != nil {
		_ = writer.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = writer.Close()
		_ = reader.Close()
	})
	return reader, writer
}

// Named pipes do not support Go file deadlines on all platforms. Cleanup
// releases the child and closes the guard writer to unblock an outstanding read.
func readExecTestFIFO(reader *os.File, watchdog time.Duration) (byte, error) {
	type readResult struct {
		signal byte
		err    error
	}
	done := make(chan readResult, 1)
	go func() {
		var signal [1]byte
		_, err := io.ReadFull(reader, signal[:])
		done <- readResult{signal[0], err}
	}()
	select {
	case result := <-done:
		return result.signal, result.err
	case <-time.After(watchdog):
		return 0, os.ErrDeadlineExceeded
	}
}
