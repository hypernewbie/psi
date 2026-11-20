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
	"math"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/hypernewbie/psi/internal/secretmem"
)

// OpenAI Codex sign-in constants. The client ID, token endpoint, credential
// type, and account parser are shared with refresh in codex_auth.go. The
// authorization endpoint is Pi 1.5.2's public-client contract; psi binds its
// own IPv4 loopback listener and derives the redirect from it.
const (
	codexLoginAuthorizeURL  = "https://auth.openai.com/oauth/authorize"
	codexLoginCallbackAddr  = "127.0.0.1:1455"
	codexLoginCallbackRetry = "127.0.0.1:1457"
	codexLoginCallbackPath  = "/auth/callback"
	codexLoginScope         = "openid profile email offline_access"
	codexLoginOriginator    = "psi"
	// codexLoginExchangeTimeout bounds the single authorization-code
	// exchange, including its response body read.
	codexLoginExchangeTimeout = 15 * time.Second
	// codexLoginServerTimeout bounds each phase of one local callback
	// request so an incomplete connection cannot hold the wait.
	codexLoginServerTimeout = 5 * time.Second
	// codexLoginQueryLimit and codexLoginHeaderLimit bound unsolicited
	// local input. A valid callback carries one state and one code.
	codexLoginQueryLimit = 8 * 1024
	// codexLoginPasteLimit is the same bound for one manual-input line.
	codexLoginPasteLimit  = codexLoginQueryLimit
	codexLoginHeaderLimit = 16 * 1024
)

// Static Codex sign-in failures. None echoes a code, verifier, token, query,
// response body, or transport error.
var (
	errCodexLoginPasteEmpty   = errors.New("paste the OpenAI Codex redirect URL, code#state, query string, or code")
	errCodexLoginPasteInvalid = errors.New("invalid OpenAI Codex sign-in input; paste the redirect URL, code#state, query string, or code")
	errCodexLoginPasteState   = errors.New("OpenAI Codex sign-in state does not match; paste the current redirect")
	errCodexLoginConflict     = errors.New("OpenAI Codex credentials changed during sign-in; refusing overwrite")
	errCodexLoginLockLost     = errors.New("auth.json lock changed during sign-in; refusing overwrite")
	// ErrCodexLoginClaimed means a callback or a previous manual submission
	// already claimed this attempt. The winning input still finishes.
	ErrCodexLoginClaimed = errors.New("OpenAI Codex sign-in is already completing")
)

// CodexLogin runs one interactive Codex sign-in: it captures the stored
// openai-codex baseline, serves one loopback authorization callback,
// exchanges the code once, and atomically saves the resulting OAuth entry.
// Browser launching stays in the app; ready receives the authorization URL
// while the listener serves callbacks and must return before the exchange
// starts.
//
// Bind, Chmod, and Rename are test seams. Bind receives the requested
// loopback address; nil uses net.Listen. Nil filesystem hooks use os
// defaults. There is deliberately no per-instance mutex: one Run owns one
// attempt, and a cancelled attempt is never resumed.
type CodexLogin struct {
	Path   string
	HTTP   *http.Client
	Bind   func(addr string) (net.Listener, error)
	Chmod  func(string, os.FileMode) error
	Rename func(string, string) error
	// now selects the clock for the saved expiry. Nil means time.Now.
	now func() time.Time
}

func (l *CodexLogin) nowOrNow() time.Time {
	if l.now != nil {
		return l.now()
	}
	return time.Now()
}

func (l *CodexLogin) client() *http.Client {
	if l.HTTP != nil {
		return l.HTTP
	}
	return http.DefaultClient
}

// Run executes one complete attempt. A nil error means the credential was
// saved. A context error means the caller cancelled before commit and
// nothing was written. Any other error is a static, non-retryable failure;
// the caller starts a fresh Run rather than reusing the consumed code. The
// browser wait has no new overall deadline; cancellation is the only bound.
func (l *CodexLogin) Run(ctx context.Context, ready func(authURL string)) error {
	return l.run(ctx, func(authURL string, _ bool, _ func([]byte) error) {
		if ready != nil {
			ready(authURL)
		}
	})
}

// RunInteractive is the browser path with the method-specific details the
// REPL needs: whether the OS assigned the callback port, and a nonblocking
// submit that parses one pasted redirect or code and offers it to the same
// one-shot gate as the browser callback. submit never makes a network request
// and borrows its input only for the call.
func (l *CodexLogin) RunInteractive(ctx context.Context, ready func(authURL string, osAssigned bool, submit func([]byte) error)) error {
	return l.run(ctx, ready)
}

func (l *CodexLogin) run(ctx context.Context, ready func(authURL string, osAssigned bool, submit func([]byte) error)) error {
	if l.Path == "" {
		return fmt.Errorf("cannot save the OpenAI Codex login without an auth path")
	}
	if err := os.MkdirAll(filepath.Dir(l.Path), 0o700); err != nil {
		return fmt.Errorf("cannot prepare the OpenAI Codex login")
	}
	baseline, err := l.captureBaseline(ctx)
	if err != nil {
		return err
	}
	defer secretmem.Wipe(baseline.raw[:cap(baseline.raw)])
	verifier, challenge, state, err := codexLoginSecrets()
	if err != nil {
		return fmt.Errorf("cannot start the OpenAI Codex sign-in")
	}
	listener, osAssigned, err := l.bind()
	if err != nil {
		return fmt.Errorf("cannot start the OpenAI Codex browser sign-in; choose device code login instead")
	}
	redirectURI, host, err := codexLoginRedirect(listener)
	if err != nil {
		_ = listener.Close()
		return err
	}
	gate := newCodexLoginCallbackGate(host, state)
	code, err := waitCodexLoginCallback(ctx, listener, gate, func() {
		if ready != nil {
			ready(codexLoginAuthURL(challenge, state, redirectURI), osAssigned, gate.submit)
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
	exchangeCtx, cancel := context.WithTimeout(ctx, codexLoginExchangeTimeout)
	defer cancel()
	token, err := l.exchangeCode(exchangeCtx, code, verifier, redirectURI)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return err
	}
	return l.commit(ctx, baseline, token)
}

// bind tries the two documented Codex callback ports in order. Only a real
// address-in-use failure advances to the next choice; a permission or other
// bind failure is terminal. If both ports are busy, one OS-assigned port is
// the final best-effort choice. The incumbent listener is never probed or
// stopped.
func (l *CodexLogin) bind() (net.Listener, bool, error) {
	bind := l.Bind
	if bind == nil {
		bind = func(addr string) (net.Listener, error) {
			return net.Listen("tcp4", addr)
		}
	}
	for _, addr := range []string{codexLoginCallbackAddr, codexLoginCallbackRetry} {
		listener, err := bind(addr)
		if err == nil {
			return listener, false, nil
		}
		if !codexLoginAddrInUse(err) {
			return nil, false, err
		}
	}
	listener, err := bind("127.0.0.1:0")
	if err != nil {
		return nil, false, err
	}
	return listener, true, nil
}

// codexLoginRedirect derives the callback URL and Host authority from the
// listener psi actually owns. A non-IPv4 or non-loopback address is refused
// instead of becoming an authorization redirect.
func codexLoginRedirect(listener net.Listener) (redirectURI, host string, err error) {
	addr, ok := listener.Addr().(*net.TCPAddr)
	if !ok || addr.Port <= 0 {
		return "", "", fmt.Errorf("cannot start the OpenAI Codex sign-in callback")
	}
	ipv4 := addr.IP.To4()
	if ipv4 == nil || !ipv4.IsLoopback() {
		return "", "", fmt.Errorf("cannot start the OpenAI Codex sign-in callback")
	}
	host = net.JoinHostPort(ipv4.String(), strconv.Itoa(addr.Port))
	return "http://" + host + codexLoginCallbackPath, host, nil
}

// codexLoginSecrets draws fresh PKCE and state values for one attempt. Each
// value holds 32 random bytes; unpadded base64url encodes them as 43
// characters, inside PKCE's 43-128 range, with 256 bits of entropy.
func codexLoginSecrets() (verifier, challenge, state string, err error) {
	random := func() (string, error) {
		var raw [32]byte
		if _, err := rand.Read(raw[:]); err != nil {
			return "", err
		}
		return base64.RawURLEncoding.EncodeToString(raw[:]), nil
	}
	if verifier, err = random(); err != nil {
		return "", "", "", err
	}
	if state, err = random(); err != nil {
		return "", "", "", err
	}
	sum := sha256.Sum256([]byte(verifier))
	return verifier, base64.RawURLEncoding.EncodeToString(sum[:]), state, nil
}

// codexLoginAuthURL builds the authorization URL. Values.Encode emits keys in
// sorted order, so the URL is stable for tests as well as the browser. The
// direct OpenAI grant's nonce, device ID, and resource are absent by design.
func codexLoginAuthURL(challenge, state, redirectURI string) string {
	form := url.Values{
		"client_id":                  {codexClientID},
		"response_type":              {"code"},
		"redirect_uri":               {redirectURI},
		"scope":                      {codexLoginScope},
		"state":                      {state},
		"code_challenge":             {challenge},
		"code_challenge_method":      {"S256"},
		"id_token_add_organizations": {"true"},
		"codex_cli_simplified_flow":  {"true"},
		"originator":                 {codexLoginOriginator},
	}
	return codexLoginAuthorizeURL + "?" + form.Encode()
}

// Static browser pages. They carry no interpolated values, so a callback
// query can never reflect into the browser. The success page says only that
// authorization returned, not that the credential persisted.
const (
	codexLoginSuccessPage = `<!doctype html><html><head><meta charset="utf-8"><title>psi sign-in</title></head><body><p>OpenAI Codex sign-in returned to psi. You can close this window.</p></body></html>`
	codexLoginDeniedPage  = `<!doctype html><html><head><meta charset="utf-8"><title>psi sign-in</title></head><body><p>OpenAI Codex sign-in was not completed. You can close this window.</p></body></html>`
	codexLoginInvalidPage = `<!doctype html><html><head><meta charset="utf-8"><title>psi sign-in</title></head><body><p>Invalid sign-in request.</p></body></html>`
)

// codexLoginOutcome is one validated terminal callback: either a denial or
// an authorization code for this attempt.
type codexLoginOutcome struct {
	code   string
	denied bool
}

// parseCodexLoginCallback validates one callback request against the owned
// Host authority and expected state. Invalid requests leave the wait active;
// a wrong-state denial cannot abort the legitimate pending login.
func parseCodexLoginCallback(r *http.Request, host, expectedState string) (codexLoginOutcome, int, bool) {
	var outcome codexLoginOutcome
	if r.Method != http.MethodGet || r.Host != host || r.URL == nil ||
		r.URL.IsAbs() || r.URL.EscapedPath() != codexLoginCallbackPath {
		return outcome, http.StatusNotFound, false
	}
	// Bound the raw query before parsing so an oversized value never
	// reaches the query parser. Headers are bounded by MaxHeaderBytes.
	if len(r.URL.RawQuery) > codexLoginQueryLimit {
		return outcome, http.StatusBadRequest, false
	}
	values, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		return outcome, http.StatusBadRequest, false
	}
	for _, name := range []string{"state", "code", "error"} {
		if len(values[name]) > 1 {
			return outcome, http.StatusBadRequest, false
		}
	}
	if state := values.Get("state"); state == "" || state != expectedState {
		return outcome, http.StatusBadRequest, false
	}
	nonblank := func(value string) bool {
		return value != "" && value == strings.TrimSpace(value)
	}
	code, denial := values.Get("code"), values.Get("error")
	switch {
	case !values.Has("error") && values.Has("code") && nonblank(code):
		outcome.code = code
	case !values.Has("code") && values.Has("error") && nonblank(denial):
		outcome.denied = true
	default:
		return outcome, http.StatusBadRequest, false
	}
	return outcome, http.StatusOK, true
}

// parseCodexLoginPaste accepts the four Pi manual-input forms: a full
// redirect URL, code#state, a query string, or a bare code. It checks a
// supplied state without claiming the attempt on invalid input. The input is
// bounded to one line. The conversion to string is a required copy for
// net/url and strings; the caller keeps ownership of the byte slice and wipes
// it after this call. The returned code is later copied into the exchange
// form, which is another unavoidable string boundary.
func parseCodexLoginPaste(input []byte, expectedState string) (codexLoginOutcome, error) {
	if len(input) > codexLoginPasteLimit || bytes.ContainsAny(input, "\r\n") {
		return codexLoginOutcome{}, errCodexLoginPasteInvalid
	}
	trimmed := bytes.TrimSpace(input)
	if len(trimmed) == 0 {
		return codexLoginOutcome{}, errCodexLoginPasteEmpty
	}
	for _, b := range trimmed {
		if b < 0x20 || b == 0x7f {
			return codexLoginOutcome{}, errCodexLoginPasteInvalid
		}
	}
	text := string(trimmed)
	if strings.HasPrefix(text, "http://") || strings.HasPrefix(text, "https://") {
		parsed, err := url.Parse(text)
		if err != nil || parsed.Host == "" || parsed.RawQuery == "" {
			return codexLoginOutcome{}, errCodexLoginPasteInvalid
		}
		return parseCodexLoginQuery(parsed.RawQuery, expectedState)
	}
	if strings.HasPrefix(text, "?") {
		return parseCodexLoginQuery(text[1:], expectedState)
	}
	if before, query, found := strings.Cut(text, "?"); found && strings.Contains(before, "/auth/callback") {
		return parseCodexLoginQuery(query, expectedState)
	}
	if strings.Contains(text, "#") {
		code, state, _ := strings.Cut(text, "#")
		if !validCodexManualCode(code) || state == "" || strings.Contains(state, "#") {
			return codexLoginOutcome{}, errCodexLoginPasteInvalid
		}
		if state != expectedState {
			return codexLoginOutcome{}, errCodexLoginPasteState
		}
		return codexLoginOutcome{code: code}, nil
	}
	if strings.Contains(text, "=") {
		return parseCodexLoginQuery(text, expectedState)
	}
	if !validCodexManualCode(text) || strings.ContainsAny(text, "?#&") {
		return codexLoginOutcome{}, errCodexLoginPasteInvalid
	}
	return codexLoginOutcome{code: text}, nil
}

// Validate the decoded code as well as the input line: URL escapes can
// introduce spaces or control bytes after the initial line check.
func validCodexManualCode(code string) bool {
	for i := range len(code) {
		// Exclude ASCII space, control bytes, DEL, and non-ASCII bytes.
		if code[i] <= ' ' || code[i] >= 0x7f {
			return false
		}
	}
	return code != ""
}

// parseCodexLoginQuery validates one pasted query. A supplied state must
// be nonempty and match this attempt; an omitted state is allowed.
func parseCodexLoginQuery(raw, expectedState string) (codexLoginOutcome, error) {
	if raw == "" || len(raw) > codexLoginPasteLimit {
		return codexLoginOutcome{}, errCodexLoginPasteInvalid
	}
	values, err := url.ParseQuery(raw)
	if err != nil {
		return codexLoginOutcome{}, errCodexLoginPasteInvalid
	}
	for _, name := range []string{"code", "state", "error"} {
		if len(values[name]) > 1 {
			return codexLoginOutcome{}, errCodexLoginPasteInvalid
		}
	}
	if values.Has("error") {
		return codexLoginOutcome{}, errCodexLoginPasteInvalid
	}
	code := values.Get("code")
	if !validCodexManualCode(code) {
		return codexLoginOutcome{}, errCodexLoginPasteInvalid
	}
	if values.Has("state") {
		state := values.Get("state")
		if state == "" || state != strings.TrimSpace(state) {
			return codexLoginOutcome{}, errCodexLoginPasteInvalid
		}
		if state != expectedState {
			return codexLoginOutcome{}, errCodexLoginPasteState
		}
	}
	return codexLoginOutcome{code: code}, nil
}

// codexLoginCallbackGate owns one attempt's callback latch and terminal
// outcome. Exactly one valid outcome is claimed and published once to the
// buffered channel; invalid requests and late duplicates have no effect.
// A gate is always used by pointer: the latch must never be copied.
type codexLoginCallbackGate struct {
	host    string
	state   string
	result  chan codexLoginOutcome
	claimed atomic.Bool
}

func newCodexLoginCallbackGate(host, state string) *codexLoginCallbackGate {
	return &codexLoginCallbackGate{host: host, state: state, result: make(chan codexLoginOutcome, 1)}
}

func (g *codexLoginCallbackGate) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		outcome, status, ok := parseCodexLoginCallback(r, g.host, g.state)
		if !ok {
			writeOAuthCallbackPage(w, status, codexLoginInvalidPage)
			return
		}
		if !g.claimed.CompareAndSwap(false, true) {
			writeOAuthCallbackPage(w, http.StatusOK, codexLoginSuccessPage)
			return
		}
		if outcome.denied {
			writeOAuthCallbackPage(w, http.StatusBadRequest, codexLoginDeniedPage)
		} else {
			writeOAuthCallbackPage(w, http.StatusOK, codexLoginSuccessPage)
		}
		// Publish after the response is written so server shutdown cannot
		// truncate the browser page when the wait returns immediately.
		g.result <- outcome
	})
}

// claim publishes exactly one outcome. The buffered result send is
// nonblocking because only the first claimant can send.
func (g *codexLoginCallbackGate) claim(outcome codexLoginOutcome) bool {
	if !g.claimed.CompareAndSwap(false, true) {
		return false
	}
	g.result <- outcome
	return true
}

// submit validates one pasted redirect or code and offers it to the same
// one-shot gate as the browser callback. Invalid input does not claim the
// attempt, so a valid callback can still finish. The input is borrowed for
// this call only.
func (g *codexLoginCallbackGate) submit(input []byte) error {
	outcome, err := parseCodexLoginPaste(input, g.state)
	if err != nil {
		return err
	}
	if !g.claim(outcome) {
		return ErrCodexLoginClaimed
	}
	return nil
}

// waitCodexLoginCallback serves the bound listener until one valid callback,
// denial, or cancellation. The listener and all accepted connections close on
// every path, and the owned Serve goroutine is joined before return.
func waitCodexLoginCallback(ctx context.Context, listener net.Listener, gate *codexLoginCallbackGate, ready func()) (string, error) {
	server := &http.Server{
		Handler:           gate.handler(),
		ReadTimeout:       codexLoginServerTimeout,
		ReadHeaderTimeout: codexLoginServerTimeout,
		WriteTimeout:      codexLoginServerTimeout,
		IdleTimeout:       codexLoginServerTimeout,
		MaxHeaderBytes:    codexLoginHeaderLimit,
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
	ready()
	select {
	case outcome := <-gate.result:
		if outcome.denied {
			return "", fmt.Errorf("OpenAI Codex sign-in was not completed")
		}
		return outcome.code, nil
	case <-serveDone:
		return "", fmt.Errorf("OpenAI Codex sign-in callback failed")
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

// codexLoginToken is the accepted exchange result. Only the credential fields
// psi stores are retained; the ID token and scope response are ignored.
type codexLoginToken struct {
	access    string
	refresh   string
	expires   int64
	accountID string
}

// exchangeCode performs the single authorization-code exchange. It never
// retries: a code can be consumed even when its response is lost, so any
// failure starts over with a fresh login.
func (l *CodexLogin) exchangeCode(ctx context.Context, code, verifier, redirectURI string) (codexLoginToken, error) {
	form := url.Values{
		"grant_type":    {"authorization_code"},
		"client_id":     {codexClientID},
		"code":          {code},
		"code_verifier": {verifier},
		"redirect_uri":  {redirectURI},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, codexTokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return codexLoginToken{}, fmt.Errorf("invalid OpenAI Codex sign-in endpoint")
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	data, err := doOpenAITokenRequest(l.client(), req)
	if err != nil {
		switch {
		case errors.Is(err, errOpenAITokenTransport):
			return codexLoginToken{}, fmt.Errorf("OpenAI Codex sign-in request failed; start /login openai-codex again")
		case errors.Is(err, errOpenAITokenStatus):
			return codexLoginToken{}, fmt.Errorf("OpenAI Codex sign-in failed; start /login openai-codex again")
		default:
			return codexLoginToken{}, fmt.Errorf("invalid OpenAI Codex sign-in response; start /login openai-codex again")
		}
	}
	defer secretmem.Wipe(data[:cap(data)])
	return decodeCodexLoginToken(data, l.nowOrNow())
}

// decodeCodexLoginToken validates the Codex token contract: nonblank access
// and refresh tokens, a positive finite numeric lifetime, a future epoch
// millisecond expiry that fits in int64, and an account claim in the access
// token. The ID token and scope are deliberately not read.
func decodeCodexLoginToken(data []byte, now time.Time) (codexLoginToken, error) {
	var payload struct {
		Access    string `json:"access_token"`
		Refresh   string `json:"refresh_token"`
		ExpiresIn any    `json:"expires_in"`
	}
	if json.Unmarshal(data, &payload) != nil {
		return codexLoginToken{}, fmt.Errorf("invalid OpenAI Codex sign-in response; start /login openai-codex again")
	}
	if strings.TrimSpace(payload.Access) == "" || strings.TrimSpace(payload.Refresh) == "" {
		return codexLoginToken{}, fmt.Errorf("invalid OpenAI Codex sign-in response; start /login openai-codex again")
	}
	lifetime, ok := payload.ExpiresIn.(float64)
	if !ok || math.IsNaN(lifetime) || math.IsInf(lifetime, 0) || lifetime <= 0 {
		return codexLoginToken{}, fmt.Errorf("invalid OpenAI Codex sign-in response; start /login openai-codex again")
	}
	// 2^63 is the first float64 value above int64's range. Comparing before
	// conversion avoids an implementation-defined overflow result.
	const int64Limit = float64(1 << 63)
	expiry := float64(now.UnixMilli()) + lifetime*1000
	if math.IsNaN(expiry) || math.IsInf(expiry, 0) || expiry >= int64Limit ||
		int64(expiry) <= now.UnixMilli() {
		return codexLoginToken{}, fmt.Errorf("invalid OpenAI Codex sign-in response; start /login openai-codex again")
	}
	account, err := codexAccount(codexCredential{Access: payload.Access})
	if err != nil {
		return codexLoginToken{}, fmt.Errorf("OpenAI Codex sign-in did not include a ChatGPT account; start /login openai-codex again")
	}
	return codexLoginToken{
		access:    payload.Access,
		refresh:   payload.Refresh,
		expires:   int64(expiry),
		accountID: account,
	}, nil
}

// codexLoginBaseline pins the stored openai-codex entry and the resolved
// destination across the browser wait. raw is a compacted, owned copy so a
// reindented but otherwise identical entry is not a conflict.
type codexLoginBaseline struct {
	target     authTarget
	parentInfo os.FileInfo
	file       bool
	present    bool
	raw        json.RawMessage
}

// captureBaseline reads the stored document under a short shared lease and
// releases the lease before any browser interaction. A missing file is a
// valid first-login baseline; unsafe or nonregular targets fail closed.
func (l *CodexLogin) captureBaseline(ctx context.Context) (codexLoginBaseline, error) {
	var baseline codexLoginBaseline
	unlock, _, err := acquireAuthLock(ctx, l.Path+".lock", 0, 0, apiKeyAuthLockWait)
	if err != nil {
		if ctx.Err() != nil {
			return baseline, ctx.Err()
		}
		return baseline, fmt.Errorf("auth.json is busy; retry /login openai-codex")
	}
	defer unlock()
	target, err := resolveAuthTarget(l.Path)
	if err != nil {
		return baseline, err
	}
	parentInfo, err := statAuthIdentity(filepath.Dir(target.path))
	if err != nil {
		return baseline, fmt.Errorf("cannot resolve auth.json directory")
	}
	entries, _, exists, err := readAuthDocument(target)
	if err != nil {
		return baseline, err
	}
	defer releaseRawEntries(entries)
	baseline = codexLoginBaseline{target: target, parentInfo: parentInfo, file: exists}
	raw, present := entries["openai-codex"]
	baseline.present = present
	if present {
		compact, err := compactAuthEntry(raw)
		if err != nil {
			return baseline, fmt.Errorf("auth.json credential entry is malformed")
		}
		baseline.raw = compact
	}
	return baseline, nil
}

// compactAuthEntry returns an owned compact copy of one JSON value. Comparing
// compact forms ignores whitespace introduced by unrelated rewrites.
func compactAuthEntry(raw json.RawMessage) (json.RawMessage, error) {
	var buf bytes.Buffer
	if err := json.Compact(&buf, raw); err != nil {
		return nil, err
	}
	return json.RawMessage(buf.Bytes()), nil
}

// sameAuthEntryJSON reports whether two raw JSON values differ only in
// insignificant whitespace.
func sameAuthEntryJSON(a, b json.RawMessage) bool {
	compactA, errA := compactAuthEntry(a)
	if errA != nil {
		return false
	}
	defer secretmem.Wipe(compactA[:cap(compactA)])
	compactB, errB := compactAuthEntry(b)
	if errB != nil {
		return false
	}
	defer secretmem.Wipe(compactB[:cap(compactB)])
	return bytes.Equal(compactA, compactB)
}

// equalAuthDocuments compares every copied entry, including unrelated
// providers, using compacted JSON so reindentation is not a conflict.
func equalAuthDocuments(a, b map[string]json.RawMessage) bool {
	if len(a) != len(b) {
		return false
	}
	for key, value := range a {
		other, ok := b[key]
		if !ok || !sameAuthEntryJSON(value, other) {
			return false
		}
	}
	return true
}

// commit saves the exchanged credential under a fresh shared lease. The
// destination path, parent identity, and Codex entry must still match the
// baseline. An unrelated provider change, or a new or renamed file at the
// same destination with an unchanged Codex entry, is merged.
func (l *CodexLogin) commit(ctx context.Context, baseline codexLoginBaseline, token codexLoginToken) error {
	unlock, leaseValid, err := acquireAuthLock(ctx, l.Path+".lock", 0, 0, apiKeyAuthLockWait)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("auth.json is busy; retry /login openai-codex")
	}
	defer unlock()
	currentTarget, err := resolveAuthTarget(l.Path)
	if err != nil {
		return errCodexLoginConflict
	}
	currentParent, err := statAuthIdentity(filepath.Dir(currentTarget.path))
	if err != nil || baseline.parentInfo == nil || !os.SameFile(baseline.parentInfo, currentParent) {
		return errCodexLoginConflict
	}
	if currentTarget.path != baseline.target.path || (baseline.file && !currentTarget.exists) {
		return errCodexLoginConflict
	}
	entries, mode, exists, err := readAuthDocument(currentTarget)
	if err != nil {
		return errCodexLoginConflict
	}
	defer releaseRawEntries(entries)
	if exists != currentTarget.exists {
		return errCodexLoginConflict
	}
	raw, present := entries["openai-codex"]
	if present != baseline.present || (present && !sameAuthEntryJSON(raw, baseline.raw)) {
		return errCodexLoginConflict
	}
	// An explicit login replaces the identity with exactly the defined
	// OAuth fields. The code and verifier are never stored.
	newEntry, err := json.Marshal(map[string]any{
		"type":      "oauth",
		"access":    token.access,
		"refresh":   token.refresh,
		"expires":   token.expires,
		"accountId": token.accountID,
	})
	if err != nil {
		return fmt.Errorf("cannot save the OpenAI Codex login")
	}
	next := make(map[string]json.RawMessage, len(entries)+1)
	for key, value := range entries {
		next[key] = value
	}
	next["openai-codex"] = newEntry
	defer releaseRawEntries(next)
	validate := func() error {
		if !leaseValid() {
			return errCodexLoginLockLost
		}
		latest, _, latestExists, err := readAuthDocument(currentTarget)
		if err != nil {
			return errCodexLoginConflict
		}
		defer releaseRawEntries(latest)
		if latestExists != currentTarget.exists || !equalAuthDocuments(entries, latest) {
			return errCodexLoginConflict
		}
		return nil
	}
	// Cancellation before admission writes nothing. Once admitted, the
	// save finishes under the lease and reports its actual outcome.
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := writeAuthDocumentWithHooks(currentTarget, next, mode, validate, authFileHooks{Chmod: l.Chmod, Rename: l.Rename}); err != nil {
		switch {
		case errors.Is(err, errCodexLoginConflict), errors.Is(err, errCodexLoginLockLost):
			return err
		case errors.Is(err, errAuthDocumentTooLarge):
			return fmt.Errorf("cannot save the OpenAI Codex login: auth.json would exceed its size limit")
		default:
			return fmt.Errorf("cannot save the OpenAI Codex login")
		}
	}
	return nil
}
