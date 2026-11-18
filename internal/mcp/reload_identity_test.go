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

func TestManagerReloadSeparatesEnvironmentAndHeaders(t *testing.T) {
	for _, test := range []struct{ name, before, after string }{
		{name: "env-to-header", before: `"env":{"authorization":"synthetic-token"}`, after: `"headers":{"Authorization":"synthetic-token"}`},
		{name: "header-to-env", before: `"headers":{"Authorization":"synthetic-token"}`, after: `"env":{"authorization":"synthetic-token"}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			dir := t.TempDir()
			config := func(fields string) string {
				return `{"mcpServers":{"fixture":{"url":"http://example.test/mcp","exposure":"direct",` + fields + `}}}`
			}
			path := writeTestFile(t, dir, "mcp.json", config(test.before))
			cfg, err := LoadConfig(path, "", false)
			if err != nil || len(cfg.Servers) != 1 {
				t.Fatalf("initial config = %+v, error = %v", cfg, err)
			}
			synctest.Test(t, func(t *testing.T) {
				calls := 0
				server := sdk.NewServer(&sdk.Implementation{Name: "fixture", Version: "1"}, nil)
				server.AddTool(sdkTool("echo", "Echo", map[string]any{"type": "object"}), func(context.Context, *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
					calls++
					return &sdk.CallToolResult{}, nil
				})
				input := Input{UserPath: path, Cwd: dir}
				manager := NewManager(Options{Input: input})
				t.Cleanup(func() { manager.Stop(); manager.Wait() })
				snapshot := connectInMemoryServer(t, manager, "fixture", server, time.Minute)
				state := manager.servers["fixture"]
				state.cfg = cfg.Servers[0]
				state.resolved, state.resolvedValid = state.cfg, true
				old, ok := snapshot.Find("mcp__fixture__echo")
				if !ok {
					t.Fatal("initial echo declaration missing")
				}
				if result := manager.Call(t.Context(), old, tools.Invocation{Name: old.Name}); result.IsError {
					t.Fatalf("initial route failed: %+v", result)
				}
				// Hold replacement setup before transport creation. The assertion is
				// withdrawal at config acceptance, independent of network readiness.
				for i := 0; i < maxSetupWorkers; i++ {
					manager.setupSlot <- struct{}{}
				}
				writeTestFile(t, dir, "mcp.json", config(test.after))
				manager.applyReload(reloadRequest{input: input})
				synctest.Wait()
				current := manager.Snapshot()
				if len(current.Tools) != 0 || current.Servers[0].State != serverStateConnecting || state.epoch == old.Epoch {
					t.Fatalf("changed credential source kept the obsolete connection: %+v", current)
				}
				if result := manager.Call(t.Context(), old, tools.Invocation{Name: old.Name}); !result.IsError || !strings.Contains(result.Content[0].Text, "stale") {
					t.Fatalf("obsolete credential route remained executable: %+v", result)
				}
				if calls != 1 {
					t.Fatalf("obsolete server calls = %d, want only the initial probe", calls)
				}
				if original, ok := snapshot.Find(old.Name); !ok || original.Epoch != old.Epoch {
					t.Fatal("reload mutated the earlier request snapshot")
				}
			})
		})
	}
}
