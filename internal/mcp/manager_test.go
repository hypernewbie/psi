package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"psi/internal/tools"
)

func TestMCPHelperProcess(t *testing.T) {
	if os.Getenv("PSI_MCP_HELPER") != "1" {
		return
	}
	if marker := os.Getenv("PSI_MCP_MARKER"); marker != "" {
		_ = os.WriteFile(marker, []byte("started"), 0o600)
	}
	if delay := os.Getenv("PSI_MCP_DELAY_MS"); delay != "" {
		if ms, err := time.ParseDuration(delay + "ms"); err == nil {
			time.Sleep(ms)
		}
	}
	server := sdk.NewServer(&sdk.Implementation{Name: "fixture", Version: "1"}, &sdk.ServerOptions{PageSize: envInt("PSI_MCP_PAGE_SIZE")})
	server.AddTool(&sdk.Tool{
		Name:        "echo",
		Description: "Echo text",
		InputSchema: map[string]any{"type": "object", "properties": map[string]any{"text": map[string]any{"type": "string"}}},
	}, func(_ context.Context, req *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
		var args struct {
			Text string `json:"text"`
		}
		if err := json.Unmarshal(req.Params.Arguments, &args); err != nil {
			return nil, err
		}
		return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: args.Text}}}, nil
	})
	server.AddTool(&sdk.Tool{
		Name:        "hidden",
		Description: "Hidden tool",
		InputSchema: map[string]any{"type": "object"},
	}, func(context.Context, *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
		return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: "hidden"}}}, nil
	})
	server.AddTool(&sdk.Tool{Name: "progress", Description: "Progress", InputSchema: map[string]any{"type": "object"}}, func(ctx context.Context, req *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
		token := req.Params.GetProgressToken()
		for i := 0; i < 10; i++ {
			if err := req.Session.NotifyProgress(ctx, &sdk.ProgressNotificationParams{ProgressToken: token, Progress: float64(i)}); err != nil {
				return nil, err
			}
			time.Sleep(50 * time.Millisecond)
		}
		return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: "progress done"}}}, nil
	})
	server.AddTool(&sdk.Tool{Name: "hang", Description: "Hang", InputSchema: map[string]any{"type": "object"}}, func(ctx context.Context, _ *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(time.Second):
		}
		return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: "hang done"}}}, nil
	})
	server.AddTool(&sdk.Tool{Name: "crash", Description: "Crash", InputSchema: map[string]any{"type": "object"}}, func(context.Context, *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
		os.Exit(0)
		return nil, nil
	})
	server.AddTool(&sdk.Tool{Name: "slow", Description: "Slow", InputSchema: map[string]any{"type": "object"}}, func(ctx context.Context, _ *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
		delay := time.Duration(envInt("PSI_MCP_SLOW_MS")) * time.Millisecond
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(delay):
		}
		return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: "slow done"}}}, nil
	})
	for i := 0; i < envInt("PSI_MCP_TOOLS"); i++ {
		name := "bulk_" + strconv.Itoa(i)
		server.AddTool(&sdk.Tool{Name: name, Description: name, InputSchema: map[string]any{"type": "object"}}, func(context.Context, *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
			return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: "bulk"}}}, nil
		})
	}
	if os.Getenv("PSI_MCP_DYNAMIC") == "1" {
		server.AddTool(&sdk.Tool{Name: "refresh", Description: "Refresh tools", InputSchema: map[string]any{"type": "object"}}, func(context.Context, *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
			server.AddTool(&sdk.Tool{Name: "dynamic_tool", Description: "Added later", InputSchema: map[string]any{"type": "object"}}, func(context.Context, *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
				return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: "dynamic"}}}, nil
			})
			return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: "added"}}}, nil
		})
	}
	if os.Getenv("PSI_MCP_BAD_OUTPUT") == "1" {
		fmt.Fprintln(os.Stdout, "not-json")
	}
	if os.Getenv("PSI_MCP_STDERR_FLOOD") == "1" {
		_, _ = os.Stderr.Write(bytes.Repeat([]byte("x"), 200<<10))
	}
	if err := server.Run(context.Background(), &sdk.StdioTransport{}); err != nil {
		os.Exit(1)
	}
	os.Exit(0)
}

func testServerConfig(name, fixture, marker string) string {
	env := `"PSI_MCP_HELPER": "1", "PSI_MCP_FIXTURE": "` + fixture + `"`
	if marker != "" {
		env += `, "PSI_MCP_MARKER": "` + marker + `"`
	}
	return `"` + name + `": {"command": ` + jsonQuote(os.Args[0]) + `, "args": ["-test.run=^TestMCPHelperProcess$"], "env": {` + env + `}, "timeout": 5}`
}

func jsonQuote(value string) string {
	data, _ := json.Marshal(value)
	return string(data)
}

func envInt(name string) int {
	value, _ := strconv.Atoi(os.Getenv(name))
	return value
}

func waitServerState(t *testing.T, manager *Manager, name, want string) *Snapshot {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		snapshot := manager.Snapshot()
		for _, server := range snapshot.Servers {
			if server.Name != name {
				continue
			}
			if server.State == want {
				return snapshot
			}
			if server.State == serverStateFailed && want != serverStateFailed {
				t.Fatalf("server %s failed early: %+v", name, server)
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("server %s never reached %s: %+v", name, want, manager.Snapshot().Servers)
	return nil
}

func TestManagerStdioEndToEnd(t *testing.T) {
	dir := t.TempDir()
	user := writeTestFile(t, dir, "mcp.json", `{"mcpServers": {`+testServerConfig("fixture", "basic", "")+`}}`)
	manager := NewManager(Options{Input: Input{UserPath: user, Cwd: dir, ReadAvailable: true}, Version: "test"})
	manager.Start()
	t.Cleanup(func() {
		manager.Stop()
		manager.Wait()
	})
	snapshot := waitServerState(t, manager, "fixture", serverStateReady)
	tool, ok := snapshot.Find("mcp__fixture__echo")
	if !ok {
		t.Fatalf("declared tool missing: %+v", snapshot.Tools)
	}
	result := manager.Call(context.Background(), tool, tools.Invocation{Name: tool.Name, Arguments: map[string]any{"text": "hello"}})
	if result.IsError || len(result.Content) != 1 || result.Content[0].Text != "hello" {
		t.Fatalf("call result = %+v", result)
	}
}

func TestManagerSnapshotDoesNotBlockSetup(t *testing.T) {
	dir := t.TempDir()
	config := `{"mcpServers": {"fixture": {"command": ` + jsonQuote(os.Args[0]) + `, "args": ["-test.run=^TestMCPHelperProcess$"], "env": {"PSI_MCP_HELPER": "1", "PSI_MCP_DELAY_MS": "1500"}, "timeout": 5}}}`
	user := writeTestFile(t, dir, "mcp.json", config)
	manager := NewManager(Options{Input: Input{UserPath: user, Cwd: dir, ReadAvailable: true}})
	manager.Start()
	t.Cleanup(func() {
		manager.Stop()
		manager.Wait()
	})
	start := time.Now()
	snapshot := manager.Snapshot()
	if elapsed := time.Since(start); elapsed > 500*time.Millisecond {
		t.Fatalf("snapshot blocked for %v", elapsed)
	}
	if len(snapshot.Servers) != 0 && snapshot.Servers[0].State == serverStateReady {
		t.Fatal("gated server became ready before its gate")
	}
	waitServerState(t, manager, "fixture", serverStateReady)
}

func TestManagerStaleRouteRejectedAfterReplacement(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "mcp.json")
	write := func(label string) {
		content := `{"mcpServers": {"fixture": {"command": ` + jsonQuote(os.Args[0]) + `, "args": ["-test.run=^TestMCPHelperProcess$"], "env": {"PSI_MCP_HELPER": "1", "PSI_MCP_LABEL": ` + jsonQuote(label) + `}, "timeout": 5}}}`
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("one")
	manager := NewManager(Options{Input: Input{UserPath: path, Cwd: dir, ReadAvailable: true}})
	manager.Start()
	t.Cleanup(func() {
		manager.Stop()
		manager.Wait()
	})
	snapshot := waitServerState(t, manager, "fixture", serverStateReady)
	old, ok := snapshot.Find("mcp__fixture__echo")
	if !ok {
		t.Fatal("tool missing")
	}
	write("two")
	manager.Reload(Input{UserPath: path, Cwd: dir, ReadAvailable: true})
	deadline := time.Now().Add(10 * time.Second)
	var replacement Tool
	for time.Now().Before(deadline) {
		snapshot = manager.Snapshot()
		if tool, ok := snapshot.Find("mcp__fixture__echo"); ok && tool.Epoch != old.Epoch {
			replacement = tool
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if replacement.Name == "" {
		t.Fatal("replacement never published")
	}
	stale := manager.Call(context.Background(), old, tools.Invocation{Name: old.Name, Arguments: map[string]any{"text": "old"}})
	if !stale.IsError {
		t.Fatalf("stale route reached a server: %+v", stale)
	}
	fresh := manager.Call(context.Background(), replacement, tools.Invocation{Name: replacement.Name, Arguments: map[string]any{"text": "new"}})
	if fresh.IsError || fresh.Content[0].Text != "new" {
		t.Fatalf("replacement call = %+v", fresh)
	}
}

func TestManagerExposureAndDisabled(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "disabled-started")
	config := `{"mcpServers": {
		"fixture": {"command": ` + jsonQuote(os.Args[0]) + `, "args": ["-test.run=^TestMCPHelperProcess$"], "env": {"PSI_MCP_HELPER": "1", "PSI_MCP_FIXTURE": "basic"}, "timeout": 5, "toolExposure": {"hidden": "hidden"}},
		"disabled": {"command": ` + jsonQuote(os.Args[0]) + `, "args": ["-test.run=^TestMCPHelperProcess$"], "env": {"PSI_MCP_HELPER": "1", "PSI_MCP_FIXTURE": "basic", "PSI_MCP_MARKER": ` + jsonQuote(marker) + `}, "enabled": false}
	}}`
	user := writeTestFile(t, dir, "mcp.json", config)
	manager := NewManager(Options{Input: Input{UserPath: user, Cwd: dir, ReadAvailable: true}})
	manager.Start()
	t.Cleanup(func() {
		manager.Stop()
		manager.Wait()
	})
	snapshot := waitServerState(t, manager, "fixture", serverStateReady)
	if _, ok := snapshot.Find("mcp__fixture__hidden"); ok {
		t.Error("hidden tool was declared")
	}
	if _, ok := snapshot.Find("mcp__disabled__echo"); ok {
		t.Error("disabled server tool was declared")
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Errorf("disabled server started its child: %v", err)
	}
}

func TestManagerPaginationAndListChanged(t *testing.T) {
	dir := t.TempDir()
	config := `{"mcpServers": {"fixture": {"command": ` + jsonQuote(os.Args[0]) + `, "args": ["-test.run=^TestMCPHelperProcess$"], "env": {"PSI_MCP_HELPER": "1", "PSI_MCP_TOOLS": "5", "PSI_MCP_PAGE_SIZE": "2", "PSI_MCP_DYNAMIC": "1"}, "timeout": 5, "exposure": "direct"}}}`
	user := writeTestFile(t, dir, "mcp.json", config)
	manager := NewManager(Options{Input: Input{UserPath: user, Cwd: dir, ReadAvailable: true}})
	manager.Start()
	t.Cleanup(func() {
		manager.Stop()
		manager.Wait()
	})
	snapshot := waitServerState(t, manager, "fixture", serverStateReady)
	if len(snapshot.Tools) != 12 {
		t.Fatalf("paged tools = %d, want 12", len(snapshot.Tools))
	}
	refresh, ok := snapshot.Find("mcp__fixture__refresh")
	if !ok {
		t.Fatal("refresh tool missing")
	}
	result := manager.Call(context.Background(), refresh, tools.Invocation{Name: refresh.Name})
	if result.IsError {
		t.Fatalf("refresh call = %+v", result)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, ok := manager.Snapshot().Find("mcp__fixture__dynamic_tool"); ok {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("tool-list-changed notification did not publish the added tool")
}

func TestManagerBadServerDoesNotBlockSibling(t *testing.T) {
	dir := t.TempDir()
	good := `"good": {"command": ` + jsonQuote(os.Args[0]) + `, "args": ["-test.run=^TestMCPHelperProcess$"], "env": {"PSI_MCP_HELPER": "1"}, "timeout": 5}`
	bad := `"bad": {"command": ` + jsonQuote(os.Args[0]) + `, "args": ["-test.run=^TestMCPHelperProcess$"], "env": {"PSI_MCP_HELPER": "1", "PSI_MCP_BAD_OUTPUT": "1"}, "timeout": 5}`
	user := writeTestFile(t, dir, "mcp.json", `{"mcpServers": {`+bad+`,`+good+`}}`)
	manager := NewManager(Options{Input: Input{UserPath: user, Cwd: dir, ReadAvailable: true}})
	manager.Start()
	t.Cleanup(func() {
		manager.Stop()
		manager.Wait()
	})
	snapshot := waitServerState(t, manager, "good", serverStateReady)
	if _, ok := snapshot.Find("mcp__good__echo"); !ok {
		t.Fatalf("healthy sibling missing: %+v", snapshot.Tools)
	}
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		for _, server := range manager.Snapshot().Servers {
			if server.Name == "bad" && server.State == serverStateFailed {
				return
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("malformed server never failed: %+v", manager.Snapshot().Servers)
}

func TestManagerReloadNullRootKeepsAcceptedConfig(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "mcp.json")
	writeTestFile(t, dir, "mcp.json", `{"mcpServers": {`+testServerConfig("fixture", "basic", "")+`}}`)
	manager := NewManager(Options{Input: Input{UserPath: path, Cwd: dir, ReadAvailable: true}})
	manager.Start()
	t.Cleanup(func() {
		manager.Stop()
		manager.Wait()
	})
	waitServerState(t, manager, "fixture", serverStateReady)
	if err := os.WriteFile(path, []byte(`{"mcpServers": null}`), 0o600); err != nil {
		t.Fatal(err)
	}
	manager.Reload(Input{UserPath: path, Cwd: dir, ReadAvailable: true})
	time.Sleep(300 * time.Millisecond)
	snapshot := manager.Snapshot()
	if _, ok := snapshot.Find("mcp__fixture__echo"); !ok {
		t.Fatalf("malformed reload withdrew the accepted catalog: %+v", snapshot.Servers)
	}
	if snapshot.ReloadError == "" {
		t.Fatal("malformed reload did not report an error")
	}
}

func TestManagerProgressRenewsIdleTimeout(t *testing.T) {
	dir := t.TempDir()
	config := `{"mcpServers": {"fixture": {"command": ` + jsonQuote(os.Args[0]) + `, "args": ["-test.run=^TestMCPHelperProcess$"], "env": {"PSI_MCP_HELPER": "1"}, "timeout": 0.2, "exposure": "direct"}}}`
	user := writeTestFile(t, dir, "mcp.json", config)
	manager := NewManager(Options{Input: Input{UserPath: user, Cwd: dir, ReadAvailable: true}})
	manager.Start()
	t.Cleanup(func() {
		manager.Stop()
		manager.Wait()
	})
	snapshot := waitServerState(t, manager, "fixture", serverStateReady)
	progress, ok := snapshot.Find("mcp__fixture__progress")
	if !ok {
		t.Fatal("progress tool missing")
	}
	result := manager.Call(context.Background(), progress, tools.Invocation{Name: progress.Name})
	if result.IsError {
		t.Fatalf("progress call timed out despite renewal: %+v", result)
	}
	hang, ok := snapshot.Find("mcp__fixture__hang")
	if !ok {
		t.Fatal("hang tool missing")
	}
	start := time.Now()
	result = manager.Call(context.Background(), hang, tools.Invocation{Name: hang.Name})
	if !result.IsError {
		t.Fatal("non-progressing call did not time out")
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("idle timeout took %v", elapsed)
	}
}

func TestManagerReconnectReusesResolvedCommand(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "interpolation-marker")
	config := `{"mcpServers": {"fixture": {"command": ` + jsonQuote(os.Args[0]) + `, "args": ["-test.run=^TestMCPHelperProcess$"], "env": {"PSI_MCP_HELPER": "1", "TOKEN": "!printf x >> ` + marker + `"}, "timeout": 5, "exposure": "direct"}}}`
	user := writeTestFile(t, dir, "mcp.json", config)
	manager := NewManager(Options{Input: Input{UserPath: user, Cwd: dir, ReadAvailable: true}})
	manager.Start()
	t.Cleanup(func() {
		manager.Stop()
		manager.Wait()
	})
	snapshot := waitServerState(t, manager, "fixture", serverStateReady)
	if data, _ := os.ReadFile(marker); string(data) != "x" {
		t.Fatalf("interpolation marker after setup = %q", data)
	}
	crash, ok := snapshot.Find("mcp__fixture__crash")
	if !ok {
		t.Fatal("crash tool missing")
	}
	_ = manager.Call(context.Background(), crash, tools.Invocation{Name: crash.Name})
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		snapshot = manager.Snapshot()
		if _, ok := snapshot.Find("mcp__fixture__echo"); ok {
			for _, status := range snapshot.Servers {
				if status.Name == "fixture" && status.State == serverStateReady {
					goto recovered
				}
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("connection did not recover after crash")
recovered:
	if data, _ := os.ReadFile(marker); string(data) != "x" {
		t.Fatalf("automatic reconnect re-ran !command: %q", data)
	}
	manager.Reload(Input{UserPath: user, Cwd: dir, ReadAvailable: true})
	deadline = time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if data, _ := os.ReadFile(marker); string(data) == "xx" {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	data, _ := os.ReadFile(marker)
	t.Fatalf("explicit reload did not re-run !command: %q", data)
}

func TestManagerStderrFloodDoesNotContaminateStdout(t *testing.T) {
	dir := t.TempDir()
	config := `{"mcpServers": {"fixture": {"command": ` + jsonQuote(os.Args[0]) + `, "args": ["-test.run=^TestMCPHelperProcess$"], "env": {"PSI_MCP_HELPER": "1", "PSI_MCP_STDERR_FLOOD": "1"}, "timeout": 5}}}`
	user := writeTestFile(t, dir, "mcp.json", config)
	manager := NewManager(Options{Input: Input{UserPath: user, Cwd: dir, ReadAvailable: true}})
	manager.Start()
	t.Cleanup(func() {
		manager.Stop()
		manager.Wait()
	})
	snapshot := waitServerState(t, manager, "fixture", serverStateReady)
	tool, ok := snapshot.Find("mcp__fixture__echo")
	if !ok {
		t.Fatal("echo tool missing after stderr flood")
	}
	result := manager.Call(context.Background(), tool, tools.Invocation{Name: tool.Name, Arguments: map[string]any{"text": "clean"}})
	if result.IsError || result.Content[0].Text != "clean" {
		t.Fatalf("stderr flood contaminated stdout: %+v", result)
	}
}

func TestManagerServerQueueDoesNotStarveAnotherServer(t *testing.T) {
	dir := t.TempDir()
	a := `"a": {"command": ` + jsonQuote(os.Args[0]) + `, "args": ["-test.run=^TestMCPHelperProcess$"], "env": {"PSI_MCP_HELPER": "1", "PSI_MCP_SLOW_MS": "400"}, "timeout": 5, "exposure": "direct"}`
	b := `"b": {"command": ` + jsonQuote(os.Args[0]) + `, "args": ["-test.run=^TestMCPHelperProcess$"], "env": {"PSI_MCP_HELPER": "1"}, "timeout": 5, "exposure": "direct"}`
	user := writeTestFile(t, dir, "mcp.json", `{"mcpServers": {`+a+`,`+b+`}}`)
	manager := NewManager(Options{Input: Input{UserPath: user, Cwd: dir, ReadAvailable: true}})
	manager.Start()
	t.Cleanup(func() {
		manager.Stop()
		manager.Wait()
	})
	snapshot := waitServerState(t, manager, "a", serverStateReady)
	waitServerState(t, manager, "b", serverStateReady)
	slow, ok := snapshot.Find("mcp__a__slow")
	if !ok {
		t.Fatal("slow tool missing")
	}
	echo, ok := snapshot.Find("mcp__b__echo")
	if !ok {
		t.Fatal("echo tool missing")
	}
	var wg sync.WaitGroup
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = manager.Call(context.Background(), slow, tools.Invocation{Name: slow.Name})
		}()
	}
	time.Sleep(50 * time.Millisecond)
	start := time.Now()
	result := manager.Call(context.Background(), echo, tools.Invocation{Name: echo.Name, Arguments: map[string]any{"text": "fast"}})
	if result.IsError || result.Content[0].Text != "fast" {
		t.Fatalf("other server call = %+v", result)
	}
	if elapsed := time.Since(start); elapsed > 300*time.Millisecond {
		t.Fatalf("other server call waited behind the slow queue: %v", elapsed)
	}
	wg.Wait()
}
