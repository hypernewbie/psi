package provider

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func completionsTextDeltaStream(text string) string {
	return `data: {"choices":[{"index":0,"delta":{"content":"` + text + `"},"finish_reason":"stop"}]}` + "\n\n"
}

func completionsTextStream(text string) string {
	return completionsTextDeltaStream(text) + "data: [DONE]\n\n"
}

func keylessCompletionsRequest() Request {
	return Request{
		Model:    ModelMeta{ID: "keyless", API: "openai-completions", Provider: "local", Custom: true, AllowMissingAPIKey: true},
		Thinking: "off",
		Messages: []Message{{Role: "user", Content: []Block{{Type: "text", Text: "hi"}}}},
	}
}

// A keyless Chat Completions request uses the shared retry loop. A transient
// status retries without ever adding an Authorization field. A 401 is auth
// failure, not absence, and must stop after one attempt with or without a key.
func TestOptionalAPIKeyNetworkFailure(t *testing.T) {
	t.Run("transient status retries keyless without auth", func(t *testing.T) {
		var requests atomic.Int64
		var authFields atomic.Int64
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			requests.Add(1)
			if _, present := r.Header["Authorization"]; present {
				authFields.Add(1)
			}
			if requests.Load() == 1 {
				w.Header().Set("Retry-After", "1")
				http.Error(w, "synthetic retry", http.StatusServiceUnavailable)
				return
			}
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, completionsTextStream("keyless retry"))
		}))
		defer server.Close()

		client := NewCompletionsClient(server.URL+"/v1", "", 1, time.Second)
		response, err := client.Send(context.Background(), keylessCompletionsRequest(), nil)
		if err != nil {
			t.Fatal(err)
		}
		if response.Text != "keyless retry" {
			t.Fatalf("response text = %q", response.Text)
		}
		if requests.Load() != 2 {
			t.Fatalf("requests = %d, want one 503 and one success", requests.Load())
		}
		if authFields.Load() != 0 {
			t.Fatalf("%d requests carried an Authorization field", authFields.Load())
		}
	})

	t.Run("401 stops after one attempt", func(t *testing.T) {
		var requests atomic.Int64
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			requests.Add(1)
			http.Error(w, "synthetic unauthorized", http.StatusUnauthorized)
		}))
		defer server.Close()

		client := NewCompletionsClient(server.URL+"/v1", "", 2, time.Second)
		if _, err := client.Send(context.Background(), keylessCompletionsRequest(), nil); err == nil {
			t.Fatal("a 401 was accepted")
		} else {
			var providerErr *ProviderError
			if !errors.As(err, &providerErr) || providerErr.Kind != ErrAuth {
				t.Fatalf("401 error = %v", err)
			}
		}
		if requests.Load() != 1 {
			t.Fatalf("requests = %d, want one", requests.Load())
		}
	})

	t.Run("keyed 401 has no unauthenticated fallback", func(t *testing.T) {
		var requests atomic.Int64
		var keyed atomic.Int64
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			requests.Add(1)
			if r.Header.Get("Authorization") == "Bearer synthetic-key" {
				keyed.Add(1)
			}
			http.Error(w, "synthetic unauthorized", http.StatusUnauthorized)
		}))
		defer server.Close()

		client := NewCompletionsClient(server.URL+"/v1", "synthetic-key", 2, time.Second)
		request := keylessCompletionsRequest()
		request.Model.AllowMissingAPIKey = false
		if _, err := client.Send(context.Background(), request, nil); err == nil {
			t.Fatal("a keyed 401 was accepted")
		}
		if requests.Load() != 1 || keyed.Load() != 1 {
			t.Fatalf("requests = %d, keyed = %d; want one keyed attempt", requests.Load(), keyed.Load())
		}
	})

	t.Run("cancelled partial stream is not retried", func(t *testing.T) {
		var requests atomic.Int64
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			requests.Add(1)
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, completionsTextDeltaStream("partial"))
			if flusher, ok := w.(http.Flusher); ok {
				flusher.Flush()
			}
			<-r.Context().Done()
		}))
		defer server.Close()

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		var once sync.Once
		var streamed string
		client := NewCompletionsClient(server.URL+"/v1", "", 2, time.Second)
		response, err := client.Send(ctx, keylessCompletionsRequest(), func(delta Delta) {
			if delta.Kind != DeltaText || delta.Text == "" {
				return
			}
			streamed += delta.Text
			once.Do(cancel)
		})
		var interrupt *StreamInterrupt
		if !errors.As(err, &interrupt) {
			t.Fatalf("cancelled partial stream error = %v", err)
		}
		if response == nil || response.Text != "partial" {
			t.Fatalf("retained partial response = %+v", response)
		}
		if interrupt.Partial == nil || interrupt.Partial.Text != "partial" || streamed != "partial" {
			t.Fatalf("partial text = %+v, streamed %q", interrupt.Partial, streamed)
		}
		if requests.Load() != 1 {
			t.Fatalf("requests = %d, want one", requests.Load())
		}
	})
}
