package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"math"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/hypernewbie/psi/internal/secretmem"
)

// OpenAI ChatGPT subscription constants, matching Pi 1.5.0's openai provider.
// The token endpoint and resource are fixed; model configuration never
// overrides them.
const (
	openAITokenURL        = "https://auth.openai.com/api/accounts/oauth/token"
	openAIResource        = "https://api.openai.com/v1"
	openAIDirectScope     = "chatgpt.tokens.use.direct"
	openAIRefreshWindowMs = 5 * 60 * 1000
	openAIExpiryMarginMs  = 3 * 60 * 1000
	openAIAuthDocLimit    = maxAuthJSONSize
	openAITokenBodyLimit  = 64 * 1024
	openAIRefreshTimeout  = 15 * time.Second
	openAIAuthLockWait    = 5 * time.Second
)

// OpenAIAuth consumes the Pi-registered openai credential. A stored entry owns
// the provider: OAuth failures and invalid entries never fall back to an API
// key. Only an absent entry permits models.json, then OPENAI_API_KEY.
//
// Interactive sign-in lives in openai_login.go. There is deliberately no
// per-instance mutex here: concurrent callers coordinate through the shared
// auth.json lease; a caller that waits rereads the stored result and reuses
// a credential another caller refreshed.
type OpenAIAuth struct {
	Path          string
	HTTP          *http.Client
	Env           map[string]string
	ConfiguredKey string
	Chmod         func(string, os.FileMode) error
	Rename        func(string, string) error
	// now selects the clock for expiry boundaries. Nil means time.Now.
	now func() time.Time
	// refreshTimeout overrides the fixed 15-second refresh deadline in tests.
	// Zero means the production deadline. Never user configuration.
	refreshTimeout time.Duration
}

func (a *OpenAIAuth) nowOrNow() time.Time {
	if a.now != nil {
		return a.now()
	}
	return time.Now()
}

func (a *OpenAIAuth) refreshTimeoutOrDefault() time.Duration {
	if a.refreshTimeout > 0 {
		return a.refreshTimeout
	}
	return openAIRefreshTimeout
}

// Check is local-only. OAuth entries that are fresh or able to refresh are
// usable; the refresh happens on submission so discovery makes no requests.
func (a *OpenAIAuth) Check() error {
	return a.CheckForEndpoint(openAIResource)
}

// openAIOfficialBase reports whether base is the exact official OpenAI
// endpoint, allowing only the existing harmless trailing-slash
// normalization. It mirrors the subscription request profile.
func openAIOfficialBase(base string) bool {
	return strings.TrimRight(strings.TrimSpace(base), "/") == openAIResource
}

// errOpenAIOAuthEndpoint refuses a subscription login on a custom endpoint.
// The message stays static: the configured URL is never echoed.
var errOpenAIOAuthEndpoint = errors.New("OpenAI OAuth login requires the official OpenAI endpoint; this model overrides it")

// CheckForEndpoint is the local-only credential check bound to one effective
// endpoint. A stored OAuth login requires the official base; API-key
// entries keep their existing override behavior.
func (a *OpenAIAuth) CheckForEndpoint(base string) error {
	entries, err := AuthEntries(a.Path)
	if err != nil {
		return err
	}
	defer ReleaseAuthEntries(entries)
	state := openAICredentialStateAt(a.Env, entries, a.Path, a.ConfiguredKey, a.nowOrNow())
	if !state.Usable {
		return state.Err
	}
	if state.OAuth && !openAIOfficialBase(base) {
		return errOpenAIOAuthEndpoint
	}
	return nil
}

// openAICredentialState resolves the openai credential from one shared auth
// snapshot. It never makes network requests.
func openAICredentialState(env map[string]string, entries map[string]json.RawMessage, authPath, configuredKey string) CredentialState {
	return openAICredentialStateAt(env, entries, authPath, configuredKey, time.Now())
}

func openAICredentialStateAt(env map[string]string, entries map[string]json.RawMessage, authPath, configuredKey string, now time.Time) CredentialState {
	raw, present := entries["openai"]
	if !present {
		_, source, err := apiKeyFromEntries(env, entries, authPath, "openai", configuredKey)
		if err != nil {
			return CredentialState{Err: err}
		}
		return CredentialState{Usable: true, Source: source}
	}
	var probe struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil {
		return CredentialState{Err: fmt.Errorf("auth.json openai credential must be an API-key or OAuth object")}
	}
	switch probe.Type {
	case "", "api_key":
		_, source, err := apiKeyFromEntries(env, entries, authPath, "openai", configuredKey)
		if err != nil {
			return CredentialState{Err: err}
		}
		return CredentialState{Usable: true, Source: source}
	case "oauth":
		access, refresh, expires, clientID, err := parseOpenAIOAuthFields(raw)
		if err != nil {
			return CredentialState{Err: err}
		}
		if openAIAccessFresh(access, expires, now) {
			return CredentialState{Usable: true, Source: "auth.json:openai", OAuth: true}
		}
		if strings.TrimSpace(refresh) == "" || strings.TrimSpace(clientID) == "" {
			return CredentialState{Err: fmt.Errorf("OpenAI OAuth login cannot refresh; run /login openai in psi")}
		}
		return CredentialState{Usable: true, Source: "auth.json:openai", OAuth: true}
	default:
		return CredentialState{Err: fmt.Errorf("unsupported openai credential type")}
	}
}

// parseOpenAIOAuthFields validates the stored OAuth entry. A missing access
// token is not an error here; the caller refreshes when the entry can.
// A missing or non-numeric expiry is always invalid.
func parseOpenAIOAuthFields(raw json.RawMessage) (access, refresh string, expires float64, clientID string, err error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil || fields == nil {
		return "", "", 0, "", fmt.Errorf("auth.json openai credential must be an API-key or OAuth object")
	}
	stringField := func(name string) (string, error) {
		data, ok := fields[name]
		if !ok || len(bytes.TrimSpace(data)) == 0 || string(bytes.TrimSpace(data)) == "null" {
			return "", nil
		}
		var value string
		if err := json.Unmarshal(data, &value); err != nil {
			return "", fmt.Errorf("auth.json openai OAuth credential is invalid")
		}
		return value, nil
	}
	if access, err = stringField("access"); err != nil {
		return "", "", 0, "", err
	}
	if refresh, err = stringField("refresh"); err != nil {
		return "", "", 0, "", err
	}
	if clientID, err = stringField("clientId"); err != nil {
		return "", "", 0, "", err
	}
	data, ok := fields["expires"]
	if !ok {
		return "", "", 0, "", fmt.Errorf("auth.json openai OAuth credential has an invalid expiry")
	}
	var numeric any
	if err := json.Unmarshal(data, &numeric); err != nil {
		return "", "", 0, "", fmt.Errorf("auth.json openai OAuth credential has an invalid expiry")
	}
	value, ok := numeric.(float64)
	if !ok || math.IsNaN(value) || math.IsInf(value, 0) {
		return "", "", 0, "", fmt.Errorf("auth.json openai OAuth credential has an invalid expiry")
	}
	return access, refresh, value, clientID, nil
}

func openAIAccessFresh(access string, expires float64, now time.Time) bool {
	return strings.TrimSpace(access) != "" && expires > float64(now.UnixMilli())+openAIRefreshWindowMs
}

// openAIBaseline pins the six compared OpenAI fields for compare-before-write.
type openAIBaseline struct {
	fields map[string]json.RawMessage
}

func openAIBaselineFrom(raw json.RawMessage) (openAIBaseline, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil || fields == nil {
		return openAIBaseline{}, fmt.Errorf("auth.json openai credential must be an API-key or OAuth object")
	}
	out := make(map[string]json.RawMessage, 6)
	for _, name := range []string{"type", "access", "refresh", "expires", "clientId", "scopes"} {
		out[name] = fields[name]
	}
	return openAIBaseline{fields: out}, nil
}

func (b openAIBaseline) matches(raw json.RawMessage) bool {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil || fields == nil {
		return false
	}
	for name, want := range b.fields {
		if !bytes.Equal(want, fields[name]) {
			return false
		}
	}
	return true
}

// Get resolves the bearer token for one Responses request. A fresh OAuth
// token and all API-key paths are local. A refresh holds the shared lease
// through exchange and persistence, even if the caller cancels; after a
// successful commit a cancelled caller receives its context error and submits
// no request.
func (a *OpenAIAuth) Get(ctx context.Context) (string, error) {
	return a.GetForEndpoint(ctx, openAIResource)
}

// GetForEndpoint resolves the bearer token for one Responses request bound
// to one effective endpoint. A stored OAuth login requires the official
// base and fails before any refresh or POST; API-key entries keep their
// existing override behavior. The endpoint is rechecked under this call's
// own lease, so an earlier Check is never authority to send.
func (a *OpenAIAuth) GetForEndpoint(ctx context.Context, base string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	unlock, valid, err := acquireAuthLock(ctx, a.Path+".lock", 0, 0, openAIAuthLockWait)
	if err != nil {
		if authPathAbsent(a.Path, err) {
			key, _, resolveErr := apiKeyFromEntries(a.Env, nil, a.Path, "openai", a.ConfiguredKey)
			return key, resolveErr
		}
		return "", err
	}
	defer unlock()

	all, err := readOpenAIAuthDoc(a.Path)
	if err != nil {
		if authPathAbsent(a.Path, err) {
			key, _, resolveErr := apiKeyFromEntries(a.Env, nil, a.Path, "openai", a.ConfiguredKey)
			return key, resolveErr
		}
		return "", err
	}
	defer ReleaseAuthEntries(all)
	raw, present := all["openai"]
	if !present {
		key, _, resolveErr := apiKeyFromEntries(a.Env, all, a.Path, "openai", a.ConfiguredKey)
		return key, resolveErr
	}
	var probe struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil {
		return "", fmt.Errorf("auth.json openai credential must be an API-key or OAuth object")
	}
	if probe.Type == "" || probe.Type == "api_key" {
		key, _, resolveErr := apiKeyFromEntries(a.Env, all, a.Path, "openai", a.ConfiguredKey)
		return key, resolveErr
	}
	if probe.Type != "oauth" {
		return "", fmt.Errorf("unsupported openai credential type")
	}
	if !openAIOfficialBase(base) {
		return "", errOpenAIOAuthEndpoint
	}
	access, refresh, expires, clientID, err := parseOpenAIOAuthFields(raw)
	if err != nil {
		return "", err
	}
	if openAIAccessFresh(access, expires, a.nowOrNow()) {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		return access, nil
	}
	if strings.TrimSpace(refresh) == "" || strings.TrimSpace(clientID) == "" {
		return "", fmt.Errorf("OpenAI OAuth login cannot refresh; run /login openai in psi")
	}
	baseline, err := openAIBaselineFrom(raw)
	if err != nil {
		return "", err
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if st, err := os.Lstat(a.Path); err != nil || st.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("cannot safely refresh a symlinked auth.json; replace it with a regular file, then run /login openai in psi")
	}
	refreshCtx, cancel := context.WithTimeout(context.Background(), a.refreshTimeoutOrDefault())
	defer cancel()
	token, err := a.exchange(refreshCtx, refresh, clientID)
	if err != nil {
		return "", err
	}
	// Reread under the retained lease and merge, preserving concurrent
	// changes to other providers and unknown OpenAI fields.
	current, info, err := readRegularOpenAIAuthDoc(a.Path)
	if err != nil {
		return "", fmt.Errorf("OpenAI credentials changed during refresh; refusing overwrite")
	}
	defer ReleaseAuthEntries(current)
	currentRaw, ok := current["openai"]
	if !ok || !baseline.matches(currentRaw) {
		return "", fmt.Errorf("OpenAI credentials changed during refresh; refusing overwrite")
	}
	if !valid() {
		return "", fmt.Errorf("auth.json lock changed during OAuth refresh; refusing overwrite")
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(currentRaw, &fields); err != nil || fields == nil {
		return "", fmt.Errorf("invalid OpenAI credential object")
	}
	for key, value := range map[string]any{
		"type":     "oauth",
		"access":   token.access,
		"refresh":  token.refresh,
		"expires":  token.expires,
		"clientId": clientID,
		"scopes":   token.scopes,
	} {
		fields[key], _ = json.Marshal(value)
	}
	next := maps.Clone(current)
	next["openai"], _ = json.Marshal(fields)
	encoded, err := encodeOpenAIAuthDoc(next)
	if err != nil {
		return "", err
	}
	validate := func() error {
		final, finalInfo, err := readRegularOpenAIAuthDoc(a.Path)
		defer ReleaseAuthEntries(final)
		if err != nil || !sameOpenAIAuthFile(info, finalInfo) || !maps.EqualFunc(current, final, equalAuthEntry) {
			return fmt.Errorf("OpenAI credentials changed during refresh; refusing overwrite")
		}
		if !valid() {
			return fmt.Errorf("auth.json lock changed during OAuth refresh; refusing overwrite")
		}
		return nil
	}
	if err := persistAuthDoc(encoded, a.Path, validate, authFileHooks{Chmod: a.Chmod, Rename: a.Rename}); err != nil {
		return "", err
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	return token.access, nil
}

// Token-endpoint call sentinels. Refresh and login share the transport
// code while keeping their own static errors.
var (
	errOpenAITokenTransport = errors.New("openai token transport")
	errOpenAITokenStatus    = errors.New("openai token status")
	errOpenAITokenBody      = errors.New("openai token body")
)

// doOpenAITokenRequest sends one token-endpoint request on a shallow client
// copy that refuses redirects, so the form is never forwarded elsewhere.
// It never retries and never surfaces the response body.
func doOpenAITokenRequest(client *http.Client, req *http.Request) ([]byte, error) {
	if client == nil {
		client = http.DefaultClient
	}
	single := *client
	single.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := single.Do(req)
	if err != nil {
		return nil, errOpenAITokenTransport
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, errOpenAITokenStatus
	}
	data, readErr := io.ReadAll(io.LimitReader(resp.Body, openAITokenBodyLimit+1))
	if readErr != nil || len(data) > openAITokenBodyLimit {
		// A partial or oversized body can already contain token material.
		// Wipe it before discarding the only reference.
		secretmem.Wipe(data[:cap(data)])
		return nil, errOpenAITokenBody
	}
	return data, nil
}

// openAITokenPayload is one decoded token-endpoint body. The ID token
// decodes only for login's presence check; refresh ignores it.
type openAITokenPayload struct {
	Access    string `json:"access_token"`
	Refresh   string `json:"refresh_token"`
	Scope     string `json:"scope"`
	ExpiresIn any    `json:"expires_in"`
	IDToken   any    `json:"id_token"`
}

// decodeOpenAITokenResponse unmarshals one token-endpoint body for refresh
// and login. Unknown fields are ignored; both callers validate further.
func decodeOpenAITokenResponse(data []byte) (openAITokenPayload, bool) {
	var resp openAITokenPayload
	if json.Unmarshal(data, &resp) != nil {
		return openAITokenPayload{}, false
	}
	return resp, true
}

// credential validates the shared token-response contract: nonblank access,
// refresh, and scope, a positive finite lifetime, the direct-token scope,
// and a finite computed expiry. Login additionally requires hasIDToken.
func (r openAITokenPayload) credential(now time.Time) (openAIToken, bool) {
	if strings.TrimSpace(r.Access) == "" || strings.TrimSpace(r.Refresh) == "" || strings.TrimSpace(r.Scope) == "" {
		return openAIToken{}, false
	}
	lifetime, ok := r.ExpiresIn.(float64)
	if !ok || math.IsNaN(lifetime) || math.IsInf(lifetime, 0) || lifetime <= 0 {
		return openAIToken{}, false
	}
	scopes := strings.Fields(r.Scope)
	found := false
	for _, scope := range scopes {
		if scope == openAIDirectScope {
			found = true
			break
		}
	}
	if !found {
		return openAIToken{}, false
	}
	expiry := float64(now.UnixMilli()) + lifetime*1000 - openAIExpiryMarginMs
	if math.IsNaN(expiry) || math.IsInf(expiry, 0) {
		return openAIToken{}, false
	}
	return openAIToken{access: r.Access, refresh: r.Refresh, expires: expiry, scopes: scopes}, true
}

// hasIDToken reports the login-only ID-token presence check. Pi requires
// the token but never reads or stores profile contents; neither does psi.
func (r openAITokenPayload) hasIDToken() bool {
	value, ok := r.IDToken.(string)
	return ok && strings.TrimSpace(value) != ""
}

type openAIToken struct {
	access  string
	refresh string
	expires float64
	scopes  []string
}

// exchange performs one refresh attempt against the fixed token endpoint.
// It never retries and never surfaces the response body.
func (a *OpenAIAuth) exchange(ctx context.Context, refresh, clientID string) (openAIToken, error) {
	form := url.Values{
		"grant_type":    {"refresh_token"},
		"client_id":     {clientID},
		"refresh_token": {refresh},
		"resource":      {openAIResource},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, openAITokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return openAIToken{}, fmt.Errorf("invalid OpenAI token endpoint")
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	data, err := doOpenAITokenRequest(a.HTTP, req)
	if err != nil {
		switch {
		case errors.Is(err, errOpenAITokenTransport):
			return openAIToken{}, fmt.Errorf("OpenAI OAuth refresh request failed")
		case errors.Is(err, errOpenAITokenStatus):
			return openAIToken{}, fmt.Errorf("OpenAI OAuth refresh failed; run /login openai in psi")
		default:
			return openAIToken{}, fmt.Errorf("invalid OpenAI OAuth refresh response")
		}
	}
	resp, ok := decodeOpenAITokenResponse(data)
	if !ok {
		return openAIToken{}, fmt.Errorf("invalid OpenAI OAuth refresh response")
	}
	token, ok := resp.credential(a.nowOrNow())
	if !ok {
		return openAIToken{}, fmt.Errorf("invalid OpenAI OAuth refresh response")
	}
	return token, nil
}

var errOpenAIAuthUnsafe = errors.New("auth.json is unsafe; refusing overwrite")

// readRegularOpenAIAuthDoc snapshots a write target. Unlike a read-only
// credential check, a write never follows a symlink. A missing target has
// no file identity and can seed a new login document.
func readRegularOpenAIAuthDoc(path string) (map[string]json.RawMessage, os.FileInfo, error) {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return map[string]json.RawMessage{}, nil, nil
	}
	if err != nil || !info.Mode().IsRegular() {
		return nil, nil, errOpenAIAuthUnsafe
	}
	doc, err := readOpenAIAuthDoc(path)
	if err != nil {
		return nil, nil, err
	}
	after, err := os.Lstat(path)
	if err != nil || !sameOpenAIAuthFile(info, after) {
		ReleaseAuthEntries(doc)
		return nil, nil, errOpenAIAuthUnsafe
	}
	return doc, info, nil
}

func sameOpenAIAuthFile(before, after os.FileInfo) bool {
	if before == nil || after == nil {
		return before == nil && after == nil
	}
	return after.Mode() == before.Mode() && os.SameFile(before, after)
}

func equalAuthEntry(a, b json.RawMessage) bool { return bytes.Equal(a, b) }

func encodeOpenAIAuthDoc(doc map[string]json.RawMessage) ([]byte, error) {
	encoded, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("cannot encode auth.json")
	}
	// Include the trailing newline appended by persistAuthDoc.
	if len(encoded)+1 > openAIAuthDocLimit {
		return nil, fmt.Errorf("cannot save credentials: auth.json would exceed its size limit")
	}
	return encoded, nil
}

// readOpenAIAuthDoc reads one bounded auth document. A missing file returns
// its os error so the caller can treat it as an absent entry; malformed,
// oversized, and unreadable documents are safe errors.
func readOpenAIAuthDoc(path string) (map[string]json.RawMessage, error) {
	data, err := readAuthFile(path)
	if err != nil {
		return nil, err
	}
	defer secretmem.Wipe(data[:cap(data)])
	var all map[string]json.RawMessage
	if !utf8.Valid(data) || json.Unmarshal(stripAuthBOM(data), &all) != nil || all == nil {
		ReleaseAuthEntries(all)
		return nil, fmt.Errorf("auth.json is malformed")
	}
	return all, nil
}
