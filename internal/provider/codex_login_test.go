package provider

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// codexLoginTestAccess returns a JWT-shaped token whose middle segment holds
// the ChatGPT account claim. Only codexAccount reads this segment.
func codexLoginTestAccess(account string) string {
	payload := base64.RawURLEncoding.EncodeToString([]byte(`{"https://api.openai.com/auth":{"chatgpt_account_id":"` + account + `"}}`))
	return "eyJhbGciOiJub25lIn0." + payload + ".synthetic-signature"
}

func codexLoginTestTokenBody(access, refresh string) string {
	return `{"access_token":"` + access + `","refresh_token":"` + refresh + `","expires_in":3600,"id_token":"ignored-id-token","scope":"openid profile email offline_access"}`
}

// deliverCodexLoginCallbacks sends callback requests to addr with the exact
// owned Host authority. It reports delivery errors instead of failing the
// test, so delivery goroutines can join deterministically.
func deliverCodexLoginCallbacks(addr string, queries ...string) ([]int, error) {
	client := &http.Client{Transport: &http.Transport{Proxy: nil}, Timeout: 5 * time.Second}
	defer client.CloseIdleConnections()
	statuses := make([]int, 0, len(queries))
	for _, query := range queries {
		req, err := http.NewRequest(http.MethodGet, "http://"+addr+"/auth/callback?"+query, nil)
		if err != nil {
			return nil, err
		}
		req.Host = addr
		resp, err := client.Do(req)
		if err != nil {
			return nil, err
		}
		body, readErr := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if readErr != nil {
			return nil, readErr
		}
		if resp.ContentLength != int64(len(body)) || len(body) == 0 {
			return nil, errors.New("callback page did not have a complete fixed-length body")
		}
		statuses = append(statuses, resp.StatusCode)
	}
	return statuses, nil
}

// codexLoginEphemeralBind is the test listener seam: an ephemeral IPv4
// loopback listener whose address the test reads after Run binds it.
func codexLoginEphemeralBind(addr *string) func(string) (net.Listener, error) {
	return func(string) (net.Listener, error) {
		listener, err := net.Listen("tcp4", "127.0.0.1:0")
		if err != nil {
			return nil, err
		}
		*addr = listener.Addr().String()
		return listener, nil
	}
}

// completeCodexLoginTest delivers one correlated callback from inside ready.
func completeCodexLoginTest(t *testing.T, addr, authURL, code string) {
	t.Helper()
	parsed, err := url.Parse(authURL)
	if err != nil {
		t.Fatal(err)
	}
	statuses, err := deliverCodexLoginCallbacks(addr, "state="+parsed.Query().Get("state")+"&code="+code)
	if err != nil {
		t.Fatalf("callback delivery: %v", err)
	}
	if len(statuses) != 1 || statuses[0] != http.StatusOK {
		t.Fatalf("callback statuses = %v", statuses)
	}
}

func writeCodexLoginAuth(t *testing.T, path, doc string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
}

func readCodexLoginEntry(t *testing.T, path string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var all map[string]map[string]any
	if err := json.Unmarshal(data, &all); err != nil {
		t.Fatal(err)
	}
	return all["openai-codex"]
}

// The browser path derives its redirect from the owned listener, checks the
// Codex authorize contract, claims one callback, and exchanges the code with
// S256 PKCE. Wrong-state, mixed, and duplicate callbacks neither exchange
// twice nor abort the valid attempt. L1-L4.
func TestCodexLoginBrowser(t *testing.T) {
	authPath := filepath.Join(t.TempDir(), "auth.json")
	access := codexLoginTestAccess("acct-browser")
	var exchanges atomic.Int32
	var gotForm url.Values
	var gotAccept, gotContentType string
	login := &CodexLogin{
		Path: authPath,
		HTTP: &http.Client{Transport: &openAIFakeToken{handler: func(r *http.Request) (*http.Response, error) {
			exchanges.Add(1)
			if r.URL.String() != codexTokenURL {
				return nil, errors.New("unexpected token URL " + r.URL.String())
			}
			body, _ := io.ReadAll(r.Body)
			form, err := url.ParseQuery(string(body))
			if err != nil {
				return nil, err
			}
			gotForm = form
			gotAccept, gotContentType = r.Header.Get("Accept"), r.Header.Get("Content-Type")
			return openAITokenResponse(http.StatusOK, codexLoginTestTokenBody(access, "codex-browser-refresh")), nil
		}}},
		now: fixedOpenAINow,
	}
	var addr string
	login.Bind = codexLoginEphemeralBind(&addr)
	var authURL string
	ready := func(rawURL string) {
		authURL = rawURL
		parsed, err := url.Parse(rawURL)
		if err != nil {
			t.Fatal(err)
		}
		query := parsed.Query()
		if parsed.Scheme+"://"+parsed.Host+parsed.Path != codexLoginAuthorizeURL {
			t.Fatalf("authorize URL = %q", rawURL)
		}
		if query.Get("client_id") != codexClientID || query.Get("response_type") != "code" ||
			query.Get("redirect_uri") != "http://"+addr+"/auth/callback" ||
			query.Get("scope") != codexLoginScope || query.Get("code_challenge_method") != "S256" ||
			query.Get("id_token_add_organizations") != "true" || query.Get("codex_cli_simplified_flow") != "true" ||
			query.Get("originator") != "psi" {
			t.Fatalf("authorize query = %q", parsed.RawQuery)
		}
		for _, absent := range []string{"nonce", "resource", "ext_agent_host_id", "agent_name_hint"} {
			if query.Has(absent) {
				t.Fatalf("direct OpenAI grant field %q leaked into the Codex request", absent)
			}
		}
		for _, name := range []string{"code_challenge", "state"} {
			if len(query.Get(name)) != 43 {
				t.Fatalf("%s length = %d, want 43", name, len(query.Get(name)))
			}
		}
		state := query.Get("state")
		statuses, err := deliverCodexLoginCallbacks(addr,
			"state=wrong-state&code=wrong-code",
			"state="+state+"&code=mixed-code&error=access_denied",
			"state="+state+"&state=duplicate&code=duplicate-state",
			"state="+state+"&code=",
			"state="+state+"&code=browser-code",
			"state="+state+"&code=browser-code",
		)
		if err != nil {
			t.Fatalf("callback delivery: %v", err)
		}
		want := []int{400, 400, 400, 400, 200, 200}
		if len(statuses) != len(want) {
			t.Fatalf("callback statuses = %v", statuses)
		}
		for i := range want {
			if statuses[i] != want[i] {
				t.Fatalf("callback statuses = %v, want %v", statuses, want)
			}
		}
	}
	if err := login.Run(context.Background(), ready); err != nil {
		t.Fatalf("login: %v", err)
	}
	if exchanges.Load() != 1 {
		t.Fatalf("exchanges = %d, want 1", exchanges.Load())
	}
	parsed, err := url.Parse(authURL)
	if err != nil {
		t.Fatal(err)
	}
	if gotForm.Get("grant_type") != "authorization_code" || gotForm.Get("client_id") != codexClientID ||
		gotForm.Get("code") != "browser-code" || gotForm.Get("redirect_uri") != "http://"+addr+"/auth/callback" {
		t.Fatalf("exchange form = %v", gotForm)
	}
	sum := sha256.Sum256([]byte(gotForm.Get("code_verifier")))
	if base64.RawURLEncoding.EncodeToString(sum[:]) != parsed.Query().Get("code_challenge") {
		t.Fatal("challenge does not match the submitted verifier")
	}
	if gotAccept != "application/json" || gotContentType != "application/x-www-form-urlencoded" {
		t.Fatalf("exchange headers = %q %q", gotAccept, gotContentType)
	}
	entry := readCodexLoginEntry(t, authPath)
	if len(entry) != 5 || entry["type"] != "oauth" || entry["access"] != access ||
		entry["refresh"] != "codex-browser-refresh" || entry["accountId"] != "acct-browser" {
		t.Fatalf("stored entry = %v", entry)
	}
	if entry["expires"] != float64(1700000000000)+3600*1000 {
		t.Fatalf("stored expiry = %v", entry["expires"])
	}
	data, err := os.ReadFile(authPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, transient := range []string{"browser-code", gotForm.Get("code_verifier"), "ignored-id-token"} {
		if transient != "" && strings.Contains(string(data), transient) {
			t.Fatalf("transient secret persisted in %s", data)
		}
	}
	requireAuthWriteMode(t, authPath, 0o600)
	// The callback listener must be released before the exchange completes,
	// so a fresh attempt can bind the same address.
	listener, err := net.Listen("tcp4", addr)
	if err != nil {
		t.Fatalf("listener was not released: %v", err)
	}
	_ = listener.Close()
}

// The callback latch publishes exactly one outcome. Invalid requests never
// claim the attempt, and a matched denial ends the wait without an exchange.
func TestCodexLoginCallbackGate(t *testing.T) {
	const host = "127.0.0.1:1455"
	const state = "valid-state-value-for-codex-gate"
	gate := newCodexLoginCallbackGate(host, state)
	for _, target := range []string{
		"/auth/%63allback?state=" + state + "&code=encoded-path",
		"/auth/callback?state=" + state + "%20&code=padded-state",
		"/auth/callback?state=" + state + "&code=code&error=",
		"/auth/callback?state=" + state + "&error=access_denied&code=",
		"/auth/callback?state=" + state + "&code=code&state=duplicate",
		"/auth/callback?state=wrong&error=access_denied",
	} {
		request := httptest.NewRequest(http.MethodGet, target, nil)
		request.Host = host
		recorder := httptest.NewRecorder()
		gate.handler().ServeHTTP(recorder, request)
		if recorder.Code < 400 {
			t.Errorf("invalid callback returned %d: %s", recorder.Code, target)
		}
	}
	// The wrapper server's Host differs from the owned authority, so use a
	// direct request through the handler for the valid outcomes.
	for _, target := range []string{
		"/auth/callback?state=" + state + "&code=win-code",
		"/auth/callback?state=" + state + "&code=win-code",
	} {
		request := httptest.NewRequest(http.MethodGet, target, nil)
		request.Host = host
		recorder := httptest.NewRecorder()
		gate.handler().ServeHTTP(recorder, request)
		if recorder.Code != http.StatusOK {
			t.Fatalf("valid callback returned %d", recorder.Code)
		}
	}
	select {
	case outcome := <-gate.result:
		if outcome.denied || outcome.code != "win-code" {
			t.Fatalf("outcome = %+v", outcome)
		}
	default:
		t.Fatal("no outcome published")
	}
	select {
	case outcome := <-gate.result:
		t.Fatalf("second outcome published: %+v", outcome)
	default:
	}
}

// A matched denial returns a static failure without a token POST or a saved
// credential.
func TestCodexLoginDenialWithoutExchange(t *testing.T) {
	authPath := filepath.Join(t.TempDir(), "auth.json")
	var exchanges atomic.Int32
	login := &CodexLogin{
		Path: authPath,
		HTTP: &http.Client{Transport: &openAIFakeToken{handler: func(r *http.Request) (*http.Response, error) {
			exchanges.Add(1)
			return openAITokenResponse(http.StatusOK, codexLoginTestTokenBody(codexLoginTestAccess("acct"), "refresh")), nil
		}}},
	}
	var addr string
	login.Bind = codexLoginEphemeralBind(&addr)
	err := login.Run(context.Background(), func(rawURL string) {
		parsed, err := url.Parse(rawURL)
		if err != nil {
			t.Fatal(err)
		}
		statuses, err := deliverCodexLoginCallbacks(addr, "state="+parsed.Query().Get("state")+"&error=access_denied")
		if err != nil || len(statuses) != 1 || statuses[0] != http.StatusBadRequest {
			t.Fatalf("denial delivery: statuses=%v err=%v", statuses, err)
		}
	})
	if err == nil || !strings.Contains(err.Error(), "not completed") {
		t.Fatalf("denial = %v", err)
	}
	if exchanges.Load() != 0 {
		t.Fatalf("denial exchanged %d times", exchanges.Load())
	}
	if _, err := os.Stat(authPath); !os.IsNotExist(err) {
		t.Fatalf("denial wrote %v", err)
	}
}

// Cancelling the browser wait writes nothing, releases the listener, and a
// fresh attempt uses new state. A bind failure never presents a URL.
func TestCodexLoginCancelAndBindFailure(t *testing.T) {
	authPath := filepath.Join(t.TempDir(), "auth.json")
	var addr string
	newLogin := func() *CodexLogin {
		return &CodexLogin{
			Path: authPath,
			HTTP: &http.Client{Transport: &openAIFakeToken{handler: func(r *http.Request) (*http.Response, error) {
				return openAITokenResponse(http.StatusOK, codexLoginTestTokenBody(codexLoginTestAccess("acct"), "refresh")), nil
			}}},
			Bind: codexLoginEphemeralBind(&addr),
		}
	}
	var firstURL, secondURL string
	ctx, cancel := context.WithCancel(context.Background())
	if err := newLogin().Run(ctx, func(rawURL string) {
		firstURL = rawURL
		cancel()
	}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled login = %v", err)
	}
	if _, err := os.Stat(authPath); !os.IsNotExist(err) {
		t.Fatalf("cancelled login wrote %v", err)
	}
	if err := newLogin().Run(context.Background(), func(rawURL string) {
		secondURL = rawURL
		completeCodexLoginTest(t, addr, rawURL, "retry-code")
	}); err != nil {
		t.Fatalf("retry: %v", err)
	}
	first, _ := url.Parse(firstURL)
	second, _ := url.Parse(secondURL)
	if first.Query().Get("state") == second.Query().Get("state") {
		t.Fatal("retry reused the cancelled state")
	}

	ready := false
	failing := &CodexLogin{
		Path: authPath,
		Bind: func(string) (net.Listener, error) {
			return nil, errors.New("synthetic bind failure")
		},
	}
	if err := failing.Run(context.Background(), func(string) { ready = true }); err == nil {
		t.Fatal("bind failure was accepted")
	}
	if ready {
		t.Fatal("bind failure presented an authorization URL")
	}
}

// A browser callback can finish while the launcher is still returning. If
// login was cancelled by then, the queued code must not be exchanged.
func TestCodexLoginCancelWithCallbackReady(t *testing.T) {
	authPath := filepath.Join(t.TempDir(), "auth.json")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	transport := &openAIFakeToken{}
	var addr string
	login := &CodexLogin{
		Path: authPath,
		Bind: codexLoginEphemeralBind(&addr),
		HTTP: &http.Client{Transport: transport},
	}
	err := login.Run(ctx, func(rawURL string) {
		completeCodexLoginTest(t, addr, rawURL, "queued-code")
		cancel()
	})
	if calls := transport.calls.Load(); !errors.Is(err, context.Canceled) || calls != 0 {
		t.Fatalf("login returned %v after %d exchanges; want cancellation without exchange", err, calls)
	}
	if _, err := os.Stat(authPath); !os.IsNotExist(err) {
		t.Fatalf("cancelled sign-in did not leave auth absent: %v", err)
	}
}

// Exchange failures are static and never touch credentials. A redirect is
// not followed and an ambiguous transport failure is not retried. L4-L6.
func TestCodexLoginExchangeFailures(t *testing.T) {
	const oldDoc = `{"deepseek":{"type":"api_key","key":"keep"},"openai-codex":{"type":"oauth","access":"old-access","refresh":"old-refresh","expires":1,"accountId":"old-account"}}`
	validAccess := codexLoginTestAccess("new-account")
	cases := []struct {
		name    string
		handler func(*http.Request) (*http.Response, error)
		check   func(*testing.T, error)
	}{
		{
			name: "redirect not followed",
			handler: func(r *http.Request) (*http.Response, error) {
				header := make(http.Header)
				header.Set("Location", "https://redirect.invalid/steal")
				return &http.Response{StatusCode: http.StatusFound, Header: header, Body: io.NopCloser(strings.NewReader(""))}, nil
			},
			check: func(t *testing.T, err error) {
				t.Helper()
				if err == nil || !strings.Contains(err.Error(), "sign-in failed") {
					t.Fatalf("redirect error = %v", err)
				}
			},
		},
		{
			name: "oversized body",
			handler: func(r *http.Request) (*http.Response, error) {
				body := strings.Repeat("x", openAITokenBodyLimit+1)
				return openAITokenResponse(http.StatusOK, body), nil
			},
			check: func(t *testing.T, err error) {
				t.Helper()
				if err == nil || !strings.Contains(err.Error(), "invalid OpenAI Codex sign-in response") {
					t.Fatalf("oversized error = %v", err)
				}
			},
		},
		{
			name: "incomplete body",
			handler: func(r *http.Request) (*http.Response, error) {
				return openAITokenResponse(http.StatusOK, `{"access_token":"`+validAccess+`"`), nil
			},
			check: func(t *testing.T, err error) {
				t.Helper()
				if err == nil || !strings.Contains(err.Error(), "invalid OpenAI Codex sign-in response") {
					t.Fatalf("incomplete error = %v", err)
				}
			},
		},
		{
			name: "zero expiry",
			handler: func(r *http.Request) (*http.Response, error) {
				return openAITokenResponse(http.StatusOK, `{"access_token":"`+validAccess+`","refresh_token":"r","expires_in":0}`), nil
			},
			check: func(t *testing.T, err error) {
				t.Helper()
				if err == nil || !strings.Contains(err.Error(), "invalid OpenAI Codex sign-in response") {
					t.Fatalf("expiry error = %v", err)
				}
			},
		},
		{
			name: "overflowing expiry",
			handler: func(r *http.Request) (*http.Response, error) {
				return openAITokenResponse(http.StatusOK, `{"access_token":"`+validAccess+`","refresh_token":"r","expires_in":1e308}`), nil
			},
			check: func(t *testing.T, err error) {
				t.Helper()
				if err == nil || !strings.Contains(err.Error(), "invalid OpenAI Codex sign-in response") {
					t.Fatalf("overflow error = %v", err)
				}
			},
		},
		{
			name: "missing account claim",
			handler: func(r *http.Request) (*http.Response, error) {
				return openAITokenResponse(http.StatusOK, `{"access_token":"opaque-access","refresh_token":"r","expires_in":3600}`), nil
			},
			check: func(t *testing.T, err error) {
				t.Helper()
				if err == nil || !strings.Contains(err.Error(), "did not include a ChatGPT account") {
					t.Fatalf("account error = %v", err)
				}
			},
		},
		{
			name: "transport failure once",
			handler: func(r *http.Request) (*http.Response, error) {
				return nil, errors.New("synthetic transport failure with codex-secret-code")
			},
			check: func(t *testing.T, err error) {
				t.Helper()
				if err == nil || !strings.Contains(err.Error(), "request failed") {
					t.Fatalf("transport error = %v", err)
				}
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			authPath := filepath.Join(dir, "auth.json")
			writeCodexLoginAuth(t, authPath, oldDoc)
			var calls atomic.Int32
			transport := &openAIFakeToken{handler: func(r *http.Request) (*http.Response, error) {
				calls.Add(1)
				// Every failure case must remain on one exchange attempt.
				return tc.handler(r)
			}}
			login := &CodexLogin{Path: authPath, HTTP: &http.Client{Transport: transport}, now: fixedOpenAINow}
			var addr string
			login.Bind = codexLoginEphemeralBind(&addr)
			err := login.Run(context.Background(), func(rawURL string) {
				completeCodexLoginTest(t, addr, rawURL, "codex-secret-code")
			})
			if err == nil {
				t.Fatal("exchange failure was accepted")
			}
			if calls.Load() != 1 {
				t.Fatalf("exchange attempts = %d, want 1", calls.Load())
			}
			tc.check(t, err)
			for _, secret := range []string{"codex-secret-code", codexClientID, validAccess, "old-access", "old-refresh"} {
				if strings.Contains(err.Error(), secret) {
					t.Fatalf("error %q contains secret %q", err, secret)
				}
			}
			after, readErr := os.ReadFile(authPath)
			if readErr != nil || string(after) != oldDoc {
				t.Fatalf("credentials changed after failure: %v", readErr)
			}
			temps, _ := filepath.Glob(filepath.Join(dir, ".psi-auth-*"))
			if len(temps) != 0 {
				t.Fatalf("temporary files remain: %v", temps)
			}
		})
	}
}

// An in-flight exchange ends on cancellation and reports a static failure.
// The request body read cannot outlive the per-request context.
func TestCodexLoginExchangeCancel(t *testing.T) {
	authPath := filepath.Join(t.TempDir(), "auth.json")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var calls atomic.Int32
	transport := &openAIFakeToken{handler: func(r *http.Request) (*http.Response, error) {
		calls.Add(1)
		cancel()
		<-r.Context().Done()
		return nil, r.Context().Err()
	}}
	login := &CodexLogin{Path: authPath, HTTP: &http.Client{Transport: transport}}
	_, err := login.exchangeCode(ctx, "cancel-code", "cancel-verifier", "http://127.0.0.1:1455/auth/callback")
	if err == nil || !strings.Contains(err.Error(), "request failed") {
		t.Fatalf("cancelled exchange = %v", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("exchange attempts = %d, want 1", calls.Load())
	}
	if strings.Contains(err.Error(), "cancel-code") || strings.Contains(err.Error(), "cancel-verifier") {
		t.Fatalf("cancelled exchange leaked OAuth input: %v", err)
	}
}

// A valid replacement follows a symlink to its regular target, preserves
// that target's mode and link, and merges an unrelated entry updated during
// the browser wait. The codex entry itself is compared after compaction.
func TestCodexLoginCommitSymlinkMerge(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink auth path test is POSIX-only")
	}
	dir := t.TempDir()
	target := filepath.Join(dir, "target.json")
	authPath := filepath.Join(dir, "auth.json")
	writeCodexLoginAuth(t, target, `{"deepseek":{"type":"api_key","key":"old-key"},"openai-codex":{"type":"oauth","access":"old-access","refresh":"old-refresh","expires":1,"accountId":"old-account"}}`)
	if err := os.Chmod(target, 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Base(target), authPath); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	access := codexLoginTestAccess("acct-merge")
	login := &CodexLogin{
		Path: authPath,
		HTTP: &http.Client{Transport: &openAIFakeToken{handler: func(r *http.Request) (*http.Response, error) {
			return openAITokenResponse(http.StatusOK, codexLoginTestTokenBody(access, "new-refresh")), nil
		}}},
		now: fixedOpenAINow,
	}
	var addr string
	login.Bind = codexLoginEphemeralBind(&addr)
	err := login.Run(context.Background(), func(rawURL string) {
		// An unrelated writer updates its own entry while the browser wait
		// is active. The Codex entry and indentation stay equivalent.
		updated := "{ \"deepseek\": { \"type\": \"api_key\", \"key\": \"updated-key\" },\n" +
			"  \"openai-codex\": { \"type\": \"oauth\", \"access\": \"old-access\", \"refresh\": \"old-refresh\", \"expires\": 1, \"accountId\": \"old-account\" } }"
		writeCodexLoginAuth(t, target, updated)
		completeCodexLoginTest(t, addr, rawURL, "merge-code")
	})
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	linkInfo, err := os.Lstat(authPath)
	if err != nil || linkInfo.Mode()&os.ModeSymlink == 0 {
		t.Fatal("auth.json link was replaced")
	}
	link, err := os.Readlink(authPath)
	if err != nil || link != filepath.Base(target) {
		t.Fatal("auth.json link text changed")
	}
	requireAuthWriteMode(t, target, 0o640)
	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	var all map[string]map[string]any
	if err := json.Unmarshal(data, &all); err != nil {
		t.Fatal(err)
	}
	if all["deepseek"]["key"] != "updated-key" {
		t.Fatalf("unrelated update was lost: %v", all["deepseek"])
	}
	entry := all["openai-codex"]
	if entry["access"] != access || entry["refresh"] != "new-refresh" || entry["accountId"] != "acct-merge" {
		t.Fatalf("stored entry = %v", entry)
	}
}

// A changed Codex entry, a retargeted link, a replaced directory, and an
// in-place edit after the commit snapshot are conflicts, never
// last-writer-wins overwrites.
func TestCodexLoginCommitConflicts(t *testing.T) {
	access := codexLoginTestAccess("acct-conflict")
	newLogin := func(authPath string, hooks func()) *CodexLogin {
		login := &CodexLogin{
			Path: authPath,
			HTTP: &http.Client{Transport: &openAIFakeToken{handler: func(r *http.Request) (*http.Response, error) {
				return openAITokenResponse(http.StatusOK, codexLoginTestTokenBody(access, "new-refresh")), nil
			}}},
			now: fixedOpenAINow,
		}
		login.Chmod = func(path string, mode os.FileMode) error {
			hooks()
			return os.Chmod(path, mode)
		}
		return login
	}
	baseDoc := `{"openai-codex":{"type":"oauth","access":"old-access","refresh":"old-refresh","expires":1,"accountId":"old-account"}}`
	t.Run("changed codex entry during wait", func(t *testing.T) {
		authPath := filepath.Join(t.TempDir(), "auth.json")
		writeCodexLoginAuth(t, authPath, baseDoc)
		login := newLogin(authPath, func() {})
		var addr string
		login.Bind = codexLoginEphemeralBind(&addr)
		err := login.Run(context.Background(), func(rawURL string) {
			writeCodexLoginAuth(t, authPath, `{"openai-codex":{"type":"oauth","access":"other-access","refresh":"other-refresh","expires":1,"accountId":"other"}}`)
			completeCodexLoginTest(t, addr, rawURL, "conflict-code")
		})
		if !errors.Is(err, errCodexLoginConflict) {
			t.Fatalf("changed entry error = %v", err)
		}
		if got := readCodexLoginEntry(t, authPath); got["access"] != "other-access" {
			t.Fatalf("changed entry was overwritten: %v", got)
		}
	})
	t.Run("retargeted link", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("symlink auth path test is POSIX-only")
		}
		dir := t.TempDir()
		first := filepath.Join(dir, "first.json")
		second := filepath.Join(dir, "second.json")
		authPath := filepath.Join(dir, "auth.json")
		writeCodexLoginAuth(t, first, baseDoc)
		writeCodexLoginAuth(t, second, baseDoc)
		if err := os.Symlink(first, authPath); err != nil {
			t.Skipf("symlink unavailable: %v", err)
		}
		login := newLogin(authPath, func() {})
		var addr string
		login.Bind = codexLoginEphemeralBind(&addr)
		err := login.Run(context.Background(), func(rawURL string) {
			if err := os.Remove(authPath); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(second, authPath); err != nil {
				t.Fatal(err)
			}
			completeCodexLoginTest(t, addr, rawURL, "retarget-code")
		})
		if !errors.Is(err, errCodexLoginConflict) {
			t.Fatalf("retargeted link error = %v", err)
		}
		for _, path := range []string{first, second} {
			if got := readCodexLoginEntry(t, path); got["access"] != "old-access" {
				t.Fatalf("retarget wrote %s: %v", path, got)
			}
		}
	})
	t.Run("replaced directory", func(t *testing.T) {
		dir := t.TempDir()
		authPath := filepath.Join(dir, "auth.json")
		writeCodexLoginAuth(t, authPath, baseDoc)
		moved := dir + "-moved"
		login := newLogin(authPath, func() {})
		var addr string
		login.Bind = codexLoginEphemeralBind(&addr)
		err := login.Run(context.Background(), func(rawURL string) {
			if err := os.Rename(dir, moved); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(dir, 0o700); err != nil {
				t.Fatal(err)
			}
			writeCodexLoginAuth(t, authPath, baseDoc)
			completeCodexLoginTest(t, addr, rawURL, "parent-code")
		})
		if !errors.Is(err, errCodexLoginConflict) {
			t.Fatalf("replaced directory error = %v", err)
		}
		for _, path := range []string{filepath.Join(moved, "auth.json"), authPath} {
			if got := readCodexLoginEntry(t, path); got["access"] != "old-access" {
				t.Fatalf("replaced directory wrote %s: %v", path, got)
			}
		}
	})
	t.Run("in-place edit after commit snapshot", func(t *testing.T) {
		authPath := filepath.Join(t.TempDir(), "auth.json")
		writeCodexLoginAuth(t, authPath, baseDoc)
		edited := false
		login := newLogin(authPath, func() {
			if edited {
				return
			}
			edited = true
			writeCodexLoginAuth(t, authPath, `{"openai-codex":{"type":"oauth","access":"late-access","refresh":"late-refresh","expires":1,"accountId":"late"}}`)
		})
		var addr string
		login.Bind = codexLoginEphemeralBind(&addr)
		err := login.Run(context.Background(), func(rawURL string) {
			completeCodexLoginTest(t, addr, rawURL, "late-code")
		})
		if !edited {
			t.Fatal("test did not reach the post-snapshot hook")
		}
		if !errors.Is(err, errCodexLoginConflict) {
			t.Fatalf("post-snapshot edit error = %v", err)
		}
		if got := readCodexLoginEntry(t, authPath); got["access"] != "late-access" {
			t.Fatalf("post-snapshot edit was overwritten: %v", got)
		}
	})
}

// A staged-write failure leaves the previous document and no temp file.
// Cancellation before admission writes nothing, while cancellation after
// admission finishes the admitted save.
func TestCodexLoginCommitOutcomes(t *testing.T) {
	access := codexLoginTestAccess("acct-commit")
	baseDoc := `{"deepseek":{"type":"api_key","key":"keep"},"openai-codex":{"type":"oauth","access":"old-access","refresh":"old-refresh","expires":1,"accountId":"old"}}`
	makeLogin := func(authPath string) *CodexLogin {
		login := &CodexLogin{
			Path: authPath,
			HTTP: &http.Client{Transport: &openAIFakeToken{handler: func(r *http.Request) (*http.Response, error) {
				return openAITokenResponse(http.StatusOK, codexLoginTestTokenBody(access, "new-refresh")), nil
			}}},
			now: fixedOpenAINow,
		}
		var addr string
		login.Bind = codexLoginEphemeralBind(&addr)
		return login
	}

	t.Run("staged write failure", func(t *testing.T) {
		dir := t.TempDir()
		authPath := filepath.Join(dir, "auth.json")
		writeCodexLoginAuth(t, authPath, baseDoc)
		login := makeLogin(authPath)
		login.Rename = func(string, string) error { return errors.New("synthetic rename failure") }
		var addr string
		login.Bind = codexLoginEphemeralBind(&addr)
		err := login.Run(context.Background(), func(rawURL string) {
			completeCodexLoginTest(t, addr, rawURL, "rename-code")
		})
		if err == nil || !strings.Contains(err.Error(), "cannot save the OpenAI Codex login") {
			t.Fatalf("rename failure = %v", err)
		}
		after, readErr := os.ReadFile(authPath)
		if readErr != nil || string(after) != baseDoc {
			t.Fatalf("staged failure changed the document: %v", readErr)
		}
		temps, _ := filepath.Glob(filepath.Join(dir, ".psi-auth-*"))
		if len(temps) != 0 {
			t.Fatalf("temporary files remain: %v", temps)
		}
	})

	t.Run("lost lease", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("a live holder prevents lock replacement on Windows")
		}
		dir := t.TempDir()
		authPath := filepath.Join(dir, "auth.json")
		writeCodexLoginAuth(t, authPath, baseDoc)
		login := makeLogin(authPath)
		var addr string
		login.Bind = codexLoginEphemeralBind(&addr)
		login.Chmod = func(path string, mode os.FileMode) error {
			lock := authPath + ".lock"
			if err := os.RemoveAll(lock); err != nil {
				return err
			}
			if err := os.Mkdir(lock, 0o700); err != nil {
				return err
			}
			return os.Chmod(path, mode)
		}
		err := login.Run(context.Background(), func(rawURL string) {
			completeCodexLoginTest(t, addr, rawURL, "lost-lease-code")
		})
		if !errors.Is(err, errCodexLoginLockLost) {
			t.Fatalf("lost lease error = %v", err)
		}
		after, readErr := os.ReadFile(authPath)
		if readErr != nil || string(after) != baseDoc {
			t.Fatalf("lost lease changed the document: %v", readErr)
		}
	})

	t.Run("cancel before admission", func(t *testing.T) {
		dir := t.TempDir()
		authPath := filepath.Join(dir, "auth.json")
		writeCodexLoginAuth(t, authPath, baseDoc)
		login := makeLogin(authPath)
		var addr string
		login.Bind = codexLoginEphemeralBind(&addr)
		ctx, cancel := context.WithCancel(context.Background())
		login.HTTP = &http.Client{Transport: &openAIFakeToken{handler: func(r *http.Request) (*http.Response, error) {
			cancel()
			return openAITokenResponse(http.StatusOK, codexLoginTestTokenBody(access, "new-refresh")), nil
		}}}
		err := login.Run(ctx, func(rawURL string) {
			completeCodexLoginTest(t, addr, rawURL, "cancel-code")
		})
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("pre-admission cancel = %v", err)
		}
		after, readErr := os.ReadFile(authPath)
		if readErr != nil || string(after) != baseDoc {
			t.Fatalf("pre-admission cancel changed the document: %v", readErr)
		}
	})

	t.Run("cancel after admission", func(t *testing.T) {
		dir := t.TempDir()
		authPath := filepath.Join(dir, "auth.json")
		writeCodexLoginAuth(t, authPath, baseDoc)
		login := makeLogin(authPath)
		var addr string
		login.Bind = codexLoginEphemeralBind(&addr)
		ctx, cancel := context.WithCancel(context.Background())
		login.Chmod = func(path string, mode os.FileMode) error {
			// writeAuthDocument has staged its temporary file, so the save
			// is admitted. The commit must finish and report success.
			cancel()
			return os.Chmod(path, mode)
		}
		err := login.Run(ctx, func(rawURL string) {
			completeCodexLoginTest(t, addr, rawURL, "admitted-code")
		})
		if err != nil {
			t.Fatalf("admitted save after cancel = %v", err)
		}
		if got := readCodexLoginEntry(t, authPath); got["access"] != access {
			t.Fatalf("admitted save did not persist: %v", got)
		}
	})
}

// Token decoding validates the Codex contract without reading scope or ID
// tokens. A positive finite numeric lifetime becomes a future millisecond
// expiry, and the account comes only from the access token.
func TestCodexLoginTokenContract(t *testing.T) {
	now := time.UnixMilli(1700000000000)
	access := codexLoginTestAccess("acct-contract")
	valid := `{"access_token":"` + access + `","refresh_token":"r","expires_in":3600,"id_token":"profile-only","scope":"unexpected scope"}`
	token, err := decodeCodexLoginToken([]byte(valid), now)
	if err != nil {
		t.Fatalf("valid token = %v", err)
	}
	if token.expires != now.UnixMilli()+3600*1000 || token.accountID != "acct-contract" {
		t.Fatalf("token = %+v", token)
	}
	for name, body := range map[string]string{
		"missing refresh": `{"access_token":"` + access + `","expires_in":3600}`,
		"string expiry":   `{"access_token":"` + access + `","refresh_token":"r","expires_in":"3600"}`,
		"negative expiry": `{"access_token":"` + access + `","refresh_token":"r","expires_in":-1}`,
		"no account":      `{"access_token":"opaque","refresh_token":"r","expires_in":3600}`,
	} {
		if _, err := decodeCodexLoginToken([]byte(body), now); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
}

// Callback requests on an unrelated Host authority are refused even with a
// valid state, so a local web page cannot claim the sign-in attempt.
func TestCodexLoginCallbackHostBound(t *testing.T) {
	const host = "127.0.0.1:1455"
	const state = "state-for-host-bound-test"
	gate := newCodexLoginCallbackGate(host, state)
	request := httptest.NewRequest(http.MethodGet, "/auth/callback?state="+state+"&code=code", nil)
	request.Host = "127.0.0.1:1457"
	recorder := httptest.NewRecorder()
	gate.handler().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("unrelated host status = %d", recorder.Code)
	}
	select {
	case outcome := <-gate.result:
		t.Fatalf("unrelated host claimed the attempt: %+v", outcome)
	default:
	}
}
