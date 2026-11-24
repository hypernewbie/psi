package app

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/hypernewbie/psi/internal/config"
	"github.com/hypernewbie/psi/internal/provider"
	"github.com/hypernewbie/psi/internal/terminal"
)

// waitAppOutput waits for want in the console output. The watchdog only
// fires when the app wedges; it never asserts performance.
func waitAppOutput(t *testing.T, out *notifyingBuffer, want string) {
	t.Helper()
	deadline := time.After(60 * time.Second)
	for !strings.Contains(out.String(), want) {
		select {
		case <-out.notify:
		case <-deadline:
			t.Fatalf("timed out waiting for %q", want)
		}
	}
}

// deliverAppLoginCallback sends one callback to the test listener with the
// unchanged production Host authority.
func deliverAppLoginCallback(t *testing.T, addr, query string) int {
	t.Helper()
	client := &http.Client{Transport: &http.Transport{Proxy: nil}, Timeout: 5 * time.Second}
	defer client.CloseIdleConnections()
	req, err := http.NewRequest(http.MethodGet, "http://"+addr+"/auth/callback?"+query, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Host = "127.0.0.1:1455"
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.StatusCode
}

func openAILoginTestModels(t *testing.T) *config.ModelsFile {
	t.Helper()
	models, err := config.LoadModels(filepath.Join(t.TempDir(), "models.json"))
	if err != nil {
		t.Fatal(err)
	}
	if models.Providers == nil {
		models.Providers = map[string]config.ProviderMeta{}
	}
	return models
}

func startLoginTestREPL(t *testing.T, r *runtime) (*terminal.Console, *notifyingBuffer, *os.File, chan error) {
	t.Helper()
	return startLoginTestREPLWidth(t, r, 80)
}

func startLoginTestREPLWidth(t *testing.T, r *runtime, width int) (*terminal.Console, *notifyingBuffer, *os.File, chan error) {
	t.Helper()
	return startLoginTestREPLSize(t, r, width, 24)
}

// startLoginTestREPLSize sets an explicit terminal size before the REPL
// starts, so panel row budgets are deterministic.
func startLoginTestREPLSize(t *testing.T, r *runtime, width, height int) (*terminal.Console, *notifyingBuffer, *os.File, chan error) {
	t.Helper()
	input, inputWrite, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	out := &notifyingBuffer{notify: make(chan struct{}, 1)}
	console := terminal.NewConsole(input, out)
	console.SizeFunc = func() (int, int) { return width, height }
	result := make(chan error, 1)
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		err := r.repl(console)
		console.Close()
		_ = input.Close()
		result <- err
	}()
	t.Cleanup(func() {
		_ = inputWrite.Close()
		select {
		case <-finished:
		case <-time.After(5 * time.Second):
			t.Error("REPL did not stop after input closed")
		}
	})
	return console, out, inputWrite, result
}

// A cold interactive start selects OpenAI from the bare /login picker.
// The real login operation signs in through the injected listener, transport,
// and browser, then one prompt completes through the configured Responses
// client. The login command never becomes a chat message, and the
// authorization URL, code, and tokens never enter chat or recall. L1, L3,
// L7-L10.
func TestOpenAILoginColdStartThroughREPL(t *testing.T) {
	clearProviderAPIKeys(t)
	r := testRuntime(t)
	r.models = openAILoginTestModels(t)
	r.settings.MaxRetries = 0
	var mu sync.Mutex
	var exchanges, posts atomic.Int32
	var bearers []string
	var bodies []map[string]any
	r.openAITransport = roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		switch req.URL.String() {
		case "https://auth.openai.com/api/accounts/oauth/token":
			exchanges.Add(1)
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"access_token":"cold-access","refresh_token":"cold-refresh","scope":"openid chatgpt.tokens.use.direct","expires_in":3600,"id_token":"cold-id-token"}`)), Header: make(http.Header)}, nil
		case "https://api.openai.com/v1/responses":
			posts.Add(1)
			data, _ := io.ReadAll(req.Body)
			var decoded map[string]any
			_ = json.Unmarshal(data, &decoded)
			mu.Lock()
			bearers = append(bearers, req.Header.Get("Authorization"))
			bodies = append(bodies, decoded)
			mu.Unlock()
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(openAISyntheticStream("cold-openai-reply"))), Header: make(http.Header)}, nil
		default:
			return nil, &provider.ProviderError{Kind: provider.ErrInvalid, Message: "unexpected test URL"}
		}
	})
	var bindAddr string
	r.openAIBind = func() (net.Listener, error) {
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			return nil, err
		}
		bindAddr = listener.Addr().String()
		return listener, nil
	}
	browserURLs := make(chan string, 1)
	r.openAIBrowser = func(_ context.Context, authURL string) error {
		browserURLs <- authURL
		return nil
	}
	if err := r.configure(config.StartupOverrides{Provider: "openai", Model: "gpt-6.1-sol", Thinking: "off", ThinkingExplicit: true}); err != nil {
		t.Fatal(err)
	}
	if r.authErr == nil {
		t.Fatal("cold OpenAI configure unexpectedly usable")
	}
	console, out, inputWrite, result := startLoginTestREPL(t, r)
	waitAppOutput(t, out, "gpt-6.1-sol")
	if _, err := inputWrite.Write([]byte("/login\r")); err != nil {
		t.Fatal(err)
	}
	waitAppOutput(t, out, "Providers ·")
	if _, err := inputWrite.Write([]byte("openai\r")); err != nil {
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
	state := parsed.Query().Get("state")
	if state == "" {
		t.Fatalf("no state in %q", authURL)
	}
	if status := deliverAppLoginCallback(t, bindAddr, "state="+state+"&code=cold-code&client_id=cold-client"); status != 200 {
		t.Fatalf("callback status = %d", status)
	}
	waitAppOutput(t, out, "Signed in to ChatGPT. Model: openai/gpt-6.1-sol")
	if _, err := inputWrite.Write([]byte("Say PSI_OPENAI_OK\r")); err != nil {
		t.Fatal(err)
	}
	waitAppOutput(t, out, "cold-openai-reply")
	if err := inputWrite.Close(); err != nil {
		t.Fatal(err)
	}
	if err := <-result; err != nil {
		t.Fatalf("repl: %v", err)
	}
	if r.authErr != nil {
		t.Fatalf("authErr after login: %v", r.authErr)
	}
	if r.ov.Provider != "openai" || r.ov.Model != "gpt-6.1-sol" {
		t.Fatalf("selection = %s/%s", r.ov.Provider, r.ov.Model)
	}
	if exchanges.Load() != 1 || posts.Load() != 1 {
		t.Fatalf("exchanges=%d posts=%d", exchanges.Load(), posts.Load())
	}
	mu.Lock()
	defer mu.Unlock()
	if len(bearers) != 1 || bearers[0] != "Bearer cold-access" {
		t.Fatalf("bearers = %q", bearers)
	}
	if len(bodies) != 1 || bodies[0]["store"] != false {
		t.Fatalf("bodies = %v", bodies)
	}
	if _, hasLimit := bodies[0]["max_output_tokens"]; hasLimit {
		t.Fatal("subscription request kept max_output_tokens")
	}
	authData, err := os.ReadFile(filepath.Join(r.agentDir, "auth.json"))
	if err != nil {
		t.Fatal(err)
	}
	var entries map[string]map[string]any
	if err := json.Unmarshal(authData, &entries); err != nil {
		t.Fatal(err)
	}
	entry, ok := entries["openai"]
	if !ok || len(entry) != 6 || entry["type"] != "oauth" || entry["access"] != "cold-access" || entry["clientId"] != "cold-client" {
		t.Fatalf("stored entry = %v", entries["openai"])
	}
	sessionData, err := os.ReadFile(r.store.File())
	if err != nil {
		t.Fatal(err)
	}
	sessionText := string(sessionData)
	if !strings.Contains(sessionText, "Say PSI_OPENAI_OK") || !strings.Contains(sessionText, "cold-openai-reply") {
		t.Fatalf("prompt or reply missing from the session: %s", sessionText)
	}
	for _, secret := range []string{"/login openai", "cold-code", "cold-access", "cold-refresh", "cold-id-token", state, "auth.openai.com", "127.0.0.1:1455"} {
		if strings.Contains(sessionText, secret) {
			t.Fatalf("session contains %q", secret)
		}
	}
	for _, recalled := range console.Editor.History() {
		for _, secret := range []string{"http", "cold-code", "cold-access", "cold-refresh", state} {
			if strings.Contains(recalled, secret) {
				t.Fatalf("recall contains %q in %q", secret, recalled)
			}
		}
	}
}

// An unknown login provider stays local: no browser, credential, or chat
// message.
func TestOpenAILoginUsageStaysLocal(t *testing.T) {
	for _, line := range []string{"/login bogus", "/login openai extra"} {
		t.Run(line, func(t *testing.T) {
			clearProviderAPIKeys(t)
			r := testRuntime(t)
			r.models = openAILoginTestModels(t)
			r.settings.MaxRetries = 0
			r.openAITransport = roundTripperFunc(func(req *http.Request) (*http.Response, error) {
				return nil, &provider.ProviderError{Kind: provider.ErrInvalid, Message: "unexpected test URL"}
			})
			r.openAIBind = func() (net.Listener, error) {
				t.Error("listener bound for a usage error")
				return net.Listen("tcp", "127.0.0.1:0")
			}
			r.openAIBrowser = func(_ context.Context, _ string) error {
				t.Error("browser opened for a usage error")
				return nil
			}
			if err := r.configure(config.StartupOverrides{Provider: "openai", Model: "gpt-6.1-sol", Thinking: "off", ThinkingExplicit: true}); err != nil {
				t.Fatal(err)
			}
			_, out, inputWrite, result := startLoginTestREPL(t, r)
			waitAppOutput(t, out, "gpt-6.1-sol")
			if _, err := inputWrite.Write([]byte(line + "\r")); err != nil {
				t.Fatal(err)
			}
			want := "unknown provider"
			if line == "/login openai extra" {
				want = "use /login [provider]"
			}
			waitAppOutput(t, out, want)
			if err := inputWrite.Close(); err != nil {
				t.Fatal(err)
			}
			if err := <-result; err != nil {
				t.Fatalf("repl: %v", err)
			}
			if _, err := os.Stat(filepath.Join(r.agentDir, "auth.json")); !os.IsNotExist(err) {
				t.Fatalf("usage error wrote %v", err)
			}
			// Nothing is stored, so the session file may not exist yet.
			if sessionData, err := os.ReadFile(r.store.File()); err == nil {
				if strings.Contains(string(sessionData), "/login") {
					t.Fatalf("usage error entered the session: %s", sessionData)
				}
			} else if !os.IsNotExist(err) {
				t.Fatal(err)
			}
		})
	}
}

// A /login typed during an active run stays local and restored: it is
// never steered as prompt text. L1, L6.
func TestOpenAILoginDuringRunRestored(t *testing.T) {
	clearProviderAPIKeys(t)
	r := testRuntime(t)
	r.models = openAILoginTestModels(t)
	r.settings.MaxRetries = 0
	authPath := filepath.Join(r.agentDir, "auth.json")
	writeAppFile(t, authPath, `{"openai":{"type":"oauth","access":"busy-access","refresh":"busy-refresh","expires":9999999999999,"clientId":"busy-client","scopes":["chatgpt.tokens.use.direct"]}}`)
	var posts atomic.Int32
	entered := make(chan struct{})
	release := make(chan struct{})
	var once atomic.Bool
	r.openAITransport = roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.String() != "https://api.openai.com/v1/responses" {
			return nil, &provider.ProviderError{Kind: provider.ErrInvalid, Message: "unexpected test URL"}
		}
		posts.Add(1)
		if once.CompareAndSwap(false, true) {
			close(entered)
		}
		<-release
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(openAISyntheticStream("busy-reply"))), Header: make(http.Header)}, nil
	})
	r.openAIBind = func() (net.Listener, error) {
		t.Error("listener bound during a run")
		return net.Listen("tcp", "127.0.0.1:0")
	}
	browserURLs := make(chan string, 1)
	r.openAIBrowser = func(_ context.Context, authURL string) error {
		browserURLs <- authURL
		return nil
	}
	if err := r.configure(config.StartupOverrides{Provider: "openai", Model: "gpt-6.1-sol", Thinking: "off", ThinkingExplicit: true}); err != nil {
		t.Fatal(err)
	}
	if r.authErr != nil {
		t.Fatalf("authErr: %v", r.authErr)
	}
	console, out, inputWrite, result := startLoginTestREPL(t, r)
	finishRun := sync.OnceFunc(func() { close(release) })
	t.Cleanup(finishRun)
	waitAppOutput(t, out, "gpt-6.1-sol")
	if _, err := inputWrite.Write([]byte("hi\r")); err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-time.After(60 * time.Second):
		t.Fatal("run never started")
	}
	// Ctrl+O is handled after the login command. Its status confirms the
	// REPL processed the command before the fake model run can finish.
	if _, err := inputWrite.Write([]byte("/login openai\r\x0f")); err != nil {
		t.Fatal(err)
	}
	waitAppOutput(t, out, "Skill and web cards:")
	finishRun()
	waitAppOutput(t, out, "busy-reply")
	if err := inputWrite.Close(); err != nil {
		t.Fatal(err)
	}
	if err := <-result; err != nil {
		t.Fatalf("repl: %v", err)
	}
	select {
	case unexpected := <-browserURLs:
		t.Fatalf("login started during a run: %q", unexpected)
	default:
	}
	if posts.Load() != 1 {
		t.Fatalf("posts = %d, want 1 without steering", posts.Load())
	}
	if console.Editor.Text() != "/login openai" {
		t.Fatalf("draft = %q, want the restored command", console.Editor.Text())
	}
	sessionData, err := os.ReadFile(r.store.File())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(sessionData), "/login openai") {
		t.Fatalf("login command entered the session: %s", sessionData)
	}
}

// A pending login ignores ordinary input; Esc cancels it; a second login
// then succeeds with fresh secrets. L1, L5, L6.
func TestOpenAILoginCancelThenRetry(t *testing.T) {
	clearProviderAPIKeys(t)
	r := testRuntime(t)
	r.models = openAILoginTestModels(t)
	r.settings.MaxRetries = 0
	r.openAITransport = roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.String() != "https://auth.openai.com/api/accounts/oauth/token" {
			return nil, &provider.ProviderError{Kind: provider.ErrInvalid, Message: "unexpected test URL"}
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"access_token":"retry-access","refresh_token":"retry-refresh","scope":"openid chatgpt.tokens.use.direct","expires_in":3600,"id_token":"retry-id-token"}`)), Header: make(http.Header)}, nil
	})
	var bindAddr string
	r.openAIBind = func() (net.Listener, error) {
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			return nil, err
		}
		bindAddr = listener.Addr().String()
		return listener, nil
	}
	browserURLs := make(chan string, 2)
	r.openAIBrowser = func(_ context.Context, authURL string) error {
		browserURLs <- authURL
		return nil
	}
	if err := r.configure(config.StartupOverrides{Provider: "openai", Model: "gpt-6.1-sol", Thinking: "off", ThinkingExplicit: true}); err != nil {
		t.Fatal(err)
	}
	_, out, inputWrite, result := startLoginTestREPL(t, r)
	waitAppOutput(t, out, "gpt-6.1-sol")
	if _, err := inputWrite.Write([]byte("/login openai\r")); err != nil {
		t.Fatal(err)
	}
	var firstURL, secondURL string
	select {
	case firstURL = <-browserURLs:
	case <-time.After(60 * time.Second):
		t.Fatal("first browser open never happened")
	}
	if _, err := inputWrite.Write([]byte("hello-during-login\r")); err != nil {
		t.Fatal(err)
	}
	if _, err := inputWrite.Write([]byte("\x03")); err != nil {
		t.Fatal(err)
	}
	waitAppOutput(t, out, "ChatGPT sign-in cancelled.")
	if _, err := os.Stat(filepath.Join(r.agentDir, "auth.json")); !os.IsNotExist(err) {
		t.Fatalf("cancelled login wrote %v", err)
	}
	if _, err := inputWrite.Write([]byte("/login openai\r")); err != nil {
		t.Fatal(err)
	}
	select {
	case secondURL = <-browserURLs:
	case <-time.After(60 * time.Second):
		t.Fatal("second browser open never happened")
	}
	first, _ := url.Parse(firstURL)
	second, _ := url.Parse(secondURL)
	if first.Query().Get("state") == second.Query().Get("state") {
		t.Fatal("retry reused the cancelled state")
	}
	if status := deliverAppLoginCallback(t, bindAddr, "state="+second.Query().Get("state")+"&code=retry-code&client_id=retry-client"); status != 200 {
		t.Fatalf("callback status = %d", status)
	}
	waitAppOutput(t, out, "Signed in to ChatGPT. Model: openai/gpt-6.1-sol")
	if err := inputWrite.Close(); err != nil {
		t.Fatal(err)
	}
	if err := <-result; err != nil {
		t.Fatalf("repl: %v", err)
	}
	sessionData, err := os.ReadFile(r.store.File())
	if err != nil {
		if os.IsNotExist(err) {
			return
		}
		t.Fatal(err)
	}
	if strings.Contains(string(sessionData), "hello-during-login") || strings.Contains(string(sessionData), "/login openai") {
		t.Fatalf("ignored input entered the session: %s", sessionData)
	}
}

// Ctrl+P and Ctrl+T have no effect during a pending login and work again
// after cancel. Two usable models make a stray cycle observable: the final
// selection proves exactly one cycle ran. L6.
func TestOpenAILoginHotkeysSuppressed(t *testing.T) {
	clearProviderAPIKeys(t)
	r := testRuntime(t)
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-synthetic-hotkey")
	providerMeta := r.models.Providers["anthropic"]
	providerMeta.Models = append(providerMeta.Models, config.ModelMeta{ID: "m2", Reasoning: true, MaxTokens: 16384})
	r.models.Providers["anthropic"] = providerMeta
	r.settings.MaxRetries = 0
	r.openAITransport = roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		return nil, &provider.ProviderError{Kind: provider.ErrInvalid, Message: "unexpected test URL"}
	})
	r.openAIBind = func() (net.Listener, error) { return net.Listen("tcp", "127.0.0.1:0") }
	started := make(chan struct{})
	var once atomic.Bool
	r.openAIBrowser = func(ctx context.Context, _ string) error {
		if once.CompareAndSwap(false, true) {
			close(started)
		}
		<-ctx.Done()
		return ctx.Err()
	}
	if err := r.configure(config.StartupOverrides{Provider: "anthropic", Model: "m", Thinking: "off", ThinkingExplicit: true}); err != nil {
		t.Fatal(err)
	}
	if r.authErr != nil {
		t.Fatalf("authErr: %v", r.authErr)
	}
	_, out, inputWrite, result := startLoginTestREPL(t, r)
	waitAppOutput(t, out, "(anthropic) m")
	if _, err := inputWrite.Write([]byte("/login openai\r")); err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(60 * time.Second):
		t.Fatal("login never reached the browser")
	}
	if _, err := inputWrite.Write([]byte("\x10\x14")); err != nil {
		t.Fatal(err)
	}
	if _, err := inputWrite.Write([]byte("\x1b")); err != nil {
		t.Fatal(err)
	}
	waitAppOutput(t, out, "ChatGPT sign-in cancelled.")
	if _, err := inputWrite.Write([]byte("\x10")); err != nil {
		t.Fatal(err)
	}
	waitAppOutput(t, out, "Switched to m2")
	if _, err := inputWrite.Write([]byte("\x14")); err != nil {
		t.Fatal(err)
	}
	waitAppOutput(t, out, "Thinking blocks:")
	if err := inputWrite.Close(); err != nil {
		t.Fatal(err)
	}
	if err := <-result; err != nil {
		t.Fatalf("repl: %v", err)
	}
	if r.ov.Model != "m2" {
		t.Fatalf("model = %q, want exactly one post-cancel cycle to m2", r.ov.Model)
	}
	if !r.settings.HideThinkingBlock {
		t.Fatal("Ctrl+T toggle lost")
	}
	if _, err := os.Stat(filepath.Join(r.agentDir, "auth.json")); !os.IsNotExist(err) {
		t.Fatalf("cancelled login wrote %v", err)
	}
}

// A paste that starts under login mode stays suppressed through its
// PasteEnd even when the login settles in the middle. Inputs ride an
// injected channel, so each send proves the REPL received the marker. L1.
func TestOpenAILoginSplitPasteSuppressed(t *testing.T) {
	clearProviderAPIKeys(t)
	r := testRuntime(t)
	r.models = openAILoginTestModels(t)
	r.settings.MaxRetries = 0
	r.openAITransport = roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		switch req.URL.String() {
		case "https://auth.openai.com/api/accounts/oauth/token":
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"access_token":"paste-access","refresh_token":"paste-refresh","scope":"openid chatgpt.tokens.use.direct","expires_in":3600,"id_token":"paste-id-token"}`)), Header: make(http.Header)}, nil
		case "https://api.openai.com/v1/responses":
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(openAISyntheticStream("paste-reply"))), Header: make(http.Header)}, nil
		default:
			return nil, &provider.ProviderError{Kind: provider.ErrInvalid, Message: "unexpected test URL"}
		}
	})
	var bindAddr string
	r.openAIBind = func() (net.Listener, error) {
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			return nil, err
		}
		bindAddr = listener.Addr().String()
		return listener, nil
	}
	browserURLs := make(chan string, 1)
	r.openAIBrowser = func(_ context.Context, authURL string) error {
		browserURLs <- authURL
		return nil
	}
	if err := r.configure(config.StartupOverrides{Provider: "openai", Model: "gpt-6.1-sol", Thinking: "off", ThinkingExplicit: true}); err != nil {
		t.Fatal(err)
	}
	input, inputWrite, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	out := &notifyingBuffer{notify: make(chan struct{}, 1)}
	console := terminal.NewConsole(input, out)
	console.SizeFunc = func() (int, int) { return 80, 24 }
	inputs := make(chan terminal.Input)
	console.Inputs = inputs
	result := make(chan error, 1)
	go func() { result <- r.repl(console) }()
	defer func() {
		_ = inputWrite.Close()
		console.Close()
		_ = input.Close()
	}()
	send := func(in terminal.Input) {
		t.Helper()
		select {
		case inputs <- in:
		case <-time.After(60 * time.Second):
			t.Fatal("repl stopped receiving inputs")
		}
	}
	waitAppOutput(t, out, "gpt-6.1-sol")
	send(terminal.Input{Key: []byte("/login openai")})
	send(terminal.Input{Key: []byte("\r")})
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
	send(terminal.Input{Paste: true, PasteStart: true})
	if status := deliverAppLoginCallback(t, bindAddr, "state="+parsed.Query().Get("state")+"&code=paste-code&client_id=paste-client"); status != 200 {
		t.Fatalf("callback status = %d", status)
	}
	send(terminal.Input{Paste: true, Key: []byte("CALLBACK-FRAGMENT")})
	send(terminal.Input{Paste: true, Key: []byte("-tail")})
	send(terminal.Input{Paste: true, PasteEnd: true})
	waitAppOutput(t, out, "Signed in to ChatGPT. Model: openai/gpt-6.1-sol")
	send(terminal.Input{Key: []byte("PING")})
	send(terminal.Input{Key: []byte("\r")})
	waitAppOutput(t, out, "paste-reply")
	close(inputs)
	if err := <-result; err != nil {
		t.Fatalf("repl: %v", err)
	}
	sessionData, err := os.ReadFile(r.store.File())
	if err != nil {
		t.Fatal(err)
	}
	sessionText := string(sessionData)
	if !strings.Contains(sessionText, "PING") {
		t.Fatalf("prompt missing from the session: %s", sessionText)
	}
	for _, secret := range []string{"CALLBACK-FRAGMENT", "-tail", "paste-code", "paste-access"} {
		if strings.Contains(sessionText, secret) {
			t.Fatalf("session contains %q", secret)
		}
	}
	for _, recalled := range console.Editor.History() {
		if strings.Contains(recalled, "CALLBACK") {
			t.Fatalf("recall contains a callback fragment: %q", recalled)
		}
	}
}

// EOF during a pending login cancels the attempt and joins the worker
// before the REPL returns. No credential is written. L5, L6.
func TestOpenAILoginEOFCleansUp(t *testing.T) {
	clearProviderAPIKeys(t)
	r := testRuntime(t)
	r.models = openAILoginTestModels(t)
	r.settings.MaxRetries = 0
	r.openAITransport = roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		return nil, &provider.ProviderError{Kind: provider.ErrInvalid, Message: "unexpected test URL"}
	})
	r.openAIBind = func() (net.Listener, error) { return net.Listen("tcp", "127.0.0.1:0") }
	browserURLs := make(chan string, 1)
	r.openAIBrowser = func(ctx context.Context, authURL string) error {
		browserURLs <- authURL
		<-ctx.Done()
		return ctx.Err()
	}
	if err := r.configure(config.StartupOverrides{Provider: "openai", Model: "gpt-6.1-sol", Thinking: "off", ThinkingExplicit: true}); err != nil {
		t.Fatal(err)
	}
	_, out, inputWrite, result := startLoginTestREPL(t, r)
	waitAppOutput(t, out, "gpt-6.1-sol")
	if _, err := inputWrite.Write([]byte("/login openai\r")); err != nil {
		t.Fatal(err)
	}
	select {
	case <-browserURLs:
	case <-time.After(60 * time.Second):
		t.Fatal("browser never opened")
	}
	if err := inputWrite.Close(); err != nil {
		t.Fatal(err)
	}
	if err := <-result; err != nil {
		t.Fatalf("repl: %v", err)
	}
	if !strings.Contains(out.String(), "ChatGPT sign-in cancelled.") {
		t.Fatalf("cancel notice missing: %q", out.String())
	}
	if _, err := os.Stat(filepath.Join(r.agentDir, "auth.json")); !os.IsNotExist(err) {
		t.Fatalf("EOF login wrote %v", err)
	}
}

// shutdownTestSignal is the signal the shutdown path honors on each
// platform: SIGTERM on Unix, interrupt on Windows.
func shutdownTestSignal() os.Signal {
	if goruntime.GOOS == "windows" {
		return os.Interrupt
	}
	return syscall.SIGTERM
}

// A shutdown signal during a pending login cancels the attempt and joins
// the worker before the REPL returns. L5, L6.
func TestOpenAILoginShutdownCleansUp(t *testing.T) {
	clearProviderAPIKeys(t)
	r := testRuntime(t)
	r.models = openAILoginTestModels(t)
	r.settings.MaxRetries = 0
	r.openAITransport = roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		return nil, &provider.ProviderError{Kind: provider.ErrInvalid, Message: "unexpected test URL"}
	})
	r.openAIBind = func() (net.Listener, error) { return net.Listen("tcp", "127.0.0.1:0") }
	browserURLs := make(chan string, 1)
	r.openAIBrowser = func(ctx context.Context, authURL string) error {
		browserURLs <- authURL
		<-ctx.Done()
		return ctx.Err()
	}
	if err := r.configure(config.StartupOverrides{Provider: "openai", Model: "gpt-6.1-sol", Thinking: "off", ThinkingExplicit: true}); err != nil {
		t.Fatal(err)
	}
	r.signals = make(chan os.Signal, 8)
	_, out, inputWrite, result := startLoginTestREPL(t, r)
	waitAppOutput(t, out, "gpt-6.1-sol")
	if _, err := inputWrite.Write([]byte("/login openai\r")); err != nil {
		t.Fatal(err)
	}
	select {
	case <-browserURLs:
	case <-time.After(60 * time.Second):
		t.Fatal("browser never opened")
	}
	r.signals <- shutdownTestSignal()
	if err := <-result; err != nil {
		t.Fatalf("repl: %v", err)
	}
	if err := inputWrite.Close(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "ChatGPT sign-in cancelled.") {
		t.Fatalf("cancel notice missing: %q", out.String())
	}
	if _, err := os.Stat(filepath.Join(r.agentDir, "auth.json")); !os.IsNotExist(err) {
		t.Fatalf("shutdown login wrote %v", err)
	}
}

// Cancelling after the commit is admitted still settles the save exactly
// once, but the saved login never activates a model. L5, L7.
func TestOpenAILoginCommitAdmittedSettlesWithoutActivation(t *testing.T) {
	clearProviderAPIKeys(t)
	r := testRuntime(t)
	r.settings.MaxRetries = 0
	r.openAITransport = roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.String() != "https://auth.openai.com/api/accounts/oauth/token" {
			return nil, &provider.ProviderError{Kind: provider.ErrInvalid, Message: "unexpected test URL"}
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"access_token":"admitted-access","refresh_token":"admitted-refresh","scope":"openid chatgpt.tokens.use.direct","expires_in":3600,"id_token":"admitted-id-token"}`)), Header: make(http.Header)}, nil
	})
	var bindAddr string
	r.openAIBind = func() (net.Listener, error) {
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			return nil, err
		}
		bindAddr = listener.Addr().String()
		return listener, nil
	}
	browserURLs := make(chan string, 1)
	r.openAIBrowser = func(_ context.Context, authURL string) error {
		browserURLs <- authURL
		return nil
	}
	entered := make(chan struct{})
	release := make(chan struct{})
	var once atomic.Bool
	var renames atomic.Int32
	r.openAIRename = func(oldpath, newpath string) error {
		renames.Add(1)
		if once.CompareAndSwap(false, true) {
			close(entered)
		}
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
	if _, err := inputWrite.Write([]byte("/login openai\r")); err != nil {
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
	if status := deliverAppLoginCallback(t, bindAddr, "state="+parsed.Query().Get("state")+"&code=admitted-code&client_id=admitted-client"); status != 200 {
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
	// Writing Ctrl+C to the pipe does not mean the REPL handled it.
	waitAppOutput(t, out, "Cancelling ChatGPT sign-in")
	releaseSave()
	waitAppOutput(t, out, "activation was skipped after cancel")
	if err := inputWrite.Close(); err != nil {
		t.Fatal(err)
	}
	if err := <-result; err != nil {
		t.Fatalf("repl: %v", err)
	}
	if renames.Load() != 1 {
		t.Fatalf("renames = %d, want exactly 1", renames.Load())
	}
	authData, err := os.ReadFile(filepath.Join(r.agentDir, "auth.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(authData), "admitted-access") {
		t.Fatalf("admitted save missing: %s", authData)
	}
	if r.ov.Provider != "anthropic" || r.ov.Model != "m" {
		t.Fatalf("cancelled login activated %s/%s", r.ov.Provider, r.ov.Model)
	}
	if r.authErr == nil {
		t.Fatal("cancelled login cleared the prior auth error")
	}
}

// Activation keeps a working non-OpenAI selection and stores nothing. L7.
func TestOpenAILoginActivationRetainsWorkingModel(t *testing.T) {
	clearProviderAPIKeys(t)
	r := testRuntime(t)
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-synthetic-activation")
	if err := r.configure(config.StartupOverrides{Provider: "anthropic", Model: "m", Thinking: "off", ThinkingExplicit: true}); err != nil {
		t.Fatal(err)
	}
	if r.authErr != nil {
		t.Fatalf("authErr: %v", r.authErr)
	}
	before := r.store.EntryCount()
	kind, text := r.activateOpenAILogin()
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

// Activation rechecks the current supported OpenAI model and clears the
// stale auth error without a model-change entry. L7.
func TestOpenAILoginActivationSelectsCurrentOpenAIModel(t *testing.T) {
	clearProviderAPIKeys(t)
	r := testRuntime(t)
	r.models = openAILoginTestModels(t)
	if err := r.configure(config.StartupOverrides{Provider: "openai", Model: "gpt-6.1-sol", Thinking: "off", ThinkingExplicit: true}); err != nil {
		t.Fatal(err)
	}
	if r.authErr == nil {
		t.Fatal("cold OpenAI configure unexpectedly usable")
	}
	writeAppFile(t, filepath.Join(r.agentDir, "auth.json"), `{"openai":{"type":"oauth","access":"activate-access","refresh":"activate-refresh","expires":9999999999999,"clientId":"activate-client","scopes":["chatgpt.tokens.use.direct"]}}`)
	before := r.store.EntryCount()
	kind, text := r.activateOpenAILogin()
	if kind != terminal.NoticeInfo || !strings.Contains(text, "Model: openai/gpt-6.1-sol") {
		t.Fatalf("activation = %v %q", kind, text)
	}
	if r.authErr != nil {
		t.Fatalf("authErr after activation: %v", r.authErr)
	}
	if r.ov.Provider != "openai" || r.ov.Model != "gpt-6.1-sol" {
		t.Fatalf("selection = %s/%s", r.ov.Provider, r.ov.Model)
	}
	if live := r.liveSettings(); live.Provider != r.prov || live.Model.ID != r.ov.Model || live.AuthErr != nil {
		t.Fatal("unchanged activation did not publish the refreshed provider")
	}
	if r.store.EntryCount() != before {
		t.Fatal("unchanged activation stored a model-change entry")
	}
}

// Activation selects the official OpenAI default for an offline
// non-OpenAI selection and records the move. L7.
func TestOpenAILoginActivationSelectsDefaultForOfflineSelection(t *testing.T) {
	clearProviderAPIKeys(t)
	r := testRuntime(t)
	if err := r.configure(config.StartupOverrides{Provider: "anthropic", Model: "m", Thinking: "off", ThinkingExplicit: true}); err != nil {
		t.Fatal(err)
	}
	if r.authErr == nil {
		t.Fatal("cold anthropic configure unexpectedly usable")
	}
	writeAppFile(t, filepath.Join(r.agentDir, "auth.json"), `{"openai":{"type":"oauth","access":"default-access","refresh":"default-refresh","expires":9999999999999,"clientId":"default-client","scopes":["chatgpt.tokens.use.direct"]}}`)
	before := r.store.EntryCount()
	kind, text := r.activateOpenAILogin()
	if kind != terminal.NoticeInfo || !strings.Contains(text, "Model: openai/gpt-6.1-sol") {
		t.Fatalf("activation = %v %q", kind, text)
	}
	if r.ov.Provider != "openai" || r.ov.Model != "gpt-6.1-sol" || r.authErr != nil {
		t.Fatalf("selection = %s/%s (%v)", r.ov.Provider, r.ov.Model, r.authErr)
	}
	saved, err := r.store.ModelState()
	if err != nil {
		t.Fatal(err)
	}
	live := r.liveSettings()
	if r.store.EntryCount() <= before || saved.Provider != "openai" || saved.ModelID != r.ov.Model ||
		live.Provider != r.prov || live.AuthErr != nil || live.Model.ID != saved.ModelID || live.Thinking != saved.ThinkingLevel {
		t.Fatalf("activation did not publish the saved selection: saved=%+v, live=%s/%s at %s", saved, live.Model.ProviderID(), live.Model.ID, live.Thinking)
	}
}

// A saved login with an unusable activation keeps the prior runtime and
// reports both outcomes separately. L7, L9.
func TestOpenAILoginActivationFailureKeepsPriorRuntime(t *testing.T) {
	clearProviderAPIKeys(t)
	r := testRuntime(t)
	overridePath := filepath.Join(t.TempDir(), "models.json")
	if err := os.WriteFile(overridePath, []byte(`{"providers":{"anthropic":{"models":[{"id":"m","contextWindow":200000,"maxTokens":16384}]},"openai":{"baseUrl":"https://override.example/v1"}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	models, err := config.LoadModels(overridePath)
	if err != nil {
		t.Fatal(err)
	}
	r.models = models
	if err := r.configure(config.StartupOverrides{Provider: "anthropic", Model: "m", Thinking: "off", ThinkingExplicit: true}); err != nil {
		t.Fatal(err)
	}
	priorErr := r.authErr
	if priorErr == nil {
		t.Fatal("cold anthropic has no auth error")
	}
	writeAppFile(t, filepath.Join(r.agentDir, "auth.json"), `{"openai":{"type":"oauth","access":"override-access","refresh":"override-refresh","expires":9999999999999,"clientId":"override-client","scopes":["chatgpt.tokens.use.direct"]}}`)
	kind, text := r.activateOpenAILogin()
	if kind != terminal.NoticeError || !strings.Contains(text, "Signed in to ChatGPT") || !strings.Contains(text, "model activation failed") {
		t.Fatalf("activation = %v %q", kind, text)
	}
	if r.ov.Provider != "anthropic" || r.ov.Model != "m" {
		t.Fatalf("failed activation moved to %s/%s", r.ov.Provider, r.ov.Model)
	}
	if r.authErr == nil || r.authErr.Error() != priorErr.Error() {
		t.Fatalf("authErr = %v, want the retained %v", r.authErr, priorErr)
	}
}

// A stored OAuth login on an endpoint override fails the submit before any
// refresh or POST. L7-L9.
func TestOpenAISubmitRefusesOAuthOverrideWithoutPOST(t *testing.T) {
	clearProviderAPIKeys(t)
	r := testRuntime(t)
	overridePath := filepath.Join(t.TempDir(), "models.json")
	if err := os.WriteFile(overridePath, []byte(`{"providers":{"openai":{"baseUrl":"https://override.example/v1","apiKey":"synthetic-lower-priority"}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	models, err := config.LoadModels(overridePath)
	if err != nil {
		t.Fatal(err)
	}
	r.models = models
	r.settings.MaxRetries = 0
	writeAppFile(t, filepath.Join(r.agentDir, "auth.json"), `{"openai":{"type":"oauth","access":"override-access","refresh":"override-refresh","expires":9999999999999,"clientId":"override-client","scopes":["chatgpt.tokens.use.direct"]}}`)
	if err := r.configure(config.StartupOverrides{Provider: "openai", Model: "gpt-6.1-sol", Thinking: "off", ThinkingExplicit: true}); err != nil {
		t.Fatal(err)
	}
	if r.authErr == nil || !strings.Contains(r.authErr.Error(), "official OpenAI endpoint") {
		t.Fatalf("authErr = %v", r.authErr)
	}
	var calls atomic.Int32
	responses, ok := r.prov.(*provider.ResponsesClient)
	if !ok {
		t.Fatal("override did not configure a Responses client")
	}
	responses.HTTP.Transport = roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		calls.Add(1)
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(openAISyntheticStream("must-not-send"))), Header: make(http.Header)}, nil
	})
	_, err = r.prov.Send(context.Background(), provider.Request{Model: r.cfg.Model, Thinking: "off", Messages: []provider.Message{{Role: "user", Content: []provider.Block{{Type: "text", Text: "hi"}}}}}, nil)
	if err == nil || !strings.Contains(err.Error(), "official OpenAI endpoint") {
		t.Fatalf("submit = %v", err)
	}
	if calls.Load() != 0 {
		t.Fatalf("refused submit made %d HTTP calls", calls.Load())
	}
}

// A stored API key keeps working on a direct endpoint override. L9.
func TestOpenAISubmitAllowsAPIKeyOverride(t *testing.T) {
	clearProviderAPIKeys(t)
	r := testRuntime(t)
	overridePath := filepath.Join(t.TempDir(), "models.json")
	if err := os.WriteFile(overridePath, []byte(`{"providers":{"openai":{"baseUrl":"https://override.example/v1"}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	models, err := config.LoadModels(overridePath)
	if err != nil {
		t.Fatal(err)
	}
	r.models = models
	r.settings.MaxRetries = 0
	writeAppFile(t, filepath.Join(r.agentDir, "auth.json"), `{"openai":{"type":"api_key","key":"direct-key"}}`)
	if err := r.configure(config.StartupOverrides{Provider: "openai", Model: "gpt-6.1-sol", Thinking: "off", ThinkingExplicit: true}); err != nil {
		t.Fatal(err)
	}
	if r.authErr != nil {
		t.Fatalf("authErr = %v", r.authErr)
	}
	responses, ok := r.prov.(*provider.ResponsesClient)
	if !ok {
		t.Fatal("override did not configure a Responses client")
	}
	responses.HTTP.Transport = roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.String() != "https://override.example/v1/responses" || req.Header.Get("Authorization") != "Bearer direct-key" {
			return nil, &provider.ProviderError{Kind: provider.ErrInvalid, Message: "unexpected override request"}
		}
		if req.Header.Get("session-id") != r.store.Header().ID || req.Header.Get("session_id") != "" {
			t.Error("endpoint override did not retain the existing Responses session header")
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(openAISyntheticStream("override-reply"))), Header: make(http.Header)}, nil
	})
	resp, err := r.prov.Send(context.Background(), provider.Request{Model: r.cfg.Model, Thinking: "off", Messages: []provider.Message{{Role: "user", Content: []provider.Block{{Type: "text", Text: "hi"}}}}}, nil)
	if err != nil || resp.Text != "override-reply" {
		t.Fatalf("override send = %+v, %v", resp, err)
	}
}

// A redirect on a built-in OpenAI Responses send is never followed, for
// either auth kind. L9, L10.
func TestOpenAIResponsesRedirectNotFollowed(t *testing.T) {
	clearProviderAPIKeys(t)
	r := testRuntime(t)
	r.models = openAILoginTestModels(t)
	r.settings.MaxRetries = 0
	writeAppFile(t, filepath.Join(r.agentDir, "auth.json"), `{"openai":{"type":"oauth","access":"redirect-access","refresh":"redirect-refresh","expires":9999999999999,"clientId":"redirect-client","scopes":["chatgpt.tokens.use.direct"]}}`)
	if err := r.configure(config.StartupOverrides{Provider: "openai", Model: "gpt-6.1-sol", Thinking: "off", ThinkingExplicit: true}); err != nil {
		t.Fatal(err)
	}
	if r.authErr != nil {
		t.Fatalf("authErr = %v", r.authErr)
	}
	var evil atomic.Int32
	responses, ok := r.prov.(*provider.ResponsesClient)
	if !ok {
		t.Fatal("openai did not configure a Responses client")
	}
	responses.HTTP.Transport = roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		if !strings.HasPrefix(req.URL.String(), "https://api.openai.com/") {
			evil.Add(1)
			return nil, &provider.ProviderError{Kind: provider.ErrInvalid, Message: "unexpected redirect target"}
		}
		resp := &http.Response{StatusCode: 302, Body: io.NopCloser(strings.NewReader("")), Header: make(http.Header)}
		resp.Header.Set("Location", "https://evil.example/responses")
		return resp, nil
	})
	if _, err := r.prov.Send(context.Background(), provider.Request{Model: r.cfg.Model, Thinking: "off", Messages: []provider.Message{{Role: "user", Content: []provider.Block{{Type: "text", Text: "hi"}}}}}, nil); err == nil {
		t.Fatal("redirect accepted")
	}
	if evil.Load() != 0 {
		t.Fatal("redirect target requested")
	}
}
