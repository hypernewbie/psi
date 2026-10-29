package agent

import (
	"context"
	"strings"
	"testing"

	"psi/internal/provider"
)

type cancelAfterFirstCompactionSummary struct {
	fake   *provider.Fake
	cancel context.CancelFunc
	calls  int
}

func (p *cancelAfterFirstCompactionSummary) Send(ctx context.Context, request provider.Request, onText func(string)) (*provider.Response, error) {
	p.calls++
	response, err := p.fake.Send(ctx, request, onText)
	if p.calls == 1 {
		p.cancel()
	}
	return response, err
}

func (p *cancelAfterFirstCompactionSummary) CloseIdle() { p.fake.CloseIdle() }

func TestSplitCompactionCancellationRetainsKnownUsageWithoutCheckpoint(t *testing.T) {
	store, _, fake, loop, _ := testSetup(t)
	if _, err := store.AppendMessage(userMsg("older task")); err != nil {
		t.Fatal(err)
	}
	appendAssistantText(t, store, "older progress")
	if _, err := store.AppendMessage(userMsg("current task")); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 4; i++ {
		appendAssistantText(t, store, strings.Repeat(string(rune('a'+i)), 80_000))
	}
	before := len(store.Entries())
	fake.Responses = []*provider.Response{{Text: "history checkpoint", StopReason: "stop", Usage: provider.Usage{Input: 20, Output: 5, TotalTokens: 25}}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	provider := &cancelAfterFirstCompactionSummary{fake: fake, cancel: cancel}
	loop.prov = provider
	err := loop.Compact(ctx, Config{Model: modelMeta(), KeepRecentTokens: 20_000}, "")
	if err == nil || !strings.Contains(err.Error(), "canceled") {
		t.Fatalf("split compaction cancellation=%v", err)
	}
	entries := store.Entries()
	if provider.calls != 2 || len(fake.Calls) != 2 || len(entries) != before+1 || entries[before].Kind != "usage" || entries[before].Usage.Usage.TotalTokens != 25 {
		t.Fatalf("known usage or cancellation outcome lost: calls=%d fake=%d entries=%+v", provider.calls, len(fake.Calls), entries[before:])
	}
	for _, entry := range entries {
		if entry.Kind == "compaction" {
			t.Fatal("cancelled split compaction appended a checkpoint")
		}
	}
}
