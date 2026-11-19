package provider

import (
	"context"
	"encoding/json"
	"errors"
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
)

// codexLoginTestAddrInUse produces a real platform address-in-use error from
// a task-owned ephemeral socket. No fixed port or incumbent process is used.
func codexLoginTestAddrInUse(t *testing.T) error {
	t.Helper()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	_, err = net.Listen("tcp4", listener.Addr().String())
	if err == nil {
		t.Fatal("duplicate bind unexpectedly succeeded")
	}
	return err
}

func TestCodexLoginAddrInUse(t *testing.T) {
	if err := codexLoginTestAddrInUse(t); !codexLoginAddrInUse(err) {
		t.Fatalf("real duplicate bind was not classified as address in use: %v", err)
	}
	if codexLoginAddrInUse(errors.New("synthetic permission failure")) {
		t.Fatal("non-bind error was classified as address in use")
	}
}

// The callback tries 1455, then 1457, then one OS-assigned port. Only a real
// address-in-use failure advances. A terminal failure presents no URL and
// writes no credential. The authorization and exchange redirects are equal.
func TestCodexLoginBindFallback(t *testing.T) {
	occupied := codexLoginTestAddrInUse(t)
	t.Run("1455 busy uses 1457", func(t *testing.T) {
		authPath := filepath.Join(t.TempDir(), "auth.json")
		var calls []string
		var listener net.Listener
		var gotRedirect string
		login := &CodexLogin{
			Path: authPath,
			HTTP: &http.Client{Transport: &openAIFakeToken{handler: func(r *http.Request) (*http.Response, error) {
				body, _ := io.ReadAll(r.Body)
				form, _ := url.ParseQuery(string(body))
				gotRedirect = form.Get("redirect_uri")
				return openAITokenResponse(http.StatusOK, codexLoginTestTokenBody(codexLoginTestAccess("acct"), "refresh")), nil
			}}},
			Bind: func(addr string) (net.Listener, error) {
				calls = append(calls, addr)
				if len(calls) == 1 {
					return nil, occupied
				}
				var err error
				listener, err = net.Listen("tcp4", "127.0.0.1:0")
				return listener, err
			},
			now: fixedOpenAINow,
		}
		var authURL string
		var osAssigned bool
		err := login.RunInteractive(context.Background(), func(rawURL string, assigned bool, submit func([]byte) error) {
			authURL, osAssigned = rawURL, assigned
			parsed, err := url.Parse(rawURL)
			if err != nil {
				t.Fatal(err)
			}
			if submit == nil {
				t.Fatal("manual submit missing")
			}
			state := parsed.Query().Get("state")
			statuses, err := deliverCodexLoginCallbacks(listener.Addr().String(), "state="+state+"&code=bind-code")
			if err != nil || len(statuses) != 1 || statuses[0] != http.StatusOK {
				t.Fatalf("callback = %v %v", statuses, err)
			}
		})
		if err != nil {
			t.Fatalf("login: %v", err)
		}
		if len(calls) != 2 || calls[0] != codexLoginCallbackAddr || calls[1] != codexLoginCallbackRetry {
			t.Fatalf("bind calls = %v", calls)
		}
		if osAssigned {
			t.Fatal("1457 was reported as OS-assigned")
		}
		parsed, _ := url.Parse(authURL)
		if parsed.Query().Get("redirect_uri") != "http://"+listener.Addr().String()+codexLoginCallbackPath {
			t.Fatalf("authorize redirect = %q", parsed.Query().Get("redirect_uri"))
		}
		if gotRedirect != parsed.Query().Get("redirect_uri") {
			t.Fatalf("exchange redirect = %q, authorize redirect = %q", gotRedirect, parsed.Query().Get("redirect_uri"))
		}
	})

	t.Run("both busy uses OS-assigned", func(t *testing.T) {
		authPath := filepath.Join(t.TempDir(), "auth.json")
		var calls []string
		var listener net.Listener
		var gotRedirect string
		login := &CodexLogin{
			Path: authPath,
			HTTP: &http.Client{Transport: &openAIFakeToken{handler: func(r *http.Request) (*http.Response, error) {
				body, _ := io.ReadAll(r.Body)
				form, _ := url.ParseQuery(string(body))
				gotRedirect = form.Get("redirect_uri")
				return openAITokenResponse(http.StatusOK, codexLoginTestTokenBody(codexLoginTestAccess("acct"), "refresh")), nil
			}}},
			Bind: func(addr string) (net.Listener, error) {
				calls = append(calls, addr)
				if len(calls) <= 2 {
					return nil, occupied
				}
				var err error
				listener, err = net.Listen("tcp4", "127.0.0.1:0")
				return listener, err
			},
			now: fixedOpenAINow,
		}
		var authURL string
		var osAssigned bool
		err := login.RunInteractive(context.Background(), func(rawURL string, assigned bool, submit func([]byte) error) {
			authURL, osAssigned = rawURL, assigned
			parsed, _ := url.Parse(rawURL)
			state := parsed.Query().Get("state")
			statuses, err := deliverCodexLoginCallbacks(listener.Addr().String(), "state="+state+"&code=os-code")
			if err != nil || len(statuses) != 1 || statuses[0] != http.StatusOK {
				t.Fatalf("callback = %v %v", statuses, err)
			}
		})
		if err != nil {
			t.Fatalf("login: %v", err)
		}
		if len(calls) != 3 || calls[0] != codexLoginCallbackAddr || calls[1] != codexLoginCallbackRetry || calls[2] != "127.0.0.1:0" {
			t.Fatalf("bind calls = %v", calls)
		}
		if !osAssigned {
			t.Fatal("OS-assigned port was not reported")
		}
		parsed, _ := url.Parse(authURL)
		want := "http://" + listener.Addr().String() + codexLoginCallbackPath
		if parsed.Query().Get("redirect_uri") != want || gotRedirect != want {
			t.Fatalf("authorize redirect = %q exchange redirect = %q want %q", parsed.Query().Get("redirect_uri"), gotRedirect, want)
		}
	})

	for _, tc := range []struct {
		name   string
		failAt int
	}{
		{name: "first non-busy failure", failAt: 1},
		{name: "second non-busy failure", failAt: 2},
		{name: "final bind failure", failAt: 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			authPath := filepath.Join(t.TempDir(), "auth.json")
			var calls int
			ready := false
			login := &CodexLogin{
				Path: authPath,
				Bind: func(string) (net.Listener, error) {
					calls++
					if calls == tc.failAt {
						return nil, errors.New("synthetic bind failure")
					}
					return nil, occupied
				},
			}
			err := login.RunInteractive(context.Background(), func(string, bool, func([]byte) error) { ready = true })
			if err == nil {
				t.Fatal("bind failure was accepted")
			}
			if ready || calls != tc.failAt {
				t.Fatalf("ready=%v calls=%d want %d", ready, calls, tc.failAt)
			}
			if _, statErr := os.Stat(authPath); !os.IsNotExist(statErr) {
				t.Fatalf("bind failure wrote credentials: %v", statErr)
			}
		})
	}
}

func TestParseCodexLoginPaste(t *testing.T) {
	const state = "expected-state"
	valid := []string{
		"http://127.0.0.1:1455/auth/callback?code=url-code&state=" + state,
		"url-code#" + state,
		"code=query-code&state=" + state,
		"bare-code",
		"?code=leading-query&state=" + state,
		"127.0.0.1:1457/auth/callback?code=hostless&state=" + state,
	}
	for _, input := range valid {
		outcome, err := parseCodexLoginPaste([]byte(input), state)
		if err != nil {
			t.Fatalf("%q: %v", input, err)
		}
		if outcome.code == "" || outcome.denied {
			t.Fatalf("%q: outcome = %+v", input, outcome)
		}
	}
	invalid := []struct {
		input string
		want  error
	}{
		{"", errCodexLoginPasteEmpty},
		{"url-code#wrong-state", errCodexLoginPasteState},
		{"code=query-code&state=wrong", errCodexLoginPasteState},
		{"code=one&code=two&state=" + state, errCodexLoginPasteInvalid},
		{"code=one&error=access_denied&state=" + state, errCodexLoginPasteInvalid},
		{"code#", errCodexLoginPasteInvalid},
		{"#state", errCodexLoginPasteInvalid},
		{"code#state#extra", errCodexLoginPasteInvalid},
		{"two words", errCodexLoginPasteInvalid},
		{"line\nbreak", errCodexLoginPasteInvalid},
		{"bare-code\r\n", errCodexLoginPasteInvalid},
		{"two words#" + state, errCodexLoginPasteInvalid},
		{"code=embedded%00control&state=" + state, errCodexLoginPasteInvalid},
		{strings.Repeat(" ", codexLoginPasteLimit) + "bare-code", errCodexLoginPasteInvalid},
		{"http://127.0.0.1:1455/auth/callback?error=access_denied&state=" + state, errCodexLoginPasteInvalid},
		{strings.Repeat("x", codexLoginPasteLimit+1), errCodexLoginPasteInvalid},
	}
	for _, tc := range invalid {
		_, err := parseCodexLoginPaste([]byte(tc.input), state)
		if !errors.Is(err, tc.want) {
			t.Fatalf("%q: error = %v, want %v", tc.input, err, tc.want)
		}
	}
}

// One gate accepts exactly one callback or manual submission. Invalid manual
// input does not claim the attempt, so the browser callback can still win.
func TestCodexLoginManualGate(t *testing.T) {
	const host = "127.0.0.1:1455"
	const state = "gate-state"
	newRequest := func(query string) *http.Request {
		req := httptest.NewRequest(http.MethodGet, "/auth/callback?"+query, nil)
		req.Host = host
		return req
	}

	t.Run("manual first", func(t *testing.T) {
		gate := newCodexLoginCallbackGate(host, state)
		if err := gate.submit([]byte("manual-code#" + state)); err != nil {
			t.Fatal(err)
		}
		if err := gate.submit([]byte("second-code#" + state)); !errors.Is(err, ErrCodexLoginClaimed) {
			t.Fatalf("second submit = %v", err)
		}
		recorder := httptest.NewRecorder()
		gate.handler().ServeHTTP(recorder, newRequest("state="+state+"&code=callback-code"))
		if recorder.Code != http.StatusOK {
			t.Fatalf("late callback status = %d", recorder.Code)
		}
		outcome := <-gate.result
		if outcome.code != "manual-code" {
			t.Fatalf("winner = %q", outcome.code)
		}
	})

	t.Run("callback first", func(t *testing.T) {
		gate := newCodexLoginCallbackGate(host, state)
		recorder := httptest.NewRecorder()
		gate.handler().ServeHTTP(recorder, newRequest("state="+state+"&code=callback-code"))
		if recorder.Code != http.StatusOK {
			t.Fatalf("callback status = %d", recorder.Code)
		}
		if err := gate.submit([]byte("manual-code#" + state)); !errors.Is(err, ErrCodexLoginClaimed) {
			t.Fatalf("submit = %v", err)
		}
		outcome := <-gate.result
		if outcome.code != "callback-code" {
			t.Fatalf("winner = %q", outcome.code)
		}
	})

	t.Run("invalid manual leaves callback pending", func(t *testing.T) {
		gate := newCodexLoginCallbackGate(host, state)
		if err := gate.submit([]byte("wrong#" + state + "-other")); !errors.Is(err, errCodexLoginPasteState) {
			t.Fatalf("invalid submit = %v", err)
		}
		select {
		case outcome := <-gate.result:
			t.Fatalf("invalid input claimed the gate: %+v", outcome)
		default:
		}
		recorder := httptest.NewRecorder()
		gate.handler().ServeHTTP(recorder, newRequest("state="+state+"&code=callback-code"))
		if recorder.Code != http.StatusOK {
			t.Fatalf("callback status = %d", recorder.Code)
		}
		outcome := <-gate.result
		if outcome.code != "callback-code" {
			t.Fatalf("winner = %q", outcome.code)
		}
	})

	t.Run("errors do not echo input", func(t *testing.T) {
		gate := newCodexLoginCallbackGate(host, state)
		secret := "SENTINEL-PASTE-VALUE"
		err := gate.submit([]byte(secret + " wrong"))
		if err == nil {
			t.Fatal("invalid input accepted")
		}
		if strings.Contains(err.Error(), secret) {
			t.Fatalf("error echoed input: %v", err)
		}
	})
}

// A successful manual submission drives the same browser exchange once with
// the listener redirect and stores the credential.
func TestCodexLoginManualExchange(t *testing.T) {
	authPath := filepath.Join(t.TempDir(), "auth.json")
	access := codexLoginTestAccess("acct-manual")
	var exchanges atomic.Int32
	var gotForm url.Values
	login := &CodexLogin{
		Path: authPath,
		HTTP: &http.Client{Transport: &openAIFakeToken{handler: func(r *http.Request) (*http.Response, error) {
			exchanges.Add(1)
			body, _ := io.ReadAll(r.Body)
			gotForm, _ = url.ParseQuery(string(body))
			return openAITokenResponse(http.StatusOK, codexLoginTestTokenBody(access, "manual-refresh")), nil
		}}},
		Bind: codexLoginEphemeralBind(new(string)),
		now:  fixedOpenAINow,
	}
	var redirect string
	err := login.RunInteractive(context.Background(), func(rawURL string, _ bool, submit func([]byte) error) {
		parsed, _ := url.Parse(rawURL)
		redirect = parsed.Query().Get("redirect_uri")
		if err := submit([]byte("manual-code")); err != nil {
			t.Fatal(err)
		}
	})
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	if exchanges.Load() != 1 || gotForm.Get("code") != "manual-code" || gotForm.Get("redirect_uri") != redirect {
		t.Fatalf("exchanges=%d form=%v redirect=%q", exchanges.Load(), gotForm, redirect)
	}
	entry := readCodexLoginEntry(t, authPath)
	if entry["access"] != access || entry["refresh"] != "manual-refresh" {
		t.Fatalf("stored entry = %v", entry)
	}
	data, _ := json.Marshal(entry)
	if strings.Contains(string(data), "manual-code") {
		t.Fatal("manual code persisted")
	}
}
