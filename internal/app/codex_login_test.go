package app

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"psi/internal/config"
	"psi/internal/provider"
	"psi/internal/session"
	"psi/internal/terminal"
)

// codexAppTestAccess returns a JWT-shaped access token whose middle segment
// carries the ChatGPT account claim used by CodexAuth.
func codexAppTestAccess(account string) string {
	payload := base64.RawURLEncoding.EncodeToString([]byte(`{"https://api.openai.com/auth":{"chatgpt_account_id":"` + account + `"}}`))
	return "eyJhbGciOiJub25lIn0." + payload + ".synthetic-signature"
}

func codexAppTestTokenBody(access, refresh string) string {
	return `{"access_token":"` + access + `","refresh_token":"` + refresh + `","expires_in":3600,"id_token":"ignored-id-token","scope":"openid profile email offline_access"}`
}

// deliverAppCodexCallback sends one callback to the test listener with the
// exact owned Host authority. The production port check stays unchanged.
func deliverAppCodexCallback(t *testing.T, addr, query string) int {
	t.Helper()
	client := &http.Client{Transport: &http.Transport{Proxy: nil}, Timeout: 5 * time.Second}
	defer client.CloseIdleConnections()
	req, err := http.NewRequest(http.MethodGet, "http://"+addr+"/auth/callback?"+query, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Host = addr
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.StatusCode
}

func codexAppTestModels(t *testing.T, baseURL string) *config.ModelsFile {
	t.Helper()
	models := openAILoginTestModels(t)
	if models.Providers == nil {
		models.Providers = map[string]config.ProviderMeta{}
	}
	models.Providers["openai-codex"] = config.ProviderMeta{BaseURL: baseURL}
	return models
}

func writeCodexLoginTestReply(w http.ResponseWriter, reply string) {
	message := map[string]any{"type": "message", "id": "msg_codex_login", "phase": "final_answer", "content": []any{map[string]any{"type": "output_text", "text": reply}}}
	w.Header().Set("Content-Type", "text/event-stream")
	_, _ = io.WriteString(w, openAISyntheticEvent("response.output_item.added", map[string]any{"output_index": 0, "item": message})+
		openAISyntheticEvent("response.output_text.delta", map[string]any{"output_index": 0, "delta": reply})+
		openAISyntheticEvent("response.output_item.done", map[string]any{"output_index": 0, "item": message})+
		openAISyntheticEvent("response.completed", map[string]any{"response": map[string]any{"id": "resp_codex_login", "status": "completed", "output": []any{message}}}))
}

// A cold interactive start signs in through the native Codex browser flow on
// the injected listener, saves the Codex entry, keeps the same model without
// a duplicate change, and completes one reply through a configured local
// Codex endpoint. No token, code, or verifier reaches chat, recall, session,
// or output. L1-L4, L7-L9.
func TestCodexLoginREPL(t *testing.T) {
	clearProviderAPIKeys(t)
	r := testRuntime(t)
	access, refresh := codexAppTestAccess("acct-repl"), "codex-repl-refresh"
	var requests atomic.Int32
	var gotAuth, gotAccount string
	var gotBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		requests.Add(1)
		gotAuth, gotAccount = req.Header.Get("Authorization"), req.Header.Get("chatgpt-account-id")
		if req.URL.Path != "/codex/responses" {
			t.Errorf("model path = %q", req.URL.Path)
			return
		}
		if err := json.NewDecoder(req.Body).Decode(&gotBody); err != nil {
			t.Error(err)
			return
		}
		writeCodexLoginTestReply(w, "codex-login-reply")
	}))
	defer server.Close()
	r.models = codexAppTestModels(t, server.URL)
	r.settings.MaxRetries = 0
	var exchanges atomic.Int32
	r.codexTransport = roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.String() != "https://auth.openai.com/oauth/token" {
			return nil, &provider.ProviderError{Kind: provider.ErrInvalid, Message: "unexpected test URL"}
		}
		exchanges.Add(1)
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(codexAppTestTokenBody(access, refresh))), Header: make(http.Header)}, nil
	})
	var bindAddr string
	r.codexBind = func(string) (net.Listener, error) {
		listener, err := net.Listen("tcp4", "127.0.0.1:0")
		if err != nil {
			return nil, err
		}
		bindAddr = listener.Addr().String()
		return listener, nil
	}
	browserURLs := make(chan string, 1)
	r.codexBrowser = func(_ context.Context, authURL string) error {
		browserURLs <- authURL
		return errors.New("synthetic launcher failure")
	}
	if err := r.configure(config.StartupOverrides{Provider: "openai-codex", Model: "gpt-5.6-luna", Thinking: "off", ThinkingExplicit: true}); err != nil {
		t.Fatal(err)
	}
	if r.authErr == nil {
		t.Fatal("cold Codex configure unexpectedly usable")
	}
	console, out, inputWrite, result := startLoginTestREPL(t, r)
	waitAppOutput(t, out, "gpt-5.6-luna")
	if _, err := inputWrite.Write([]byte("/login openai-codex\r\r")); err != nil {
		t.Fatal(err)
	}
	waitAppOutput(t, out, "Complete OpenAI Codex sign-in in your browser")
	var authURL string
	select {
	case authURL = <-browserURLs:
	case <-time.After(60 * time.Second):
		t.Fatal("browser never opened")
	}
	parsed, err := url.Parse(authURL)
	if err != nil {
		t.Fatal(err)
	}
	state := parsed.Query().Get("state")
	if state == "" {
		t.Fatalf("no state in %q", authURL)
	}
	if status := deliverAppCodexCallback(t, bindAddr, "state="+state+"&code=codex-app-code"); status != http.StatusOK {
		t.Fatalf("callback status = %d", status)
	}
	waitAppOutput(t, out, "Signed in to OpenAI Codex. Model: openai-codex/gpt-5.6-luna")
	if changes := countSessionEntries(r.store, "model_change"); changes != 0 {
		t.Fatalf("same-model activation appended %d model changes", changes)
	}
	if _, err := inputWrite.Write([]byte("Say PSI_CODEX_OK\r")); err != nil {
		t.Fatal(err)
	}
	waitAppOutput(t, out, "codex-login-reply")
	if err := inputWrite.Close(); err != nil {
		t.Fatal(err)
	}
	if err := <-result; err != nil {
		t.Fatalf("repl: %v", err)
	}
	if exchanges.Load() != 1 || requests.Load() != 1 {
		t.Fatalf("exchanges=%d requests=%d", exchanges.Load(), requests.Load())
	}
	if gotAuth != "Bearer "+access || gotAccount != "acct-repl" {
		t.Fatalf("model auth = %q account = %q", gotAuth, gotAccount)
	}
	if gotBody["model"] != "gpt-5.6-luna" || gotBody["store"] != false {
		t.Fatalf("model body = %v", gotBody)
	}
	if reasoning, ok := gotBody["reasoning"].(map[string]any); !ok || reasoning["effort"] != "none" {
		t.Fatalf("thinking off did not send effort none: %v", gotBody["reasoning"])
	}
	if r.authErr != nil || r.ov.Provider != "openai-codex" || r.ov.Model != "gpt-5.6-luna" {
		t.Fatalf("runtime = %s/%s err=%v", r.ov.Provider, r.ov.Model, r.authErr)
	}
	authData, err := os.ReadFile(filepath.Join(r.agentDir, "auth.json"))
	if err != nil {
		t.Fatal(err)
	}
	var entries map[string]map[string]any
	if err := json.Unmarshal(authData, &entries); err != nil {
		t.Fatal(err)
	}
	entry := entries["openai-codex"]
	if len(entry) != 5 || entry["type"] != "oauth" || entry["access"] != access ||
		entry["refresh"] != refresh || entry["accountId"] != "acct-repl" {
		t.Fatalf("stored entry = %v", entry)
	}
	sessionData, err := os.ReadFile(r.store.File())
	if err != nil {
		t.Fatal(err)
	}
	sessionText := string(sessionData)
	if !strings.Contains(sessionText, "Say PSI_CODEX_OK") || !strings.Contains(sessionText, "codex-login-reply") {
		t.Fatalf("prompt or reply missing from the session: %s", sessionText)
	}
	for _, secret := range []string{"/login openai-codex", "codex-app-code", access, refresh, "ignored-id-token", state, "auth.openai.com"} {
		if strings.Contains(sessionText, secret) {
			t.Fatalf("session contains %q", secret)
		}
	}
	for _, secret := range []string{"codex-app-code", access, refresh, "ignored-id-token", state} {
		for _, recalled := range console.Editor.History() {
			if strings.Contains(recalled, secret) {
				t.Fatalf("recall contains %q in %q", secret, recalled)
			}
		}
	}
}

// countSessionEntries counts one entry kind in the live session projection.
func countSessionEntries(store *session.Store, kind string) int {
	count := 0
	for _, entry := range store.Entries() {
		if entry.Kind == kind {
			count++
		}
	}
	return count
}

// Activation keeps a working non-Codex selection and stores nothing. L7.
func TestCodexLoginActivationRetainsWorkingModel(t *testing.T) {
	clearProviderAPIKeys(t)
	r := testRuntime(t)
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-synthetic-codex-activation")
	if err := r.configure(config.StartupOverrides{Provider: "anthropic", Model: "m", Thinking: "off", ThinkingExplicit: true}); err != nil {
		t.Fatal(err)
	}
	if r.authErr != nil {
		t.Fatalf("authErr: %v", r.authErr)
	}
	before := r.store.EntryCount()
	kind, text := r.activateCodexLogin()
	if kind != terminal.NoticeInfo || !strings.Contains(text, "Model unchanged: anthropic/m") {
		t.Fatalf("activation = %v %q", kind, text)
	}
	if r.ov.Provider != "anthropic" || r.ov.Model != "m" || r.authErr != nil {
		t.Fatalf("selection moved to %s/%s (%v)", r.ov.Provider, r.ov.Model, r.authErr)
	}
	if r.store.EntryCount() != before {
		t.Fatal("retained activation stored an entry")
	}
}

// Activation rechecks the same Codex model, clears the stale auth error, and
// does not append a duplicate model change. L7.
func TestCodexLoginActivationSameModel(t *testing.T) {
	clearProviderAPIKeys(t)
	r := testRuntime(t)
	r.models = codexAppTestModels(t, "http://127.0.0.1:1")
	if err := r.configure(config.StartupOverrides{Provider: "openai-codex", Model: "gpt-5.6-luna", Thinking: "off", ThinkingExplicit: true}); err != nil {
		t.Fatal(err)
	}
	if r.authErr == nil {
		t.Fatal("cold Codex configure unexpectedly usable")
	}
	writeAppFile(t, filepath.Join(r.agentDir, "auth.json"), `{"openai-codex":{"type":"oauth","access":"same-access","refresh":"same-refresh","expires":9999999999999,"accountId":"same-account"}}`)
	before := r.store.EntryCount()
	kind, text := r.activateCodexLogin()
	if kind != terminal.NoticeInfo || !strings.Contains(text, "Model: openai-codex/gpt-5.6-luna") {
		t.Fatalf("activation = %v %q", kind, text)
	}
	if r.authErr != nil || r.ov.Provider != "openai-codex" || r.ov.Model != "gpt-5.6-luna" {
		t.Fatalf("selection = %s/%s (%v)", r.ov.Provider, r.ov.Model, r.authErr)
	}
	if live := r.liveSettings(); live.Provider != r.prov || live.Model.ID != r.ov.Model || live.AuthErr != nil {
		t.Fatal("same-model activation did not publish the refreshed provider")
	}
	if r.store.EntryCount() != before {
		t.Fatal("same-model activation stored a model-change entry")
	}
}

// Activation selects the bundled Codex default for an offline non-Codex
// selection, persists the move, and reports no partial state. L7.
func TestCodexLoginActivationOfflineSelectsDefault(t *testing.T) {
	clearProviderAPIKeys(t)
	r := testRuntime(t)
	if err := r.configure(config.StartupOverrides{Provider: "anthropic", Model: "m", Thinking: "off", ThinkingExplicit: true}); err != nil {
		t.Fatal(err)
	}
	if r.authErr == nil {
		t.Fatal("cold anthropic configure unexpectedly usable")
	}
	r.models = codexAppTestModels(t, "http://127.0.0.1:1")
	writeAppFile(t, filepath.Join(r.agentDir, "auth.json"), `{"openai-codex":{"type":"oauth","access":"default-access","refresh":"default-refresh","expires":9999999999999,"accountId":"default-account"}}`)
	before := r.store.EntryCount()
	kind, text := r.activateCodexLogin()
	if kind != terminal.NoticeInfo || !strings.Contains(text, "Model: openai-codex/gpt-5.6-luna") {
		t.Fatalf("activation = %v %q", kind, text)
	}
	saved, err := r.store.ModelState()
	if err != nil {
		t.Fatal(err)
	}
	live := r.liveSettings()
	if r.authErr != nil || r.store.EntryCount() <= before || saved.Provider != "openai-codex" ||
		saved.ModelID != "gpt-5.6-luna" || saved.ThinkingLevel != "off" ||
		live.Provider != r.prov || live.AuthErr != nil || live.Model.ID != saved.ModelID || live.Thinking != saved.ThinkingLevel {
		t.Fatalf("activation did not persist the selection: saved=%+v live=%s/%s at %s", saved, live.Model.ProviderID(), live.Model.ID, live.Thinking)
	}
}

// A model or thinking persistence failure after the save reports the
// activation failure but never removes the saved credential. L7, L9.
func TestCodexLoginActivationPersistenceFailureKeepsCredential(t *testing.T) {
	clearProviderAPIKeys(t)
	r := testRuntime(t)
	if err := r.configure(config.StartupOverrides{Provider: "anthropic", Model: "m", Thinking: "off", ThinkingExplicit: true}); err != nil {
		t.Fatal(err)
	}
	if r.authErr == nil {
		t.Fatal("cold anthropic configure unexpectedly usable")
	}
	r.models = codexAppTestModels(t, "http://127.0.0.1:1")
	authPath := filepath.Join(r.agentDir, "auth.json")
	writeAppFile(t, authPath, `{"openai-codex":{"type":"oauth","access":"persist-access","refresh":"persist-refresh","expires":9999999999999,"accountId":"persist-account"}}`)
	// Flush a conversation boundary so later model-change appends are
	// durable and observe the injected write failure.
	if _, err := r.store.AppendMessage(agentMessageUser("seed")); err != nil {
		t.Fatal(err)
	}
	r.store.FailWritesForTest(fmt.Errorf("synthetic persistence failure"))
	kind, text := r.activateCodexLogin()
	if kind != terminal.NoticeError || !strings.Contains(text, "session update failed") {
		t.Fatalf("activation = %v %q", kind, text)
	}
	if r.ov.Provider != "anthropic" || r.ov.Model != "m" {
		t.Fatalf("failed persistence moved to %s/%s", r.ov.Provider, r.ov.Model)
	}
	authData, err := os.ReadFile(authPath)
	if err != nil || !strings.Contains(string(authData), "persist-access") {
		t.Fatalf("saved credential was rolled back: %v", err)
	}
}

// A save admitted before cancellation still finishes; settlement reports the
// save without activating a model. L5, L7.
func TestCodexLoginCommitAdmittedSettlesWithoutActivation(t *testing.T) {
	clearProviderAPIKeys(t)
	r := testRuntime(t)
	access := codexAppTestAccess("acct-admitted")
	r.codexTransport = roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.String() != "https://auth.openai.com/oauth/token" {
			return nil, &provider.ProviderError{Kind: provider.ErrInvalid, Message: "unexpected test URL"}
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(codexAppTestTokenBody(access, "admitted-refresh"))), Header: make(http.Header)}, nil
	})
	var bindAddr string
	r.codexBind = func(string) (net.Listener, error) {
		listener, err := net.Listen("tcp4", "127.0.0.1:0")
		if err != nil {
			return nil, err
		}
		bindAddr = listener.Addr().String()
		return listener, nil
	}
	browserURLs := make(chan string, 1)
	r.codexBrowser = func(_ context.Context, authURL string) error {
		browserURLs <- authURL
		return nil
	}
	entered := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	r.codexRename = func(oldpath, newpath string) error {
		once.Do(func() { close(entered) })
		<-release
		return os.Rename(oldpath, newpath)
	}
	if err := r.configure(config.StartupOverrides{Provider: "anthropic", Model: "m", Thinking: "off", ThinkingExplicit: true}); err != nil {
		t.Fatal(err)
	}
	if r.authErr == nil {
		t.Fatal("cold anthropic configure unexpectedly usable")
	}
	_, out, inputWrite, result := startLoginTestREPL(t, r)
	releaseSave := sync.OnceFunc(func() { close(release) })
	t.Cleanup(releaseSave)
	waitAppOutput(t, out, "(anthropic) m")
	if _, err := inputWrite.Write([]byte("/login openai-codex\r\r")); err != nil {
		t.Fatal(err)
	}
	var authURL string
	select {
	case authURL = <-browserURLs:
	case <-time.After(60 * time.Second):
		t.Fatal("browser never opened")
	}
	parsed, err := url.Parse(authURL)
	if err != nil {
		t.Fatal(err)
	}
	if status := deliverAppCodexCallback(t, bindAddr, "state="+parsed.Query().Get("state")+"&code=admitted-code"); status != http.StatusOK {
		t.Fatalf("callback status = %d", status)
	}
	select {
	case <-entered:
	case <-time.After(60 * time.Second):
		t.Fatal("commit never reached rename")
	}
	if _, err := inputWrite.Write([]byte("\x03")); err != nil {
		t.Fatal(err)
	}
	waitAppOutput(t, out, "Cancelling OpenAI Codex sign-in")
	releaseSave()
	waitAppOutput(t, out, "model activation was skipped")
	if err := inputWrite.Close(); err != nil {
		t.Fatal(err)
	}
	if err := <-result; err != nil {
		t.Fatalf("repl: %v", err)
	}
	authData, err := os.ReadFile(filepath.Join(r.agentDir, "auth.json"))
	if err != nil || !strings.Contains(string(authData), access) {
		t.Fatalf("admitted save missing: %v", err)
	}
	if r.ov.Provider != "anthropic" || r.ov.Model != "m" {
		t.Fatalf("cancelled login activated %s/%s", r.ov.Provider, r.ov.Model)
	}
	if r.authErr == nil {
		t.Fatal("cancelled login cleared the prior auth error")
	}
}
