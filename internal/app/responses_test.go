package app

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"psi/internal/config"
	"psi/internal/modelmeta"
	"psi/internal/provider"
)

func TestConfigureDispatchesOpenCodeResponsesWithProviderKey(t *testing.T) {
	var served atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		served.Store(true)
		if r.URL.Path != "/inference/go/openai/v1/responses" || r.Header.Get("Authorization") != "Bearer synthetic-opencode-responses-key" ||
			r.Header.Get("X-OpenCode-Session") == "" || r.Header.Get("X-Client-Request-Id") != r.Header.Get("X-OpenCode-Session") {
			t.Errorf("OpenCode Responses request path/auth/session = %s %#v", r.URL.Path, r.Header)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"configured-response\",\"status\":\"completed\",\"output\":[]}}\n\n")
	}))
	defer server.Close()
	t.Setenv("OPENCODE_API_KEY", "synthetic-opencode-responses-key")
	r := testRuntime(t)
	r.models = &config.ModelsFile{Providers: map[string]config.ProviderMeta{"opencode-go": {Models: []config.ModelMeta{{
		ID: "synthetic-responses", API: "openai-responses", BaseURL: server.URL + "/inference/go/openai/v1", Reasoning: true,
		ThinkingLevelMap: map[string]*string{"off": nil},
		Compat:           modelmeta.Compat{"sessionAffinityFormat": json.RawMessage(`"openai-nosession"`), "supportsMidConvoSystemMessages": json.RawMessage(`true`), "supportsAdditionalTools": json.RawMessage(`true`)},
	}}}}}
	if err := r.configure(config.StartupOverrides{Provider: "opencode-go", Model: "synthetic-responses", Thinking: "off"}); err != nil {
		t.Fatal(err)
	}
	client, ok := r.prov.(*provider.ResponsesClient)
	if !ok || client.APIKey != "synthetic-opencode-responses-key" || client.SessionID != r.store.Header().ID || r.authErr != nil || r.cfg.Model.API != "openai-responses" {
		t.Fatal("configuration did not select generic Responses with OpenCode's provider credential")
	}
	response, err := client.Send(context.Background(), provider.Request{Model: r.cfg.Model, Thinking: "off", Messages: []provider.Message{{Role: "user", Content: []provider.Block{{Type: "text", Text: "synthetic"}}}}}, nil)
	if err != nil || response.ResponseID != "configured-response" || !served.Load() {
		t.Fatalf("configured Responses request = %+v, %v (served %v)", response, err, served.Load())
	}
	client.CloseIdle()
}
