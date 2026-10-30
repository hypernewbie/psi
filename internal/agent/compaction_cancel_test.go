package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"psi/internal/provider"
	"psi/internal/session"
	"psi/internal/testutil/leakcheck"
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

func TestCompactionCancellationLeavesSessionConsistent(t *testing.T) {
	defer leakcheck.Verify(t)
	store, _, fake, loop, _ := testSetup(t)

	// Seed session with history that qualifies for compaction
	if _, err := store.AppendMessage(userMsg("initial user goal")); err != nil {
		t.Fatal(err)
	}
	appendAssistantText(t, store, "step 1 completed")
	if _, err := store.AppendMessage(userMsg("continue next step")); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		appendAssistantText(t, store, strings.Repeat(fmt.Sprintf("progress %d ", i), 2000))
	}

	beforeProj, err := store.Projection()
	if err != nil {
		t.Fatal(err)
	}
	if err := session.CheckContinuable(beforeProj.Flat, beforeProj.Entries); err != nil {
		t.Fatalf("session before compaction not continuable: %v", err)
	}

	// Provider that cancels context when summary request arrives
	ctx, cancel := context.WithCancel(context.Background())
	cancelProvider := &cancelAfterFirstCompactionSummary{
		fake:   fake,
		cancel: cancel,
	}
	fake.Responses = []*provider.Response{
		{Text: "summary history", StopReason: "stop"},
		{Text: "summary split", StopReason: "stop"},
		{Text: "new assistant response", StopReason: "stop"},
	}
	loop.prov = cancelProvider

	cfg := Config{Model: modelMeta(), KeepRecentTokens: 1000}
	err = loop.Compact(ctx, cfg, "")
	if err == nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}

	// Invariant: Session remains 100% consistent: either compacted or original, never corrupted or half-compacted
	afterProj, err := store.Projection()
	if err != nil {
		t.Fatalf("projection after cancelled compaction failed: %v", err)
	}
	if err := session.CheckContinuable(afterProj.Flat, afterProj.Entries); err != nil {
		t.Fatalf("session after cancelled compaction corrupted: %v", err)
	}

	// Verify no incomplete or corrupted compaction entry was appended
	for _, entry := range store.Entries() {
		if entry.Kind == "compaction" {
			t.Fatal("compaction entry exists after cancelled compaction")
		}
	}

	// Verify session can continue normally with a new prompt
	loop.prov = fake
	if err := loop.Submit(context.Background(), "resumed instruction", cfg); err != nil {
		t.Fatalf("subsequent prompt failed after cancelled compaction: %v", err)
	}
	finalProj, err := store.Projection()
	if err != nil {
		t.Fatal(err)
	}
	if err := session.CheckContinuable(finalProj.Flat, finalProj.Entries); err != nil {
		t.Fatalf("session not continuable after resume: %v", err)
	}
}

func TestCompactionCancellationDuringCommitConsistency(t *testing.T) {
	defer leakcheck.Verify(t)
	store, _, _, _, _ := testSetup(t)

	if _, err := store.AppendMessage(userMsg("task")); err != nil {
		t.Fatal(err)
	}
	appendAssistantText(t, store, "result")
	path, err := store.ActivePath()
	if err != nil {
		t.Fatal(err)
	}
	keepID := path[len(path)-1].ID()

	// Cancel context BEFORE calling Compact
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	id, err := Compact(ctx, store, "summary", keepID, 100, nil)
	if err == nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got id=%s err=%v", id, err)
	}

	// Invariant: no compaction entry was added
	for _, entry := range store.Entries() {
		if entry.Kind == "compaction" {
			t.Fatal("cancelled Compact appended a compaction entry")
		}
	}
}

func TestCompactionWithSteeringArrivingConcurrently(t *testing.T) {
	defer leakcheck.Verify(t)
	store, _, fake, loop, _ := testSetup(t)

	if _, err := store.AppendMessage(userMsg("first instruction")); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 4; i++ {
		appendAssistantText(t, store, strings.Repeat(fmt.Sprintf("large step %d ", i), 3000))
	}

	enteredSummary := make(chan struct{})
	releaseSummary := make(chan struct{})

	blocking := &blockingProvider{
		Provider: fake,
		entered:  enteredSummary,
		release:  releaseSummary,
	}
	loop.prov = blocking

	fake.Responses = []*provider.Response{
		{Text: "compaction summary", StopReason: "stop"},
		{Text: "final reply after steer", StopReason: "stop"},
	}

	cfg := Config{Model: modelMeta(), KeepRecentTokens: 1000}

	done := make(chan error, 1)
	go func() {
		done <- loop.Compact(context.Background(), cfg, "")
	}()

	<-enteredSummary

	// Steer while compaction is in progress
	loop.Steer("steered during compaction")

	// Release summary
	close(releaseSummary)

	if err := <-done; err != nil {
		t.Fatalf("compaction failed: %v", err)
	}

	// Invariant: steering was queued behind compaction and is still pending for the next turn
	pending := loop.PendingSteering()
	if len(pending) != 1 || pending[0] != "steered during compaction" {
		t.Fatalf("pending steering = %v, want ['steered during compaction']", pending)
	}

	// Verify session tree has valid compaction entry and is continuable
	proj, err := store.Projection()
	if err != nil {
		t.Fatal(err)
	}
	if err := session.CheckContinuable(proj.Flat, proj.Entries); err != nil {
		t.Fatalf("session invalid after compaction: %v", err)
	}

	// Running a subsequent turn delivers steering cleanly
	loop.prov = fake
	if err := loop.Submit(context.Background(), "proceed", cfg); err != nil {
		t.Fatalf("subsequent submit failed: %v", err)
	}
	if p := loop.PendingSteering(); len(p) != 0 {
		t.Fatalf("steering not drained after submit: %v", p)
	}
}
