package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestAdaptiveHTTPUsesEffortAndPreservesCacheAccounting(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		thinking, ok := body["thinking"].(map[string]any)
		if !ok || thinking["type"] != "adaptive" {
			t.Errorf("wrong adaptive profile: %+v", body)
		}
		if body["output_config"].(map[string]any)["effort"] != "low" {
			t.Error("managed effort changed the cached top-level setting")
		}
		messages := body["messages"].([]any)
		last := messages[len(messages)-1].(map[string]any)
		if last["output_config"].(map[string]any)["effort"] != "high" {
			t.Error("effective effort marker missing")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"synthetic-response\",\"model\":\"synthetic-returned-model\",\"usage\":{\"input_tokens\":11,\"cache_read_input_tokens\":3,\"cache_creation_input_tokens\":14,\"cache_creation\":{\"ephemeral_1h_input_tokens\":8}}}}\n\n"+
			"event: content_block_start\ndata: {\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"ok\"}}\n\n"+
			"event: content_block_stop\ndata: {\"index\":0}\n\n"+
			"event: message_delta\ndata: {\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":7}}\n\n"+
			"event: message_stop\ndata: {}\n\n")
	}))
	defer server.Close()
	client := NewClient(server.URL, "synthetic-key", 0, time.Second)
	defer client.CloseIdle()
	resp, err := client.Send(context.Background(), Request{Model: ModelMeta{ID: "synthetic-model", API: "anthropic-messages", Provider: "anthropic", Reasoning: true, MaxTokens: 64000, AdaptiveThinking: true, MidConvoEffort: true}, Thinking: "xhigh", Messages: []Message{{Role: "user", Content: []Block{{Type: "text", Text: "synthetic"}}}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Model != "synthetic-returned-model" || resp.ResponseID != "synthetic-response" || resp.ThinkingLevel != "high" || resp.Usage.TotalTokens != 35 || resp.Usage.CacheWrite1h == nil || *resp.Usage.CacheWrite1h != 8 {
		t.Fatalf("returned metadata: %+v", resp)
	}
}
