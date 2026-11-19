//go:build acceptance && unix

package app

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

type httpAcceptanceProvider struct {
	mu        sync.Mutex
	calls     int
	toolCalls int
	toolsSeen bool
	done      bool
	failures  []string
}

func (p *httpAcceptanceProvider) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var body map[string]any
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad request", 400)
		return
	}
	p.mu.Lock()
	p.calls++
	toolDeclared := false
	if tools, ok := body["tools"].([]any); ok {
		for _, raw := range tools {
			tool, _ := raw.(map[string]any)
			if tool["name"] == "mcp__http_fixture__echo" {
				toolDeclared = true
			}
		}
	}
	if toolDeclared {
		p.toolsSeen = true
	}
	sawResult := false
	messages, _ := body["messages"].([]any)
	for _, raw := range messages {
		message, _ := raw.(map[string]any)
		if message["role"] != "user" {
			continue
		}
		content, _ := message["content"].([]any)
		for _, blockRaw := range content {
			block, _ := blockRaw.(map[string]any)
			if block["type"] != "tool_result" {
				continue
			}
			sawResult = true
			if !strings.Contains(blockText(block["content"]), "HTTP_ACCEPTANCE_TOOL") {
				p.failures = append(p.failures, fmt.Sprintf("tool result = %v", block["content"]))
			}
		}
	}
	call := p.calls
	if sawResult {
		p.done = true
	}
	p.mu.Unlock()
	w.Header().Set("Content-Type", "text/event-stream")
	switch {
	case sawResult:
		writeRPCProviderText(w, "HTTP_ACCEPTANCE_DONE")
	case toolDeclared:
		p.mu.Lock()
		p.toolCalls++
		p.mu.Unlock()
		writeRPCProviderTool(w, "toolu_http", "mcp__http_fixture__echo", map[string]any{"text": "hello"})
	default:
		writeRPCProviderText(w, "WAIT_"+fmt.Sprint(call))
	}
}

func writeRPCProviderTool(w http.ResponseWriter, id, name string, args map[string]any) {
	encoded, _ := json.Marshal(args)
	fmt.Fprint(w, "event: message_start\ndata: {\"message\":{\"id\":\"synthetic\",\"model\":\"m\",\"usage\":{\"input_tokens\":1}}}\n\n")
	fmt.Fprintf(w, "event: content_block_start\ndata: {\"index\":0,\"content_block\":{\"type\":\"tool_use\",\"id\":%q,\"name\":%q,\"input\":{}}}\n\n", id, name)
	fmt.Fprintf(w, "event: content_block_delta\ndata: {\"index\":0,\"delta\":{\"type\":\"input_json_delta\",\"partial_json\":%q}}\n\n", string(encoded))
	fmt.Fprint(w, "event: content_block_stop\ndata: {\"index\":0}\n\n")
	fmt.Fprint(w, "event: message_delta\ndata: {\"delta\":{\"stop_reason\":\"tool_use\"}}\n\n")
	fmt.Fprint(w, "event: message_stop\ndata: {}\n\n")
}

func writeRPCProviderText(w http.ResponseWriter, text string) {
	fmt.Fprint(w, "event: message_start\ndata: {\"message\":{\"id\":\"synthetic\",\"model\":\"m\",\"usage\":{\"input_tokens\":1}}}\n\n")
	fmt.Fprint(w, "event: content_block_start\ndata: {\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n")
	fmt.Fprintf(w, "event: content_block_delta\ndata: {\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":%q}}\n\n", text)
	fmt.Fprint(w, "event: content_block_stop\ndata: {\"index\":0}\n\n")
	fmt.Fprint(w, "event: message_delta\ndata: {\"delta\":{\"stop_reason\":\"end_turn\"}}\n\n")
	fmt.Fprint(w, "event: message_stop\ndata: {}\n\n")
}

func TestMCPHTTPAcceptance(t *testing.T) {
	if *reviewBinary == "" {
		t.Skip("requires -psi-test-binary for the delivered executable")
	}
	base := t.TempDir()
	home := filepath.Join(base, "home")
	agentDir := filepath.Join(base, "agent")
	work := filepath.Join(base, "work")
	tmp := filepath.Join(base, "tmp")
	for _, dir := range []string{home, agentDir, work, tmp} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	mcpServer := sdk.NewServer(&sdk.Implementation{Name: "http-fixture", Version: "1"}, nil)
	var toolCalls atomic.Int32
	mcpServer.AddTool(&sdk.Tool{Name: "echo", Description: "Echo", InputSchema: map[string]any{"type": "object"}}, func(context.Context, *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
		toolCalls.Add(1)
		return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: "HTTP_ACCEPTANCE_TOOL"}}}, nil
	})
	handler := sdk.NewStreamableHTTPHandler(func(*http.Request) *sdk.Server { return mcpServer }, nil)
	httpServer := httptest.NewServer(handler)
	t.Cleanup(httpServer.Close)

	provider := &httpAcceptanceProvider{}
	providerServer := httptest.NewServer(provider)
	t.Cleanup(providerServer.Close)

	writeAppFile(t, filepath.Join(agentDir, "settings.json"), `{"defaultProvider":"anthropic","defaultModel":"m","compaction":{"enabled":false}}`)
	writeAppFile(t, filepath.Join(agentDir, "models.json"), `{"providers":{"anthropic":{"baseUrl":"`+providerServer.URL+`/v1","models":[{"id":"m","maxTokens":4096,"contextWindow":100000}]}}}`)
	writeAppFile(t, filepath.Join(agentDir, "auth.json"), `{"anthropic":{"type":"api_key","key":"synthetic-http-key"}}`)
	writeAppFile(t, filepath.Join(agentDir, "mcp.json"), `{"mcpServers":{"http-fixture":{"url":"`+httpServer.URL+`","exposure":"direct","timeout":10}}}`)

	cmd := exec.Command(*reviewBinary, "--mode", "rpc", "--deny-project", "--no-context-files", "--no-skills", "--no-themes", "--no-session")
	cmd.Dir = work
	cmd.Env = append(appTestEnv(home, agentDir), "TMPDIR="+tmp, "TMP="+tmp, "TEMP="+tmp)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	stderrFile, err := os.CreateTemp(tmp, "rpc-stderr-*")
	if err != nil {
		t.Fatal(err)
	}
	defer stderrFile.Close()
	cmd.Stderr = stderrFile
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	t.Cleanup(func() {
		_ = stdin.Close()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			_ = cmd.Process.Kill()
			<-done
			t.Fatalf("rpc did not exit after stdin EOF; stderr=%s", readTestFile(stderrFile.Name()))
		}
	})

	events := make(chan string, 256)
	go func() {
		scanner := bufio.NewScanner(stdout)
		scanner.Buffer(make([]byte, 64*1024), 10*1024*1024)
		for scanner.Scan() {
			events <- scanner.Text()
		}
		close(events)
	}()
	watchdog := time.NewTimer(45 * time.Second)
	defer watchdog.Stop()
	settled := func() bool {
		for {
			select {
			case line, ok := <-events:
				if !ok {
					t.Fatalf("rpc exited early: %s", readTestFile(stderrFile.Name()))
				}
				var event map[string]any
				if json.Unmarshal([]byte(line), &event) == nil && event["type"] == "agent_settled" {
					return true
				}
			case <-watchdog.C:
				t.Fatalf("rpc acceptance watchdog: %s", readTestFile(stderrFile.Name()))
			}
		}
	}
	for turn := 0; turn < 32; turn++ {
		if _, err := io.WriteString(stdin, fmt.Sprintf(`{"id":"p%d","type":"prompt","message":"readiness turn %d"}`+"\n", turn, turn)); err != nil {
			t.Fatal(err)
		}
		settled()
		provider.mu.Lock()
		finished := provider.done && provider.toolsSeen
		provider.mu.Unlock()
		if finished {
			break
		}
	}
	provider.mu.Lock()
	calls, seen, finished, failures := provider.calls, provider.toolsSeen, provider.done, append([]string(nil), provider.failures...)
	provider.mu.Unlock()
	if !seen {
		t.Fatalf("provider never saw an MCP declaration after %d turns", calls)
	}
	if !finished {
		t.Fatalf("MCP call did not complete after %d provider requests", calls)
	}
	if toolCalls.Load() != 1 {
		t.Fatalf("server tool calls = %d, want 1", toolCalls.Load())
	}
	for _, failure := range failures {
		t.Error(failure)
	}
}

func readTestFile(path string) string {
	data, _ := os.ReadFile(path)
	return string(data)
}
