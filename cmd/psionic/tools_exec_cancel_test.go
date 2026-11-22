package main

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// TestRunExecCancellationReturnsPromptly proves that a cancelled run stops
// the command instead of waiting for it. The command sleeps far longer than
// the generous margin below, so a run that waited would fail the test.
func TestRunExecCancellationReturnsPromptly(t *testing.T) {
	workspace := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(150 * time.Millisecond)
		cancel()
	}()
	start := time.Now()
	result := runExec(ctx, workspace, execTestSleep(), resolveExecLimits(&execSettings{TimeoutMs: ptr(int64(60000))}))
	elapsed := time.Since(start)
	if !errors.Is(result.err, context.Canceled) {
		t.Fatalf("expected cancellation, got %+v", result)
	}
	if result.status != "cancelled" {
		t.Fatalf("status = %q", result.status)
	}
	if elapsed > 15*time.Second {
		t.Fatalf("cancelled run took %s", elapsed)
	}
}

// TestRunExecRefusesAnEmptyCommand proves the tool never starts a shell for
// an empty command line.
func TestRunExecRefusesAnEmptyCommand(t *testing.T) {
	result := runExec(context.Background(), t.TempDir(), "   ", resolveExecLimits(nil))
	if result.err == nil {
		t.Fatal("an empty command was accepted")
	}
	if !strings.Contains(result.err.Error(), "command is required") {
		t.Fatalf("error = %v", result.err)
	}
}
