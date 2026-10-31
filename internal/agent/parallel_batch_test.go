package agent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"psi/internal/event"
	"psi/internal/provider"
	"psi/internal/session"
	"psi/internal/testutil/leakcheck"
	"psi/internal/tools"
)

// Parallel batch regressions: eligible calls start concurrently after
// preflight, results settle in assistant-call order, workers never touch the
// store or sink, and cancellation and storage failures keep their guarantees.

// parallelHarness wires a loop whose reads gate per resolved path: a read
// signals entry after opening its file, then blocks until released.
type parallelHarness struct {
	ws    string
	store *session.Store
	reg   *tools.Registry
	fake  *provider.Fake
	loop  *Loop

	eventMu sync.Mutex
	events  []event.Event

	gateMu   sync.Mutex
	entered  map[string]chan struct{}
	released map[string]chan struct{}
}

func newParallelHarness(t *testing.T, responses []*provider.Response) *parallelHarness {
	t.Helper()
	ws := t.TempDir()
	store, err := session.New(session.OpenOptions{Cwd: ws, SessionDir: t.TempDir(), Persist: true})
	if err != nil {
		t.Fatal(err)
	}
	h := &parallelHarness{
		ws: ws, store: store, reg: tools.NewRegistry(ws), fake: &provider.Fake{Responses: responses},
		entered: map[string]chan struct{}{}, released: map[string]chan struct{}{},
	}
	channel := func(m map[string]chan struct{}, key string) chan struct{} {
		h.gateMu.Lock()
		defer h.gateMu.Unlock()
		c, ok := m[key]
		if !ok {
			c = make(chan struct{})
			m[key] = c
		}
		return c
	}
	h.reg.Read.AfterOpen = func(path string) {
		close(channel(h.entered, path))
		<-channel(h.released, path)
	}
	h.loop = New(store, h.reg, h.fake, h.logEvent)
	return h
}

func (h *parallelHarness) logEvent(e event.Event) {
	h.eventMu.Lock()
	h.events = append(h.events, e)
	h.eventMu.Unlock()
}

func (h *parallelHarness) config() Config {
	return Config{Model: modelMeta(), Thinking: "off", Tools: toolDefs(), System: "base"}
}

// gate pre-registers the entry and release channels for name, so the test can
// wait on them before the worker opens the file.
func (h *parallelHarness) gate(name string) (entered, released chan struct{}) {
	h.gateMu.Lock()
	defer h.gateMu.Unlock()
	key := filepath.Join(h.ws, name)
	if c, ok := h.entered[key]; ok {
		return c, h.released[key]
	}
	entered, released = make(chan struct{}), make(chan struct{})
	h.entered[key], h.released[key] = entered, released
	return entered, released
}

func (h *parallelHarness) enteredFile(name string) <-chan struct{} {
	entered, _ := h.gate(name)
	return entered
}

func (h *parallelHarness) releaseFile(name string) {
	_, released := h.gate(name)
	close(released)
}

// openGate pre-registers name with its release already settled, for calls the
// test does not gate.
func (h *parallelHarness) openGate(name string) {
	_, released := h.gate(name)
	close(released)
}

func (h *parallelHarness) eventsOf(types ...event.Type) []event.Event {
	h.eventMu.Lock()
	defer h.eventMu.Unlock()
	var out []event.Event
	for _, e := range h.events {
		for _, want := range types {
			if e.Type == want {
				out = append(out, e)
				break
			}
		}
	}
	return out
}

// batchResponse returns one assistant response that calls the given tools.
func batchResponse(calls ...provider.Block) *provider.Response {
	return &provider.Response{ToolCalls: calls, StopReason: "toolUse"}
}

func readCall(id, file string) provider.Block {
	return provider.Block{Type: "toolCall", ID: id, Name: "read", Arguments: map[string]any{"path": file}}
}

func writeCall(id, file, content string) provider.Block {
	return provider.Block{Type: "toolCall", ID: id, Name: "write", Arguments: map[string]any{"path": file, "content": content}}
}

func writeFileFor(t *testing.T, ws, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(ws, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestParallelReadsStartBeforeEitherCompletes: two gated reads both start
// before either completes; the second finishes first, and settlement still
// waits behind the batch barrier in call order.
func TestParallelReadsStartBeforeEitherCompletes(t *testing.T) {
	h := newParallelHarness(t, []*provider.Response{
		batchResponse(readCall("r1", "a.txt"), readCall("r2", "b.txt")),
		{Text: "done", StopReason: "stop"},
	})
	writeFileFor(t, h.ws, "a.txt", "alpha")
	writeFileFor(t, h.ws, "b.txt", "beta")
	// Pre-register both gates so entry waits cannot race worker start.
	aEntered, _ := h.gate("a.txt")
	bEntered, _ := h.gate("b.txt")
	done := make(chan error, 1)
	go func() { done <- h.loop.Submit(context.Background(), "go", h.config()) }()
	<-aEntered
	<-bEntered
	// Both reads are live at once. Release the second first.
	h.releaseFile("b.txt")
	// r2 finished executing, but r1 still runs: no row may settle and no new
	// provider request may cross the settlement barrier.
	if ends := h.eventsOf(event.ToolEnd); len(ends) != 0 {
		t.Fatalf("settlement crossed the batch barrier: %+v", ends)
	}
	if n := h.fake.CallCount(); n != 1 {
		t.Fatalf("next request crossed the settlement barrier: %d requests", n)
	}
	h.releaseFile("a.txt")
	if err := <-done; err != nil {
		t.Fatalf("run failed: %v", err)
	}
	ends := h.eventsOf(event.ToolEnd)
	if len(ends) != 2 || ends[0].ToolCallID != "r1" || ends[1].ToolCallID != "r2" {
		t.Fatalf("terminal rows settle in call order, got %+v", ends)
	}
	starts := h.eventsOf(event.ToolStart)
	if len(starts) != 2 || starts[0].ToolCallID != "r1" || starts[1].ToolCallID != "r2" {
		t.Fatalf("starts preflight in call order, got %+v", starts)
	}
	proj, err := h.store.Projection()
	if err != nil {
		t.Fatal(err)
	}
	var stored []string
	for _, m := range proj.Flat {
		if m.Role == "toolResult" {
			stored = append(stored, m.ToolResult.ToolCallID)
		}
	}
	if len(stored) != 2 || stored[0] != "r1" || stored[1] != "r2" {
		t.Fatalf("stored results keep call order, got %v", stored)
	}
	// The next request carries both results in call order, exactly once.
	if h.fake.CallCount() != 2 {
		t.Fatalf("expected two provider requests, got %d", h.fake.CallCount())
	}
	var seen []string
	for _, m := range h.fake.Requests()[1].Messages {
		if m.Role == "toolResult" {
			seen = append(seen, m.ToolCallID)
		}
	}
	if len(seen) != 2 || seen[0] != "r1" || seen[1] != "r2" {
		t.Fatalf("next request results = %v", seen)
	}
}

// TestParallelFailureKeepsSuccessfulResults: one read fails while another
// succeeds; both results reach the next request exactly once.
func TestParallelFailureKeepsSuccessfulResults(t *testing.T) {
	h := newParallelHarness(t, []*provider.Response{
		batchResponse(readCall("bad", "missing.txt"), readCall("good", "good.txt")),
		{Text: "done", StopReason: "stop"},
	})
	writeFileFor(t, h.ws, "good.txt", "ok")
	h.openGate("good.txt")
	if err := h.loop.Submit(context.Background(), "go", h.config()); err != nil {
		t.Fatal(err)
	}
	if h.fake.CallCount() != 2 {
		t.Fatalf("expected two requests, got %d", h.fake.CallCount())
	}
	var good, bad int
	for _, m := range h.fake.Requests()[1].Messages {
		if m.Role == "toolResult" && m.ToolCallID == "good" {
			good++
			if m.IsError {
				t.Fatal("successful read reported as error")
			}
		}
		if m.Role == "toolResult" && m.ToolCallID == "bad" {
			bad++
			if !m.IsError {
				t.Fatal("failed read reported as success")
			}
		}
	}
	if good != 1 || bad != 1 {
		t.Fatalf("results must reach the next request exactly once: good=%d bad=%d", good, bad)
	}
}

// TestParallelDisabledAndUnknownToolsDoNotRun: disabled tools settle with the
// not-enabled error; a nonexistent tool never executes.
func TestParallelDisabledAndUnknownToolsDoNotRun(t *testing.T) {
	h := newParallelHarness(t, []*provider.Response{
		batchResponse(
			readCall("ok", "ok.txt"),
			provider.Block{Type: "toolCall", ID: "off", Name: "bash", Arguments: map[string]any{"command": "touch ran-bash"}},
			provider.Block{Type: "toolCall", ID: "ghost", Name: "nonexistent", Arguments: map[string]any{}},
		),
		{Text: "done", StopReason: "stop"},
	})
	writeFileFor(t, h.ws, "ok.txt", "ok")
	h.openGate("ok.txt")
	cfg := h.config()
	cfg.Tools = []provider.ToolDef{{Name: "read", Description: "read", Schema: map[string]any{"type": "object"}}}
	if err := h.loop.Submit(context.Background(), "go", cfg); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(h.ws, "ran-bash")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("disabled bash tool ran")
	}
	for _, m := range h.fake.Calls[1].Messages {
		if m.Role == "toolResult" && (m.ToolCallID == "off" || m.ToolCallID == "ghost") && !m.IsError {
			t.Fatalf("ineligible call must be an error result: %+v", m)
		}
	}
	if ends := h.eventsOf(event.ToolEnd); len(ends) != 3 {
		t.Fatalf("every call settles exactly one row, got %+v", ends)
	}
}

// TestParallelStartedCallsSettleCancelled: cancellation during execution
// joins every started worker; started calls report truthful cancellation.
func TestParallelStartedCallsSettleCancelled(t *testing.T) {
	h := newParallelHarness(t, []*provider.Response{
		batchResponse(readCall("r1", "a.txt"), readCall("r2", "b.txt")),
	})
	writeFileFor(t, h.ws, "a.txt", "a")
	writeFileFor(t, h.ws, "b.txt", "b")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	// Pre-register gates: both calls are admitted before cancellation.
	aEntered, _ := h.gate("a.txt")
	bEntered, _ := h.gate("b.txt")
	go func() { done <- h.loop.Submit(ctx, "go", h.config()) }()
	<-aEntered
	<-bEntered
	cancel()
	h.releaseFile("a.txt")
	h.releaseFile("b.txt")
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("expected cancellation, got %v", err)
	}
	proj, err := h.store.Projection()
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range proj.Flat {
		if m.Role != "toolResult" {
			continue
		}
		if !strings.Contains(string(m.ToolResult.Details), `"cancelled"`) {
			t.Fatalf("started call %s must settle cancelled, got %s", m.ToolResult.ToolCallID, m.ToolResult.Details)
		}
		if m.ToolResult.ToolCallID == "r1" && strings.Contains(string(m.ToolResult.Details), "not_started") {
			t.Fatal("started call mislabeled as not started")
		}
	}
	if len(session.UnresolvedToolCalls(proj.Flat)) != 0 {
		t.Fatal("cancelled batch left unresolved calls")
	}
}

// TestParallelChainedInternalCallCancelledDoesNotRun: a queued internal
// control call behind a blocked one never runs after cancellation; the
// blocked one settles truthfully. The internal chain makes the not-started
// schedule deterministic.
func TestParallelChainedInternalCallCancelledDoesNotRun(t *testing.T) {
	ws := t.TempDir()
	store, err := session.New(session.OpenOptions{Cwd: ws, SessionDir: t.TempDir(), Persist: false})
	if err != nil {
		t.Fatal(err)
	}
	reg := tools.NewRegistry(ws)
	firstEntered := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	fake := &provider.Fake{Responses: []*provider.Response{
		batchResponse(
			provider.Block{Type: "toolCall", ID: "c1", Name: "control", Arguments: map[string]any{}},
			provider.Block{Type: "toolCall", ID: "c2", Name: "control", Arguments: map[string]any{}},
		),
	}}
	cfg := Config{
		Model: modelMeta(), Tools: toolDefs(),
		InternalTools: map[string]InternalToolHandler{
			"control": func(ctx context.Context, _ tools.Invocation) tools.Result {
				once.Do(func() { close(firstEntered) })
				<-release
				if ctx.Err() != nil {
					return tools.Result{IsError: true, Content: []tools.Block{{Type: "text", Text: "control interrupted"}}}
				}
				return tools.Result{Content: []tools.Block{{Type: "text", Text: "control ran"}}}
			},
		},
	}
	loop := New(store, reg, fake, func(event.Event) {})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- loop.Submit(ctx, "go", cfg) }()
	<-firstEntered
	cancel()
	close(release)
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("expected cancellation, got %v", err)
	}
	proj, err := store.Projection()
	if err != nil {
		t.Fatal(err)
	}
	states := map[string]string{}
	for _, m := range proj.Flat {
		if m.Role == "toolResult" {
			states[m.ToolResult.ToolCallID] = string(m.ToolResult.Details)
		}
	}
	if !strings.Contains(states["c1"], `"cancelled"`) {
		t.Fatalf("blocked control call must settle cancelled, got %s", states["c1"])
	}
	if !strings.Contains(states["c2"], "not_started") {
		t.Fatalf("queued control call must record not_started, got %s", states["c2"])
	}
	if len(session.UnresolvedToolCalls(proj.Flat)) != 0 {
		t.Fatal("cancelled batch left unresolved calls")
	}
}

// TestParallelStorageFailureBlocksContinuation: a storage failure after tool
// execution stops the run; unsaved results never reach a later request.
func TestParallelStorageFailureBlocksContinuation(t *testing.T) {
	h := newParallelHarness(t, []*provider.Response{
		batchResponse(readCall("r1", "a.txt"), readCall("r2", "b.txt")),
		{Text: "never", StopReason: "stop"},
	})
	writeFileFor(t, h.ws, "a.txt", "a")
	writeFileFor(t, h.ws, "b.txt", "b")
	// Fail durable writes when the first worker starts: both workers already
	// passed admission, so execution happens, and the first settle refuses.
	h.openGate("a.txt")
	h.openGate("b.txt")
	failNow := func() { h.store.FailWritesForTest(errors.New("disk refusal")) }
	h.loop = New(h.store, h.reg, h.fake, func(e event.Event) {
		h.logEvent(e)
		if e.Type == event.ToolStart && e.ToolCallID == "r1" {
			failNow()
		}
	})
	err := h.loop.Submit(context.Background(), "go", h.config())
	if err == nil {
		t.Fatal("storage failure must stop the run")
	}
	if !strings.Contains(err.Error(), "storage failure") {
		t.Fatalf("run must report the storage failure, got %v", err)
	}
	if n := h.fake.CallCount(); n != 1 {
		t.Fatalf("unsaved results must not reach a next request, got %d requests", n)
	}
	ends := h.eventsOf(event.ToolEnd)
	if len(ends) != 0 {
		t.Fatalf("no row may commit without durable settlement, got %+v", ends)
	}
}

// TestParallelQueuedMutationCancelledDoesNotRun: two writes to one file race
// for the mutation slot; whichever holds it blocks before writing. After
// cancellation neither write mutates the file, and the queued one never runs.
// Registration order between the two workers is scheduling-dependent, so the
// assertions cover both orders; the queue's own ordering has dedicated tests.
func TestParallelQueuedMutationCancelledDoesNotRun(t *testing.T) {
	ws := t.TempDir()
	store, err := session.New(session.OpenOptions{Cwd: ws, SessionDir: t.TempDir(), Persist: false})
	if err != nil {
		t.Fatal(err)
	}
	reg := tools.NewRegistry(ws)
	entered := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	reg.Write.AfterMkdir = func() {
		once.Do(func() { close(entered) })
		<-release
	}
	fake := &provider.Fake{Responses: []*provider.Response{
		batchResponse(writeCall("w1", "f.txt", "first"), writeCall("w2", "f.txt", "second")),
	}}
	loop := New(store, reg, fake, func(event.Event) {})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- loop.Submit(ctx, "go", Config{Model: modelMeta(), Tools: toolDefs()}) }()
	<-entered
	cancel()
	close(release)
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("expected cancellation, got %v", err)
	}
	if _, err := os.Stat(filepath.Join(ws, "f.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("cancelled queued mutation ran")
	}
	proj, err := store.Projection()
	if err != nil {
		t.Fatal(err)
	}
	settled := 0
	for _, m := range proj.Flat {
		if m.Role != "toolResult" {
			continue
		}
		settled++
		if text := strings.Join(textOf(m.ToolResult.Content), " "); !strings.Contains(text, "cancel") {
			t.Fatalf("write %s must report cancellation, got %s", m.ToolResult.ToolCallID, text)
		}
	}
	if settled != 2 {
		t.Fatalf("both writes must settle, got %d", settled)
	}
}

// TestParallelCompletedMutationReportsOutcome: a write that finished before
// cancellation reports that the file changed; it never claims a rollback.
func TestParallelCompletedMutationReportsOutcome(t *testing.T) {
	ws := t.TempDir()
	store, err := session.New(session.OpenOptions{Cwd: ws, SessionDir: t.TempDir(), Persist: false})
	if err != nil {
		t.Fatal(err)
	}
	reg := tools.NewRegistry(ws)
	wrote := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	reg.Write.WriteFile = func(path string, data []byte, mode os.FileMode) error {
		if err := os.WriteFile(path, data, mode); err != nil {
			return err
		}
		once.Do(func() { close(wrote) })
		<-release
		return nil
	}
	fake := &provider.Fake{Responses: []*provider.Response{
		batchResponse(writeCall("w1", "f.txt", "kept")),
	}}
	loop := New(store, reg, fake, func(event.Event) {})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- loop.Submit(ctx, "go", Config{Model: modelMeta(), Tools: toolDefs()}) }()
	<-wrote
	cancel()
	close(release)
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("expected cancellation, got %v", err)
	}
	proj, err := store.Projection()
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range proj.Flat {
		if m.Role == "toolResult" && m.ToolResult.ToolCallID == "w1" {
			if !strings.Contains(strings.Join(textOf(m.ToolResult.Content), " "), "file was modified") {
				t.Fatalf("completed mutation must report its actual outcome: %s", m.ToolResult.Details)
			}
		}
	}
	data, _ := os.ReadFile(filepath.Join(ws, "f.txt"))
	if string(data) != "kept" {
		t.Fatalf("file content changed unexpectedly: %q", data)
	}
}

func textOf(blocks []session.UserContentBlock) []string {
	var out []string
	for _, b := range blocks {
		if b.Text != nil {
			out = append(out, b.Text.Text)
		}
	}
	return out
}

// TestParallelBashProcessesAreIndependent: concurrent bash calls keep
// separate process state and both settle.
func TestParallelBashProcessesAreIndependent(t *testing.T) {
	ws := t.TempDir()
	store, err := session.New(session.OpenOptions{Cwd: ws, SessionDir: t.TempDir(), Persist: false})
	if err != nil {
		t.Fatal(err)
	}
	reg := tools.NewRegistry(ws)
	fake := &provider.Fake{Responses: []*provider.Response{
		batchResponse(
			provider.Block{Type: "toolCall", ID: "b1", Name: "bash", Arguments: map[string]any{"command": "printf one"}},
			provider.Block{Type: "toolCall", ID: "b2", Name: "bash", Arguments: map[string]any{"command": "printf two"}},
		),
		{Text: "done", StopReason: "stop"},
	}}
	loop := New(store, reg, fake, func(event.Event) {})
	if err := loop.Submit(context.Background(), "go", Config{Model: modelMeta(), Tools: toolDefs()}); err != nil {
		t.Fatal(err)
	}
	proj, err := store.Projection()
	if err != nil {
		t.Fatal(err)
	}
	texts := map[string]string{}
	for _, m := range proj.Flat {
		if m.Role == "toolResult" {
			for _, block := range m.ToolResult.Content {
				if block.Text != nil {
					texts[m.ToolResult.ToolCallID] = block.Text.Text
				}
			}
		}
	}
	if texts["b1"] != "one" || texts["b2"] != "two" {
		t.Fatalf("concurrent bash results crossed: %v", texts)
	}
}

// TestParallelLoopControlMixesWithReads: a control call in a mixed batch
// keeps its state transitions ordered, settles in call order, and defers its
// model-visible continuation until the whole batch is durable. The read gates
// the batch, so the control call runs concurrently and settles after it.
func TestParallelLoopControlMixesWithReads(t *testing.T) {
	ws := t.TempDir()
	store, err := session.New(session.OpenOptions{Cwd: ws, SessionDir: t.TempDir(), Persist: false})
	if err != nil {
		t.Fatal(err)
	}
	reg := tools.NewRegistry(ws)
	readEntered := make(chan struct{})
	readRelease := make(chan struct{})
	var once sync.Once
	reg.Read.AfterOpen = func(string) {
		once.Do(func() { close(readEntered) })
		<-readRelease
	}
	if err := os.WriteFile(filepath.Join(ws, "a.txt"), []byte("data"), 0o644); err != nil {
		t.Fatal(err)
	}
	fake := &provider.Fake{Responses: []*provider.Response{
		batchResponse(
			readCall("r1", "a.txt"),
			provider.Block{Type: "toolCall", ID: "control-1", Name: "loop_control", Arguments: map[string]any{"status": "next", "summary": "step done"}},
		),
		{Text: "finished", StopReason: "stop"},
	}}
	controlRan := make(chan struct{}, 1)
	var continuations []string
	loop := New(store, reg, fake, func(event.Event) {})
	cfg := Config{
		Model: modelMeta(), Tools: toolDefs(),
		InternalTools: map[string]InternalToolHandler{
			"loop_control": func(_ context.Context, _ tools.Invocation) tools.Result {
				controlRan <- struct{}{}
				return tools.Result{Content: []tools.Block{{Type: "text", Text: "advanced"}}}
			},
		},
		AfterToolCall: func(_ context.Context, inv tools.Invocation, _ tools.Result) error {
			if inv.Name != "loop_control" {
				return nil
			}
			continuations = append(continuations, "iteration")
			// Mirror the app wiring: the continuation is a hidden custom entry
			// appended after the control result is durable.
			_, err := store.AppendCustomMessage("psi.loop.iteration", "continue the active task", false, nil)
			return err
		},
	}
	done := make(chan error, 1)
	go func() { done <- loop.Submit(context.Background(), "go", cfg) }()
	<-readEntered
	// The control call may run while the read still holds the batch open.
	select {
	case <-controlRan:
	default:
	}
	close(readRelease)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	proj, err := store.Projection()
	if err != nil {
		t.Fatal(err)
	}
	var order []string
	for _, m := range proj.Flat {
		switch {
		case m.Role == "toolResult":
			order = append(order, "result:"+m.ToolResult.ToolCallID)
		case m.Role == "custom":
			order = append(order, "custom:"+m.Custom.CustomType)
		}
	}
	want := []string{"result:r1", "result:control-1"}
	if len(order) < 2 || order[0] != want[0] || order[1] != want[1] {
		t.Fatalf("settlement order = %v, want %v first", order, want)
	}
	if len(continuations) != 1 {
		t.Fatalf("continuation count = %d, want 1", len(continuations))
	}
	// The follow-up request carries both results in call order, then the
	// hidden continuation as the final user message.
	reqs := fake.Requests()
	if len(reqs) != 2 {
		t.Fatalf("expected 2 requests, got %d", len(reqs))
	}
	var tail []string
	for _, m := range reqs[1].Messages {
		if m.Role == "toolResult" {
			tail = append(tail, "toolResult:"+m.ToolCallID)
		} else if m.Role == "user" {
			tail = append(tail, "user")
		}
	}
	if len(tail) != 4 || tail[1] != "toolResult:r1" || tail[2] != "toolResult:control-1" || tail[3] != "user" {
		t.Fatalf("next request order = %v, want [user toolResult:r1 toolResult:control-1 user]", tail)
	}
}

// TestParallelSteeringWaitsForBatchSettlement: steering queued while a batch
// runs is delivered after settlement and before the next request.
func TestParallelSteeringWaitsForBatchSettlement(t *testing.T) {
	h := newParallelHarness(t, []*provider.Response{
		batchResponse(readCall("r1", "a.txt")),
		{Text: "done", StopReason: "stop"},
	})
	writeFileFor(t, h.ws, "a.txt", "a")
	aEntered, _ := h.gate("a.txt")
	done := make(chan error, 1)
	go func() { done <- h.loop.Submit(context.Background(), "go", h.config()) }()
	<-aEntered
	// The batch holds the barrier; steering queued now must not jump it.
	h.loop.Steer("meanwhile")
	if n := h.fake.CallCount(); n != 1 {
		t.Fatalf("steering crossed the settlement barrier: %d requests", n)
	}
	h.releaseFile("a.txt")
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	// The follow-up request carries the steering as a user message after the
	// tool result.
	if h.fake.CallCount() != 2 {
		t.Fatalf("expected 2 requests, got %d", h.fake.CallCount())
	}
	var sawSteer bool
	for _, m := range h.fake.Requests()[1].Messages {
		if m.Role == "user" && containsBlockText(m.Content, "meanwhile") {
			sawSteer = true
		}
	}
	if !sawSteer {
		t.Fatalf("steering never reached the next request: %+v", h.fake.Calls[1].Messages)
	}
}

// TestParallelBatchSettlesImagesAndText: a batch with one image read and one
// text read settles both content kinds with call/result linkage intact.
func TestParallelBatchSettlesImagesAndText(t *testing.T) {
	h := newParallelHarness(t, []*provider.Response{
		batchResponse(readCall("img", "dot.png"), readCall("txt", "note.txt")),
		{Text: "done", StopReason: "stop"},
	})
	// 1x1 transparent PNG.
	png, err := os.ReadFile(filepath.Join("testdata", "dot.png"))
	if err != nil {
		png = []byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a, 0x00, 0x00, 0x00, 0x0d, 0x49,
			0x48, 0x44, 0x52, 0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01, 0x08, 0x06, 0x00,
			0x00, 0x00, 0x1f, 0x15, 0xc4, 0x89, 0x00, 0x00, 0x00, 0x0d, 0x49, 0x44, 0x41, 0x54,
			0x78, 0x9c, 0x63, 0x00, 0x01, 0x00, 0x00, 0x05, 0x00, 0x01, 0x0d, 0x0a, 0x2d, 0xb4,
			0x00, 0x00, 0x00, 0x00, 0x49, 0x45, 0x4e, 0x44, 0xae, 0x42, 0x60, 0x82}
	}
	if err := os.WriteFile(filepath.Join(h.ws, "dot.png"), png, 0o644); err != nil {
		t.Fatal(err)
	}
	writeFileFor(t, h.ws, "note.txt", "words")
	h.openGate("dot.png")
	h.openGate("note.txt")
	if err := h.loop.Submit(context.Background(), "go", h.config()); err != nil {
		t.Fatal(err)
	}
	if h.fake.CallCount() != 2 {
		t.Fatalf("expected 2 requests, got %d", h.fake.CallCount())
	}
	var images, texts int
	for _, m := range h.fake.Requests()[1].Messages {
		if m.Role != "toolResult" {
			continue
		}
		for _, b := range m.Content {
			switch b.Type {
			case "image":
				images++
				if b.MimeType != "image/png" || b.Data == "" {
					t.Fatalf("image block lost payload: %+v", b)
				}
			case "text":
				texts++
			}
		}
	}
	if images != 1 || texts != 2 {
		t.Fatalf("parallel batch content kinds: images=%d texts=%d", images, texts)
	}
	// Reopen the session file and continue: the JSONL prefix survives and the
	// projection resolves both calls without repair.
	reopened, err := session.OpenFile(h.store.File(), h.ws, false)
	if err != nil {
		t.Fatal(err)
	}
	proj, err := reopened.Projection()
	if err != nil {
		t.Fatal(err)
	}
	if len(session.UnresolvedToolCalls(proj.Flat)) != 0 {
		t.Fatal("reopened session left unresolved calls")
	}
	var settled []string
	for _, m := range proj.Flat {
		if m.Role == "toolResult" {
			settled = append(settled, m.ToolResult.ToolCallID)
		}
	}
	if len(settled) != 2 || settled[0] != "img" || settled[1] != "txt" {
		t.Fatalf("reopened results out of order: %v", settled)
	}
}

// TestParallelLargeBatchStaysBounded runs a 200-call read batch and records
// heap growth. The batch coordinator must hold every worker result without
// unbounded buffering; the ceiling documents the measured behavior.
func TestParallelLargeBatchStaysBounded(t *testing.T) {
	const calls = 200
	ws := t.TempDir()
	store, err := session.New(session.OpenOptions{Cwd: ws, SessionDir: t.TempDir(), Persist: false})
	if err != nil {
		t.Fatal(err)
	}
	reg := tools.NewRegistry(ws)
	body := strings.Repeat("x", 4096)
	if err := os.WriteFile(filepath.Join(ws, "big.txt"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	var batch []provider.Block
	for i := 0; i < calls; i++ {
		batch = append(batch, readCall(fmt.Sprintf("r%d", i), "big.txt"))
	}
	fake := &provider.Fake{Responses: []*provider.Response{
		{ToolCalls: batch, StopReason: "toolUse"},
		{Text: "done", StopReason: "stop"},
	}}
	loop := New(store, reg, fake, func(event.Event) {})
	var before runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	start := time.Now()
	if err := loop.Submit(context.Background(), "go", Config{Model: modelMeta(), Tools: toolDefs()}); err != nil {
		t.Fatal(err)
	}
	elapsed := time.Since(start)
	var after runtime.MemStats
	runtime.ReadMemStats(&after)
	grew := after.TotalAlloc - before.TotalAlloc
	proj, err := store.Projection()
	if err != nil {
		t.Fatal(err)
	}
	settled := 0
	for _, m := range proj.Flat {
		if m.Role == "toolResult" {
			settled++
		}
	}
	if settled != calls {
		t.Fatalf("settled %d of %d calls", settled, calls)
	}
	// Recorded profile: 200 x 4 KiB reads complete well under a second with
	// tens of MiB of total allocation. The ceilings keep regressions visible
	// without pinning exact numbers.
	if grew > 256<<20 {
		t.Fatalf("200-call batch allocated %d bytes", grew)
	}
	if elapsed > 30*time.Second {
		t.Fatalf("200-call batch took %s", elapsed)
	}
	t.Logf("batch=%d settled=%d alloc=%d bytes elapsed=%s", calls, settled, grew, elapsed)
}

// TestBatchDrain_StorageFailureWaitsForInFlightWorkers: storage failure during coordinate
// calls stopBatch() and b.drain() joins all in-flight workers. Asserts no background
// worker continues writing or executing after Submit returns, and no goroutine leaks.
func TestBatchDrain_StorageFailureWaitsForInFlightWorkers(t *testing.T) {
	defer leakcheck.Verify(t)
	ws := t.TempDir()
	store, err := session.New(session.OpenOptions{Cwd: ws, SessionDir: t.TempDir(), Persist: true})
	if err != nil {
		t.Fatal(err)
	}
	reg := tools.NewRegistry(ws)
	writeFileFor(t, ws, "fast.txt", "fast data")

	var activeWorkers atomic.Int32
	var submitReturned atomic.Bool
	var workerExecutedAfterSubmit atomic.Bool

	worker1Entered := make(chan struct{})
	worker2Entered := make(chan struct{})
	var w1Once, w2Once sync.Once

	reg.Write.WriteFile = func(path string, data []byte, mode os.FileMode) error {
		activeWorkers.Add(1)
		defer activeWorkers.Add(-1)

		if strings.Contains(path, "w1.txt") {
			w1Once.Do(func() { close(worker1Entered) })
		}
		if strings.Contains(path, "w2.txt") {
			w2Once.Do(func() { close(worker2Entered) })
		}

		time.Sleep(50 * time.Millisecond)

		if submitReturned.Load() {
			workerExecutedAfterSubmit.Store(true)
		}
		return os.WriteFile(path, data, mode)
	}

	fake := &provider.Fake{Responses: []*provider.Response{
		batchResponse(
			readCall("r0", "fast.txt"),
			writeCall("w1", "w1.txt", "payload 1"),
			writeCall("w2", "w2.txt", "payload 2"),
		),
		{Text: "never reached", StopReason: "stop"},
	}}

	loop := New(store, reg, fake, func(e event.Event) {})
	cfg := Config{Model: modelMeta(), Tools: toolDefs()}

	done := make(chan error, 1)
	go func() {
		done <- loop.Submit(context.Background(), "go", cfg)
	}()

	// Wait until workers w1 and w2 have entered WriteFile
	<-worker1Entered
	<-worker2Entered

	// Now fail writes before r0 settles
	store.FailWritesForTest(errors.New("simulated storage failure"))

	err = <-done
	submitReturned.Store(true)

	if err == nil {
		t.Fatal("expected storage failure error, got nil")
	}
	if !strings.Contains(err.Error(), "storage failure") {
		t.Fatalf("expected storage failure, got %v", err)
	}

	// Invariant: b.drain() must have waited for all in-flight workers!
	if active := activeWorkers.Load(); active != 0 {
		t.Fatalf("active workers when Submit returned = %d, want 0", active)
	}
	if workerExecutedAfterSubmit.Load() {
		t.Fatal("worker executed after Submit returned!")
	}
}

// TestParallelBatch_CancellationTenCallsSomeRunningSomeQueued: context cancelled with 10 tool
// calls in progress (5 started, 5 queued/unadmitted). Admitted calls report cancelled,
// unadmitted report not_started, and every tool_use ID has a matching tool_result (no orphans).
func TestParallelBatch_CancellationTenCallsSomeRunningSomeQueued(t *testing.T) {
	defer leakcheck.Verify(t)
	ws := t.TempDir()
	store, err := session.New(session.OpenOptions{Cwd: ws, SessionDir: t.TempDir(), Persist: false})
	if err != nil {
		t.Fatal(err)
	}
	reg := tools.NewRegistry(ws)
	for i := 0; i < 3; i++ {
		writeFileFor(t, ws, fmt.Sprintf("file%d.txt", i), "data")
	}

	// 5 builtin calls: b0, b1, b2 (reads); b3, b4 (writes to same file "mut.txt" so b4 is queued)
	// 5 internal calls: c0, c1, c2, c3, c4 (chained, so c1..c4 are queued)
	calls := []provider.Block{
		readCall("b0", "file0.txt"),
		readCall("b1", "file1.txt"),
		readCall("b2", "file2.txt"),
		writeCall("b3", "mut.txt", "v1"),
		writeCall("b4", "mut.txt", "v2"),
		{Type: "toolCall", ID: "c0", Name: "control", Arguments: map[string]any{"idx": 0}},
		{Type: "toolCall", ID: "c1", Name: "control", Arguments: map[string]any{"idx": 1}},
		{Type: "toolCall", ID: "c2", Name: "control", Arguments: map[string]any{"idx": 2}},
		{Type: "toolCall", ID: "c3", Name: "control", Arguments: map[string]any{"idx": 3}},
		{Type: "toolCall", ID: "c4", Name: "control", Arguments: map[string]any{"idx": 4}},
	}

	b0Entered := make(chan struct{})
	b1Entered := make(chan struct{})
	b2Entered := make(chan struct{})
	b3Entered := make(chan struct{})
	c0Entered := make(chan struct{})
	releaseAll := make(chan struct{})

	reg.Read.AfterOpen = func(path string) {
		if strings.Contains(path, "file0.txt") {
			close(b0Entered)
		} else if strings.Contains(path, "file1.txt") {
			close(b1Entered)
		} else if strings.Contains(path, "file2.txt") {
			close(b2Entered)
		}
		<-releaseAll
	}

	var b3Once sync.Once
	reg.Write.AfterMkdir = func() {
		b3Once.Do(func() { close(b3Entered) })
		<-releaseAll
	}

	var c0Once sync.Once
	cfg := Config{
		Model: modelMeta(), Tools: toolDefs(),
		InternalTools: map[string]InternalToolHandler{
			"control": func(ctx context.Context, inv tools.Invocation) tools.Result {
				if inv.ID == "c0" {
					c0Once.Do(func() { close(c0Entered) })
					<-releaseAll
				}
				if ctx.Err() != nil {
					return tools.Result{IsError: true, Content: []tools.Block{{Type: "text", Text: "control cancelled"}}}
				}
				return tools.Result{Content: []tools.Block{{Type: "text", Text: "control done"}}}
			},
		},
	}

	fake := &provider.Fake{Responses: []*provider.Response{
		batchResponse(calls...),
	}}
	loop := New(store, reg, fake, func(event.Event) {})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- loop.Submit(ctx, "go", cfg)
	}()

	// Wait for b0, b1, b2, b3, c0 to enter
	<-b0Entered
	<-b1Entered
	<-b2Entered
	<-b3Entered
	<-c0Entered

	// Cancel while 5 are running and 5 are queued
	cancel()
	close(releaseAll)

	err = <-done
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}

	proj, err := store.Projection()
	if err != nil {
		t.Fatal(err)
	}

	// 1. Every tool_use ID gets a matching tool_result in the session (no orphaned tool_use IDs)
	unresolved := session.UnresolvedToolCalls(session.ConvertToLLM(proj.Flat))
	if len(unresolved) != 0 {
		t.Fatalf("orphaned tool_use IDs found: %v", unresolved)
	}

	resultsByID := make(map[string]*session.ToolResultMessage)
	var resultOrder []string
	for _, m := range proj.Flat {
		if m.Role == "toolResult" && m.ToolResult != nil {
			resultsByID[m.ToolResult.ToolCallID] = m.ToolResult
			resultOrder = append(resultOrder, m.ToolResult.ToolCallID)
		}
	}

	if len(resultsByID) != 10 {
		t.Fatalf("expected 10 tool results, got %d", len(resultsByID))
	}

	expectedIDs := []string{"b0", "b1", "b2", "b3", "b4", "c0", "c1", "c2", "c3", "c4"}
	for i, id := range expectedIDs {
		if resultOrder[i] != id {
			t.Errorf("result %d = %s, want %s", i, resultOrder[i], id)
		}
	}

	// 2. Started calls report cancelled
	for _, id := range []string{"b0", "b1", "b2", "b3", "c0"} {
		res := resultsByID[id]
		if !strings.Contains(string(res.Details), `"cancelled"`) {
			t.Errorf("started call %s details = %s, want cancelled", id, res.Details)
		}
		if strings.Contains(string(res.Details), "not_started") {
			t.Errorf("started call %s was mislabeled as not_started", id)
		}
	}

	// 3. Unadmitted calls report not_started (specifically c1..c4 which waited on chain)
	for _, id := range []string{"c1", "c2", "c3", "c4"} {
		res := resultsByID[id]
		if !strings.Contains(string(res.Details), "not_started") {
			t.Errorf("unadmitted call %s details = %s, want not_started", id, res.Details)
		}
	}
}

// TestParallelBatch_OutOfOrderSettlementAppendsInAssistantOrder: when tool calls finish
// out of order, results are appended to session strictly in assistant order (c1, then c2, then c3...).
func TestParallelBatch_OutOfOrderSettlementAppendsInAssistantOrder(t *testing.T) {
	defer leakcheck.Verify(t)
	const n = 5
	var calls []provider.Block
	for i := 1; i <= n; i++ {
		calls = append(calls, readCall(fmt.Sprintf("r%d", i), fmt.Sprintf("f%d.txt", i)))
	}

	h := newParallelHarness(t, []*provider.Response{
		batchResponse(calls...),
		{Text: "done", StopReason: "stop"},
	})

	for i := 1; i <= n; i++ {
		writeFileFor(t, h.ws, fmt.Sprintf("f%d.txt", i), fmt.Sprintf("content-%d", i))
	}

	// Pre-register gates for all 5 files
	gates := make([]chan struct{}, n+1)
	for i := 1; i <= n; i++ {
		g, _ := h.gate(fmt.Sprintf("f%d.txt", i))
		gates[i] = g
	}

	done := make(chan error, 1)
	go func() {
		done <- h.loop.Submit(context.Background(), "go", h.config())
	}()

	// Wait for all 5 to be admitted and entered
	for i := 1; i <= n; i++ {
		<-gates[i]
	}

	// Release in scrambled order: r5, r3, r4, r2, and finally r1
	h.releaseFile("f5.txt")
	h.releaseFile("f3.txt")
	h.releaseFile("f4.txt")
	h.releaseFile("f2.txt")

	// Since r1 has not completed, NO tool results may have settled yet
	if ends := h.eventsOf(event.ToolEnd); len(ends) != 0 {
		t.Fatalf("results settled before r1 finished: %+v", ends)
	}

	// Release r1
	h.releaseFile("f1.txt")

	if err := <-done; err != nil {
		t.Fatalf("run failed: %v", err)
	}

	// Invariant: ToolEnd events are strictly in call order: r1, r2, r3, r4, r5
	ends := h.eventsOf(event.ToolEnd)
	if len(ends) != n {
		t.Fatalf("expected %d ToolEnd events, got %d", n, len(ends))
	}
	for i := 1; i <= n; i++ {
		expectedID := fmt.Sprintf("r%d", i)
		if ends[i-1].ToolCallID != expectedID {
			t.Errorf("ToolEnd %d = %s, want %s", i-1, ends[i-1].ToolCallID, expectedID)
		}
	}

	// Invariant: session projection results are strictly in call order
	proj, err := h.store.Projection()
	if err != nil {
		t.Fatal(err)
	}
	var storedIDs []string
	for _, m := range proj.Flat {
		if m.Role == "toolResult" && m.ToolResult != nil {
			storedIDs = append(storedIDs, m.ToolResult.ToolCallID)
		}
	}
	if len(storedIDs) != n {
		t.Fatalf("expected %d stored tool results, got %d", n, len(storedIDs))
	}
	for i := 1; i <= n; i++ {
		expectedID := fmt.Sprintf("r%d", i)
		if storedIDs[i-1] != expectedID {
			t.Errorf("stored result %d = %s, want %s", i-1, storedIDs[i-1], expectedID)
		}
	}

	// Invariant: provider request has tool results strictly in call order
	if h.fake.CallCount() != 2 {
		t.Fatalf("expected 2 provider requests, got %d", h.fake.CallCount())
	}
	var reqIDs []string
	for _, m := range h.fake.Requests()[1].Messages {
		if m.Role == "toolResult" {
			reqIDs = append(reqIDs, m.ToolCallID)
		}
	}
	for i := 1; i <= n; i++ {
		expectedID := fmt.Sprintf("r%d", i)
		if reqIDs[i-1] != expectedID {
			t.Errorf("request result %d = %s, want %s", i-1, reqIDs[i-1], expectedID)
		}
	}
}

// TestParallelBatch_MultipleMutationsSameFileInSingleBatch: multiple mutations (write -> edit -> edit -> write)
// to the same file in a single batch are serialized by the mutation queue and reflect sequential replay.
func TestParallelBatch_MultipleMutationsSameFileInSingleBatch(t *testing.T) {
	defer leakcheck.Verify(t)
	ws := t.TempDir()
	store, err := session.New(session.OpenOptions{Cwd: ws, SessionDir: t.TempDir(), Persist: false})
	if err != nil {
		t.Fatal(err)
	}
	reg := tools.NewRegistry(ws)

	// In one batch: write -> edit -> edit -> write
	batchCalls := []provider.Block{
		writeCall("w1", "doc.txt", "line 1\nline 2\nline 3\n"),
		{
			Type: "toolCall", ID: "e1", Name: "edit",
			Arguments: map[string]any{
				"path": "doc.txt",
				"edits": []any{
					map[string]any{"oldText": "line 2\n", "newText": "line 2 updated\n"},
				},
			},
		},
		{
			Type: "toolCall", ID: "e2", Name: "edit",
			Arguments: map[string]any{
				"path": "doc.txt",
				"edits": []any{
					map[string]any{"oldText": "line 3\n", "newText": "line 3 updated\n"},
				},
			},
		},
		writeCall("w2", "doc.txt", "line 1\nline 2 updated\nline 3 updated\nline 4 final\n"),
	}

	fake := &provider.Fake{Responses: []*provider.Response{
		batchResponse(batchCalls...),
		{Text: "done", StopReason: "stop"},
	}}

	loop := New(store, reg, fake, func(event.Event) {})
	cfg := Config{Model: modelMeta(), Tools: toolDefs()}

	if err := loop.Submit(context.Background(), "go", cfg); err != nil {
		t.Fatalf("batch failed: %v", err)
	}

	// Final file content reflects sequential application of all 4 mutations
	content, err := os.ReadFile(filepath.Join(ws, "doc.txt"))
	if err != nil {
		t.Fatal(err)
	}
	expectedContent := "line 1\nline 2 updated\nline 3 updated\nline 4 final\n"
	if string(content) != expectedContent {
		t.Fatalf("file content = %q, want %q", string(content), expectedContent)
	}

	// Projection has all 4 results without error
	proj, err := store.Projection()
	if err != nil {
		t.Fatal(err)
	}
	settledCount := 0
	for _, m := range proj.Flat {
		if m.Role == "toolResult" && m.ToolResult != nil {
			settledCount++
			if m.ToolResult.IsError {
				t.Fatalf("tool %s failed: %+v", m.ToolResult.ToolCallID, m.ToolResult.Content)
			}
		}
	}
	if settledCount != 4 {
		t.Fatalf("expected 4 settled results, got %d", settledCount)
	}
}

// TestParallelBatch_InternalToolsSequentialWhileBuiltinsOverlap: internal control tools
// execute strictly sequentially relative to each other, while built-in tools overlap concurrently.
func TestParallelBatch_InternalToolsSequentialWhileBuiltinsOverlap(t *testing.T) {
	defer leakcheck.Verify(t)
	ws := t.TempDir()
	store, err := session.New(session.OpenOptions{Cwd: ws, SessionDir: t.TempDir(), Persist: false})
	if err != nil {
		t.Fatal(err)
	}
	reg := tools.NewRegistry(ws)
	writeFileFor(t, ws, "b1.txt", "b1")
	writeFileFor(t, ws, "b2.txt", "b2")
	writeFileFor(t, ws, "b3.txt", "b3")

	var activeInternal atomic.Int32
	var maxInternal atomic.Int32
	var internalOrder []string
	var internalMu sync.Mutex

	var activeBuiltin atomic.Int32
	var maxBuiltin atomic.Int32
	var builtinOverlapWithInternal atomic.Bool

	releaseBuiltins := make(chan struct{})

	reg.Read.AfterOpen = func(string) {
		cur := activeBuiltin.Add(1)
		defer activeBuiltin.Add(-1)
		for {
			old := maxBuiltin.Load()
			if cur <= old || maxBuiltin.CompareAndSwap(old, cur) {
				break
			}
		}
		if activeInternal.Load() > 0 {
			builtinOverlapWithInternal.Store(true)
		}
		<-releaseBuiltins
	}

	batchCalls := []provider.Block{
		readCall("b1", "b1.txt"),
		{Type: "toolCall", ID: "i1", Name: "loop_control", Arguments: map[string]any{"step": 1}},
		readCall("b2", "b2.txt"),
		{Type: "toolCall", ID: "i2", Name: "loop_control", Arguments: map[string]any{"step": 2}},
		readCall("b3", "b3.txt"),
		{Type: "toolCall", ID: "i3", Name: "loop_control", Arguments: map[string]any{"step": 3}},
	}

	fake := &provider.Fake{Responses: []*provider.Response{
		batchResponse(batchCalls...),
		{Text: "done", StopReason: "stop"},
	}}

	internalDone := make(chan struct{})
	cfg := Config{
		Model: modelMeta(), Tools: toolDefs(),
		InternalTools: map[string]InternalToolHandler{
			"loop_control": func(_ context.Context, inv tools.Invocation) tools.Result {
				cur := activeInternal.Add(1)
				defer activeInternal.Add(-1)
				for {
					old := maxInternal.Load()
					if cur <= old || maxInternal.CompareAndSwap(old, cur) {
						break
					}
				}
				if cur > 1 {
					t.Errorf("concurrent internal tools detected! count = %d", cur)
				}
				if activeBuiltin.Load() > 0 {
					builtinOverlapWithInternal.Store(true)
				}
				internalMu.Lock()
				internalOrder = append(internalOrder, inv.ID)
				if len(internalOrder) == 3 {
					close(internalDone)
				}
				internalMu.Unlock()
				time.Sleep(15 * time.Millisecond)
				return tools.Result{Content: []tools.Block{{Type: "text", Text: "step ok"}}}
			},
		},
	}

	loop := New(store, reg, fake, func(event.Event) {})
	done := make(chan error, 1)
	go func() {
		done <- loop.Submit(context.Background(), "go", cfg)
	}()

	// Wait until all 3 internal tools have executed while built-ins were held open
	<-internalDone
	close(releaseBuiltins)

	if err := <-done; err != nil {
		t.Fatalf("batch failed: %v", err)
	}

	// 1. Internal tools were strictly sequential (maxInternal == 1)
	if m := maxInternal.Load(); m != 1 {
		t.Fatalf("max concurrent internal tools = %d, want 1", m)
	}

	// 2. Internal tools executed in call order
	internalMu.Lock()
	if len(internalOrder) != 3 || internalOrder[0] != "i1" || internalOrder[1] != "i2" || internalOrder[2] != "i3" {
		t.Fatalf("internal execution order = %v, want [i1, i2, i3]", internalOrder)
	}
	internalMu.Unlock()

	// 3. Built-in tools overlapped concurrently with each other
	if b := maxBuiltin.Load(); b <= 1 {
		t.Fatalf("max concurrent built-in tools = %d, expected > 1 (overlap)", b)
	}

	// 4. Built-in tools overlapped with internal tools
	if !builtinOverlapWithInternal.Load() {
		t.Fatal("expected built-in tools to overlap concurrently with internal tools")
	}

	// 5. Settlement order preserves assistant call order
	proj, err := store.Projection()
	if err != nil {
		t.Fatal(err)
	}
	var storedIDs []string
	for _, m := range proj.Flat {
		if m.Role == "toolResult" && m.ToolResult != nil {
			storedIDs = append(storedIDs, m.ToolResult.ToolCallID)
		}
	}
	wantIDs := []string{"b1", "i1", "b2", "i2", "b3", "i3"}
	if len(storedIDs) != len(wantIDs) {
		t.Fatalf("stored results = %v, want %v", storedIDs, wantIDs)
	}
	for i, id := range wantIDs {
		if storedIDs[i] != id {
			t.Errorf("result %d = %s, want %s", i, storedIDs[i], id)
		}
	}
}

// TestParallelBatch_ToolPanicDoesNotWedgeBatch: panics inside builtin tools or internal tools
// are recovered into error results; the mutation slot is released and the batch settles cleanly.
func TestParallelBatch_ToolPanicDoesNotWedgeBatch(t *testing.T) {
	defer leakcheck.Verify(t)
	ws := t.TempDir()
	store, err := session.New(session.OpenOptions{Cwd: ws, SessionDir: t.TempDir(), Persist: false})
	if err != nil {
		t.Fatal(err)
	}
	reg := tools.NewRegistry(ws)
	writeFileFor(t, ws, "ok.txt", "ok")

	reg.Read.AfterOpen = func(path string) {
		if strings.Contains(path, "panic.txt") {
			panic("simulated builtin tool panic")
		}
	}
	writeFileFor(t, ws, "panic.txt", "panic")

	batchCalls := []provider.Block{
		readCall("r1", "panic.txt"),
		{Type: "toolCall", ID: "i1", Name: "panicking_internal", Arguments: map[string]any{}},
		readCall("r2", "ok.txt"),
	}

	fake := &provider.Fake{Responses: []*provider.Response{
		batchResponse(batchCalls...),
		{Text: "done after panic", StopReason: "stop"},
	}}

	cfg := Config{
		Model: modelMeta(), Tools: toolDefs(),
		InternalTools: map[string]InternalToolHandler{
			"panicking_internal": func(context.Context, tools.Invocation) tools.Result {
				panic("simulated internal tool panic")
			},
		},
	}

	loop := New(store, reg, fake, func(event.Event) {})
	if err := loop.Submit(context.Background(), "go", cfg); err != nil {
		t.Fatalf("submit failed after tool panic: %v", err)
	}

	proj, err := store.Projection()
	if err != nil {
		t.Fatal(err)
	}
	results := make(map[string]*session.ToolResultMessage)
	for _, m := range proj.Flat {
		if m.Role == "toolResult" && m.ToolResult != nil {
			results[m.ToolResult.ToolCallID] = m.ToolResult
		}
	}

	if len(results) != 3 {
		t.Fatalf("expected 3 tool results, got %d", len(results))
	}
	if !results["r1"].IsError || !strings.Contains(results["r1"].Content[0].Text.Text, "panic") {
		t.Errorf("r1 result = %+v, want panic error", results["r1"])
	}
	if !results["i1"].IsError || !strings.Contains(results["i1"].Content[0].Text.Text, "panic") {
		t.Errorf("i1 result = %+v, want panic error", results["i1"])
	}
	if results["r2"].IsError {
		t.Errorf("r2 result should be success, got error: %+v", results["r2"])
	}
}

// TestParallelBatch_PreflightCompletesBeforeAnyWorkerBody asserts that for a batch
// with multiple tool calls, every ToolStart event and Prepare call completes before
// the first worker body executes.
func TestParallelBatch_PreflightCompletesBeforeAnyWorkerBody(t *testing.T) {
	defer leakcheck.Verify(t)
	ws := t.TempDir()
	store, err := session.New(session.OpenOptions{Cwd: ws, SessionDir: t.TempDir(), Persist: false})
	if err != nil {
		t.Fatal(err)
	}
	reg := tools.NewRegistry(ws)
	writeFileFor(t, ws, "f1.txt", "content1")
	writeFileFor(t, ws, "f2.txt", "content2")

	var (
		mu                  sync.Mutex
		preflightStarts     []string
		preflightPrepares   []string
		firstWorkerStarts   []string
		firstWorkerPrepares []string
		workerEnteredCount  int
	)

	onPrepareHook = func(inv tools.Invocation) {
		mu.Lock()
		defer mu.Unlock()
		preflightPrepares = append(preflightPrepares, inv.ID)
	}
	defer func() { onPrepareHook = nil }()

	gate := make(chan struct{})
	recordWorkerEntry := func() {
		mu.Lock()
		if workerEnteredCount == 0 {
			firstWorkerStarts = append([]string(nil), preflightStarts...)
			firstWorkerPrepares = append([]string(nil), preflightPrepares...)
		}
		workerEnteredCount++
		mu.Unlock()
		<-gate
	}

	reg.Read.AfterOpen = func(path string) {
		recordWorkerEntry()
	}
	reg.Write.WriteFile = func(path string, data []byte, perm os.FileMode) error {
		recordWorkerEntry()
		return os.WriteFile(path, data, perm)
	}

	batchCalls := []provider.Block{
		readCall("r1", "f1.txt"),
		readCall("r2", "f2.txt"),
		writeCall("w1", "f3.txt", "content3"),
	}

	fake := &provider.Fake{Responses: []*provider.Response{
		batchResponse(batchCalls...),
		{Text: "done", StopReason: "stop"},
	}}

	loop := New(store, reg, fake, func(e event.Event) {
		if e.Type == event.ToolStart {
			mu.Lock()
			preflightStarts = append(preflightStarts, e.ToolCallID)
			mu.Unlock()
		}
	})

	done := make(chan error, 1)
	go func() {
		done <- loop.Submit(context.Background(), "go", Config{Model: modelMeta(), Tools: toolDefs()})
	}()

	// Wait until at least one worker has entered its body
	for {
		mu.Lock()
		c := workerEnteredCount
		mu.Unlock()
		if c > 0 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	// Release workers
	close(gate)
	if err := <-done; err != nil {
		t.Fatalf("run failed: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()

	wantCalls := []string{"r1", "r2", "w1"}
	if len(firstWorkerStarts) != len(wantCalls) {
		t.Fatalf("at first worker entry, starts count = %d, want %d (%v)", len(firstWorkerStarts), len(wantCalls), firstWorkerStarts)
	}
	for i, want := range wantCalls {
		if firstWorkerStarts[i] != want {
			t.Errorf("firstWorkerStarts[%d] = %q, want %q", i, firstWorkerStarts[i], want)
		}
	}

	// Builtin calls r1, r2, w1 all call Prepare
	if len(firstWorkerPrepares) != len(wantCalls) {
		t.Fatalf("at first worker entry, prepares count = %d, want %d (%v)", len(firstWorkerPrepares), len(wantCalls), firstWorkerPrepares)
	}
	for i, want := range wantCalls {
		if firstWorkerPrepares[i] != want {
			t.Errorf("firstWorkerPrepares[%d] = %q, want %q", i, firstWorkerPrepares[i], want)
		}
	}
}

// TestParallelBatch_PanicRecoveryFormatting asserts that panic recovery formats
// error and string panic values directly without custom prefixes.
func TestParallelBatch_PanicRecoveryFormatting(t *testing.T) {
	defer leakcheck.Verify(t)
	ws := t.TempDir()
	store, err := session.New(session.OpenOptions{Cwd: ws, SessionDir: t.TempDir(), Persist: false})
	if err != nil {
		t.Fatal(err)
	}
	reg := tools.NewRegistry(ws)
	writeFileFor(t, ws, "err.txt", "err")
	writeFileFor(t, ws, "str.txt", "str")
	writeFileFor(t, ws, "ok.txt", "ok")

	reg.Read.AfterOpen = func(path string) {
		if strings.Contains(path, "err.txt") {
			panic(errors.New("builtin error message"))
		}
		if strings.Contains(path, "str.txt") {
			panic("builtin string message")
		}
	}

	batchCalls := []provider.Block{
		readCall("r1", "err.txt"),
		readCall("r2", "str.txt"),
		{Type: "toolCall", ID: "i1", Name: "err_internal", Arguments: map[string]any{}},
		{Type: "toolCall", ID: "i2", Name: "str_internal", Arguments: map[string]any{}},
		readCall("r3", "ok.txt"),
	}

	fake := &provider.Fake{Responses: []*provider.Response{
		batchResponse(batchCalls...),
		{Text: "done after panics", StopReason: "stop"},
	}}

	cfg := Config{
		Model: modelMeta(), Tools: toolDefs(),
		InternalTools: map[string]InternalToolHandler{
			"err_internal": func(context.Context, tools.Invocation) tools.Result {
				panic(errors.New("internal error message"))
			},
			"str_internal": func(context.Context, tools.Invocation) tools.Result {
				panic("internal string message")
			},
		},
	}

	loop := New(store, reg, fake, func(event.Event) {})
	if err := loop.Submit(context.Background(), "go", cfg); err != nil {
		t.Fatalf("submit failed: %v", err)
	}

	proj, err := store.Projection()
	if err != nil {
		t.Fatal(err)
	}
	results := make(map[string]*session.ToolResultMessage)
	for _, m := range proj.Flat {
		if m.Role == "toolResult" && m.ToolResult != nil {
			results[m.ToolResult.ToolCallID] = m.ToolResult
		}
	}

	if len(results) != 5 {
		t.Fatalf("expected 5 tool results, got %d", len(results))
	}

	// Builtin panic formatting
	if res := results["r1"]; !res.IsError || res.Content[0].Text.Text != "builtin error message" {
		t.Errorf("r1 (builtin error panic) = %+v, want 'builtin error message'", res)
	}
	if res := results["r2"]; !res.IsError || res.Content[0].Text.Text != "builtin string message" {
		t.Errorf("r2 (builtin string panic) = %+v, want 'builtin string message'", res)
	}

	// Internal panic formatting
	if res := results["i1"]; !res.IsError || res.Content[0].Text.Text != "internal error message" {
		t.Errorf("i1 (internal error panic) = %+v, want 'internal error message'", res)
	}
	if res := results["i2"]; !res.IsError || res.Content[0].Text.Text != "internal string message" {
		t.Errorf("i2 (internal string panic) = %+v, want 'internal string message'", res)
	}

	// Successful call
	if res := results["r3"]; res.IsError {
		t.Errorf("r3 should have succeeded, got: %+v", res)
	}
}
