package provider

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"psi/internal/modelmeta"
)

// A provider that models.json declares uses the generic protocol adapters.
func TestConfiguredProviderBuildsGenericRequests(t *testing.T) {
	model := ModelMeta{ID: "m", API: "openai-completions", Provider: "local", BaseURL: "https://provider.example/v1", Custom: true, Reasoning: true, MaxTokens: 1000, Compat: modelmeta.Compat{"maxTokensField": json.RawMessage(`"max_tokens"`)}}
	request, err := BuildRequest(Request{Model: model, Thinking: "off", System: "sys", Messages: []Message{{Role: "user", Content: []Block{{Type: "text", Text: "hi"}}}}})
	if err != nil {
		t.Fatal(err)
	}
	if request["max_tokens"] != 1000 {
		t.Fatalf("request did not use the declared compatibility: %#v", request)
	}
	anthropic := ModelMeta{ID: "m", API: "anthropic-messages", Provider: "local", BaseURL: "https://provider.example", Custom: true, MaxTokens: 1000}
	if _, err := BuildRequest(Request{Model: anthropic, Thinking: "off", Messages: []Message{{Role: "user", Content: []Block{{Type: "text", Text: "hi"}}}}}); err != nil {
		t.Fatalf("a configured anthropic-messages provider was rejected: %v", err)
	}
	responses := ModelMeta{ID: "m", API: "openai-responses", Provider: "local", BaseURL: "https://provider.example/v1", Custom: true, MaxTokens: 1000}
	if _, err := BuildRequest(Request{Model: responses, Thinking: "off", Messages: []Message{{Role: "user", Content: []Block{{Type: "text", Text: "hi"}}}}}); err != nil {
		t.Fatalf("a configured openai-responses provider was rejected: %v", err)
	}
	codex := ModelMeta{ID: "m", API: "openai-codex-responses", Provider: "local", BaseURL: "https://provider.example", Custom: true, MaxTokens: 1000}
	if _, err := BuildRequest(Request{Model: codex, Thinking: "off"}); err == nil {
		t.Fatal("the Codex adapter was accepted for a configured provider")
	}
}

// An Anthropic-compatible provider that needs a feature the adapter cannot
// replay stays unavailable instead of running without it.
func TestConfiguredAnthropicProviderRejectsUnreplayableCompatibility(t *testing.T) {
	model := ModelMeta{ID: "m", API: "anthropic-messages", Provider: "local", Custom: true, MaxTokens: 1000,
		Compat: modelmeta.Compat{"supportsMidConvoSystemMessages": json.RawMessage("true")}}
	if _, err := BuildRequest(Request{Model: model, Thinking: "off"}); err == nil {
		t.Fatal("an unreplayable Anthropic feature was accepted")
	}
	adaptive := ModelMeta{ID: "m", API: "anthropic-messages", Provider: "local", Custom: true, MaxTokens: 1000,
		Compat: modelmeta.Compat{"forceAdaptiveThinking": json.RawMessage("true")}}
	if _, err := BuildRequest(Request{Model: adaptive, Thinking: "off"}); err != nil {
		t.Fatalf("a supported Anthropic feature was rejected: %v", err)
	}
}

// A configured provider resolves its key from auth.json or from models.json
// and has no environment variable of its own.
func TestConfiguredProviderCredentialPrecedence(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "auth.json")
	env := map[string]string{"LOCAL_API_KEY": "env-local"}
	if key, source, err := SelectProviderAPIKey(env, path, "local", "$LOCAL_API_KEY"); err != nil || key != "env-local" || source != "models.json:local" {
		t.Fatalf("configured key = %q %q %v", key, source, err)
	}
	if err := os.WriteFile(path, []byte(`{"local":{"type":"api_key","key":"stored-local"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	if key, source, err := SelectProviderAPIKey(env, path, "local", "$LOCAL_API_KEY"); err != nil || key != "stored-local" || source != "auth.json:local" {
		t.Fatalf("stored key = %q %q %v", key, source, err)
	}
	if key, _, err := SelectProviderAPIKey(env, path, "local", ""); err != nil || key != "stored-local" {
		t.Fatalf("stored key without a configured key = %q %v", key, err)
	}
	if err := os.WriteFile(path, []byte(`{"local":{"type":"oauth","access":"token"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	if key, _, err := SelectProviderAPIKey(env, path, "local", "configured-local"); err == nil || key != "" {
		t.Fatalf("an unsupported stored credential fell back to %q (%v)", key, err)
	}
	if err := os.WriteFile(path, []byte(`{}`), 0600); err != nil {
		t.Fatal(err)
	}
	if key, _, err := SelectProviderAPIKey(env, path, "local", ""); err == nil || key != "" {
		t.Fatalf("a configured provider without a key resolved to %q (%v)", key, err)
	}
}

// The credential list covers the configured providers in the same read.
func TestCredentialsCoverConfiguredProviders(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "auth.json")
	if err := os.WriteFile(path, []byte(`{"local":{"type":"api_key","key":"stored-local"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	ResetAuthReads()
	states := Credentials(map[string]string{}, path, map[string]string{}, []string{"local"})
	if reads := AuthReads(); reads != 1 {
		t.Fatalf("credential resolution read auth.json %d times", reads)
	}
	state := states["local"]
	if !state.Usable || state.Source != "auth.json:local" {
		t.Fatalf("configured provider credential = %+v", state)
	}
}

// A configured provider sends its configured headers and the bearer header
// that authHeader asks for.
func TestConfiguredProviderSendsAuthHeaderAndCustomHeaders(t *testing.T) {
	var seen http.Header
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r.Header.Clone()
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	}))
	defer server.Close()
	client := NewCompletionsClient(server.URL, "stored-key", 0, 0)
	client.Headers = map[string]string{"X-Deployment": "synthetic", "Authorization": "Bearer stored-key"}
	model := ModelMeta{ID: "m", API: "openai-completions", Provider: "local", Custom: true, MaxTokens: 100}
	response, err := client.Send(context.Background(), Request{Model: model, Thinking: "off", Messages: []Message{{Role: "user", Content: []Block{{Type: "text", Text: "hi"}}}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if response.Text != "ok" {
		t.Fatalf("response = %+v", response)
	}
	if seen.Get("X-Deployment") != "synthetic" || seen.Get("Authorization") != "Bearer stored-key" {
		t.Fatalf("request headers = %#v", seen)
	}
	if strings.Contains(seen.Get("Authorization"), "stored-key") == false {
		t.Fatalf("authorization header = %q", seen.Get("Authorization"))
	}
}
