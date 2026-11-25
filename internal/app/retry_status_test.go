package app

import (
	"testing"
	"time"
)

func TestRetryActivityShowsAttemptAndRoundedBackoff(t *testing.T) {
	if got, want := formatRetryActivity(1, 3, 1500*time.Millisecond), "Retrying (1/3) in 2s... (escape to cancel)"; got != want {
		t.Fatalf("got %q want %q", got, want)
	}
	if got, want := formatRetryActivity(2, 3, 250*time.Millisecond), "Retrying (2/3) in 1s... (escape to cancel)"; got != want {
		t.Fatalf("got %q want %q", got, want)
	}
	if got, want := formatRetryActivity(1, 3, 0), "Retrying (1/3) in 0s... (escape to cancel)"; got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

// Pi's CountdownTimer starts at the rounded-up wait and drops one second at
// each full second of the wait, then stays at zero.
func TestRetryIndicatorCountsDownLikePi(t *testing.T) {
	started := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
	indicator := &retryIndicator{attempt: 2, maximum: 3, delay: 2500 * time.Millisecond, started: started}
	for _, tc := range []struct {
		elapsed time.Duration
		want    string
	}{
		{0, "Retrying (2/3) in 3s... (escape to cancel)"},
		{999 * time.Millisecond, "Retrying (2/3) in 3s... (escape to cancel)"},
		{time.Second, "Retrying (2/3) in 2s... (escape to cancel)"},
		{2 * time.Second, "Retrying (2/3) in 1s... (escape to cancel)"},
		{2500 * time.Millisecond, "Retrying (2/3) in 1s... (escape to cancel)"},
		{3 * time.Second, "Retrying (2/3) in 0s... (escape to cancel)"},
		{time.Hour, "Retrying (2/3) in 0s... (escape to cancel)"},
	} {
		if got := indicator.text(started.Add(tc.elapsed)); got != tc.want {
			t.Fatalf("text after %v = %q, want %q", tc.elapsed, got, tc.want)
		}
	}
	if got, want := formatRetryActivity(2, 3, 2500*time.Millisecond), indicator.text(started); got != want {
		t.Fatalf("indicator start %q differs from the activity text %q", got, want)
	}
}
