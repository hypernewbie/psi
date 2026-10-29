//go:build liveprovider

package provider

import (
	"context"
	"os"
	"testing"
	"time"

	"psi/internal/config"
	"psi/internal/providerregistry"
)

// PLAN6 live smoke: one bounded text prompt using an explicitly selected
// provider/model. Skipped without PSI_LIVE_PROVIDER=1 and a provider opt-in.
func TestLiveConfiguredProviderText(t *testing.T) {
	if os.Getenv("PSI_LIVE_PROVIDER") != "1" {
		t.Skip("live provider opt-in only (PSI_LIVE_PROVIDER=1)")
	}
	family, api, modelID := os.Getenv("PSI_LIVE_PROVIDER_ID"), os.Getenv("PSI_LIVE_API"), os.Getenv("PSI_LIVE_MODEL")
	if family == "" || api == "" || modelID == "" {
		t.Fatal("live opt-in requires PSI_LIVE_PROVIDER_ID, PSI_LIVE_API, and PSI_LIVE_MODEL")
	}
	if family != "minimax" && family != "opencode-go" && family != "deepseek" && family != "zai" && family != "google" {
		t.Fatalf("provider %q is outside the PLAN6 live-test scope", family)
	}
	spec, ok := providerregistry.Lookup(family)
	if !ok || !providerregistry.SupportsAPI(family, api) {
		t.Fatalf("unsupported live provider/API pair %q/%q", family, api)
	}
	key := os.Getenv(spec.Environment)
	if key == "" {
		t.Fatalf("live opt-in requires the selected provider's %s", spec.Environment)
	}
	var catalogModel config.ModelMeta
	for _, candidate := range config.BundledModels(family) {
		if candidate.ID == modelID && candidate.API == api {
			catalogModel = candidate
			break
		}
	}
	if catalogModel.ID == "" {
		t.Fatalf("live model %q/%q must match a model in the bundled public catalog", family, modelID)
	}
	baseURL := os.Getenv("PSI_LIVE_BASE_URL")
	if baseURL == "" && family == "opencode-go" {
		t.Fatal("OpenCode Go live tests require model-specific PSI_LIVE_BASE_URL")
	}
	if baseURL == "" {
		baseURL = catalogModel.BaseURL
	}
	if baseURL == "" {
		baseURL = providerregistry.DefaultEndpoint(family, api)
	}
	forceAdaptive, _, _ := catalogModel.Compat.Bool("forceAdaptiveThinking")
	midConvoEffort, _, _ := catalogModel.Compat.Bool("supportsMidConvoEffort")
	allowEmptySignature, _, _ := catalogModel.Compat.Bool("allowEmptySignature")
	model := ModelMeta{ID: catalogModel.ID, API: catalogModel.API, Provider: family, BaseURL: baseURL, MaxTokens: catalogModel.MaxTokens, ContextWindow: catalogModel.ContextWindow, Reasoning: catalogModel.Reasoning, InputImage: catalogModel.InputImage, ThinkingLevelMap: catalogModel.ThinkingLevelMap, AdaptiveThinking: forceAdaptive, MidConvoEffort: midConvoEffort, AllowEmptySignature: allowEmptySignature, Compat: catalogModel.Compat}
	maxTokens := model.MaxTokens
	if maxTokens <= 0 || maxTokens > 64 {
		maxTokens = 64
	}
	request := Request{Model: model, Thinking: "off", MaxTokens: maxTokens, Messages: []Message{{Role: "user", Content: []Block{{Type: "text", Text: "Reply with exactly: PSI_LIVE_OK"}}}}}
	var client Provider
	switch api {
	case "anthropic-messages":
		client = NewClient(baseURL, key, 0, 30*time.Second)
	case "openai-completions":
		client = NewCompletionsClient(baseURL, key, 0, 30*time.Second)
	case "google-generative-ai":
		client = NewGoogleClient(baseURL, key, 0, 30*time.Second)
	case "openai-responses":
		client = NewResponsesClient(baseURL, key, 0, 30*time.Second)
	default:
		t.Fatalf("no live client for API %q", api)
	}
	defer client.CloseIdle()
	if family == "opencode-go" {
		switch c := client.(type) {
		case *Client:
			c.SessionID = "psi-live-provider-smoke"
		case *CompletionsClient:
			c.SessionID = "psi-live-provider-smoke"
		case *GoogleClient:
			c.SessionID = "psi-live-provider-smoke"
		case *ResponsesClient:
			c.SessionID = "psi-live-provider-smoke"
		default:
			t.Fatalf("unexpected OpenCode Go client type %T", client)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	response, err := client.Send(ctx, request, nil)
	if err != nil {
		t.Fatalf("live %s/%s text request: %v", family, modelID, err)
	}
	if response.Text == "" {
		t.Fatalf("live %s/%s returned no text", family, modelID)
	}
}

func TestLiveAnthropic(t *testing.T) {
	if os.Getenv("PSI_LIVE_ANTHROPIC") != "1" {
		t.Skip("Anthropic live opt-in only (PSI_LIVE_ANTHROPIC=1)")
	}
	key := os.Getenv("ANTHROPIC_API_KEY")
	if key == "" {
		t.Skip("ANTHROPIC_API_KEY not set")
	}
	c := NewClient("", key, 1, 30*time.Second)
	defer c.CloseIdle()
	// Small text prompt.
	req := Request{Model: ModelMeta{ID: "claude-sonnet-4-5", API: "anthropic-messages", MaxTokens: 64, Reasoning: false}, Messages: []Message{{Role: "user", Content: []Block{{Type: "text", Text: "Say hi in one word."}}}}}
	resp, err := c.Send(context.Background(), req, nil)
	if err != nil {
		t.Fatalf("live text: %v", err)
	}
	if resp.Text == "" {
		t.Errorf("empty live response")
	}
	// Cancel one request.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.Send(ctx, req, nil); err == nil {
		t.Errorf("cancelled request should fail")
	}
}
