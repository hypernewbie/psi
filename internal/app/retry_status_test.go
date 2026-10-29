package app

import (
	"testing"
	"time"
)

func TestRetryActivityShowsAttemptAndRoundedBackoff(t *testing.T) {
	if got, want := formatRetryActivity(1, 3, 1500*time.Millisecond), "Retrying 1/3 · 2s (Esc/Ctrl+C cancel)"; got != want {
		t.Fatalf("got %q want %q", got, want)
	}
	if got, want := formatRetryActivity(2, 3, 250*time.Millisecond), "Retrying 2/3 · 250ms (Esc/Ctrl+C cancel)"; got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}
