package provider

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"psi/internal/testutil/leakcheck"
)

func TestCloseIdleConcurrentWithSend(t *testing.T) {
	defer leakcheck.Verify(t)

	// An SSE server that delivers a response with a small delay between chunks.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}

		io.WriteString(w, "event: message_start\ndata: {\"message\":{\"id\":\"m1\",\"model\":\"claude-3-5-sonnet\",\"usage\":{\"input_tokens\":10}}}\n\n")
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		time.Sleep(5 * time.Millisecond)

		io.WriteString(w, "event: content_block_start\ndata: {\"index\":0,\"content_block\":{\"type\":\"text\"}}\n\n")
		io.WriteString(w, "event: content_block_delta\ndata: {\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"concurrent\"}}\n\n")
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		time.Sleep(5 * time.Millisecond)

		io.WriteString(w, "event: content_block_stop\ndata: {\"index\":0}\n\n")
		io.WriteString(w, "event: message_delta\ndata: {\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":2}}\n\n")
		io.WriteString(w, "event: message_stop\ndata: {}\n\n")
	}))
	defer server.Close()

	client := NewClient(server.URL, "test-key", 2, time.Second)
	defer client.CloseIdle()

	req := Request{
		Model:    ModelMeta{ID: "claude-3-5-sonnet", API: "anthropic-messages"},
		Messages: []Message{{Role: "user", Content: []Block{{Type: "text", Text: "hi"}}}},
		Thinking: "off",
	}

	const senders = 10
	const closers = 4
	stopClosers := make(chan struct{})

	var wg sync.WaitGroup

	// Launch background goroutines repeatedly calling CloseIdle
	for i := 0; i < closers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stopClosers:
					return
				default:
					client.CloseIdle()
					time.Sleep(2 * time.Millisecond)
				}
			}
		}()
	}

	// Launch parallel Send callers
	var successfulSends atomic.Int32
	for i := 0; i < senders; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for iter := 0; iter < 3; iter++ {
				_, err := client.Send(context.Background(), req, nil)
				if err == nil {
					successfulSends.Add(1)
				}
			}
		}()
	}

	// Wait for senders to finish, then stop closers
	time.Sleep(100 * time.Millisecond)
	close(stopClosers)
	wg.Wait()

	if successfulSends.Load() == 0 {
		t.Fatal("expected at least some successful sends despite CloseIdle calls")
	}
}

func TestFakeCloseIdleConcurrentWithSend(t *testing.T) {
	defer leakcheck.Verify(t)

	const count = 30
	responses := make([]*Response, count)
	for i := 0; i < count; i++ {
		responses[i] = &Response{Text: "resp"}
	}
	fake := &Fake{
		Responses: responses,
	}

	var wg sync.WaitGroup
	stop := make(chan struct{})

	// Closer goroutine
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
				fake.CloseIdle()
				time.Sleep(1 * time.Millisecond)
			}
		}
	}()

	// Senders
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 3; j++ {
				fake.Send(context.Background(), Request{}, nil)
			}
		}()
	}

	time.Sleep(50 * time.Millisecond)
	close(stop)
	wg.Wait()

	fake.CloseIdle()
	if fake.CallCount() != count {
		t.Fatalf("expected %d calls recorded, got %d", count, fake.CallCount())
	}
}
