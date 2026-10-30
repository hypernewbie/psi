package agent

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"psi/internal/event"
	"psi/internal/provider"
	"psi/internal/session"
	"psi/internal/tools"
)

func TestUnknownContentRefusedBeforeUserAppend(t *testing.T) {
	store, _, fake, loop, _ := testSetup(t)
	var message session.AgentMessage
	if err := json.Unmarshal([]byte(`{"role":"futureContextRole","content":"must-not-drop"}`), &message); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AppendMessage(message); err != nil {
		t.Fatal(err)
	}
	before := len(store.Entries())
	if err := loop.Submit(context.Background(), "next", Config{Model: modelMeta()}); err == nil {
		t.Fatal("unknown role was silently filtered")
	}
	if len(fake.Calls) != 0 || len(store.Entries()) != before {
		t.Fatal("unsafe session mutated or sent to provider")
	}
}

func TestDisabledToolCannotRun(t *testing.T) {
	store, _, fake, loop, _ := testSetup(t)
	fake.Responses = []*provider.Response{
		{ToolCalls: []provider.Block{{Type: "toolCall", ID: "c", Name: "write", Arguments: map[string]any{"path": "bad.txt", "content": "no"}}}, StopReason: "toolUse"},
		{Text: "done", StopReason: "stop"},
	}
	if err := loop.Submit(context.Background(), "run", Config{Model: modelMeta(), Tools: tools.Definitions([]string{"read"})}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(store.Cwd(), "bad.txt")); !os.IsNotExist(err) {
		t.Fatal("model executed a disabled tool")
	}
}

func TestThinkingAndImportedImagesSurviveRequests(t *testing.T) {
	store, _, fake, loop, _ := testSetup(t)
	var message session.AgentMessage
	if err := json.Unmarshal([]byte(`{"role":"user","content":[{"type":"text","text":"image"},{"type":"image","data":"aGVsbG8=","mimeType":"image/png"}],"timestamp":1}`), &message); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AppendMessage(message); err != nil {
		t.Fatal(err)
	}
	fake.Responses = []*provider.Response{{Content: []provider.Block{{Type: "thinking", Thinking: "thought", ThinkingSignature: "opaque-sig"}, {Type: "text", Text: "answer"}}, Text: "answer", StopReason: "stop", ResponseID: "r1"}, {Text: "next", StopReason: "stop"}}
	model := modelMeta()
	model.InputImage = true
	cfg := Config{Model: model, System: "base"}
	if err := loop.Submit(context.Background(), "continue", cfg); err != nil {
		t.Fatal(err)
	}
	if len(fake.Calls[0].Messages[0].Content) != 2 || fake.Calls[0].Messages[0].Content[1].Type != "image" {
		t.Fatal("imported image silently dropped")
	}
	if err := loop.Submit(context.Background(), "again", cfg); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, m := range fake.Calls[1].Messages {
		for _, b := range m.Content {
			if b.Type == "thinking" {
				found = b.ThinkingSignature == "opaque-sig"
			}
		}
	}
	if !found {
		t.Fatal("newly generated thinking signature was lost before replay")
	}
}

func TestManualCompactionEmitsStartAndEndReason(t *testing.T) {
	store, reg, fake, _, _ := testSetup(t)
	if _, err := store.AppendMessage(userMsg("older request")); err != nil {
		t.Fatal(err)
	}
	fake.Responses = []*provider.Response{{Text: "summary", StopReason: "stop"}}
	var phases []event.Event
	loop := New(store, reg, fake, func(e event.Event) {
		if e.Type == event.CompactionStart || e.Type == event.CompactionEnd {
			phases = append(phases, e)
		}
	})
	if err := loop.Compact(context.Background(), Config{Model: modelMeta(), KeepRecentTokens: 0}, ""); err != nil {
		t.Fatal(err)
	}
	if len(phases) != 2 || phases[0].Type != event.CompactionStart || phases[0].State != event.CompactionManual || phases[1].Type != event.CompactionEnd || phases[1].State != event.CompactionManual {
		t.Fatalf("manual phase sequence: %+v", phases)
	}
}

func TestAutomaticCompactionNoWorkDoesNotRetryProvider(t *testing.T) {
	store, reg, fake, _, _ := testSetup(t)
	fake.Responses = []*provider.Response{{Text: "answer", StopReason: "stop"}}
	var events []event.Event
	loop := New(store, reg, fake, func(e event.Event) { events = append(events, e) })
	cfg := Config{
		Model:            provider.ModelMeta{ID: "m", API: "anthropic-messages", ContextWindow: 1000, MaxTokens: 100},
		AutoCompact:      true,
		ReserveTokens:    2000,
		KeepRecentTokens: 20000,
	}
	if err := loop.Submit(context.Background(), "short request", cfg); err != nil {
		t.Fatal(err)
	}
	if len(fake.Calls) != 1 {
		t.Fatalf("provider calls after automatic no-op = %d, want 1", len(fake.Calls))
	}
	for _, e := range events {
		if e.Type == event.CompactionStart || e.Type == event.CompactionEnd {
			t.Fatalf("no-work compaction emitted activity event: %+v", e)
		}
	}
	path, err := store.ActivePath()
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range path {
		if entry.Kind == "compaction" {
			t.Fatal("automatic no-op wrote a compaction checkpoint")
		}
	}
}

func TestAutomaticCompactionEmitsThresholdReason(t *testing.T) {
	store, reg, fake, _, _ := testSetup(t)
	old := strings.Repeat("older history ", 1000)
	if _, err := store.AppendMessage(userMsg(old)); err != nil {
		t.Fatal(err)
	}
	fake.Responses = []*provider.Response{{Text: "summary", StopReason: "stop"}, {Text: "answer", StopReason: "stop"}}
	cfg := Config{Model: provider.ModelMeta{ID: "m", API: "anthropic-messages", ContextWindow: 10000, MaxTokens: 1000}, AutoCompact: true, ReserveTokens: 8000, KeepRecentTokens: 1}
	var phases []event.Event
	loop := New(store, reg, fake, func(e event.Event) {
		if e.Type == event.CompactionStart || e.Type == event.CompactionEnd {
			phases = append(phases, e)
		}
	})
	if err := loop.Submit(context.Background(), "current task", cfg); err != nil {
		t.Fatal(err)
	}
	if len(phases) != 2 || phases[0].Type != event.CompactionStart || phases[0].State != event.CompactionThreshold || phases[1].Type != event.CompactionEnd || phases[1].State != event.CompactionThreshold {
		t.Fatalf("threshold phase sequence: %+v", phases)
	}
}

func TestZeroRecentBudgetWritesRetainNoneCompaction(t *testing.T) {
	store, _, fake, loop, _ := testSetup(t)
	if _, err := store.AppendMessage(userMsg("old request")); err != nil {
		t.Fatal(err)
	}
	fake.Responses = []*provider.Response{{Text: "summary", StopReason: "stop"}}
	if err := loop.Compact(context.Background(), Config{Model: modelMeta(), KeepRecentTokens: 0}, ""); err != nil {
		t.Fatal(err)
	}
	entries := store.Entries()
	last := entries[len(entries)-1]
	if last.Compaction == nil || last.Compaction.FirstKeptEntryID != last.ID() {
		t.Fatal("zero budget did not honor retain-none semantics")
	}
}

func TestRealCompactionIsTransactionalAndUsesFullCheckpoint(t *testing.T) {
	store, _, fake, loop, _ := testSetup(t)
	initial, later := "initial", "latest instructions"
	store.AppendMessage(session.AgentMessage{Role: "system", System: &session.SystemMessage{Role: "system", Content: "", Sections: map[string]*string{"instructions": &initial}, Timestamp: 1}})
	store.AppendMessage(userMsg(strings.Repeat("old history ", 50)))
	store.AppendMessage(session.AgentMessage{Role: "system", System: &session.SystemMessage{Role: "system", Content: "", Sections: map[string]*string{"instructions": &later}, Timestamp: 2}})
	store.AppendMessage(userMsg(strings.Repeat("recent ", 50)))
	before := len(store.Entries())
	cfg := Config{Model: modelMeta(), KeepRecentTokens: 20}
	fake.Errors = []error{errors.New("summary unavailable")}
	if err := loop.Compact(context.Background(), cfg, "focus"); err == nil {
		t.Fatal("failed summary succeeded")
	}
	if len(store.Entries()) != before {
		t.Fatal("failed summary destroyed context")
	}
	fake.Errors = nil
	fake.Responses = []*provider.Response{nil, {Text: "Actual summary of the older work", StopReason: "stop", Usage: provider.Usage{Input: 9, Output: 3, TotalTokens: 12}}}
	if err := loop.Compact(context.Background(), cfg, "focus"); err != nil {
		t.Fatal(err)
	}
	path, err := store.ActivePath()
	if err != nil {
		t.Fatal(err)
	}
	compaction := path[len(path)-1].Compaction
	if compaction == nil || compaction.Summary != "Actual summary of the older work" || compaction.Usage == nil || compaction.Usage.TotalTokens != 12 {
		t.Fatal("compaction used placeholder text or discarded summary usage")
	}
	checkpoint := session.ReplaySystem([]session.AgentMessage{{Role: "system", System: compaction.SystemMessage}})
	if checkpoint.Content != later {
		t.Fatalf("compaction captured stale prompt: %q", checkpoint.Content)
	}
	if len(fake.Calls[1].Tools) > 0 || !strings.Contains(fake.Calls[1].Messages[0].Content[0].Text, "old history") {
		t.Fatal("summary did not use actual older context")
	}
}
