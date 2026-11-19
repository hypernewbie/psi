package app

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"psi/internal/config"
	"psi/internal/provider"
	"psi/internal/terminal"
	"psi/internal/testutil"
)

// Each exit path cancels and joins an in-flight poll without saving credentials.
func TestCodexDeviceLoginREPLStopPolling(t *testing.T) {
	readErr := errors.New("synthetic terminal read error")
	for _, tc := range []struct {
		name  string
		input terminal.Input
	}{
		{"escape", terminal.Input{Key: []byte("\x1b")}},
		{"ctrl-d", terminal.Input{Key: []byte("\x04")}},
		{"read error", terminal.Input{Err: readErr}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := testRuntime(t)
			polling := make(chan struct{})
			stopped := make(chan struct{})
			browserStarted := make(chan string, 1)
			browserStopped := make(chan struct{})
			r.codexBrowser = func(ctx context.Context, target string) error {
				browserStarted <- target
				<-ctx.Done()
				close(browserStopped)
				return ctx.Err()
			}
			r.codexTransport = roundTripperFunc(func(req *http.Request) (*http.Response, error) {
				switch req.URL.Path {
				case "/api/accounts/deviceauth/usercode":
					return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"user_code":"ABCD-EFGH","device_auth_id":"device-1","interval":1}`))}, nil
				case "/api/accounts/deviceauth/token":
					close(polling)
					<-req.Context().Done()
					close(stopped)
					return nil, req.Context().Err()
				default:
					t.Error("unexpected request during device sign-in")
					return nil, errors.New("unexpected request")
				}
			})

			_, out, inputs, finished, stop := startCodexInputREPL(t, r, tc.input.Err)
			sendCodexInput(t, inputs, terminal.Input{Key: []byte("/login openai-codex")})
			sendCodexInput(t, inputs, terminal.Input{Key: []byte("\r")})
			waitAppOutput(t, out, "Device code login (headless)")
			if tc.name == "escape" {
				// Typing must not turn the choice picker into a search.
				sendCodexInput(t, inputs, terminal.Input{Key: []byte("no-matching-method")})
			}
			sendCodexInput(t, inputs, terminal.Input{Key: []byte("\x1b[B")})
			sendCodexInput(t, inputs, terminal.Input{Key: []byte("\r")})
			watchdog := time.NewTimer(60 * time.Second)
			defer watchdog.Stop()
			select {
			case <-polling:
			case <-watchdog.C:
				t.Fatal("device poll did not start")
			}
			if tc.name == "escape" {
				waitAppOutput(t, out, "Enter open browser")
				sendCodexInput(t, inputs, terminal.Input{Key: []byte("\r")})
				select {
				case target := <-browserStarted:
					if target != "https://auth.openai.com/codex/device" {
						t.Fatalf("wrong verification target: %q", target)
					}
				case <-watchdog.C:
					t.Fatal("Enter did not open the verification page")
				}
			}
			sendCodexInput(t, inputs, tc.input)
			if tc.name == "escape" {
				waitAppOutput(t, out, "OpenAI Codex sign-in cancelled")
				select {
				case <-browserStopped:
				default:
					t.Fatal("REPL settled before the requested browser launcher stopped")
				}
				sendCodexInput(t, inputs, terminal.Input{Key: []byte("/login")})
				sendCodexInput(t, inputs, terminal.Input{Key: []byte("\r")})
				waitAppOutput(t, out, "Providers")
				screen := testutil.NewVTModel(80, 24)
				screen.Feed([]byte(out.String()))
				if strings.Contains(screen.Text(), "ABCD-EFGH") || !screen.CursorVisible {
					t.Fatalf("device panel remained visible after cancellation: %q", screen.Text())
				}
			} else {
				select {
				case <-finished:
				case <-watchdog.C:
					t.Fatal("REPL did not exit after cancelling device sign-in")
				}
			}
			select {
			case <-stopped:
			default:
				t.Fatal("REPL settled before the active poll stopped")
			}
			stop()
			if _, err := os.Stat(filepath.Join(r.agentDir, "auth.json")); !os.IsNotExist(err) {
				t.Fatalf("cancelled sign-in wrote credentials: %v", err)
			}
		})
	}
}

// A cold REPL picks device sign-in from the Codex method picker, receives the
// user code in the interactive view, completes a synthetic reply, and saves
// the credential. Device mode does not open a browser without Enter and
// never binds a listener. The user code is not stored in the session,
// recall, or bridge state. L1-L4, L7-L9.
func TestCodexDeviceLoginREPL(t *testing.T) {
	clearProviderAPIKeys(t)
	r := testRuntime(t)
	access, refresh := codexAppTestAccess("acct-device-repl"), "device-repl-refresh"
	var starts, polls, exchanges atomic.Int32
	var gotForm url.Values
	server, models := codexReplyTestModels(t, "codex-device-reply")
	defer server.Close()
	r.models = models
	r.settings.MaxRetries = 0
	var bindCalled, browserCalled atomic.Bool
	r.codexBind = func(string) (net.Listener, error) {
		bindCalled.Store(true)
		return nil, errors.New("device mode must not bind")
	}
	r.codexBrowser = func(context.Context, string) error {
		browserCalled.Store(true)
		return errors.New("device mode must not launch a browser")
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
