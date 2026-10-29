package provider

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"psi/internal/modelmeta"
)

func TestCompletionsToolDeltasResolveByCallIDWithoutIndex(t *testing.T) {
	model := ModelMeta{ID: "model", API: "openai-completions", Provider: "opencode-go", Reasoning: true, Compat: modelmeta.Compat{}}
	wire := `data: {"choices":[{"index":0,"delta":{"tool_calls":[{"id":"call-a","function":{"name":"read","arguments":"{\"path\":"}}]},"finish_reason":null}]}` + "\n\n" +
		`data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":7,"id":"call-a","function":{"arguments":"\"a.txt\"}"}}]},"finish_reason":null}]}` + "\n\n" +
		`data: {"choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}` + "\n\n" + "data: [DONE]\n\n"
	response, err := parseCompletionsSSE(strings.NewReader(wire), time.Second, context.Background(), nil, Request{Model: model, Thinking: "off"})
	if err != nil {
		t.Fatal(err)
	}
	if len(response.ToolCalls) != 1 || response.ToolCalls[0].ID != "call-a" || response.ToolCalls[0].Arguments["path"] != "a.txt" {
		t.Fatalf("ID-resolved tool call = %+v", response.ToolCalls)
	}
}

func TestCompletionsMissingKeyAndHTTPErrorDoNotLeakSecrets(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.URL.RawQuery != "" {
			t.Errorf("credential appeared in request URL query: %q", r.URL.RawQuery)
		}
		http.Error(w, "provider echoed synthetic-secret-key", http.StatusBadRequest)
	}))
	defer server.Close()
	model := ModelMeta{ID: "model", API: "openai-completions", Provider: "deepseek", Compat: modelmeta.Compat{}}
	client := NewCompletionsClient(server.URL, "", 0, time.Second)
	if _, err := client.Send(context.Background(), Request{Model: model, Thinking: "off"}, nil); err == nil || !strings.Contains(err.Error(), "No API key") {
		t.Fatalf("missing key result = %v", err)
	}
	if requests.Load() != 0 {
		t.Fatal("request was sent without credentials")
	}
	client.APIKey = "synthetic-secret-key"
	if _, err := client.Send(context.Background(), Request{Model: model, Thinking: "off"}, nil); err == nil || strings.Contains(err.Error(), client.APIKey) {
		t.Fatalf("HTTP error exposed credential or was accepted: %v", err)
	}
	if requests.Load() != 1 {
		t.Fatalf("request count = %d", requests.Load())
	}
}
