package agent

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"psi/internal/event"
	"psi/internal/provider"
	"psi/internal/session"
)

func TestReviewCancelledHTTPStreamPersistsUsage(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		// Even a completed call in an incomplete message is not executable.
		fmt.Fprint(w, `event: message_start
data: {"message":{"id":"synthetic","usage":{"input_tokens":777}}}

event: content_block_start
data: {"index":0,"content_block":{"type":"tool_use","id":"c","name":"write","input":{"path":"must-not-exist","content":"wrong"}}}

event: content_block_stop
data: {"index":0}

event: content_block_start
data: {"index":1,"content_block":{"type":"text"}}

event: content_block_delta
data: {"index":1,"delta":{"type":"text_delta","text":"visible"}}

`)
	}))
	defer server.Close()
	_, _, _, loop, _ := testSetup(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	loop.sink = func(e event.Event) {
		if e.Type == event.TextDelta {
			cancel()
		}
	}
	client := provider.NewClient(server.URL, "synthetic-key", 3, time.Second)
	defer client.CloseIdle()
	loop.prov = client
	cfg := Config{Model: modelMeta(), Thinking: "off", Tools: toolDefs(), System: "base"}
	if err := loop.Submit(ctx, "synthetic input", cfg); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected cancellation, got %v", err)
	}
	reopened, err := session.OpenFile(loop.store.File(), loop.store.Cwd(), true)
	if err != nil {
		t.Fatal(err)
	}
	path, err := reopened.ActivePath()
	if err != nil {
		t.Fatal(err)
	}
	usageRecords, partials := 0, 0
	for _, e := range path {
		if u := e.Usage; u != nil {
			usageRecords++
			if u.Usage.Input != 777 || u.Provider != "anthropic" || u.Model != cfg.Model.ID {
				t.Fatalf("wrong durable usage metadata: %+v", u)
			}
		}
		if e.Message != nil && e.Message.Message.Assistant != nil {
			a := e.Message.Message.Assistant
			if a.StopReason != "aborted" || len(a.Content) != 1 || a.Content[0].Text == nil || a.Content[0].Text.Text != "visible" {
				t.Fatalf("wrong durable partial: %+v", a)
			}
			partials++
		}
	}
	if requests.Load() != 1 || usageRecords != 1 || partials != 1 {
		t.Fatalf("requests=%d usage records=%d partials=%d", requests.Load(), usageRecords, partials)
	}
	if _, err := os.Stat(filepath.Join(loop.store.Cwd(), "must-not-exist")); !os.IsNotExist(err) {
		t.Fatal("an incomplete message executed its tool call")
	}
}
