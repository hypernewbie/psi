package agent

// Phase H regressions for PLAN3 F14: compaction threshold arithmetic cannot
// wrap. Pi compares contextTokens > contextWindow - reserveTokens; the psi
// port must agree across zero, exact, over, above-window, and MaxInt.

import (
	"math"
	"testing"
)

func TestF14ThresholdBoundaries(t *testing.T) {
	const window = 200000
	for _, tc := range []struct {
		est, reserve int
		want         bool
	}{
		{0, 0, false},                    // zero
		{window, 0, false},               // exact threshold
		{window + 1, 0, true},            // one over
		{1000, 1000, false},              // ordinary below
		{199001, 1000, true},             // ordinary above
		{1000, window, true},             // reserve at window
		{1000, window + 1, true},         // reserve above window
		{0, window + 1, false},           // empty context never compacts
		{1000, math.MaxInt, true},        // former wrap: must be true
		{0, math.MaxInt, false},          // empty stays false
		{math.MaxInt, 0, true},           // huge estimate
		{math.MaxInt, math.MaxInt, true}, // no wrap on either side
	} {
		if got := ShouldCompact(tc.est, window, tc.reserve, true); got != tc.want {
			t.Errorf("est=%d reserve=%d: got %v want %v", tc.est, tc.reserve, got, tc.want)
		}
	}
	if ShouldCompact(1, window, 0, false) {
		t.Error("disabled must not compact")
	}
	if ShouldCompact(1, 0, 0, true) || ShouldCompact(1, -5, 0, true) {
		t.Error("unknown context must not compact")
	}
}

func TestF14Monotonicity(t *testing.T) {
	const window = 200000
	// Increasing estimates never un-trigger; increasing reserves never relax.
	for _, reserve := range []int{0, 1000, window - 1, window, window + 1, math.MaxInt} {
		last := false
		for est := 0; est <= window+10; est += 7 {
			got := ShouldCompact(est, window, reserve, true)
			if !last && got {
				last = true
			}
			if last && !got {
				t.Fatalf("non-monotonic estimate %d reserve %d", est, reserve)
			}
		}
	}
	for est := 0; est <= 10; est++ {
		// Decreasing reserve relaxes: once false, stays false.
		falseSeen := false
		for reserve := window + 10; reserve >= 0; reserve -= 1000 {
			got := ShouldCompact(est, window, reserve, true)
			if !got {
				falseSeen = true
			} else if falseSeen {
				t.Fatalf("non-monotonic reserve %d est %d", reserve, est)
			}
		}
	}
}

// TestF14WrapClassAudit records the related-arithmetic review (P3-045):
// bash timeouts validate finite seconds within Duration range; retry backoff
// is attempt-capped; OAuth expiry validates before Duration conversion;
// estimator sums are bounded by the 32MiB record limit. Only the threshold
// addition could wrap, and it no longer adds.
func TestF14WrapClassAudit(t *testing.T) {
	if got := ShouldCompact(1000, 200000, math.MaxInt, true); !got {
		t.Fatal("MaxInt reserve must trigger, not wrap to false")
	}
}
