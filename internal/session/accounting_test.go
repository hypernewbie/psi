package session

import (
	"math"
	"testing"
)

func TestAccountingCacheHitRateUsesLatestAssistantMessage(t *testing.T) {
	store, err := New(OpenOptions{Cwd: "/tmp/ws", Persist: false})
	if err != nil {
		t.Fatal(err)
	}
	appendUsageMessage(t, store, Usage{Input: 100, CacheRead: 0, CacheWrite: 0}, "stop")
	appendUsageMessage(t, store, Usage{Input: 126, CacheRead: 874, CacheWrite: 0}, "stop")
	accounting, err := store.Accounting()
	if err != nil {
		t.Fatal(err)
	}
	if accounting.CacheHitRate == nil {
		t.Fatal("CacheHitRate is nil after two assistant messages")
	}
	if math.Abs(*accounting.CacheHitRate-87.4) > 0.0001 {
		t.Fatalf("CacheHitRate=%v; want 87.4", *accounting.CacheHitRate)
	}
}

func TestAccountingCacheHitRateClearedWhenLatestMessageHasNoPromptTokens(t *testing.T) {
	store, err := New(OpenOptions{Cwd: "/tmp/ws", Persist: false})
	if err != nil {
		t.Fatal(err)
	}
	appendUsageMessage(t, store, Usage{Input: 126, CacheRead: 874, CacheWrite: 0}, "stop")
	appendUsageMessage(t, store, Usage{Output: 5}, "stop")
	accounting, err := store.Accounting()
	if err != nil {
		t.Fatal(err)
	}
	if accounting.CacheHitRate != nil {
		t.Fatalf("CacheHitRate=%v; want nil when the latest assistant message has no prompt tokens", *accounting.CacheHitRate)
	}
}

func TestAccountingCacheHitRateIsAbsentWithoutAssistantMessages(t *testing.T) {
	t.Run("user only", func(t *testing.T) {
		store, err := New(OpenOptions{Cwd: "/tmp/ws", Persist: false})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := store.AppendMessage(AgentMessage{Role: "user", User: &UserMessage{Role: "user", Content: "hi"}}); err != nil {
			t.Fatal(err)
		}
		accounting, err := store.Accounting()
		if err != nil {
			t.Fatal(err)
		}
		if accounting.CacheHitRate != nil {
			t.Fatalf("CacheHitRate=%v; want nil", *accounting.CacheHitRate)
		}
	})
	t.Run("zero prompt tokens", func(t *testing.T) {
		store, err := New(OpenOptions{Cwd: "/tmp/ws", Persist: false})
		if err != nil {
			t.Fatal(err)
		}
		appendUsageMessage(t, store, Usage{}, "stop")
		accounting, err := store.Accounting()
		if err != nil {
			t.Fatal(err)
		}
		if accounting.CacheHitRate != nil {
			t.Fatalf("CacheHitRate=%v; want nil", *accounting.CacheHitRate)
		}
	})
}
