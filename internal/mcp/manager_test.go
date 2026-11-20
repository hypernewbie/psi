package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/hypernewbie/psi/internal/tools"
)

func TestMCPHelperProcess(t *testing.T) {
	if os.Getenv("PSI_MCP_HELPER") != "1" {
		return
	}
	if marker := os.Getenv("PSI_MCP_MARKER"); marker != "" {
		_ = os.WriteFile(marker, []byte("started"), 0o600)
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
	server.AddTool(&sdk.Tool{Name: "crash", Description: "Crash", InputSchema: map[string]any{"type": "object"}}, func(context.Context, *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
		os.Exit(0)
		return nil, nil
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
	env := `"PSI_MCP_HELPER": "1", "PSI_MCP_FIXTURE": ` + jsonQuote(fixture)
	if marker != "" {
		// The marker is a file path, so it must be JSON-quoted. A raw
		// Windows path would be an invalid JSON escape.
		env += `, "PSI_MCP_MARKER": ` + jsonQuote(marker)
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

// connectInMemoryServer exercises SDK calls and notifications without OS I/O,
// so synctest can control deadlines and wait for queued calls deterministically.
func connectInMemoryServer(t *testing.T, manager *Manager, name string, server *sdk.Server, timeout time.Duration) *Snapshot {
	t.Helper()
	ctx, cancel := context.WithCancel(manager.ctx)
	conn := &connection{manager: manager, server: name, epoch: 1, ctx: ctx, cancel: cancel,
		permits: make(chan struct{}, maxCallsPerServer), tokens: map[string]*callState{}, listCh: make(chan struct{}, 1)}
	clientTransport, serverTransport := sdk.NewInMemoryTransports()
	serverSession, err := server.Connect(t.Context(), serverTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = serverSession.Close() })
	client := sdk.NewClient(&sdk.Implementation{Name: "test", Version: "1"}, &sdk.ClientOptions{
		ToolListChangedHandler: func(context.Context, *sdk.ToolListChangedRequest) {
			conn.signalListChanged()
		},
		ProgressNotificationHandler: func(_ context.Context, req *sdk.ProgressNotificationClientRequest) {
			conn.noteProgress(req.Params.ProgressToken, req.Params.Progress)
		},
	})
	conn.session, err = client.Connect(t.Context(), clientTransport, &sdk.ClientSessionOptions{ProtocolVersion: "2025-11-25"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.session.Close(); cancel() })
	raw, err := listAllTools(t.Context(), conn.session)
	if err != nil {
		t.Fatal(err)
	}
	conn.lastList = manager.opts.now()
	cfg := Server{Name: name, Enabled: true, Exposure: ExposureDirect, Timeout: timeout}
	conn.catalog, err = buildCatalog(cfg, conn.epoch, raw)
	if err != nil {
		t.Fatal(err)
	}
	manager.mu.Lock()
	manager.servers[name] = &serverState{cfg: cfg, epoch: conn.epoch, status: serverStateReady, conn: conn, catalog: conn.catalog}
	manager.order = append(manager.order, name)
	manager.publishLocked()
	manager.mu.Unlock()
	return manager.Snapshot()
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

func TestCallCancellationDoesNotWaitForUncooperativeServer(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	defer close(release)
	server := sdk.NewServer(&sdk.Implementation{Name: "fixture", Version: "1"}, nil)
	server.AddTool(sdkTool("hang", "Ignore cancellation", map[string]any{"type": "object"}), func(context.Context, *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
		close(entered)
		<-release
		return &sdk.CallToolResult{}, nil
	})
	server.AddTool(sdkTool("echo", "Still responsive", map[string]any{"type": "object"}), func(context.Context, *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
		return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: "responsive"}}}, nil
	})
	manager := NewManager(Options{})
	t.Cleanup(func() {
		manager.Stop()
		manager.Wait()
	})
	snapshot := connectInMemoryServer(t, manager, "fixture", server, time.Hour)
	hang, ok := snapshot.Find("mcp__fixture__hang")
	if !ok {
		t.Fatal("hanging tool missing")
	}
	echo, ok := snapshot.Find("mcp__fixture__echo")
	if !ok {
		t.Fatal("echo tool missing")
	}
	caller, cancelCall := context.WithCancel(context.Background())
	t.Cleanup(cancelCall)
	done := make(chan tools.Result, 1)
	go func() { done <- manager.Call(caller, hang, tools.Invocation{Name: hang.Name}) }()
	select {
	case <-entered:
	case <-time.After(10 * time.Second):
		t.Fatal("server never entered the hanging handler")
	}
	cancelCall()
	select {
	case result := <-done:
		if !result.IsError || len(result.Content) != 1 || !strings.Contains(result.Content[0].Text, "was cancelled") {
			t.Fatalf("cancelled call = %+v", result)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("call waited for the server to honor cancellation")
	}
	// The hanging handler remains gated. Another call must still succeed.
	probeCtx, cancelProbe := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancelProbe()
	result := manager.Call(probeCtx, echo, tools.Invocation{Name: echo.Name})
	if result.IsError || len(result.Content) != 1 || result.Content[0].Text != "responsive" {
		t.Fatalf("call after cancellation = %+v", result)
	}
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
	// echo, hidden, crash, refresh, and five bulk tools span five pages.
	const wantTools = 4 + 5
	if len(snapshot.Tools) != wantTools {
		t.Fatalf("paged tools = %d, want %d", len(snapshot.Tools), wantTools)
	}
	if _, ok := snapshot.Find("mcp__fixture__dynamic_tool"); ok {
		t.Fatal("dynamic tool was present before the server changed its catalog")
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
	deadline := time.Now().Add(5 * time.Second)
	var snapshot *Snapshot
	for time.Now().Before(deadline) {
		snapshot = manager.Snapshot()
		if snapshot.ReloadError != "" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, ok := snapshot.Find("mcp__fixture__echo"); !ok {
		t.Fatalf("malformed reload withdrew the accepted catalog: %+v", snapshot.Servers)
	}
	if snapshot.ReloadError == "" {
		t.Fatal("malformed reload did not report an error")
	}
}

func TestManagerProgressAndDeadlines(t *testing.T) {
	for _, test := range []struct {
		name      string
		progress  int
		wantError bool
		want      string
	}{
		{name: "renewed-idle", progress: 3, want: "progress done"},
		{name: "stalled", wantError: true, want: "timed out (waiting for progress)"},
		{name: "hard-limit", progress: 20, wantError: true, want: "timed out (hard deadline)"},
	} {
		t.Run(test.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				server := sdk.NewServer(&sdk.Implementation{Name: "fixture", Version: "1"}, nil)
				server.AddTool(sdkTool("progress", "Report progress", map[string]any{"type": "object"}), func(ctx context.Context, req *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
					for i := 0; i < test.progress; i++ {
						select {
						case <-ctx.Done():
							return nil, ctx.Err()
						case <-time.After(750 * time.Millisecond):
						}
						if err := req.Session.NotifyProgress(ctx, &sdk.ProgressNotificationParams{ProgressToken: req.Params.GetProgressToken(), Progress: float64(i)}); err != nil {
							return nil, err
						}
					}
					if test.progress == 0 {
						<-ctx.Done()
						return nil, ctx.Err()
					}
					return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: "progress done"}}}, nil
				})
				manager := NewManager(Options{})
				t.Cleanup(func() { manager.Stop(); manager.Wait() })
				// Fake time advances only when all SDK and manager goroutines are
				// blocked. Progress arrives before each one-second idle deadline;
				// it must not extend the ten-second hard deadline.
				snapshot := connectInMemoryServer(t, manager, "fixture", server, time.Second)
				tool, ok := snapshot.Find("mcp__fixture__progress")
				if !ok {
					t.Fatal("progress tool missing")
				}
				result := manager.Call(t.Context(), tool, tools.Invocation{Name: tool.Name})
				if result.IsError != test.wantError || len(result.Content) != 1 || !strings.Contains(result.Content[0].Text, test.want) {
					t.Fatalf("progress call = %+v; want %q", result, test.want)
				}
			})
		})
	}
}

func TestManagerReconnectReusesResolvedCommand(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "interpolation-marker")
	// The marker path reaches the interpolation shell as a redirect target,
	// so it needs forward slashes: a Windows backslash path is both an
	// invalid JSON escape and a shell escape. The whole value is quoted so
	// the JSON stays valid on every platform.
	interpolation := "!printf x >> " + filepath.ToSlash(marker)
	config := `{"mcpServers": {"fixture": {"command": ` + jsonQuote(os.Args[0]) + `, "args": ["-test.run=^TestMCPHelperProcess$"], "env": {"PSI_MCP_HELPER": "1", "TOKEN": ` + jsonQuote(interpolation) + `}, "timeout": 5, "exposure": "direct"}}}`
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
	result := manager.Call(context.Background(), crash, tools.Invocation{Name: crash.Name})
	if !result.IsError {
		t.Fatalf("crashed call reported success: %+v", result)
	}
	deadline := time.Now().Add(10 * time.Second)
	var replacement Tool
	for time.Now().Before(deadline) {
		snapshot = manager.Snapshot()
		if tool, ok := snapshot.Find("mcp__fixture__echo"); ok && tool.Epoch != crash.Epoch {
			replacement = tool
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if replacement.Name == "" {
		t.Fatal("connection did not recover with a new epoch after crash")
	}
	result = manager.Call(context.Background(), replacement, tools.Invocation{Name: replacement.Name, Arguments: map[string]any{"text": "recovered"}})
	if result.IsError || len(result.Content) != 1 || result.Content[0].Text != "recovered" {
		t.Fatalf("call after reconnect = %+v", result)
	}
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
	synctest.Test(t, func(t *testing.T) {
		entered, release := make(chan struct{}, maxCallsPerServer), make(chan struct{})
		defer close(release)
		busy := sdk.NewServer(&sdk.Implementation{Name: "busy", Version: "1"}, nil)
		busy.AddTool(sdkTool("wait", "Wait for release", map[string]any{"type": "object"}), func(ctx context.Context, _ *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
			entered <- struct{}{}
			select {
			case <-release:
			case <-ctx.Done():
			}
			return &sdk.CallToolResult{}, nil
		})
		responsive := sdk.NewServer(&sdk.Implementation{Name: "responsive", Version: "1"}, nil)
		responsive.AddTool(sdkTool("echo", "Echo", map[string]any{"type": "object"}), func(context.Context, *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
			return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: "responsive"}}}, nil
		})
		manager := NewManager(Options{})
		t.Cleanup(func() { manager.Stop(); manager.Wait() })
		connectInMemoryServer(t, manager, "busy", busy, time.Hour)
		snapshot := connectInMemoryServer(t, manager, "responsive", responsive, time.Hour)
		wait, ok := snapshot.Find("mcp__busy__wait")
		if !ok {
			t.Fatal("waiting tool missing")
		}
		echo, ok := snapshot.Find("mcp__responsive__echo")
		if !ok {
			t.Fatal("echo tool missing")
		}
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		// More queued work than the entire manager has capacity for must not
		// take global permits while waiting for the busy server's permits.
		const calls = maxCallsPerManager + 1
		done := make(chan tools.Result, calls)
		for i := 0; i < calls; i++ {
			go func() { done <- manager.Call(ctx, wait, tools.Invocation{Name: wait.Name}) }()
		}
		for i := 0; i < maxCallsPerServer; i++ {
			<-entered
		}
		synctest.Wait()
		result := manager.Call(t.Context(), echo, tools.Invocation{Name: echo.Name})
		if result.IsError || len(result.Content) != 1 || result.Content[0].Text != "responsive" {
			t.Fatalf("sibling call while the busy server remains gated = %+v", result)
		}
		cancel()
		for i := 0; i < calls; i++ {
			<-done
		}
	})
}

func TestManagerReloadAppliesExposureToLiveCalls(t *testing.T) {
	for _, policy := range []struct {
		name     string
		exposure Exposure
		perTool  bool
	}{
		{name: "server-hidden", exposure: ExposureHidden},
		{name: "tool-deferred", exposure: ExposureDeferred, perTool: true},
	} {
		for _, phase := range []string{"server", "manager"} {
			t.Run(policy.name+"/"+phase, func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					dir := t.TempDir()
					writeConfig := func(exposure Exposure) string {
						entry := map[string]any{"command": filepath.Join(dir, "must-not-start"), "timeout": time.Hour.Seconds(), "exposure": ExposureDirect}
						if policy.perTool {
							entry["toolExposure"] = map[string]Exposure{"echo": exposure}
						} else {
							entry["exposure"] = exposure
						}
						data, err := json.Marshal(map[string]any{"mcpServers": map[string]any{"fixture": entry}})
						if err != nil {
							t.Fatal(err)
						}
						return writeTestFile(t, dir, "mcp.json", string(data))
					}
					input := Input{UserPath: writeConfig(ExposureDirect), Cwd: dir}
					cfg, err := LoadConfig(input.UserPath, "", false)
					if err != nil || len(cfg.Servers) != 1 {
						t.Fatalf("initial config = %+v, %v", cfg, err)
					}
					var dispatched atomic.Int32
					server := sdk.NewServer(&sdk.Implementation{Name: "fixture", Version: "1"}, nil)
					server.AddTool(sdkTool("echo", "Count dispatches", map[string]any{"type": "object"}), func(context.Context, *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
						dispatched.Add(1)
						return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: "echo"}}}, nil
					})
					manager := NewManager(Options{Input: input})
					t.Cleanup(func() { manager.Stop(); manager.Wait() })
					before := connectInMemoryServer(t, manager, "fixture", server, time.Hour)
					tool, ok := before.Find("mcp__fixture__echo")
					if !ok {
						t.Fatal("echo tool missing")
					}
					// Install the accepted connection's file identity. Public reloads
					// must reuse this SDK session, never start the sentinel command.
					manager.servers["fixture"].cfg = cfg.Servers[0]
					conn := manager.servers["fixture"].conn
					manager.Start()
					synctest.Wait()
					gate := conn.permits
					if phase == "manager" {
						gate = manager.callSlot
					}
					for range cap(gate) {
						gate <- struct{}{}
					}
					releaseGate := sync.OnceFunc(func() {
						for range cap(gate) {
							<-gate
						}
					})
					defer releaseGate()
					done := make(chan tools.Result, 1)
					go func() { done <- manager.Call(t.Context(), tool, tools.Invocation{Name: tool.Name}) }()
					synctest.Wait()
					select {
					case result := <-done:
						t.Fatalf("call bypassed admission gate: %+v", result)
					default:
					}
					writeConfig(policy.exposure)
					manager.Reload(input)
					synctest.Wait()
					if _, ok := manager.Snapshot().Find(tool.Name); ok {
						t.Fatal("reload still declares the withdrawn tool")
					}
					if old, ok := before.Find(tool.Name); !ok || old.Exposure != ExposureDirect {
						t.Fatal("reload mutated the old request snapshot")
					}
					releaseGate()
					queued := <-done
					captured := manager.Call(t.Context(), tool, tools.Invocation{Name: tool.Name})
					for name, result := range map[string]tools.Result{"queued": queued, "captured": captured} {
						if !result.IsError || len(result.Content) != 1 || result.Content[0].Text != "MCP tool fixture/echo is unavailable (stale)" {
							t.Errorf("%s call after exposure withdrawal = %+v", name, result)
						}
					}
					if got := dispatched.Load(); got != 0 {
						t.Errorf("withdrawn tool reached the server %d times", got)
					}
					writeConfig(ExposureDirect)
					manager.Reload(input)
					synctest.Wait()
					fresh, ok := manager.Snapshot().Find(tool.Name)
					if !ok || fresh.Epoch != tool.Epoch || manager.servers["fixture"].conn != conn {
						t.Fatal("exposure reload replaced the connection or lost the tool")
					}
					result := manager.Call(t.Context(), fresh, tools.Invocation{Name: fresh.Name})
					if result.IsError || len(result.Content) != 1 || result.Content[0].Text != "echo" || dispatched.Load() != 1 {
						t.Fatalf("re-enabled tool call = %+v; dispatches = %d", result, dispatched.Load())
					}
					if len(conn.permits) != 0 || len(manager.callSlot) != 0 {
						t.Fatal("exposure withdrawal retained admission capacity")
					}
				})
			})
		}
	}
}

func TestManagerPublicationUsesReloadedExposure(t *testing.T) {
	for _, policy := range []struct{ before, after Exposure }{
		{before: ExposureDirect, after: ExposureHidden},
		{before: ExposureHidden, after: ExposureDirect},
	} {
		t.Run(string(policy.before)+"-to-"+string(policy.after), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				var dispatched atomic.Int32
				server := sdk.NewServer(&sdk.Implementation{Name: "fixture", Version: "1"}, nil)
				server.AddTool(sdkTool("echo", "Count dispatches", map[string]any{"type": "object"}), func(context.Context, *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
					dispatched.Add(1)
					return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: "echo"}}}, nil
				})
				manager := NewManager(Options{})
				t.Cleanup(func() { manager.Stop(); manager.Wait() })
				snapshot := connectInMemoryServer(t, manager, "fixture", server, time.Hour)
				tool, ok := snapshot.Find("mcp__fixture__echo")
				if !ok {
					t.Fatal("echo tool missing")
				}
				state := manager.servers["fixture"]
				conn := state.conn
				// Model setup paused after discovery but before publication. An
				// exposure-only reload keeps that setup's epoch, so publication
				// must apply the accepted policy rather than its captured policy.
				manager.mu.Lock()
				state.cfg.Exposure = policy.before
				conn.catalog = recomputeExposure(conn.catalog, state.cfg)
				state.conn, state.catalog = nil, nil
				state.status, state.setupActive = serverStateConnecting, true
				reloaded := state.cfg
				reloaded.Exposure = policy.after
				manager.acceptConfigLocked(&Config{Servers: []Server{reloaded}}, Input{}, nil)
				manager.mu.Unlock()
				manager.publishConnection(state, tool.Epoch, conn)
				synctest.Wait()
				_, declared := manager.Snapshot().Find(tool.Name)
				wantDeclared := policy.after == ExposureDirect
				if declared != wantDeclared {
					t.Errorf("publication declares tool = %v; want %v", declared, wantDeclared)
				}
				result := manager.Call(t.Context(), tool, tools.Invocation{Name: tool.Name})
				wantText, wantCalls := "MCP tool fixture/echo is unavailable (stale)", int32(0)
				if wantDeclared {
					wantText, wantCalls = "echo", 1
				}
				wantError := !wantDeclared
				if result.IsError != wantError || len(result.Content) != 1 || result.Content[0].Text != wantText || dispatched.Load() != wantCalls {
					t.Fatalf("call after publication = %+v; dispatches = %d", result, dispatched.Load())
				}
			})
		})
	}
}

func TestManagerRouteLiveRechecksRawName(t *testing.T) {
	manager := NewManager(Options{})
	conn := &connection{}
	tool := Tool{Name: "mcp__s__t", RawName: "t", Server: "s", Epoch: 1, Exposure: ExposureDirect}
	conn.catalog = []Tool{tool}
	manager.servers["s"] = &serverState{cfg: Server{Name: "s", Enabled: true}, status: serverStateReady, epoch: 1, conn: conn, catalog: conn.catalog}
	manager.order = []string{"s"}
	manager.publishLocked()
	if !manager.routeLive(tool, conn) {
		t.Fatal("declared route was rejected")
	}
	conn.catalog[0].RawName = "other"
	if manager.routeLive(tool, conn) {
		t.Fatal("replaced raw route passed the post-admission check")
	}
}

func TestManagerPublishesConfigWarnings(t *testing.T) {
	dir := t.TempDir()
	config := `{"mcpServers": {` + testServerConfig("fixture", "basic", "") + `, "bad": {"command": "node", "exposure": "not-an-exposure"}}}`
	path := writeTestFile(t, dir, "mcp.json", config)
	manager := NewManager(Options{Input: Input{UserPath: path, Cwd: dir, ReadAvailable: true}})
	manager.Start()
	t.Cleanup(func() {
		manager.Stop()
		manager.Wait()
	})
	snapshot := waitServerState(t, manager, "fixture", serverStateReady)
	if len(snapshot.Warnings) == 0 {
		t.Fatalf("config warning missing: %+v", snapshot)
	}
	if !strings.Contains(snapshot.Warnings[0], "bad") {
		t.Fatalf("config warning did not identify the entry: %v", snapshot.Warnings)
	}
}

func TestManagerAcceptedReloadRefillsReconnectAllowance(t *testing.T) {
	manager := NewManager(Options{})
	server := Server{Name: "fixture", Transport: TransportStdio, Enabled: true}
	state := &serverState{cfg: server, status: serverStateReady, epoch: 1, reconnects: maxReconnects}
	manager.servers["fixture"] = state
	manager.acceptConfigLocked(&Config{Servers: []Server{server}}, Input{}, nil)
	if state.reconnects != 0 {
		t.Fatalf("reconnect allowance after reload = %d", state.reconnects)
	}
}

func TestCallFailureDistinguishesConnectionLossFromCallerCancellation(t *testing.T) {
	for _, callerCancelled := range []bool{false, true} {
		t.Run(fmt.Sprintf("caller_cancelled=%t", callerCancelled), func(t *testing.T) {
			parent, cancelParent := context.WithCancel(context.Background())
			defer cancelParent()
			callCtx, cancelCall := context.WithCancel(parent)
			defer cancelCall()
			if callerCancelled {
				cancelParent()
			}
			// Reproduce cleanup winning the race with the call result. The
			// derived context alone cannot identify who cancelled the call.
			cancelCall()
			conn := &connection{}
			result := conn.callFailure(Tool{Server: "fixture", RawName: "crash"}, sdk.ErrConnectionClosed, &callState{}, callCtx, parent)
			want := "outcome may be unknown"
			if callerCancelled {
				want = "was cancelled"
			}
			if !result.IsError || len(result.Content) != 1 || !strings.Contains(result.Content[0].Text, want) {
				t.Fatalf("call failure = %+v; want %q", result, want)
			}
		})
	}
}

func TestManagerBoundsConfigWarnings(t *testing.T) {
	manager := NewManager(Options{})
	for i := 0; i < maxConfigWarnings+5; i++ {
		manager.warnings = append(manager.warnings, fmt.Sprintf("warning %d", i))
	}
	manager.publishLocked()
	warnings := manager.Snapshot().Warnings
	if len(warnings) != maxConfigWarnings+1 {
		t.Fatalf("bounded warnings = %d", len(warnings))
	}
	if warnings[len(warnings)-1] != "5 more config warnings" {
		t.Fatalf("warning trailer = %q", warnings[len(warnings)-1])
	}
}

func TestManagerRejectsHTTPBeforeEndpointRequest(t *testing.T) {
	var requests atomic.Int32
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		http.Error(w, "unexpected request", http.StatusBadRequest)
	}))
	t.Cleanup(endpoint.Close)

	manager := NewManager(Options{})
	t.Cleanup(func() {
		manager.Stop()
		manager.Wait()
	})
	server := Server{Name: "remote", Transport: TransportHTTP, URL: endpoint.URL}
	state := &serverState{cfg: server}
	_, conn, err := manager.connect(context.Background(), state, 1, server, Input{})
	if conn == nil {
		t.Fatal("HTTP rejection did not return connection state")
	}
	if class := ErrorClass(err); class != "unsupported" {
		t.Fatalf("HTTP setup error class = %q, want unsupported (err=%v)", class, err)
	}
	if got := requests.Load(); got != 0 {
		t.Fatalf("HTTP setup made %d endpoint requests before rejection", got)
	}
}
