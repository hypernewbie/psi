//go:build unix

package app

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"psi/internal/session"
)

const rpcCompactPartial = "ORIGINAL_PARTIAL"

type rpcCompactStub struct {
	server *httptest.Server

	mu           sync.Mutex
	calls        int
	firstSeen    chan struct{}
	firstCancel  chan struct{}
	summarySeen  chan string
	continuation chan string

	releaseSummary      chan struct{}
	releaseContinuation chan struct{}
}

func startRPCCompactStub(t *testing.T) *rpcCompactStub {
	t.Helper()
	stub := &rpcCompactStub{
		firstSeen: make(chan struct{}), firstCancel: make(chan struct{}),
		summarySeen: make(chan string, 1), continuation: make(chan string, 1),
		releaseSummary: make(chan struct{}), releaseContinuation: make(chan struct{}),
	}
	var firstOnce, cancelOnce sync.Once
	stub.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read request: %v", err)
			return
		}
		stub.mu.Lock()
		stub.calls++
		index := stub.calls
		stub.mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		w.(http.Flusher).Flush()
		switch index {
		case 1:
			fmt.Fprint(w, rpcCompactSSEStart("first"))
			fmt.Fprintf(w, "event: content_block_delta\ndata: {\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":%q}}\n\n", rpcCompactPartial)
			w.(http.Flusher).Flush()
			firstOnce.Do(func() { close(stub.firstSeen) })
			<-r.Context().Done()
			cancelOnce.Do(func() { close(stub.firstCancel) })
		case 2:
			stub.summarySeen <- string(body)
			select {
			case <-stub.releaseSummary:
			case <-r.Context().Done():
				return
			}
			fmt.Fprint(w, rpcCompactSSEText("SUMMARY_COMMITTED"))
		case 3:
			stub.continuation <- string(body)
			select {
			case <-stub.releaseContinuation:
			case <-r.Context().Done():
				return
			}
			fmt.Fprint(w, rpcCompactSSEText("STEER_DELIVERED"))
		default:
			t.Errorf("unexpected provider request %d", index)
		}
	}))
	t.Cleanup(stub.server.Close)
	return stub
}

func (s *rpcCompactStub) callCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

func rpcCompactSSEStart(id string) string {
	return fmt.Sprintf("event: message_start\ndata: {\"message\":{\"id\":%q,\"model\":\"local-model\",\"usage\":{\"input_tokens\":5}}}\n\n", id) +
		"event: content_block_start\ndata: {\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n"
}

func rpcCompactSSEText(text string) string {
	return rpcCompactSSEStart("reply") +
		fmt.Sprintf("event: content_block_delta\ndata: {\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":%q}}\n\n", text) +
		"event: content_block_stop\ndata: {\"index\":0}\n\nevent: message_delta\ndata: {\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":1}}\n\nevent: message_stop\ndata: {}\n\n"
}

func rpcCompactFixture(t *testing.T, baseURL string) (*quitFixture, string) {
	t.Helper()
	f := &quitFixture{home: t.TempDir(), work: t.TempDir()}
	f.agentDir = filepath.Join(f.home, ".pi", "agent")
	writeAppFile(t, filepath.Join(f.agentDir, "settings.json"), `{"defaultProvider":"anthropic","defaultModel":"local-model","defaultThinkingLevel":"off","compaction":{"enabled":false,"keepRecentTokens":50}}`)
	writeAppFile(t, filepath.Join(f.agentDir, "models.json"), `{"providers":{"anthropic":{"models":[{"id":"local-model","api":"anthropic-messages","baseUrl":"`+baseURL+`","contextWindow":22000,"maxTokens":4096,"input":["text"]}]}}}`)
	writeAppFile(t, filepath.Join(f.agentDir, "auth.json"), `{"anthropic":{"type":"api_key","key":"synthetic-key"}}`)
	store, err := session.New(session.OpenOptions{Cwd: f.work, SessionDir: filepath.Join(f.home, "sessions"), AgentDir: f.agentDir, Persist: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.AppendMessage(agentMessageUser("seed")); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AppendMessage(session.AgentMessage{Role: "assistant", Assistant: &session.AssistantMessage{
		Role: "assistant", API: "anthropic-messages", Provider: "anthropic", Model: "local-model",
		StopReason: "stop", Content: []session.ContentBlock{{Type: "text", Text: &session.TextContent{Type: "text", Text: strings.Repeat("prior ", 6000)}}},
	}}); err != nil {
		t.Fatal(err)
	}
	path := store.File()
	store.Unlock()
	return f, path
}

func rpcCompactSawPartial(objects []map[string]any) bool {
	for _, obj := range objects {
		if obj["type"] != "message_update" {
			continue
		}
		update, _ := obj["assistantMessageEvent"].(map[string]any)
		if update["type"] == "text_delta" && update["delta"] == rpcCompactPartial {
			return true
		}
	}
	return false
}

func rpcCompactResponse(objects []map[string]any, id string) map[string]any {
	return responseByID(objects, id)
}

func TestRPCProcessCompactSettlement(t *testing.T) {
	stub := startRPCCompactStub(t)
	f, sessionPath := rpcCompactFixture(t, stub.server.URL)
	child := startRPCChild(t, f, "--session", sessionPath)

	child.send(map[string]any{"id": "p1", "type": "prompt", "message": "second work"})
	child.log.waitObjects(t, "partial text", rpcCompactSawPartial)
	<-stub.firstSeen
	child.send(map[string]any{"id": "c1", "type": "compact", "customInstructions": "FOCUS_MARKER"})
	select {
	case body := <-stub.summarySeen:
		if !strings.Contains(body, "FOCUS_MARKER") {
			t.Fatalf("summary request lacks the focus marker: %s", body)
		}
	case <-time.After(8 * time.Second):
		t.Fatal("summary request never arrived")
	}
	<-stub.firstCancel
	prompts, aborted, checkpoints := compactTranscript(t, sessionPath)
	if prompts != 1 || aborted != 1 || checkpoints != 0 {
		t.Fatalf("durable prompts/aborted/checkpoints at summary start = %d/%d/%d, want 1/1/0", prompts, aborted, checkpoints)
	}
	if got := rpcCompactResponse(child.log.objects(t), "c1"); got != nil {
		t.Fatalf("terminal compact response arrived while the summary was held: %v", got)
	}
	close(stub.releaseSummary)
	child.log.waitObjects(t, "compact response", func(objects []map[string]any) bool {
		got := rpcCompactResponse(objects, "c1")
		return got != nil && got["success"] == true
	})
	objects := child.log.objects(t)
	if !hasEvent(objects, "compaction_end", map[string]any{"reason": "manual"}) {
		t.Fatalf("missing manual compaction_end: %v", objects)
	}
	if n := rpcCountResponses(objects, "c1"); n != 1 {
		t.Fatalf("terminal compact responses = %d, want 1", n)
	}
	child.closeStdin()
	child.waitExit()
	prompts, aborted, checkpoints = compactTranscript(t, sessionPath)
	if prompts != 1 || aborted != 1 || checkpoints != 1 {
		t.Fatalf("durable prompts/aborted/checkpoints = %d/%d/%d, want 1/1/1", prompts, aborted, checkpoints)
	}
	if calls := stub.callCount(); calls != 2 {
		t.Fatalf("provider calls = %d, want the interrupted run and one summary", calls)
	}
}

func TestRPCCompactDeliversPreviouslyQueuedSteering(t *testing.T) {
	t.Run("completed", func(t *testing.T) { testRPCCompactQueuedSteering(t, false) })
	t.Run("aborted after checkpoint", func(t *testing.T) { testRPCCompactQueuedSteering(t, true) })
}

func testRPCCompactQueuedSteering(t *testing.T, abort bool) {
	t.Helper()
	stub := startRPCCompactStub(t)
	f, sessionPath := rpcCompactFixture(t, stub.server.URL)
	child := startRPCChild(t, f, "--session", sessionPath)

	child.send(map[string]any{"id": "p1", "type": "prompt", "message": "second work"})
	child.log.waitObjects(t, "partial text", rpcCompactSawPartial)
	child.send(map[string]any{"id": "p2", "type": "prompt", "message": "queued steer", "streamingBehavior": "steer"})
	child.log.waitObjects(t, "queued prompt response", func(objects []map[string]any) bool {
		got := rpcCompactResponse(objects, "p2")
		return got != nil && got["success"] == true
	})
	child.send(map[string]any{"id": "c1", "type": "compact"})
	select {
	case <-stub.summarySeen:
	case <-time.After(8 * time.Second):
		t.Fatal("summary request never arrived")
	}
	close(stub.releaseSummary)
	var continuation string
	select {
	case continuation = <-stub.continuation:
	case <-time.After(8 * time.Second):
		t.Fatal("queued steering was never delivered after compaction")
	}
	if !strings.Contains(continuation, "queued steer") {
		t.Fatalf("continuation request lost queued steering: %s", continuation)
	}
	// The queued turn starts only after the checkpoint is durable. Aborting
	// that turn must not report the already committed compact as failed.
	if abort {
		child.send(map[string]any{"id": "a1", "type": "abort"})
	} else {
		close(stub.releaseContinuation)
	}
	objects := child.log.waitObjects(t, "compact response", func(objects []map[string]any) bool {
		return rpcCompactResponse(objects, "c1") != nil
	})
	if got := rpcCompactResponse(objects, "c1"); got["success"] != true {
		t.Fatalf("compaction result after queued turn (abort=%v): %v", abort, got)
	}
	if !hasEvent(objects, "compaction_end", map[string]any{"reason": "manual", "aborted": false}) {
		t.Fatalf("missing successful compaction event: %v", objects)
	}
	if !abort {
		var delivered bool
		for _, obj := range objects {
			message, _ := obj["message"].(map[string]any)
			if obj["type"] == "message_end" && message["role"] == "assistant" {
				raw, _ := json.Marshal(message["content"])
				delivered = delivered || strings.Contains(string(raw), "STEER_DELIVERED")
			}
		}
		if !delivered {
			t.Fatal("compact response preceded the queued turn's durable assistant event")
		}
	}
	child.closeStdin()
	child.waitExit()
	if calls := stub.callCount(); calls != 3 {
		t.Fatalf("provider calls = %d, want interrupted run, summary, continuation", calls)
	}
	_, entries, _, err := session.ReadFileEntries(sessionPath)
	if err != nil {
		t.Fatal(err)
	}
	prompts, queued, checkpoints := 0, 0, 0
	for _, entry := range entries {
		if entry.Compaction != nil {
			checkpoints++
		}
		if entry.Message != nil && entry.Message.Message.User != nil {
			switch entry.Message.Message.User.Content {
			case "second work":
				prompts++
			case "queued steer":
				queued++
			}
		}
	}
	if prompts != 1 || queued != 1 || checkpoints != 1 {
		t.Fatalf("original/queued prompts/checkpoints = %d/%d/%d, want 1/1/1", prompts, queued, checkpoints)
	}
}

func TestRPCProcessEOFShutdownWaitsForWorkers(t *testing.T) {
	stub := startRPCCompactStub(t)
	f, sessionPath := rpcCompactFixture(t, stub.server.URL)
	child := startRPCChild(t, f, "--session", sessionPath)

	child.send(map[string]any{"id": "p1", "type": "prompt", "message": "second work"})
	child.log.waitObjects(t, "partial text", rpcCompactSawPartial)
	<-stub.firstSeen
	child.send(map[string]any{"id": "c1", "type": "compact"})
	select {
	case <-stub.summarySeen:
	case <-time.After(8 * time.Second):
		t.Fatal("summary request never arrived")
	}
	child.closeStdin()
	child.waitExit()
	objects := child.log.objects(t)
	got := rpcCompactResponse(objects, "c1")
	if got == nil || got["success"] != false || got["error"] == "" {
		t.Fatalf("shutdown compact terminal response: %v", got)
	}
	if n := rpcCountResponses(objects, "c1"); n != 1 {
		t.Fatalf("terminal compact responses = %d, want 1", n)
	}
	prompts, aborted, checkpoints := compactTranscript(t, sessionPath)
	if prompts != 1 || aborted != 1 || checkpoints != 0 {
		t.Fatalf("durable prompts/aborted/checkpoints = %d/%d/%d, want 1/1/0", prompts, aborted, checkpoints)
	}
}

func TestRPCProcessSignalShutdownWaitsForWorkers(t *testing.T) {
	stub := startRPCCompactStub(t)
	f, sessionPath := rpcCompactFixture(t, stub.server.URL)
	child := startRPCChild(t, f, "--session", sessionPath)

	child.send(map[string]any{"id": "p1", "type": "prompt", "message": "second work"})
	child.log.waitObjects(t, "partial text", rpcCompactSawPartial)
	<-stub.firstSeen
	child.send(map[string]any{"id": "c1", "type": "compact"})
	select {
	case <-stub.summarySeen:
	case <-time.After(8 * time.Second):
		t.Fatal("summary request never arrived")
	}
	if err := child.cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-child.done:
		child.joined = true
		var exitErr *exec.ExitError
		if err == nil || !errors.As(err, &exitErr) || exitErr.ExitCode() != 143 {
			t.Fatalf("signal exit = %v, want status 143", err)
		}
	case <-time.After(8 * time.Second):
		_ = child.cmd.Process.Kill()
		<-child.done
		child.joined = true
		t.Fatal("rpc process did not exit after SIGTERM")
	}
	objects := child.log.objects(t)
	got := rpcCompactResponse(objects, "c1")
	if got == nil || got["success"] != false || got["error"] == "" {
		t.Fatalf("signal shutdown compact terminal response: %v", got)
	}
	if n := rpcCountResponses(objects, "c1"); n != 1 {
		t.Fatalf("terminal compact responses = %d, want 1", n)
	}
}

func rpcCountResponses(objects []map[string]any, id string) int {
	n := 0
	for _, obj := range objects {
		if obj["type"] == "response" && obj["id"] == id {
			n++
		}
	}
	return n
}
