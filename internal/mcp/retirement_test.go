package mcp

import (
	"context"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"psi/internal/tools"
)

// forwardedCallTransport pauses callers after their requests have reached the
// server. SDK reads remain independent, so replies and EOF can settle first.
type forwardedCallTransport struct {
	sdk.Transport
	entered chan<- struct{}
	release <-chan struct{}
}

func (t *forwardedCallTransport) Connect(ctx context.Context) (sdk.Connection, error) {
	conn, err := t.Transport.Connect(ctx)
	if err != nil {
		return nil, err
	}
	return &forwardedCallConnection{Connection: conn, entered: t.entered, release: t.release}, nil
}

type forwardedCallConnection struct {
	sdk.Connection
	entered chan<- struct{}
	release <-chan struct{}
}

func (c *forwardedCallConnection) Write(ctx context.Context, message jsonrpc.Message) error {
	err := c.Connection.Write(ctx, message)
	if request, ok := message.(*jsonrpc.Request); err == nil && ok && request.Method == "tools/call" {
		c.entered <- struct{}{}
		<-c.release
	}
	return err
}

func TestManagerRetirementPreservesCompletedReplies(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		manager := NewManager(Options{})
		t.Cleanup(func() { manager.Stop(); manager.Wait() })
		ctx, cancel := context.WithCancel(manager.ctx)
		t.Cleanup(cancel)
		tool := Tool{Name: "mcp__fixture__echo", RawName: "echo", Server: "fixture", Epoch: 1, Exposure: ExposureDirect}
		conn := &connection{manager: manager, server: "fixture", epoch: 1, ctx: ctx, cancel: cancel,
			permits: make(chan struct{}, maxCallsPerServer), tokens: map[string]*callState{}, catalog: []Tool{tool}}
		server := sdk.NewServer(&sdk.Implementation{Name: "fixture", Version: "1"}, nil)
		server.AddTool(sdkTool("echo", "Complete before exit", map[string]any{"type": "object"}), func(context.Context, *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
			return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: "final reply"}}}, nil
		})
		clientTransport, serverTransport := sdk.NewInMemoryTransports()
		serverSession, err := server.Connect(t.Context(), serverTransport, nil)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = serverSession.Close() })
		entered, writeGate := make(chan struct{}, maxCallsPerServer), make(chan struct{})
		releaseWrites := sync.OnceFunc(func() { close(writeGate) })
		defer releaseWrites()
		client := sdk.NewClient(&sdk.Implementation{Name: "test", Version: "1"}, &sdk.ClientOptions{
			Capabilities: &sdk.ClientCapabilities{}, MultiRoundTrip: &sdk.MultiRoundTripOptions{Disabled: true},
		})
		conn.session, err = client.Connect(t.Context(), &forwardedCallTransport{
			Transport: clientTransport, entered: entered, release: writeGate,
		}, &sdk.ClientSessionOptions{ProtocolVersion: "2025-11-25"})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = conn.session.Close() })
		manager.servers["fixture"] = &serverState{
			cfg:  Server{Name: "fixture", Enabled: true, Exposure: ExposureDirect, Timeout: time.Hour},
			conn: conn, catalog: conn.catalog, epoch: 1, status: serverStateReady,
			// Exhaust recovery so retirement does not start another fixture.
			reconnects: maxReconnects,
		}
		manager.order = []string{"fixture"}
		manager.publishLocked()
		watchDone := make(chan struct{})
		go func() { conn.watch(); close(watchDone) }()
		done := make(chan tools.Result, maxCallsPerServer)
		for range maxCallsPerServer {
			go func() { done <- manager.Call(t.Context(), tool, tools.Invocation{Name: tool.Name}) }()
		}
		for range maxCallsPerServer {
			<-entered
		}
		// With callers gated after Write, every SDK reply is processed before
		// EOF. This models a caller descheduled after its successful write.
		synctest.Wait()
		if err := serverSession.Close(); err != nil {
			t.Fatal(err)
		}
		<-watchDone
		synctest.Wait()
		if _, ok := manager.Snapshot().Find(tool.Name); ok || ctx.Err() == nil {
			t.Fatal("connection retirement did not withdraw and cancel the old session")
		}
		releaseWrites()
		for range maxCallsPerServer {
			result := <-done
			if result.IsError || len(result.Content) != 1 || result.Content[0].Text != "final reply" {
				t.Errorf("completed reply after retirement = %+v", result)
			}
		}
	})
}

func TestManagerStopCancelsUnsettledCall(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		entered, release := make(chan struct{}), make(chan struct{})
		defer close(release)
		server := sdk.NewServer(&sdk.Implementation{Name: "fixture", Version: "1"}, nil)
		server.AddTool(sdkTool("hang", "Ignore cancellation", map[string]any{"type": "object"}), func(context.Context, *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
			close(entered)
			<-release
			return &sdk.CallToolResult{}, nil
		})
		manager := NewManager(Options{})
		t.Cleanup(func() { manager.Stop(); manager.Wait() })
		snapshot := connectInMemoryServer(t, manager, "fixture", server, time.Hour)
		tool, ok := snapshot.Find("mcp__fixture__hang")
		if !ok {
			t.Fatal("hanging tool missing")
		}
		done := make(chan tools.Result, 1)
		go func() { done <- manager.Call(t.Context(), tool, tools.Invocation{Name: tool.Name}) }()
		<-entered
		manager.Stop()
		synctest.Wait()
		// The server is still gated and ignores cancellation. Stop must release
		// the caller without waiting for a reply or an idle/hard deadline.
		select {
		case result := <-done:
			if !result.IsError || len(result.Content) != 1 || !strings.Contains(result.Content[0].Text, "outcome may be unknown") {
				t.Fatalf("unfinished call after Stop = %+v", result)
			}
		default:
			t.Fatal("Stop left the caller waiting for the server")
		}
		if len(manager.callSlot) != 0 || len(manager.Snapshot().Tools) != 0 {
			t.Fatal("Stop retained call capacity or published tools")
		}
	})
}
