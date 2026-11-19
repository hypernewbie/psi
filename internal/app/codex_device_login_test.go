package app

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"psi/internal/config"
	"psi/internal/provider"
)

// A cold REPL picks device sign-in from the Codex method picker, receives the
// user code in the interactive view, completes a synthetic reply, and saves
// the credential. Device mode never opens a browser or binds a listener. The
// user code is not stored in the session, recall, or bridge state. L1-L4, L7-L9.
func TestCodexDeviceLoginREPL(t *testing.T) {
	clearProviderAPIKeys(t)
	r := testRuntime(t)
	access, refresh := codexAppTestAccess("acct-device-repl"), "device-repl-refresh"
	var starts, polls, exchanges atomic.Int32
	var gotForm url.Values
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Path != "/codex/responses" {
			t.Errorf("model path = %q", req.URL.Path)
			return
		}
		message := map[string]any{"type": "message", "id": "msg_codex_device", "phase": "final_answer", "content": []any{map[string]any{"type": "output_text", "text": "codex-device-reply"}}}
		event := func(typ string, fields map[string]any) {
			fields["type"] = typ
			data, _ := json.Marshal(fields)
			fmt.Fprintf(w, "event: %s\ndata: %s\n\n", typ, data)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		event("response.output_item.added", map[string]any{"output_index": 0, "item": message})
		event("response.output_text.delta", map[string]any{"output_index": 0, "delta": "codex-device-reply"})
		event("response.output_item.done", map[string]any{"output_index": 0, "item": message})
		event("response.completed", map[string]any{"response": map[string]any{"id": "resp_codex_device", "status": "completed", "output": []any{message}}})
	}))
	defer server.Close()
	r.models = codexAppTestModels(t, server.URL)
	r.settings.MaxRetries = 0
	var bindCalled, browserCalled atomic.Bool
	r.codexBind = func(string) (net.Listener, error) {
		bindCalled.Store(true)
		return nil, fmt.Errorf("device mode must not bind")
	}
	r.codexBrowser = func(context.Context, string) error {
		browserCalled.Store(true)
		return fmt.Errorf("device mode must not launch a browser")
	}
	r.codexWait = func(context.Context, time.Duration) error { return nil }
	r.codexTransport = roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		switch req.URL.String() {
		case "https://auth.openai.com/api/accounts/deviceauth/usercode":
			starts.Add(1)
			return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"user_code":"ABCD-EFGH","device_auth_id":"device-1","interval":1}`)), Header: make(http.Header)}, nil
		case "https://auth.openai.com/api/accounts/deviceauth/token":
			polls.Add(1)
			if polls.Load() == 1 {
				return &http.Response{StatusCode: http.StatusForbidden, Body: io.NopCloser(strings.NewReader("")), Header: make(http.Header)}, nil
			}
			return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"authorization_code":"device-app-code","code_verifier":"device-app-verifier"}`)), Header: make(http.Header)}, nil
		case "https://auth.openai.com/oauth/token":
			exchanges.Add(1)
			if err := req.ParseForm(); err != nil {
				return nil, err
			}
			gotForm = req.PostForm
			return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(codexAppTestTokenBody(access, refresh))), Header: make(http.Header)}, nil
		default:
			return nil, &provider.ProviderError{Kind: provider.ErrInvalid, Message: "unexpected test URL"}
		}
	})
	if err := r.configure(config.StartupOverrides{Provider: "openai-codex", Model: "gpt-5.6-luna", Thinking: "off", ThinkingExplicit: true}); err != nil {
		t.Fatal(err)
	}
	if r.authErr == nil {
		t.Fatal("cold Codex configure unexpectedly usable")
	}
	console, out, inputWrite, result := startLoginTestREPL(t, r)
	waitAppOutput(t, out, "gpt-5.6-luna")
	if _, err := inputWrite.Write([]byte("/login openai-codex\r")); err != nil {
		t.Fatal(err)
	}
	waitAppOutput(t, out, "Device code login (headless)")
	if _, err := inputWrite.Write([]byte("\x1b[B\r")); err != nil {
		t.Fatal(err)
	}
	waitAppOutput(t, out, "ABCD-EFGH")
	waitAppOutput(t, out, "https://auth.openai.com/codex/device")
	waitAppOutput(t, out, "Signed in to OpenAI Codex. Model: openai-codex/gpt-5.6-luna")
	if _, err := inputWrite.Write([]byte("Say PSI_CODEX_OK\r")); err != nil {
		t.Fatal(err)
	}
	waitAppOutput(t, out, "codex-device-reply")
	if err := inputWrite.Close(); err != nil {
		t.Fatal(err)
	}
	if err := <-result; err != nil {
		t.Fatalf("repl: %v", err)
	}
	if starts.Load() != 1 || polls.Load() != 2 || exchanges.Load() != 1 {
		t.Fatalf("starts=%d polls=%d exchanges=%d", starts.Load(), polls.Load(), exchanges.Load())
	}
	if bindCalled.Load() || browserCalled.Load() {
		t.Fatalf("bind=%v browser=%v", bindCalled.Load(), browserCalled.Load())
	}
	if gotForm.Get("redirect_uri") != "https://auth.openai.com/deviceauth/callback" ||
		gotForm.Get("code") != "device-app-code" || gotForm.Get("code_verifier") != "device-app-verifier" {
		t.Fatalf("exchange form = %v", gotForm)
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
		entry["refresh"] != refresh || entry["accountId"] != "acct-device-repl" {
		t.Fatalf("stored entry = %v", entry)
	}
	sessionData, err := os.ReadFile(r.store.File())
	if err != nil {
		t.Fatal(err)
	}
	sessionText := string(sessionData)
	if !strings.Contains(sessionText, "Say PSI_CODEX_OK") || !strings.Contains(sessionText, "codex-device-reply") {
		t.Fatalf("prompt or reply missing from the session: %s", sessionText)
	}
	for _, transient := range []string{"ABCD-EFGH", "device-app-code", "device-app-verifier", access, refresh} {
		if strings.Contains(sessionText, transient) {
			t.Fatalf("session contains %q", transient)
		}
		for _, recalled := range console.Editor.History() {
			if strings.Contains(recalled, transient) {
				t.Fatalf("recall contains %q in %q", transient, recalled)
			}
		}
	}
	if r.bridge != nil {
		t.Fatalf("bridge was started for a device login: %+v", r.bridge)
	}
}
