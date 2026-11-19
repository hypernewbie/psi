//go:build acceptance && unix

package app

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

var mcpTestLauncher = flag.String("mcp-test-launcher", "", "Absolute npx path for the pinned Chrome DevTools MCP package")
var mcpTestBrowser = flag.String("mcp-test-browser", "", "Absolute Chrome executable for the isolated acceptance profile")

type chromeProviderStub struct {
	mu         sync.Mutex
	calls      int
	declared   map[string]bool
	pageToken  string
	pageURL    string
	sawImage   bool
	imageBytes int
	failures   []string
}

func (p *chromeProviderStub) fail(format string, args ...any) {
	p.mu.Lock()
	p.failures = append(p.failures, fmt.Sprintf(format, args...))
	p.mu.Unlock()
}

func (p *chromeProviderStub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var body map[string]any
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		p.fail("decode request: %v", err)
		http.Error(w, "bad request", 400)
		return
	}
	p.mu.Lock()
	p.calls++
	call := p.calls
	if call == 1 {
		p.declared = map[string]bool{}
		if tools, ok := body["tools"].([]any); ok {
			for _, raw := range tools {
				tool, _ := raw.(map[string]any)
				name, _ := tool["name"].(string)
				if strings.HasPrefix(name, "mcp__") {
					p.declared[name] = true
				}
			}
		}
	}
	p.mu.Unlock()
	p.inspectToolResult(call, body)
	w.Header().Set("Content-Type", "text/event-stream")
	switch call {
	case 1:
		p.writeToolUse(w, "toolu_nav", "mcp__chrome_devtools__navigate_page", map[string]any{"url": p.pageURL})
	case 2:
		p.writeToolUse(w, "toolu_snap", "mcp__chrome_devtools__take_snapshot", map[string]any{})
	case 3:
		p.writeToolUse(w, "toolu_shot", "mcp__chrome_devtools__take_screenshot", map[string]any{})
	default:
		p.writeText(w, "ACCEPTANCE_DONE")
	}
}

func (p *chromeProviderStub) inspectToolResult(call int, body map[string]any) {
	messages, _ := body["messages"].([]any)
	for i := len(messages) - 1; i >= 0; i-- {
		message, _ := messages[i].(map[string]any)
		content, _ := message["content"].([]any)
		for j := len(content) - 1; j >= 0; j-- {
			block, _ := content[j].(map[string]any)
			if block["type"] != "tool_result" {
				continue
			}
			if isError, _ := block["is_error"].(bool); isError {
				p.fail("tool result %d is an error: %v", call, block["content"])
			}
			switch call {
			case 3:
				text := blockText(block["content"])
				if !strings.Contains(text, p.pageToken) {
					p.fail("snapshot result missing page token %q: %q", p.pageToken, text)
				}
			case 4:
				if data, media := blockImage(block["content"]); data > 0 {
					p.sawImage = true
					p.imageBytes = data
					if media != "image/png" && media != "image/jpeg" && media != "image/webp" && media != "image/gif" {
						p.fail("screenshot MIME = %q", media)
					}
				}
			}
			return
		}
	}
}

func blockText(content any) string {
	switch value := content.(type) {
	case string:
		return value
	case []any:
		var b strings.Builder
		for _, raw := range value {
			block, _ := raw.(map[string]any)
			if block["type"] == "text" {
				text, _ := block["text"].(string)
				b.WriteString(text)
			}
		}
		return b.String()
	}
	return ""
}

func blockImage(content any) (int, string) {
	blocks, _ := content.([]any)
	for _, raw := range blocks {
		block, _ := raw.(map[string]any)
		if block["type"] != "image" {
			continue
		}
		source, _ := block["source"].(map[string]any)
		media, _ := source["media_type"].(string)
		data, _ := source["data"].(string)
		return len(data), media
	}
	return 0, ""
}

func (p *chromeProviderStub) writeToolUse(w http.ResponseWriter, id, name string, input map[string]any) {
	encoded, _ := json.Marshal(input)
	fmt.Fprint(w, "event: message_start\ndata: {\"message\":{\"id\":\"synthetic\",\"model\":\"m\",\"usage\":{\"input_tokens\":1}}}\n\n")
	fmt.Fprintf(w, "event: content_block_start\ndata: {\"index\":0,\"content_block\":{\"type\":\"tool_use\",\"id\":%q,\"name\":%q,\"input\":{}}}\n\n", id, name)
	fmt.Fprintf(w, "event: content_block_delta\ndata: {\"index\":0,\"delta\":{\"type\":\"input_json_delta\",\"partial_json\":%q}}\n\n", string(encoded))
	fmt.Fprint(w, "event: content_block_stop\ndata: {\"index\":0}\n\n")
	fmt.Fprint(w, "event: message_delta\ndata: {\"delta\":{\"stop_reason\":\"tool_use\"}}\n\n")
	fmt.Fprint(w, "event: message_stop\ndata: {}\n\n")
}

func (p *chromeProviderStub) writeText(w http.ResponseWriter, text string) {
	fmt.Fprint(w, "event: message_start\ndata: {\"message\":{\"id\":\"synthetic\",\"model\":\"m\",\"usage\":{\"input_tokens\":1}}}\n\n")
	fmt.Fprint(w, "event: content_block_start\ndata: {\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n")
	fmt.Fprintf(w, "event: content_block_delta\ndata: {\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":%q}}\n\n", text)
	fmt.Fprint(w, "event: content_block_stop\ndata: {\"index\":0}\n\n")
	fmt.Fprint(w, "event: message_delta\ndata: {\"delta\":{\"stop_reason\":\"end_turn\"}}\n\n")
	fmt.Fprint(w, "event: message_stop\ndata: {}\n\n")
}

func TestMCPChromeAcceptance(t *testing.T) {
	if testing.Short() {
		t.Skip("acceptance test")
	}
	if *reviewBinary == "" {
		t.Skip("requires -psi-test-binary for the delivered executable")
	}
	if *mcpTestLauncher == "" || *mcpTestBrowser == "" {
		t.Skip("requires -mcp-test-launcher and -mcp-test-browser")
	}
	base := t.TempDir()
	home := filepath.Join(base, "home")
	agentDir := filepath.Join(base, "agent")
	work := filepath.Join(base, "work")
	tmp := filepath.Join(base, "tmp")
	npmCache := filepath.Join(base, "npm-cache")
	profile := filepath.Join(base, "browser-profile")
	sessions := filepath.Join(base, "sessions")
	outputs := filepath.Join(base, "outputs")
	for _, dir := range []string{home, agentDir, work, tmp, npmCache, profile, sessions, outputs} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	npmrc := filepath.Join(base, "npmrc")
	globalrc := filepath.Join(base, "npm-globalrc")
	for _, path := range []string{npmrc, globalrc} {
		if err := os.WriteFile(path, nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}

	pageToken := fmt.Sprintf("PSI_MCP_ACCEPTANCE_%d", time.Now().UnixNano())
	var pageHits atomic.Int64
	page := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		pageHits.Add(1)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprintf(w, "<!doctype html><html><head><title>psi MCP acceptance</title></head><body><h1>%s</h1><p>Known acceptance marker.</p></body></html>", pageToken)
	}))
	defer page.Close()

	stub := &chromeProviderStub{pageToken: pageToken, pageURL: page.URL + "/"}
	providerServer := httptest.NewServer(stub)
	defer providerServer.Close()

	t.Cleanup(func() { killOwnedProcesses([]string{profile, npmCache, "chrome-devtools-mcp@1.13.0"}) })

	writeAppFile(t, filepath.Join(agentDir, "settings.json"), `{"defaultProvider":"anthropic","defaultModel":"m","compaction":{"enabled":false}}`)
	writeAppFile(t, filepath.Join(agentDir, "models.json"), `{"providers":{"anthropic":{"baseUrl":"`+providerServer.URL+`/v1","models":[{"id":"m","maxTokens":4096,"contextWindow":100000,"input":["text","image"]}]}}}`)
	writeAppFile(t, filepath.Join(agentDir, "auth.json"), `{"anthropic":{"type":"api_key","key":"synthetic-acceptance-key"}}`)

	mcpConfig := map[string]any{
		"mcpServers": map[string]any{
			"chrome-devtools": map[string]any{
				"command": *mcpTestLauncher,
				"args": []string{
					"-y", "chrome-devtools-mcp@1.13.0",
					"--headless",
					"--executablePath", *mcpTestBrowser,
					"--userDataDir", profile,
					"--no-usage-statistics",
					"--no-performance-crux",
					"--no-javascript-evaluation",
					"--no-page-id-routing",
					"--viewport", "1280x720",
				},
				"env": map[string]string{
					"CI": "1", "NO_UPDATE_NOTIFIER": "1",
					"HOME": home, "TMPDIR": tmp, "TMP": tmp, "TEMP": tmp,
					"npm_config_cache": npmCache, "npm_config_userconfig": npmrc, "npm_config_globalconfig": globalrc,
					"npm_config_ignore_scripts": "true", "npm_config_audit": "false", "npm_config_fund": "false",
					"npm_config_update_notifier": "false", "npm_config_loglevel": "error",
					"CHROME_DEVTOOLS_MCP_NO_USAGE_STATISTICS": "1",
				},
				"timeout":  120,
				"exposure": "hidden",
				"toolExposure": map[string]string{
					"navigate_page": "direct", "take_snapshot": "direct", "take_screenshot": "direct",
				},
			},
		},
	}
	encoded, _ := json.MarshalIndent(mcpConfig, "", "  ")
	writeAppFile(t, filepath.Join(agentDir, "mcp.json"), string(encoded))

	extraEnv := []string{
		"TMPDIR=" + tmp, "TMP=" + tmp, "TEMP=" + tmp,
		"CI=1", "NO_UPDATE_NOTIFIER=1", "CHROME_DEVTOOLS_MCP_NO_USAGE_STATISTICS=1",
		"npm_config_cache=" + npmCache, "npm_config_userconfig=" + npmrc, "npm_config_globalconfig=" + globalrc,
		"npm_config_ignore_scripts=true", "npm_config_audit=false", "npm_config_fund=false",
		"npm_config_update_notifier=false", "npm_config_loglevel=error",
	}
	searchPath := filepath.Dir(*mcpTestLauncher) + ":/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin"
	args := []string{"--deny-project", "--no-context-files", "--no-skills", "--no-themes", "--session-dir", sessions}
	q := startReviewProcessEnv(t, home, work, agentDir, *reviewBinary, searchPath, extraEnv, args...)
	q.prompt()
	readyDeadline := time.Now().Add(180 * time.Second)
	for {
		q.write("/mcp\r")
		q.prompt()
		if strings.Contains(q.screen.Text(), "chrome-devtools") && strings.Contains(q.screen.Text(), "ready") {
			break
		}
		if time.Now().After(readyDeadline) {
			t.Fatalf("mcp status never reached ready: %q", q.screen.Text())
		}
		time.Sleep(250 * time.Millisecond)
	}
	t.Logf("mcp status ready: %s", q.screen.Text())
	q.prompt()
	q.write("Use the browser tools to navigate the acceptance page, take a snapshot, and take a screenshot.\r")
	waitScreenAll(t, q, []string{"ACCEPTANCE_DONE"}, 180*time.Second)
	q.write("/quit\r")
	finishAcceptance(t, q, 60*time.Second)

	stub.mu.Lock()
	declared := map[string]bool{}
	for name := range stub.declared {
		declared[name] = true
	}
	calls, sawImage, imageBytes := stub.calls, stub.sawImage, stub.imageBytes
	failures := append([]string(nil), stub.failures...)
	stub.mu.Unlock()
	for _, name := range []string{"mcp__chrome_devtools__navigate_page", "mcp__chrome_devtools__take_snapshot", "mcp__chrome_devtools__take_screenshot"} {
		if !declared[name] {
			t.Errorf("declared tools missing %s: %v", name, declared)
		}
	}
	if pageHits.Load() == 0 {
		t.Error("synthetic page was never requested")
	}
	if !sawImage {
		t.Error("screenshot image never reached the provider request")
	}
	for _, failure := range failures {
		t.Error(failure)
	}
	t.Logf("provider calls=%d page_hits=%d declared=%v screenshot_base64_bytes=%d", calls, pageHits.Load(), declared, imageBytes)

	sessionFile := findSessionFile(t, sessions)
	dumpSessionFiles(t, sessions)
	assertSessionScreenshotImage(t, sessionFile)

	waitOwnedProcessesGone(t, []string{profile, npmCache, "chrome-devtools-mcp@1.13.0"}, 15*time.Second)

	// Reopen the durable session. The provider endpoint must receive no new
	// request, and the final assistant text must load from history.
	before := calls
	q2 := startReviewProcessEnv(t, home, work, agentDir, *reviewBinary, searchPath, extraEnv, "--deny-project", "--no-context-files", "--no-skills", "--no-themes", "--session", sessionFile)
	q2.prompt()
	waitScreenAll(t, q2, []string{"ACCEPTANCE_DONE"}, 30*time.Second)
	q2.write("/quit\r")
	finishAcceptance(t, q2, 60*time.Second)
	stub.mu.Lock()
	after := stub.calls
	stub.mu.Unlock()
	if after != before {
		t.Errorf("session replay made a new provider request: %d -> %d", before, after)
	}
}

func waitScreenAll(t *testing.T, q *reviewPTY, wants []string, timeout time.Duration) {
	t.Helper()
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	for {
		text := q.screen.Text()
		ok := true
		for _, want := range wants {
			if !strings.Contains(text, want) {
				ok = false
				break
			}
		}
		if ok {
			return
		}
		select {
		case chunk, open := <-q.chunks:
			if !open {
				t.Fatalf("process exited waiting for %v: %q", wants, text)
			}
			q.pending += string(chunk)
			q.raw.Write(chunk)
			q.screen.Feed(chunk)
		case <-deadline.C:
			t.Fatalf("timeout waiting for %v: %q", wants, text)
		}
	}
}

func finishAcceptance(t *testing.T, q *reviewPTY, timeout time.Duration) {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- q.cmd.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("process exit: %v; output=%q", err, q.raw.String())
		}
	case <-time.After(timeout):
		_ = q.cmd.Process.Kill()
		<-done
		t.Fatalf("process did not exit in %v; output=%q", timeout, q.raw.String())
	}
}

func dumpSessionFiles(t *testing.T, root string) {
	t.Helper()
	_ = filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		data, _ := os.ReadFile(path)
		t.Logf("session file %s size=%d first=%q", path, len(data), firstLineBytes(data))
		return nil
	})
}

func firstLineBytes(data []byte) string {
	for i, b := range data {
		if b == '\n' {
			return string(data[:i])
		}
	}
	return string(data)
}

func findSessionFile(t *testing.T, root string) string {
	t.Helper()
	var found string
	_ = filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || found != "" {
			return nil
		}
		if strings.HasSuffix(path, ".jsonl") {
			found = path
		}
		return nil
	})
	if found == "" {
		t.Fatalf("no session file under %s", root)
	}
	return found
}

func assertSessionScreenshotImage(t *testing.T, path string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var record map[string]any
		if json.Unmarshal([]byte(line), &record) != nil {
			continue
		}
		if message, ok := record["message"].(map[string]any); ok {
			record = message
		}
		if record["role"] != "toolResult" {
			continue
		}
		if record["toolName"] != "mcp__chrome_devtools__take_screenshot" {
			continue
		}
		blocks, _ := record["content"].([]any)
		for _, raw := range blocks {
			block, _ := raw.(map[string]any)
			if block["type"] != "image" {
				continue
			}
			encoded, _ := block["data"].(string)
			if encoded == "" {
				continue
			}
			decoded, decodeErr := base64.StdEncoding.DecodeString(encoded)
			if decodeErr != nil || len(decoded) < 8 || !bytes.HasPrefix(decoded, []byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a}) {
				t.Fatalf("durable screenshot is not a valid PNG: err=%v bytes=%d", decodeErr, len(decoded))
			}
			t.Logf("durable screenshot image bytes=%d decoded=%d mime=%v", len(encoded), len(decoded), block["mimeType"])
			return
		}
	}
	t.Fatal("durable session has no screenshot image block")
}

func waitOwnedProcessesGone(t *testing.T, markers []string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		left := ownedProcesses(markers)
		if len(left) == 0 {
			return
		}
		if time.Now().After(deadline) {
			for _, line := range left {
				fields := strings.Fields(line)
				if len(fields) > 0 {
					if pid, err := strconv.Atoi(fields[0]); err == nil {
						_ = exec.Command("kill", "-9", strconv.Itoa(pid)).Run()
					}
				}
			}
			t.Fatalf("owned processes survived cleanup: %v", left)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func ownedProcesses(markers []string) []string {
	out, err := exec.Command("ps", "-axo", "pid=,command=").Output()
	if err != nil {
		return nil
	}
	var matched []string
	for _, line := range strings.Split(string(out), "\n") {
		for _, marker := range markers {
			if strings.Contains(line, marker) {
				matched = append(matched, strings.TrimSpace(line))
				break
			}
		}
	}
	return matched
}

func killOwnedProcesses(markers []string) {
	for _, line := range ownedProcesses(markers) {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		if pid, err := strconv.Atoi(fields[0]); err == nil {
			_ = exec.Command("kill", "-9", strconv.Itoa(pid)).Run()
		}
	}
}
