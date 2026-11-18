package mcp

import (
	"context"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"psi/internal/tools"
)

func TestCrossServerNameCollisionWithdrawsCapturedAndQueuedCalls(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		calls := 0
		handler := func(context.Context, *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
			calls++
			return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: "correct server"}}}, nil
		}
		makeServer := func(name, rawTool string) *sdk.Server {
			server := sdk.NewServer(&sdk.Implementation{Name: name, Version: "1"}, nil)
			server.AddTool(sdkTool(rawTool, rawTool, map[string]any{"type": "object"}), handler)
			server.AddTool(sdkTool("health", "Unambiguous sibling", map[string]any{"type": "object"}), handler)
			return server
		}
		manager := NewManager(Options{})
		t.Cleanup(func() { manager.Stop(); manager.Wait() })
		before := connectInMemoryServer(t, manager, "a", makeServer("a", "b__c"), time.Hour)
		original, ok := before.Find("mcp__a__b__c")
		if !ok {
			t.Fatal("initial declaration missing")
		}
		for i := 0; i < maxCallsPerManager; i++ {
			manager.callSlot <- struct{}{}
		}
		queued := make(chan tools.Result, 1)
		go func() { queued <- manager.Call(t.Context(), original, tools.Invocation{Name: original.Name}) }()
		synctest.Wait()
		if permits := len(manager.servers["a"].conn.permits); permits != 1 {
			t.Fatalf("queued call did not reach manager admission: server permits=%d", permits)
		}

		// Both server names are valid and have distinct normalized namespaces,
		// but the delimiter also occurs in tool names. Their model names collide.
		colliding := connectInMemoryServer(t, manager, "a__b", makeServer("a__b", "c"), time.Hour)
		if _, declared := colliding.Find(original.Name); declared || len(colliding.Tools) != 2 {
			t.Errorf("ambiguous names were declared or healthy siblings were lost: %+v", colliding.Tools)
		}
		for _, status := range colliding.Servers {
			if status.ErrorClass != "tool-name-collision" || status.Exposed != 1 || status.Discovered != 2 {
				t.Errorf("collision status = %+v", status)
			}
		}
		captured := make(chan tools.Result, 1)
		go func() { captured <- manager.Call(t.Context(), original, tools.Invocation{Name: original.Name}) }()
		synctest.Wait()
		for i := 0; i < maxCallsPerManager; i++ {
			<-manager.callSlot
		}
		for _, result := range []tools.Result{<-captured, <-queued} {
			if !result.IsError || len(result.Content) != 1 || !strings.Contains(result.Content[0].Text, "stale") {
				t.Fatalf("ambiguous captured/queued call remained executable: %+v", result)
			}
		}
		if calls != 0 {
			t.Fatalf("ambiguous routes dispatched %d calls", calls)
		}
		healthy, ok := colliding.Find("mcp__a__b__health")
		if !ok {
			t.Fatal("unambiguous sibling declaration missing")
		}
		if result := manager.Call(t.Context(), healthy, tools.Invocation{Name: healthy.Name}); result.IsError || calls != 1 {
			t.Fatalf("collision blocked a healthy sibling: %+v; calls=%d", result, calls)
		}

		// Hide the second route through accepted config. The first becomes
		// unambiguous again without replacing either connection or old snapshot.
		first, second := manager.servers["a"].cfg, manager.servers["a__b"].cfg
		second.Exposure = ExposureHidden
		manager.mu.Lock()
		manager.acceptConfigLocked(&Config{Servers: []Server{first, second}}, Input{}, nil)
		manager.publishLocked()
		manager.mu.Unlock()
		current := manager.Snapshot()
		recovered, found := current.Find(original.Name)
		if !found || len(current.Tools) != 2 || recovered.Server != "a" || recovered.Epoch != original.Epoch {
			t.Fatalf("unambiguous route did not recover on its original connection: %+v", current)
		}
		for _, status := range current.Servers {
			if status.ErrorClass != "" {
				t.Fatalf("resolved collision kept an error: %+v", status)
			}
		}
		if result := manager.Call(t.Context(), original, tools.Invocation{Name: original.Name}); result.IsError || calls != 2 {
			t.Fatalf("unambiguous route failed: %+v; calls=%d", result, calls)
		}
		if len(before.Tools) != 2 || before.Tools[0].Name != original.Name || before.Tools[0].Server != original.Server || before.Tools[0].Epoch != original.Epoch {
			t.Fatal("collision handling mutated the earlier request snapshot")
		}
	})
}
