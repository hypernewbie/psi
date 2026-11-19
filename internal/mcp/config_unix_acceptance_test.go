//go:build unix && acceptance

package mcp

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestInterpolationCancellationWithInheritedOutput(t *testing.T) {
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash is required for the process-group fixture")
	}
	dir := t.TempDir()
	openFIFO := func(name string) *os.File {
		t.Helper()
		path := filepath.Join(dir, name)
		if err := syscall.Mkfifo(path, 0o600); err != nil {
			t.Fatal(err)
		}
		// Keep both ends open so a peer can open either end without blocking.
		f, err := os.OpenFile(path, os.O_RDWR, 0)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = f.Close() })
		return f
	}
	ready, release := openFIFO("ready"), openFIFO("release")
	t.Setenv("PSI_SHELL", bash)
	t.Setenv("MCP_TEST_READY", ready.Name())
	t.Setenv("MCP_TEST_RELEASE", release.Name())
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	defer func() {
		_, _ = io.WriteString(ready, "r") // Unblock the reader if startup failed.
		_, _ = io.WriteString(release, "release\n")
	}()

	// Job control puts the background job in a separate group. It keeps the
	// shell's output descriptors until the test releases it, even when the
	// interpolation runner kills the shell's group.
	done := make(chan error, 1)
	go func() {
		_, err := runInterpolationCommand(ctx, `set -m
			(exec 3< "$MCP_TEST_RELEASE"; printf r > "$MCP_TEST_READY"; read -r _ <&3) &
			wait`)
		done <- err
	}()
	started := make(chan error, 1)
	go func() {
		var b [1]byte
		_, err := io.ReadFull(ready, b[:])
		started <- err
	}()
	watchdog := time.NewTimer(8 * time.Second)
	defer watchdog.Stop()
	select {
	case err := <-started:
		if err != nil {
			t.Fatal(err)
		}
	case err := <-done:
		t.Fatalf("interpolation returned before the fixture was ready: %v", err)
	case <-watchdog.C:
		t.Fatal("fixture did not signal readiness")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("interpolation returned %v, want context.Canceled", err)
		}
	case <-watchdog.C:
		t.Fatal("cancellation waited for the descendant to close its output pipes")
	}
}
