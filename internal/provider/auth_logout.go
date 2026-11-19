package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"psi/internal/secretmem"
)

// oauthRevokeURL is the fixed OpenAI token-revocation endpoint used by Codex
// CLI. Logout uses only the response status.
const oauthRevokeURL = "https://auth.openai.com/oauth/revoke"

// oauthRevokeTimeout bounds one best-effort request. Local deletion does not
// depend on this request completing.
const oauthRevokeTimeout = 10 * time.Second

// RevocationOutcome reports what happened away from the local auth file.
type RevocationOutcome uint8

const (
	// RevocationNotAttempted covers API keys and preflight refusals.
	RevocationNotAttempted RevocationOutcome = iota
	// RevocationNoUsableToken means the OAuth entry had no token to send.
	RevocationNoUsableToken
	// RevocationAcceptedRefresh means a 2xx answer to a refresh hint.
	RevocationAcceptedRefresh
	// RevocationAcceptedAccess means a 2xx answer to an access hint.
	RevocationAcceptedAccess
	// RevocationUnconfirmed covers a rejected, redirected, timed-out, or
	// failed request. It does not imply that the token is still valid.
	RevocationUnconfirmed
)

// LogoutResult separates local removal from best-effort remote revocation.
type LogoutResult struct {
	Removed    bool
	Revocation RevocationOutcome
}

// revokeRequest is one selected token and its JSON request fields.
type revokeRequest struct {
	token    string
	hint     string
	clientID string
}

// selectRevokeRequest prefers a nonblank refresh token. Direct OpenAI needs
// the stored client ID for a refresh request; Codex uses its built-in client
// ID. If that pair is not usable, use a nonblank access token without a client
// ID. No request follows a refresh attempt.
func selectRevokeRequest(providerID string, entry map[string]json.RawMessage) (revokeRequest, bool) {
	var clientID string
	switch providerID {
	case "openai-codex":
		clientID = codexClientID
	case "openai":
		clientID = usableOAuthString(entry, "clientId")
	default:
		return revokeRequest{}, false
	}
	if clientID != "" {
		if refresh := usableOAuthString(entry, "refresh"); refresh != "" {
			return revokeRequest{token: refresh, hint: "refresh_token", clientID: clientID}, true
		}
	}
	if access := usableOAuthString(entry, "access"); access != "" {
		return revokeRequest{token: access, hint: "access_token"}, true
	}
	return revokeRequest{}, false
}

// usableOAuthString treats a missing, null, blank, or wrong-typed field as
// unusable. It returns the stored value unchanged so the request sends exactly
// what the file contains.
func usableOAuthString(entry map[string]json.RawMessage, name string) string {
	raw, ok := entry[name]
	if !ok {
		return ""
	}
	value := bytes.TrimSpace(raw)
	if len(value) == 0 || bytes.Equal(value, []byte("null")) {
		return ""
	}
	var text string
	if json.Unmarshal(value, &text) != nil {
		return ""
	}
	if strings.TrimSpace(text) == "" {
		return ""
	}
	return text
}

// revokeOAuthToken sends one bounded request. Redirects are not followed and
// the response body is never read or returned. JSON decoding creates a string
// to interpret escapes and test for blanks. The request body wipes its JSON
// bytes on Close; encoding/json and net/http own their internal copies.
func revokeOAuthToken(ctx context.Context, request revokeRequest, client *http.Client) RevocationOutcome {
	payload := struct {
		Token         string `json:"token"`
		TokenTypeHint string `json:"token_type_hint"`
		ClientID      string `json:"client_id,omitempty"`
	}{
		Token:         request.token,
		TokenTypeHint: request.hint,
		ClientID:      request.clientID,
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return RevocationUnconfirmed
	}
	body := &revokeRequestBody{data: encoded}
	reqCtx, cancel := context.WithTimeout(ctx, oauthRevokeTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, oauthRevokeURL, body)
	if err != nil {
		_ = body.Close()
		return RevocationUnconfirmed
	}
	req.Header.Set("Content-Type", "application/json")
	// The body is not replayable: GetBody stays nil so a redirect cannot
	// resend the token.
	req.ContentLength = int64(len(encoded))
	if client == nil {
		client = http.DefaultClient
	}
	unredirected := *client
	unredirected.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}
	resp, err := unredirected.Do(req)
	if err != nil {
		if resp != nil && resp.Body != nil {
			_ = resp.Body.Close()
		}
		return RevocationUnconfirmed
	}
	if resp.Body != nil {
		_ = resp.Body.Close()
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return RevocationUnconfirmed
	}
	if request.hint == "refresh_token" {
		return RevocationAcceptedRefresh
	}
	return RevocationAcceptedAccess
}

// revokeRequestBody owns the application buffer. The transport must close it,
// possibly after Do returns, and Close may run concurrently with Read.
type revokeRequestBody struct {
	mu     sync.Mutex
	data   []byte
	offset int
	closed bool
}

func (b *revokeRequestBody) Read(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed || b.offset >= len(b.data) {
		return 0, io.EOF
	}
	n := copy(p, b.data[b.offset:])
	b.offset += n
	return n, nil
}

func (b *revokeRequestBody) Close() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.closed {
		secretmem.Wipe(b.data[:cap(b.data)])
		b.data = nil
		b.closed = true
	}
	return nil
}

// authSnapshotMatches compares the raw entry bytes and permission bits that
// logout read before any request with a fresh read. Formatting differences in
// unrelated top-level whitespace do not change an entry's raw value.
func authSnapshotMatches(baseline map[string]json.RawMessage, mode os.FileMode, current map[string]json.RawMessage, currentMode os.FileMode) bool {
	if mode != currentMode || len(baseline) != len(current) {
		return false
	}
	for providerID, want := range baseline {
		got, ok := current[providerID]
		if !ok || !bytes.Equal(want, got) {
			return false
		}
	}
	return true
}
