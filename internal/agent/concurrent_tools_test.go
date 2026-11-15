package agent

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

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
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("external handler did not start")
	}
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

func TestConcurrentExternalToolSettlesInAssistantOrder(t *testing.T) {
	external := "mcp__fixture__gated"
	response := batchResponse(
		provider.Block{Type: "toolCall", ID: "e1", Name: external, Arguments: map[string]any{"id": "1"}},
		provider.Block{Type: "toolCall", ID: "e2", Name: external, Arguments: map[string]any{"id": "2"}},
	)
	h := newParallelHarness(t, []*provider.Response{response, {Text: "done", StopReason: "stop"}})
	cfg := h.config()
	cfg.Tools = append(cfg.Tools, externalToolDef(external))
	entered := map[string]chan struct{}{"1": make(chan struct{}), "2": make(chan struct{})}
	release := map[string]chan struct{}{"1": make(chan struct{}), "2": make(chan struct{})}
	cfg.ConcurrentTools = func(name string) (InternalToolHandler, bool) {
		if name != external {
			return nil, false
		}
		return func(ctx context.Context, invocation tools.Invocation) tools.Result {
			id, _ := invocation.Arguments["id"].(string)
			close(entered[id])
			select {
			case <-release[id]:
			case <-ctx.Done():
				return tools.Result{IsError: true}
			}
			return tools.Result{Content: []tools.Block{{Type: "text", Text: id}}}
		}, true
	}
	done := make(chan error, 1)
	go func() { done <- h.loop.Submit(context.Background(), "go", cfg) }()
	<-entered["1"]
	<-entered["2"]
	close(release["2"])
	time.Sleep(50 * time.Millisecond)
	if ends := h.eventsOf(event.ToolEnd); len(ends) != 0 {
		t.Fatalf("settlement crossed the barrier: %+v", ends)
	}
	close(release["1"])
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	ends := h.eventsOf(event.ToolEnd)
	if len(ends) != 2 || ends[0].ToolCallID != "e1" || ends[1].ToolCallID != "e2" {
		t.Fatalf("event order = %+v", ends)
	}
	proj, err := h.store.Projection()
	if err != nil {
		t.Fatal(err)
	}
	var stored []string
	for _, message := range proj.Flat {
		if message.Role == "toolResult" && message.ToolResult != nil {
			stored = append(stored, message.ToolResult.ToolCallID)
		}
	}
	if len(stored) != 2 || stored[0] != "e1" || stored[1] != "e2" {
		t.Fatalf("stored order = %v", stored)
	}
}
