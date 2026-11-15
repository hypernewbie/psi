package provider

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// betaTestStream is a complete Anthropic event stream.
const betaTestStream = "event: message_start\ndata: {\"message\":{\"id\":\"synthetic\",\"model\":\"synthetic-model\",\"usage\":{\"input_tokens\":1,\"output_tokens\":0}}}\n\n" +
	"event: content_block_start\ndata: {\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n" +
	"event: content_block_delta\ndata: {\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"synthetic answer\"}}\n\n" +
	"event: content_block_stop\ndata: {\"index\":0}\n\n" +
	"event: message_delta\ndata: {\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"input_tokens\":1,\"output_tokens\":1}}\n\n" +
	"event: message_stop\ndata: {}\n\n"

// The endpoint rejects a request body that uses a beta feature without
// declaring it in the anthropic-beta header.
func TestAnthropicBetaHeaderDeclaresTheFeaturesTheBodyUses(t *testing.T) {
	var mu sync.Mutex
	var betas []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		betas = append(betas, r.Header.Get("anthropic-beta"))
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, betaTestStream)
	}))
	defer server.Close()

	adaptive := "high"
	client := NewClient(server.URL+"/anthropic", "synthetic-api-key", 0, time.Second)
	for _, test := range []struct {
		name  string
		model ModelMeta
		want  string
	}{
		{
			name:  "budget thinking replays thinking blocks",
			model: ModelMeta{ID: "qwen3.8-flash", API: "anthropic-messages", Provider: "opencode-go", MaxTokens: 16384, Reasoning: true},
			want:  anthropicBetaInterleavedThinking,
		},
		{
			name: "managed effort sends the output-config features",
			model: ModelMeta{ID: "claude-haiku-5-5", API: "anthropic-messages", Provider: "opencode-go", MaxTokens: 16384, Reasoning: true,
				AdaptiveThinking: true, MidConvoEffort: true, ThinkingLevelMap: map[string]*string{"high": &adaptive}},
			want: anthropicBetaMidConversationOutput + "," + anthropicBetaThinkingBindingControls,
		},
	} {
		if _, err := client.Send(context.Background(), Request{Model: test.model, Thinking: "high"}, nil); err != nil {
			t.Fatalf("%s request failed: %v", test.name, err)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if len(betas) != 2 {
		t.Fatalf("beta header count = %d, want 2", len(betas))
	}
	if betas[0] != anthropicBetaInterleavedThinking {
		t.Fatalf("budget thinking beta = %q, want %q", betas[0], anthropicBetaInterleavedThinking)
	}
	wantManaged := anthropicBetaMidConversationOutput + "," + anthropicBetaThinkingBindingControls
	if betas[1] != wantManaged {
		t.Fatalf("managed effort beta = %q, want %q", betas[1], wantManaged)
	}
}

// A configured header replaces the computed set, and an empty value disables it.
func TestAnthropicConfiguredBetaHeaderReplacesTheComputedSet(t *testing.T) {
	var mu sync.Mutex
	var betas []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		betas = append(betas, r.Header.Get("anthropic-beta"))
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, betaTestStream)
	}))
	defer server.Close()

	model := ModelMeta{ID: "qwen3.8-flash", API: "anthropic-messages", Provider: "opencode-go", MaxTokens: 16384, Reasoning: true}
	client := NewClient(server.URL+"/anthropic", "synthetic-api-key", 0, time.Second)
	client.Headers = map[string]string{"anthropic-beta": "synthetic-feature"}
	if _, err := client.Send(context.Background(), Request{Model: model, Thinking: "high"}, nil); err != nil {
		t.Fatal(err)
	}
	client.Headers = map[string]string{"anthropic-beta": ""}
	if _, err := client.Send(context.Background(), Request{Model: model, Thinking: "high"}, nil); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(betas) != 2 {
		t.Fatalf("beta header count = %d, want 2", len(betas))
	}
	if betas[0] != "synthetic-feature" {
		t.Fatalf("configured beta = %q, want the configured value", betas[0])
	}
	if betas[1] != "" {
		t.Fatalf("disabled beta = %q, want an empty header", betas[1])
	}
}

// A request without tools and without thinking needs no beta feature.
func TestAnthropicBetaHeaderStaysEmptyWithoutAFeature(t *testing.T) {
	var mu sync.Mutex
	var present []bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		_, present = present, append(present, r.Header.Get("anthropic-beta") != "")
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, betaTestStream)
	}))
	defer server.Close()

	client := NewClient(server.URL+"/anthropic", "synthetic-api-key", 0, time.Second)
	model := ModelMeta{ID: "qwen3.8-flash", API: "anthropic-messages", Provider: "opencode-go", MaxTokens: 16384}
	if _, err := client.Send(context.Background(), Request{Model: model, Thinking: "off"}, nil); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(present) != 1 || present[0] {
		t.Fatalf("beta header present = %v, want no header", present)
	}
}

// The declared features describe the request body exactly.
func TestAnthropicBetaFeaturesMatchTheRequestBody(t *testing.T) {
	adaptive := "high"
	adaptiveModel := ModelMeta{ID: "claude-haiku-5-5", API: "anthropic-messages", Provider: "opencode-go", MaxTokens: 16384, Reasoning: true,
		AdaptiveThinking: true, MidConvoEffort: true, ThinkingLevelMap: map[string]*string{"high": &adaptive}}
	budgetModel := ModelMeta{ID: "qwen3.8-flash", API: "anthropic-messages", Provider: "opencode-go", MaxTokens: 16384, Reasoning: true}

	adaptiveFeatures := anthropicBetaFeatures(adaptiveModel, Request{Model: adaptiveModel, Thinking: "high",
		Tools: []ToolDef{{Name: "read"}}}, nil)
	if !containsFeature(adaptiveFeatures, anthropicBetaMidConversationOutput) || !containsFeature(adaptiveFeatures, anthropicBetaThinkingBindingControls) {
		t.Fatalf("managed effort features = %v", adaptiveFeatures)
	}
	if containsFeature(adaptiveFeatures, anthropicBetaInterleavedThinking) {
		t.Fatalf("adaptive thinking must not declare the interleaved feature: %v", adaptiveFeatures)
	}
	body, err := BuildRequest(Request{Model: adaptiveModel, Thinking: "high", Tools: []ToolDef{{Name: "read"}}})
	if err != nil {
		t.Fatal(err)
	}
	if body["output_config"] == nil {
		t.Fatal("managed effort body has no output config")
	}
	budgetFeatures := anthropicBetaFeatures(budgetModel, Request{Model: budgetModel, Thinking: "high"}, nil)
	if !containsFeature(budgetFeatures, anthropicBetaInterleavedThinking) {
		t.Fatalf("budget thinking features = %v", budgetFeatures)
	}
	if containsFeature(budgetFeatures, anthropicBetaMidConversationOutput) {
		t.Fatalf("budget thinking must not declare the output-config feature: %v", budgetFeatures)
	}
}

// The interleaved feature needs a thinking level that is not off.
func TestAnthropicBetaFeaturesNeedAnEnabledThinkingLevel(t *testing.T) {
	model := ModelMeta{ID: "qwen3.8-flash", API: "anthropic-messages", Provider: "opencode-go", MaxTokens: 16384, Reasoning: true}
	if features := anthropicBetaFeatures(model, Request{Model: model, Thinking: "off"}, nil); containsFeature(features, anthropicBetaInterleavedThinking) {
		t.Fatalf("disabled thinking features = %v", features)
	}
}

// containsFeature reports whether the list holds the feature.
func containsFeature(features []string, feature string) bool {
	for _, candidate := range features {
		if candidate == feature {
			return true
		}
	}
	return false
}

// The beta header is sent for the OpenCode Go Anthropic endpoint.
func TestAnthropicBetaHeaderReachesTheOpenCodeGoEndpoint(t *testing.T) {
	var mu sync.Mutex
	var header string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		header = r.Header.Get("anthropic-beta")
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, betaTestStream)
	}))
	defer server.Close()

	client := NewClient(server.URL+"/anthropic", "synthetic-api-key", 0, time.Second)
	model := ModelMeta{ID: "minimax-m3", API: "anthropic-messages", Provider: "opencode-go", MaxTokens: 16384, Reasoning: true}
	if _, err := client.Send(context.Background(), Request{Model: model, Thinking: "high"}, nil); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if !strings.Contains(header, anthropicBetaInterleavedThinking) {
		t.Fatalf("anthropic-beta = %q, want the interleaved feature", header)
	}
}
