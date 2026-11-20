package app

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hypernewbie/psi/internal/config"
	"github.com/hypernewbie/psi/internal/provider"
)

func openAISyntheticEvent(event string, payload map[string]any) string {
	payload["type"] = event
	encoded, _ := json.Marshal(payload)
	return "event: " + event + "\ndata: " + string(encoded) + "\n\n"
}

func openAISyntheticStream(text string) string {
	return openAISyntheticEvent("response.output_item.done", map[string]any{"output_index": 0, "item": map[string]any{"type": "message", "id": "msg_openai", "role": "assistant", "content": []any{map[string]any{"type": "output_text", "text": text}}}}) +
		openAISyntheticEvent("response.completed", map[string]any{"response": map[string]any{"id": "resp_openai", "status": "completed", "output": []any{}}})
}

func TestOpenAIAppSubscriptionTextAndRefresh(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "")
	r := testRuntime(t)
	models, err := config.LoadModels(filepath.Join(t.TempDir(), "models.json"))
	if err != nil {
		t.Fatal(err)
	}
	r.models = models
	r.settings.MaxRetries = 0
	authPath := filepath.Join(r.agentDir, "auth.json")
	fresh := time.Now().Add(2 * time.Hour).UnixMilli()
	writeAppFile(t, authPath, `{"openai":{"type":"oauth","access":"synthetic-fresh-access","refresh":"synthetic-refresh","expires":`+strconv.FormatInt(fresh, 10)+`,"clientId":"synthetic-client","scopes":["openid","chatgpt.tokens.use.direct"]}}`)
	if err := r.configure(config.StartupOverrides{Provider: "openai", Model: "gpt-6.1-sol", Thinking: "off", ThinkingExplicit: true}); err != nil {
		t.Fatal(err)
	}
	if r.authErr != nil {
		t.Fatalf("openai auth: %v", r.authErr)
	}
	responses, ok := r.prov.(*provider.ResponsesClient)
	if !ok {
		t.Fatal("openai did not configure a Responses client")
	}
	if responses.BaseURL != "https://api.openai.com/v1" {
		t.Fatalf("openai base URL = %q", responses.BaseURL)
	}
	var exchanges, posts atomic.Int32
	var bearers []string
	var bodies []map[string]any
	var sessions, requestIDs []string
	responses.HTTP.Transport = roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		switch req.URL.String() {
		case "https://auth.openai.com/api/accounts/oauth/token":
			exchanges.Add(1)
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"access_token":"synthetic-rotated-access","refresh_token":"synthetic-rotated-refresh","scope":"openid chatgpt.tokens.use.direct","expires_in":3600}`)), Header: make(http.Header)}, nil
		case "https://api.openai.com/v1/responses":
			posts.Add(1)
			bearers = append(bearers, req.Header.Get("Authorization"))
			sessions = append(sessions, req.Header.Get("session_id"))
			requestIDs = append(requestIDs, req.Header.Get("x-client-request-id"))
			data, _ := io.ReadAll(req.Body)
			var decoded map[string]any
			_ = json.Unmarshal(data, &decoded)
			bodies = append(bodies, decoded)
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(openAISyntheticStream("synthetic-openai-response-text"))), Header: make(http.Header)}, nil
		default:
			return nil, &provider.ProviderError{Kind: provider.ErrInvalid, Message: "unexpected test URL"}
		}
	})
	sessionID := r.store.Header().ID
	// First request uses the fresh stored token without an exchange.
	resp, err := r.prov.Send(context.Background(), provider.Request{Model: r.cfg.Model, Thinking: "off", Messages: []provider.Message{{Role: "user", Content: []provider.Block{{Type: "text", Text: "hi"}}}}}, nil)
	if err != nil {
		t.Fatalf("first send: %v", err)
	}
	if resp.Text != "synthetic-openai-response-text" || resp.RawStopReason != "completed" {
		t.Fatalf("first response = %+v", resp)
	}
	if exchanges.Load() != 0 || posts.Load() != 1 {
		t.Fatalf("first request exchanges=%d posts=%d", exchanges.Load(), posts.Load())
	}
	if bearers[0] != "Bearer synthetic-fresh-access" {
		t.Fatalf("first bearer = %q", bearers[0])
	}
	if sessions[0] != sessionID || requestIDs[0] != sessionID {
		t.Fatalf("session headers = %q %q, want %q", sessions[0], requestIDs[0], sessionID)
	}
	if bodies[0]["store"] != false {
		t.Fatalf("store = %v", bodies[0]["store"])
	}
	if _, hasLimit := bodies[0]["max_output_tokens"]; hasLimit {
		t.Fatal("subscription request kept max_output_tokens")
	}
	// Expire the synthetic credential between requests to exercise refresh.
	writeAppFile(t, authPath, `{"openai":{"type":"oauth","access":"synthetic-fresh-access","refresh":"synthetic-refresh","expires":1,"clientId":"synthetic-client","scopes":["openid","chatgpt.tokens.use.direct"]}}`)
	resp, err = r.prov.Send(context.Background(), provider.Request{Model: r.cfg.Model, Thinking: "off", Messages: []provider.Message{{Role: "user", Content: []provider.Block{{Type: "text", Text: "hi again"}}}}}, nil)
	if err != nil {
		t.Fatalf("second send: %v", err)
	}
	if resp.Text != "synthetic-openai-response-text" {
		t.Fatalf("second response text = %q", resp.Text)
	}
	if exchanges.Load() != 1 || posts.Load() != 2 {
		t.Fatalf("second request exchanges=%d posts=%d", exchanges.Load(), posts.Load())
	}
	if bearers[1] != "Bearer synthetic-rotated-access" {
		t.Fatalf("second bearer = %q", bearers[1])
	}
	if _, hasLimit := bodies[1]["max_output_tokens"]; hasLimit {
		t.Fatal("refreshed subscription request kept max_output_tokens")
	}
	data, _ := os.ReadFile(authPath)
	var all map[string]json.RawMessage
	if json.Unmarshal(data, &all) != nil {
		t.Fatal("auth.json was corrupted")
	}
	var updated struct {
		Access   string `json:"access"`
		Refresh  string `json:"refresh"`
		ClientID string `json:"clientId"`
	}
	if json.Unmarshal(all["openai"], &updated) != nil {
		t.Fatal("openai entry was corrupted")
	}
	if updated.Access != "synthetic-rotated-access" || updated.Refresh != "synthetic-rotated-refresh" || updated.ClientID != "synthetic-client" {
		t.Fatalf("persisted rotation = %+v", updated)
	}
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestOpenAIHelpDescribesLoginFlow(t *testing.T) {
	help := helpText("test")
	if !strings.Contains(help, "/login openai") {
		t.Fatalf("help text lacks the OpenAI sign-in command: %q", help)
	}
	for _, want := range []string{"openai-codex", "primary ChatGPT quota"} {
		if !strings.Contains(help, want) {
			t.Fatalf("help text lacks %q: %q", want, help)
		}
	}
	for _, stale := range []string{"existing Pi login", "no login UI", "sign in through Pi", "Using ChatGPT plan", "Manage usage"} {
		if strings.Contains(help, stale) {
			t.Fatalf("help text keeps a stale login claim %q: %q", stale, help)
		}
	}
}
