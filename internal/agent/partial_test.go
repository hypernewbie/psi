package agent

// PLAN3 F12 agent settlement: a stream that reports usage then fails persists
// that usage exactly once, saves visible partial text, executes no tool, and
// never retries the turn. Reopening the session shows the same accounting.

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"psi/internal/provider"
	"psi/internal/session"
)

func interruptServer(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fl, ok := w.(http.Flusher)
		if !ok {
			t.Error("no flusher")
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintf(w, "event: message_start\ndata: {\"message\":{\"id\":\"m1\",\"usage\":{\"input_tokens\":777,\"cache_read_input_tokens\":77}}}\n\n")
		fl.Flush()
		fmt.Fprintf(w, "event: content_block_start\ndata: {\"index\":0,\"content_block\":{\"type\":\"text\"}}\n\n")
		fl.Flush()
		fmt.Fprintf(w, "event: content_block_delta\ndata: {\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"visible\"}}\n\n")
		fl.Flush()
		// Drop the connection mid-stream: no stop reason, no message_stop.
		if hj, ok := w.(http.Hijacker); ok {
			if conn, _, err := hj.Hijack(); err == nil {
				conn.Close()
				return
			}
		}
	}))
}

func TestF12AgentPersistsPartialUsageExactlyOnce(t *testing.T) {
	server := interruptServer(t)
	defer server.Close()
	_, _, _, loop, _ := testSetup(t)
	loop.prov = provider.NewClient(server.URL, "test-key", 0, 5*time.Second)
	cfg := Config{Model: modelMeta(), Thinking: "off", Tools: toolDefs(), MaxTurns: 3, System: "base"}
	err := loop.Submit(context.Background(), "hello", cfg)
	if err == nil || !strings.Contains(err.Error(), "interrupted") {
		t.Fatalf("expected interruption, got %v", err)
	}
	// Usage recorded exactly once with known input, no invented output.
	path, err := loop.store.ActivePath()
	if err != nil {
		t.Fatal(err)
	}
	inputs, outputs := 0, 0
	var totalIn int64
	for _, e := range path {
		if e.Kind == "usage" && e.Usage != nil {
			inputs++
			totalIn += int64(e.Usage.Usage.Input)
			if e.Usage.Usage.Output != 0 {
				outputs++
			}
		}
		if e.Kind == "message" && e.Message.Message.Assistant != nil && len(e.Message.Message.Assistant.Content) > 0 {
			for _, b := range e.Message.Message.Assistant.Content {
				if b.Type == "toolCall" || (b.Call != nil) {
					t.Fatal("assistant tool call persisted from failed stream")
				}
			}
		}
	}
	if inputs != 1 {
		t.Fatalf("usage entries = %d, want exactly 1", inputs)
	}
	if totalIn != 777 {
		t.Fatalf("input total = %d, want 777", totalIn)
	}
	if outputs != 0 {
		t.Fatal("output invented on failed stream")
	}
	// Visible partial text must reach display-only replay after failure.
	var visible strings.Builder
	complete, err := loop.store.Timeline(false, 0, func(item session.TranscriptItem) bool {
		visible.WriteString(item.Text)
		return true
	})
	if err != nil || !complete || !strings.Contains(visible.String(), "visible") {
		t.Fatalf("partial text did not survive replay: complete=%v err=%v", complete, err)
	}
	// Accounting survives reopen: same durable records.
	store2, err := session.OpenFile(loop.store.File(), loop.store.Cwd(), true)
	if err != nil {
		t.Fatal(err)
	}
	proj2, err := store2.Projection()
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, e := range proj2.Entries {
		if e.Kind == "usage" && e.Usage != nil && int64(e.Usage.Usage.Input) == 777 {
			found = true
		}
	}
	if !found {
		t.Fatal("usage lost after reopen")
	}
}
