package provider

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"maps"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"time"
)

// OpenAI ChatGPT sign-in constants, matching Pi 1.5.2's public-client flow.
// The agent name is psi; every other contract value is Pi's.
const (
	openAILoginAuthorizeURL = "https://auth.openai.com/api/accounts/authorize"
	openAILoginClientID     = "dynamic_agent_client"
	openAILoginAgentName    = "psi"
	openAILoginRedirectURI  = "http://127.0.0.1:1455/auth/callback"
	openAILoginCallbackAddr = "127.0.0.1:1455"
	openAILoginCallbackHost = "127.0.0.1:1455"
	openAILoginCallbackPath = "/auth/callback"
	openAILoginScope        = "openid profile email offline_access resource.invoke chatgpt.tokens.use.direct"
	// openAILoginWaitTimeout bounds one browser authorization wait. The
	// caller context still cancels earlier, and the code exchange has its
	// own shorter deadline.
	openAILoginWaitTimeout = 5 * time.Minute
	// openAILoginExchangeTimeout bounds the single authorization-code
	// exchange, matching the refresh deadline.
	openAILoginExchangeTimeout = 15 * time.Second
	// openAILoginServerTimeout bounds each phase of one local callback
	// request so an incomplete connection cannot hold the wait.
	openAILoginServerTimeout = 5 * time.Second
	// openAILoginQueryLimit and openAILoginHeaderLimit bound unsolicited
	// local input. The expected callback carries a code, an issued client
	// ID, and state in well under a kilobyte.
	openAILoginQueryLimit  = 8 * 1024
	openAILoginHeaderLimit = 16 * 1024
)

// Static sign-in failures. None echoes a code, token, query, response body,
// or transport error.
var (
	errOpenAILoginConflict = errors.New("OpenAI credentials changed during sign-in; refusing overwrite")
	errOpenAILoginLockLost = errors.New("auth.json lock changed during sign-in; refusing overwrite")
)

// OpenAILogin runs one interactive ChatGPT sign-in: it captures the stored
// OpenAI baseline, serves one loopback authorization callback, exchanges
// the code once, and atomically saves the resulting OAuth entry. Browser
// launching stays in the app; ready receives the authorization URL while
// the listener serves callbacks and must return before the exchange starts.
//
// Bind creates the callback listener and is the one test seam. Nil binds
// the fixed production loopback address; tests supply an ephemeral
// listener. There is deliberately no per-instance mutex: one Run owns one
// attempt, and a cancelled attempt is never resumed.
type OpenAILogin struct {
	Path     string
	DeviceID string
	HTTP     *http.Client
	Bind     func() (net.Listener, error)
	Chmod    func(string, os.FileMode) error
	Rename   func(string, string) error
	// now selects the clock for the saved expiry. Nil means time.Now.
	now func() time.Time
}

func (l *OpenAILogin) nowOrNow() time.Time {
	if l.now != nil {
		return l.now()
	}
	return time.Now()
}

func (l *OpenAILogin) client() *http.Client {
	if l.HTTP != nil {
		return l.HTTP
	}
	return http.DefaultClient
}

// Run executes one complete attempt. A nil error means the credential was
// saved. A context error means the caller cancelled before commit and
// nothing was written. Any other error is a static, non-retryable failure;
// the caller starts a fresh Run rather than reusing the consumed code.
func (l *OpenAILogin) Run(ctx context.Context, ready func(authURL string)) error {
	if strings.TrimSpace(l.DeviceID) == "" {
		return fmt.Errorf("ChatGPT sign-in requires a device ID")
	}
	if l.Path == "" {
		return fmt.Errorf("cannot save the ChatGPT login without an auth path")
	}
	if err := os.MkdirAll(filepath.Dir(l.Path), 0o700); err != nil {
		return fmt.Errorf("cannot prepare the ChatGPT login")
	}
	baseline, err := l.captureBaseline(ctx)
	if err != nil {
		return err
	}
	verifier, challenge, state, nonce, err := openAILoginSecrets()
	if err != nil {
		return fmt.Errorf("cannot start the ChatGPT sign-in")
	}
	bind := l.Bind
	if bind == nil {
		bind = func() (net.Listener, error) {
			return net.Listen("tcp", openAILoginCallbackAddr)
		}
	}
	listener, err := bind()
	if err != nil {
		if errors.Is(err, syscall.EADDRINUSE) {
			return fmt.Errorf("port 1455 is in use, probably by another unfinished login; cancel that login and try /login openai again")
		}
		return fmt.Errorf("cannot start the ChatGPT sign-in callback")
	}
	code, clientID, err := waitOpenAILoginCallback(ctx, listener, state, func() {
		if ready != nil {
			ready(openAILoginAuthURL(challenge, state, nonce, l.DeviceID))
		}
	})
	if err != nil {
		return err
	}
	// The callback can win the select even if cancellation is already
	// ready. Do not start exchanging the code after that cancellation.
	if err := ctx.Err(); err != nil {
		return err
	}
	exchangeCtx, cancel := context.WithTimeout(ctx, openAILoginExchangeTimeout)
	defer cancel()
	token, err := l.exchangeCode(exchangeCtx, code, verifier, clientID)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return err
	}
	return l.commit(ctx, baseline, token, clientID)
}

// openAILoginBaseline pins the stored OpenAI entry across the browser wait:
// whether the file existed, whether it held an openai value, and that
// value's complete raw JSON.
type openAILoginBaseline struct {
	file    bool
	present bool
	raw     json.RawMessage
}

// captureBaseline reads the stored document under a short shared lease and
// releases the lease before any browser interaction. Symlinks and
// non-regular files are refused outright: unlike refresh, an explicit login
// never follows a link it did not create.
func (l *OpenAILogin) captureBaseline(ctx context.Context) (openAILoginBaseline, error) {
	var baseline openAILoginBaseline
	unlock, _, err := acquireAuthLock(ctx, l.Path+".lock", 0, 0, openAIAuthLockWait)
	if err != nil {
		if ctx.Err() != nil {
			return baseline, ctx.Err()
		}
		return baseline, fmt.Errorf("auth.json is busy; retry /login openai")
	}
	defer unlock()
	all, info, err := readRegularOpenAIAuthDoc(l.Path)
	if err != nil {
		return baseline, err
	}
	baseline.file = info != nil
	baseline.raw, baseline.present = all["openai"]
	return baseline, nil
}

// openAILoginSecrets draws fresh PKCE, state, and nonce values for one
// attempt. Each value holds 32 random bytes, as in Pi: unpadded base64url
// encodes 32 bytes as 43 characters, inside PKCE's 43-128 range, with 256
// bits of entropy. The challenge is the S256 transform of the verifier.
func openAILoginSecrets() (verifier, challenge, state, nonce string, err error) {
	random := func() (string, error) {
		var raw [32]byte
		if _, err := rand.Read(raw[:]); err != nil {
			return "", err
		}
		return base64.RawURLEncoding.EncodeToString(raw[:]), nil
	}
	if verifier, err = random(); err != nil {
		return "", "", "", "", err
	}
	if state, err = random(); err != nil {
		return "", "", "", "", err
	}
	if nonce, err = random(); err != nil {
		return "", "", "", "", err
	}
	sum := sha256.Sum256([]byte(verifier))
	return verifier, base64.RawURLEncoding.EncodeToString(sum[:]), state, nonce, nil
}

// openAILoginAuthURL builds the authorization URL from Pi's public-client
// contract. Values.Encode emits keys in sorted order, so the URL is stable
// for tests as well as the browser.
func openAILoginAuthURL(challenge, state, nonce, deviceID string) string {
	form := url.Values{
		"client_id":             {openAILoginClientID},
		"agent_name_hint":       {openAILoginAgentName},
		"ext_agent_host_id":     {"urn:uuid:" + strings.ToLower(deviceID)},
		"response_type":         {"code"},
		"redirect_uri":          {openAILoginRedirectURI},
		"resource":              {openAIResource},
		"scope":                 {openAILoginScope},
		"state":                 {state},
		"code_challenge":        {challenge},
		"code_challenge_method": {"S256"},
		"nonce":                 {nonce},
	}
	return openAILoginAuthorizeURL + "?" + form.Encode()
}

// Static browser pages. They carry no interpolated values, so a callback
// query can never reflect into the browser. The success page says only
// that authorization returned, not that the credential persisted.
const (
	openAILoginSuccessPage = `<!doctype html><html><head><meta charset="utf-8"><title>psi sign-in</title></head><body><p>ChatGPT sign-in returned to psi. You can close this window.</p></body></html>`
	openAILoginDeniedPage  = `<!doctype html><html><head><meta charset="utf-8"><title>psi sign-in</title></head><body><p>ChatGPT sign-in was not completed. You can close this window.</p></body></html>`
	openAILoginInvalidPage = `<!doctype html><html><head><meta charset="utf-8"><title>psi sign-in</title></head><body><p>Invalid sign-in request.</p></body></html>`
)

// openAILoginOutcome is one validated terminal callback: either a denial
// or a code with its issued client ID.
type openAILoginOutcome struct {
	code     string
	clientID string
	denied   bool
}

// parseOpenAILoginCallback validates one callback request. It returns the
// status for the static invalid page when the request is unusable. The
// single nonblank state is checked before denial and success alike, so a
// wrong-state denial cannot abort the legitimate pending login. Invalid
// requests leave the wait active.
func parseOpenAILoginCallback(r *http.Request, expectedState string) (openAILoginOutcome, int, bool) {
	var outcome openAILoginOutcome
	if r.Method != http.MethodGet || r.Host != openAILoginCallbackHost || r.URL == nil ||
		r.URL.IsAbs() || r.URL.EscapedPath() != openAILoginCallbackPath {
		return outcome, http.StatusNotFound, false
	}
	// Bound the raw query before parsing so an oversized value never
	// reaches the query parser. Headers are bounded by MaxHeaderBytes.
	if len(r.URL.RawQuery) > openAILoginQueryLimit {
		return outcome, http.StatusBadRequest, false
	}
	values, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		return outcome, http.StatusBadRequest, false
	}
	for _, name := range []string{"state", "code", "client_id", "error"} {
		if len(values[name]) > 1 {
			return outcome, http.StatusBadRequest, false
		}
	}
	if state := values.Get("state"); state == "" || state != expectedState {
		return outcome, http.StatusBadRequest, false
	}
	code, clientID, denial := values.Get("code"), values.Get("client_id"), values.Get("error")
	nonblank := func(value string) bool {
		return value != "" && value == strings.TrimSpace(value)
	}
	switch {
	case !values.Has("code") && nonblank(denial):
		// A denial can carry client_id metadata; it is ignored rather
		// than rejecting a correlated denial.
		outcome.denied = true
	case !values.Has("error") && nonblank(code) && nonblank(clientID):
		outcome.code, outcome.clientID = code, clientID
	default:
		return outcome, http.StatusBadRequest, false
	}
	return outcome, http.StatusOK, true
}

// writeOAuthCallbackPage sends one static browser page. A disconnected
// browser does not invalidate an accepted code.
func writeOAuthCallbackPage(w http.ResponseWriter, status int, page string) {
	header := w.Header()
	header.Set("Content-Type", "text/html; charset=utf-8")
	header.Set("Cache-Control", "no-store")
	header.Set("Referrer-Policy", "no-referrer")
	// The server may close immediately after the flush. A fixed length
	// lets the browser finish reading the page without a final chunk.
	header.Set("Content-Length", strconv.Itoa(len(page)))
	w.WriteHeader(status)
	_, _ = io.WriteString(w, page)
	if flusher, ok := w.(http.Flusher); ok {
		flusher.Flush()
	}
}

// openAILoginCallbackGate owns one attempt's callback latch and terminal
// outcome. Exactly one valid outcome is claimed and published once to the
// buffered channel; invalid requests and late duplicates have no effect.
// A gate is always used by pointer: the latch must never be copied.
type openAILoginCallbackGate struct {
	state   string
	result  chan openAILoginOutcome
	claimed atomic.Bool
}

func newOpenAILoginCallbackGate(state string) *openAILoginCallbackGate {
	return &openAILoginCallbackGate{state: state, result: make(chan openAILoginOutcome, 1)}
}

func (g *openAILoginCallbackGate) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		outcome, status, ok := parseOpenAILoginCallback(r, g.state)
		if !ok {
			writeOAuthCallbackPage(w, status, openAILoginInvalidPage)
			return
		}
		if !g.claimed.CompareAndSwap(false, true) {
			writeOAuthCallbackPage(w, http.StatusOK, openAILoginSuccessPage)
			return
		}
		if outcome.denied {
			writeOAuthCallbackPage(w, http.StatusBadRequest, openAILoginDeniedPage)
		} else {
			writeOAuthCallbackPage(w, http.StatusOK, openAILoginSuccessPage)
		}
		g.result <- outcome
	})
}

// waitOpenAILoginCallback serves the bound listener until one valid
// callback, denial, timeout, or cancellation. The listener and all
// accepted connections close on every path, and the owned Serve goroutine
// is joined before return.
func waitOpenAILoginCallback(ctx context.Context, listener net.Listener, state string, ready func()) (string, string, error) {
	gate := newOpenAILoginCallbackGate(state)
	server := &http.Server{
		Handler:           gate.handler(),
		ReadTimeout:       openAILoginServerTimeout,
		ReadHeaderTimeout: openAILoginServerTimeout,
		WriteTimeout:      openAILoginServerTimeout,
		IdleTimeout:       openAILoginServerTimeout,
		MaxHeaderBytes:    openAILoginHeaderLimit,
		// Raw server logging stays suppressed; the operation reports
		// only static failures.
		ErrorLog: log.New(io.Discard, "", 0),
	}
	serveDone := make(chan struct{})
	go func() {
		defer close(serveDone)
		_ = server.Serve(listener)
	}()
	defer func() {
		_ = server.Close()
		<-serveDone
	}()
	timer := time.NewTimer(openAILoginWaitTimeout)
	defer timer.Stop()
	ready()
	select {
	case outcome := <-gate.result:
		if outcome.denied {
			return "", "", fmt.Errorf("ChatGPT sign-in was not completed")
		}
		return outcome.code, outcome.clientID, nil
	case <-serveDone:
		return "", "", fmt.Errorf("ChatGPT sign-in callback failed")
	case <-timer.C:
		return "", "", fmt.Errorf("ChatGPT sign-in timed out; try /login openai again")
	case <-ctx.Done():
		return "", "", ctx.Err()
	}
}

// exchangeCode performs the single authorization-code exchange. It never
// retries: a code can be consumed even when its response is lost, so any
// failure starts over with a fresh login.
func (l *OpenAILogin) exchangeCode(ctx context.Context, code, verifier, clientID string) (openAIToken, error) {
	form := url.Values{
		"grant_type":    {"authorization_code"},
		"client_id":     {clientID},
		"code":          {code},
		"code_verifier": {verifier},
		"redirect_uri":  {openAILoginRedirectURI},
		"resource":      {openAIResource},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, openAITokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return openAIToken{}, fmt.Errorf("invalid ChatGPT sign-in endpoint")
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	data, err := doOpenAITokenRequest(l.client(), req)
	if err != nil {
		switch {
		case errors.Is(err, errOpenAITokenTransport):
			return openAIToken{}, fmt.Errorf("ChatGPT sign-in request failed; start /login openai again")
		case errors.Is(err, errOpenAITokenStatus):
			return openAIToken{}, fmt.Errorf("ChatGPT sign-in failed; start /login openai again")
		default:
			return openAIToken{}, fmt.Errorf("invalid ChatGPT sign-in response; start /login openai again")
		}
	}
	resp, ok := decodeOpenAITokenResponse(data)
	if !ok {
		return openAIToken{}, fmt.Errorf("invalid ChatGPT sign-in response; start /login openai again")
	}
	if !resp.hasIDToken() {
		return openAIToken{}, fmt.Errorf("invalid ChatGPT sign-in response; start /login openai again")
	}
	token, ok := resp.credential(l.nowOrNow())
	if !ok {
		return openAIToken{}, fmt.Errorf("invalid ChatGPT sign-in response; start /login openai again")
	}
	return token, nil
}

// commit saves the exchanged credential under a fresh shared lease. An
// unrelated provider change survives; a changed, replaced, or deleted
// OpenAI entry is a conflict, never a last-writer-wins overwrite. Once the
// atomic save begins, it finishes synchronously before cancellation is
// acknowledged.
func (l *OpenAILogin) commit(ctx context.Context, baseline openAILoginBaseline, token openAIToken, clientID string) error {
	unlock, valid, err := acquireAuthLock(ctx, l.Path+".lock", 0, 0, openAIAuthLockWait)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("auth.json is busy; retry /login openai")
	}
	defer unlock()
	current, info, err := l.checkLoginBaseline(baseline)
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	// An explicit login replaces the identity with exactly the defined
	// OAuth fields. No old API key or invalid-entry field survives into
	// the new credential; the code, verifier, nonce, and ID token are
	// never stored.
	entry, err := json.Marshal(map[string]any{
		"type":     "oauth",
		"access":   token.access,
		"refresh":  token.refresh,
		"expires":  token.expires,
		"clientId": clientID,
		"scopes":   token.scopes,
	})
	if err != nil {
		return fmt.Errorf("cannot save the ChatGPT login")
	}
	next := maps.Clone(current)
	next["openai"] = entry
	encoded, err := encodeOpenAIAuthDoc(next)
	if err != nil {
		return err
	}
	validate := func() error {
		latest, latestInfo, err := l.checkLoginBaseline(baseline)
		if err != nil {
			return err
		}
		// Merge changes made during the browser wait, but never discard
		// a write made after we took the snapshot used to build next.
		if !sameOpenAIAuthFile(info, latestInfo) || !maps.EqualFunc(current, latest, equalAuthEntry) {
			return errOpenAILoginConflict
		}
		if !valid() {
			return errOpenAILoginLockLost
		}
		return nil
	}
	if err := persistAuthDoc(encoded, l.Path, validate, authFileHooks{Chmod: l.Chmod, Rename: l.Rename}); err != nil {
		if errors.Is(err, errOpenAILoginConflict) || errors.Is(err, errOpenAILoginLockLost) || errors.Is(err, errOpenAIAuthUnsafe) {
			return err
		}
		return fmt.Errorf("cannot save the ChatGPT login")
	}
	return nil
}

// checkLoginBaseline rereads the document and compares the stored OpenAI
// entry with the captured baseline. A file that appeared since an absent
// baseline merges only when it holds no OpenAI entry.
func (l *OpenAILogin) checkLoginBaseline(baseline openAILoginBaseline) (map[string]json.RawMessage, os.FileInfo, error) {
	current, info, err := readRegularOpenAIAuthDoc(l.Path)
	if err != nil {
		if errors.Is(err, errOpenAIAuthUnsafe) {
			return nil, nil, err
		}
		return nil, nil, errOpenAILoginConflict
	}
	raw, present := current["openai"]
	if (baseline.file && info == nil) || present != baseline.present || !bytes.Equal(raw, baseline.raw) {
		return nil, nil, errOpenAILoginConflict
	}
	return current, info, nil
}
