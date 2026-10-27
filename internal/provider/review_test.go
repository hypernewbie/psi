package provider

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestReviewCancelledRetryKeepsPartialUsage(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	partial := &Response{Usage: Usage{Input: 777}}
	calls := 0
	resp, err := sendWithRetry(ctx, 3, func() (*Response, error) {
		calls++
		cancel()
		return partial, &StreamInterrupt{Partial: partial, Err: errors.New("synthetic stream interruption")}
	})
	var interrupted *StreamInterrupt
	if !errors.As(err, &interrupted) || !interrupted.KnownUsage() || !errors.Is(err, context.Canceled) || resp != partial || calls != 1 {
		t.Fatalf("cancel discarded known usage: response=%+v error=%v calls=%d", resp, err, calls)
	}
}

func TestReviewAbandonedPsiLeaseIsRecoverable(t *testing.T) {
	lock := filepath.Join(t.TempDir(), "auth.json.lock")
	if err := os.Mkdir(lock, 0o700); err != nil {
		t.Fatal(err)
	}
	// This is the on-disk state left by a killed PLAN3 psi holder.
	if err := os.WriteFile(filepath.Join(lock, "owner"), []byte("abandoned synthetic owner"), 0o600); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(lock, old, old); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()
	unlock, valid, err := acquireAuthLock(ctx, lock, 0, 0)
	if err != nil {
		t.Fatalf("abandoned psi lease cannot recover: %v", err)
	}
	defer unlock()
	if !valid() {
		t.Fatal("replacement lease is invalid")
	}
}

func TestReviewAuthLeaseUsesEmptyPiDirectory(t *testing.T) {
	lock := filepath.Join(t.TempDir(), "auth.json.lock")
	unlock, _, err := acquireAuthLock(context.Background(), lock, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	entries, err := os.ReadDir(lock)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatal("Pi proper-lockfile cannot reclaim a nonempty lease directory after a crash")
	}
}
