package agent

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"psi/internal/event"
	"psi/internal/provider"
	"psi/internal/session"
	"psi/internal/tools"
)

func appendAssistantText(t *testing.T, store *session.Store, text string) string {
	t.Helper()
	id, err := store.AppendMessage(session.AgentMessage{Role: "assistant", Assistant: &session.AssistantMessage{
		Role: "assistant", Content: []session.ContentBlock{{Type: "text", Text: &session.TextContent{Type: "text", Text: text}}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func prepareFromStore(t *testing.T, store *session.Store, keep int) CompactionPreparation {
	t.Helper()
	path, err := store.ActivePath()
	if err != nil {
		t.Fatal(err)
	}
	projection, err := store.Projection()
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := PrepareCompaction(path, projection, keep)
	if err != nil {
		t.Fatal(err)
	}
	return prepared
}

func TestPrepareCompactionReportsNoWorkOutcomes(t *testing.T) {
	store, _, _, _, _ := testSetup(t)
	if got := prepareFromStore(t, store, 20_000).Disposition; got != CompactionEmpty {
		t.Fatalf("empty disposition = %q", got)
	}
	if _, err := store.AppendMessage(userMsg("small request")); err != nil {
		t.Fatal(err)
	}
	appendAssistantText(t, store, "small answer")
	if got := prepareFromStore(t, store, 20_000).Disposition; got != CompactionWithinBudget {
		t.Fatalf("short-session disposition = %q", got)
	}
	path, err := store.ActivePath()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.AppendCompaction("already summarized", path[0].ID(), 100, nil); err != nil {
		t.Fatal(err)
	}
	if got := prepareFromStore(t, store, 20_000).Disposition; got != CompactionAlreadyDone {
		t.Fatalf("already-compacted disposition = %q", got)
	}
}

func TestCompactNoWorkIsTypedAndHasNoSideEffects(t *testing.T) {
	store, _, fake, loop, _ := testSetup(t)
	before := len(store.Entries())
	var phases int
	loop = New(store, tools.NewRegistry(store.Cwd()), fake, func(e event.Event) {
		if e.Type == event.CompactionStart || e.Type == event.CompactionEnd {
			phases++
		}
	})
	err := loop.Compact(t.Context(), Config{Model: modelMeta(), KeepRecentTokens: 20_000}, "")
	var noWork *CompactionNoWorkError
	if !errors.As(err, &noWork) || noWork.Disposition != CompactionEmpty {
		t.Fatalf("empty compact error = %v", err)
	}
	if len(fake.Calls) != 0 || len(store.Entries()) != before || phases != 0 {
		t.Fatalf("no-work compact had effects: calls=%d entries=%d phases=%d", len(fake.Calls), len(store.Entries()), phases)
	}
}

func TestLongSingleUserSpanCompactsAtOrdinaryRetentionBudget(t *testing.T) {
	store, _, fake, loop, _ := testSetup(t)
	if _, err := store.AppendMessage(userMsg("original request")); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 4; i++ {
		appendAssistantText(t, store, strings.Repeat(string(rune('a'+i)), 80_000))
	}
	path, err := store.ActivePath()
	if err != nil {
		t.Fatal(err)
	}
	lastAssistantID := path[len(path)-1].ID()
	fake.Responses = []*provider.Response{{Text: "safe turn checkpoint", StopReason: "stop", Usage: provider.Usage{Input: 20, Output: 5, TotalTokens: 25}}}
	if err := loop.Compact(t.Context(), Config{Model: modelMeta(), KeepRecentTokens: 20_000}, ""); err != nil {
		t.Fatal(err)
	}
	if len(fake.Calls) != 1 {
		t.Fatalf("summary calls = %d, want one turn-prefix call", len(fake.Calls))
	}
	prompt := fake.Calls[0].Messages[0].Content[0].Text
	if !strings.Contains(prompt, "The messages above are earlier context from an ongoing conversation") || !strings.Contains(prompt, "original request") || !strings.Contains(prompt, strings.Repeat("a", 100)) {
		t.Fatalf("turn-prefix summary omitted request/progress: %q", prompt[:min(500, len(prompt))])
	}
	if strings.Contains(prompt, strings.Repeat("d", 100)) {
		t.Fatal("summary included the retained continuation")
	}
	compaction := store.Entries()[len(store.Entries())-1].Compaction
	if compaction == nil || compaction.FirstKeptEntryID != lastAssistantID || !strings.Contains(compaction.Summary, "safe turn checkpoint") || compaction.Usage == nil || compaction.Usage.TotalTokens != 25 {
		t.Fatalf("wrong compaction result: %+v, expected first kept %s", compaction, lastAssistantID)
	}
}

func TestThresholdNoWorkContinuesWithoutCompaction(t *testing.T) {
	store, _, fake, loop, _ := testSetup(t)
	fake.Responses = []*provider.Response{{Text: "answer", StopReason: "stop"}}
	cfg := Config{Model: provider.ModelMeta{ID: "m", API: "anthropic-messages", ContextWindow: 10_000, MaxTokens: 1000}, AutoCompact: true, ReserveTokens: 20_000, KeepRecentTokens: 20_000}
	var compactionEvents int
	loop = New(store, tools.NewRegistry(store.Cwd()), fake, func(e event.Event) {
		if e.Type == event.CompactionStart || e.Type == event.CompactionEnd {
			compactionEvents++
		}
	})
	if err := loop.Submit(t.Context(), "short new request", cfg); err != nil {
		t.Fatal(err)
	}
	if len(fake.Calls) != 1 || compactionEvents != 0 {
		t.Fatalf("threshold no-op blocked request or emitted activity: calls=%d events=%d", len(fake.Calls), compactionEvents)
	}
}

func TestOverflowNoWorkReturnsOriginalContextFailure(t *testing.T) {
	store, _, fake, loop, _ := testSetup(t)
	limit := &provider.ProviderError{Kind: provider.ErrContextLimit, Status: 400, Message: "synthetic context limit"}
	fake.Errors = []error{limit}
	cfg := Config{Model: provider.ModelMeta{ID: "m", API: "anthropic-messages", ContextWindow: 100_000, MaxTokens: 1000}, AutoCompact: true, KeepRecentTokens: 20_000}
	var compactionEvents int
	loop = New(store, tools.NewRegistry(store.Cwd()), fake, func(e event.Event) {
		if e.Type == event.CompactionStart || e.Type == event.CompactionEnd {
			compactionEvents++
		}
	})
	err := loop.Submit(t.Context(), "short new request", cfg)
	if !errors.Is(err, limit) || !strings.Contains(err.Error(), "synthetic context limit") {
		t.Fatalf("overflow no-op replaced original error: %v", err)
	}
	for _, entry := range store.Entries() {
		if entry.Kind == "compaction" {
			t.Fatal("no-op recovery appended compaction")
		}
	}
	if len(fake.Calls) != 1 || compactionEvents != 0 {
		t.Fatalf("overflow no-op retried or emitted activity: calls=%d events=%d", len(fake.Calls), compactionEvents)
	}
}

func TestSplitCompactionSummarizesHistoryAndPrefixWithCombinedUsage(t *testing.T) {
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
	path, err := store.ActivePath()
	if err != nil {
		t.Fatal(err)
	}
	lastID := path[len(path)-1].ID()
	fake.Responses = []*provider.Response{
		{Text: "history checkpoint", StopReason: "stop", Usage: provider.Usage{Input: 20, Output: 5, TotalTokens: 25}},
		{Text: "turn checkpoint", StopReason: "stop", Usage: provider.Usage{Input: 10, Output: 3, TotalTokens: 13}},
	}
	if err := loop.Compact(t.Context(), Config{Model: modelMeta(), KeepRecentTokens: 20_000}, ""); err != nil {
		t.Fatal(err)
	}
	if len(fake.Calls) != 2 {
		t.Fatalf("summary calls=%d, want two", len(fake.Calls))
	}
	if !strings.Contains(fake.Calls[0].Messages[0].Content[0].Text, "older task") || !strings.Contains(fake.Calls[1].Messages[0].Content[0].Text, "Original Request") {
		t.Fatalf("wrong two-stage prompts: %#v", fake.Calls)
	}
	compact := store.Entries()[len(store.Entries())-1].Compaction
	if compact == nil || compact.FirstKeptEntryID != lastID || !strings.Contains(compact.Summary, "history checkpoint") || !strings.Contains(compact.Summary, "turn checkpoint") {
		t.Fatalf("split compaction lost output or continuation: %+v", compact)
	}
	if compact.Usage == nil || compact.Usage.Input != 30 || compact.Usage.Output != 8 || compact.Usage.TotalTokens != 38 {
		t.Fatalf("split summary usage was not combined: %+v", compact.Usage)
	}
}

func TestSplitCompactionCarriesPreviousSummaryIntoHistoryUpdate(t *testing.T) {
	store, _, fake, loop, _ := testSetup(t)
	if _, err := store.AppendMessage(userMsg("ancient discarded request")); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AppendMessage(userMsg("retained boundary request")); err != nil {
		t.Fatal(err)
	}
	path, err := store.ActivePath()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.AppendCompaction("previous checkpoint", path[len(path)-1].ID(), 100, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AppendMessage(userMsg("ongoing current task")); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 4; i++ {
		appendAssistantText(t, store, strings.Repeat(string(rune('a'+i)), 80_000))
	}
	fake.Responses = []*provider.Response{
		{Text: "updated history", StopReason: "stop"},
		{Text: "current-turn prefix", StopReason: "stop"},
	}
	if err := loop.Compact(t.Context(), Config{Model: modelMeta(), KeepRecentTokens: 20_000}, ""); err != nil {
		t.Fatal(err)
	}
	if len(fake.Calls) != 2 {
		t.Fatalf("summary calls=%d, want history update and turn prefix", len(fake.Calls))
	}
	prompt := fake.Calls[0].Messages[0].Content[0].Text
	if !strings.Contains(prompt, "<previous-summary>\nprevious checkpoint\n</previous-summary>") || !strings.Contains(prompt, "retained boundary request") {
		t.Fatalf("history update omitted previous summary or projected retained context: %q", prompt[:min(600, len(prompt))])
	}
	if strings.Contains(prompt, "ancient discarded request") {
		t.Fatal("summary included raw history discarded by the prior compaction")
	}
}

func TestSplitCompactionFailureDoesNotAppendCheckpoint(t *testing.T) {
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
	fake.Errors = []error{nil, fmt.Errorf("second summary failed")}
	err := loop.Compact(t.Context(), Config{Model: modelMeta(), KeepRecentTokens: 20_000}, "")
	if err == nil || !strings.Contains(err.Error(), "second summary failed") {
		t.Fatalf("second call failure=%v", err)
	}
	if len(fake.Calls) != 2 || len(store.Entries()) != before+1 || store.Entries()[before].Kind != "usage" || store.Entries()[before].Usage.Usage.TotalTokens != 25 {
		t.Fatalf("partial compaction usage/entries wrong: calls=%d entries=%d before=%d", len(fake.Calls), len(store.Entries()), before)
	}
	for _, entry := range store.Entries() {
		if entry.Kind == "compaction" {
			t.Fatal("failed split compaction appended a checkpoint")
		}
	}
}

func TestIncompleteSplitSummaryPersistsKnownUsageWithoutCheckpoint(t *testing.T) {
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
	fake.Responses = []*provider.Response{
		{Text: "history checkpoint", StopReason: "stop", Usage: provider.Usage{Input: 20, Output: 5, TotalTokens: 25}},
		{Text: "partial turn checkpoint", StopReason: "length", Usage: provider.Usage{Input: 10, Output: 7, TotalTokens: 17}},
	}
	err := loop.Compact(t.Context(), Config{Model: modelMeta(), KeepRecentTokens: 20_000}, "")
	if err == nil || !strings.Contains(err.Error(), "incomplete") {
		t.Fatalf("incomplete summary error=%v", err)
	}
	entries := store.Entries()
	if len(fake.Calls) != 2 || len(entries) != before+2 || entries[before].Kind != "usage" || entries[before+1].Kind != "usage" {
		t.Fatalf("known summary usage was not retained or checkpoint was written: calls=%d entries=%+v", len(fake.Calls), entries[before:])
	}
	if entries[before].Usage.Usage.TotalTokens != 17 || entries[before+1].Usage.Usage.TotalTokens != 25 {
		t.Fatalf("wrong retained usage records: %+v %+v", entries[before].Usage, entries[before+1].Usage)
	}
	for _, entry := range entries {
		if entry.Kind == "compaction" {
			t.Fatal("incomplete split compaction appended a checkpoint")
		}
	}
}

func TestPrepareCompactionSplitsLongAssistantSpanAtPiCutPoint(t *testing.T) {
	store, _, _, _, _ := testSetup(t)
	if _, err := store.AppendMessage(userMsg("original request")); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 4; i++ {
		appendAssistantText(t, store, strings.Repeat(string(rune('a'+i)), 25_000))
	}
	prepared := prepareFromStore(t, store, 12_000)
	if prepared.Disposition != CompactionEligible || !prepared.SplitTurn {
		t.Fatalf("preparation = %+v", prepared)
	}
	if prepared.FirstKeptEntryID == "" || len(prepared.TurnPrefix) != 3 || len(prepared.Messages) != 0 {
		t.Fatalf("wrong split boundary: firstKept=%q history=%d prefix=%d", prepared.FirstKeptEntryID, len(prepared.Messages), len(prepared.TurnPrefix))
	}
	if prepared.TurnPrefix[0].Role != "user" || prepared.TurnPrefix[1].Role != "assistant" || prepared.TurnPrefix[2].Role != "assistant" || !strings.Contains(prepared.TurnPrefix[1].Assistant.Content[0].Text.Text, "a") {
		t.Fatalf("prefix did not retain request and progress: %+v", prepared.TurnPrefix)
	}
	if prepared.FirstKeptEntryID == "" {
		t.Fatal("split preparation has no durable first-kept entry")
	}
}

func TestPrepareCompactionSummarizesOlderTurnsAndKeepsRecentUserBoundary(t *testing.T) {
	store, _, _, _, _ := testSetup(t)
	for i := 0; i < 4; i++ {
		if _, err := store.AppendMessage(userMsg("request" + strings.Repeat(" old", 1500))); err != nil {
			t.Fatal(err)
		}
		appendAssistantText(t, store, "answer"+strings.Repeat(" done", 1500))
	}
	prepared := prepareFromStore(t, store, 2_000)
	if prepared.Disposition != CompactionEligible || prepared.SplitTurn {
		t.Fatalf("expected a whole-turn cut: %+v", prepared)
	}
	if len(prepared.Messages) == 0 || prepared.FirstKeptEntryID == "" {
		t.Fatalf("older turns were not prepared: %+v", prepared)
	}
	path, _ := store.ActivePath()
	var kept *session.Entry
	for _, entry := range path {
		if entry.ID() == prepared.FirstKeptEntryID {
			kept = entry
			break
		}
	}
	if kept == nil || kept.Kind != "message" || kept.Message.Message.User == nil {
		t.Fatalf("cut did not begin at a user turn: %+v", kept)
	}
}
