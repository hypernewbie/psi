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
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const openAILoginTestDeviceID = "12345678-9abc-def0-1234-56789abcdef0"

// openAILoginEphemeralBind is the test listener seam: an ephemeral loopback
// listener whose address the test reads after Run binds it.
func openAILoginEphemeralBind(addr *string) func() (net.Listener, error) {
	return func() (net.Listener, error) {
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			return nil, err
		}
		*addr = listener.Addr().String()
		return listener, nil
	}
}

// deliverOpenAILoginCallbacks sends callback requests to addr with the
// unchanged production Host authority. It reports delivery errors instead
// of failing the test, so delivery goroutines can join deterministically.
func deliverOpenAILoginCallbacks(addr string, queries ...string) ([]int, error) {
	client := &http.Client{Transport: &http.Transport{Proxy: nil}, Timeout: 5 * time.Second}
	defer client.CloseIdleConnections()
	statuses := make([]int, 0, len(queries))
	for _, query := range queries {
		req, err := http.NewRequest(http.MethodGet, "http://"+addr+"/auth/callback?"+query, nil)
		if err != nil {
			return nil, err
		}
		req.Host = openAILoginCallbackHost
		resp, err := client.Do(req)
		if err != nil {
			return nil, err
		}
		body, readErr := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if readErr != nil {
			return nil, readErr
		}
		// The server can close as soon as the handler publishes its
		// outcome. The page must not depend on a final chunk or EOF.
		if resp.ContentLength != int64(len(body)) || len(body) == 0 {
			return nil, errors.New("callback page did not have a complete fixed-length body")
		}
		statuses = append(statuses, resp.StatusCode)
	}
	return statuses, nil
}

func openAILoginValidTokenBody() string {
	return `{"access_token":"login-access","refresh_token":"login-refresh","scope":"openid profile email offline_access resource.invoke chatgpt.tokens.use.direct","expires_in":3600,"id_token":"login-id-token"}`
}

func openAILoginTokenTransport(handler func(*http.Request) (*http.Response, error)) *http.Client {
	return &http.Client{Transport: &openAIFakeToken{handler: handler}}
}

// waitLoginChan receives one value with a watchdog. The timeout only fires
// when the implementation wedges; it never asserts performance.
func waitLoginChan(t *testing.T, ch <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(30 * time.Second):
		t.Fatalf("timed out waiting for %s", what)
	}
}

// joinLoginDelivery receives one delivery result with a watchdog and fails
// the test on delivery errors.
func joinLoginDelivery(t *testing.T, ch <-chan error, what string) {
	t.Helper()
	select {
	case err := <-ch:
		if err != nil {
			t.Fatalf("%s delivery: %v", what, err)
		}
	case <-time.After(30 * time.Second):
		t.Fatalf("timed out waiting for %s delivery", what)
	}
}

// The real builder/listener/exchange/save path runs with a fake token
// endpoint. A wrong-state and a mixed callback before the valid one neither
// exchange nor abort. L2-L4.
func TestOpenAILoginHappyPath(t *testing.T) {
	authPath := filepath.Join(t.TempDir(), "auth.json")
	var exchanges atomic.Int32
	var gotForm url.Values
	var gotAccept, gotContentType string
	login := &OpenAILogin{
		Path:     authPath,
		DeviceID: openAILoginTestDeviceID,
		HTTP: openAILoginTokenTransport(func(r *http.Request) (*http.Response, error) {
			exchanges.Add(1)
			if r.URL.String() != openAITokenURL {
				return nil, errors.New("unexpected token URL " + r.URL.String())
			}
			body, _ := io.ReadAll(r.Body)
			form, err := url.ParseQuery(string(body))
			if err != nil {
				return nil, err
			}
			gotForm = form
			gotAccept, gotContentType = r.Header.Get("Accept"), r.Header.Get("Content-Type")
			return openAITokenResponse(200, openAILoginValidTokenBody()), nil
		}),
		now: fixedOpenAINow,
	}
	var addr, authURL string
	login.Bind = openAILoginEphemeralBind(&addr)
	ready := func(rawURL string) {
		authURL = rawURL
		parsed, err := url.Parse(rawURL)
		if err != nil {
			t.Fatal(err)
		}
		state := parsed.Query().Get("state")
		// The browser can deliver its callback before its launcher exits.
		statuses, err := deliverOpenAILoginCallbacks(addr,
			"state=wrong-state&code=code-1&client_id=issued-1",
			"state="+state+"&code=code-2&client_id=issued-2&error=access_denied",
			"state="+state+"&code=login-code&client_id=issued-client-99",
		)
		if err != nil {
			t.Fatalf("callback delivery: %v", err)
		}
		if len(statuses) != 3 || statuses[0] != 400 || statuses[1] != 400 || statuses[2] != 200 {
			t.Fatalf("callback statuses = %v", statuses)
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
	query := parsed.Query()
	if parsed.Scheme+"://"+parsed.Host+parsed.Path != openAILoginAuthorizeURL {
		t.Fatalf("authorize URL = %q", authURL)
	}
	if query.Get("client_id") != openAILoginClientID || query.Get("agent_name_hint") != "psi" ||
		query.Get("ext_agent_host_id") != "urn:uuid:"+openAILoginTestDeviceID ||
		query.Get("response_type") != "code" || query.Get("redirect_uri") != openAILoginRedirectURI ||
		query.Get("resource") != openAIResource || query.Get("scope") != openAILoginScope ||
		query.Get("code_challenge_method") != "S256" {
		t.Fatalf("authorize query = %q", parsed.RawQuery)
	}
	for _, name := range []string{"code_challenge", "state", "nonce"} {
		if len(query.Get(name)) != 43 {
			t.Fatalf("%s length = %d, want 43", name, len(query.Get(name)))
		}
	}
	// The S256 challenge must match the verifier the exchange submitted.
	sum := sha256.Sum256([]byte(gotForm.Get("code_verifier")))
	if base64.RawURLEncoding.EncodeToString(sum[:]) != query.Get("code_challenge") {
		t.Fatal("challenge does not match the submitted verifier")
	}
	if gotForm.Get("grant_type") != "authorization_code" || gotForm.Get("client_id") != "issued-client-99" ||
		gotForm.Get("code") != "login-code" || gotForm.Get("redirect_uri") != openAILoginRedirectURI ||
		gotForm.Get("resource") != openAIResource {
		t.Fatalf("exchange form = %v", gotForm)
	}
	if gotAccept != "application/json" || gotContentType != "application/x-www-form-urlencoded" {
		t.Fatalf("exchange headers = %q %q", gotAccept, gotContentType)
	}
	data, err := os.ReadFile(authPath)
	if err != nil {
		t.Fatal(err)
	}
	var all map[string]map[string]any
	if err := json.Unmarshal(data, &all); err != nil {
		t.Fatal(err)
	}
	entry, ok := all["openai"]
	if !ok {
		t.Fatalf("no openai entry in %s", data)
	}
	if len(entry) != 6 || entry["type"] != "oauth" || entry["access"] != "login-access" ||
		entry["refresh"] != "login-refresh" || entry["clientId"] != "issued-client-99" {
		t.Fatalf("stored entry = %v", entry)
	}
	if entry["expires"] != float64(1700000000000)+3600*1000-openAIExpiryMarginMs {
		t.Fatalf("stored expiry = %v", entry["expires"])
	}
	if scopes, ok := entry["scopes"].([]any); !ok || len(scopes) != 6 || scopes[5] != openAIDirectScope {
		t.Fatalf("stored scopes = %v", entry["scopes"])
	}
	if strings.Contains(string(data), "login-id-token") || strings.Contains(string(data), "login-code") {
		t.Fatalf("transient secret persisted in %s", data)
	}
	requireAuthWriteMode(t, authPath, 0o600)
}

// Exactly one valid terminal outcome is published, no matter how many valid
// or invalid callbacks arrive. Invalid requests before the win neither
// consume the latch nor abort the wait. L2.
func TestOpenAILoginCallbackConsumedOnce(t *testing.T) {
	const state = "valid-state-value-for-gate-test"
	gate := newOpenAILoginCallbackGate(state)
	for _, target := range []string{
		"/auth/%63allback?state=" + state + "&code=encoded-path&client_id=issued",
		"/auth/callback?state=" + state + "%20&code=padded-state&client_id=issued",
		"/auth/callback?state=" + state + "&code=code&client_id=issued&error=",
		"/auth/callback?state=" + state + "&error=access_denied&code=",
		"/auth/callback?state=" + state + "&code=code&client_id=issued&client_id=duplicate",
	} {
		request := httptest.NewRequest(http.MethodGet, target, nil)
		request.Host = openAILoginCallbackHost
		recorder := httptest.NewRecorder()
		gate.handler().ServeHTTP(recorder, request)
		if recorder.Code < 400 {
			t.Errorf("invalid callback returned %d: %s", recorder.Code, target)
		}
	}
	server := httptest.NewServer(gate.handler())
	defer server.Close()
	addr := strings.TrimPrefix(server.URL, "http://")
	statuses, err := deliverOpenAILoginCallbacks(addr,
		"state=wrong-state&code=code-1&client_id=issued-1",
		"state="+state+"&code=code-2&client_id=issued-2&error=access_denied",
		"state="+state+"&code=&client_id=",
		"state="+state+"&code=win-code&client_id=win-client",
		"state="+state+"&code=win-code&client_id=win-client",
		"state="+state+"&error=access_denied",
	)
	if err != nil {
		t.Fatal(err)
	}
	want := []int{400, 400, 400, 200, 200, 200}
	if len(statuses) != len(want) {
		t.Fatalf("callback statuses = %v", statuses)
	}
	for i := range want {
		if statuses[i] != want[i] {
			t.Fatalf("callback statuses = %v, want %v", statuses, want)
		}
	}
	select {
	case outcome := <-gate.result:
		if outcome.denied || outcome.code != "win-code" || outcome.clientID != "win-client" {
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

// A state-valid denial ends the attempt with a static failure: no exchange,
// no save. L2, L4.
func TestOpenAILoginDenialFailsWithoutExchange(t *testing.T) {
	authPath := filepath.Join(t.TempDir(), "auth.json")
	var exchanges atomic.Int32
	login := &OpenAILogin{
		Path:     authPath,
		DeviceID: openAILoginTestDeviceID,
		HTTP: openAILoginTokenTransport(func(r *http.Request) (*http.Response, error) {
			exchanges.Add(1)
			return openAITokenResponse(200, openAILoginValidTokenBody()), nil
		}),
		now: fixedOpenAINow,
	}
	var addr string
	login.Bind = openAILoginEphemeralBind(&addr)
	delivered := make(chan error, 1)
	ready := func(rawURL string) {
		go func() {
			parsed, err := url.Parse(rawURL)
			if err != nil {
				delivered <- err
				return
			}
			_, err = deliverOpenAILoginCallbacks(addr, "state="+parsed.Query().Get("state")+"&error=access_denied&client_id=denied-client")
			delivered <- err
		}()
	}
	err := login.Run(context.Background(), ready)
	joinLoginDelivery(t, delivered, "denial")
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

// Cancelling the browser wait writes nothing and releases the listener; a
// retry starts fresh with new secrets. L1, L5.
func TestOpenAILoginCancelBrowserWaitThenRetry(t *testing.T) {
	authPath := filepath.Join(t.TempDir(), "auth.json")
	var addr string
	newLogin := func() *OpenAILogin {
		return &OpenAILogin{
			Path:     authPath,
			DeviceID: openAILoginTestDeviceID,
			HTTP: openAILoginTokenTransport(func(r *http.Request) (*http.Response, error) {
				return openAITokenResponse(200, openAILoginValidTokenBody()), nil
			}),
			Bind: openAILoginEphemeralBind(&addr),
			now:  fixedOpenAINow,
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
	delivered := make(chan error, 1)
	if err := newLogin().Run(context.Background(), func(rawURL string) {
		secondURL = rawURL
		go func() {
			parsed, err := url.Parse(rawURL)
			if err != nil {
				delivered <- err
				return
			}
			_, err = deliverOpenAILoginCallbacks(addr, "state="+parsed.Query().Get("state")+"&code=retry-code&client_id=retry-client")
			delivered <- err
		}()
	}); err != nil {
		t.Fatalf("retry: %v", err)
	}
	joinLoginDelivery(t, delivered, "retry")
	first, _ := url.Parse(firstURL)
	second, _ := url.Parse(secondURL)
	if first.Query().Get("state") == second.Query().Get("state") {
		t.Fatal("retry reused the cancelled state")
	}
}

// A browser callback can finish while the launcher is still returning. If
// login was cancelled by then, the queued code must not be exchanged.
func TestOpenAILoginCancelWithCallbackReady(t *testing.T) {
	authPath := filepath.Join(t.TempDir(), "auth.json")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	transport := &openAIFakeToken{}
	var addr string
	login := &OpenAILogin{
		Path:     authPath,
		DeviceID: openAILoginTestDeviceID,
		Bind:     openAILoginEphemeralBind(&addr),
		HTTP:     &http.Client{Transport: transport},
	}
	err := login.Run(ctx, func(rawURL string) {
		parsed, err := url.Parse(rawURL)
		if err != nil {
			t.Fatal(err)
		}
		statuses, err := deliverOpenAILoginCallbacks(addr, "state="+parsed.Query().Get("state")+"&code=queued-code&client_id=issued-client")
		if err != nil || len(statuses) != 1 || statuses[0] != http.StatusOK {
			t.Fatalf("callback delivery: statuses=%v, err=%v", statuses, err)
		}
		cancel()
	})
	if calls := transport.calls.Load(); !errors.Is(err, context.Canceled) || calls != 0 {
		t.Fatalf("login returned %v after %d exchanges; want cancellation without exchange", err, calls)
	}
	if _, err := os.Stat(authPath); !os.IsNotExist(err) {
		t.Fatalf("cancelled sign-in did not leave auth absent: %v", err)
	}
}

// Cancelling during the exchange writes nothing. L5.
func TestOpenAILoginCancelDuringExchange(t *testing.T) {
	authPath := filepath.Join(t.TempDir(), "auth.json")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var calls atomic.Int32
	login := &OpenAILogin{
		Path:     authPath,
		DeviceID: openAILoginTestDeviceID,
		HTTP: openAILoginTokenTransport(func(r *http.Request) (*http.Response, error) {
			calls.Add(1)
			// Cancel the login while the exchange is in flight, the way
			// Esc cancels a running attempt.
			cancel()
			<-r.Context().Done()
			return nil, r.Context().Err()
		}),
		now: fixedOpenAINow,
	}
	var addr string
	login.Bind = openAILoginEphemeralBind(&addr)
	delivered := make(chan error, 1)
	ready := func(rawURL string) {
		go func() {
			parsed, err := url.Parse(rawURL)
			if err != nil {
				delivered <- err
				return
			}
			_, err = deliverOpenAILoginCallbacks(addr, "state="+parsed.Query().Get("state")+"&code=cancel-code&client_id=cancel-client")
			delivered <- err
		}()
	}
	if err := login.Run(ctx, ready); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled exchange = %v", err)
	}
	joinLoginDelivery(t, delivered, "cancel")
	if calls.Load() != 1 {
		t.Fatalf("exchanges = %d, want 1", calls.Load())
	}
	if _, err := os.Stat(authPath); !os.IsNotExist(err) {
		t.Fatalf("cancelled exchange wrote %v", err)
	}
}

// A changed OpenAI entry between authorization and save is a conflict; the
// concurrent value survives and the login writes nothing. L4.
func TestOpenAILoginConflictOnChangedEntry(t *testing.T) {
	authPath := filepath.Join(t.TempDir(), "auth.json")
	writeAuthDoc(t, authPath, `{"openai":{"type":"oauth","access":"stale-access","refresh":"stale-refresh","expires":1,"clientId":"stale-client","scopes":["chatgpt.tokens.use.direct"]},"other":{"type":"api_key","key":"other-key"}}`)
	login := &OpenAILogin{
		Path:     authPath,
		DeviceID: openAILoginTestDeviceID,
		HTTP: openAILoginTokenTransport(func(r *http.Request) (*http.Response, error) {
			return openAITokenResponse(200, openAILoginValidTokenBody()), nil
		}),
		now: fixedOpenAINow,
	}
	var addr string
	login.Bind = openAILoginEphemeralBind(&addr)
	delivered := make(chan error, 1)
	ready := func(rawURL string) {
		writeAuthDoc(t, authPath, `{"openai":{"type":"oauth","access":"concurrent-access","refresh":"concurrent-refresh","expires":2,"clientId":"concurrent-client","scopes":["chatgpt.tokens.use.direct"]},"other":{"type":"api_key","key":"other-key"}}`)
		go func() {
			parsed, err := url.Parse(rawURL)
			if err != nil {
				delivered <- err
				return
			}
			_, err = deliverOpenAILoginCallbacks(addr, "state="+parsed.Query().Get("state")+"&code=conflict-code&client_id=conflict-client")
			delivered <- err
		}()
	}
	if err := login.Run(context.Background(), ready); !errors.Is(err, errOpenAILoginConflict) {
		t.Fatalf("changed entry = %v", err)
	}
	joinLoginDelivery(t, delivered, "conflict")
	data, err := os.ReadFile(authPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "concurrent-access") || strings.Contains(string(data), "login-access") {
		t.Fatalf("conflict overwrote the concurrent entry: %s", data)
	}
}

// A file that gains only an unrelated provider merges with the new login. L4.
func TestOpenAILoginMergesUnrelatedProvider(t *testing.T) {
	authPath := filepath.Join(t.TempDir(), "auth.json")
	writeAuthDoc(t, authPath, `{"unrelated":{"type":"api_key","key":"unrelated-key"}}`)
	login := &OpenAILogin{
		Path:     authPath,
		DeviceID: openAILoginTestDeviceID,
		HTTP: openAILoginTokenTransport(func(r *http.Request) (*http.Response, error) {
			return openAITokenResponse(200, openAILoginValidTokenBody()), nil
		}),
		now: fixedOpenAINow,
	}
	var addr string
	login.Bind = openAILoginEphemeralBind(&addr)
	delivered := make(chan error, 1)
	ready := func(rawURL string) {
		writeAuthDoc(t, authPath, `{"unrelated":{"type":"api_key","key":"unrelated-key"},"added":{"type":"api_key","key":"added-key"}}`)
		go func() {
			parsed, err := url.Parse(rawURL)
			if err != nil {
				delivered <- err
				return
			}
			_, err = deliverOpenAILoginCallbacks(addr, "state="+parsed.Query().Get("state")+"&code=merge-code&client_id=merge-client")
			delivered <- err
		}()
	}
	if err := login.Run(context.Background(), ready); err != nil {
		t.Fatalf("merge: %v", err)
	}
	joinLoginDelivery(t, delivered, "merge")
	data, err := os.ReadFile(authPath)
	if err != nil {
		t.Fatal(err)
	}
	var all map[string]json.RawMessage
	if err := json.Unmarshal(data, &all); err != nil {
		t.Fatal(err)
	}
	if _, ok := all["openai"]; !ok {
		t.Fatalf("login entry missing in %s", data)
	}
	if !strings.Contains(string(all["unrelated"]), "unrelated-key") || !strings.Contains(string(all["added"]), "added-key") {
		t.Fatalf("unrelated providers lost in %s", data)
	}
}

// A rename failure leaves the old bytes intact and reports no success. L4.
func TestOpenAILoginCommitRejectsStaleAuthDocument(t *testing.T) {
	const original = `{"other":{"type":"api_key","key":"synthetic-old"}}`
	const updated = `{"other":{"type":"api_key","key":"synthetic-new"}}`
	for _, tc := range []struct {
		name, late string
		replace    bool
	}{
		{"other provider changed in place after snapshot", updated, false},
		{"same contents but different file", original, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "auth.json")
			if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
				t.Fatal(err)
			}
			login := &OpenAILogin{Path: path}
			baseline, err := login.captureBaseline(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			login.Chmod = func(temp string, mode os.FileMode) error {
				target := path
				if tc.replace {
					target = filepath.Join(dir, "replacement.json")
				}
				if err := os.WriteFile(target, []byte(tc.late), 0o600); err != nil {
					return err
				}
				if tc.replace {
					if err := os.Rename(target, path); err != nil {
						return err
					}
				}
				return os.Chmod(temp, mode)
			}
			token := openAIToken{access: "login-access", refresh: "login-refresh", expires: 3600000, scopes: []string{openAIDirectScope}}
			if err := login.commit(context.Background(), baseline, token, "issued-client"); !errors.Is(err, errOpenAILoginConflict) {
				t.Fatalf("commit = %v, want conflict", err)
			}
			data, err := os.ReadFile(path)
			if err != nil || string(data) != tc.late {
				t.Fatalf("late auth document was not preserved: %s, %v", data, err)
			}
		})
	}
}

func TestOpenAILoginCommitKeepsAuthDocumentReadable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auth.json")
	const prefix, suffix = `{"other":"`, `"}`
	original := prefix + strings.Repeat("x", openAIAuthDocLimit-len(prefix)-len(suffix)) + suffix
	if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}
	login := &OpenAILogin{Path: path}
	baseline, err := login.captureBaseline(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	token := openAIToken{access: "login-access", refresh: "login-refresh", expires: 3600000, scopes: []string{openAIDirectScope}}
	if err := login.commit(context.Background(), baseline, token, "issued-client"); err == nil || !strings.Contains(err.Error(), "size limit") {
		t.Fatalf("commit = %v, want size limit error", err)
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != original {
		t.Fatal("size refusal did not preserve the original auth document")
	}
}

func TestOpenAILoginRenameFailureKeepsOldBytes(t *testing.T) {
	authPath := filepath.Join(t.TempDir(), "auth.json")
	const before = `{"unrelated":{"type":"api_key","key":"unrelated-key"}}`
	writeAuthDoc(t, authPath, before)
	var renames atomic.Int32
	login := &OpenAILogin{
		Path:     authPath,
		DeviceID: openAILoginTestDeviceID,
		HTTP: openAILoginTokenTransport(func(r *http.Request) (*http.Response, error) {
			return openAITokenResponse(200, openAILoginValidTokenBody()), nil
		}),
		Rename: func(_, _ string) error { renames.Add(1); return errors.New("injected rename failure") },
		now:    fixedOpenAINow,
	}
	var addr string
	login.Bind = openAILoginEphemeralBind(&addr)
	delivered := make(chan error, 1)
	ready := func(rawURL string) {
		go func() {
			parsed, err := url.Parse(rawURL)
			if err != nil {
				delivered <- err
				return
			}
			_, err = deliverOpenAILoginCallbacks(addr, "state="+parsed.Query().Get("state")+"&code=rename-code&client_id=rename-client")
			delivered <- err
		}()
	}
	if err := login.Run(context.Background(), ready); err == nil || !strings.Contains(err.Error(), "cannot save the ChatGPT login") {
		t.Fatalf("rename failure = %v", err)
	}
	joinLoginDelivery(t, delivered, "rename")
	if renames.Load() != 1 {
		t.Fatalf("renames = %d", renames.Load())
	}
	data, err := os.ReadFile(authPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != before {
		t.Fatalf("old bytes changed to %s", data)
	}
}

// Every invalid token response fails the login without a retry or a save:
// exactly one exchange attempt, static diagnostics, no secret reflection.
// L3, L8-L10.
func TestOpenAILoginTokenContract(t *testing.T) {
	validScope := "openid profile email offline_access resource.invoke chatgpt.tokens.use.direct"
	bodies := map[string]string{
		"missing id token":     `{"access_token":"a","refresh_token":"r","scope":"` + validScope + `","expires_in":3600}`,
		"non-string id token":  `{"access_token":"a","refresh_token":"r","scope":"` + validScope + `","expires_in":3600,"id_token":42}`,
		"blank id token":       `{"access_token":"a","refresh_token":"r","scope":"` + validScope + `","expires_in":3600,"id_token":"  "}`,
		"missing direct scope": `{"access_token":"a","refresh_token":"r","scope":"openid profile","expires_in":3600,"id_token":"id"}`,
		"missing access":       `{"refresh_token":"r","scope":"` + validScope + `","expires_in":3600,"id_token":"id"}`,
		"missing refresh":      `{"access_token":"a","scope":"` + validScope + `","expires_in":3600,"id_token":"id"}`,
		"missing scope":        `{"access_token":"a","refresh_token":"r","expires_in":3600,"id_token":"id"}`,
		"zero lifetime":        `{"access_token":"a","refresh_token":"r","scope":"` + validScope + `","expires_in":0,"id_token":"id"}`,
		"non-numeric lifetime": `{"access_token":"a","refresh_token":"r","scope":"` + validScope + `","expires_in":"soon","id_token":"id"}`,
		"malformed JSON":       `{"access_token":`,
		"oversized body":       `{"access_token":"` + strings.Repeat("a", openAITokenBodyLimit+1) + `"}`,
	}
	statuses := map[string]int{
		"bad request":  400,
		"unauthorized": 401,
		"rate limited": 429,
		"server error": 500,
	}
	run := func(t *testing.T, status int, body string) {
		t.Helper()
		authPath := filepath.Join(t.TempDir(), "auth.json")
		var exchanges atomic.Int32
		login := &OpenAILogin{
			Path:     authPath,
			DeviceID: openAILoginTestDeviceID,
			HTTP: openAILoginTokenTransport(func(r *http.Request) (*http.Response, error) {
				exchanges.Add(1)
				return openAITokenResponse(status, body), nil
			}),
			now: fixedOpenAINow,
		}
		var addr string
		login.Bind = openAILoginEphemeralBind(&addr)
		delivered := make(chan error, 1)
		ready := func(rawURL string) {
			go func() {
				parsed, err := url.Parse(rawURL)
				if err != nil {
					delivered <- err
					return
				}
				_, err = deliverOpenAILoginCallbacks(addr, "state="+parsed.Query().Get("state")+"&code=contract-code&client_id=contract-client")
				delivered <- err
			}()
		}
		err := login.Run(context.Background(), ready)
		joinLoginDelivery(t, delivered, "contract")
		if err == nil {
			t.Fatal("invalid token response accepted")
		}
		if strings.Contains(err.Error(), "contract-code") || strings.Contains(err.Error(), body) {
			t.Fatalf("error reflected a secret: %v", err)
		}
		if exchanges.Load() != 1 {
			t.Fatalf("exchanges = %d, want exactly 1", exchanges.Load())
		}
		if _, err := os.Stat(authPath); !os.IsNotExist(err) {
			t.Fatalf("invalid token response wrote %v", err)
		}
	}
	for name, body := range bodies {
		t.Run(name, func(t *testing.T) { run(t, 200, body) })
	}
	for name, status := range statuses {
		t.Run(name, func(t *testing.T) { run(t, status, `{"error":"denied"}`) })
	}
}

// A token-endpoint redirect is never followed: the exchange fails without
// requesting the redirect target. L9.
func TestOpenAILoginTokenRedirectNotFollowed(t *testing.T) {
	authPath := filepath.Join(t.TempDir(), "auth.json")
	var evil atomic.Int32
	login := &OpenAILogin{
		Path:     authPath,
		DeviceID: openAILoginTestDeviceID,
		HTTP: openAILoginTokenTransport(func(r *http.Request) (*http.Response, error) {
			if !strings.HasPrefix(r.URL.String(), "https://auth.openai.com/") {
				evil.Add(1)
			}
			resp := openAITokenResponse(302, "")
			resp.Header.Set("Location", "https://evil.example/token")
			return resp, nil
		}),
		now: fixedOpenAINow,
	}
	var addr string
	login.Bind = openAILoginEphemeralBind(&addr)
	delivered := make(chan error, 1)
	ready := func(rawURL string) {
		go func() {
			parsed, err := url.Parse(rawURL)
			if err != nil {
				delivered <- err
				return
			}
			_, err = deliverOpenAILoginCallbacks(addr, "state="+parsed.Query().Get("state")+"&code=redirect-code&client_id=redirect-client")
			delivered <- err
		}()
	}
	if err := login.Run(context.Background(), ready); err == nil {
		t.Fatal("redirect accepted")
	}
	joinLoginDelivery(t, delivered, "redirect")
	if evil.Load() != 0 {
		t.Fatal("redirect target requested")
	}
	if _, err := os.Stat(authPath); !os.IsNotExist(err) {
		t.Fatalf("redirect wrote %v", err)
	}
}

// A stored OAuth login requires the official endpoint for both Check and
// Get. API-key entries keep working on overrides. L7-L9.
func TestOpenAIEndpointBoundary(t *testing.T) {
	const override = "https://proxy.example/v1"
	oauthDoc := `{"openai":{"type":"oauth","access":"endpoint-access","refresh":"endpoint-refresh","expires":` + openAIEndpointFuture(t) + `,"clientId":"endpoint-client","scopes":["chatgpt.tokens.use.direct"]}}`
	dir := t.TempDir()
	oauthPath := filepath.Join(dir, "oauth.json")
	writeAuthDoc(t, oauthPath, oauthDoc)
	auth := &OpenAIAuth{Path: oauthPath, Env: map[string]string{}}
	if err := auth.CheckForEndpoint("https://api.openai.com/v1"); err != nil {
		t.Fatalf("official Check: %v", err)
	}
	if err := auth.CheckForEndpoint("https://api.openai.com/v1/"); err != nil {
		t.Fatalf("trailing-slash Check: %v", err)
	}
	if err := auth.CheckForEndpoint(override); !errors.Is(err, errOpenAIOAuthEndpoint) {
		t.Fatalf("override Check = %v", err)
	}
	if key, err := auth.GetForEndpoint(context.Background(), "https://api.openai.com/v1"); err != nil || key != "endpoint-access" {
		t.Fatalf("official Get = %q, %v", key, err)
	}
	var exchanges atomic.Int32
	auth.HTTP = &http.Client{Transport: &openAIFakeToken{handler: func(r *http.Request) (*http.Response, error) {
		exchanges.Add(1)
		return openAITokenResponse(200, openAILoginValidTokenBody()), nil
	}}}
	if _, err := auth.GetForEndpoint(context.Background(), override); !errors.Is(err, errOpenAIOAuthEndpoint) {
		t.Fatalf("override Get = %v", err)
	}
	if exchanges.Load() != 0 {
		t.Fatalf("override Get exchanged %d times", exchanges.Load())
	}
	keyPath := filepath.Join(dir, "key.json")
	writeAuthDoc(t, keyPath, `{"openai":{"type":"api_key","key":"direct-key"}}`)
	keyAuth := &OpenAIAuth{Path: keyPath, Env: map[string]string{}}
	if err := keyAuth.CheckForEndpoint(override); err != nil {
		t.Fatalf("API-key override Check: %v", err)
	}
	if key, err := keyAuth.GetForEndpoint(context.Background(), override); err != nil || key != "direct-key" {
		t.Fatalf("API-key override Get = %q, %v", key, err)
	}
}

func openAIEndpointFuture(t *testing.T) string {
	t.Helper()
	return strconv.FormatInt(time.Now().Add(2*time.Hour).UnixMilli(), 10)
}

// A directory at the auth path is refused on every read path without a
// fallback. The FIFO variant lives in the Unix-gated fixture. L4, L8.
func TestOpenAINonRegularAuthDirectory(t *testing.T) {
	dir := t.TempDir()
	authPath := filepath.Join(dir, "auth.json")
	if err := os.Mkdir(authPath, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := AuthEntries(authPath); err == nil {
		t.Fatal("directory accepted by AuthEntries")
	}
	auth := &OpenAIAuth{Path: authPath, Env: map[string]string{"OPENAI_API_KEY": "env-key"}}
	if err := auth.Check(); err == nil {
		t.Fatal("directory accepted by Check")
	}
	if _, err := auth.Get(context.Background()); err == nil {
		t.Fatal("directory accepted by Get")
	}
	login := &OpenAILogin{Path: authPath, DeviceID: openAILoginTestDeviceID}
	if err := login.Run(context.Background(), nil); err == nil {
		t.Fatal("directory accepted by login")
	}
}
