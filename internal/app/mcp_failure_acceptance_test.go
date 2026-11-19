//go:build acceptance && unix

package app

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hypernewbie/psi/internal/session"
)

// This delivered-app acceptance check observes asynchronous catalog publication
// through /mcp. Unlike the default-suite startup tests, it needs readiness polling.
func TestActualAppMCPCallFailureKeepsSessionUsable(t *testing.T) {
	for _, mode := range []string{"crash", "hang", "reply-exit"} {
		t.Run(mode, func(t *testing.T) {
			home, work := t.TempDir(), t.TempDir()
			agentDir := filepath.Join(home, "agent")
			sessions := filepath.Join(home, "sessions")
			listener := mcpFailureListener(t)
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body struct {
					Tools []struct{ Name string } `json:"tools"`
				}
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
					return
				}
				n := calls.Add(1)
				if n != 1 {
					mcpFailureTextResponse(w, fmt.Sprintf("MCP_CHAT_%d", n))
					return
				}
				declared := false
				for _, tool := range body.Tools {
					declared = declared || tool.Name == "mcp__failure__fail"
				}
				if !declared {
					t.Error("failure tool was not declared")
				}
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprint(w, "event: message_start\ndata: {\"message\":{\"id\":\"failure-test\",\"model\":\"m\",\"usage\":{\"input_tokens\":1}}}\n\n"+
					"event: content_block_start\ndata: {\"index\":0,\"content_block\":{\"type\":\"tool_use\",\"id\":\"failure-call\",\"name\":\"mcp__failure__fail\",\"input\":{}}}\n\n"+
					"event: content_block_stop\ndata: {\"index\":0}\n\n"+
					"event: message_delta\ndata: {\"delta\":{\"stop_reason\":\"tool_use\"}}\n\nevent: message_stop\ndata: {}\n\n")
			}))
			t.Cleanup(server.Close)
			reviewSettings(t, agentDir, server.URL)
			writeMCPFailureConfig(t, agentDir, mode, listener.Addr().String())
			q := startReviewPTY(t, home, work, agentDir, "--deny-project", "--no-context-files", "--no-skills", "--no-themes", "--session-dir", sessions)
			child := acceptMCPFailureChild(t, listener)
			child.start(t)
			q.prompt()
			waitMCPFailureReady(t, q)
			q.write("run the failure tool\r")
			child.want(t, "called")

			lastReply := 2
			var replacement *mcpFailureChild
			if mode == "hang" {
				q.write("\x1b")
				q.wait("cancelled")
				q.prompt()
				// The server still ignores cancellation. The prompt must return
				// before the server exits or the five-minute timeout expires.
				if _, err := child.conn.Write([]byte("p")); err != nil {
					t.Fatal(err)
				}
				child.want(t, "alive")
			} else {
				child.exited(t)
				replacement = acceptMCPFailureChild(t, listener)
				// Reconnect initialization remains gated. No completed call can
				// be replayed while the original turn settles.
				q.wait("MCP_CHAT_2")
				q.prompt()
				lastReply = 3
			}
			q.write("/mcp\r")
			q.wait("failure")
			q.prompt()
			q.write("ordinary chat after failure\r")
			q.wait(fmt.Sprintf("MCP_CHAT_%d", lastReply))
			q.prompt()
			q.write("/quit\r")
			q.finish()
			if replacement != nil {
				replacement.exited(t)
			} else {
				child.exited(t)
			}
			if calls.Load() != int32(lastReply) {
				t.Fatalf("provider requests = %d; want %d", calls.Load(), lastReply)
			}
			assertMCPCallSettled(t, sessions, mode)
		})
	}
}

func waitMCPFailureReady(t *testing.T, q *reviewPTY) {
	t.Helper()
	deadline := time.NewTimer(15 * time.Second)
	defer deadline.Stop()
	for {
		select {
		case <-deadline.C:
			t.Fatal("failure fixture never became ready")
		default:
		}
		q.write("/mcp\r")
		q.wait("mcp:")
		q.prompt()
		for _, line := range strings.Split(q.screen.Text(), "\n") {
			fields := strings.Fields(line)
			if len(fields) >= 3 && fields[0] == "failure" && fields[1] == "stdio" && fields[2] == "ready" {
				return
			}
		}
	}
}

func assertMCPCallSettled(t *testing.T, sessions, mode string) {
	t.Helper()
	store, err := session.OpenFile(findSessionFile(t, sessions), "", false)
	if err != nil {
		t.Fatal(err)
	}
	projection, err := store.Projection()
	if err != nil {
		t.Fatal(err)
	}
	for _, message := range projection.Flat {
		result := message.ToolResult
		if message.Role != "toolResult" || result == nil || result.ToolCallID != "failure-call" {
			continue
		}
		want, wantError := "cancelled", true
		switch mode {
		case "crash":
			want = "outcome may be unknown"
		case "reply-exit":
			want, wantError = "MCP_FINAL_REPLY", false
		}
		if result.IsError != wantError {
			t.Fatalf("tool result error = %v; want %v: %+v", result.IsError, wantError, result)
		}
		encoded, _ := json.Marshal(result.Content)
		if !strings.Contains(string(encoded), want) {
			t.Fatalf("tool result lacks %q: %s", want, encoded)
		}
		return
	}
	t.Fatal("MCP tool never settled in durable history")
}
