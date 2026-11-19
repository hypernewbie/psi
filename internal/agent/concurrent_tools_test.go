package agent

import (
	"context"
	"encoding/json"
	"errors"
	"sync/atomic"
	"testing"
	"testing/synctest"

	"psi/internal/event"
	"psi/internal/provider"
	"psi/internal/tools"
)

func externalToolDef(name string) provider.ToolDef {
	return provider.ToolDef{Name: name, Description: "external", Schema: map[string]any{"type": "object", "properties": map[string]any{}}}
}

// TestConcurrentExternalToolsOverlap: two enabled external tools reach their
// handlers before either is released, so they use the parallel worker path,
// and both results settle durably.
func TestConcurrentExternalToolsOverlap(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		first, second := "mcp__fixture__echo", "mcp__fixture__ping"
		response := batchResponse(
			provider.Block{Type: "toolCall", ID: "e1", Name: first, Arguments: map[string]any{}},
			provider.Block{Type: "toolCall", ID: "e2", Name: second, Arguments: map[string]any{}},
		)
		h := newParallelHarness(t, []*provider.Response{response, {Text: "done", StopReason: "stop"}})
		cfg := h.config()
		cfg.Tools = append(cfg.Tools, externalToolDef(first), externalToolDef(second))
		entered := make(chan struct{}, 2)
		release := make(chan struct{})
		var lookups atomic.Int32
		cfg.ConcurrentTools = func(name string) (InternalToolHandler, bool) {
			lookups.Add(1)
			if name != first && name != second {
				return nil, false
			}
			return func(_ context.Context, invocation tools.Invocation) tools.Result {
				entered <- struct{}{}
				<-release
				return tools.Result{Content: []tools.Block{{Type: "text", Text: invocation.ID}}}
			}, true
		}
		done := make(chan error, 1)
		go func() { done <- h.loop.Submit(context.Background(), "go", cfg) }()

		synctest.Wait()
		if got := len(entered); got != 2 {
			t.Fatalf("external handlers entered before release = %d, want 2", got)
		}
		close(release)
		if err := <-done; err != nil {
			t.Fatal(err)
		}
		if lookups.Load() != 2 {
			t.Fatalf("lookup count = %d, want 2", lookups.Load())
		}

		projection, err := h.store.Projection()
		if err != nil {
			t.Fatal(err)
		}
		results := map[string]string{}
		for _, message := range projection.Flat {
			if message.Role != "toolResult" || message.ToolResult == nil {
				continue
			}
			results[message.ToolResult.ToolCallID] = message.ToolResult.Content[0].Text.Text
		}
		if results["e1"] != "e1" || results["e2"] != "e2" {
			t.Fatalf("persisted results = %v, want e1 and e2", results)
		}
	})
}

// TestConcurrentExternalToolCancelledBeforeDispatchDoesNotRun: a batch that is
// already cancelled must not invoke an external handler. The call still settles
// in assistant order as not started.
func TestConcurrentExternalToolCancelledBeforeDispatchDoesNotRun(t *testing.T) {
	external := "mcp__fixture__echo"
	h := newParallelHarness(t, nil)
	cfg := h.config()
	cfg.Tools = append(cfg.Tools, externalToolDef(external))
	var calls atomic.Int32
	cfg.ConcurrentTools = func(name string) (InternalToolHandler, bool) {
		if name != external {
			return nil, false
		}
		return func(context.Context, tools.Invocation) tools.Result {
			calls.Add(1)
			return tools.Result{Content: []tools.Block{{Type: "text", Text: "external handler ran"}}}
		}, true
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := h.loop.runBatch(ctx, []provider.Block{
		{Type: "toolCall", ID: "e1", Name: external, Arguments: map[string]any{}},
	}, cfg)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("runBatch error = %v, want context.Canceled", err)
	}
	if n := calls.Load(); n != 0 {
		t.Fatalf("external handler calls = %d, want 0", n)
	}

	projection, err := h.store.Projection()
	if err != nil {
		t.Fatal(err)
	}
	for _, message := range projection.Flat {
		if message.Role != "toolResult" || message.ToolResult == nil || message.ToolResult.ToolCallID != "e1" {
			continue
		}
		var details map[string]json.RawMessage
		if err := json.Unmarshal(message.ToolResult.Details, &details); err != nil {
			t.Fatalf("details = %q: %v", message.ToolResult.Details, err)
		}
		if got := string(details["psiOutcome"]); got != `"not_started"` {
			t.Fatalf("psiOutcome = %s, want \"not_started\"", got)
		}
		return
	}
	t.Fatal("cancelled external call was not persisted")
}

func TestConcurrentExternalToolDisabledNameNeverLooksUp(t *testing.T) {
	h := newParallelHarness(t, []*provider.Response{
		batchResponse(provider.Block{Type: "toolCall", ID: "e1", Name: "mcp__fixture__echo", Arguments: map[string]any{}}),
		{Text: "done", StopReason: "stop"},
	})
	cfg := h.config()
	var lookedUp atomic.Bool
	cfg.ConcurrentTools = func(string) (InternalToolHandler, bool) {
		lookedUp.Store(true)
		return func(context.Context, tools.Invocation) tools.Result { return tools.Result{} }, true
	}
	if err := h.loop.Submit(context.Background(), "go", cfg); err != nil {
		t.Fatal(err)
	}
	if lookedUp.Load() {
		t.Fatal("disabled name reached the external lookup")
	}
	projection, err := h.store.Projection()
	if err != nil {
		t.Fatal(err)
	}
	for _, message := range projection.Flat {
		if message.Role == "toolResult" && message.ToolResult != nil && message.ToolResult.ToolCallID == "e1" {
			if !message.ToolResult.IsError || message.ToolResult.Content[0].Text.Text != "Tool is not enabled in this session" {
				t.Fatalf("disabled result = %+v", message.ToolResult)
			}
			return
		}
	}
	t.Fatal("disabled result missing")
}

// TestBatchCoordinateSettlesOutOfOrderCompletionsInAssistantOrder verifies that
// the coordinator persists results and emits events in call order even when
// completion events arrive in the opposite order.
func TestBatchCoordinateSettlesOutOfOrderCompletionsInAssistantOrder(t *testing.T) {
	h := newParallelHarness(t, nil)
	plans := []*batchPlan{
		{invocation: tools.Invocation{ID: "e1", Name: "mcp__fixture__gated"}},
		{invocation: tools.Invocation{ID: "e2", Name: "mcp__fixture__gated"}},
	}
	completions := make(chan batchCompletion, len(plans))
	completions <- batchCompletion{
		index: 1, started: true,
		result: tools.Result{Content: []tools.Block{{Type: "text", Text: "e2"}}},
	}
	completions <- batchCompletion{
		index: 0, started: true,
		result: tools.Result{Content: []tools.Block{{Type: "text", Text: "e1"}}},
	}

	b := &batch{
		loop:        h.loop,
		plans:       plans,
		completions: completions,
		outstanding: len(plans),
	}
	if err := b.coordinate(context.Background(), func() {}); err != nil {
		t.Fatalf("coordinate failed: %v", err)
	}

	ends := h.eventsOf(event.ToolEnd)
	if len(ends) != len(plans) || ends[0].ToolCallID != "e1" || ends[1].ToolCallID != "e2" {
		t.Fatalf("ToolEnd order = %+v, want [e1 e2]", ends)
	}

	projection, err := h.store.Projection()
	if err != nil {
		t.Fatal(err)
	}
	var storedIDs, storedText []string
	for _, message := range projection.Flat {
		if message.Role == "toolResult" && message.ToolResult != nil {
			storedIDs = append(storedIDs, message.ToolResult.ToolCallID)
			if len(message.ToolResult.Content) == 0 || message.ToolResult.Content[0].Text == nil {
				storedText = append(storedText, "")
			} else {
				storedText = append(storedText, message.ToolResult.Content[0].Text.Text)
			}
		}
	}
	if len(storedIDs) != len(plans) || storedIDs[0] != "e1" || storedIDs[1] != "e2" {
		t.Fatalf("stored result order = %v, want [e1 e2]", storedIDs)
	}
	if len(storedText) != len(plans) || storedText[0] != "e1" || storedText[1] != "e2" {
		t.Fatalf("stored result contents = %v, want [e1 e2]", storedText)
	}
}
