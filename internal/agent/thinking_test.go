package agent

// Thinking deltas reach the app as their own event kind, survive an
// interrupted turn, and never turn a reasoning mismatch into a failed turn.

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"psi/internal/event"
	"psi/internal/provider"
	"psi/internal/session"
)

func thinkingInterruptServer(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fl, ok := w.(http.Flusher)
		if !ok {
			t.Error("no flusher")
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintf(w, "event: message_start\ndata: {\"message\":{\"id\":\"m1\",\"usage\":{\"input_tokens\":10}}}\n\n")
		fl.Flush()
		fmt.Fprintf(w, "event: content_block_start\ndata: {\"index\":0,\"content_block\":{\"type\":\"thinking\",\"thinking\":\"weigh \"}}\n\n")
		fl.Flush()
		fmt.Fprintf(w, "event: content_block_delta\ndata: {\"index\":0,\"delta\":{\"type\":\"thinking_delta\",\"thinking\":\"options\"}}\n\n")
		fl.Flush()
		fmt.Fprintf(w, "event: content_block_start\ndata: {\"index\":1,\"content_block\":{\"type\":\"text\"}}\n\n")
		fl.Flush()
		fmt.Fprintf(w, "event: content_block_delta\ndata: {\"index\":1,\"delta\":{\"type\":\"text_delta\",\"text\":\"visible\"}}\n\n")
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

func TestInterruptedThinkingIsPersistedBesideVisibleText(t *testing.T) {
	server := thinkingInterruptServer(t)
	defer server.Close()
	_, _, _, loop, _ := testSetup(t)
	loop.prov = provider.NewClient(server.URL, "test-key", 0, 5*time.Second)
	var events []event.Event
	loop.sink = func(e event.Event) { events = append(events, e) }
	cfg := Config{Model: modelMeta(), Thinking: "off", Tools: toolDefs(), System: "base"}
	err := loop.Submit(context.Background(), "hello", cfg)
	if err == nil || !strings.Contains(err.Error(), "interrupted") {
		t.Fatalf("expected interruption, got %v", err)
	}
	var thinking, text strings.Builder
	for _, e := range events {
		switch e.Type {
		case event.ThinkingDelta:
			thinking.WriteString(e.Text)
		case event.TextDelta:
			text.WriteString(e.Text)
		}
	}
	if thinking.String() != "weigh options" {
		t.Fatalf("thinking events = %q, want %q", thinking.String(), "weigh options")
	}
	if text.String() != "visible" {
		t.Fatalf("text events = %q, want %q", text.String(), "visible")
	}
	// The durable message keeps the reasoning block and the visible text.
	path, err := loop.store.ActivePath()
	if err != nil {
		t.Fatal(err)
	}
	blocks := 0
	for _, e := range path {
		if e.Kind != "message" || e.Message.Message.Assistant == nil {
			continue
		}
		for _, b := range e.Message.Message.Assistant.Content {
			blocks++
			switch b.Type {
			case "thinking":
				if b.Thinking.Thinking != "weigh options" {
					t.Fatalf("persisted thinking = %q", b.Thinking.Thinking)
				}
			case "text":
				if b.Text.Text != "visible" {
					t.Fatalf("persisted text = %q", b.Text.Text)
				}
			}
		}
	}
	if blocks != 2 {
		t.Fatalf("persisted blocks = %d, want 2", blocks)
	}
	// Replay exposes the reasoning as its own transcript item.
	var replayThinking string
	if _, err := loop.store.Timeline(false, 0, func(item session.TranscriptItem) bool {
		if item.Kind == session.KindThinking {
			replayThinking = item.Text
		}
		return true
	}); err != nil {
		t.Fatal(err)
	}
	if replayThinking != "weigh options" {
		t.Fatalf("replayed thinking = %q, want %q", replayThinking, "weigh options")
	}
}

func TestThinkingMismatchNeverFailsTheTurn(t *testing.T) {
	_, _, fake, loop, _ := testSetup(t)
	// The provider streams reasoning that the final response contradicts.
	fake.ThinkingDeltas = [][]string{{"first"}}
	fake.Responses = []*provider.Response{{Text: "done", Thinking: "second", StopReason: "stop"}}
	var events []event.Event
	loop.sink = func(e event.Event) { events = append(events, e) }
	cfg := Config{Model: modelMeta(), Thinking: "off", Tools: toolDefs(), System: "base"}
	if err := loop.Submit(context.Background(), "hello", cfg); err != nil {
		t.Fatalf("thinking mismatch failed the turn: %v", err)
	}
	var thinking strings.Builder
	for _, e := range events {
		if e.Type == event.ThinkingDelta {
			thinking.WriteString(e.Text)
		}
	}
	if thinking.String() != "first" {
		t.Fatalf("thinking events = %q, want %q", thinking.String(), "first")
	}
	// The durable message keeps the authoritative reasoning.
	path, err := loop.store.ActivePath()
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range path {
		if e.Kind != "message" || e.Message.Message.Assistant == nil {
			continue
		}
		for _, b := range e.Message.Message.Assistant.Content {
			if b.Type == "thinking" && b.Thinking.Thinking != "second" {
				t.Fatalf("persisted thinking = %q, want %q", b.Thinking.Thinking, "second")
			}
		}
	}
}

func TestThinkingDeltaWithoutStreamedTextIsEmittedOnce(t *testing.T) {
	_, _, fake, loop, _ := testSetup(t)
	fake.Responses = []*provider.Response{{Text: "done", Thinking: "only final", StopReason: "stop"}}
	var events []event.Event
	loop.sink = func(e event.Event) { events = append(events, e) }
	cfg := Config{Model: modelMeta(), Thinking: "off", Tools: toolDefs(), System: "base"}
	if err := loop.Submit(context.Background(), "hello", cfg); err != nil {
		t.Fatal(err)
	}
	var thinking strings.Builder
	thinkingAt, textAt := -1, -1
	for i, e := range events {
		switch e.Type {
		case event.ThinkingDelta:
			if thinkingAt < 0 {
				thinkingAt = i
			}
			thinking.WriteString(e.Text)
		case event.TextDelta:
			if textAt < 0 {
				textAt = i
			}
		}
	}
	if thinking.String() != "only final" {
		t.Fatalf("thinking events = %q, want %q", thinking.String(), "only final")
	}
	// Reasoning precedes the answer in the message content; the unstreamed
	// tail must keep that order so the ledger renders thinking before text.
	if thinkingAt > textAt {
		t.Fatalf("thinking event (%d) followed the text event (%d)", thinkingAt, textAt)
	}
}
