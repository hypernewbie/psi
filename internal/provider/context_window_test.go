package provider

import (
	"testing"
)

// outputLimit reads the output token limit from a built request body.
func outputLimit(body map[string]any, api string) int {
	switch api {
	case "openai-completions":
		if limit, ok := body["max_tokens"].(int); ok {
			return limit
		}
		limit, _ := body["max_completion_tokens"].(int)
		return limit
	case "openai-responses":
		limit, _ := body["max_output_tokens"].(int)
		return limit
	case "google-generative-ai":
		config, _ := body["generationConfig"].(map[string]any)
		limit, _ := config["maxOutputTokens"].(int)
		return limit
	case "mistral-conversations":
		limit, _ := body["maxTokens"].(int)
		return limit
	default:
		limit, _ := body["max_tokens"].(int)
		return limit
	}
}

// A model whose output cap equals its context window would ask for more
// tokens than the endpoint can return, and the endpoint rejects that request.
func TestOutputTokensStayInsideTheContextWindow(t *testing.T) {
	model := ModelMeta{ID: "kimi-k3", API: "openai-completions", Provider: "opencode-go",
		MaxTokens: 1048576, ContextWindow: 1048576, Reasoning: true}
	req := Request{Model: model, System: "You are psi.",
		Messages: []Message{{Role: "user", Content: []Block{{Type: "text", Text: "hello"}}}}}
	body, err := BuildRequest(req)
	if err != nil {
		t.Fatal(err)
	}
	limit := outputLimit(body, model.API)
	if limit <= 0 {
		t.Fatalf("output limit = %d, want a positive limit", limit)
	}
	if limit >= model.ContextWindow {
		t.Fatalf("max_tokens = %d, want less than the context window %d", limit, model.ContextWindow)
	}
	if limit <= 0 {
		t.Fatalf("max_tokens = %d, want a positive limit", limit)
	}
}

// A larger prompt leaves less room for the answer.
func TestOutputTokensShrinkWithThePrompt(t *testing.T) {
	model := ModelMeta{ID: "kimi-k3", API: "openai-completions", Provider: "opencode-go",
		MaxTokens: 1048576, ContextWindow: 1048576, Reasoning: true}
	short := Request{Model: model, System: "You are psi.",
		Messages: []Message{{Role: "user", Content: []Block{{Type: "text", Text: "hello"}}}}}
	long := Request{Model: model, System: "You are psi.",
		Messages: []Message{{Role: "user", Content: []Block{{Type: "text", Text: string(make([]byte, 400000))}}}}}
	shortBody, err := BuildRequest(short)
	if err != nil {
		t.Fatal(err)
	}
	longBody, err := BuildRequest(long)
	if err != nil {
		t.Fatal(err)
	}
	shortLimit := outputLimit(shortBody, model.API)
	longLimit := outputLimit(longBody, model.API)
	if longLimit >= shortLimit {
		t.Fatalf("long prompt limit = %d, want less than the short prompt limit %d", longLimit, shortLimit)
	}
}

// A model with room to spare keeps its requested output limit.
func TestOutputTokensKeepTheRequestedLimitWhenTheWindowAllowsIt(t *testing.T) {
	model := ModelMeta{ID: "deepseek-v4-pro", API: "openai-completions", Provider: "opencode-go",
		MaxTokens: 384000, ContextWindow: 1000000, Reasoning: true}
	body, err := BuildRequest(Request{Model: model, System: "You are psi.",
		Messages: []Message{{Role: "user", Content: []Block{{Type: "text", Text: "hello"}}}}})
	if err != nil {
		t.Fatal(err)
	}
	if limit := outputLimit(body, model.API); limit != 384000 {
		t.Fatalf("max_tokens = %d, want the model limit 384000", limit)
	}
}

// Every adapter applies the cap.
func TestEveryAdapterAppliesTheContextCap(t *testing.T) {
	for _, test := range []struct{ api, provider string }{
		{"openai-completions", "opencode-go"},
		{"openai-responses", "opencode-go"},
		{"google-generative-ai", "opencode-go"},
		{"mistral-conversations", "mistral"},
	} {
		api, provider := test.api, test.provider
		model := ModelMeta{ID: "synthetic", API: api, Provider: provider, MaxTokens: 262144, ContextWindow: 262144, Reasoning: true}
		body, err := BuildRequest(Request{Model: model, System: "You are psi.",
			Messages: []Message{{Role: "user", Content: []Block{{Type: "text", Text: "hello"}}}}})
		if err != nil {
			t.Fatalf("%s: %v", api, err)
		}
		limit := outputLimit(body, api)
		if limit <= 0 {
			t.Fatalf("%s limit = %d, want a positive limit", api, limit)
		}
		if limit >= model.ContextWindow {
			t.Fatalf("%s limit = %d, want less than the context window %d", api, limit, model.ContextWindow)
		}
	}
}

// The Anthropic adapter keeps the thinking budget inside the output limit.
func TestAnthropicOutputTokensKeepTheThinkingBudget(t *testing.T) {
	model := ModelMeta{ID: "qwen3.8-flash", API: "anthropic-messages", Provider: "opencode-go",
		MaxTokens: 16384, ContextWindow: 16384, Reasoning: true}
	body, err := BuildRequest(Request{Model: model, Thinking: "high", System: "You are psi.",
		Messages: []Message{{Role: "user", Content: []Block{{Type: "text", Text: "hello"}}}}})
	if err != nil {
		t.Fatal(err)
	}
	limit := outputLimit(body, model.API)
	budget, _ := body["thinking"].(map[string]any)["budget_tokens"].(int)
	if budget <= 0 || budget >= limit {
		t.Fatalf("thinking budget %d does not fit the output limit %d", budget, limit)
	}
}
