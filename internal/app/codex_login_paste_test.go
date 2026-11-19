package app

import (
	"context"
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
	"psi/internal/terminal"
)

// codexReplyTestModels points openai-codex at a local Responses endpoint
// that returns reply after each completed prompt.
func codexReplyTestModels(t *testing.T, reply string) (*httptest.Server, *config.ModelsFile) {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Path != "/codex/responses" {
			t.Errorf("model path = %q", req.URL.Path)
			return
		}
		message := map[string]any{"type": "message", "id": "msg_" + reply, "phase": "final_answer", "content": []any{map[string]any{"type": "output_text", "text": reply}}}
		event := func(typ string, fields map[string]any) {
			fields["type"] = typ
			data, _ := json.Marshal(fields)
			fmt.Fprintf(w, "event: %s\ndata: %s\n\n", typ, data)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		event("response.output_item.added", map[string]any{"output_index": 0, "item": message})
		event("response.output_text.delta", map[string]any{"output_index": 0, "delta": reply})
		event("response.output_item.done", map[string]any{"output_index": 0, "item": message})
		event("response.completed", map[string]any{"response": map[string]any{"id": "resp_" + reply, "status": "completed", "output": []any{message}}})
	}))
	return server, codexAppTestModels(t, server.URL)
}

func startCodexInputREPL(t *testing.T, r *runtime, wantErr error) (*terminal.Console, *notifyingBuffer, chan terminal.Input, func()) {
	t.Helper()
	input, inputWrite, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	out := &notifyingBuffer{notify: make(chan struct{}, 1)}
	console := terminal.NewConsole(input, out)
	console.Pause()
	console.SizeFunc = func() (int, int) { return 80, 24 }
	inputs := make(chan terminal.Input)
	console.Inputs = inputs
	result := make(chan error, 1)
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		result <- r.repl(console)
	}()
	var stopOnce sync.Once
	stop := func() {
		stopOnce.Do(func() {
			_ = inputWrite.Close()
			close(inputs)
			defer input.Close()
			select {
			case <-finished:
				console.Close()
				if err := <-result; !errors.Is(err, wantErr) {
					t.Errorf("repl error = %v, want %v", err, wantErr)
				}
			case <-time.After(5 * time.Second):
				t.Error("REPL did not stop after input closed")
			}
		})
	}
	t.Cleanup(stop)
	return console, out, inputs, stop
}

func sendCodexInput(t *testing.T, inputs chan terminal.Input, input terminal.Input) {
	t.Helper()
	select {
	case inputs <- input:
	case <-time.After(60 * time.Second):
		t.Fatal("repl stopped receiving inputs")
	}
}

func waitCodexPasteField(t *testing.T, out *notifyingBuffer) {
	t.Helper()
	waitAppOutput(t, out, "paste redirect URL")
}

func startCodexPasteLogin(t *testing.T, r *runtime, reply string) (*terminal.Console, *notifyingBuffer, chan terminal.Input, string, string, func()) {
	t.Helper()
	clearProviderAPIKeys(t)
	server, models := codexReplyTestModels(t, reply)
	t.Cleanup(server.Close)
	r.models = models
	r.settings.MaxRetries = 0
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
	if err := r.configure(config.StartupOverrides{Provider: "openai-codex", Model: "gpt-5.6-luna", Thinking: "off", ThinkingExplicit: true}); err != nil {
		t.Fatal(err)
	}
	if r.authErr == nil {
		t.Fatal("cold Codex configure unexpectedly usable")
	}
	console, out, inputs, stop := startCodexInputREPL(t, r, nil)
	waitAppOutput(t, out, "gpt-5.6-luna")
	sendCodexInput(t, inputs, terminal.Input{Key: []byte("/login openai-codex")})
	sendCodexInput(t, inputs, terminal.Input{Key: []byte("\r")})
	waitAppOutput(t, out, "Browser login (default)")
	sendCodexInput(t, inputs, terminal.Input{Key: []byte("\r")})
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
	waitCodexPasteField(t, out)
	return console, out, inputs, bindAddr, parsed.Query().Get("state"), stop
}

// Masked manual paste delivers the callback URL without echo. The code is
// exchanged once and a normal prompt completes after settlement. L1-L9.
func TestCodexLoginManualPasteREPL(t *testing.T) {
	r := testRuntime(t)
	var exchanges atomic.Int32
	var gotForm url.Values
	r.codexTransport = roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.String() != "https://auth.openai.com/oauth/token" {
			return nil, fmt.Errorf("unexpected URL %s", req.URL)
		}
		exchanges.Add(1)
		if err := req.ParseForm(); err != nil {
			return nil, err
		}
		gotForm = req.PostForm
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(codexAppTestTokenBody(codexAppTestAccess("acct-manual-app"), "manual-app-refresh"))), Header: make(http.Header)}, nil
	})
	console, out, inputs, bindAddr, state, stop := startCodexPasteLogin(t, r, "manual-paste-reply")
	paste := "http://" + bindAddr + "/auth/callback?code=manual-app-code&state=" + state
	sendCodexInput(t, inputs, terminal.Input{Paste: true, PasteStart: true})
	sendCodexInput(t, inputs, terminal.Input{Paste: true, Key: []byte(paste)})
	sendCodexInput(t, inputs, terminal.Input{Paste: true, PasteEnd: true})
	sendCodexInput(t, inputs, terminal.Input{Key: []byte("\r")})
	waitAppOutput(t, out, "Signed in to OpenAI Codex. Model: openai-codex/gpt-5.6-luna")
	sendCodexInput(t, inputs, terminal.Input{Key: []byte("Say PSI_CODEX_OK")})
	sendCodexInput(t, inputs, terminal.Input{Key: []byte("\r")})
	waitAppOutput(t, out, "manual-paste-reply")
	stop()
	if exchanges.Load() != 1 || gotForm.Get("code") != "manual-app-code" ||
		gotForm.Get("redirect_uri") != "http://"+bindAddr+"/auth/callback" {
		t.Fatalf("exchanges=%d form=%v", exchanges.Load(), gotForm)
	}
	sessionData, err := os.ReadFile(r.store.File())
	if err != nil {
		t.Fatal(err)
	}
	sessionText := string(sessionData)
	if !strings.Contains(sessionText, "Say PSI_CODEX_OK") || !strings.Contains(sessionText, "manual-paste-reply") {
		t.Fatalf("prompt or reply missing: %s", sessionText)
	}
	for _, secret := range []string{"manual-app-code", state, paste} {
		if strings.Contains(sessionText, secret) {
			t.Fatalf("session contains %q", secret)
		}
		for _, recalled := range console.Editor.History() {
			if strings.Contains(recalled, secret) {
				t.Fatalf("recall contains %q in %q", secret, recalled)
			}
		}
	}
	if strings.Contains(out.String(), "manual-app-code") {
		t.Fatal("pasted value reached terminal output")
	}
}

// A callback that wins during a paste wipes the masked draft and discards the
// rest of the transaction. The next normal prompt still completes. L5-L9.
func TestCodexLoginCallbackDuringPasteREPL(t *testing.T) {
	r := testRuntime(t)
	var exchanges atomic.Int32
	var gotForm url.Values
	r.codexTransport = roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.String() != "https://auth.openai.com/oauth/token" {
			return nil, fmt.Errorf("unexpected URL %s", req.URL)
		}
		exchanges.Add(1)
		if err := req.ParseForm(); err != nil {
			return nil, err
		}
		gotForm = req.PostForm
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(codexAppTestTokenBody(codexAppTestAccess("acct-callback-paste"), "callback-paste-refresh"))), Header: make(http.Header)}, nil
	})
	_, out, inputs, bindAddr, state, stop := startCodexPasteLogin(t, r, "after-paste-reply")
	sendCodexInput(t, inputs, terminal.Input{Paste: true, PasteStart: true})
	sendCodexInput(t, inputs, terminal.Input{Paste: true, Key: []byte("PASTE-SENTINEL")})
	if status := deliverAppCodexCallback(t, bindAddr, "state="+state+"&code=callback-paste-code"); status != http.StatusOK {
		t.Fatalf("callback status = %d", status)
	}
	waitAppOutput(t, out, "Signed in to OpenAI Codex. Model: openai-codex/gpt-5.6-luna")
	sendCodexInput(t, inputs, terminal.Input{Paste: true, Key: []byte("REST-SENTINEL")})
	sendCodexInput(t, inputs, terminal.Input{Paste: true, PasteEnd: true})
	sendCodexInput(t, inputs, terminal.Input{Key: []byte("Say PSI_CODEX_OK")})
	sendCodexInput(t, inputs, terminal.Input{Key: []byte("\r")})
	waitAppOutput(t, out, "after-paste-reply")
	stop()
	if exchanges.Load() != 1 || gotForm.Get("code") != "callback-paste-code" {
		t.Fatalf("exchanges=%d form=%v", exchanges.Load(), gotForm)
	}
	sessionData, err := os.ReadFile(r.store.File())
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"PASTE-SENTINEL", "REST-SENTINEL", "callback-paste-code", state} {
		if strings.Contains(string(sessionData), secret) {
			t.Fatalf("session contains %q", secret)
		}
	}
	for _, secret := range []string{"PASTE-SENTINEL", "REST-SENTINEL", "callback-paste-code"} {
		if strings.Contains(out.String(), secret) {
			t.Fatalf("output contains %q", secret)
		}
	}
}

// Cancelling during a paste wipes the draft, discards the rest through
// PasteEnd, and leaves a usable prompt. No credential is written. L5, L6.
func TestCodexLoginCancelDuringPasteREPL(t *testing.T) {
	r := testRuntime(t)
	var exchanges atomic.Int32
	r.codexTransport = roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		exchanges.Add(1)
		return nil, fmt.Errorf("unexpected exchange")
	})
	_, out, inputs, _, _, _ := startCodexPasteLogin(t, r, "cancel-paste-reply")
	sendCodexInput(t, inputs, terminal.Input{Paste: true, PasteStart: true})
	sendCodexInput(t, inputs, terminal.Input{Paste: true, Key: []byte("CANCEL-SENTINEL")})
	sendCodexInput(t, inputs, terminal.Input{Key: []byte{3}})
	waitAppOutput(t, out, "OpenAI Codex sign-in cancelled")
	sendCodexInput(t, inputs, terminal.Input{Paste: true, PasteEnd: true})
	// The input after PasteEnd must reach the normal editor, not the
	// discarded paste transaction.
	sendCodexInput(t, inputs, terminal.Input{Key: []byte("/login")})
	sendCodexInput(t, inputs, terminal.Input{Key: []byte("\r")})
	waitAppOutput(t, out, "Providers")
	if exchanges.Load() != 0 {
		t.Fatalf("cancelled paste exchanged %d times", exchanges.Load())
	}
	if _, err := os.Stat(filepath.Join(r.agentDir, "auth.json")); !os.IsNotExist(err) {
		t.Fatalf("cancelled paste wrote credentials: %v", err)
	}
	if strings.Contains(out.String(), "CANCEL-SENTINEL") {
		t.Fatal("cancelled paste echoed its secret")
	}
}

// A callback can settle before the REPL processes a paste event that was
// already queued. Settlement speculatively suppresses the next paste through
// PasteEnd, so no chunk reaches chat, and a later prompt still completes.
func TestCodexLoginCallbackBeforeQueuedPasteREPL(t *testing.T) {
	r := testRuntime(t)
	var exchanges atomic.Int32
	var gotForm url.Values
	r.codexTransport = roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.String() != "https://auth.openai.com/oauth/token" {
			return nil, fmt.Errorf("unexpected URL %s", req.URL)
		}
		exchanges.Add(1)
		if err := req.ParseForm(); err != nil {
			return nil, err
		}
		gotForm = req.PostForm
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(codexAppTestTokenBody(codexAppTestAccess("acct-queued-paste"), "queued-paste-refresh"))), Header: make(http.Header)}, nil
	})
	_, out, inputs, bindAddr, state, stop := startCodexPasteLogin(t, r, "queued-paste-reply")
	if status := deliverAppCodexCallback(t, bindAddr, "state="+state+"&code=queued-paste-code"); status != http.StatusOK {
		t.Fatalf("callback status = %d", status)
	}
	waitAppOutput(t, out, "Signed in to OpenAI Codex. Model: openai-codex/gpt-5.6-luna")
	sendCodexInput(t, inputs, terminal.Input{Paste: true, PasteStart: true})
	sendCodexInput(t, inputs, terminal.Input{Paste: true, Key: []byte("QUEUED-SENTINEL")})
	sendCodexInput(t, inputs, terminal.Input{Paste: true, PasteEnd: true})
	sendCodexInput(t, inputs, terminal.Input{Key: []byte("Say PSI_CODEX_OK")})
	sendCodexInput(t, inputs, terminal.Input{Key: []byte("\r")})
	waitAppOutput(t, out, "queued-paste-reply")
	stop()
	if exchanges.Load() != 1 || gotForm.Get("code") != "queued-paste-code" {
		t.Fatalf("exchanges=%d form=%v", exchanges.Load(), gotForm)
	}
	sessionData, err := os.ReadFile(r.store.File())
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"QUEUED-SENTINEL", "queued-paste-code", state} {
		if strings.Contains(string(sessionData), secret) {
			t.Fatalf("session contains %q", secret)
		}
	}
	if strings.Contains(out.String(), "QUEUED-SENTINEL") {
		t.Fatal("queued paste echoed its secret")
	}
}
