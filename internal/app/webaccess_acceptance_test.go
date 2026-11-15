//go:build acceptance

package app

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"psi/internal/testutil"
)

// These tests drive the built binary over RPC against a loopback Anthropic
// protocol fixture. The fixture emits deterministic tool calls; the real
// binary performs the live web request. Run with:
//
//	PSI_WEB_BINARY=$PWD/.webaccess-check/psi PSI_LIVE_WEB=1 \
//	PSI_WEB_CONFIG_PATH=~/.pi/web-search.json \
//	go test -tags acceptance ./internal/app -run TestWebAccessLive -v
//
// They are manual acceptance tests, not part of CI.

type acceptanceTurn struct {
	tools   []string
	results []string
}

type acceptanceFixture struct {
	mu    sync.Mutex
	turns []acceptanceTurn
	// step returns the tool call for a turn, or an empty name for final text.
	step func(turn int, results []string) (name, argsJSON string)
}

func (f *acceptanceFixture) Snapshot() []acceptanceTurn {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]acceptanceTurn(nil), f.turns...)
}

func (f *acceptanceFixture) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Tools    []map[string]any `json:"tools"`
		Messages []struct {
			Role    string `json:"role"`
			Content []struct {
				Type    string          `json:"type"`
				Content json.RawMessage `json:"content"`
			} `json:"content"`
		} `json:"messages"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	turn := acceptanceTurn{}
	for _, tool := range body.Tools {
		if name, ok := tool["name"].(string); ok {
			turn.tools = append(turn.tools, name)
		}
	}
	for _, msg := range body.Messages {
		for _, block := range msg.Content {
			if block.Type != "tool_result" {
				continue
			}
			var text string
			if err := json.Unmarshal(block.Content, &text); err != nil {
				var parts []struct {
					Type string `json:"type"`
					Text string `json:"text"`
				}
				if json.Unmarshal(block.Content, &parts) == nil {
					for _, part := range parts {
						text += part.Text
					}
				}
			}
			turn.results = append(turn.results, text)
		}
	}
	f.mu.Lock()
	turnIndex := len(f.turns)
	f.turns = append(f.turns, turn)
	step := f.step
	f.mu.Unlock()

	name, argsJSON := "", ""
	if step != nil {
		name, argsJSON = step(turnIndex, turn.results)
	}

	w.Header().Set("Content-Type", "text/event-stream")
	if name != "" {
		fmt.Fprint(w, "event: message_start\ndata: {\"message\":{\"id\":\"live-1\",\"model\":\"web-fixture\",\"usage\":{\"input_tokens\":1}}}\n\n")
		fmt.Fprintf(w, "event: content_block_start\ndata: {\"index\":0,\"content_block\":{\"type\":\"tool_use\",\"id\":%q,\"name\":%q,\"input\":%s}}\n\n", fmt.Sprintf("call-live-%d", turnIndex+1), name, argsJSON)
		fmt.Fprint(w, "event: content_block_stop\ndata: {\"index\":0}\n\n")
		fmt.Fprint(w, "event: message_delta\ndata: {\"delta\":{\"stop_reason\":\"tool_use\"}}\n\nevent: message_stop\ndata: {}\n\n")
		return
	}
	fmt.Fprint(w, "event: message_start\ndata: {\"message\":{\"id\":\"live-2\",\"model\":\"web-fixture\",\"usage\":{\"input_tokens\":1}}}\n\n")
	fmt.Fprint(w, "event: content_block_start\ndata: {\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n")
	fmt.Fprint(w, "event: content_block_delta\ndata: {\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"LIVE_WEB_DONE\"}}\n\n")
	fmt.Fprint(w, "event: content_block_stop\ndata: {\"index\":0}\n\n")
	fmt.Fprint(w, "event: message_delta\ndata: {\"delta\":{\"stop_reason\":\"end_turn\"}}\n\nevent: message_stop\ndata: {}\n\n")
}

type acceptanceEnv struct {
	home, work, agent, sessions, config string
	fixture                             *acceptanceFixture
	fixtureURL                          string
	server                              *httptest.Server
}

func prepareAcceptance(t *testing.T, step func(turn int, results []string) (name, argsJSON string)) *acceptanceEnv {
	t.Helper()
	binary := os.Getenv("PSI_WEB_BINARY")
	configPath := os.Getenv("PSI_WEB_CONFIG_PATH")
	if binary == "" || configPath == "" {
		t.Fatal("PSI_WEB_BINARY and PSI_WEB_CONFIG_PATH are required with PSI_LIVE_WEB=1")
	}
	if filepath.Base(configPath) != "web-search.json" {
		t.Fatalf("PSI_WEB_CONFIG_PATH must name web-search.json")
	}
	for _, path := range []string{binary, configPath} {
		info, err := os.Stat(path)
		if err != nil || info.IsDir() {
			t.Fatalf("missing acceptance prerequisite: %s", filepath.Base(path))
		}
	}
	home := t.TempDir()
	work := t.TempDir()
	agentDir := filepath.Join(home, ".pi", "agent")
	sessions := filepath.Join(home, "sessions")
	for _, dir := range []string{agentDir, sessions} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	fixture := &acceptanceFixture{step: step}
	server := httptest.NewServer(fixture)
	t.Cleanup(server.Close)
	writeAppFile(t, filepath.Join(agentDir, "models.json"), `{"providers":{"anthropic":{"baseUrl":"`+server.URL+`","models":[{"id":"web-fixture","name":"Web Fixture","contextWindow":64000,"maxTokens":1000,"input":["text"]}]}}}`)
	writeAppFile(t, filepath.Join(agentDir, "auth.json"), `{"anthropic":{"type":"api_key","key":"invented-web-key"}}`)
	return &acceptanceEnv{home: home, work: work, agent: agentDir, sessions: sessions, config: configPath, fixture: fixture, fixtureURL: server.URL, server: server}
}

func (e *acceptanceEnv) run(t *testing.T, args ...string) []byte {
	t.Helper()
	binary := os.Getenv("PSI_WEB_BINARY")
	cmd := exec.Command(binary, args...)
	cmd.Dir = e.work
	pluginDir := filepath.Dir(e.config)
	cmd.Env = testutil.ChildEnv(e.home, map[string]string{
		"PSI_CODING_AGENT_DIR":        e.agent,
		"PI_CODING_AGENT_DIR":         pluginDir,
		"PI_CODING_AGENT_SESSION_DIR": e.sessions,
		"LANG":                        "C.UTF-8",
		"TERM":                        "xterm",
	})
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	var out bytes.Buffer
	settled := make(chan struct{})
	go func() {
		scanner := bufio.NewScanner(stdout)
		scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
		once := sync.Once{}
		for scanner.Scan() {
			line := scanner.Bytes()
			out.Write(append([]byte(nil), line...))
			out.WriteByte('\n')
			if bytes.Contains(line, []byte(`"type":"agent_settled"`)) {
				once.Do(func() { close(settled) })
			}
		}
		done <- scanner.Err()
	}()
	if _, err := fmt.Fprintln(stdin, `{"id":"p1","type":"prompt","message":"fetch the example page"}`); err != nil {
		t.Fatal(err)
	}
	select {
	case <-settled:
	case err := <-done:
		t.Fatalf("child exited before settlement: %v\nstderr: %s\nstdout: %s", err, stderr.String(), out.String())
	case <-time.After(5 * time.Minute):
		_ = cmd.Process.Kill()
		t.Fatalf("acceptance watchdog expired\nstderr: %s", stderr.String())
	}
	_ = stdin.Close()
	if err := cmd.Wait(); err != nil {
		t.Fatalf("child failed: %v\nstderr: %s\nstdout: %s", err, stderr.String(), out.String())
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("stdout read: %v", err)
		}
	case <-time.After(10 * time.Second):
	}
	// Stdout must be JSONL only.
	for _, line := range strings.Split(strings.TrimSpace(out.String()), "\n") {
		if !json.Valid([]byte(line)) {
			t.Fatalf("non-JSONL stdout: %q", line)
		}
	}
	return out.Bytes()
}

func TestWebAccessLiveFetchBinary(t *testing.T) {
	if os.Getenv("PSI_LIVE_WEB") != "1" {
		t.Skip("set PSI_LIVE_WEB=1 to run live acceptance")
	}
	env := prepareAcceptance(t, func(turn int, _ []string) (string, string) {
		if turn == 0 {
			return "fetch_content", `{"url":"https://example.com","mode":"readable"}`
		}
		return "", ""
	})
	env.run(t, "--mode", "rpc", "--tools", "fetch_content", "--provider", "anthropic", "--model", "web-fixture",
		"--deny-project", "--no-session", "--no-context-files", "--no-skills")

	turns := env.snapshotTurns()
	if len(turns) < 2 {
		t.Fatalf("expected two model turns, got %d", len(turns))
	}
	if !contains(turns[0].tools, "fetch_content") {
		t.Fatalf("fetch_content was not advertised: %v", turns[0].tools)
	}
	if len(turns[1].results) != 1 {
		t.Fatalf("tool results = %d", len(turns[1].results))
	}
	result := turns[1].results[0]
	if strings.TrimSpace(result) == "" || !strings.Contains(result, "example") {
		t.Fatalf("unexpected tool result: %.400q", result)
	}
	if !strings.Contains(result, "content:") {
		t.Fatalf("tool result lost its content boundary: %.400q", result)
	}
	if strings.Contains(result, "fetch_content failed") {
		t.Fatalf("tool result is an error: %.400q", result)
	}
	t.Logf("live fetch result excerpt: %s", excerpt(result, 300))
}

func contains(list []string, want string) bool {
	for _, item := range list {
		if item == want {
			return true
		}
	}
	return false
}

func excerpt(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

func (e *acceptanceEnv) snapshotTurns() []acceptanceTurn {
	return e.fixture.Snapshot()
}

func TestWebAccessLiveMVPBinary(t *testing.T) {
	if os.Getenv("PSI_LIVE_WEB") != "1" {
		t.Skip("set PSI_LIVE_WEB=1 to run live acceptance")
	}
	var (
		mu         sync.Mutex
		candidates []string
		tried      int
		fetchedID  string
	)
	env := prepareAcceptance(t, func(turn int, results []string) (string, string) {
		mu.Lock()
		defer mu.Unlock()
		switch turn {
		case 0:
			return "web_search", `{"query":"Go net/http Client Timeout documentation","provider":"searxng","numResults":3}`
		case 1:
			if len(results) == 0 {
				t.Errorf("search produced no tool result")
				return "", ""
			}
			candidates = resultURLs(results[0])
			if len(candidates) == 0 {
				t.Errorf("search produced no public result URLs:\n%s", results[0])
				return "", ""
			}
			if tried >= len(candidates) || tried >= 3 {
				return "", ""
			}
			url := candidates[tried]
			tried++
			t.Logf("acceptance fetch attempt %d: %s", tried, url)
			return "fetch_content", fmt.Sprintf(`{"url":%q,"mode":"readable"}`, url)
		default:
			if fetchedID == "" && len(results) > 0 {
				last := results[len(results)-1]
				if !strings.Contains(last, "fetch_content failed") {
					if id := responseIDValue(last); id != "" {
						fetchedID = id
						return "get_search_content", fmt.Sprintf(`{"responseId":%q}`, id)
					}
				}
				if tried < len(candidates) && tried < 3 {
					url := candidates[tried]
					tried++
					t.Logf("acceptance fetch attempt %d after error (%s): %s", tried, excerpt(last, 160), url)
					return "fetch_content", fmt.Sprintf(`{"url":%q,"mode":"readable"}`, url)
				}
			}
			return "", ""
		}
	})
	env.run(t, "--mode", "rpc", "--tools", "web_search,fetch_content,get_search_content",
		"--provider", "anthropic", "--model", "web-fixture",
		"--deny-project", "--no-session", "--no-context-files", "--no-skills")

	turns := env.snapshotTurns()
	if len(turns) < 3 {
		t.Fatalf("expected search, fetch, and content turns; got %d", len(turns))
	}
	for _, name := range []string{"web_search", "fetch_content", "get_search_content"} {
		if !contains(turns[0].tools, name) {
			t.Fatalf("%s was not advertised: %v", name, turns[0].tools)
		}
	}
	// Find the first successful fetch and its page retrieval.
	var fetchResult, contentResult string
	for _, turn := range turns[1:] {
		if len(turn.results) == 0 {
			continue
		}
		result := turn.results[len(turn.results)-1]
		switch {
		case fetchResult == "" && strings.Contains(result, "mode:") && strings.Contains(result, "content:"):
			if !strings.Contains(result, "fetch_content failed") {
				fetchResult = result
			}
		case fetchResult != "" && contentResult == "" && strings.Contains(result, "kind: fetch"):
			contentResult = result
		}
	}
	if strings.TrimSpace(fetchResult) == "" {
		t.Fatal("no successful live fetch occurred")
	}
	if strings.TrimSpace(contentResult) == "" {
		t.Fatalf("no stored-page retrieval occurred after fetch: %.300q", fetchResult)
	}
	if !strings.Contains(fetchResult, "responseId:") {
		t.Fatalf("fetch result has no response ID: %.300q", fetchResult)
	}
	if !strings.Contains(contentResult, "offset: 0") || !strings.Contains(contentResult, "length:") {
		t.Fatalf("retrieval lost paging metadata: %.400q", contentResult)
	}
	t.Logf("live search/fetch/retrieval excerpt: %s", excerpt(contentResult, 300))
}

func resultURLs(text string) []string {
	var out []string
	for _, line := range strings.Split(text, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "https://") || strings.HasPrefix(trimmed, "http://") {
			out = append(out, trimmed)
			if len(out) == 5 {
				break
			}
		}
	}
	return out
}

func responseIDValue(text string) string {
	for _, line := range strings.Split(text, "\n") {
		if strings.HasPrefix(line, "responseId: ") {
			return strings.TrimSpace(strings.TrimPrefix(line, "responseId: "))
		}
	}
	return ""
}

func TestWebAccessLiveBatchBinary(t *testing.T) {
	if os.Getenv("PSI_LIVE_WEB") != "1" {
		t.Skip("set PSI_LIVE_WEB=1 to run live acceptance")
	}
	env := prepareAcceptance(t, func(turn int, results []string) (string, string) {
		switch turn {
		case 0:
			return "fetch_content", `{"urls":["https://example.com","https://example.org"],"mode":"readable"}`
		case 1:
			if len(results) != 1 || !strings.Contains(results[0], "urls: 2") || !strings.Contains(results[0], "ok: 2") {
				t.Errorf("batch fetch did not return two successes: %.400q", strings.Join(results, "\n"))
				return "", ""
			}
			id := responseIDValue(results[0])
			if id == "" {
				t.Errorf("batch fetch returned no responseId")
				return "", ""
			}
			return "get_search_content", fmt.Sprintf(`{"responseId":%q,"urlIndex":1}`, id)
		}
		return "", ""
	})
	env.run(t, "--mode", "rpc", "--tools", "fetch_content,get_search_content",
		"--provider", "anthropic", "--model", "web-fixture",
		"--deny-project", "--no-session", "--no-context-files", "--no-skills")

	turns := env.snapshotTurns()
	if len(turns) < 3 {
		t.Fatalf("expected fetch and retrieval turns, got %d", len(turns))
	}
	fetchResult := turns[1].results[0]
	if !strings.Contains(fetchResult, "1. url: https://example.com\n") || !strings.Contains(fetchResult, "2. url: https://example.org\n") {
		t.Fatalf("batch sections not in input order: %.500q", fetchResult)
	}
	if len(turns[2].results) == 0 {
		t.Fatal("no retrieval result")
	}
	content := turns[2].results[len(turns[2].results)-1]
	if !strings.Contains(content, "kind: fetch") || !strings.Contains(content, "url: https://example.org\n") {
		t.Fatalf("urlIndex retrieval selected the wrong page: %.500q", content)
	}
	if !strings.Contains(content, "Example Domain") {
		t.Fatalf("retrieved page lacks expected text: %.500q", content)
	}
	t.Logf("live batch retrieval excerpt: %s", excerpt(content, 300))
}
