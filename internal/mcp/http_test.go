package mcp

import (
	"bytes"
	"context"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/hypernewbie/psi/internal/tools"
)

func httpFixture(t *testing.T) *sdk.Server {
	t.Helper()
	server := sdk.NewServer(&sdk.Implementation{Name: "http-fixture", Version: "1"}, nil)
	server.AddTool(&sdk.Tool{Name: "refresh", Description: "Refresh", InputSchema: map[string]any{"type": "object"}}, func(context.Context, *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
		server.AddTool(&sdk.Tool{Name: "dynamic_tool", Description: "Dynamic", InputSchema: map[string]any{"type": "object"}}, func(context.Context, *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
			return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: "dynamic"}}}, nil
		})
		return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: "refreshed"}}}, nil
	})
	server.AddTool(&sdk.Tool{
		Name:        "echo",
		Description: "Echo",
		InputSchema: map[string]any{"type": "object", "properties": map[string]any{"text": map[string]any{"type": "string"}}},
	}, func(_ context.Context, req *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
		return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: string(req.Params.Arguments)}}}, nil
	})
	return server
}

func writeHTTPConfig(t *testing.T, dir, url string, headers map[string]string) string {
	return writeHTTPConfigEnabled(t, dir, url, headers, true)
}

func writeHTTPConfigEnabled(t *testing.T, dir, url string, headers map[string]string, enabled bool) string {
	t.Helper()
	if headers == nil {
		headers = map[string]string{}
	}
	config := map[string]any{"mcpServers": map[string]any{"http-fixture": map[string]any{
		"url": url, "headers": headers, "exposure": "direct", "timeout": 5, "enabled": enabled,
	}}}

	data, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "mcp.json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func waitForHTTPSnapshotChange(t *testing.T, manager *Manager, deadline time.Time) {
	t.Helper()
	timeLeft := time.Until(deadline)
	if timeLeft <= 0 {
		t.Fatal("manager did not publish the expected snapshot before the test deadline")
	}
	timer := time.NewTimer(timeLeft)
	defer timer.Stop()
	select {
	case <-manager.snapshotChange:
	case <-timer.C:
		t.Fatal("manager did not publish the expected snapshot before the test deadline")
	}
}

func waitHTTPServerState(t *testing.T, manager *Manager, name, want string) *Snapshot {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
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
		waitForHTTPSnapshotChange(t, manager, deadline)
	}
}

func TestManagerHTTPEchoAndHeaders(t *testing.T) {
	server := httpFixture(t)
	mcpHandler := sdk.NewStreamableHTTPHandler(func(*http.Request) *sdk.Server { return server }, nil)
	var missing atomic.Int32
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Test") != "ok" {
			missing.Add(1)
			http.Error(w, "missing header", 400)
			return
		}
		mcpHandler.ServeHTTP(w, r)
	})
	httpServer := httptest.NewServer(handler)
	t.Cleanup(httpServer.Close)
	dir := t.TempDir()
	path := writeHTTPConfig(t, dir, httpServer.URL, map[string]string{"X-Test": "ok"})
	manager := NewManager(Options{Input: Input{UserPath: path, Cwd: dir, ReadAvailable: true}})
	manager.Start()
	t.Cleanup(func() {
		manager.Stop()
		manager.Wait()
	})
	snapshot := waitHTTPServerState(t, manager, "http-fixture", serverStateReady)
	tool, ok := snapshot.Find("mcp__http_fixture__echo")
	if !ok {
		t.Fatalf("declared tool missing: %+v", snapshot.Tools)
	}
	result := manager.Call(context.Background(), tool, tools.Invocation{Name: tool.Name, Arguments: map[string]any{"text": "hello"}})
	if result.IsError {
		t.Fatalf("http call: %+v", result)
	}
	if missing.Load() != 0 {
		t.Fatalf("requests without the configured header: %d", missing.Load())
	}
}

func TestManagerHTTPRejectsRedirect(t *testing.T) {
	var secondHits atomic.Int32
	second := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		secondHits.Add(1)
		http.Error(w, "should not be reached", 500)
	}))
	t.Cleanup(second.Close)
	first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, second.URL+r.URL.Path, http.StatusTemporaryRedirect)
	}))
	t.Cleanup(first.Close)
	dir := t.TempDir()
	path := writeHTTPConfig(t, dir, first.URL, nil)
	manager := NewManager(Options{Input: Input{UserPath: path, Cwd: dir, ReadAvailable: true}})
	manager.Start()
	t.Cleanup(func() {
		manager.Stop()
		manager.Wait()
	})
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		snapshot := manager.Snapshot()
		for _, status := range snapshot.Servers {
			if status.State == serverStateFailed {
				if secondHits.Load() != 0 {
					t.Fatalf("redirect target was reached %d times", secondHits.Load())
				}
				return
			}
		}
		waitForHTTPSnapshotChange(t, manager, deadline)
	}
	t.Fatalf("redirecting endpoint did not fail: %+v", manager.Snapshot().Servers)
}

func TestHTTPNonStreamResponsesEnforceBodyCap(t *testing.T) {
	body := strings.Repeat("x", maxHTTPBody+1)
	for _, tc := range []struct {
		name, method, contentType string
		status                    int
	}{
		{"POST error", http.MethodPost, "text/event-stream", http.StatusBadRequest},
		{"GET error", http.MethodGet, "text/event-stream", http.StatusNotImplemented},
		{"invalid media type", http.MethodPost, "text/event-stream-extra", http.StatusOK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			conn := &connection{ctx: t.Context(), listenerChecked: make(chan struct{})}
			conn.errClass.Store("")
			transport := &headerRoundTripper{conn: conn, base: roundTripFunc(func(*http.Request) (*http.Response, error) {
				return &http.Response{
					StatusCode: tc.status,
					Header:     http.Header{"Content-Type": {tc.contentType}},
					Body:       io.NopCloser(strings.NewReader(body)),
				}, nil
			})}
			req, err := http.NewRequestWithContext(t.Context(), tc.method, "http://fixture.test/mcp", nil)
			if err != nil {
				t.Fatal(err)
			}
			resp, err := transport.RoundTrip(req)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			read, err := io.Copy(io.Discard, resp.Body)
			if read != maxHTTPBody || ErrorClass(err) != "limit" {
				t.Fatalf("body read = %d bytes, %v; want %d and a limit error", read, err, maxHTTPBody)
			}
		})
	}
}

func TestManagerHTTPSessionLossReconnectsWithoutReplay(t *testing.T) {
	server := httpFixture(t)
	mcpHandler := sdk.NewStreamableHTTPHandler(func(*http.Request) *sdk.Server { return server }, nil)
	var loseSession atomic.Bool
	var toolCalls atomic.Int32
	var initializes atomic.Int32
	lost := make(chan struct{}, 1)
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && strings.Contains(r.URL.Path, "/mcp") {
			toolCalls.Add(1)
		}
		if rpcMethod(r) == "initialize" {
			initializes.Add(1)
		}
		if r.Method == http.MethodPost && loseSession.Load() && r.Header.Get("Mcp-Session-Id") != "" {
			body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
			r.Body = io.NopCloser(bytes.NewReader(body))
			if bytes.Contains(body, []byte(`"method":"tools/call"`)) {
				select {
				case lost <- struct{}{}:
				default:
				}
				http.Error(w, "session gone", http.StatusNotFound)
				return
			}
		}
		mcpHandler.ServeHTTP(w, r)
	})
	httpServer := httptest.NewServer(handler)
	t.Cleanup(httpServer.Close)
	dir := t.TempDir()
	path := writeHTTPConfig(t, dir, httpServer.URL, nil)
	manager := NewManager(Options{Input: Input{UserPath: path, Cwd: dir, ReadAvailable: true}})
	manager.Start()
	t.Cleanup(func() {
		manager.Stop()
		manager.Wait()
	})
	snapshot := waitHTTPServerState(t, manager, "http-fixture", serverStateReady)
	old, ok := snapshot.Find("mcp__http_fixture__echo")
	if !ok {
		t.Fatal("echo tool missing")
	}
	loseSession.Store(true)
	result := manager.Call(context.Background(), old, tools.Invocation{Name: old.Name, Arguments: map[string]any{"text": "lost"}})
	select {
	case <-lost:
	case <-time.After(time.Second):
		t.Fatal("session-loss response did not reach the tool call")
	}
	loseSession.Store(false)
	if !result.IsError || !strings.Contains(result.Content[0].Text, "session-lost") {
		t.Fatalf("session-loss call = %+v", result)
	}
	deadline := time.Now().Add(10 * time.Second)
	var replacement Tool
	for time.Now().Before(deadline) {
		snapshot = manager.Snapshot()
		for _, status := range snapshot.Servers {
			if status.Name != "http-fixture" || status.State != serverStateReady {
				continue
			}
			if tool, ok := snapshot.Find("mcp__http_fixture__echo"); ok && tool.Epoch != old.Epoch {
				replacement = tool
			}
		}
		if replacement.Name != "" {
			break
		}
		waitForHTTPSnapshotChange(t, manager, deadline)
	}
	if replacement.Name == "" {
		t.Fatalf("connection did not recover: %+v", manager.Snapshot().Servers)
	}
	if initializes.Load() != 2 {
		t.Fatalf("session-loss recovery initialized %d times, want 2", initializes.Load())
	}
	if stale := manager.Call(context.Background(), old, tools.Invocation{Name: old.Name}); !stale.IsError {
		t.Fatalf("old route reached the replacement: %+v", stale)
	}
	snapshot = manager.Snapshot()
	fresh := manager.Call(context.Background(), replacement, tools.Invocation{Name: replacement.Name, Arguments: map[string]any{"text": "again"}})
	if fresh.IsError {
		t.Fatalf("replacement call = %+v", fresh)
	}
}

func TestManagerHTTPRejectsUntrustedCertificate(t *testing.T) {
	server := httpFixture(t)
	handler := sdk.NewStreamableHTTPHandler(func(*http.Request) *sdk.Server { return server }, nil)
	httpServer := httptest.NewTLSServer(handler)
	t.Cleanup(httpServer.Close)
	dir := t.TempDir()
	path := writeHTTPConfig(t, dir, httpServer.URL, nil)
	manager := NewManager(Options{Input: Input{UserPath: path, Cwd: dir, ReadAvailable: true}})
	manager.Start()
	t.Cleanup(func() {
		manager.Stop()
		manager.Wait()
	})
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		snapshot := manager.Snapshot()
		for _, status := range snapshot.Servers {
			if status.State != serverStateFailed {
				continue
			}
			if status.ErrorClass != "certificate" {
				t.Fatalf("certificate failure class = %q", status.ErrorClass)
			}
			return
		}
		waitForHTTPSnapshotChange(t, manager, deadline)
	}
	t.Fatalf("untrusted certificate did not fail permanently: %+v", manager.Snapshot().Servers)
}

func TestManagerHTTPStatelessAndUnsupportedListener(t *testing.T) {
	server := httpFixture(t)
	handler := sdk.NewStreamableHTTPHandler(func(*http.Request) *sdk.Server { return server }, &sdk.StreamableHTTPOptions{Stateless: true})
	httpServer := httptest.NewServer(handler)
	t.Cleanup(httpServer.Close)
	dir := t.TempDir()
	path := writeHTTPConfig(t, dir, httpServer.URL, nil)
	manager := NewManager(Options{Input: Input{UserPath: path, Cwd: dir, ReadAvailable: true}})
	manager.Start()
	t.Cleanup(func() {
		manager.Stop()
		manager.Wait()
	})
	snapshot := waitHTTPServerState(t, manager, "http-fixture", serverStateReady)
	tool, ok := snapshot.Find("mcp__http_fixture__echo")
	if !ok {
		t.Fatalf("stateless tool missing: %+v", snapshot.Tools)
	}
	result := manager.Call(context.Background(), tool, tools.Invocation{Name: tool.Name, Arguments: map[string]any{"text": "stateless"}})
	if result.IsError {
		t.Fatalf("stateless call = %+v", result)
	}
}

func TestManagerHTTPStopClosesIdleConnection(t *testing.T) {
	server := httpFixture(t)
	handler := sdk.NewStreamableHTTPHandler(func(*http.Request) *sdk.Server { return server }, &sdk.StreamableHTTPOptions{Stateless: true, JSONResponse: true})
	httpServer := httptest.NewServer(handler)
	t.Cleanup(httpServer.Close)
	dir := t.TempDir()
	path := writeHTTPConfig(t, dir, httpServer.URL, nil)
	manager := NewManager(Options{Input: Input{UserPath: path, Cwd: dir, ReadAvailable: true}})
	manager.Start()
	t.Cleanup(func() { manager.Stop(); manager.Wait() })
	snapshot := waitHTTPServerState(t, manager, "http-fixture", serverStateReady)
	tool, ok := snapshot.Find("mcp__http_fixture__echo")
	if !ok {
		t.Fatal("echo tool missing")
	}

	var socket net.Conn
	pooled := make(chan error, 1)
	ctx := httptrace.WithClientTrace(t.Context(), &httptrace.ClientTrace{
		GotConn:     func(info httptrace.GotConnInfo) { socket = info.Conn },
		PutIdleConn: func(err error) { pooled <- err },
	})
	if result := manager.Call(ctx, tool, tools.Invocation{Name: tool.Name, Arguments: map[string]any{"text": "idle"}}); result.IsError {
		t.Fatalf("call failed: %+v", result)
	}
	select {
	case err := <-pooled:
		if err != nil || socket == nil {
			t.Fatalf("call socket was not returned to the pool: socket=%v err=%v", socket, err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("call socket never entered the pool")
	}

	manager.Stop()
	manager.Wait()
	if err := socket.SetDeadline(time.Time{}); !errors.Is(err, net.ErrClosed) {
		t.Fatalf("call socket was not closed after shutdown: %v", err)
	}
}

func TestManagerHTTPSetupBudgetCancelsStalledHeaders(t *testing.T) {
	stalled := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(1 * time.Second):
		}
	}))
	t.Cleanup(stalled.Close)
	dir := t.TempDir()
	data, _ := json.Marshal(map[string]any{"mcpServers": map[string]any{"http-fixture": map[string]any{"url": stalled.URL, "timeout": 0.1, "exposure": "direct"}}})
	path := filepath.Join(dir, "mcp.json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	manager := NewManager(Options{Input: Input{UserPath: path, Cwd: dir, ReadAvailable: true}})
	manager.Start()
	t.Cleanup(func() {
		manager.Stop()
		manager.Wait()
	})
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		snapshot := manager.Snapshot()
		for _, status := range snapshot.Servers {
			if status.Name == "http-fixture" && status.State == serverStateFailed {
				return
			}
		}
		waitForHTTPSnapshotChange(t, manager, deadline)
	}
	t.Fatalf("stalled setup outlived its budget: %+v", manager.Snapshot().Servers)
}

func TestManagerHTTPDiscoveryRetriesAndRetryAfter(t *testing.T) {
	server := httpFixture(t)
	mcpHandler := sdk.NewStreamableHTTPHandler(func(*http.Request) *sdk.Server { return server }, nil)
	var attempts atomic.Int32
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && rpcMethod(r) == "initialize" {
			if attempts.Add(1) <= 2 {
				w.Header().Set("Retry-After", "1")
				http.Error(w, "try later", http.StatusInternalServerError)
				return
			}
		}
		mcpHandler.ServeHTTP(w, r)
	})
	httpServer := httptest.NewServer(handler)
	t.Cleanup(httpServer.Close)
	dir := t.TempDir()
	path := writeHTTPConfig(t, dir, httpServer.URL, nil)
	var delays []time.Duration
	manager := NewManager(Options{Input: Input{UserPath: path, Cwd: dir, ReadAvailable: true}, sleep: func(ctx context.Context, delay time.Duration) bool {
		delays = append(delays, delay)
		return true
	}})
	manager.Start()
	t.Cleanup(func() {
		manager.Stop()
		manager.Wait()
	})
	waitHTTPServerState(t, manager, "http-fixture", serverStateReady)
	if attempts.Load() != 3 {
		t.Fatalf("initialize attempts = %d, want 3", attempts.Load())
	}
	if len(delays) != 2 {
		t.Fatalf("retry delays = %v", delays)
	}
	for _, delay := range delays {
		if delay < time.Second {
			t.Fatalf("Retry-After was clamped below the server delay: %v", delay)
		}
	}
}

func TestManagerHTTPDiscoveryRetryAfterBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name    string
		headers []string
	}{
		{name: "invalid", headers: []string{"not-a-date"}},
		{name: "over five seconds", headers: []string{"6"}},
		{name: "negative", headers: []string{"-1"}},
		{name: "duplicate", headers: []string{"1", "2"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var attempts atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodPost {
					attempts.Add(1)
				}
				for _, value := range tc.headers {
					w.Header().Add("Retry-After", value)
				}
				http.Error(w, "try later", http.StatusInternalServerError)
			}))
			t.Cleanup(server.Close)
			dir := t.TempDir()
			path := writeHTTPConfig(t, dir, server.URL, nil)
			slept := false
			manager := NewManager(Options{Input: Input{UserPath: path, Cwd: dir, ReadAvailable: true}, sleep: func(context.Context, time.Duration) bool {
				slept = true
				return true
			}})
			manager.Start()
			t.Cleanup(func() {
				manager.Stop()
				manager.Wait()
			})
			deadline := time.Now().Add(5 * time.Second)
			for time.Now().Before(deadline) {
				snapshot := manager.Snapshot()
				for _, status := range snapshot.Servers {
					if status.State == serverStateFailed {
						if attempts.Load() != 1 {
							t.Fatalf("%s: attempts = %d, want 1", tc.name, attempts.Load())
						}
						if slept {
							t.Fatal("invalid Retry-After still slept and retried")
						}
						return
					}
				}
				waitForHTTPSnapshotChange(t, manager, deadline)
			}
			t.Fatalf("%s: server did not fail: %+v", tc.name, manager.Snapshot().Servers)
		})
	}
}

func TestManagerHTTPDiscoveryBudgetStopsAfterTwoRetries(t *testing.T) {
	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && rpcMethod(r) == "initialize" {
			attempts.Add(1)
		}
		http.Error(w, "still down", http.StatusServiceUnavailable)
	}))
	t.Cleanup(server.Close)
	dir := t.TempDir()
	path := writeHTTPConfig(t, dir, server.URL, nil)
	manager := NewManager(Options{Input: Input{UserPath: path, Cwd: dir, ReadAvailable: true}, sleep: func(context.Context, time.Duration) bool { return true }})
	manager.Start()
	t.Cleanup(func() {
		manager.Stop()
		manager.Wait()
	})
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		for _, status := range manager.Snapshot().Servers {
			if status.State == serverStateFailed {
				if attempts.Load() != 3 {
					t.Fatalf("attempts = %d, want 3", attempts.Load())
				}
				return
			}
		}
		waitForHTTPSnapshotChange(t, manager, deadline)
	}
	t.Fatalf("server did not fail: %+v", manager.Snapshot().Servers)
}

func TestManagerHTTPStandaloneAuthFailure(t *testing.T) {
	for _, status := range []int{http.StatusUnauthorized, http.StatusForbidden} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			server := httpFixture(t)
			mcpHandler := sdk.NewStreamableHTTPHandler(func(*http.Request) *sdk.Server { return server }, nil)
			var posts atomic.Int32
			handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodPost && rpcMethod(r) == "initialize" {
					posts.Add(1)
				}
				if r.Method == http.MethodGet {
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(status)
					_, _ = w.Write([]byte(`{"error":"auth required"}`))
					return
				}
				mcpHandler.ServeHTTP(w, r)
			})
			httpServer := httptest.NewServer(handler)
			t.Cleanup(httpServer.Close)
			dir := t.TempDir()
			path := writeHTTPConfig(t, dir, httpServer.URL, nil)
			manager := NewManager(Options{Input: Input{UserPath: path, Cwd: dir, ReadAvailable: true}, sleep: func(context.Context, time.Duration) bool { return true }})
			manager.Start()
			t.Cleanup(func() {
				manager.Stop()
				manager.Wait()
			})
			deadline := time.Now().Add(5 * time.Second)
			for time.Now().Before(deadline) {
				for _, serverStatus := range manager.Snapshot().Servers {
					if serverStatus.State == serverStateFailed {
						if serverStatus.ErrorClass != "auth" {
							t.Fatalf("class = %q", serverStatus.ErrorClass)
						}
						if posts.Load() != 1 {
							t.Fatalf("initialize posts = %d, want 1", posts.Load())
						}
						return
					}
				}
				waitForHTTPSnapshotChange(t, manager, deadline)
			}
			t.Fatalf("GET %d did not fail setup: %+v", status, manager.Snapshot().Servers)
		})
	}
}

func rpcMethod(r *http.Request) string {
	if r.Body == nil {
		return ""
	}
	body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	r.Body = io.NopCloser(bytes.NewReader(body))
	var probe struct {
		Method string `json:"method"`
	}
	_ = json.Unmarshal(body, &probe)
	return probe.Method
}

func TestManagerHTTPFailedDiscoveryCancelsPendingReply(t *testing.T) {
	replyStarted := make(chan struct{})
	replyCanceled := make(chan struct{})
	releaseReply := make(chan struct{})
	defer close(releaseReply) // Let the fixture stop even if the regression fails.
	server := httpFixture(t)
	pingCtx, stopPing := context.WithCancel(t.Context())
	defer stopPing()
	handler := sdk.NewStreamableHTTPHandler(func(*http.Request) *sdk.Server { return server }, nil)
	httpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			switch rpcMethod(r) {
			case "tools/list":
				for session := range server.Sessions() {
					go func() { _ = session.Ping(pingCtx, nil) }()
				}
				select {
				case <-replyStarted:
					stopPing() // The fixture's own Ping must not hold up DELETE.
					http.Error(w, "discovery failed", http.StatusBadRequest)
				case <-r.Context().Done():
				}
				return
			case "": // The client's JSON-RPC response to the ping.
				w.WriteHeader(http.StatusBadRequest)
				w.(http.Flusher).Flush()
				close(replyStarted)
				select {
				case <-r.Context().Done():
					close(replyCanceled)
				case <-releaseReply:
				}
				return
			}
		}
		handler.ServeHTTP(w, r)
	}))
	t.Cleanup(httpServer.Close)
	dir := t.TempDir()
	manager := NewManager(Options{Input: Input{UserPath: writeHTTPConfig(t, dir, httpServer.URL, nil), Cwd: dir}})
	manager.Start()
	t.Cleanup(func() { manager.Stop(); manager.Wait() })
	select {
	case <-replyStarted:
	case <-time.After(10 * time.Second):
		t.Fatal("client did not reply to the ping during discovery")
	}
	waitHTTPServerState(t, manager, "http-fixture", serverStateFailed)
	select {
	case <-replyCanceled:
	case <-time.After(10 * time.Second):
		t.Fatal("discovery failure left the reply POST in flight")
	}
}

func TestManagerHTTPInitialListenerBudget(t *testing.T) {
	server := httpFixture(t)
	mcpHandler := sdk.NewStreamableHTTPHandler(func(*http.Request) *sdk.Server { return server }, nil)
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			<-r.Context().Done()
			return
		}
		mcpHandler.ServeHTTP(w, r)
	})
	httpServer := httptest.NewServer(handler)
	t.Cleanup(httpServer.Close)
	dir := t.TempDir()
	data, _ := json.Marshal(map[string]any{"mcpServers": map[string]any{"http-fixture": map[string]any{"url": httpServer.URL, "timeout": 0.2, "exposure": "direct"}}})
	path := filepath.Join(dir, "mcp.json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	manager := NewManager(Options{Input: Input{UserPath: path, Cwd: dir, ReadAvailable: true}})
	manager.Start()
	t.Cleanup(func() {
		manager.Stop()
		manager.Wait()
	})
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		for _, status := range manager.Snapshot().Servers {
			if status.State == serverStateFailed {
				return
			}
		}
		waitForHTTPSnapshotChange(t, manager, deadline)
	}
	t.Fatalf("stalled initial GET outlived the setup budget: %+v", manager.Snapshot().Servers)
}

func TestManagerHTTPRelistTimeoutRetainsCatalog(t *testing.T) {
	server := httpFixture(t)
	var stallList atomic.Bool
	mcpHandler := sdk.NewStreamableHTTPHandler(func(*http.Request) *sdk.Server { return server }, nil)
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && stallList.Load() && rpcMethod(r) == "tools/list" {
			<-r.Context().Done()
			return
		}
		mcpHandler.ServeHTTP(w, r)
	})
	httpServer := httptest.NewServer(handler)
	t.Cleanup(httpServer.Close)
	dir := t.TempDir()
	data, _ := json.Marshal(map[string]any{"mcpServers": map[string]any{"http-fixture": map[string]any{"url": httpServer.URL, "timeout": 0.2, "exposure": "direct"}}})
	path := filepath.Join(dir, "mcp.json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	manager := NewManager(Options{Input: Input{UserPath: path, Cwd: dir, ReadAvailable: true}})
	manager.Start()
	t.Cleanup(func() {
		stallList.Store(false)
		manager.Stop()
		manager.Wait()
	})
	snapshot := waitHTTPServerState(t, manager, "http-fixture", serverStateReady)
	echo, ok := snapshot.Find("mcp__http_fixture__echo")
	if !ok {
		t.Fatal("echo tool missing")
	}
	stallList.Store(true)
	refresh, ok := snapshot.Find("mcp__http_fixture__refresh")
	if !ok {
		t.Fatal("refresh tool missing")
	}
	if result := manager.Call(context.Background(), refresh, tools.Invocation{Name: refresh.Name}); result.IsError {
		t.Fatalf("refresh call = %+v", result)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		for _, status := range manager.Snapshot().Servers {
			if status.RefreshWarning != "" {
				if status.State != serverStateReady {
					t.Fatalf("re-list timeout withdrew the catalog: %+v", status)
				}
				result := manager.Call(context.Background(), echo, tools.Invocation{Name: echo.Name, Arguments: map[string]any{"text": "after"}})
				if result.IsError {
					t.Fatalf("listener or catalog was lost after re-list timeout: %+v", result)
				}
				manager.mu.Lock()
				conn := manager.servers["http-fixture"].conn
				manager.mu.Unlock()
				stallList.Store(false)
				if err := conn.refreshCatalog(); err != nil {
					t.Fatalf("recovery refresh = %v", err)
				}
				deadline = time.Now().Add(5 * time.Second)
				for time.Now().Before(deadline) {
					if manager.Snapshot().Servers[0].RefreshWarning == "" {
						return
					}
					waitForHTTPSnapshotChange(t, manager, deadline)
				}
				t.Fatalf("refresh warning did not clear: %+v", manager.Snapshot().Servers)
			}
		}
		waitForHTTPSnapshotChange(t, manager, deadline)
	}
	t.Fatalf("re-list timeout did not produce a warning: %+v", manager.Snapshot().Servers)
}

func TestManagerHTTPUnsupportedListenerForms(t *testing.T) {
	for _, form := range []string{"405", "200 non-SSE"} {
		t.Run(form, func(t *testing.T) {
			server := httpFixture(t)
			mcpHandler := sdk.NewStreamableHTTPHandler(func(*http.Request) *sdk.Server { return server }, nil)
			handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodGet {
					if form == "405" {
						w.WriteHeader(http.StatusMethodNotAllowed)
					} else {
						w.Header().Set("Content-Type", "text/plain")
						w.WriteHeader(http.StatusOK)
						_, _ = w.Write([]byte("listener unavailable"))
					}
					return
				}
				mcpHandler.ServeHTTP(w, r)
			})
			httpServer := httptest.NewServer(handler)
			t.Cleanup(httpServer.Close)
			dir := t.TempDir()
			path := writeHTTPConfig(t, dir, httpServer.URL, nil)
			manager := NewManager(Options{Input: Input{UserPath: path, Cwd: dir, ReadAvailable: true}})
			manager.Start()
			t.Cleanup(func() {
				manager.Stop()
				manager.Wait()
			})
			snapshot := waitHTTPServerState(t, manager, "http-fixture", serverStateReady)
			tool, ok := snapshot.Find("mcp__http_fixture__echo")
			if !ok {
				t.Fatalf("tool missing with unsupported listener: %+v", snapshot.Tools)
			}
			if !snapshot.Servers[0].NotificationsUnavailable {
				t.Fatalf("unsupported listener was not reported: %+v", snapshot.Servers)
			}
			result := manager.Call(context.Background(), tool, tools.Invocation{Name: tool.Name, Arguments: map[string]any{"text": "ok"}})
			if result.IsError {
				t.Fatalf("call failed with unsupported listener: %+v", result)
			}
		})
	}
}

func TestManagerHTTPTrustedTLSAndHostnameMismatch(t *testing.T) {
	server := httpFixture(t)
	handler := sdk.NewStreamableHTTPHandler(func(*http.Request) *sdk.Server { return server }, nil)
	tlsServer := httptest.NewTLSServer(handler)
	t.Cleanup(tlsServer.Close)
	roots := x509.NewCertPool()
	roots.AddCert(tlsServer.Certificate())
	dir := t.TempDir()
	path := writeHTTPConfig(t, dir, tlsServer.URL, nil)
	manager := NewManager(Options{Input: Input{UserPath: path, Cwd: dir, ReadAvailable: true}, roots: roots})
	manager.Start()
	t.Cleanup(func() {
		manager.Stop()
		manager.Wait()
	})
	snapshot := waitHTTPServerState(t, manager, "http-fixture", serverStateReady)
	tool, ok := snapshot.Find("mcp__http_fixture__echo")
	if !ok {
		t.Fatal("trusted TLS tool missing")
	}
	result := manager.Call(context.Background(), tool, tools.Invocation{Name: tool.Name, Arguments: map[string]any{"text": "tls"}})
	if result.IsError {
		t.Fatalf("trusted TLS call = %+v", result)
	}

	mismatchURL := strings.Replace(tlsServer.URL, "127.0.0.1", "localhost", 1)
	mismatchDir := t.TempDir()
	mismatchPath := writeHTTPConfig(t, mismatchDir, mismatchURL, nil)
	mismatch := NewManager(Options{Input: Input{UserPath: mismatchPath, Cwd: mismatchDir, ReadAvailable: true}, roots: roots})
	mismatch.Start()
	t.Cleanup(func() {
		mismatch.Stop()
		mismatch.Wait()
	})
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		for _, status := range mismatch.Snapshot().Servers {
			if status.State == serverStateFailed {
				if status.ErrorClass != "certificate" {
					t.Fatalf("hostname failure class = %q", status.ErrorClass)
				}
				return
			}
		}
		waitForHTTPSnapshotChange(t, mismatch, deadline)
	}
	t.Fatalf("hostname mismatch did not fail: %+v", mismatch.Snapshot().Servers)
}

func TestManagerHTTPStandaloneDiscoveryRetries(t *testing.T) {
	for _, tc := range []struct {
		name      string
		status    int
		failures  int32
		wantDelay time.Duration
		wantState string
		wantClass string
	}{
		{"rate limit recovers", http.StatusTooManyRequests, 1, 2 * time.Second, serverStateReady, ""},
		{"server error recovers", http.StatusServiceUnavailable, 1, 2 * time.Second, serverStateReady, ""},
		{"expired session recovers", http.StatusNotFound, 1, 250 * time.Millisecond, serverStateReady, ""},
		{"rate limit exhausts retries", http.StatusTooManyRequests, maxDiscoveryTries, 2 * time.Second, serverStateFailed, "rate-limit"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httpFixture(t)
			handler := sdk.NewStreamableHTTPHandler(func(*http.Request) *sdk.Server { return server }, nil)
			var gets atomic.Int32
			httpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodGet && gets.Add(1) <= tc.failures {
					if tc.status != http.StatusNotFound {
						w.Header().Set("Retry-After", "2")
					}
					http.Error(w, "try later", tc.status)
					return
				}
				if r.Method == http.MethodPost && rpcMethod(r) == "notifications/initialized" {
					// A successful notification must not replace the listener's advice.
					w.Header().Set("Retry-After", "0")
				}
				handler.ServeHTTP(w, r)
			}))
			t.Cleanup(httpServer.Close)
			dir := t.TempDir()
			path := writeHTTPConfig(t, dir, httpServer.URL, nil)
			var delays []time.Duration
			manager := NewManager(Options{Input: Input{UserPath: path, Cwd: dir}, sleep: func(_ context.Context, delay time.Duration) bool {
				delays = append(delays, delay)
				return true
			}})
			manager.Start()
			t.Cleanup(func() { manager.Stop(); manager.Wait() })
			snapshot := waitHTTPServerState(t, manager, "http-fixture", tc.wantState)
			wantGets := tc.failures
			if tc.wantState == serverStateReady {
				wantGets++
				if _, ok := snapshot.Find("mcp__http_fixture__echo"); !ok {
					t.Fatal("listener recovered without publishing the tools")
				}
			}
			if got := gets.Load(); got != wantGets {
				t.Fatalf("listener attempts = %d, want %d", got, wantGets)
			}
			if got := snapshot.Servers[0].ErrorClass; got != tc.wantClass {
				t.Fatalf("error class = %q, want %q", got, tc.wantClass)
			}
			if len(delays) != int(wantGets)-1 {
				t.Fatalf("retry delays = %v, want one per retry", delays)
			}
			for _, delay := range delays {
				if delay != tc.wantDelay {
					t.Fatalf("retry delay = %v, want %v", delay, tc.wantDelay)
				}
			}
		})
	}
}

func TestManagerHTTPBodyStallCancelsSetup(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":`))
		select {
		case <-r.Context().Done():
		case <-time.After(time.Second):
		}
	}))
	t.Cleanup(server.Close)
	dir := t.TempDir()
	data, _ := json.Marshal(map[string]any{"mcpServers": map[string]any{"http-fixture": map[string]any{"url": server.URL, "timeout": 0.2, "exposure": "direct"}}})
	path := filepath.Join(dir, "mcp.json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	manager := NewManager(Options{Input: Input{UserPath: path, Cwd: dir, ReadAvailable: true}})
	manager.Start()
	t.Cleanup(func() {
		manager.Stop()
		manager.Wait()
	})
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		for _, status := range manager.Snapshot().Servers {
			if status.State == serverStateFailed {
				return
			}
		}
		waitForHTTPSnapshotChange(t, manager, deadline)
	}
	t.Fatalf("stalled response body outlived the setup budget: %+v", manager.Snapshot().Servers)
}

func TestManagerHTTPJSONAndSSECallResponses(t *testing.T) {
	for _, mode := range []string{"json", "sse"} {
		t.Run(mode, func(t *testing.T) {
			server := httpFixture(t)
			handler := sdk.NewStreamableHTTPHandler(func(*http.Request) *sdk.Server { return server }, &sdk.StreamableHTTPOptions{JSONResponse: mode == "json"})
			httpServer := httptest.NewServer(handler)
			t.Cleanup(httpServer.Close)
			dir := t.TempDir()
			path := writeHTTPConfig(t, dir, httpServer.URL, nil)
			manager := NewManager(Options{Input: Input{UserPath: path, Cwd: dir, ReadAvailable: true}})
			manager.Start()
			t.Cleanup(func() {
				manager.Stop()
				manager.Wait()
			})
			snapshot := waitHTTPServerState(t, manager, "http-fixture", serverStateReady)
			tool, ok := snapshot.Find("mcp__http_fixture__echo")
			if !ok {
				t.Fatalf("%s tool missing", mode)
			}
			result := manager.Call(context.Background(), tool, tools.Invocation{Name: tool.Name, Arguments: map[string]any{"text": mode}})
			if result.IsError || !strings.Contains(result.Content[0].Text, mode) {
				t.Fatalf("%s call = %+v", mode, result)
			}
		})
	}
}

func TestManagerHTTPReloadWithdrawsRoutesBeforeSessionDeleteCompletes(t *testing.T) {
	deleteEntered := make(chan string, 1)
	allowDelete := make(chan struct{})
	deleteFinished := make(chan bool, 1)
	server := httpFixture(t)
	mcpHandler := sdk.NewStreamableHTTPHandler(func(*http.Request) *sdk.Server { return server }, nil)
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			deleteEntered <- r.Header.Get("Mcp-Session-Id")
			select {
			case <-allowDelete:
			case <-r.Context().Done():
				deleteFinished <- false
				return
			}
		}
		mcpHandler.ServeHTTP(w, r)
		if r.Method == http.MethodDelete {
			deleteFinished <- true
		}
	})
	httpServer := httptest.NewServer(handler)
	t.Cleanup(httpServer.Close)
	dir := t.TempDir()
	path := writeHTTPConfig(t, dir, httpServer.URL, nil)
	input := Input{UserPath: path, Cwd: dir}
	manager := NewManager(Options{Input: input})
	manager.Start()
	t.Cleanup(func() { manager.Stop(); manager.Wait() })
	waitHTTPServerState(t, manager, "http-fixture", serverStateReady)
	writeHTTPConfigEnabled(t, dir, httpServer.URL, nil, false)
	manager.Reload(input)
	select {
	case id := <-deleteEntered:
		if id == "" {
			t.Fatal("DELETE did not identify the retired session")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("manager retirement did not send DELETE")
	}
	snapshot := waitHTTPServerState(t, manager, "http-fixture", serverStateDisabled)
	if len(snapshot.Tools) != 0 {
		t.Fatalf("retired tool routes remain published: %+v", snapshot.Tools)
	}
	close(allowDelete)
	select {
	case completed := <-deleteFinished:
		if !completed {
			t.Fatal("retirement canceled the DELETE request before cleanup completed")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("server cleanup did not finish after releasing DELETE")
	}
}

func TestManagerHTTPHealthySiblingAfterFailure(t *testing.T) {
	failing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "down", http.StatusServiceUnavailable)
	}))
	t.Cleanup(failing.Close)
	server := httpFixture(t)
	handler := sdk.NewStreamableHTTPHandler(func(*http.Request) *sdk.Server { return server }, nil)
	good := httptest.NewServer(handler)
	t.Cleanup(good.Close)
	dir := t.TempDir()
	config := map[string]any{"mcpServers": map[string]any{
		"bad":  map[string]any{"url": failing.URL, "timeout": 0.3, "exposure": "direct"},
		"good": map[string]any{"url": good.URL, "timeout": 5, "exposure": "direct"},
	}}
	data, _ := json.Marshal(config)
	path := filepath.Join(dir, "mcp.json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	manager := NewManager(Options{Input: Input{UserPath: path, Cwd: dir, ReadAvailable: true}, sleep: func(context.Context, time.Duration) bool { return true }})
	manager.Start()
	t.Cleanup(func() {
		manager.Stop()
		manager.Wait()
	})
	snapshot := waitHTTPServerState(t, manager, "good", serverStateReady)
	tool, ok := snapshot.Find("mcp__good__echo")
	if !ok {
		t.Fatalf("healthy tool missing: %+v", snapshot.Tools)
	}
	result := manager.Call(context.Background(), tool, tools.Invocation{Name: tool.Name, Arguments: map[string]any{"text": "healthy"}})
	if result.IsError {
		t.Fatalf("healthy sibling call = %+v", result)
	}
}

func TestManagerHTTPDroppedCallIsNotReplayed(t *testing.T) {
	server := httpFixture(t)
	mcpHandler := sdk.NewStreamableHTTPHandler(func(*http.Request) *sdk.Server { return server }, nil)
	var dropped atomic.Bool
	var calls atomic.Int32
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && rpcMethod(r) == "tools/call" && dropped.CompareAndSwap(true, false) {
			calls.Add(1)
			mcpHandler.ServeHTTP(&discardWriter{header: w.Header()}, r)
			return
		}
		mcpHandler.ServeHTTP(w, r)
	})
	httpServer := httptest.NewServer(handler)
	t.Cleanup(httpServer.Close)
	dir := t.TempDir()
	path := writeHTTPConfig(t, dir, httpServer.URL, nil)
	manager := NewManager(Options{Input: Input{UserPath: path, Cwd: dir, ReadAvailable: true}})
	manager.Start()
	t.Cleanup(func() {
		manager.Stop()
		manager.Wait()
	})
	snapshot := waitHTTPServerState(t, manager, "http-fixture", serverStateReady)
	tool, ok := snapshot.Find("mcp__http_fixture__echo")
	if !ok {
		t.Fatal("echo tool missing")
	}
	dropped.Store(true)
	result := manager.Call(context.Background(), tool, tools.Invocation{Name: tool.Name, Arguments: map[string]any{"text": "once"}})
	if !result.IsError {
		t.Fatalf("dropped call succeeded: %+v", result)
	}
	if calls.Load() != 1 {
		t.Fatalf("server tool calls = %d, want 1", calls.Load())
	}
	if !strings.Contains(result.Content[0].Text, "outcome may be unknown") {
		t.Fatalf("dropped call did not report an ambiguous outcome: %+v", result)
	}
	// The next call must not replay the dropped one.
	result = manager.Call(context.Background(), tool, tools.Invocation{Name: tool.Name, Arguments: map[string]any{"text": "again"}})
	if result.IsError {
		t.Fatalf("follow-up call = %+v", result)
	}
}

type discardWriter struct {
	header http.Header
	status int
}

func (w *discardWriter) Header() http.Header         { return w.header }
func (w *discardWriter) WriteHeader(code int)        { w.status = code }
func (w *discardWriter) Write(p []byte) (int, error) { return len(p), nil }

func TestBoundedReadCloserExactAndOverCap(t *testing.T) {
	exact := &boundedReadCloser{body: io.NopCloser(bytes.NewReader(bytes.Repeat([]byte("x"), maxHTTPBody))), remaining: maxHTTPBody}
	data, err := io.ReadAll(exact)
	if err != nil || len(data) != maxHTTPBody {
		t.Fatalf("exact cap: len=%d err=%v", len(data), err)
	}
	over := &boundedReadCloser{body: io.NopCloser(bytes.NewReader(bytes.Repeat([]byte("x"), maxHTTPBody+1))), remaining: maxHTTPBody}
	if _, err := io.ReadAll(over); err == nil {
		t.Fatal("over-cap body was accepted")
	}
}

func TestManagerHTTPPrimingEventThenEOFIsBounded(t *testing.T) {
	server := httpFixture(t)
	mcpHandler := sdk.NewStreamableHTTPHandler(func(*http.Request) *sdk.Server { return server }, nil)
	var gets atomic.Int32
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			gets.Add(1)
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = w.Write([]byte(": priming\n\n"))
			return
		}
		mcpHandler.ServeHTTP(w, r)
	})
	httpServer := httptest.NewServer(handler)
	t.Cleanup(httpServer.Close)
	dir := t.TempDir()
	path := writeHTTPConfig(t, dir, httpServer.URL, nil)
	manager := NewManager(Options{Input: Input{UserPath: path, Cwd: dir, ReadAvailable: true}, sleep: func(context.Context, time.Duration) bool { return true }})
	manager.Start()
	t.Cleanup(func() {
		manager.Stop()
		manager.Wait()
	})
	waitHTTPServerState(t, manager, "http-fixture", serverStateFailed)
	// EOF may win before setup publishes or after a connection becomes ready.
	// Either path must exhaust the finite discovery/reconnect allowances.
	if count := gets.Load(); count < 1 || count > maxDiscoveryTries*(maxReconnects+1) {
		t.Fatalf("priming EOF GET attempts = %d; exceeded the lifecycle budget", count)
	}
}

func TestManagerHTTPMalformedDiscoveryStopsWithoutReplay(t *testing.T) {
	server := httpFixture(t)
	mcpHandler := sdk.NewStreamableHTTPHandler(func(*http.Request) *sdk.Server { return server }, nil)
	var initializes atomic.Int32
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method := rpcMethod(r)
		if method == "initialize" {
			initializes.Add(1)
		}
		if method == "tools/list" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":`))
			return
		}
		mcpHandler.ServeHTTP(w, r)
	})
	httpServer := httptest.NewServer(handler)
	t.Cleanup(httpServer.Close)
	dir := t.TempDir()
	path := writeHTTPConfig(t, dir, httpServer.URL, nil)
	manager := NewManager(Options{Input: Input{UserPath: path, Cwd: dir, ReadAvailable: true}, sleep: func(context.Context, time.Duration) bool { return true }})
	manager.Start()
	t.Cleanup(func() {
		manager.Stop()
		manager.Wait()
	})
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		for _, status := range manager.Snapshot().Servers {
			if status.State == serverStateFailed {
				if initializes.Load() > 3 {
					t.Fatalf("malformed discovery retried %d times", initializes.Load())
				}
				return
			}
		}
		waitForHTTPSnapshotChange(t, manager, deadline)
	}
	t.Fatalf("malformed discovery did not fail: %+v", manager.Snapshot().Servers)
}

func TestManagerHTTPInputRequiredIsNotReplayed(t *testing.T) {
	server := httpFixture(t)
	mcpHandler := sdk.NewStreamableHTTPHandler(func(*http.Request) *sdk.Server { return server }, nil)
	var calls atomic.Int32
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if rpcMethod(r) == "tools/call" {
			calls.Add(1)
			var probe struct {
				ID any `json:"id"`
			}
			body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
			_ = json.Unmarshal(body, &probe)
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"jsonrpc": "2.0", "id": probe.ID,
				"result": map[string]any{"content": []any{}, "resultType": "input_required"},
			})
			return
		}
		mcpHandler.ServeHTTP(w, r)
	})
	httpServer := httptest.NewServer(handler)
	t.Cleanup(httpServer.Close)
	dir := t.TempDir()
	path := writeHTTPConfig(t, dir, httpServer.URL, nil)
	manager := NewManager(Options{Input: Input{UserPath: path, Cwd: dir, ReadAvailable: true}})
	manager.Start()
	t.Cleanup(func() {
		manager.Stop()
		manager.Wait()
	})
	snapshot := waitHTTPServerState(t, manager, "http-fixture", serverStateReady)
	tool, ok := snapshot.Find("mcp__http_fixture__echo")
	if !ok {
		t.Fatal("echo tool missing")
	}
	result := manager.Call(context.Background(), tool, tools.Invocation{Name: tool.Name, Arguments: map[string]any{"text": "input"}})
	if !result.IsError || !strings.Contains(result.Content[0].Text, "interactive input") {
		t.Fatalf("input-required result = %+v", result)
	}
	if calls.Load() != 1 {
		t.Fatalf("input-required server calls = %d, want 1", calls.Load())
	}
}

func TestManagerHTTPConcurrentCallsKeepResultsIsolated(t *testing.T) {
	server := httpFixture(t)
	mcpHandler := sdk.NewStreamableHTTPHandler(func(*http.Request) *sdk.Server { return server }, nil)
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if rpcMethod(r) == "tools/call" {
			var probe struct {
				Params struct {
					Arguments map[string]any `json:"arguments"`
				} `json:"params"`
			}
			body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
			r.Body = io.NopCloser(bytes.NewReader(body))
			_ = json.Unmarshal(body, &probe)
			if probe.Params.Arguments["text"] == "fail" {
				http.Error(w, "synthetic failure", http.StatusInternalServerError)
				return
			}
		}
		mcpHandler.ServeHTTP(w, r)
	})
	httpServer := httptest.NewServer(handler)
	t.Cleanup(httpServer.Close)
	dir := t.TempDir()
	path := writeHTTPConfig(t, dir, httpServer.URL, nil)
	manager := NewManager(Options{Input: Input{UserPath: path, Cwd: dir, ReadAvailable: true}})
	manager.Start()
	t.Cleanup(func() {
		manager.Stop()
		manager.Wait()
	})
	snapshot := waitHTTPServerState(t, manager, "http-fixture", serverStateReady)
	tool, ok := snapshot.Find("mcp__http_fixture__echo")
	if !ok {
		t.Fatal("echo tool missing")
	}
	var wg sync.WaitGroup
	results := make(chan tools.Result, 2)
	wg.Add(2)
	go func() {
		defer wg.Done()
		results <- manager.Call(context.Background(), tool, tools.Invocation{Name: tool.Name, Arguments: map[string]any{"text": "good"}})
	}()
	go func() {
		defer wg.Done()
		results <- manager.Call(context.Background(), tool, tools.Invocation{Name: tool.Name, Arguments: map[string]any{"text": "fail"}})
	}()
	wg.Wait()
	close(results)
	var successes, failures int
	for result := range results {
		if result.IsError {
			failures++
			if !strings.Contains(result.Content[0].Text, "server") {
				t.Fatalf("failure lost its HTTP status class: %+v", result)
			}
		} else {
			successes++
		}
	}
	if successes != 1 || failures != 1 {
		t.Fatalf("concurrent results crossed: successes=%d failures=%d", successes, failures)
	}
}

func TestManagerHTTPDiscovery408And429Retry(t *testing.T) {
	for _, status := range []int{http.StatusRequestTimeout, http.StatusTooManyRequests, http.StatusNotImplemented} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			server := httpFixture(t)
			mcpHandler := sdk.NewStreamableHTTPHandler(func(*http.Request) *sdk.Server { return server }, nil)
			var attempts atomic.Int32
			handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if rpcMethod(r) == "initialize" {
					if attempts.Add(1) == 1 {
						http.Error(w, "retry", status)
						return
					}
				}
				mcpHandler.ServeHTTP(w, r)
			})
			httpServer := httptest.NewServer(handler)
			t.Cleanup(httpServer.Close)
			dir := t.TempDir()
			path := writeHTTPConfig(t, dir, httpServer.URL, nil)
			manager := NewManager(Options{Input: Input{UserPath: path, Cwd: dir, ReadAvailable: true}, sleep: func(context.Context, time.Duration) bool { return true }})
			manager.Start()
			t.Cleanup(func() {
				manager.Stop()
				manager.Wait()
			})
			waitHTTPServerState(t, manager, "http-fixture", serverStateReady)
			if attempts.Load() != 2 {
				t.Fatalf("initialize attempts = %d, want 2", attempts.Load())
			}
		})
	}
}

func TestManagerHTTPRetryAfterDate(t *testing.T) {
	server := httpFixture(t)
	mcpHandler := sdk.NewStreamableHTTPHandler(func(*http.Request) *sdk.Server { return server }, nil)
	var attempts atomic.Int32
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if rpcMethod(r) == "initialize" && attempts.Add(1) == 1 {
			w.Header().Set("Retry-After", time.Now().Add(3*time.Second).UTC().Format(http.TimeFormat))
			http.Error(w, "retry", http.StatusTooManyRequests)
			return
		}
		mcpHandler.ServeHTTP(w, r)
	})
	httpServer := httptest.NewServer(handler)
	t.Cleanup(httpServer.Close)
	dir := t.TempDir()
	path := writeHTTPConfig(t, dir, httpServer.URL, nil)
	var delays []time.Duration
	manager := NewManager(Options{Input: Input{UserPath: path, Cwd: dir, ReadAvailable: true}, sleep: func(_ context.Context, delay time.Duration) bool {
		delays = append(delays, delay)
		return true
	}})
	manager.Start()
	t.Cleanup(func() {
		manager.Stop()
		manager.Wait()
	})
	waitHTTPServerState(t, manager, "http-fixture", serverStateReady)
	if len(delays) != 1 || delays[0] < 2*time.Second || delays[0] > 3*time.Second {
		t.Fatalf("HTTP-date Retry-After delays = %v", delays)
	}
}

func TestManagerHTTPRetryAfterBudgetStops(t *testing.T) {
	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts.Add(1)
		w.Header().Set("Retry-After", "5")
		http.Error(w, "retry later", http.StatusServiceUnavailable)
	}))
	t.Cleanup(server.Close)
	dir := t.TempDir()
	path := writeHTTPConfig(t, dir, server.URL, nil) // 5s setup, so a 5s delay cannot leave time for a retry.
	slept := false
	manager := NewManager(Options{Input: Input{UserPath: path, Cwd: dir, ReadAvailable: true}, sleep: func(context.Context, time.Duration) bool {
		slept = true
		return true
	}})
	manager.Start()
	t.Cleanup(func() { manager.Stop(); manager.Wait() })
	waitHTTPServerState(t, manager, "http-fixture", serverStateFailed)
	if attempts.Load() != 1 || slept {
		t.Fatalf("retry without enough budget: %d requests, waited: %v", attempts.Load(), slept)
	}
}

func TestManagerHTTPRejectsPermanentRedirect(t *testing.T) {
	var secondHits atomic.Int32
	second := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		secondHits.Add(1)
		http.Error(w, "should not be reached", 500)
	}))
	t.Cleanup(second.Close)
	first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, second.URL+r.URL.Path, http.StatusPermanentRedirect)
	}))
	t.Cleanup(first.Close)
	dir := t.TempDir()
	path := writeHTTPConfig(t, dir, first.URL, nil)
	manager := NewManager(Options{Input: Input{UserPath: path, Cwd: dir, ReadAvailable: true}})
	manager.Start()
	t.Cleanup(func() {
		manager.Stop()
		manager.Wait()
	})
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		for _, status := range manager.Snapshot().Servers {
			if status.State == serverStateFailed {
				if secondHits.Load() != 0 {
					t.Fatalf("permanent redirect target was reached %d times", secondHits.Load())
				}
				return
			}
		}
		waitForHTTPSnapshotChange(t, manager, deadline)
	}
	t.Fatalf("permanent redirect did not fail: %+v", manager.Snapshot().Servers)
}

func TestManagerHTTPSSEResultThenOpenBody(t *testing.T) {
	release := make(chan struct{})
	defer close(release)
	server := httpFixture(t)
	mcpHandler := sdk.NewStreamableHTTPHandler(func(*http.Request) *sdk.Server { return server }, nil)
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method := rpcMethod(r)
		if method == "tools/call" {
			var probe struct {
				ID any `json:"id"`
			}
			body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
			_ = json.Unmarshal(body, &probe)
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			fmt.Fprintf(w, "event: message\ndata: {\"jsonrpc\":\"2.0\",\"id\":%s,\"result\":{\"content\":[{\"type\":\"text\",\"text\":\"sse-result\"}]}}\n\n", jsonID(probe.ID))
			if flusher, ok := w.(http.Flusher); ok {
				flusher.Flush()
			}
			select {
			case <-r.Context().Done():
			case <-release:
			}
			return
		}
		mcpHandler.ServeHTTP(w, r)
	})
	httpServer := httptest.NewServer(handler)
	t.Cleanup(httpServer.Close)
	dir := t.TempDir()
	path := writeHTTPConfig(t, dir, httpServer.URL, nil)
	manager := NewManager(Options{Input: Input{UserPath: path, Cwd: dir, ReadAvailable: true}})
	manager.Start()
	t.Cleanup(func() {
		manager.Stop()
		manager.Wait()
	})
	snapshot := waitHTTPServerState(t, manager, "http-fixture", serverStateReady)
	tool, ok := snapshot.Find("mcp__http_fixture__echo")
	if !ok {
		t.Fatal("echo tool missing")
	}
	// The server cannot supply EOF until the client closes the body or this
	// test releases its gate. A complete result must settle without EOF.
	result := manager.Call(context.Background(), tool, tools.Invocation{Name: tool.Name, Arguments: map[string]any{"text": "sse"}})
	if result.IsError || result.Content[0].Text != "sse-result" {
		t.Fatalf("SSE result = %+v", result)
	}
	result = manager.Call(context.Background(), tool, tools.Invocation{Name: tool.Name, Arguments: map[string]any{"text": "again"}})
	if result.IsError || result.Content[0].Text != "sse-result" {
		t.Fatalf("follow-up SSE result = %+v", result)
	}
}

func jsonID(value any) string {
	data, _ := json.Marshal(value)
	return string(data)
}

func TestManagerHTTPConcurrentCancellationDoesNotCorruptSuccess(t *testing.T) {
	entered := make(chan struct{})
	requestCanceled := make(chan struct{})
	releaseRequest := make(chan struct{})
	defer close(releaseRequest)
	var heldFirstCall atomic.Bool
	server := httpFixture(t)
	mcpHandler := sdk.NewStreamableHTTPHandler(func(*http.Request) *sdk.Server { return server }, nil)
	httpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Hold the first HTTP call outside the SDK server's serial handler queue.
		// The test must not rely on best-effort MCP cancellation to let echo run.
		if r.Method == http.MethodPost && rpcMethod(r) == "tools/call" && !heldFirstCall.Swap(true) {
			w.Header().Set("Content-Type", "text/event-stream")
			w.(http.Flusher).Flush()
			close(entered)
			select {
			case <-r.Context().Done():
				close(requestCanceled)
			case <-releaseRequest:
			}
			return
		}
		mcpHandler.ServeHTTP(w, r)
	}))
	t.Cleanup(httpServer.Close)
	dir := t.TempDir()
	path := writeHTTPConfig(t, dir, httpServer.URL, nil)
	manager := NewManager(Options{Input: Input{UserPath: path, Cwd: dir, ReadAvailable: true}})
	manager.Start()
	t.Cleanup(func() {
		manager.Stop()
		manager.Wait()
	})
	snapshot := waitHTTPServerState(t, manager, "http-fixture", serverStateReady)
	refresh, ok := snapshot.Find("mcp__http_fixture__refresh")
	if !ok {
		t.Fatal("refresh tool missing")
	}
	echo, ok := snapshot.Find("mcp__http_fixture__echo")
	if !ok {
		t.Fatal("echo tool missing")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan tools.Result, 1)
	go func() {
		done <- manager.Call(ctx, refresh, tools.Invocation{Name: refresh.Name})
	}()
	select {
	case <-entered:
	case <-time.After(10 * time.Second):
		t.Fatal("server did not enter the call before cancellation")
	}
	cancel()
	result := manager.Call(context.Background(), echo, tools.Invocation{Name: echo.Name, Arguments: map[string]any{"text": "survives"}})
	if result.IsError || result.Content[0].Text == "" {
		t.Fatalf("successful call was corrupted by cancellation: %+v", result)
	}
	select {
	case cancelled := <-done:
		if !cancelled.IsError || len(cancelled.Content) != 1 || !strings.Contains(cancelled.Content[0].Text, "was cancelled") {
			t.Fatalf("cancelled HTTP call = %+v", cancelled)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("cancelled tool call did not return")
	}
	select {
	case <-requestCanceled:
	case <-time.After(10 * time.Second):
		t.Fatal("cancelled tool call left its HTTP response open")
	}
}

func TestManagerHTTPDiscoveryOverflowLatchesBeforeSDKFailure(t *testing.T) {
	server := httpFixture(t)
	mcpHandler := sdk.NewStreamableHTTPHandler(func(*http.Request) *sdk.Server { return server }, nil)
	var initializes atomic.Int32
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method := rpcMethod(r)
		if method == "initialize" {
			initializes.Add(1)
		}
		if method == "tools/list" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(bytes.Repeat([]byte("x"), maxHTTPBody+1))
			return
		}
		mcpHandler.ServeHTTP(w, r)
	})
	httpServer := httptest.NewServer(handler)
	t.Cleanup(httpServer.Close)
	dir := t.TempDir()
	path := writeHTTPConfig(t, dir, httpServer.URL, nil)
	manager := NewManager(Options{Input: Input{UserPath: path, Cwd: dir, ReadAvailable: true}, sleep: func(context.Context, time.Duration) bool { return true }})
	manager.Start()
	t.Cleanup(func() {
		manager.Stop()
		manager.Wait()
	})
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		for _, status := range manager.Snapshot().Servers {
			if status.State == serverStateFailed {
				if status.ErrorClass != "limit" {
					t.Fatalf("overflow failure class = %q", status.ErrorClass)
				}
				if initializes.Load() != 1 {
					t.Fatalf("overflow retried %d times", initializes.Load())
				}
				return
			}
		}
		waitForHTTPSnapshotChange(t, manager, deadline)
	}
	t.Fatalf("overflow did not fail as a limit: %+v", manager.Snapshot().Servers)
}

func TestManagerHTTPLiveBodyOverflowDoesNotReconnect(t *testing.T) {
	server := httpFixture(t)
	handler := sdk.NewStreamableHTTPHandler(func(*http.Request) *sdk.Server { return server }, nil)
	var initializes atomic.Int32
	httpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch rpcMethod(r) {
		case "initialize":
			initializes.Add(1)
		case "tools/call":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(bytes.Repeat([]byte(" "), maxHTTPBody+1))
			return
		}
		handler.ServeHTTP(w, r)
	}))
	t.Cleanup(httpServer.Close)
	dir := t.TempDir()
	manager := NewManager(Options{Input: Input{UserPath: writeHTTPConfig(t, dir, httpServer.URL, nil), Cwd: dir}})
	manager.Start()
	t.Cleanup(func() { manager.Stop(); manager.Wait() })
	snapshot := waitHTTPServerState(t, manager, "http-fixture", serverStateReady)
	tool, ok := snapshot.Find("mcp__http_fixture__echo")
	if !ok {
		t.Fatal("echo tool missing")
	}
	result := manager.Call(t.Context(), tool, tools.Invocation{Name: tool.Name})
	if !result.IsError || len(result.Content) != 1 || !strings.Contains(result.Content[0].Text, "limit") {
		t.Fatalf("oversize response did not produce a limit error: %+v", result)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		snapshot = manager.Snapshot()
		status := snapshot.Servers[0]
		if status.State == serverStateFailed {
			if status.ErrorClass != "limit" || len(snapshot.Tools) != 0 {
				t.Fatalf("oversize response did not withdraw the tools as a limit failure: %+v", status)
			}
			return
		}
		if status.State == serverStateReconnecting || initializes.Load() != 1 {
			t.Fatalf("body overflow triggered reconnection: %+v", status)
		}
		waitForHTTPSnapshotChange(t, manager, deadline)
	}
}

func TestManagerHTTPOversizeSSEEventIsBounded(t *testing.T) {
	server := httpFixture(t)
	mcpHandler := sdk.NewStreamableHTTPHandler(func(*http.Request) *sdk.Server { return server }, nil)
	var gets atomic.Int32
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			gets.Add(1)
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = w.Write([]byte("event: message\ndata: " + strings.Repeat("x", maxHTTPBody+1) + "\n\n"))
			return
		}
		mcpHandler.ServeHTTP(w, r)
	})
	httpServer := httptest.NewServer(handler)
	t.Cleanup(httpServer.Close)
	dir := t.TempDir()
	path := writeHTTPConfig(t, dir, httpServer.URL, nil)
	manager := NewManager(Options{Input: Input{UserPath: path, Cwd: dir, ReadAvailable: true}, sleep: func(context.Context, time.Duration) bool { return true }})
	manager.Start()
	t.Cleanup(func() {
		manager.Stop()
		manager.Wait()
	})
	waitHTTPServerState(t, manager, "http-fixture", serverStateFailed)
	if count := gets.Load(); count < 1 || count > maxDiscoveryTries*(maxReconnects+1) {
		t.Fatalf("oversize SSE GET attempts = %d; exceeded the lifecycle budget", count)
	}
}

func TestManagerHTTPSSEEventCapIsPerEvent(t *testing.T) {
	server := httpFixture(t)
	mcpHandler := sdk.NewStreamableHTTPHandler(func(*http.Request) *sdk.Server { return server }, nil)
	var gets atomic.Int32
	pong := make(chan string, 2)
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			gets.Add(1)
			w.Header().Set("Content-Type", "text/event-stream")
			// Each ping is below the 16 MiB event cap; together they exceed it.
			// Replies prove both frames were parsed, unlike ignored SSE comments.
			padding := strings.Repeat("x", 9<<20)
			for _, id := range []string{"first", "second"} {
				fmt.Fprintf(w, "event: message\ndata: {\"jsonrpc\":\"2.0\",\"id\":%q,\"method\":\"ping\",\"params\":{\"_meta\":{\"padding\":%q}}}\n\n", id, padding)
			}
			w.(http.Flusher).Flush()
			<-r.Context().Done()
			return
		}
		if r.Method == http.MethodPost && rpcMethod(r) == "" {
			var response struct {
				ID    string `json:"id"`
				Error any    `json:"error"`
			}
			if err := json.NewDecoder(r.Body).Decode(&response); err != nil || response.Error != nil {
				t.Errorf("ping reply = %+v, decode error = %v", response, err)
			}
			pong <- response.ID
			w.WriteHeader(http.StatusAccepted)
			return
		}
		mcpHandler.ServeHTTP(w, r)
	})
	httpServer := httptest.NewServer(handler)
	t.Cleanup(httpServer.Close)
	dir := t.TempDir()
	path := writeHTTPConfig(t, dir, httpServer.URL, nil)
	manager := NewManager(Options{Input: Input{UserPath: path, Cwd: dir, ReadAvailable: true}})
	manager.Start()
	t.Cleanup(func() {
		manager.Stop()
		manager.Wait()
	})
	waitHTTPServerState(t, manager, "http-fixture", serverStateReady)
	seen := map[string]bool{}
	for i := 0; i < cap(pong); i++ {
		select {
		case id := <-pong:
			seen[id] = true
		case <-time.After(10 * time.Second):
			t.Fatal("client did not acknowledge both bounded SSE events")
		}
	}
	if !seen["first"] || !seen["second"] || gets.Load() != 1 {
		t.Fatalf("bounded SSE pings = %v, GET attempts = %d", seen, gets.Load())
	}
	tool, ok := manager.Snapshot().Find("mcp__http_fixture__echo")
	if !ok {
		t.Fatal("tool missing after bounded SSE events")
	}
	if result := manager.Call(context.Background(), tool, tools.Invocation{Name: tool.Name, Arguments: map[string]any{"text": "ok"}}); result.IsError {
		t.Fatalf("call after bounded SSE events = %+v", result)
	}
}

func TestManagerHTTPSession404JSONBody(t *testing.T) {
	server := httpFixture(t)
	mcpHandler := sdk.NewStreamableHTTPHandler(func(*http.Request) *sdk.Server { return server }, nil)
	var loseSession atomic.Bool
	var initializes atomic.Int32
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if rpcMethod(r) == "initialize" {
			initializes.Add(1)
		}
		if r.Method == http.MethodPost && loseSession.Load() && r.Header.Get("Mcp-Session-Id") != "" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","error":{"code":-32001,"message":"session gone"}}`))
			return
		}
		mcpHandler.ServeHTTP(w, r)
	})
	httpServer := httptest.NewServer(handler)
	t.Cleanup(httpServer.Close)
	dir := t.TempDir()
	path := writeHTTPConfig(t, dir, httpServer.URL, nil)
	manager := NewManager(Options{Input: Input{UserPath: path, Cwd: dir, ReadAvailable: true}})
	manager.Start()
	t.Cleanup(func() {
		manager.Stop()
		manager.Wait()
	})
	snapshot := waitHTTPServerState(t, manager, "http-fixture", serverStateReady)
	tool, ok := snapshot.Find("mcp__http_fixture__echo")
	if !ok {
		t.Fatal("echo tool missing")
	}
	loseSession.Store(true)
	result := manager.Call(context.Background(), tool, tools.Invocation{Name: tool.Name, Arguments: map[string]any{"text": "lost"}})
	loseSession.Store(false)
	if !result.IsError || !strings.Contains(result.Content[0].Text, "session-lost") {
		t.Fatalf("JSON 404 result = %+v", result)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		for _, status := range manager.Snapshot().Servers {
			if status.State == serverStateReady && initializes.Load() == 2 {
				return
			}
		}
		waitForHTTPSnapshotChange(t, manager, deadline)
	}
	t.Fatalf("JSON 404 did not reconnect exactly once: initializes=%d status=%+v", initializes.Load(), manager.Snapshot().Servers)
}

func TestManagerHTTPLive429RetriesInPlace(t *testing.T) {
	server := httpFixture(t)
	mcpHandler := sdk.NewStreamableHTTPHandler(func(*http.Request) *sdk.Server { return server }, nil)
	var rateLimitOnce atomic.Bool
	var initializes atomic.Int32
	var listAttempts atomic.Int32
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method := rpcMethod(r)
		if method == "initialize" {
			initializes.Add(1)
		}
		if method == "tools/list" {
			listAttempts.Add(1)
			if rateLimitOnce.CompareAndSwap(true, false) {
				w.Header().Set("Retry-After", "1")
				http.Error(w, "slow down", http.StatusTooManyRequests)
				return
			}
		}
		mcpHandler.ServeHTTP(w, r)
	})
	httpServer := httptest.NewServer(handler)
	t.Cleanup(httpServer.Close)
	dir := t.TempDir()
	path := writeHTTPConfig(t, dir, httpServer.URL, nil)
	var delays []time.Duration
	manager := NewManager(Options{Input: Input{UserPath: path, Cwd: dir, ReadAvailable: true}, sleep: func(_ context.Context, delay time.Duration) bool {
		delays = append(delays, delay)
		return true
	}})
	manager.Start()
	t.Cleanup(func() {
		manager.Stop()
		manager.Wait()
	})
	snapshot := waitHTTPServerState(t, manager, "http-fixture", serverStateReady)
	refresh, ok := snapshot.Find("mcp__http_fixture__refresh")
	if !ok {
		t.Fatal("refresh tool missing")
	}
	rateLimitOnce.Store(true)
	if result := manager.Call(context.Background(), refresh, tools.Invocation{Name: refresh.Name}); result.IsError {
		t.Fatalf("refresh call = %+v", result)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, ok := manager.Snapshot().Find("mcp__http_fixture__dynamic_tool"); ok {
			if initializes.Load() != 1 {
				t.Fatalf("live 429 created a new session: initializes=%d", initializes.Load())
			}
			if listAttempts.Load() < 2 {
				t.Fatalf("live 429 did not retry tools/list: %d", listAttempts.Load())
			}
			retrySawHeader := false
			for _, delay := range delays {
				if delay >= time.Second {
					retrySawHeader = true
				}
			}
			if !retrySawHeader {
				t.Fatalf("live 429 Retry-After delays = %v", delays)
			}
			return
		}
		waitForHTTPSnapshotChange(t, manager, deadline)
	}
	t.Fatalf("live 429 did not recover catalog: %+v", manager.Snapshot().Servers)
}

func TestRequestObservationIsOneUse(t *testing.T) {
	obs := newRequestObservation("tools/call")
	obs.claim(http.StatusInternalServerError, "server")
	obs.claim(http.StatusUnauthorized, "auth")
	if class, status := obs.snapshot(); class != "server" || status != http.StatusInternalServerError {
		t.Fatalf("observation = %q %d", class, status)
	}
	obs.latch("limit")
	if class, _ := obs.snapshot(); class != "limit" {
		t.Fatalf("limit did not take precedence: %q", class)
	}
	obs.finish()
	obs.latch("auth")
	if class, _ := obs.snapshot(); class != "limit" {
		t.Fatalf("finished observation changed: %q", class)
	}
}

func TestHTTPObservationDoesNotLeakAcrossRequests(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Header.Get("X-Obs") {
		case "auth":
			http.Error(w, "unauthorized", http.StatusUnauthorized)
		default:
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{}`))
		}
	}))
	t.Cleanup(server.Close)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	conn := &connection{ctx: ctx, cancel: cancel, listenerChecked: make(chan struct{})}
	conn.errClass.Store("")
	transport := &headerRoundTripper{base: http.DefaultTransport, conn: conn}
	do := func(obs *requestObservation, header string, mark bool) *http.Response {
		t.Helper()
		requestCtx := withRequestObservation(context.Background(), obs)
		if mark && obs != nil {
			requestCtx = withOutboundObservation(requestCtx, obs)
		}
		req, err := http.NewRequestWithContext(requestCtx, http.MethodPost, server.URL, strings.NewReader(`{}`))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("X-Obs", header)
		resp, err := transport.RoundTrip(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		return resp
	}
	authObs := newRequestObservation("tools/call")
	do(authObs, "auth", true)
	if class, status := authObs.snapshot(); class != "auth" || status != http.StatusUnauthorized {
		t.Fatalf("auth observation = %q %d", class, status)
	}
	okObs := newRequestObservation("tools/call")
	do(okObs, "ok", true)
	if class, status := okObs.snapshot(); class != "" || status != http.StatusOK {
		t.Fatalf("success observation = %q %d", class, status)
	}
	cancelObs := newRequestObservation("tools/call")
	do(cancelObs, "auth", false)
	if class, status := cancelObs.snapshot(); class != "" || status != 0 {
		t.Fatalf("unmarked POST claimed observation: %q %d", class, status)
	}
	if class := conn.latchedClass(); class != "" {
		t.Fatalf("observed call latched the connection: %q", class)
	}
}

func TestManagerHTTPReloadResolvesEnvironmentValues(t *testing.T) {
	t.Setenv("MCP_TEST_TOKEN", "one")
	server := httpFixture(t)
	mcpHandler := sdk.NewStreamableHTTPHandler(func(*http.Request) *sdk.Server { return server }, nil)
	var lastHeader atomic.Value
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		lastHeader.Store(r.Header.Get("X-Token"))
		mcpHandler.ServeHTTP(w, r)
	})
	httpServer := httptest.NewServer(handler)
	t.Cleanup(httpServer.Close)
	dir := t.TempDir()
	path := writeHTTPConfig(t, dir, httpServer.URL, map[string]string{"X-Token": "${MCP_TEST_TOKEN}"})
	input := Input{UserPath: path, Cwd: dir, ReadAvailable: true}
	manager := NewManager(Options{Input: input})
	manager.Start()
	t.Cleanup(func() {
		manager.Stop()
		manager.Wait()
	})
	snapshot := waitHTTPServerState(t, manager, "http-fixture", serverStateReady)
	echo, ok := snapshot.Find("mcp__http_fixture__echo")
	if !ok {
		t.Fatal("echo tool missing")
	}
	if result := manager.Call(context.Background(), echo, tools.Invocation{Name: echo.Name, Arguments: map[string]any{"text": "one"}}); result.IsError {
		t.Fatalf("initial call = %+v", result)
	}
	if got, _ := lastHeader.Load().(string); got != "one" {
		t.Fatalf("initial header = %q", got)
	}
	t.Setenv("MCP_TEST_TOKEN", "two")
	manager.Reload(input)
	reloaded := false
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		snapshot = manager.Snapshot()
		replacement, ok := snapshot.Find("mcp__http_fixture__echo")
		if ok && replacement.Epoch > echo.Epoch {
			if status := snapshot.Servers[0]; status.State != serverStateReady {
				waitForHTTPSnapshotChange(t, manager, deadline)
				continue
			}
			echo = replacement
			reloaded = true
			break
		}
		waitForHTTPSnapshotChange(t, manager, deadline)
	}
	if !reloaded {
		t.Fatalf("reload did not replace the connection: %+v", manager.Snapshot().Servers)
	}
	if result := manager.Call(context.Background(), echo, tools.Invocation{Name: echo.Name, Arguments: map[string]any{"text": "two"}}); result.IsError {
		t.Fatalf("reloaded call = %+v", result)
	}
	if got, _ := lastHeader.Load().(string); got != "two" {
		t.Fatalf("reloaded header = %q, want two", got)
	}
	writeHTTPConfigEnabled(t, dir, httpServer.URL, map[string]string{"X-Token": "${MCP_TEST_TOKEN}"}, false)
	manager.Reload(input)
	disabled := false
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		statuses := manager.Snapshot().Servers
		if len(statuses) == 1 && statuses[0].State == serverStateDisabled {
			disabled = true
			break
		}
		waitForHTTPSnapshotChange(t, manager, deadline)
	}
	if !disabled {
		t.Fatalf("disable reload did not publish: %+v", manager.Snapshot().Servers)
	}
	t.Setenv("MCP_TEST_TOKEN", "three")
	writeHTTPConfigEnabled(t, dir, httpServer.URL, map[string]string{"X-Token": "${MCP_TEST_TOKEN}"}, true)
	manager.Reload(input)
	reloaded = false
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		snapshot = manager.Snapshot()
		replacement, ok := snapshot.Find("mcp__http_fixture__echo")
		if ok && replacement.Epoch > echo.Epoch && snapshot.Servers[0].State == serverStateReady {
			echo = replacement
			reloaded = true
			break
		}
		waitForHTTPSnapshotChange(t, manager, deadline)
	}
	if !reloaded {
		t.Fatalf("re-enable did not replace the connection: %+v", manager.Snapshot().Servers)
	}
	if result := manager.Call(context.Background(), echo, tools.Invocation{Name: echo.Name, Arguments: map[string]any{"text": "three"}}); result.IsError {
		t.Fatalf("re-enabled call = %+v", result)
	}
	if got, _ := lastHeader.Load().(string); got != "three" {
		t.Fatalf("re-enabled header = %q, want three", got)
	}
}

func TestManagerHTTPRejectsInvalidListenerStatus(t *testing.T) {
	server := httpFixture(t)
	mcpHandler := sdk.NewStreamableHTTPHandler(func(*http.Request) *sdk.Server { return server }, nil)
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			http.Error(w, "bad listener request", http.StatusBadRequest)
			return
		}
		mcpHandler.ServeHTTP(w, r)
	})
	httpServer := httptest.NewServer(handler)
	t.Cleanup(httpServer.Close)
	dir := t.TempDir()
	path := writeHTTPConfig(t, dir, httpServer.URL, nil)
	manager := NewManager(Options{Input: Input{UserPath: path, Cwd: dir, ReadAvailable: true}})
	manager.Start()
	t.Cleanup(func() {
		manager.Stop()
		manager.Wait()
	})
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		for _, status := range manager.Snapshot().Servers {
			if status.State == serverStateFailed {
				if status.ErrorClass != "listener" {
					t.Fatalf("invalid listener class = %q", status.ErrorClass)
				}
				return
			}
		}
		waitForHTTPSnapshotChange(t, manager, deadline)
	}
	t.Fatalf("invalid listener status did not fail: %+v", manager.Snapshot().Servers)
}

func TestManagerPolicyLossWithdrawsProjectSetup(t *testing.T) {
	release := make(chan struct{})
	stalled := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-release
	}))
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(stalled.Close)
	dir := t.TempDir()
	project := filepath.Join(dir, "project.json")
	data, _ := json.Marshal(map[string]any{"mcpServers": map[string]any{"project-fixture": map[string]any{
		"url": stalled.URL, "timeout": 30, "exposure": "direct",
	}}})
	if err := os.WriteFile(project, data, 0o600); err != nil {
		t.Fatal(err)
	}
	user := filepath.Join(dir, "mcp.json")
	manager := NewManager(Options{Input: Input{UserPath: user, ProjectPath: project, ProjectTrusted: true, Cwd: dir, ReadAvailable: true}})
	manager.Start()
	t.Cleanup(func() {
		manager.Stop()
		manager.Wait()
	})
	t.Cleanup(unblock)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && len(manager.Snapshot().Servers) == 0 {
		waitForHTTPSnapshotChange(t, manager, deadline)
	}
	if len(manager.Snapshot().Servers) == 0 {
		t.Fatal("project setup did not start")
	}
	if err := os.WriteFile(user, []byte(`{"mcpServers":`), 0o600); err != nil {
		t.Fatal(err)
	}
	manager.Reload(Input{UserPath: user, ProjectPath: project, ProjectTrusted: false, Cwd: dir, ReadAvailable: true})
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		for _, status := range manager.Snapshot().Servers {
			if status.ErrorClass == "permission" {
				if status.State != serverStateFailed {
					t.Fatalf("permission state = %s", status.State)
				}
				if _, ok := manager.Snapshot().Find("mcp__project_fixture__echo"); ok {
					t.Fatal("revoked project setup published tools")
				}
				unblock()
				return
			}
		}
		waitForHTTPSnapshotChange(t, manager, deadline)
	}
	t.Fatalf("policy loss did not withdraw project setup: %+v", manager.Snapshot().Servers)
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func TestHeaderRoundTripperCancelsInFlightRequest(t *testing.T) {
	started := make(chan struct{})
	canceled := make(chan struct{})
	base := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		close(started)
		<-req.Context().Done()
		close(canceled)
		return nil, req.Context().Err()
	})
	ctx, cancel := context.WithCancel(context.Background())
	conn := &connection{ctx: ctx, cancel: cancel, listenerChecked: make(chan struct{})}
	conn.errClass.Store("")
	transport := &headerRoundTripper{base: base, conn: conn}
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, "http://example.test/mcp", strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	go func() { _, _ = transport.RoundTrip(req) }()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("request did not start")
	}
	cancel()
	select {
	case <-canceled:
	case <-time.After(time.Second):
		t.Fatal("connection cancellation did not abort the request")
	}
}

func TestManagerHTTPUnchangedReloadKeepsConnection(t *testing.T) {
	server := httpFixture(t)
	mcpHandler := sdk.NewStreamableHTTPHandler(func(*http.Request) *sdk.Server { return server }, nil)
	var initializes atomic.Int32
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if rpcMethod(r) == "initialize" {
			initializes.Add(1)
		}
		mcpHandler.ServeHTTP(w, r)
	})
	httpServer := httptest.NewServer(handler)
	t.Cleanup(httpServer.Close)
	dir := t.TempDir()
	path := writeHTTPConfig(t, dir, httpServer.URL, nil)
	input := Input{UserPath: path, Cwd: dir, ReadAvailable: true}
	manager := NewManager(Options{Input: input})
	manager.Start()
	t.Cleanup(func() {
		manager.Stop()
		manager.Wait()
	})
	waitHTTPServerState(t, manager, "http-fixture", serverStateReady)
	if initializes.Load() != 1 {
		t.Fatalf("initial session count = %d", initializes.Load())
	}
	manager.Reload(input)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		snapshot := manager.Snapshot()
		if snapshot.Revision > 1 && len(snapshot.Servers) == 1 && snapshot.Servers[0].State == serverStateReady {
			if initializes.Load() != 1 {
				t.Fatalf("unchanged reload created a new session: %d", initializes.Load())
			}
			return
		}
		waitForHTTPSnapshotChange(t, manager, deadline)
	}
	t.Fatalf("reload did not publish: %+v", manager.Snapshot())
}

type closeTrackingBody struct {
	closed chan struct{}
	once   sync.Once
}

func (b *closeTrackingBody) Read([]byte) (int, error) { return 0, io.EOF }
func (b *closeTrackingBody) Close() error {
	b.once.Do(func() { close(b.closed) })
	return nil
}

func TestCancelBodyClosesOnRequestCancellation(t *testing.T) {
	body := &closeTrackingBody{closed: make(chan struct{})}
	requestCtx, cancel := context.WithCancel(context.Background())
	_ = newCancelBody(body, context.Background(), requestCtx, nil, nil)
	cancel()
	select {
	case <-body.closed:
	case <-time.After(time.Second):
		t.Fatal("request cancellation did not close the response body")
	}
}

func TestHeaderRoundTripperClassifiesListenerBeforeSignaling(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	classifying := make(chan struct{})
	resume := make(chan struct{})
	// Parsing an HTTP-date Retry-After gives the test a gate inside classification.
	manager := NewManager(Options{now: func() time.Time {
		close(classifying)
		<-resume
		return now
	}})
	t.Cleanup(manager.Stop)
	conn := &connection{manager: manager, ctx: t.Context(), listenerChecked: make(chan struct{})}
	conn.errClass.Store("")
	base := roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusTooManyRequests,
			Header:     http.Header{"Retry-After": {now.Add(time.Second).Format(http.TimeFormat)}},
			Body:       http.NoBody,
		}, nil
	})
	transport := &headerRoundTripper{base: base, conn: conn}
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://example.test/mcp", nil)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		resp, err := transport.RoundTrip(req)
		if err == nil {
			err = resp.Body.Close()
		}
		done <- err
	}()
	select {
	case <-classifying:
	case <-time.After(5 * time.Second):
		close(resume)
		t.Fatal("listener response did not reach classification")
	}
	select {
	case <-conn.listenerChecked:
		t.Error("listener signaled before the GET outcome was classified")
	default:
	}
	close(resume)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	select {
	case <-conn.listenerChecked:
		if class := conn.terminalClass(); class != "rate-limit" {
			t.Fatalf("listener class = %q, want rate-limit", class)
		}
	default:
		t.Fatal("listener did not signal after classifying the response")
	}
}

func TestHeaderRoundTripperMarksListenerNetworkFailure(t *testing.T) {
	base := roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("synthetic listener failure")
	})
	conn := &connection{ctx: context.Background(), cancel: func() {}, listenerChecked: make(chan struct{})}
	conn.errClass.Store("")
	transport := &headerRoundTripper{base: base, conn: conn}
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, "http://example.test/mcp", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := transport.RoundTrip(req); err == nil {
		t.Fatal("listener failure was swallowed")
	}
	if !conn.listenerUnavailable.Load() {
		t.Fatal("listener failure did not mark notifications unavailable")
	}
}
