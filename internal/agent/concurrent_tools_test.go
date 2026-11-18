package agent

import (
	"context"
	"sync/atomic"
	"testing"

	"psi/internal/event"
	"psi/internal/provider"
	"psi/internal/tools"
)

func externalToolDef(name string) provider.ToolDef {
	return provider.ToolDef{Name: name, Description: "external", Schema: map[string]any{"type": "object", "properties": map[string]any{}}}
}

// TestConcurrentExternalToolUsesParallelPath: an enabled external tool runs
// through the same worker/completion path as built-ins, and an undeclared name
// never reaches the lookup.
func TestConcurrentExternalToolUsesParallelPath(t *testing.T) {
	external := "mcp__fixture__echo"
	response := batchResponse(provider.Block{Type: "toolCall", ID: "e1", Name: external, Arguments: map[string]any{"text": "hi"}})
	h := newParallelHarness(t, []*provider.Response{response, {Text: "done", StopReason: "stop"}})
	cfg := h.config()
	cfg.Tools = append(cfg.Tools, externalToolDef(external))
	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	var lookups atomic.Int32
	cfg.ConcurrentTools = func(name string) (InternalToolHandler, bool) {
		lookups.Add(1)
		if name != external {
			return nil, false
		}
		return func(ctx context.Context, invocation tools.Invocation) tools.Result {
			entered <- struct{}{}
			select {
			case <-release:
			case <-ctx.Done():
				return tools.Result{IsError: true}
			}
			return tools.Result{Content: []tools.Block{{Type: "text", Text: "external result"}}}
		}, true
	}
	done := make(chan error, 1)
	go func() { done <- h.loop.Submit(context.Background(), "go", cfg) }()
	<-entered
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if lookups.Load() != 1 {
		t.Fatalf("lookup count = %d", lookups.Load())
	}
	projection, err := h.store.Projection()
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, message := range projection.Flat {
		if message.Role == "toolResult" && message.ToolResult != nil && message.ToolResult.ToolCallID == "e1" {
			found = true
			if message.ToolResult.Content[0].Text.Text != "external result" {
				t.Errorf("result = %+v", message.ToolResult.Content)
			}
		}
	}
	if !found {
		t.Fatal("external result was not persisted")
	}
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
