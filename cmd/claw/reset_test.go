package main

import (
	"testing"
	"time"
)

func intPtr(v int) *int { return &v }

func TestResolveResetPolicy(t *testing.T) {
	base := &sessionSettings{
		Reset: &resetSettings{Mode: "daily", AtHour: intPtr(4), IdleMinutes: intPtr(30)},
		ByType: &resetByType{
			Thread: &resetSettings{Mode: "idle", IdleMinutes: intPtr(15)},
			Group:  &resetSettings{AtHour: intPtr(6)},
		},
	}
	if got := resolveResetPolicy(nil, "direct", nil); got.mode != "none" || got.atHour != 4 {
		t.Fatalf("default = %+v", got)
	}
	if got := resolveResetPolicy(base, "direct", nil); got.mode != "daily" || got.atHour != 4 || got.idleMinutes != 30 {
		t.Fatalf("direct = %+v", got)
	}
	if got := resolveResetPolicy(base, "group", nil); got.mode != "daily" || got.atHour != 6 {
		t.Fatalf("group = %+v", got)
	}
	if got := resolveResetPolicy(base, "thread", nil); got.mode != "idle" || got.idleMinutes != 15 {
		t.Fatalf("thread = %+v", got)
	}
	override := &resetSettings{Mode: "none"}
	if got := resolveResetPolicy(base, "thread", override); got.mode != "none" {
		t.Fatalf("override = %+v", got)
	}
	if got := resolveResetPolicy(&sessionSettings{Reset: &resetSettings{AtHour: intPtr(99)}}, "direct", nil); got.atHour != 23 {
		t.Fatalf("atHour clamp = %+v", got)
	}
	disabled := &sessionSettings{Reset: &resetSettings{Mode: "none"}, ByType: &resetByType{Group: &resetSettings{AtHour: intPtr(6)}}}
	if got := resolveResetPolicy(disabled, "group", nil); got.mode != "none" {
		t.Fatalf("per-type entry enabled a disabled base: %+v", got)
	}
}

func TestDailyResetAt(t *testing.T) {
	loc := time.FixedZone("test", -5*60*60)
	after := time.Date(2026, 3, 10, 9, 0, 0, 0, loc)
	if got := dailyResetAt(after, 4); !got.Equal(time.Date(2026, 3, 10, 4, 0, 0, 0, loc)) {
		t.Fatalf("after boundary = %v", got)
	}
	before := time.Date(2026, 3, 10, 3, 0, 0, 0, loc)
	if got := dailyResetAt(before, 4); !got.Equal(time.Date(2026, 3, 9, 4, 0, 0, 0, loc)) {
		t.Fatalf("before boundary = %v", got)
	}
}

func TestSessionFresh(t *testing.T) {
	now := time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC)
	daily := resetPolicy{mode: "daily", atHour: 4}
	if fresh, reason := sessionFresh(now.Add(-2*time.Hour), now, now, daily); !fresh {
		t.Fatalf("fresh session stale: %s", reason)
	}
	if fresh, reason := sessionFresh(now.Add(-10*time.Hour), now, now, daily); fresh || reason != "daily" {
		t.Fatalf("old session = %v %q", fresh, reason)
	}
	idle := resetPolicy{mode: "idle", idleMinutes: 30}
	if fresh, reason := sessionFresh(now.Add(-10*time.Hour), now.Add(-10*time.Minute), now, idle); !fresh {
		t.Fatalf("recent interaction stale: %s", reason)
	}
	if fresh, reason := sessionFresh(now.Add(-10*time.Hour), now.Add(-45*time.Minute), now, idle); fresh || reason != "idle" {
		t.Fatalf("idle session = %v %q", fresh, reason)
	}
	if fresh, _ := sessionFresh(now.AddDate(0, 0, -2), now.AddDate(0, 0, -2), now, resetPolicy{mode: "none"}); !fresh {
		t.Fatal("none policy expired")
	}
}
