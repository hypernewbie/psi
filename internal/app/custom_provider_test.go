package app

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hypernewbie/psi/internal/agent"
	"github.com/hypernewbie/psi/internal/config"
	"github.com/hypernewbie/psi/internal/provider"
	"github.com/hypernewbie/psi/internal/session"
)

// A command line reference to a provider from models.json resolves after the
// file loads.
func TestConfiguredProviderResolvesCommandLineReferences(t *testing.T) {
	models := &config.ModelsFile{Providers: map[string]config.ProviderMeta{"local-openai": {
		API: "openai-completions", BaseURL: "https://provider.example/v1",
		Models: []config.ModelMeta{{ID: "qwen2.5-coder:7b"}},
	}}}
	resolved, err := resolveConfiguredModelOption(Options{Model: "local-openai/qwen2.5-coder:7b"}, models)
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Provider != "local-openai" || resolved.Model != "qwen2.5-coder:7b" {
		t.Fatalf("qualified reference = %+v", resolved)
	}
	resolved, err = resolveConfiguredModelOption(Options{Provider: "local-openai", Model: "qwen2.5-coder:7b"}, models)
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Provider != "local-openai" || resolved.Model != "qwen2.5-coder:7b" {
		t.Fatalf("provider and model = %+v", resolved)
	}
	resolved, err = resolveConfiguredModelOption(Options{Model: "local-openai/meta-llama/Llama-3"}, models)
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Provider != "local-openai" || resolved.Model != "meta-llama/Llama-3" {
		t.Fatalf("model with a slash = %+v", resolved)
	}
	if _, err := resolveConfiguredModelOption(Options{Provider: "anthropic", Model: "local-openai/m"}, models); err == nil {
		t.Fatal("a conflicting provider and model were accepted")
	}
	if _, err := resolveConfiguredModelOption(Options{Provider: "not-a-provider"}, models); err == nil {
		t.Fatal("an unknown provider was accepted")
	}
}

// A provider from models.json appears in the model list and in the picker
// when it has a credential.
func TestConfiguredProviderAppearsInModelListAndPicker(t *testing.T) {
	r := testRuntime(t)
	modelsPath := filepath.Join(r.agentDir, "models.json")
	raw := `{"providers":{"local-openai":{"name":"Local","api":"openai-completions","baseUrl":"https://provider.example/v1","apiKey":"synthetic-local-key","models":[{"id":"qwen2.5-coder","contextWindow":32000,"maxTokens":4096}]}}}`
	if err := os.WriteFile(modelsPath, []byte(raw), 0600); err != nil {
		t.Fatal(err)
	}
	loaded, err := config.LoadModels(modelsPath)
	if err != nil {
		t.Fatal(err)
	}
	r.models = loaded

	var output bytes.Buffer
	if err := listAvailableModels(&output, r.agentDir, ""); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "local-openai") || !strings.Contains(output.String(), "qwen2.5-coder") {
		t.Fatalf("model list does not show the configured provider:\n%s", output.String())
	}

	selector := modelSelector(r)
	found := false
	for _, item := range selector.Items {
		if item.ID == "local-openai/qwen2.5-coder" {
			found = true
		}
	}
	if !found {
		t.Fatalf("the picker does not offer the configured model: %+v", selector.Items)
	}
}

// The reload reads models.json again, and a malformed file keeps the current
// model configuration.
func TestReloadReadsModelsJSONAgain(t *testing.T) {
	r := testRuntime(t)
	modelsPath := filepath.Join(r.agentDir, "models.json")
	raw := `{"providers":{"local-openai":{"api":"openai-completions","baseUrl":"https://provider.example/v1","apiKey":"synthetic-local-key","models":[{"id":"qwen2.5-coder"}]}}}`
	if err := os.WriteFile(modelsPath, []byte(raw), 0600); err != nil {
		t.Fatal(err)
	}
	if err := r.reloadModels(); err != nil {
		t.Fatal(err)
	}
	if _, err := r.models.SelectModel("local-openai", "qwen2.5-coder"); err != nil {
		t.Fatalf("the reloaded models.json was not installed: %v", err)
	}
	if err := os.WriteFile(modelsPath, []byte(`{"providers":`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := r.reloadModels(); err == nil {
		t.Fatal("a malformed models.json was accepted")
	}
	if _, err := r.models.SelectModel("local-openai", "qwen2.5-coder"); err != nil {
		t.Fatalf("a malformed models.json replaced the working configuration: %v", err)
	}
}

func TestCompactionProviderUsesReloadedCatalog(t *testing.T) {
	r := testRuntime(t)
	// The active model in this fixture is offline. Only the configured
	// compactor needs a client for this test, and it sends no requests.
	r.authErr = nil
	r.publishLive()
	const compactor = "local-openai/summary"
	writeCatalog := func(maxTokens string) {
		t.Helper()
		raw := `{"providers":{"local-openai":{"api":"openai-completions","baseUrl":"https://provider.example/v1","apiKey":"synthetic-local-key","models":[{"id":"summary","maxTokens":` + maxTokens + `}]}}}`
		if err := os.WriteFile(filepath.Join(r.agentDir, "models.json"), []byte(raw), 0600); err != nil {
			t.Fatal(err)
		}
	}
	writeCatalog("2048")
	if err := r.reloadModels(); err != nil {
		t.Fatal(err)
	}
	writeCatalog("4096")

	type resolution struct {
		model provider.ModelMeta
		err   error
	}
	settings := r.settings
	resolve := func() resolution {
		_, model, cleanup, err := r.compactionProvider(compactor, settings)
		if cleanup != nil {
			cleanup()
		}
		return resolution{model, err}
	}
	start := make(chan struct{})
	resolved := make(chan resolution, 1)
	go func() {
		<-start
		resolved <- resolve()
	}()
	close(start)
	reloadErr := r.reloadModels()
	overlapping := <-resolved
	if reloadErr != nil {
		t.Fatal(reloadErr)
	}
	if overlapping.err != nil || overlapping.model.ID != "summary" || (overlapping.model.MaxTokens != 2048 && overlapping.model.MaxTokens != 4096) {
		t.Fatalf("compactor resolution during reload = %+v, %v; want summary from either catalog", overlapping.model, overlapping.err)
	}
	latest := resolve()
	if latest.err != nil || latest.model.ID != "summary" || latest.model.MaxTokens != 4096 {
		t.Fatalf("compactor resolution after reload = %+v, %v; want summary from the new catalog", latest.model, latest.err)
	}
}

func TestCompactionPinsExplicitActiveModelAcrossSplit(t *testing.T) {
	r := testRuntime(t)
	for _, task := range []struct {
		prompt string
		spans  int
	}{{"older task", 1}, {"current task", 4}} {
		if _, err := r.store.AppendMessage(agentMessageUser(task.prompt)); err != nil {
			t.Fatal(err)
		}
		for range task.spans {
			if _, err := r.store.AppendMessage(session.AgentMessage{Role: "assistant", Assistant: &session.AssistantMessage{
				Role: "assistant", Content: []session.ContentBlock{{Type: "text", Text: &session.TextContent{Type: "text", Text: strings.Repeat("work ", 16_000)}}},
			}}); err != nil {
				t.Fatal(err)
			}
		}
	}
	oldModel := r.cfg.Model
	newModel := oldModel
	newModel.ID = "next"
	old := &provider.Fake{Responses: []*provider.Response{{Text: "history summary", StopReason: "stop"}, {Text: "prefix summary", StopReason: "stop"}}}
	next := &provider.Fake{Responses: []*provider.Response{{Text: "next response", StopReason: "stop"}, {Text: "next response", StopReason: "stop"}}}
	r.prov, r.authErr = old, nil
	r.cfg.KeepRecentTokens = 20_000
	r.publishLive()
	cfg := r.cfg
	cfg.CompactionProvider = func() (provider.Provider, provider.ModelMeta, func(), error) {
		return r.compactionProvider("anthropic/m", r.settings)
	}
	cfg.LiveSettings = func() agent.LiveSettings {
		if old.CallCount() != 0 {
			return agent.LiveSettings{Provider: next, Model: newModel, Thinking: "off", KeepRecentTokens: 20_000}
		}
		return r.liveSettings()
	}
	if err := r.newLoop(nil).CompactAndResume(t.Context(), cfg, ""); err != nil {
		t.Fatal(err)
	}
	summaries, continuation := old.Requests(), next.Requests()
	if len(summaries) != 2 || summaries[0].Model.ID != oldModel.ID || summaries[1].Model.ID != oldModel.ID ||
		len(continuation) != 1 || continuation[0].Model.ID != newModel.ID {
		t.Fatalf("explicit compactor should handle both summaries, then resume on the new model; calls: compactor=%d next=%d", len(summaries), len(continuation))
	}
}

// A provider from models.json runs through the existing protocol client with
// its configured key, headers, and bearer header.
func TestConfiguredProviderRunsThroughTheCompletionsClient(t *testing.T) {
	var seen http.Header
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r.Header.Clone()
		if r.URL.Path != "/v1/chat/completions" {
			t.Errorf("request path = %q", r.URL.Path)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	}))
	defer server.Close()
	r := testRuntime(t)
	modelsPath := filepath.Join(r.agentDir, "models.json")
	raw := `{"providers":{"local-openai":{"api":"openai-completions","baseUrl":"` + server.URL + `/v1","apiKey":"synthetic-local-key","authHeader":true,"headers":{"X-Deployment":"synthetic"},"models":[{"id":"qwen2.5-coder","contextWindow":32000,"maxTokens":4096}]}}}`
	if err := os.WriteFile(modelsPath, []byte(raw), 0600); err != nil {
		t.Fatal(err)
	}
	loaded, err := config.LoadModels(modelsPath)
	if err != nil {
		t.Fatal(err)
	}
	r.models = loaded
	if err := r.configure(config.StartupOverrides{Provider: "local-openai", Model: "qwen2.5-coder", Thinking: "off"}); err != nil {
		t.Fatal(err)
	}
	if r.authErr != nil {
		t.Fatalf("the configured provider did not resolve a credential: %v", r.authErr)
	}
	response, err := r.prov.Send(context.Background(), provider.Request{Model: r.cfg.Model, Thinking: "off", Messages: []provider.Message{{Role: "user", Content: []provider.Block{{Type: "text", Text: "hi"}}}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if response.Text != "ok" {
		t.Fatalf("response = %+v", response)
	}
	if seen.Get("Authorization") != "Bearer synthetic-local-key" {
		t.Fatalf("authorization header = %q", seen.Get("Authorization"))
	}
	if seen.Get("X-Deployment") != "synthetic" {
		t.Fatalf("configured header = %q", seen.Get("X-Deployment"))
	}
}

// optionalKeyRPCRunner drives the production RPC prompt path over a runtime
// that configure built. The runtime's protocol client is real; no fake
// provider is installed.
func optionalKeyRPCRunner(t *testing.T, r *runtime) (*rpcRunner, *rpcLog) {
	t.Helper()
	log := &rpcLog{}
	enc := json.NewEncoder(log)
	enc.SetEscapeHTML(false)
	s := &rpcRunner{r: r, out: enc}
	s.loop = r.newLoop(s.sink)
	t.Cleanup(func() {
		s.cancelActive()
		s.waitWorkers()
	})
	return s, log
}

func writeOptionalKeyModels(t *testing.T, agentDir, baseURL, extra string) *config.ModelsFile {
	t.Helper()
	raw := `{"providers":{"local-openai":{"api":"openai-completions","baseUrl":"` + baseURL + `"` + extra + `,"models":[{"id":"qwen2.5-coder","contextWindow":32000,"maxTokens":4096}]}}}`
	path := filepath.Join(agentDir, "models.json")
	if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
		t.Fatal(err)
	}
	models, err := config.LoadModels(path)
	if err != nil {
		t.Fatal(err)
	}
	return models
}

func newOptionalKeyServer(t *testing.T, gotAuth *atomic.Bool, requests *atomic.Int64) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if _, present := r.Header["Authorization"]; present {
			gotAuth.Store(true)
		}
		if r.URL.Path != "/v1/chat/completions" {
			t.Errorf("request path = %q", r.URL.Path)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"keyless\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	}))
	t.Cleanup(server.Close)
	return server
}

// A custom Chat Completions model with no configured or stored key is usable,
// streams text, and sends no Authorization field at all.
func TestOptionalAPIKeyCompletions(t *testing.T) {
	var gotAuth, sawDelta atomic.Bool
	var requests atomic.Int64
	server := newOptionalKeyServer(t, &gotAuth, &requests)
	r := testRuntime(t)
	r.models = writeOptionalKeyModels(t, r.agentDir, server.URL+"/v1", "")
	if err := r.configure(config.StartupOverrides{Provider: "local-openai", Model: "qwen2.5-coder", Thinking: "off"}); err != nil {
		t.Fatal(err)
	}
	if r.authErr != nil {
		t.Fatalf("keyless custom provider kept an auth error: %v", r.authErr)
	}
	var row *usableModel
	for _, model := range r.usableModels() {
		if model.provider == "local-openai" && model.option.Model.ID == "qwen2.5-coder" {
			model := model
			row = &model
		}
	}
	if row == nil {
		t.Fatal("the keyless model is not in usableModels")
	}
	if row.credential != "no API key configured" {
		t.Fatalf("picker credential = %q", row.credential)
	}

	s, log := optionalKeyRPCRunner(t, r)
	resp := s.handle(rpcCommand{ID: strptr("optional-key"), Type: "prompt", Message: "Reply with OK only."})
	if resp == nil || !resp.Success {
		t.Fatalf("prompt response = %+v", resp)
	}
	objects := log.waitFor(t, "agent_settled", isType("agent_settled"))
	var text strings.Builder
	for _, object := range objects {
		if object["type"] != "message_update" {
			continue
		}
		event, _ := object["assistantMessageEvent"].(map[string]any)
		if event["type"] != "text_delta" {
			continue
		}
		delta, _ := event["delta"].(string)
		if delta != "" {
			sawDelta.Store(true)
			text.WriteString(delta)
		}
	}
	if !sawDelta.Load() || strings.TrimSpace(text.String()) == "" {
		t.Fatalf("no nonempty text delta in %v", objects)
	}
	settled := objects[len(objects)-1]
	if settled["type"] != "agent_settled" || settled["aborted"] != false {
		t.Fatalf("settled = %v", settled)
	}
	if requests.Load() != 1 {
		t.Fatalf("requests = %d, want one", requests.Load())
	}
	if gotAuth.Load() {
		t.Fatal("keyless request carried an Authorization field")
	}
}

// An omitted key is genuine absence. Every consulted blank, malformed, or
// unsupported credential keeps the model unavailable and sends no request.
func TestOptionalAPIKeyRejectsBadCredentials(t *testing.T) {
	const unsetEnv = "PSI_OPTIONAL_KEY_UNSET"
	const emptyEnv = "PSI_OPTIONAL_KEY_EMPTY"
	t.Setenv(unsetEnv, "placeholder")
	if err := os.Unsetenv(unsetEnv); err != nil {
		t.Fatal(err)
	}
	t.Setenv(emptyEnv, "")

	cases := []struct {
		name       string
		extra      string
		auth       string
		wantUsable bool
	}{
		{name: "empty literal", extra: `,"apiKey":""`},
		{name: "null literal", extra: `,"apiKey":null`},
		{name: "whitespace literal", extra: `,"apiKey":"   "`},
		{name: "missing environment reference", extra: `,"apiKey":"$` + unsetEnv + `"`},
		{name: "empty environment reference", extra: `,"apiKey":"$` + emptyEnv + `"`},
		{name: "command reference", extra: `,"apiKey":"!echo not-executed"`},
		{name: "authHeader without key", extra: `,"authHeader":true`},
		{name: "unsupported stored entry", extra: `,"apiKey":"synthetic-lower-priority"`, auth: `{"local-openai":{"type":"oauth","access":"synthetic"}}`},
		{name: "malformed auth document", extra: `,"apiKey":"synthetic"`, auth: `{`},
		{name: "stored key overrides blank literal", extra: `,"apiKey":""`, auth: `{"local-openai":{"type":"api_key","key":"synthetic-stored"}}`, wantUsable: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var gotAuth atomic.Bool
			var requests atomic.Int64
			server := newOptionalKeyServer(t, &gotAuth, &requests)
			r := testRuntime(t)
			if tc.auth != "" {
				if err := os.WriteFile(filepath.Join(r.agentDir, "auth.json"), []byte(tc.auth), 0600); err != nil {
					t.Fatal(err)
				}
			}
			r.models = writeOptionalKeyModels(t, r.agentDir, server.URL+"/v1", tc.extra)
			if err := r.configure(config.StartupOverrides{Provider: "local-openai", Model: "qwen2.5-coder", Thinking: "off"}); err != nil {
				t.Fatal(err)
			}
			available := false
			for _, model := range r.usableModels() {
				if model.provider == "local-openai" && model.option.Model.ID == "qwen2.5-coder" {
					available = true
				}
			}
			s, log := optionalKeyRPCRunner(t, r)
			resp := s.handle(rpcCommand{ID: strptr("bad-key"), Type: "prompt", Message: "Reply with OK only."})
			if !tc.wantUsable {
				if r.authErr == nil {
					t.Fatal("the model accepted an invalid credential")
				}
				if available {
					t.Fatal("the model with an invalid credential is in usableModels")
				}
				if resp == nil || resp.Success {
					t.Fatalf("prompt response = %+v", resp)
				}
				if requests.Load() != 0 {
					t.Fatalf("requests = %d before a rejected prompt", requests.Load())
				}
				return
			}
			if r.authErr != nil {
				t.Fatalf("stored key did not override the lower-priority literal: %v", r.authErr)
			}
			if !available {
				t.Fatal("the model with a stored key is not in usableModels")
			}
			if resp == nil || !resp.Success {
				t.Fatalf("prompt response = %+v", resp)
			}
			objects := log.waitFor(t, "agent_settled", isType("agent_settled"))
			if objects[len(objects)-1]["aborted"] != false {
				t.Fatalf("settled = %v", objects[len(objects)-1])
			}
			if requests.Load() != 1 {
				t.Fatalf("requests = %d, want one", requests.Load())
			}
			if !gotAuth.Load() {
				t.Fatal("the stored key was not sent")
			}
		})
	}
}

func anthropicSyntheticStream(text string) string {
	return "event: message_start\ndata: {\"message\":{\"id\":\"synthetic\",\"model\":\"synthetic-model\",\"usage\":{\"input_tokens\":1,\"output_tokens\":0}}}\n\n" +
		"event: content_block_start\ndata: {\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n" +
		"event: content_block_delta\ndata: {\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"" + text + "\"}}\n\n" +
		"event: content_block_stop\ndata: {\"index\":0}\n\n" +
		"event: message_delta\ndata: {\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"input_tokens\":1,\"output_tokens\":1}}\n\n" +
		"event: message_stop\ndata: {}\n\n"
}

func responsesKeylessStream(text string) string {
	item := map[string]any{"type": "message", "id": "msg_keyless", "role": "assistant", "content": []any{map[string]any{"type": "output_text", "text": text}}}
	return openAISyntheticEvent("response.output_item.added", map[string]any{"output_index": 0, "item": map[string]any{"type": "message", "id": "msg_keyless", "role": "assistant", "content": []any{}}}) +
		openAISyntheticEvent("response.output_text.delta", map[string]any{"output_index": 0, "delta": text}) +
		openAISyntheticEvent("response.output_item.done", map[string]any{"output_index": 0, "item": item}) +
		openAISyntheticEvent("response.completed", map[string]any{"response": map[string]any{"id": "resp_keyless", "status": "completed", "output": []any{item}}})
}

func writeRawTestModels(t *testing.T, agentDir, raw string) *config.ModelsFile {
	t.Helper()
	path := filepath.Join(agentDir, "models.json")
	if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
		t.Fatal(err)
	}
	models, err := config.LoadModels(path)
	if err != nil {
		t.Fatal(err)
	}
	return models
}

func headerFieldPresent(header http.Header, name string) bool {
	for field := range header {
		if strings.EqualFold(field, name) {
			return true
		}
	}
	return false
}

func optionalModelUsable(r *runtime, providerID, modelID string) bool {
	for _, model := range r.usableModels() {
		if model.provider == providerID && model.option.Model.ID == modelID {
			return true
		}
	}
	return false
}

func optionalKeyPromptText(t *testing.T, r *runtime, message string) string {
	t.Helper()
	s, log := optionalKeyRPCRunner(t, r)
	resp := s.handle(rpcCommand{ID: strptr("optional"), Type: "prompt", Message: message})
	if resp == nil || !resp.Success {
		t.Fatalf("prompt response = %+v", resp)
	}
	objects := log.waitFor(t, "agent_settled", isType("agent_settled"))
	var text strings.Builder
	for _, object := range objects {
		if object["type"] != "message_update" {
			continue
		}
		event, _ := object["assistantMessageEvent"].(map[string]any)
		if event["type"] != "text_delta" {
			continue
		}
		delta, _ := event["delta"].(string)
		text.WriteString(delta)
	}
	if strings.TrimSpace(text.String()) == "" {
		t.Fatalf("no nonempty text delta in %v", objects)
	}
	if objects[len(objects)-1]["type"] != "agent_settled" || objects[len(objects)-1]["aborted"] != false {
		t.Fatalf("settled = %v", objects[len(objects)-1])
	}
	return text.String()
}

// Responses and Anthropic Messages custom models use the same optional-key
// policy as Chat Completions. Each protocol streams on its own path and omits
// its generated credential header.
func TestOptionalAPIKeyCustomProtocols(t *testing.T) {
	t.Run("responses streams without authorization", func(t *testing.T) {
		var authField atomic.Bool
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/v1/responses" {
				t.Errorf("request path = %q", r.URL.Path)
			}
			if headerFieldPresent(r.Header, "Authorization") {
				authField.Store(true)
			}
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, responsesKeylessStream("keyless responses"))
		}))
		t.Cleanup(server.Close)
		r := testRuntime(t)
		r.models = writeRawTestModels(t, r.agentDir, `{"providers":{"responses-keyless":{"api":"openai-responses","baseUrl":"`+server.URL+`/v1","models":[{"id":"keyless-responses","contextWindow":32000,"maxTokens":1024}]}}}`)
		if err := r.configure(config.StartupOverrides{Provider: "responses-keyless", Model: "keyless-responses", Thinking: "off"}); err != nil {
			t.Fatal(err)
		}
		if r.authErr != nil {
			t.Fatalf("keyless Responses provider kept an auth error: %v", r.authErr)
		}
		if !optionalModelUsable(r, "responses-keyless", "keyless-responses") {
			t.Fatal("the keyless Responses model is not in usableModels")
		}
		if text := optionalKeyPromptText(t, r, "Reply with OK only."); !strings.Contains(text, "keyless responses") {
			t.Fatalf("streamed text = %q", text)
		}
		if authField.Load() {
			t.Fatal("keyless Responses request carried an Authorization field")
		}
	})

	t.Run("messages streams without x-api-key", func(t *testing.T) {
		var keyField atomic.Bool
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/v1/messages" {
				t.Errorf("request path = %q", r.URL.Path)
			}
			if headerFieldPresent(r.Header, "x-api-key") {
				keyField.Store(true)
			}
			if r.Header.Get("anthropic-version") != "2023-06-01" {
				t.Errorf("anthropic-version = %q", r.Header.Get("anthropic-version"))
			}
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, anthropicSyntheticStream("keyless messages"))
		}))
		t.Cleanup(server.Close)
		r := testRuntime(t)
		r.models = writeRawTestModels(t, r.agentDir, `{"providers":{"messages-keyless":{"api":"anthropic-messages","baseUrl":"`+server.URL+`","models":[{"id":"keyless-messages","contextWindow":32000,"maxTokens":1024}]}}}`)
		if err := r.configure(config.StartupOverrides{Provider: "messages-keyless", Model: "keyless-messages", Thinking: "off"}); err != nil {
			t.Fatal(err)
		}
		if r.authErr != nil {
			t.Fatalf("keyless Messages provider kept an auth error: %v", r.authErr)
		}
		if !optionalModelUsable(r, "messages-keyless", "keyless-messages") {
			t.Fatal("the keyless Messages model is not in usableModels")
		}
		if text := optionalKeyPromptText(t, r, "Reply with OK only."); !strings.Contains(text, "keyless messages") {
			t.Fatalf("streamed text = %q", text)
		}
		if keyField.Load() {
			t.Fatal("keyless Messages request carried an x-api-key field")
		}
	})

	t.Run("explicit headers are preserved", func(t *testing.T) {
		var responsesAuth, messagesKey string
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/event-stream")
			switch r.URL.Path {
			case "/v1/responses":
				responsesAuth = r.Header.Get("Authorization")
				_, _ = io.WriteString(w, responsesKeylessStream("explicit responses"))
			case "/v1/messages":
				messagesKey = r.Header.Get("x-api-key")
				_, _ = io.WriteString(w, anthropicSyntheticStream("explicit messages"))
			default:
				t.Errorf("request path = %q", r.URL.Path)
			}
		}))
		t.Cleanup(server.Close)
		r := testRuntime(t)
		r.models = writeRawTestModels(t, r.agentDir, `{"providers":{"responses-explicit":{"api":"openai-responses","baseUrl":"`+server.URL+`/v1","headers":{"Authorization":"Bearer explicit-gateway"},"models":[{"id":"explicit-responses","contextWindow":32000,"maxTokens":1024}]},"messages-explicit":{"api":"anthropic-messages","baseUrl":"`+server.URL+`","headers":{"x-api-key":"explicit-messages-key"},"models":[{"id":"explicit-messages","contextWindow":32000,"maxTokens":1024}]}}}`)
		if err := r.configure(config.StartupOverrides{Provider: "responses-explicit", Model: "explicit-responses", Thinking: "off"}); err != nil {
			t.Fatal(err)
		}
		if r.authErr != nil {
			t.Fatalf("explicit Responses header kept an auth error: %v", r.authErr)
		}
		if text := optionalKeyPromptText(t, r, "Reply with OK only."); !strings.Contains(text, "explicit responses") {
			t.Fatalf("streamed text = %q", text)
		}
		if err := r.configure(config.StartupOverrides{Provider: "messages-explicit", Model: "explicit-messages", Thinking: "off"}); err != nil {
			t.Fatal(err)
		}
		if r.authErr != nil {
			t.Fatalf("explicit Messages header kept an auth error: %v", r.authErr)
		}
		if text := optionalKeyPromptText(t, r, "Reply with OK only."); !strings.Contains(text, "explicit messages") {
			t.Fatalf("streamed text = %q", text)
		}
		if responsesAuth != "Bearer explicit-gateway" {
			t.Fatalf("explicit Responses Authorization = %q", responsesAuth)
		}
		if messagesKey != "explicit-messages-key" {
			t.Fatalf("explicit Messages x-api-key = %q", messagesKey)
		}
	})

	t.Run("mixed protocols keep unrelated keys required", func(t *testing.T) {
		r := testRuntime(t)
		r.models = writeRawTestModels(t, r.agentDir, `{"providers":{"mixed":{"api":"anthropic-messages","baseUrl":"https://gateway.example/v1","models":[{"id":"keyless-messages","contextWindow":8192,"maxTokens":1024},{"id":"keyless-responses","api":"openai-responses","contextWindow":8192,"maxTokens":1024},{"id":"needs-key","api":"google-generative-ai","contextWindow":8192,"maxTokens":1024}]}}}`)
		if err := r.configure(config.StartupOverrides{Provider: "mixed", Model: "keyless-messages", Thinking: "off"}); err != nil {
			t.Fatal(err)
		}
		if r.authErr != nil || !optionalModelUsable(r, "mixed", "keyless-messages") {
			t.Fatalf("keyless Messages = authErr %v, usable %v", r.authErr, optionalModelUsable(r, "mixed", "keyless-messages"))
		}
		if err := r.configure(config.StartupOverrides{Provider: "mixed", Model: "keyless-responses", Thinking: "off"}); err != nil {
			t.Fatal(err)
		}
		if r.authErr != nil || !optionalModelUsable(r, "mixed", "keyless-responses") {
			t.Fatalf("keyless Responses = authErr %v, usable %v", r.authErr, optionalModelUsable(r, "mixed", "keyless-responses"))
		}
		if err := r.configure(config.StartupOverrides{Provider: "mixed", Model: "needs-key", Thinking: "off"}); err != nil {
			t.Fatal(err)
		}
		if r.authErr == nil {
			t.Fatal("a Google sibling was allowed without a key")
		}
		if optionalModelUsable(r, "mixed", "needs-key") {
			t.Fatal("the Google sibling is in usableModels without a key")
		}
	})
}

func unsetTestEnv(t *testing.T, name string) {
	t.Helper()
	t.Setenv(name, "placeholder")
	if err := os.Unsetenv(name); err != nil {
		t.Fatal(err)
	}
}

// Built-in openai and anthropic models may omit a key only on a user-selected,
// non-default route. A route that normalizes back to the default stays
// required, and catalog or cache data never grants permission.
func TestOptionalAPIKeyBuiltinOverrides(t *testing.T) {
	t.Run("openai keyless override streams without authorization", func(t *testing.T) {
		var authField atomic.Bool
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/v1/responses" {
				t.Errorf("request path = %q", r.URL.Path)
			}
			if headerFieldPresent(r.Header, "Authorization") {
				authField.Store(true)
			}
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, responsesKeylessStream("keyless builtin openai"))
		}))
		t.Cleanup(server.Close)
		clearProviderAPIKeys(t)
		r := testRuntime(t)
		unsetTestEnv(t, "OPENAI_API_KEY")
		models := openAILoginTestModels(t)
		models.Providers["openai"] = config.ProviderMeta{BaseURL: server.URL + "/v1"}
		r.models = models
		if err := r.configure(config.StartupOverrides{Provider: "openai", Model: "gpt-6.1-sol", Thinking: "off", ThinkingExplicit: true}); err != nil {
			t.Fatal(err)
		}
		if r.authErr != nil {
			t.Fatalf("keyless OpenAI override kept an auth error: %v", r.authErr)
		}
		if !optionalModelUsable(r, "openai", "gpt-6.1-sol") {
			t.Fatal("the keyless OpenAI override is not in usableModels")
		}
		if text := optionalKeyPromptText(t, r, "Reply with OK only."); !strings.Contains(text, "keyless builtin openai") {
			t.Fatalf("streamed text = %q", text)
		}
		if authField.Load() {
			t.Fatal("keyless OpenAI override carried an Authorization field")
		}
	})

	t.Run("anthropic keyless override streams without x-api-key", func(t *testing.T) {
		var keyField atomic.Bool
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/v1/messages" {
				t.Errorf("request path = %q", r.URL.Path)
			}
			if headerFieldPresent(r.Header, "x-api-key") {
				keyField.Store(true)
			}
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, anthropicSyntheticStream("keyless builtin anthropic"))
		}))
		t.Cleanup(server.Close)
		r := testRuntime(t)
		unsetTestEnv(t, "ANTHROPIC_API_KEY")
		models := openAILoginTestModels(t)
		models.Providers["anthropic"] = config.ProviderMeta{BaseURL: server.URL + "/v1/"}
		r.models = models
		if err := r.configure(config.StartupOverrides{Provider: "anthropic", Model: "claude-sonnet-4-5", Thinking: "off", ThinkingExplicit: true}); err != nil {
			t.Fatal(err)
		}
		if r.authErr != nil {
			t.Fatalf("keyless Anthropic override kept an auth error: %v", r.authErr)
		}
		if !optionalModelUsable(r, "anthropic", "claude-sonnet-4-5") {
			t.Fatal("the keyless Anthropic override is not in usableModels")
		}
		if text := optionalKeyPromptText(t, r, "Reply with OK only."); !strings.Contains(text, "keyless builtin anthropic") {
			t.Fatalf("streamed text = %q", text)
		}
		if keyField.Load() {
			t.Fatal("keyless Anthropic override carried an x-api-key field")
		}
	})

	t.Run("anthropic inherited environment key is sent", func(t *testing.T) {
		var seenKey string
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			seenKey = r.Header.Get("x-api-key")
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, anthropicSyntheticStream("environment key"))
		}))
		t.Cleanup(server.Close)
		r := testRuntime(t)
		t.Setenv("ANTHROPIC_API_KEY", "synthetic-environment-key")
		models := openAILoginTestModels(t)
		models.Providers["anthropic"] = config.ProviderMeta{BaseURL: server.URL}
		r.models = models
		if err := r.configure(config.StartupOverrides{Provider: "anthropic", Model: "claude-sonnet-4-5", Thinking: "off", ThinkingExplicit: true}); err != nil {
			t.Fatal(err)
		}
		if r.authErr != nil {
			t.Fatalf("inherited key kept an auth error: %v", r.authErr)
		}
		if text := optionalKeyPromptText(t, r, "Reply with OK only."); !strings.Contains(text, "environment key") {
			t.Fatalf("streamed text = %q", text)
		}
		if seenKey != "synthetic-environment-key" {
			t.Fatalf("x-api-key = %q", seenKey)
		}
	})

	t.Run("blank environment and authHeader reject bad credentials", func(t *testing.T) {
		clearProviderAPIKeys(t)
		var requests atomic.Int64
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			requests.Add(1)
			t.Error("rejected override sent a request")
			http.Error(w, "unexpected", http.StatusInternalServerError)
		}))
		t.Cleanup(server.Close)
		for _, tc := range []struct {
			name    string
			family  string
			model   string
			setting config.ProviderMeta
			env     string
		}{
			{name: "openai blank env", family: "openai", model: "gpt-6.1-sol", setting: config.ProviderMeta{BaseURL: server.URL + "/v1"}},
			{name: "openai authHeader requires a key", family: "openai", model: "gpt-6.1-sol", setting: config.ProviderMeta{BaseURL: server.URL + "/v1", AuthHeader: true}},
			{name: "openai authHeader cannot skip a blank configured key", family: "openai", model: "gpt-6.1-sol", env: "synthetic-lower-priority", setting: config.ProviderMeta{BaseURL: server.URL + "/v1", AuthHeader: true, APIKeyPresent: true}},
			{name: "anthropic blank env", family: "anthropic", model: "claude-sonnet-4-5", setting: config.ProviderMeta{BaseURL: server.URL}},
		} {
			t.Run(tc.name, func(t *testing.T) {
				r := testRuntime(t)
				models := openAILoginTestModels(t)
				if tc.env != "" {
					t.Setenv("OPENAI_API_KEY", tc.env)
				}
				models.Providers[tc.family] = tc.setting
				r.models = models
				if err := r.configure(config.StartupOverrides{Provider: tc.family, Model: tc.model, Thinking: "off", ThinkingExplicit: true}); err != nil {
					t.Fatal(err)
				}
				if r.authErr == nil {
					t.Fatal("the override accepted a blank or vetoed credential")
				}
				if optionalModelUsable(r, tc.family, tc.model) {
					t.Fatal("the rejected override is in usableModels")
				}
				s, _ := optionalKeyRPCRunner(t, r)
				if resp := s.handle(rpcCommand{ID: strptr("rejected"), Type: "prompt", Message: "Reply with OK only."}); resp == nil || resp.Success {
					t.Fatalf("prompt response = %+v", resp)
				}
			})
		}
		if requests.Load() != 0 {
			t.Fatalf("rejected overrides made %d requests", requests.Load())
		}
	})

	t.Run("equivalent official routes stay required", func(t *testing.T) {
		r := testRuntime(t)
		unsetTestEnv(t, "OPENAI_API_KEY")
		unsetTestEnv(t, "ANTHROPIC_API_KEY")
		models := openAILoginTestModels(t)
		models.Providers["openai"] = config.ProviderMeta{
			BaseURL:        "https://gateway.example/v1",
			ModelOverrides: map[string]json.RawMessage{"gpt-6.1-sol": json.RawMessage(`{"baseUrl":"https://API.OPENAI.COM.:0443/v1/"}`)},
		}
		models.Providers["anthropic"] = config.ProviderMeta{
			BaseURL:        "https://gateway.example/anthropic",
			ModelOverrides: map[string]json.RawMessage{"claude-sonnet-4-5": json.RawMessage(`{"baseUrl":"https://api.anthropic.com/v1/"}`)},
		}
		r.models = models
		for _, tc := range []struct {
			family, model string
		}{
			{"openai", "gpt-6.1-sol"},
			{"anthropic", "claude-sonnet-4-5"},
		} {
			if err := r.configure(config.StartupOverrides{Provider: tc.family, Model: tc.model, Thinking: "off", ThinkingExplicit: true}); err != nil {
				t.Fatal(err)
			}
			if r.authErr == nil {
				t.Fatalf("%s/%s accepted an equivalent official route without a key", tc.family, tc.model)
			}
			if optionalModelUsable(r, tc.family, tc.model) {
				t.Fatalf("%s/%s is in usableModels on an equivalent official route", tc.family, tc.model)
			}
		}
		for _, tc := range []struct {
			family, model string
		}{
			{"openai", "gpt-4.1-mini"},
			{"anthropic", "claude-haiku-4-5"},
		} {
			if err := r.configure(config.StartupOverrides{Provider: tc.family, Model: tc.model, Thinking: "off", ThinkingExplicit: true}); err != nil {
				t.Fatal(err)
			}
			if r.authErr != nil || !optionalModelUsable(r, tc.family, tc.model) {
				t.Fatalf("%s/%s non-default route = authErr %v, usable %v", tc.family, tc.model, r.authErr, optionalModelUsable(r, tc.family, tc.model))
			}
		}
	})

	t.Run("invalid override URLs stay required", func(t *testing.T) {
		r := testRuntime(t)
		unsetTestEnv(t, "OPENAI_API_KEY")
		unsetTestEnv(t, "ANTHROPIC_API_KEY")
		models := openAILoginTestModels(t)
		r.models = models
		for _, tc := range []struct {
			family, model, baseURL string
		}{
			{"openai", "gpt-6.1-sol", "https://gateway.example/v1?query=1"},
			{"openai", "gpt-6.1-sol", "http://:8888/v1"},
			{"openai", "gpt-6.1-sol", "https://gateway.example:65536/v1"},
			{"anthropic", "claude-sonnet-4-5", "https://gateway.example/v1?"},
			{"anthropic", "claude-sonnet-4-5", "https://gateway.example/v1#"},
		} {
			models.Providers[tc.family] = config.ProviderMeta{BaseURL: tc.baseURL}
			if err := r.configure(config.StartupOverrides{Provider: tc.family, Model: tc.model, Thinking: "off", ThinkingExplicit: true}); err != nil {
				t.Fatal(err)
			}
			if r.authErr == nil || optionalModelUsable(r, tc.family, tc.model) {
				t.Errorf("%s/%s accepted invalid override URL %q", tc.family, tc.model, tc.baseURL)
			}
		}
	})

	t.Run("catalog and cache routes do not grant permission", func(t *testing.T) {
		clearProviderAPIKeys(t)
		unsetTestEnv(t, "OPENAI_API_KEY")
		r := testRuntime(t)
		r.models = openAILoginTestModels(t)
		if err := r.configure(config.StartupOverrides{Provider: "openai", Model: "gpt-6.1-sol", Thinking: "off", ThinkingExplicit: true}); err != nil {
			t.Fatal(err)
		}
		if r.authErr == nil || optionalModelUsable(r, "openai", "gpt-6.1-sol") {
			t.Fatal("a catalog-only OpenAI route allowed keyless use")
		}

		cachePath := filepath.Join(r.agentDir, "models-store.json")
		cache := `{"openai":{"lastModified":` + fmt.Sprint(time.Now().Add(time.Hour).UnixMilli()) + `,"models":[{"provider":"openai","type":"chat","id":"gpt-cache-only-test","api":"openai-responses","baseUrl":"https://cache.example/v1","contextWindow":32000,"maxTokens":1024}]}}`
		if err := os.WriteFile(cachePath, []byte(cache), 0600); err != nil {
			t.Fatal(err)
		}
		r.models = writeRawTestModels(t, r.agentDir, `{}`)
		if err := r.configure(config.StartupOverrides{Provider: "openai", Model: "gpt-cache-only-test", Thinking: "off", ThinkingExplicit: true}); err != nil {
			t.Fatal(err)
		}
		if r.authErr == nil || optionalModelUsable(r, "openai", "gpt-cache-only-test") {
			t.Fatal("a cache-only OpenAI route allowed keyless use")
		}
	})
}

func TestOptionalAPIKeyOpenAICredentialTransitions(t *testing.T) {
	type requestRecord struct {
		host, path, authorization string
	}
	var mu sync.Mutex
	var records []requestRecord
	transport := roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		mu.Lock()
		records = append(records, requestRecord{host: req.URL.Host, path: req.URL.Path, authorization: req.Header.Get("Authorization")})
		mu.Unlock()
		if req.URL.Host != "gateway.example" {
			return nil, &provider.ProviderError{Kind: provider.ErrInvalid, Message: "unexpected transition destination"}
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(responsesKeylessStream("transition reply"))), Header: make(http.Header)}, nil
	})
	recordCount := func() int {
		mu.Lock()
		defer mu.Unlock()
		return len(records)
	}
	lastRecord := func() requestRecord {
		mu.Lock()
		defer mu.Unlock()
		return records[len(records)-1]
	}

	clearProviderAPIKeys(t)
	r := testRuntime(t)
	unsetTestEnv(t, "OPENAI_API_KEY")
	models := openAILoginTestModels(t)
	models.Providers["openai"] = config.ProviderMeta{BaseURL: "https://gateway.example/v1"}
	r.models = models
	r.settings.MaxRetries = 0
	if err := r.configure(config.StartupOverrides{Provider: "openai", Model: "gpt-6.1-sol", Thinking: "off", ThinkingExplicit: true}); err != nil {
		t.Fatal(err)
	}
	if r.authErr != nil {
		t.Fatalf("keyless override kept an auth error: %v", r.authErr)
	}
	responses, ok := r.prov.(*provider.ResponsesClient)
	if !ok {
		t.Fatal("the override did not configure a Responses client")
	}
	responses.HTTP.Transport = transport
	send := func() (string, error) {
		resp, err := r.prov.Send(context.Background(), provider.Request{Model: r.cfg.Model, Thinking: "off", Messages: []provider.Message{{Role: "user", Content: []provider.Block{{Type: "text", Text: "hi"}}}}}, nil)
		if err != nil {
			return "", err
		}
		return resp.Text, nil
	}

	if text, err := send(); err != nil || text != "transition reply" {
		t.Fatalf("keyless transition send = %q, %v", text, err)
	}
	if record := lastRecord(); record.host != "gateway.example" || record.path != "/v1/responses" || record.authorization != "" {
		t.Fatalf("keyless transition request = %+v", record)
	}

	writeAppFile(t, filepath.Join(r.agentDir, "auth.json"), `{"openai":{"type":"api_key","key":"synthetic-transition-key"}}`)
	if text, err := send(); err != nil || text != "transition reply" {
		t.Fatalf("stored key transition send = %q, %v", text, err)
	}
	if record := lastRecord(); record.authorization != "Bearer synthetic-transition-key" {
		t.Fatalf("stored key transition authorization = %q", record.authorization)
	}

	writeAppFile(t, filepath.Join(r.agentDir, "auth.json"), `{"openai":{"type":"oauth","access":"synthetic-access","refresh":"synthetic-refresh","expires":9999999999999,"clientId":"synthetic-client","scopes":["chatgpt.tokens.use.direct"]}}`)
	before := recordCount()
	if _, err := send(); err == nil || !strings.Contains(err.Error(), "official OpenAI endpoint") {
		t.Fatalf("OAuth transition send = %v", err)
	}
	if recordCount() != before {
		t.Fatalf("OAuth transition made %d requests", recordCount()-before)
	}
	if optionalModelUsable(r, "openai", "gpt-6.1-sol") {
		t.Fatal("the picker listed an OpenAI override with an OAuth entry")
	}

	writeAppFile(t, filepath.Join(r.agentDir, "auth.json"), `{`)
	before = recordCount()
	if _, err := send(); err == nil {
		t.Fatal("malformed auth transition was accepted")
	}
	if recordCount() != before {
		t.Fatalf("malformed auth transition made %d requests", recordCount()-before)
	}

	writeAppFile(t, filepath.Join(r.agentDir, "auth.json"), `{"openai":{"type":"api_key","key":"synthetic-recovered-key"}}`)
	if text, err := send(); err != nil || text != "transition reply" {
		t.Fatalf("recovered key transition send = %q, %v", text, err)
	}
	if record := lastRecord(); record.authorization != "Bearer synthetic-recovered-key" {
		t.Fatalf("recovered key transition authorization = %q", record.authorization)
	}
}
