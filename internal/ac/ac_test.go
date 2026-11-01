package ac

import (
	"math"
	"reflect"
	"testing"
	"time"
)

func TestGrowthHistoryUsesPositiveRecentDeltas(t *testing.T) {
	var history GrowthHistory
	for _, tokens := range []int{20_000, 30_000, 28_000, 40_000, 42_000, 80_000, 90_000, 91_000} {
		history.Observe(tokens)
	}
	if got, want := history.Deltas, []int{12_000, 2_000, 38_000, 10_000, 1_000}; !reflect.DeepEqual(got, want) {
		t.Fatalf("deltas = %v, want %v", got, want)
	}
	if got := history.EstimateNextTurnGrowth(); got != 38_000 {
		t.Fatalf("forecast = %d, want max recent delta 38000", got)
	}
	history.Reset()
	if history.PreviousTokens != nil || len(history.Deltas) != 0 {
		t.Fatalf("reset left growth state: %+v", history)
	}
}

func TestAdaptiveThresholdPolicy(t *testing.T) {
	window, reserve := 100_000, 16_384
	target := 50.0
	cases := []struct {
		name                 string
		tokens, growth       int
		target               *float64
		enabled, wantCompact bool
	}{
		{"below target", 49_999, 0, &target, true, false},
		{"at target", 50_000, 0, &target, true, true},
		{"forecast crosses target", 45_000, 5_000, &target, true, true},
		{"target capped by native reserve", 39_000, 0, &target, true, false},
		{"forecast reaches target", 39_000, 11_000, &target, true, true},
		{"native threshold already crossed", 83_616, 0, &target, true, false},
		{"native auto-compaction disabled", 50_000, 0, &target, false, false},
		{"off uses native threshold", 82_000, 1_616, nil, true, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ShouldAutoCompact(tc.tokens, window, reserve, tc.enabled, tc.target, tc.growth)
			if got != tc.wantCompact {
				t.Fatalf("ShouldAutoCompact() = %v, want %v", got, tc.wantCompact)
			}
		})
	}
	if ShouldAutoCompact(39_000, window, 60_000, true, &target, 0) {
		t.Fatal("predictive target should be capped by a higher native reserve")
	}
	if ShouldAutoCompact(-1, window, reserve, true, &target, 0) {
		t.Fatal("negative context estimate should not trigger")
	}
}

func TestEndOfTurnThresholdParsingAndPolicy(t *testing.T) {
	for input, want := range map[string]EndOfTurnThreshold{
		"120000": {Unit: "tokens", Value: 120_000},
		"75.5%":  {Unit: "percent", Value: 75.5},
	} {
		got, ok := ParseEndOfTurnThreshold(input)
		if !ok || *got != want {
			t.Fatalf("ParseEndOfTurnThreshold(%q) = %+v, %v; want %+v", input, got, ok, want)
		}
	}
	if got, ok := ParseEndOfTurnThreshold("off"); !ok || got != nil {
		t.Fatalf("off threshold = %+v, %v", got, ok)
	}
	for _, input := range []string{"", "0", "101%", "75 %", "9007199254740992", "NaN%"} {
		if _, ok := ParseEndOfTurnThreshold(input); ok {
			t.Errorf("invalid threshold %q accepted", input)
		}
	}
	threshold := &EndOfTurnThreshold{Unit: "percent", Value: 40}
	if ShouldCompactAtEndOfTurn(199_999, 400_000, threshold, DefaultEndTurnMinTokens) {
		t.Fatal("default minimum tokens was not applied")
	}
	if !ShouldCompactAtEndOfTurn(200_000, 400_000, threshold, DefaultEndTurnMinTokens) {
		t.Fatal("default threshold should trigger at 200K")
	}
	if ShouldCompactAtEndOfTurn(399_999, 1_000_000, threshold, DefaultEndTurnMinTokens) {
		t.Fatal("40% of one million should be 400K")
	}
	if !ShouldCompactAtEndOfTurn(400_000, 1_000_000, threshold, DefaultEndTurnMinTokens) {
		t.Fatal("40% threshold did not trigger")
	}
}

func TestEndOfTurnDelayParsing(t *testing.T) {
	for input, want := range map[string]time.Duration{"0": 0, "1": time.Minute, "0.5": 30 * time.Second, "10": 10 * time.Minute} {
		got, ok := ParseEndOfTurnDelay(input)
		if !ok || got != want {
			t.Errorf("ParseEndOfTurnDelay(%q) = %s, %v; want %s", input, got, ok, want)
		}
	}
	for _, input := range []string{"-1", "NaN", "Inf", "1e9", "+2", ""} {
		if _, ok := ParseEndOfTurnDelay(input); ok {
			t.Errorf("invalid delay %q accepted", input)
		}
	}
	if _, ok := ParseEndOfTurnDelay("35791.4"); ok {
		t.Fatal("delay beyond timer limit was accepted")
	}
}

func TestControllerSuppressionResetAndSessionDefaults(t *testing.T) {
	controller := NewController()
	if got := controller.Snapshot(); !got.Enabled || got.EndOfTurnDelay != 10*time.Minute || got.TargetPercent == nil || *got.TargetPercent != 50 {
		t.Fatalf("wrong defaults: %+v", got)
	}
	if controller.ObserveTurn(40_000, 100_000, 16_384, true, true, false) {
		t.Fatal("first observation should not trigger")
	}
	if controller.ObserveTurn(70_000, 100_000, 16_384, true, true, true) {
		t.Fatal("pending user message must suppress proactive compaction")
	}
	if got := controller.Snapshot().Growth.EstimateNextTurnGrowth(); got != 30_000 {
		t.Fatalf("pending-turn growth was not observed: %d", got)
	}
	controller.RecordCompaction(true, "automatic", 70_000)
	if got := controller.Snapshot(); got.Growth.PreviousTokens != nil || got.CompactionCount != 1 || got.LastCompaction == nil {
		t.Fatalf("compaction did not reset and record state: %+v", got)
	}
	controller.RecordCompaction(false, "automatic", 80_000)
	if !controller.Snapshot().StoppedAfterError {
		t.Fatal("failed compaction did not stop automatic attempts")
	}
	controller.SetEnabled(true)
	if controller.Snapshot().StoppedAfterError {
		t.Fatal("enable did not clear the error stop")
	}
	controller.SetCompactorModel("anthropic/model")
	controller.ResetSession()
	got := controller.Snapshot()
	if got.CompactorModel != "" || got.CompactionCount != 0 || !got.Enabled || got.TargetPercent == nil || math.IsNaN(*got.TargetPercent) {
		t.Fatalf("session reset failed: %+v", got)
	}
}
