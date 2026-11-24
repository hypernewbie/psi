package mcp

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/hypernewbie/psi/internal/tools"
)

func TestFailedRefreshNotificationsRemainRateLimited(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		server := sdk.NewServer(&sdk.Implementation{Name: "fixture", Version: "1"}, nil)
		echo := func(context.Context, *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
			return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: "usable"}}}, nil
		}
		server.AddTool(sdkTool("echo", "Echo", map[string]any{"type": "object"}), echo)
		attempts := make(chan time.Time, 4)
		lists := 0
		server.AddReceivingMiddleware(func(next sdk.MethodHandler) sdk.MethodHandler {
			return func(ctx context.Context, method string, req sdk.Request) (sdk.Result, error) {
				if method == "tools/list" {
					lists++
					attempts <- time.Now()
					if lists == 2 {
						return nil, errors.New("refresh rejected")
					}
				}
				return next(ctx, method, req)
			}
		})
		manager := NewManager(Options{})
		t.Cleanup(func() { manager.Stop(); manager.Wait() })
		snapshot := connectInMemoryServer(t, manager, "fixture", server, time.Minute)
		<-attempts // Initial discovery is outside the notification rate limit.
		conn := manager.servers["fixture"].conn
		done := make(chan struct{})
		go func() { conn.runListChanges(); close(done) }()
		defer func() { conn.cancel(); <-done }()

		server.AddTool(sdkTool("first", "First change", map[string]any{"type": "object"}), echo)
		time.Sleep(time.Second) // Fake time includes the SDK's notification debounce.
		synctest.Wait()
		failedAt := <-attempts
		if status := manager.Snapshot().Servers[0]; status.RefreshWarning == "" || status.State != serverStateReady {
			t.Fatalf("failed refresh did not retain the ready catalog with a warning: %+v", status)
		}
		old, ok := snapshot.Find("mcp__fixture__echo")
		if !ok {
			t.Fatal("initial echo declaration missing")
		}
		if result := manager.Call(t.Context(), old, tools.Invocation{Name: old.Name}); result.IsError {
			t.Fatalf("old catalog stopped working after refresh failure: %+v", result)
		}

		// These real SDK notifications arrive after a failed refresh. They must
		// coalesce behind its rate limit, not trigger immediate extra discovery.
		// The SDK debounces server catalog notifications by this interval.
		const sdkNotificationDelay = 10 * time.Millisecond
		for _, name := range []string{"second", "latest"} {
			server.AddTool(sdkTool(name, name, map[string]any{"type": "object"}), echo)
			time.Sleep(sdkNotificationDelay)
			synctest.Wait()
			select {
			case at := <-attempts:
				t.Fatalf("failed refresh bypassed the one-second rate limit: next attempt after %s", at.Sub(failedAt))
			default:
			}
		}
		time.Sleep(time.Second - 2*sdkNotificationDelay)
		synctest.Wait()
		if at := <-attempts; at.Sub(failedAt) != time.Second {
			t.Fatalf("refresh spacing = %s, want one fake second", at.Sub(failedAt))
		}
		current := manager.Snapshot()
		if _, ok := current.Find("mcp__fixture__latest"); !ok || current.Servers[0].RefreshWarning != "" {
			t.Fatalf("successful refresh did not publish the latest catalog and clear the warning: %+v", current)
		}
		if _, ok := snapshot.Find("mcp__fixture__latest"); ok {
			t.Fatal("refresh mutated the earlier request snapshot")
		}
	})
}
