//go:build unix

package app

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// The control socket is separate from MCP stdin/stdout. Tests can observe a
// blocked launcher without releasing its gate or relying on a startup delay.
func TestMCPFailureProcess(t *testing.T) {
	if os.Getenv("PSI_MCP_FAILURE_PROCESS") != "1" || flag.Lookup("test.run").Value.String() != "^TestMCPFailureProcess$" {
		return
	}
	conn, err := net.Dial("tcp", os.Getenv("PSI_MCP_FAILURE_CONTROL"))
	if err != nil {
		os.Exit(2)
	}
	signal.Ignore(syscall.SIGTERM)
	var mu sync.Mutex
	report := func(event string) {
		mu.Lock()
		defer mu.Unlock()
		if json.NewEncoder(conn).Encode(event) != nil {
			os.Exit(2)
		}
	}
	start := make(chan struct{})
	go func() {
		reader := bufio.NewReader(conn)
		started := false
		for {
			command, err := reader.ReadByte()
			if err != nil || command == 'x' {
				os.Exit(0)
			}
			if command == 'p' {
				report("alive")
			}
			if command == 's' && !started {
				close(start)
				started = true
			}
		}
	}()
	report("started")
	<-start
	mode := os.Getenv("PSI_MCP_FAILURE_MODE")
	if mode == "exit" {
		os.Exit(23)
	}
	server := sdk.NewServer(&sdk.Implementation{Name: "failure-fixture", Version: "1"}, nil)
	server.AddTool(&sdk.Tool{Name: "fail", Description: "Controlled failure", InputSchema: map[string]any{"type": "object"}}, func(context.Context, *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
		report("called")
		if mode == "crash" {
			os.Exit(23)
		}
		// Deliberately ignore the request context and cancellation notification.
		<-make(chan struct{})
		return nil, nil
	})
	if err := server.Run(context.Background(), &sdk.StdioTransport{}); err != nil {
		os.Exit(1)
	}
	os.Exit(0)
}

type mcpFailureChild struct {
	conn net.Conn
	dec  *json.Decoder
}

func mcpFailureListener(t *testing.T) *net.TCPListener {
	t.Helper()
	listener, err := net.ListenTCP("tcp", &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	return listener
}

func acceptMCPFailureChild(t *testing.T, listener *net.TCPListener) *mcpFailureChild {
	t.Helper()
	// Deadlock watchdogs only. No test asserts a latency budget.
	if err := listener.SetDeadline(time.Now().Add(10 * time.Second)); err != nil {
		t.Fatal(err)
	}
	conn, err := listener.Accept()
	if err != nil {
		t.Fatal(err)
	}
	// Closing this private control socket is the fixture's fallback cleanup.
	// Tests must observe EOF before cleanup to prove psi stopped the process.
	t.Cleanup(func() { _ = conn.Close() })
	child := &mcpFailureChild{conn: conn, dec: json.NewDecoder(conn)}
	child.want(t, "started")
	return child
}

func (c *mcpFailureChild) want(t *testing.T, want string) {
	t.Helper()
	if err := c.conn.SetReadDeadline(time.Now().Add(10 * time.Second)); err != nil {
		t.Fatal(err)
	}
	var event string
	if err := c.dec.Decode(&event); err != nil || event != want {
		t.Fatalf("MCP child event = %q, %v; want %q", event, err, want)
	}
}

func (c *mcpFailureChild) start(t *testing.T) {
	t.Helper()
	if _, err := c.conn.Write([]byte("s")); err != nil {
		t.Fatal(err)
	}
}

func (c *mcpFailureChild) exited(t *testing.T) {
	t.Helper()
	if err := c.conn.SetReadDeadline(time.Now().Add(10 * time.Second)); err != nil {
		t.Fatal(err)
	}
	var event string
	if err := c.dec.Decode(&event); err != io.EOF {
		t.Fatalf("MCP child did not exit: event=%q error=%v", event, err)
	}
}

func writeMCPFailureConfig(t *testing.T, agentDir, mode, address string) {
	t.Helper()
	config := map[string]any{"mcpServers": map[string]any{"failure": map[string]any{
		"command": os.Args[0], "args": []string{"-test.run=^TestMCPFailureProcess$"},
		"env":     map[string]string{"PSI_MCP_FAILURE_PROCESS": "1", "PSI_MCP_FAILURE_CONTROL": address, "PSI_MCP_FAILURE_MODE": mode},
		"timeout": 300, "exposure": "direct",
	}}}
	data, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	writeAppFile(t, filepath.Join(agentDir, "mcp.json"), string(data))
}

func mcpFailureTextResponse(w http.ResponseWriter, text string) {
	w.Header().Set("Content-Type", "text/event-stream")
	fmt.Fprintf(w, "event: message_start\ndata: {\"message\":{\"id\":\"failure-test\",\"model\":\"m\",\"usage\":{\"input_tokens\":1}}}\n\n"+
		"event: content_block_start\ndata: {\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":%q}}\n\n"+
		"event: content_block_stop\ndata: {\"index\":0}\n\n"+
		"event: message_delta\ndata: {\"delta\":{\"stop_reason\":\"end_turn\"}}\n\nevent: message_stop\ndata: {}\n\n", text)
}

func TestActualAppMCPLauncherFailureDoesNotBlockChat(t *testing.T) {
	for _, mode := range []string{"exit", "hang"} {
		t.Run(mode, func(t *testing.T) {
			home, work := t.TempDir(), t.TempDir()
			agentDir := filepath.Join(home, "agent")
			listener := mcpFailureListener(t)
			requests := make(chan map[string]any, 2)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body map[string]any
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
					return
				}
				select {
				case requests <- body:
				case <-r.Context().Done():
					return
				}
				mcpFailureTextResponse(w, "CHAT_WITH_FAILED_MCP")
			}))
			t.Cleanup(server.Close)
			reviewSettings(t, agentDir, server.URL)
			writeMCPFailureConfig(t, agentDir, mode, listener.Addr().String())
			q := startReviewPTY(t, home, work, agentDir, "--deny-project", "--no-context-files", "--no-skills", "--no-themes")
			child := acceptMCPFailureChild(t, listener)
			q.prompt()
			q.write("/mcp\r")
			q.wait("connecting")
			q.prompt()
			if mode == "exit" {
				child.start(t)
				child.exited(t)
			}
			// For hang, initialization stays gated throughout this entire turn.
			q.write("chat despite launcher failure\r")
			q.wait("CHAT_WITH_FAILED_MCP")
			q.prompt()
			body := <-requests
			data, _ := json.Marshal(body["tools"])
			if strings.Contains(string(data), "mcp__failure__") {
				t.Fatalf("uninitialized tools reached provider: %s", data)
			}
			q.write("/mcp\r")
			q.wait("failure")
			q.prompt()
			q.write("/quit\r")
			q.finish()
			if mode == "hang" {
				child.exited(t)
			}
		})
	}
}
