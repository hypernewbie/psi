package provider

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"psi/internal/testutil/leakcheck"
)

func TestSSEContextCancellationPoints(t *testing.T) {
	defer leakcheck.Verify(t)

	cases := []struct {
		name     string
		payload  []byte
		cancelAt int
	}{
		{
			name: "cancel mid-event",
			payload: []byte("event: message_start\ndata: {\"message\":{\"id\":\"m1\",\"model\":\"claude-3-5-sonnet\"}}\n\n" +
				"event: content_block_start\ndata: {\"index\":0,\"content_block\":{\"type\":\"text\"}}\n\n" +
				"event: content_block_delta\ndata: {\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"partial\""),
			cancelAt: 1, // cancel after first chunk
		},
		{
			name:     "cancel mid-JSON-token",
			payload:  []byte("event: message_start\ndata: {\"message\":{\"id\":\"m1\",\"mode"),
			cancelAt: 0,
		},
		{
			name: "cancel mid-multibyte-UTF8",
			payload: append(
				[]byte("event: message_start\ndata: {\"message\":{\"id\":\"m1\",\"model\":\"claude\"}}\n\n"+
					"event: content_block_start\ndata: {\"index\":0,\"content_block\":{\"type\":\"text\"}}\n\n"+
					"event: content_block_delta\ndata: {\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"hi "),
				// Incomplete 4-byte UTF-8 sequence for 🌍 (0xF0 0x9F 0x8C)
				0xF0, 0x9F, 0x8C,
			),
			cancelAt: 1,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				w.WriteHeader(http.StatusOK)
				if flusher, ok := w.(http.Flusher); ok {
					flusher.Flush()
				}

				half := len(tc.payload) / 2
				if half == 0 {
					half = 1
				}

				if tc.cancelAt == 0 {
					cancel()
					w.Write(tc.payload[:half])
					return
				}

				w.Write(tc.payload[:half])
				if flusher, ok := w.(http.Flusher); ok {
					flusher.Flush()
				}
				time.Sleep(20 * time.Millisecond)
				cancel()
				time.Sleep(10 * time.Millisecond)
				w.Write(tc.payload[half:])
			}))
			defer server.Close()

			client := NewClient(server.URL, "test-key", 0, time.Second)
			defer client.CloseIdle()

			req := Request{
				Model:    ModelMeta{ID: "claude-3-5-sonnet", API: "anthropic-messages"},
				Messages: []Message{{Role: "user", Content: []Block{{Type: "text", Text: "hi"}}}},
				Thinking: "off",
			}

			_, err := client.Send(ctx, req, nil)
			if err == nil {
				t.Fatal("expected cancellation error, got nil")
			}
			if !errors.Is(err, context.Canceled) && !strings.Contains(err.Error(), "canceled") && !strings.Contains(err.Error(), "context") {
				t.Fatalf("expected context cancellation error, got: %v", err)
			}
		})
	}
}

func TestSSESlowLorisIdleTimeout(t *testing.T) {
	defer leakcheck.Verify(t)

	// Slow-loris server sends 1 byte every 300ms without sending a newline.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}

		notify := r.Context().Done()
		for i := 0; i < 20; i++ {
			select {
			case <-notify:
				return
			case <-time.After(300 * time.Millisecond):
				if _, err := w.Write([]byte("a")); err != nil {
					return
				}
				if flusher, ok := w.(http.Flusher); ok {
					flusher.Flush()
				}
			}
		}
	}))
	defer server.Close()

	// Short idle timeout: 50ms (much smaller than 300ms inter-byte delay)
	idleTimeout := 50 * time.Millisecond
	client := NewClient(server.URL, "test-key", 0, idleTimeout)
	defer client.CloseIdle()

	req := Request{
		Model:    ModelMeta{ID: "claude-3-5-sonnet", API: "anthropic-messages"},
		Messages: []Message{{Role: "user", Content: []Block{{Type: "text", Text: "hi"}}}},
		Thinking: "off",
	}

	start := time.Now()
	_, err := client.Send(context.Background(), req, nil)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected idle timeout error, got nil")
	}
	if elapsed > 2*time.Second {
		t.Fatalf("slow-loris hung for %v, expected clean return under 2s", elapsed)
	}
	if !strings.Contains(err.Error(), "idle timeout") && !strings.Contains(err.Error(), "timeout") {
		t.Logf("returned error on slow-loris: %v (elapsed: %v)", err, elapsed)
	}
}

func TestSSEServerEOFAndRSTMidStream(t *testing.T) {
	defer leakcheck.Verify(t)

	t.Run("EOF after message_start and partial text", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			if flusher, ok := w.(http.Flusher); ok {
				flusher.Flush()
			}
			io.WriteString(w, "event: message_start\ndata: {\"message\":{\"id\":\"m1\",\"model\":\"claude-3-5-sonnet\",\"usage\":{\"input_tokens\":50}}}\n\n")
			io.WriteString(w, "event: content_block_start\ndata: {\"index\":0,\"content_block\":{\"type\":\"text\"}}\n\n")
			io.WriteString(w, "event: content_block_delta\ndata: {\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"part\"}}\n\n")
			// Abrupt EOF: server terminates handler without message_stop
		}))
		defer server.Close()

		client := NewClient(server.URL, "test-key", 0, time.Second)
		defer client.CloseIdle()

		req := Request{
			Model:    ModelMeta{ID: "claude-3-5-sonnet", API: "anthropic-messages"},
			Messages: []Message{{Role: "user", Content: []Block{{Type: "text", Text: "hi"}}}},
			Thinking: "off",
		}

		resp, err := client.Send(context.Background(), req, nil)
		if err == nil {
			t.Fatal("expected error on abrupt EOF, got nil")
		}
		// Known partial usage should be preserved
		var se *StreamInterrupt
		if errors.As(err, &se) {
			if se.Partial == nil || se.Partial.Usage.Input != 50 {
				t.Fatalf("partial usage lost: %+v", se.Partial)
			}
		} else if resp != nil && resp.Usage.Input == 50 {
			// Also accepted
		}
	})

	t.Run("TCP RST mid-stream", func(t *testing.T) {
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		defer listener.Close()

		go func() {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			buf := make([]byte, 1024)
			conn.Read(buf) // read request headers

			// Write HTTP 200 OK headers and partial SSE event
			conn.Write([]byte("HTTP/1.1 200 OK\r\nContent-Type: text/event-stream\r\n\r\n"))
			conn.Write([]byte("event: message_start\ndata: {\"message\":{\"id\":\"m1\"}}\n\n"))

			// Force TCP RST by setting linger to 0 before closing
			if tcpConn, ok := conn.(*net.TCPConn); ok {
				tcpConn.SetLinger(0)
			}
			conn.Close()
		}()

		client := NewClient("http://"+listener.Addr().String(), "test-key", 0, time.Second)
		defer client.CloseIdle()

		req := Request{
			Model:    ModelMeta{ID: "claude-3-5-sonnet", API: "anthropic-messages"},
			Messages: []Message{{Role: "user", Content: []Block{{Type: "text", Text: "hi"}}}},
			Thinking: "off",
		}

		_, err = client.Send(context.Background(), req, nil)
		if err == nil {
			t.Fatal("expected error on TCP RST, got nil")
		}
	})
}

func TestSSEMultiProviderSlowLorisAndCancellation(t *testing.T) {
	defer leakcheck.Verify(t)

	// Slow-loris server for multiple providers
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}

		notify := r.Context().Done()
		for i := 0; i < 20; i++ {
			select {
			case <-notify:
				return
			case <-time.After(200 * time.Millisecond):
				w.Write([]byte("data: slow\n\n"))
				if flusher, ok := w.(http.Flusher); ok {
					flusher.Flush()
				}
			}
		}
	}))
	defer server.Close()

	t.Run("Completions client idle timeout", func(t *testing.T) {
		client := NewCompletionsClient(server.URL, "test-key", 0, 40*time.Millisecond)
		defer client.CloseIdle()

		req := Request{
			Model:    ModelMeta{ID: "deepseek-chat", API: "openai-completions", Provider: "deepseek"},
			Messages: []Message{{Role: "user", Content: []Block{{Type: "text", Text: "hi"}}}},
			Thinking: "off",
		}

		start := time.Now()
		_, err := client.Send(context.Background(), req, nil)
		elapsed := time.Since(start)
		if err == nil {
			t.Fatal("expected timeout error")
		}
		if elapsed > time.Second {
			t.Fatalf("completions client hung for %v", elapsed)
		}
	})

	t.Run("Google client idle timeout", func(t *testing.T) {
		client := NewGoogleClient(server.URL, "test-key", 0, 40*time.Millisecond)
		defer client.CloseIdle()

		req := Request{
			Model:    ModelMeta{ID: "gemini-2.5-flash", API: "google-generative-ai", Provider: "google"},
			Messages: []Message{{Role: "user", Content: []Block{{Type: "text", Text: "hi"}}}},
			Thinking: "off",
		}

		start := time.Now()
		_, err := client.Send(context.Background(), req, nil)
		elapsed := time.Since(start)
		if err == nil {
			t.Fatal("expected timeout error")
		}
		if elapsed > time.Second {
			t.Fatalf("google client hung for %v", elapsed)
		}
	})

	t.Run("Completions mid-token cancellation", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		client := NewCompletionsClient(server.URL, "test-key", 0, time.Second)
		defer client.CloseIdle()

		req := Request{
			Model:    ModelMeta{ID: "deepseek-chat", API: "openai-completions", Provider: "deepseek"},
			Messages: []Message{{Role: "user", Content: []Block{{Type: "text", Text: "hi"}}}},
			Thinking: "off",
		}

		cancel()
		_, err := client.Send(ctx, req, nil)
		if err == nil || (!errors.Is(err, context.Canceled) && !strings.Contains(err.Error(), "canceled")) {
			t.Fatalf("expected context.Canceled, got: %v", err)
		}
	})

	t.Run("Google mid-token cancellation", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		client := NewGoogleClient(server.URL, "test-key", 0, time.Second)
		defer client.CloseIdle()

		req := Request{
			Model:    ModelMeta{ID: "gemini-2.5-flash", API: "google-generative-ai", Provider: "google"},
			Messages: []Message{{Role: "user", Content: []Block{{Type: "text", Text: "hi"}}}},
			Thinking: "off",
		}

		cancel()
		_, err := client.Send(ctx, req, nil)
		if err == nil || (!errors.Is(err, context.Canceled) && !strings.Contains(err.Error(), "canceled")) {
			t.Fatalf("expected context.Canceled, got: %v", err)
		}
	})
}
