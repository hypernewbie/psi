package agent

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"psi/internal/event"
	"psi/internal/provider"
	"psi/internal/session"
	"psi/internal/testutil/leakcheck"
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

type blockingProvider struct {
	provider.Provider
	entered chan struct{}
	release chan struct{}
}

func (b *blockingProvider) Send(ctx context.Context, req provider.Request, onText func(string)) (*provider.Response, error) {
	select {
	case b.entered <- struct{}{}:
	default:
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-b.release:
	}
	return b.Provider.Send(ctx, req, onText)
}

func (b *blockingProvider) CloseIdle() {
	b.Provider.CloseIdle()
}

func TestConcurrentSubmit_ExactlyOneSucceeds(t *testing.T) {
	defer leakcheck.Verify(t)
	store, reg, fake, _, _ := testSetup(t)
	fake.Responses = []*provider.Response{{Text: "done", StopReason: "stop"}}

	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	bp := &blockingProvider{
		Provider: fake,
		entered:  entered,
		release:  release,
	}
	loop := New(store, reg, bp, func(event.Event) {})
	cfg := Config{Model: modelMeta(), Thinking: "off", Tools: toolDefs(), System: "base"}

	const n = 20
	start := make(chan struct{})
	var wg sync.WaitGroup
	errs := make([]error, n)

	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			<-start
			errs[idx] = loop.Submit(context.Background(), fmt.Sprintf("msg-%d", idx), cfg)
		}(i)
	}

	close(start)
	<-entered

	// While the winner is running inside Send, any new Submit must fail with "a run is already active"
	if err := loop.Submit(context.Background(), "mid-flight", cfg); err == nil || !strings.Contains(err.Error(), "a run is already active") {
		t.Fatalf("expected already active error, got %v", err)
	}

	close(release)
	wg.Wait()

	var successes, rejections int
	for _, err := range errs {
		if err == nil {
			successes++
		} else if strings.Contains(err.Error(), "a run is already active") {
			rejections++
		} else {
			t.Errorf("unexpected error: %v", err)
		}
	}
	if successes != 1 {
		t.Fatalf("expected exactly 1 success, got %d", successes)
	}
	if rejections != n-1 {
		t.Fatalf("expected %d rejections, got %d", n-1, rejections)
	}
	if loop.Running() {
		t.Fatal("loop still running after completion")
	}

	proj, err := store.Projection()
	if err != nil {
		t.Fatal(err)
	}
	var users int
	for _, m := range proj.Flat {
		if m.Role == "user" {
			users++
		}
	}
	if users != 1 {
		t.Fatalf("expected 1 user message, got %d", users)
	}
}

func TestSubmitAllowedAfterPreviousRunCompletes(t *testing.T) {
	defer leakcheck.Verify(t)
	store, _, fake, loop, _ := testSetup(t)
	fake.Responses = []*provider.Response{
		{Text: "first reply", StopReason: "stop"},
		{Text: "second reply", StopReason: "stop"},
	}
	cfg := Config{Model: modelMeta(), Thinking: "off", Tools: toolDefs(), System: "base"}

	if err := loop.Submit(context.Background(), "first", cfg); err != nil {
		t.Fatalf("first run failed: %v", err)
	}
	if loop.Running() {
		t.Fatal("loop still running after first run")
	}

	if err := loop.Submit(context.Background(), "second", cfg); err != nil {
		t.Fatalf("second run failed: %v", err)
	}
	if loop.Running() {
		t.Fatal("loop still running after second run")
	}

	proj, err := store.Projection()
	if err != nil {
		t.Fatal(err)
	}
	var userTexts []string
	for _, m := range proj.Flat {
		if m.Role == "user" {
			userTexts = append(userTexts, m.User.Content.(string))
		}
	}
	if len(userTexts) != 2 || userTexts[0] != "first" || userTexts[1] != "second" {
		t.Fatalf("user messages = %v, want [first, second]", userTexts)
	}
}

func TestSteerWhileStreamingAndBatchRunning(t *testing.T) {
	defer leakcheck.Verify(t)
	store, reg, fake, loop, _ := testSetup(t)
	ws := store.Cwd()
	if err := os.WriteFile(filepath.Join(ws, "work.txt"), []byte("data"), 0o644); err != nil {
		t.Fatal(err)
	}

	enteredTool := make(chan struct{})
	releaseTool := make(chan struct{})
	var toolOnce sync.Once
	reg.Read.AfterOpen = func(string) {
		toolOnce.Do(func() { close(enteredTool) })
		<-releaseTool
	}

	fake.Responses = []*provider.Response{
		{
			Text:       "calling tool",
			StopReason: "toolUse",
			ToolCalls: []provider.Block{
				{Type: "toolCall", ID: "t1", Name: "read", Arguments: map[string]any{"path": "work.txt"}},
			},
		},
		{Text: "processed steering", StopReason: "stop"},
	}

	cfg := Config{Model: modelMeta(), Thinking: "off", Tools: toolDefs(), System: "base"}

	done := make(chan error, 1)
	go func() {
		done <- loop.Submit(context.Background(), "initial prompt", cfg)
	}()

	<-enteredTool

	// While tool batch is running, multiple goroutines call Steer
	const n = 5
	var steerWg sync.WaitGroup
	for i := 0; i < n; i++ {
		steerWg.Add(1)
		go func(idx int) {
			defer steerWg.Done()
			loop.Steer(fmt.Sprintf("steering-%d", idx))
		}(i)
	}
	steerWg.Wait()

	close(releaseTool)

	if err := <-done; err != nil {
		t.Fatalf("run failed: %v", err)
	}

	// Provider received 2 calls: first with initial prompt, second with tool result and all 5 steering messages
	if len(fake.Calls) != 2 {
		t.Fatalf("expected 2 provider calls, got %d", len(fake.Calls))
	}

	secondReq := fake.Calls[1]
	steeredCount := 0
	for _, m := range secondReq.Messages {
		if m.Role == "user" {
			for i := 0; i < n; i++ {
				if strings.Contains(fmt.Sprint(m.Content), fmt.Sprintf("steering-%d", i)) {
					steeredCount++
				}
			}
		}
	}
	if steeredCount != n {
		t.Fatalf("expected %d steered messages in provider request, found %d", n, steeredCount)
	}

	proj, err := store.Projection()
	if err != nil {
		t.Fatal(err)
	}
	storedSteered := 0
	for _, m := range proj.Flat {
		if m.Role == "user" && strings.HasPrefix(fmt.Sprint(m.User.Content), "steering-") {
			storedSteered++
		}
	}
	if storedSteered != n {
		t.Fatalf("expected %d steered messages stored, found %d", n, storedSteered)
	}
	if pending := loop.PendingSteering(); len(pending) != 0 {
		t.Fatalf("pending steering not empty: %v", pending)
	}
}

func TestClearSteeringAndPendingSteeringConcurrency(t *testing.T) {
	defer leakcheck.Verify(t)
	_, _, _, loop, _ := testSetup(t)

	const goroutines = 8
	const itemsPerGoroutine = 50
	var wg sync.WaitGroup
	var clearedMu sync.Mutex
	cleared := make(map[string]bool)

	// Producers
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func(gid int) {
			defer wg.Done()
			for i := 0; i < itemsPerGoroutine; i++ {
				loop.Steer(fmt.Sprintf("msg-%d-%d", gid, i))
			}
		}(g)
	}

	// Consumers calling PendingSteering and ClearSteering concurrently
	stopConsumers := make(chan struct{})
	var consumerWg sync.WaitGroup
	for c := 0; c < 4; c++ {
		consumerWg.Add(1)
		go func() {
			defer consumerWg.Done()
			for {
				select {
				case <-stopConsumers:
					return
				default:
					_ = loop.PendingSteering()
					msgs := loop.ClearSteering()
					if len(msgs) > 0 {
						clearedMu.Lock()
						for _, m := range msgs {
							if cleared[m] {
								t.Errorf("duplicate message cleared: %s", m)
							}
							cleared[m] = true
						}
						clearedMu.Unlock()
					}
				}
			}
		}()
	}

	wg.Wait()
	time.Sleep(10 * time.Millisecond)
	close(stopConsumers)
	consumerWg.Wait()

	// Drain remaining
	for _, m := range loop.ClearSteering() {
		clearedMu.Lock()
		if cleared[m] {
			t.Errorf("duplicate message drained: %s", m)
		}
		cleared[m] = true
		clearedMu.Unlock()
	}

	expectedTotal := goroutines * itemsPerGoroutine
	if len(cleared) != expectedTotal {
		t.Fatalf("expected %d unique messages cleared, got %d", expectedTotal, len(cleared))
	}
}

func TestSteerRaceWithRunEnding(t *testing.T) {
	defer leakcheck.Verify(t)
	store, _, fake, loop, _ := testSetup(t)
	fake.Responses = []*provider.Response{{Text: "finished turn", StopReason: "stop"}}
	cfg := Config{Model: modelMeta(), Thinking: "off", Tools: toolDefs(), System: "base"}

	var steerOnce sync.Once
	steeredText := "late-steer-message"

	loop.sink = func(e event.Event) {
		if e.Type == event.RunEnd {
			steerOnce.Do(func() {
				loop.Steer(steeredText)
			})
		}
	}

	err := loop.Submit(context.Background(), "start", cfg)
	if err != nil {
		t.Fatalf("submit failed: %v", err)
	}

	proj, err := store.Projection()
	if err != nil {
		t.Fatal(err)
	}
	inStore := false
	for _, m := range proj.Flat {
		if m.Role == "user" && strings.Contains(fmt.Sprint(m.User.Content), steeredText) {
			inStore = true
			break
		}
	}
	inPending := false
	for _, m := range loop.PendingSteering() {
		if m == steeredText {
			inPending = true
			break
		}
	}

	if !inStore && !inPending {
		t.Fatal("late steering message was silently dropped!")
	}
	if inStore && inPending {
		t.Fatal("late steering message is duplicated in store and pending queue!")
	}
}
