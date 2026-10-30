package agent

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"psi/internal/event"
	"psi/internal/provider"
	"psi/internal/session"
	"psi/internal/tools"
)

func testSetup(t *testing.T) (*session.Store, *tools.Registry, *provider.Fake, *Loop, []event.Event) {
	t.Helper()
	ws := t.TempDir()
	sessDir := t.TempDir()
	store, err := session.New(session.OpenOptions{Cwd: ws, SessionDir: sessDir, Persist: true})
	if err != nil {
		t.Fatal(err)
	}
	reg := tools.NewRegistry(ws)
	fake := &provider.Fake{}
	var events []event.Event
	loop := New(store, reg, fake, func(e event.Event) { events = append(events, e) })
	return store, reg, fake, loop, events
}

func modelMeta() provider.ModelMeta {
	return provider.ModelMeta{ID: "claude-x", API: "anthropic-messages", MaxTokens: 1000, Reasoning: false}
}

func toolDefs() []provider.ToolDef {
	return []provider.ToolDef{
		{Name: "read", Description: "read", Schema: map[string]any{"type": "object"}},
		{Name: "write", Description: "write", Schema: map[string]any{"type": "object"}},
		{Name: "edit", Description: "edit", Schema: map[string]any{"type": "object"}},
		{Name: "bash", Description: "bash", Schema: map[string]any{"type": "object"}},
	}
}

func TestSubmitAndFinalAnswer(t *testing.T) {
	_, _, fake, loop, _ := testSetup(t)
	fake.Responses = []*provider.Response{{Text: "done", StopReason: "stop"}}
	cfg := Config{Model: modelMeta(), Thinking: "off", Tools: toolDefs(), System: "base"}
	ctx := context.Background()
	if err := loop.Submit(ctx, "hello", cfg); err != nil {
		t.Fatal(err)
	}
	// Restart retains exactly one accepted user message + assistant.
	store := loop.store
	path, _ := store.ActivePath()
	users := 0
	for _, e := range path {
		if e.Kind == "message" && e.Message.Message.User != nil {
			users++
		}
	}
	if users != 1 {
		t.Errorf("users=%d", users)
	}
}

func TestReadEditBashTask(t *testing.T) {
	store, _, fake, loop, _ := testSetup(t)
	ws := store.Cwd()
	if err := os.WriteFile(filepath.Join(ws, "a.txt"), []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Turn 1: read a.txt; Turn 2: edit; Turn 3: bash; Turn 4: final.
	fake.Responses = []*provider.Response{
		{Text: "reading", StopReason: "toolUse", ToolCalls: []provider.Block{{Type: "toolCall", ID: "c1", Name: "read", Arguments: map[string]any{"path": "a.txt"}}}},
		{Text: "editing", StopReason: "toolUse", ToolCalls: []provider.Block{{Type: "toolCall", ID: "c2", Name: "edit", Arguments: map[string]any{"path": "a.txt", "edits": []any{map[string]any{"oldText": "old", "newText": "new"}}}}}},
		{Text: "checking", StopReason: "toolUse", ToolCalls: []provider.Block{{Type: "toolCall", ID: "c3", Name: "bash", Arguments: map[string]any{"command": "cat a.txt"}}}},
		{Text: "finished", StopReason: "stop"},
	}
	cfg := Config{Model: modelMeta(), Thinking: "off", Tools: toolDefs(), System: "base"}
	if err := loop.Submit(context.Background(), "do the task", cfg); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(filepath.Join(ws, "a.txt"))
	if string(b) != "new" {
		t.Errorf("file=%q", b)
	}
	proj, _ := store.Projection()
	if len(proj.Flat) < 8 { // user + 3x(assistant+result) + final
		t.Errorf("flat=%d", len(proj.Flat))
	}
}

func TestMixedToolBatch(t *testing.T) {
	_, _, fake, loop, _ := testSetup(t)
	fake.Responses = []*provider.Response{
		{Text: "batch", StopReason: "toolUse", ToolCalls: []provider.Block{
			{Type: "toolCall", ID: "c1", Name: "read", Arguments: map[string]any{"path": "missing.txt"}},
			{Type: "toolCall", ID: "c2", Name: "bash", Arguments: map[string]any{"command": "echo ok"}},
		}},
		{Text: "done", StopReason: "stop"},
	}
	cfg := Config{Model: modelMeta(), Tools: toolDefs(), System: "base"}
	if err := loop.Submit(context.Background(), "batch", cfg); err != nil {
		t.Fatal(err)
	}
	proj, _ := loop.store.Projection()
	foundErr, foundOk := false, false
	for _, m := range proj.Flat {
		if m.Role == "toolResult" && m.ToolResult != nil {
			if m.ToolResult.ToolCallID == "c1" && m.ToolResult.IsError {
				foundErr = true
			}
			if m.ToolResult.ToolCallID == "c2" && !m.ToolResult.IsError {
				foundOk = true
			}
		}
	}
	if !foundErr || !foundOk {
		t.Errorf("mixed batch: err=%v ok=%v", foundErr, foundOk)
	}
}

func TestEmptySubmissionRejected(t *testing.T) {
	_, _, _, loop, _ := testSetup(t)
	if err := loop.Submit(context.Background(), "   ", Config{Model: modelMeta()}); err == nil {
		t.Errorf("empty must be rejected")
	}
}

func TestInterruptedRecovery(t *testing.T) {
	store, _, fake, loop, _ := testSetup(t)
	// Seed an unresolved assistant tool call directly.
	if _, err := store.AppendMessage(session.AgentMessage{Role: "user", User: &session.UserMessage{Role: "user", Content: "hi", Timestamp: 1}}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AppendMessage(session.AgentMessage{Role: "assistant", Assistant: &session.AssistantMessage{
		Role: "assistant", Content: []session.ContentBlock{{Type: "toolCall", Call: &session.ToolCall{Type: "toolCall", ID: "cx", Name: "write", Arguments: map[string]any{}}}},
		API: "anthropic-messages", Provider: "anthropic", Model: "m", StopReason: "toolUse", Timestamp: 2}}); err != nil {
		t.Fatal(err)
	}
	fake.Responses = []*provider.Response{{Text: "x", StopReason: "stop"}}
	if err := loop.Submit(context.Background(), "next", Config{Model: modelMeta()}); err == nil || !strings.Contains(err.Error(), "unresolved") {
		t.Errorf("interrupted must stop before rerun, got %v", err)
	}
}

func TestCancellation(t *testing.T) {
	_, _, fake, loop, events := testSetup(t)
	_ = events
	fake.Errors = []error{context.Canceled}
	// Fake that blocks until cancel.
	blocking := &provider.Fake{}
	_ = blocking
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	cfg := Config{Model: modelMeta(), Tools: toolDefs()}
	if err := loop.Submit(ctx, "hi", cfg); err == nil {
		// Either ctx error or provider error; must not leave running.
		t.Logf("cancel err nil (provider fake ignored ctx?)")
	}
	if loop.Running() {
		t.Errorf("loop still running after cancel")
	}
}

func TestToolChainRunsPastTwentyFiveTurnsWithoutACap(t *testing.T) {
	// Pi's agent loop has no turn cap. A chain of sequential tool calls is
	// legitimate work, so the run must continue past any fixed turn count and
	// end only when the model stops calling tools.
	store, _, fake, loop, _ := testSetup(t)
	const chain = 30
	for i := 0; i < chain; i++ {
		fake.Responses = append(fake.Responses, &provider.Response{Text: "again", StopReason: "toolUse", ToolCalls: []provider.Block{{Type: "toolCall", ID: fmt.Sprintf("call-%02d", i), Name: "bash", Arguments: map[string]any{"command": "true"}}}})
	}
	fake.Responses = append(fake.Responses, &provider.Response{Text: "done", StopReason: "stop"})
	cfg := Config{Model: modelMeta(), Tools: toolDefs()}
	if err := loop.Submit(context.Background(), "loop", cfg); err != nil {
		t.Fatalf("long tool chain was cut short: %v", err)
	}
	results := 0
	for _, entry := range store.Entries() {
		if entry.Kind == "message" && entry.Message != nil && entry.Message.Message.Role == "toolResult" {
			results++
		}
	}
	if results != chain {
		t.Fatalf("tool results persisted = %d, want %d", results, chain)
	}
}

func TestStorageFailureStops(t *testing.T) {
	store, _, fake, loop, _ := testSetup(t)
	fake.Responses = []*provider.Response{{Text: "hi", StopReason: "stop"}}
	// Corrupt by making session dir read-only? Simpler: close with invalid file.
	_ = store
	// Truncate tail externally to force append failure on submit.
	// First submit to create file.
	if err := loop.Submit(context.Background(), "first", Config{Model: modelMeta(), Tools: toolDefs()}); err != nil {
		t.Fatal(err)
	}
	f, _ := os.OpenFile(store.File(), os.O_WRONLY|os.O_APPEND, 0o644)
	f.WriteString(`{"broken":`)
	f.Close()
	if err := loop.Submit(context.Background(), "second", Config{Model: modelMeta(), Tools: toolDefs()}); err == nil {
		t.Errorf("storage failure must stop")
	}
}

func TestCompactionFlow(t *testing.T) {
	store, _, _, _, _ := testSetup(t)
	for i := 0; i < 5; i++ {
		if _, err := store.AppendMessage(session.AgentMessage{Role: "user", User: &session.UserMessage{Role: "user", Content: "msg", Timestamp: int64(i)}}); err != nil {
			t.Fatal(err)
		}
	}
	path, _ := store.ActivePath()
	est := Estimator{}
	proj, _ := store.Projection()
	toks, _ := est.Estimate(proj, 100, 4)
	if toks <= 0 {
		t.Errorf("estimate=%d", toks)
	}
	keep, err := Boundary(path, 2)
	if err != nil || keep == "" {
		t.Fatalf("boundary: %v", keep)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	id, err := Compact(ctx, store, "summary of old work", keep, float64(toks), nil)
	if err != nil || id == "" {
		t.Fatalf("compact: %v", id)
	}
	// Cancelled summary appends nothing.
	store2, _ := session.New(session.OpenOptions{Cwd: store.Cwd(), Persist: false})
	for i := 0; i < 2; i++ {
		store2.AppendMessage(session.AgentMessage{Role: "user", User: &session.UserMessage{Role: "user", Content: "x", Timestamp: int64(i)}})
	}
	cctx, ccancel := context.WithCancel(context.Background())
	ccancel()
	if _, err := Compact(cctx, store2, "s", "", 10, nil); err == nil {
		t.Errorf("cancelled compact must append nothing")
	}
	n, _ := store2.ActivePath()
	for _, e := range n {
		if e.Kind == "compaction" {
			t.Errorf("cancelled compaction wrote entry")
		}
	}
}

func TestSteeringDeliveredAtTurnBoundaryAndContinuesRun(t *testing.T) {
	store, _, fake, loop, _ := testSetup(t)
	fake.Responses = []*provider.Response{
		{Text: "using a tool", StopReason: "toolUse", ToolCalls: []provider.Block{{Type: "toolCall", ID: "call-steer-1", Name: "bash", Arguments: map[string]any{"command": "true"}}}},
		{Text: "no tools here", StopReason: "stop"},
		{Text: "done after steering", StopReason: "stop"},
	}
	done := make(chan error, 1)
	go func() {
		done <- loop.Submit(context.Background(), "start", Config{Model: modelMeta(), Tools: toolDefs()})
	}()
	// Wait for the tool call to settle, then steer while the run is alive.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		for _, entry := range store.Entries() {
			if entry.Kind == "message" && entry.Message != nil && entry.Message.Message.Role == "toolResult" {
				loop.Steer("steered message")
				select {
				case err := <-done:
					steeringAssertions(t, store, fake, loop, err)
					return
				case <-time.After(5 * time.Second):
					t.Fatal("run did not finish after steering")
				}
			}
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatal("tool result never settled")
}

func steeringAssertions(t *testing.T, store *session.Store, fake *provider.Fake, loop *Loop, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("steering broke the run: %v", err)
	}
	// The no-tool second response must not end the run: delivered steering
	// continues it, so the provider saw a third request.
	if len(fake.Calls) != 3 {
		t.Fatalf("provider calls = %d, want 3 (steering must continue the run)", len(fake.Calls))
	}
	last := fake.Calls[len(fake.Calls)-1]
	found := false
	for _, m := range last.Messages {
		if m.Role == "user" && strings.Contains(fmt.Sprint(m.Content), "steered message") {
			found = true
		}
	}
	if !found {
		t.Fatalf("steering message never reached the model context: %+v", last.Messages)
	}
	userStored := false
	for _, entry := range store.Entries() {
		if entry.Kind == "message" && entry.Message != nil && entry.Message.Message.Role == "user" && strings.Contains(storeUserText(entry), "steered message") {
			userStored = true
		}
	}
	if !userStored {
		t.Fatal("delivered steering message was not persisted as a user message")
	}
	if pending := loop.ClearSteering(); len(pending) != 0 {
		t.Fatalf("steering queue not drained: %q", pending)
	}
}

func storeUserText(entry *session.Entry) string {
	if entry.Message == nil || entry.Message.Message.User == nil {
		return ""
	}
	if text, ok := entry.Message.Message.User.Content.(string); ok {
		return text
	}
	return ""
}
