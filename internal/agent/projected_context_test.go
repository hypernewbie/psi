package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/hypernewbie/psi/internal/ac"
	"github.com/hypernewbie/psi/internal/event"
	"github.com/hypernewbie/psi/internal/provider"
	"github.com/hypernewbie/psi/internal/session"
)

// TestProjectedContextTokensAnchorsOnUsage pins Pi's projected accounting:
// newest provider usage plus trailing model-visible messages, with Pi's
// invalidation (usage before compaction or edit is untrusted) and fallback to
// a text estimate when the anchor is missing from the projection.
func TestProjectedContextTokensAnchorsOnUsage(t *testing.T) {
	store, _, _, _, _ := testSetup(t)
	if _, err := store.AppendMessage(userMsg("first")); err != nil {
		t.Fatal(err)
	}
	assistantID, err := store.AppendMessage(session.AgentMessage{Role: "assistant", Assistant: &session.AssistantMessage{
		Role: "assistant", StopReason: "stop", Usage: session.Usage{TotalTokens: 16600},
		Content: []session.ContentBlock{{Type: "text", Text: &session.TextContent{Type: "text", Text: "answer"}}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	proj, err := store.Projection()
	if err != nil {
		t.Fatal(err)
	}
	cfg := Config{System: strings.Repeat("s", 3000)}
	if got := ProjectedContextTokens(proj, cfg, 16600, assistantID, true); got != 16600 {
		t.Fatalf("at anchor = %d; want 16600", got)
	}
	// Nil projection keeps the provider value for the metadata-only footer path.
	if got := ProjectedContextTokens(nil, cfg, 16600, assistantID, true); got != 16600 {
		t.Fatalf("nil projection = %d; want 16600", got)
	}
	trailingText := strings.Repeat("u", 400)
	if _, err := store.AppendMessage(session.AgentMessage{Role: "user", User: &session.UserMessage{Role: "user", Content: trailingText}}); err != nil {
		t.Fatal(err)
	}
	proj, err = store.Projection()
	if err != nil {
		t.Fatal(err)
	}
	wantTrailing := 16600 + len(trailingText)/4 + 10
	if got := ProjectedContextTokens(proj, cfg, 16600, assistantID, true); got != wantTrailing {
		t.Fatalf("with trailing = %d; want %d", got, wantTrailing)
	}
	// Stale anchor (not in projection) falls back to a text estimate instead of
	// trusting unmatched usage as current.
	if got, want := ProjectedContextTokens(proj, cfg, 60000, "missing-id", true), EstimatedContext(proj, cfg); got != want {
		t.Fatalf("stale anchor = %d; want estimate %d", got, want)
	}
	if got, want := ProjectedContextTokens(proj, cfg, 0, "", false), EstimatedContext(proj, cfg); got != want {
		t.Fatalf("without usage = %d; want estimate %d", got, want)
	}
}

func usageDrivenSetup(t *testing.T, usage int) (*session.Store, *Loop, Config, *[]event.Event, *[]string, *[]provider.Request) {
	t.Helper()
	store, _, _, loop, _ := testSetup(t)
	if _, err := store.AppendMessage(userMsg("old task")); err != nil {
		t.Fatal(err)
	}
	appendAssistantText(t, store, strings.Repeat("older context ", 80))
	if _, err := store.AppendMessage(userMsg("another task")); err != nil {
		t.Fatal(err)
	}
	appendAssistantText(t, store, strings.Repeat("another answer ", 80))
	var events []event.Event
	loop.sink = func(e event.Event) { events = append(events, e) }
	var sequence []string
	var requests []provider.Request
	fake := reviewProvider(func(_ context.Context, req provider.Request, _ func(provider.Delta)) (*provider.Response, error) {
		requests = append(requests, req)
		if strings.Contains(req.System, "context summarization assistant") {
			sequence = append(sequence, "summary")
			return &provider.Response{Text: "checkpoint", StopReason: "stop"}, nil
		}
		if len(sequence) == 0 {
			sequence = append(sequence, "tool turn")
			return &provider.Response{Text: "running a tool", StopReason: "toolUse",
				Usage:     provider.Usage{Input: int64(usage), Output: 20, TotalTokens: int64(usage + 20)},
				ToolCalls: []provider.Block{{Type: "toolCall", ID: "probe", Name: "bash", Arguments: map[string]any{"command": "true"}}},
			}, nil
		}
		sequence = append(sequence, "next provider turn")
		return &provider.Response{Text: "done", StopReason: "stop"}, nil
	})
	loop.prov = fake
	policy := ac.NewController()
	cfg := Config{
		Model:    provider.ModelMeta{ID: "small", API: "anthropic-messages", ContextWindow: 100_000, MaxTokens: 2_000},
		Thinking: "off", Tools: toolDefs(),
		AutoCompact: true, ReserveTokens: 16_384, KeepRecentTokens: 20, ACPolicy: policy,
	}
	return store, loop, cfg, &events, &sequence, &requests
}

// TestUsageDrivenAdaptiveCompaction covers the 60K tool-turn probe: usage sits
// below the native reserve threshold but above pi-ac's predictive target, so
// the run must summarize for adaptive reasons before the next request. A split
// turn can need two summary calls; one checkpoint stays the invariant.
func TestUsageDrivenAdaptiveCompaction(t *testing.T) {
	store, loop, cfg, events, sequence, requests := usageDrivenSetup(t, 60_000)
	if err := loop.Submit(t.Context(), "continue the task", cfg); err != nil {
		t.Fatal(err)
	}
	if len(*sequence) < 3 || (*sequence)[0] != "tool turn" || (*sequence)[len(*sequence)-1] != "next provider turn" {
		t.Fatalf("sequence=%v; want tool turn, summaries, next turn", *sequence)
	}
	for _, step := range (*sequence)[1 : len(*sequence)-1] {
		if step != "summary" {
			t.Fatalf("sequence=%v; middle steps must be summaries", *sequence)
		}
	}
	checkpoints := 0
	for _, entry := range store.Entries() {
		if entry.Compaction != nil {
			checkpoints++
		}
	}
	if checkpoints != 1 {
		t.Fatalf("checkpoints=%d; want 1", checkpoints)
	}
	foundAdaptive := false
	for _, e := range *events {
		if e.Type == event.CompactionStart && e.State == event.CompactionAdaptive {
			foundAdaptive = true
		}
		if e.Type == event.CompactionStart && e.State == event.CompactionThreshold {
			t.Fatalf("60K below native threshold compacted as threshold: %+v", e)
		}
	}
	if !foundAdaptive {
		t.Fatalf("no adaptive compaction start: %+v", *events)
	}
	assertCompactedToolRequest(t, (*requests)[len(*requests)-1])
}

// TestUsageDrivenNativeThresholdCompaction covers the 95K tool-turn probe: usage
// crosses the native reserve threshold, so pi-ac leaves it to psi's native
// path and the run must summarize for threshold reasons.
func TestUsageDrivenNativeThresholdCompaction(t *testing.T) {
	store, loop, cfg, events, sequence, requests := usageDrivenSetup(t, 95_000)
	if err := loop.Submit(t.Context(), "continue the task", cfg); err != nil {
		t.Fatal(err)
	}
	if len(*sequence) < 3 || (*sequence)[0] != "tool turn" || (*sequence)[len(*sequence)-1] != "next provider turn" {
		t.Fatalf("sequence=%v; want tool turn, summaries, next turn", *sequence)
	}
	for _, step := range (*sequence)[1 : len(*sequence)-1] {
		if step != "summary" {
			t.Fatalf("sequence=%v; middle steps must be summaries", *sequence)
		}
	}
	checkpoints := 0
	for _, entry := range store.Entries() {
		if entry.Compaction != nil {
			checkpoints++
		}
	}
	if checkpoints != 1 {
		t.Fatalf("checkpoints=%d; want 1", checkpoints)
	}
	foundThreshold := false
	for _, e := range *events {
		if e.Type == event.CompactionStart && e.State == event.CompactionThreshold {
			foundThreshold = true
		}
	}
	if !foundThreshold {
		t.Fatalf("no threshold compaction start: %+v", *events)
	}
	assertCompactedToolRequest(t, (*requests)[len(*requests)-1])
}

func assertCompactedToolRequest(t *testing.T, req provider.Request) {
	t.Helper()
	encoded, err := json.Marshal(req.Messages)
	if err != nil {
		t.Fatal(err)
	}
	text := string(encoded)
	for _, expected := range []string{"checkpoint", "probe", "toolCall", "toolResult"} {
		if !strings.Contains(text, expected) {
			t.Fatalf("post-compaction request missing %q: %s", expected, text)
		}
	}
}

// TestCleanFinalReplyOverNativeThresholdCompacts pins Pi's post-run threshold
// compaction: a clean final answer that already exceeds the native reserve
// compacts once without asking for another reply.
func TestCleanFinalReplyOverNativeThresholdCompacts(t *testing.T) {
	store, reg, _, _, _ := testSetup(t)
	if _, err := store.AppendMessage(userMsg("old task")); err != nil {
		t.Fatal(err)
	}
	appendAssistantText(t, store, strings.Repeat("older context ", 80))
	var events []event.Event
	calls := 0
	fake := reviewProvider(func(_ context.Context, req provider.Request, _ func(provider.Delta)) (*provider.Response, error) {
		calls++
		if strings.Contains(req.System, "context summarization assistant") {
			return &provider.Response{Text: "checkpoint", StopReason: "stop"}, nil
		}
		return &provider.Response{Text: "final", StopReason: "stop",
			Usage: provider.Usage{Input: 95_000, Output: 20, TotalTokens: 95_020}}, nil
	})
	loop := New(store, reg, fake, func(e event.Event) { events = append(events, e) })
	cfg := Config{
		Model:    provider.ModelMeta{ID: "small", API: "anthropic-messages", ContextWindow: 100_000, MaxTokens: 2_000},
		Thinking: "off", AutoCompact: true, ReserveTokens: 16_384, KeepRecentTokens: 20,
	}
	if err := loop.Submit(t.Context(), "finish", cfg); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatalf("calls=%d; want final plus one summary", calls)
	}
	checkpoints := 0
	for _, entry := range store.Entries() {
		if entry.Compaction != nil {
			checkpoints++
		}
	}
	if checkpoints != 1 {
		t.Fatalf("checkpoints=%d; want 1", checkpoints)
	}
	foundThreshold := false
	for _, e := range events {
		if e.Type == event.CompactionStart && e.State == event.CompactionThreshold {
			foundThreshold = true
		}
	}
	if !foundThreshold {
		t.Fatalf("no threshold compaction after final reply: %+v", events)
	}
}
