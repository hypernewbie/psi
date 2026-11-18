//go:build unix

package app

// Built-binary proof for RPC live settings: one `psi --mode rpc` child with
// piped stdio speaks the line protocol while a loopback provider holds the
// first request. No PTY is involved; stdout is drained continuously so pipe
// backpressure can never stand in for settlement.

import (
	"bufio"
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"psi/internal/session"
)

func TestReviewRPCProcess(t *testing.T) {
	if os.Getenv("PSI_REVIEW_RPC_PROCESS") != "1" || flag.Lookup("test.run").Value.String() != "^TestReviewRPCProcess$" {
		return
	}
	var args []string
	if err := json.Unmarshal([]byte(os.Getenv("PSI_REVIEW_RPC_ARGS")), &args); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if err := Run(args, "review"); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	os.Exit(0)
}

// reviewRPCChild is one headless child: a stdin writer, a drained stdout
// object stream, captured stderr, and a watchdog that stops only this child.
type reviewRPCChild struct {
	t      *testing.T
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	frames chan map[string]any
	stderr bytes.Buffer
	exited chan error
}

func startReviewRPCProcess(t *testing.T, home, work, agentDir, binary string, args ...string) *reviewRPCChild {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestReviewRPCProcess$")
	if binary != "" {
		executable, err := filepath.Abs(binary)
		if err != nil {
			t.Fatal(err)
		}
		cmd = exec.Command(executable, args...)
	}
	cmd.Dir = work
	encoded, _ := json.Marshal(args)
	cmd.Env = append(appTestEnv(home, agentDir), "PSI_APP_TEST_PROCESS=", "PSI_REVIEW_RPC_PROCESS=1", "PSI_REVIEW_RPC_ARGS="+string(encoded))
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	child := &reviewRPCChild{t: t, cmd: cmd, stdin: stdin, frames: make(chan map[string]any, 128), exited: make(chan error, 1)}
	cmd.Stderr = &child.stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = child.cmd.Process.Kill()
		_ = child.stdin.Close()
	})
	go func() {
		sc := bufio.NewScanner(stdout)
		sc.Buffer(make([]byte, 64*1024), 1024*1024)
		for sc.Scan() {
			line := strings.TrimSpace(sc.Text())
			if line == "" {
				continue
			}
			var obj map[string]any
			if err := json.Unmarshal([]byte(line), &obj); err != nil {
				continue
			}
			child.frames <- obj
		}
	}()
	go func() { child.exited <- child.cmd.Wait() }()
	return child
}

func (c *reviewRPCChild) send(t *testing.T, id, typ string, fields map[string]any) {
	t.Helper()
	obj := map[string]any{"id": id, "type": typ}
	for key, value := range fields {
		obj[key] = value
	}
	raw, err := json.Marshal(obj)
	if err != nil {
		t.Fatal(err)
	}
	raw = append(raw, '\n')
	if _, err := c.stdin.Write(raw); err != nil {
		t.Fatalf("stdin write failed (stderr: %s): %v", c.stderr.String(), err)
	}
}

// nextFrame returns the next stdout object, failing after a bounded wait.
func (c *reviewRPCChild) nextFrame(t *testing.T, what string) map[string]any {
	t.Helper()
	select {
	case obj := <-c.frames:
		return obj
	case err := <-c.exited:
		t.Fatalf("child exited while waiting for %s: %v (stderr: %s)", what, err, c.stderr.String())
	case <-time.After(8 * time.Second):
		t.Fatalf("timed out waiting for %s (stderr: %s)", what, c.stderr.String())
	}
	return nil
}

// responseFor sends one command and returns its correlated response,
// collecting the events that arrive first.
func (c *reviewRPCChild) responseFor(t *testing.T, id, typ string, fields map[string]any) (map[string]any, []map[string]any) {
	t.Helper()
	c.send(t, id, typ, fields)
	var events []map[string]any
	for {
		obj := c.nextFrame(t, "response "+id)
		if obj["type"] == "response" && obj["id"] == id {
			return obj, events
		}
		events = append(events, obj)
	}
}

func rpcProcessUserTranscript(t *testing.T, agentDir string, want ...string) {
	t.Helper()
	var files []string
	root := filepath.Join(agentDir, "sessions")
	_ = filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err == nil && !entry.IsDir() && strings.HasSuffix(entry.Name(), ".jsonl") {
			files = append(files, path)
		}
		return nil
	})
	if len(files) != 1 {
		t.Fatalf("session files = %v, want exactly one", files)
	}
	reopened, err := session.OpenFile(files[0], "", false)
	if err != nil {
		t.Fatal(err)
	}
	users := rpcStoredUserTexts(reopened)
	if len(users) != len(want) {
		t.Fatalf("user transcript = %q, want %q", users, want)
	}
	for i, text := range want {
		if users[i] != text {
			t.Fatalf("user transcript = %q, want %q", users, want)
		}
	}
}

// TestRPCProcessLiveSettingsAndQueue proves through the real `psi --mode rpc`
// child that a model and thinking level changed mid-run reach the next
// provider request, while steering and follow-up queued during the held
// first request each reach the provider exactly once.
func TestRPCProcessLiveSettingsAndQueue(t *testing.T) {
	bodies := make(chan map[string]any, 8)
	release := make(chan struct{})
	var turns atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Path != "/v1/messages" || req.Header.Get("x-api-key") != "file-test-key" {
			t.Errorf("endpoint/auth not used: %s", req.URL.Path)
		}
		var body map[string]any
		if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
			t.Errorf("decode request: %v", err)
			return
		}
		bodies <- body
		w.Header().Set("Content-Type", "text/event-stream")
		w.(http.Flusher).Flush()
		select {
		case <-release:
		case <-req.Context().Done():
			return
		}
		fmt.Fprint(w, rpcLiveTextTurn(turns.Add(1)))
	}))
	// Close the loopback server from cleanup, registered before the child
	// starts: on a failure the kill runs first (LIFO) so a held stream
	// cannot deadlock server shutdown.
	t.Cleanup(server.Close)
	home, ws := t.TempDir(), t.TempDir()
	agentDir := filepath.Join(home, ".pi", "agent")
	writeAppFile(t, filepath.Join(agentDir, "settings.json"), `{"defaultProvider":"anthropic","defaultModel":"rpc-live-model","defaultThinkingLevel":"off","defaultTools":["read"],"compaction":{"enabled":false}}`)
	writeAppFile(t, filepath.Join(agentDir, "models.json"), `{"providers":{"anthropic":{"models":[`+
		`{"id":"rpc-live-model","api":"anthropic-messages","baseUrl":"`+server.URL+`","contextWindow":65536,"maxTokens":16384,"reasoning":true,"input":["text"]},`+
		`{"id":"rpc-live-next","api":"anthropic-messages","baseUrl":"`+server.URL+`","contextWindow":65536,"maxTokens":16384,"reasoning":true,"input":["text"]}]}}}`)
	writeAppFile(t, filepath.Join(agentDir, "auth.json"), `{"anthropic":{"type":"api_key","key":"file-test-key"}}`)

	child := startReviewRPCProcess(t, home, ws, agentDir, *reviewBinary, "--mode", "rpc")
	started, _ := child.responseFor(t, "p1", "prompt", map[string]any{"message": "hello"})
	if started["success"] != true {
		t.Fatalf("prompt: %v (stderr: %s)", started, child.stderr.String())
	}
	var first map[string]any
	select {
	case first = <-bodies:
	case <-time.After(8 * time.Second):
		t.Fatalf("provider never received the first request (stderr: %s)", child.stderr.String())
	}
	if first["model"] != "rpc-live-model" {
		t.Fatalf("first request model = %v, want rpc-live-model", first["model"])
	}
	if _, ok := first["thinking"]; ok {
		t.Fatalf("first request carried a thinking block: %v", first["thinking"])
	}
	steer, _ := child.responseFor(t, "p2", "prompt", map[string]any{"message": "queued steer", "streamingBehavior": "steer"})
	if steer["success"] != true || steer["data"].(map[string]any)["disposition"] != "queued" {
		t.Fatalf("steer queue: %v", steer)
	}
	follow, _ := child.responseFor(t, "p3", "prompt", map[string]any{"message": "queued follow-up", "streamingBehavior": "followUp"})
	if follow["success"] != true || follow["data"].(map[string]any)["disposition"] != "queued" {
		t.Fatalf("follow-up queue: %v", follow)
	}
	switched, _ := child.responseFor(t, "m1", "set_model", map[string]any{"provider": "anthropic", "modelId": "rpc-live-next"})
	if switched["success"] != true {
		t.Fatalf("mid-run model change was refused: %v", switched)
	}
	thinking, _ := child.responseFor(t, "t1", "set_thinking_level", map[string]any{"level": "high"})
	if thinking["success"] != true {
		t.Fatalf("mid-run thinking change was refused: %v", thinking)
	}
	state, _ := child.responseFor(t, "g1", "get_state", nil)
	data, _ := state["data"].(map[string]any)
	model, _ := data["model"].(map[string]any)
	if model["id"] != "rpc-live-next" || data["thinkingLevel"] != "high" {
		t.Fatalf("state after switch: %v", data)
	}
	close(release)
	settled := false
	for !settled {
		obj := child.nextFrame(t, "agent_settled")
		if obj["type"] == "extension_error" {
			raw, _ := json.Marshal(obj)
			t.Fatalf("run reported an error: %s", raw)
		}
		settled = obj["type"] == "agent_settled"
	}
	var live []map[string]any
	live = append(live, first)
	for len(live) < 3 {
		select {
		case body := <-bodies:
			live = append(live, body)
		case <-time.After(8 * time.Second):
			t.Fatalf("provider requests = %d, want 3", len(live))
		}
	}
	select {
	case body := <-bodies:
		raw, _ := json.Marshal(body)
		t.Fatalf("unexpected fourth provider request: %s", raw)
	case <-time.After(200 * time.Millisecond):
	}
	for i, body := range live[1:] {
		if body["model"] != "rpc-live-next" {
			t.Fatalf("live request %d model = %v, want rpc-live-next", i+2, body["model"])
		}
		block, ok := body["thinking"].(map[string]any)
		if !ok || block["budget_tokens"] != float64(8192) {
			t.Fatalf("live request %d thinking = %v, want budget 8192", i+2, body["thinking"])
		}
	}
	rawSecond, _ := json.Marshal(live[1])
	if strings.Contains(string(rawSecond), "queued follow-up") {
		t.Fatalf("second request carried the undelivered follow-up: %s", rawSecond)
	}
	// rpcLastBodyUserText is defined with the in-process live tests.
	if last := rpcLastBodyUserText(t, live[1]); last != "queued steer" {
		t.Fatalf("second request newest user message = %q, want %q", last, "queued steer")
	}
	if last := rpcLastBodyUserText(t, live[2]); last != "queued follow-up" {
		t.Fatalf("third request newest user message = %q, want %q", last, "queued follow-up")
	}
	if err := child.stdin.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-child.exited:
		if err != nil {
			t.Fatalf("child exit: %v (stderr: %s)", err, child.stderr.String())
		}
	case <-time.After(8 * time.Second):
		t.Fatalf("child did not exit after stdin EOF (stderr: %s)", child.stderr.String())
	}
	rpcProcessUserTranscript(t, agentDir, "hello", "queued steer", "queued follow-up")
}
