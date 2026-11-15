//go:build unix

package app

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"psi/internal/session"
)

func bumpReviewSettings(t *testing.T, agentDir, url string) {
	t.Helper()
	reviewSettings(t, agentDir, url)
	writeAppFile(t, filepath.Join(agentDir, "settings.json"), `{"defaultProvider":"anthropic","defaultModel":"m","compaction":{"enabled":false},"retry":{"enabled":false}}`)
}

func bumpRequestUsers(body map[string]any) []any {
	messages, _ := body["messages"].([]any)
	var users []any
	for _, raw := range messages {
		message, _ := raw.(map[string]any)
		if message["role"] == "user" {
			users = append(users, message["content"])
		}
	}
	return users
}

func TestActualBumpCommand(t *testing.T) {
	home, work := t.TempDir(), t.TempDir()
	agentDir := filepath.Join(home, "agent")
	sessionDir := filepath.Join(home, "sessions")
	var mu sync.Mutex
	var bodies []map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			return
		}
		mu.Lock()
		bodies = append(bodies, body)
		requestNumber := len(bodies)
		mu.Unlock()
		if requestNumber == 1 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusInternalServerError)
			fmt.Fprint(w, `{"type":"error","error":{"type":"api_error","message":"synthetic-failure"}}`)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "event: message_start\ndata: {\"message\":{\"id\":\"synthetic\",\"model\":\"m\",\"usage\":{\"input_tokens\":1}}}\n\nevent: content_block_start\ndata: {\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\nevent: content_block_delta\ndata: {\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"ANSWER\"}}\n\nevent: content_block_stop\ndata: {\"index\":0}\n\nevent: message_delta\ndata: {\"delta\":{\"stop_reason\":\"end_turn\"}}\n\nevent: message_stop\ndata: {}\n\n")
	}))
	defer server.Close()
	bumpReviewSettings(t, agentDir, server.URL)

	q := startReviewPTY(t, home, work, agentDir, "--deny-project", "--no-tools", "--session-dir", sessionDir)
	q.prompt()
	q.write("hello\r")
	q.wait("transport (status 500)")
	q.prompt()
	q.write("/b\r")
	q.wait("ANSWER")
	q.prompt()
	if count := strings.Count(q.screen.Text(), "  › hello"); count != 1 {
		t.Fatalf("user row count=%d; want 1\n%s", count, q.screen.Text())
	}
	q.write("/quit\r")
	q.finish()

	mu.Lock()
	captured := append([]map[string]any(nil), bodies...)
	mu.Unlock()
	if len(captured) != 2 {
		t.Fatalf("HTTP requests=%d; want 2", len(captured))
	}
	firstUsers := bumpRequestUsers(captured[0])
	retryUsers := bumpRequestUsers(captured[1])
	if !reflect.DeepEqual(firstUsers, retryUsers) {
		t.Fatalf("retry changed user content: %#v -> %#v", firstUsers, retryUsers)
	}
	if len(retryUsers) != 1 {
		t.Fatalf("retry user messages=%d; want 1", len(retryUsers))
	}

	infos, err := session.Discover(sessionDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(infos) != 1 {
		t.Fatalf("sessions=%d; want 1", len(infos))
	}
	store, err := session.OpenFile(infos[0].Path, work, true)
	if err != nil {
		t.Fatal(err)
	}
	path, err := store.ActivePath()
	if err != nil {
		t.Fatal(err)
	}
	users, failures, answers := 0, 0, 0
	for _, entry := range path {
		if entry.Kind != "message" || entry.Message == nil {
			continue
		}
		message := entry.Message.Message
		switch {
		case message.User != nil:
			users++
		case message.Assistant != nil && message.Assistant.StopReason == "error":
			failures++
			if message.Assistant.ErrorMessage == nil || *message.Assistant.ErrorMessage == "" {
				t.Fatal("stored failure lacks a reason")
			}
		case message.Assistant != nil && message.Assistant.StopReason == "stop":
			answers++
		}
	}
	if users != 1 || failures != 1 || answers != 1 {
		t.Fatalf("session users=%d failures=%d answers=%d; want 1/1/1", users, failures, answers)
	}
	visibleUsers, visibleAnswers := 0, 0
	for _, item := range session.CurrentView(path) {
		switch item.Kind {
		case session.KindUser:
			visibleUsers++
		case session.KindAssistant:
			visibleAnswers++
			if item.Text != "ANSWER" {
				t.Fatalf("unexpected visible assistant text %q", item.Text)
			}
		}
	}
	if visibleUsers != 1 || visibleAnswers != 1 {
		t.Fatalf("visible users=%d answers=%d; want 1/1", visibleUsers, visibleAnswers)
	}
}

func TestActualBumpEmptyEnter(t *testing.T) {
	home, work := t.TempDir(), t.TempDir()
	agentDir := filepath.Join(home, "agent")
	sessionDir := filepath.Join(home, "sessions")
	var mu sync.Mutex
	var bodies []map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			return
		}
		mu.Lock()
		bodies = append(bodies, body)
		requestNumber := len(bodies)
		mu.Unlock()
		if requestNumber == 1 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusInternalServerError)
			fmt.Fprint(w, `{"type":"error","error":{"type":"api_error","message":"synthetic-failure"}}`)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "event: message_start\ndata: {\"message\":{\"id\":\"synthetic\",\"model\":\"m\",\"usage\":{\"input_tokens\":1}}}\n\nevent: content_block_start\ndata: {\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\nevent: content_block_delta\ndata: {\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"ANSWER\"}}\n\nevent: content_block_stop\ndata: {\"index\":0}\n\nevent: message_delta\ndata: {\"delta\":{\"stop_reason\":\"end_turn\"}}\n\nevent: message_stop\ndata: {}\n\n")
	}))
	defer server.Close()
	bumpReviewSettings(t, agentDir, server.URL)

	q := startReviewPTY(t, home, work, agentDir, "--deny-project", "--no-tools", "--session-dir", sessionDir)
	q.prompt()
	q.write("hello\r")
	q.wait("transport (status 500)")
	q.prompt()
	q.write("\r")
	q.wait("ANSWER")
	q.prompt()
	if count := strings.Count(q.screen.Text(), "  › hello"); count != 1 {
		t.Fatalf("user row count=%d; want 1\n%s", count, q.screen.Text())
	}
	q.write("/quit\r")
	q.finish()

	mu.Lock()
	captured := append([]map[string]any(nil), bodies...)
	mu.Unlock()
	if len(captured) != 2 {
		t.Fatalf("HTTP requests=%d; want 2", len(captured))
	}
	infos, err := session.Discover(sessionDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(infos) != 1 {
		t.Fatalf("sessions=%d; want 1", len(infos))
	}
	store, err := session.OpenFile(infos[0].Path, work, true)
	if err != nil {
		t.Fatal(err)
	}
	users := 0
	for _, entry := range mustActivePath(t, store) {
		if entry.Kind == "message" && entry.Message != nil && entry.Message.Message.User != nil {
			users++
		}
	}
	if users != 1 {
		t.Fatalf("stored users=%d; want 1", users)
	}
}

func mustActivePath(t *testing.T, store *session.Store) []*session.Entry {
	t.Helper()
	path, err := store.ActivePath()
	if err != nil {
		t.Fatal(err)
	}
	return path
}

func TestActualBumpEmptyEnterCompletedAndWhitespaceStaySilent(t *testing.T) {
	home, work := t.TempDir(), t.TempDir()
	agentDir := filepath.Join(home, "agent")
	sessionDir := filepath.Join(home, "sessions")
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()
	reviewSettings(t, agentDir, server.URL)
	store, err := session.New(session.OpenOptions{Cwd: work, SessionDir: sessionDir, AgentDir: agentDir, Persist: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.AppendMessage(session.AgentMessage{Role: "user", User: &session.UserMessage{Role: "user", Content: "old question", Timestamp: 1}}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AppendMessage(session.AgentMessage{Role: "assistant", Assistant: &session.AssistantMessage{
		Role: "assistant", StopReason: "stop",
		Content: []session.ContentBlock{{Type: "text", Text: &session.TextContent{Type: "text", Text: "old answer"}}},
	}}); err != nil {
		t.Fatal(err)
	}
	pathBefore := mustActivePath(t, store)
	entriesBefore := len(pathBefore)

	q := startReviewPTY(t, home, work, agentDir, "--deny-project", "--no-tools", "--session", store.File())
	q.prompt()
	q.write("\r/hotkeys\r")
	q.wait("Keys (fixed, no Kitty required):")
	q.write("   \r/hotkeys\r")
	q.wait("Keys (fixed, no Kitty required):")
	q.write("/quit\r")
	q.finish()

	if requests.Load() != 0 {
		t.Fatalf("empty Enter reached provider %d times", requests.Load())
	}
	reopened, err := session.OpenFile(store.File(), work, true)
	if err != nil {
		t.Fatal(err)
	}
	pathAfter := mustActivePath(t, reopened)
	if len(pathAfter) != entriesBefore {
		t.Fatalf("empty Enter added session entries: %d -> %d", entriesBefore, len(pathAfter))
	}
	if count := strings.Count(q.screen.Text(), "  › old question"); count != 1 {
		t.Fatalf("resumed user row count=%d; want 1", count)
	}
}

func TestActualBumpInterruptedStream(t *testing.T) {
	home, work := t.TempDir(), t.TempDir()
	agentDir := filepath.Join(home, "agent")
	sessionDir := filepath.Join(home, "sessions")
	var requests atomic.Int32
	var mu sync.Mutex
	var bodies []map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			return
		}
		mu.Lock()
		bodies = append(bodies, body)
		mu.Unlock()
		number := requests.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		if number == 1 {
			fmt.Fprint(w, "event: message_start\ndata: {\"message\":{\"id\":\"synthetic\",\"model\":\"m\",\"usage\":{\"input_tokens\":1}}}\n\nevent: content_block_start\ndata: {\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\nevent: content_block_delta\ndata: {\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"PARTIAL\"}}\n\nevent: content_block_stop\ndata: {\"index\":0}\n\n")
			return
		}
		fmt.Fprint(w, "event: message_start\ndata: {\"message\":{\"id\":\"synthetic\",\"model\":\"m\",\"usage\":{\"input_tokens\":1}}}\n\nevent: content_block_start\ndata: {\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\nevent: content_block_delta\ndata: {\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"ANSWER\"}}\n\nevent: content_block_stop\ndata: {\"index\":0}\n\nevent: message_delta\ndata: {\"delta\":{\"stop_reason\":\"end_turn\"}}\n\nevent: message_stop\ndata: {}\n\n")
	}))
	defer server.Close()
	bumpReviewSettings(t, agentDir, server.URL)

	q := startReviewPTY(t, home, work, agentDir, "--deny-project", "--no-tools", "--session-dir", sessionDir)
	q.prompt()
	q.write("stream\r")
	q.wait("provider stream interrupted")
	q.prompt()
	if got := requests.Load(); got != 1 {
		t.Fatalf("requests before /b=%d; want 1 (no hidden replay)", got)
	}
	q.write("/b\r")
	q.wait("ANSWER")
	q.prompt()
	if got := requests.Load(); got != 2 {
		t.Fatalf("requests after /b=%d; want 2", got)
	}
	if count := strings.Count(q.screen.Text(), "PARTIAL"); count != 1 {
		t.Fatalf("visible partial count=%d; want 1\n%s", count, q.screen.Text())
	}
	q.write("/quit\r")
	q.finish()

	mu.Lock()
	captured := append([]map[string]any(nil), bodies...)
	mu.Unlock()
	if len(captured) != 2 {
		t.Fatalf("captured bodies=%d; want 2", len(captured))
	}
	retryBody, err := json.Marshal(captured[1])
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(retryBody, []byte("PARTIAL")) {
		t.Fatalf("retry request replayed the failed assistant: %s", retryBody)
	}
	infos, err := session.Discover(sessionDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(infos) != 1 {
		t.Fatalf("sessions=%d; want 1", len(infos))
	}
	store, err := session.OpenFile(infos[0].Path, work, true)
	if err != nil {
		t.Fatal(err)
	}
	partials, answers := 0, 0
	for _, entry := range mustActivePath(t, store) {
		if entry.Kind != "message" || entry.Message == nil || entry.Message.Message.Assistant == nil {
			continue
		}
		assistant := entry.Message.Message.Assistant
		if assistant.StopReason == "error" {
			partials++
		}
		if assistant.StopReason == "stop" {
			answers++
		}
	}
	if partials != 1 || answers != 1 {
		t.Fatalf("stored partials=%d answers=%d; want 1/1", partials, answers)
	}
}
