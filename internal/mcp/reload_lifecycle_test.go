package mcp

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/hypernewbie/psi/internal/tools"
)

func TestManagerStaleRouteRejectedAfterRemoveAndReadd(t *testing.T) {
	dir := t.TempDir()
	config := `{"mcpServers": {` + testServerConfig("fixture", "basic", "") + `}}`
	input := Input{UserPath: writeTestFile(t, dir, "mcp.json", config), Cwd: dir}
	manager := NewManager(Options{Input: input})
	t.Cleanup(func() { manager.Stop(); manager.Wait() })

	// Drive reload and setup synchronously instead of polling for the real
	// child process. Hold the scheduled workers until cancellation.
	for range cap(manager.setupSlot) {
		manager.setupSlot <- struct{}{}
	}
	reload := func() {
		manager.Reload(input)
		manager.applyReload(<-manager.reloads)
	}
	connect := func() Tool {
		t.Helper()
		manager.mu.Lock()
		state := manager.servers["fixture"]
		if state == nil {
			manager.mu.Unlock()
			t.Fatal("fixture not loaded")
		}
		epoch := state.epoch
		manager.mu.Unlock()
		manager.runSetup(state, epoch, t.Context())
		snapshot := manager.Snapshot()
		tool, ok := snapshot.Find("mcp__fixture__echo")
		if !ok {
			t.Fatalf("fixture echo tool missing: %+v", snapshot.Servers)
		}
		return tool
	}
	call := func(tool Tool) tools.Result {
		return manager.Call(t.Context(), tool, tools.Invocation{Name: tool.Name, Arguments: map[string]any{"text": "hello"}})
	}

	reload()
	old := connect()
	writeTestFile(t, dir, "mcp.json", `{"mcpServers":{}}`)
	reload()
	if _, ok := manager.Snapshot().Find(old.Name); ok {
		t.Fatal("removed server still has a route")
	}
	writeTestFile(t, dir, "mcp.json", config)
	reload()
	fresh := connect()

	if result := call(old); !result.IsError || len(result.Content) != 1 || result.Content[0].Text != "MCP tool fixture/echo is unavailable (stale)" {
		t.Errorf("old route after re-add = %+v, want stale route rejection", result)
	}
	if result := call(fresh); result.IsError || len(result.Content) != 1 || result.Content[0].Text != "hello" {
		t.Errorf("fresh route after re-add = %+v, want echo reply", result)
	}
}

func TestManagerConcurrentReloadKeepsLatestInput(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		manager := NewManager(Options{})
		t.Cleanup(manager.Stop)
		start := make(chan struct{})
		var callers sync.WaitGroup
		const requests = 32
		for i := range requests {
			callers.Go(func() {
				<-start
				manager.Reload(Input{Cwd: fmt.Sprintf("project-%d", i)})
			})
		}
		close(start)
		callers.Wait()
		select {
		case req := <-manager.reloads:
			if req.generation != requests || req.input != manager.input {
				t.Fatalf("queued request = %+v, want generation %d with latest input %+v", req, requests, manager.input)
			}
		default:
			t.Fatal("latest reload request was lost")
		}

		manager.Stop()
		manager.Reload(Input{Cwd: "after-stop"})
		select {
		case req := <-manager.reloads:
			t.Fatalf("stopped manager queued a reload: %+v", req)
		default:
		}
	})
}

func TestManagerFailedReloadAppliesOnlyCurrentPolicy(t *testing.T) {
	path := writeTestFile(t, t.TempDir(), "mcp.json", `null`)
	for _, phase := range []string{"current", "superseded", "stopped"} {
		t.Run(phase, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				trusted := Input{UserPath: path, ProjectTrusted: true}
				manager := NewManager(Options{Input: trusted})
				t.Cleanup(func() { manager.Stop(); manager.Wait() })
				for _, name := range []string{"user", "project"} {
					server := sdk.NewServer(&sdk.Implementation{Name: name, Version: "1"}, nil)
					connectInMemoryServer(t, manager, name, server, time.Hour)
				}
				manager.servers["project"].cfg.Source = "project"
				manager.Reload(Input{UserPath: path})
				failed := <-manager.reloads
				switch phase {
				case "superseded":
					manager.Reload(trusted)
				case "stopped":
					manager.Stop()
				}
				before := manager.Snapshot()
				manager.applyReload(failed)
				after := manager.Snapshot()
				if phase != "current" {
					if before != after {
						t.Fatalf("%s failed reload changed the snapshot: %+v", phase, after)
					}
					return
				}
				if after.ReloadError == "" || len(after.Servers) != 2 {
					t.Fatalf("current failed reload did not preserve server entries and report the error: %+v", after)
				}
				if user, project := after.Servers[0], after.Servers[1]; user.State != serverStateReady || project.State != serverStateFailed || project.ErrorClass != "permission" {
					t.Fatalf("current failed reload did not withdraw only the disallowed project server: %+v", after.Servers)
				}
			})
		})
	}
}

func TestManagerRetriesSetupAfterTrustReturns(t *testing.T) {
	dir := t.TempDir()
	config := `{"mcpServers": {` + testServerConfig("fixture", "basic", "") + `}}`
	trusted := Input{ProjectPath: writeTestFile(t, dir, "mcp.json", config), ProjectTrusted: true, Cwd: dir}
	manager := NewManager(Options{Input: trusted})
	t.Cleanup(func() { manager.Stop(); manager.Wait() })
	// Hold scheduled setup workers so the test can pause a real connection
	// after discovery, before the manager publishes its tools.
	for range cap(manager.setupSlot) {
		manager.setupSlot <- struct{}{}
	}
	manager.Reload(trusted)
	manager.applyReload(<-manager.reloads)
	state := manager.servers["fixture"]
	if state == nil {
		t.Fatal("project fixture not loaded")
	}
	epoch := state.epoch
	_, conn, err := manager.connect(t.Context(), state, epoch, state.cfg, trusted)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(conn.closeAsync)

	untrusted := trusted
	untrusted.ProjectTrusted = false
	manager.Reload(untrusted)
	manager.publishConnection(t.Context(), state, epoch, conn)
	snapshot := manager.Snapshot()
	if len(snapshot.Tools) != 0 || len(snapshot.Servers) != 1 || snapshot.Servers[0].ErrorClass != "permission" {
		t.Fatalf("setup at trust loss did not withdraw the connection as a permission failure: %+v", snapshot)
	}

	// No untrusted config was accepted. The trusted reload must still start
	// a replacement rather than reuse setup that has already been rejected.
	manager.Reload(trusted)
	manager.applyReload(<-manager.reloads)
	if state.epoch == epoch || !state.setupActive {
		t.Fatalf("restored trust did not schedule replacement setup: %+v", manager.Snapshot())
	}
	manager.runSetup(state, state.epoch, t.Context())
	tool, ok := manager.Snapshot().Find("mcp__fixture__echo")
	if !ok {
		t.Fatalf("replacement did not publish echo: %+v", manager.Snapshot())
	}
	result := manager.Call(t.Context(), tool, tools.Invocation{Name: tool.Name, Arguments: map[string]any{"text": "restored"}})
	if result.IsError || len(result.Content) != 1 || result.Content[0].Text != "restored" {
		t.Fatalf("echo after trust returned = %+v", result)
	}
}

func TestManagerHTTPDoesNotPublishCancelledSetup(t *testing.T) {
	server := httpFixture(t)
	endpoint := httptest.NewServer(sdk.NewStreamableHTTPHandler(func(*http.Request) *sdk.Server { return server }, nil))
	t.Cleanup(endpoint.Close)
	manager := NewManager(Options{})
	t.Cleanup(func() { manager.Stop(); manager.Wait() })
	config := Server{Name: "fixture", Transport: TransportHTTP, URL: endpoint.URL, Enabled: true, Exposure: ExposureDirect}
	state := &serverState{cfg: config, epoch: 1, status: serverStateConnecting, setupActive: true}
	manager.servers[config.Name] = state
	manager.order = []string{config.Name}
	setupCtx, cancel := context.WithCancel(t.Context())
	defer cancel()
	_, conn, err := manager.connect(setupCtx, state, state.epoch, config, Input{})
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	manager.publishConnection(setupCtx, state, state.epoch, conn)
	snapshot := manager.Snapshot()
	if len(snapshot.Tools) != 0 || len(snapshot.Servers) != 1 || snapshot.Servers[0].State != serverStateFailed || snapshot.Servers[0].ErrorClass != "cancelled" {
		t.Fatalf("cancelled setup was not rejected: %+v", snapshot)
	}
}

func TestManagerConnectUsesCapturedServerDuringReload(t *testing.T) {
	manager := NewManager(Options{})
	t.Cleanup(func() { manager.Stop(); manager.Wait() })
	requestStarted := make(chan struct{})
	continueRequest := make(chan struct{})
	var firstRequest sync.Once
	mcpServer := httpFixture(t)
	mcpHandler := sdk.NewStreamableHTTPHandler(func(*http.Request) *sdk.Server { return mcpServer }, nil)
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		firstRequest.Do(func() {
			close(requestStarted)
			<-continueRequest
		})
		mcpHandler.ServeHTTP(w, r)
	}))
	t.Cleanup(endpoint.Close)

	server := Server{Name: "fixture", Transport: TransportHTTP, URL: endpoint.URL, Enabled: true, Exposure: ExposureDirect}
	state := &serverState{cfg: server, status: serverStateConnecting, epoch: 1, setupActive: true}
	manager.servers[server.Name] = state
	updated := server
	updated.Exposure = ExposureHidden
	finishedReload := make(chan struct{})
	go func() {
		<-requestStarted
		manager.mu.Lock()
		manager.acceptConfigLocked(&Config{Servers: []Server{updated}}, Input{}, nil)
		manager.mu.Unlock()
		close(continueRequest)
		close(finishedReload)
	}()

	session, conn, err := manager.connect(t.Context(), state, 1, server, Input{})
	<-finishedReload
	if err != nil {
		t.Fatalf("connect with captured server failed: %v", err)
	}
	t.Cleanup(func() {
		conn.cancel()
		_ = session.Close()
	})
	var echo Tool
	for _, tool := range conn.catalog {
		if tool.Name == "mcp__fixture__echo" {
			echo = tool
			break
		}
	}
	if echo.Name == "" || echo.Exposure != ExposureDirect {
		t.Fatalf("connection did not use the captured direct exposure: %+v", conn.catalog)
	}
}
