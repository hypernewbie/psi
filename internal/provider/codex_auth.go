package provider

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"psi/internal/lockfile"
)

const codexTokenURL = "https://auth.openai.com/oauth/token"
const codexClientID = "app_EMoamEEZ73f0CkXaXp7hrann"

// CodexAuth reuses Pi's existing subscription login. Refresh updates only that
// provider and uses the shared lease in internal/lockfile. There is deliberately
// no API-key fallback and no interactive login flow.
type CodexAuth struct {
	Path     string
	HTTP     *http.Client
	TokenURL string // tests can use a local server; never read from model config
	mu       sync.Mutex
	// fs seams for deterministic fault schedules (nil = os defaults).
	Chmod  func(string, os.FileMode) error
	Rename func(string, string) error
	// StaleAfter bounds lease waits; non-positive means the Pi async policy (30s).
	StaleAfter time.Duration
	// WaitAfter bounds the wait for the shared lease; non-positive means the
	// Pi lock deadline (30s).
	WaitAfter time.Duration
	// HeartbeatInterval paces lease mtime updates; non-positive means 2s.
	HeartbeatInterval time.Duration
}

type codexCredential struct {
	Type      string `json:"type"`
	Access    string `json:"access"`
	Refresh   string `json:"refresh"`
	Expires   int64  `json:"expires"`
	AccountID string `json:"accountId"`
}

// loadLocked reads and parses auth.json. The caller must own the shared lease;
// callers outside a refresh transaction must use loadShared.
func (a *CodexAuth) loadLocked() (map[string]json.RawMessage, codexCredential, error) {
	f, err := os.Open(a.Path)
	if err != nil {
		return nil, codexCredential{}, fmt.Errorf("cannot read Codex login; run /login openai-codex in Pi")
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, 4*1024*1024+1))
	if err != nil || len(data) > 4*1024*1024 {
		return nil, codexCredential{}, fmt.Errorf("cannot read bounded auth.json")
	}
	data = stripAuthBOM(data)
	var all map[string]json.RawMessage
	if json.Unmarshal(data, &all) != nil || all == nil {
		return nil, codexCredential{}, fmt.Errorf("auth.json is malformed")
	}
	var entry codexCredential
	if json.Unmarshal(all["openai-codex"], &entry) != nil || entry.Type != "oauth" || (entry.Access == "" && entry.Refresh == "") {
		return nil, entry, fmt.Errorf("no Codex OAuth login; run /login openai-codex in Pi (no API-key fallback)")
	}
	return all, entry, nil
}

func codexAccount(entry codexCredential) (string, error) {
	if entry.AccountID != "" {
		return entry.AccountID, nil
	}
	parts := strings.Split(entry.Access, ".")
	if len(parts) == 3 {
		data, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(parts[1], "="))
		var claims struct {
			Auth struct {
				AccountID string `json:"chatgpt_account_id"`
			} `json:"https://api.openai.com/auth"`
		}
		if err == nil && json.Unmarshal(data, &claims) == nil && claims.Auth.AccountID != "" {
			return claims.Auth.AccountID, nil
		}
	}
	return "", fmt.Errorf("Codex login is missing its ChatGPT account ID; log in again through Pi")
}

// loadShared reads auth.json through the shared lease so a reader never inspects
// a partial writer document. Startup stays local-only with a bounded wait.
// A missing file still falls back to environment-based credentials upstream.
func (a *CodexAuth) loadShared(ctx context.Context) (map[string]json.RawMessage, codexCredential, error) {
	unlock, _, err := acquireAuthLock(ctx, a.Path+".lock", a.StaleAfter, a.HeartbeatInterval, a.WaitAfter)
	if err != nil {
		return nil, codexCredential{}, err
	}
	defer unlock()
	return a.loadLocked()
}

// Check is local-only. Expired credentials with a refresh token are usable;
// the refresh happens on submission so startup does not make network requests.
func (a *CodexAuth) Check() error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	all, _, err := a.loadShared(ctx)
	if err != nil {
		return err
	}
	return codexCredentialCheck(all)
}

// codexCredentialCheck validates the Codex OAuth entry in one auth.json
// snapshot.
func codexCredentialCheck(entries map[string]json.RawMessage) error {
	var entry codexCredential
	if json.Unmarshal(entries["openai-codex"], &entry) != nil || entry.Type != "oauth" || (entry.Access == "" && entry.Refresh == "") {
		return fmt.Errorf("no Codex OAuth login; run /login openai-codex in Pi (no API-key fallback)")
	}
	if entry.Expires <= time.Now().UnixMilli() {
		if entry.Refresh == "" {
			return fmt.Errorf("Codex login expired; log in again through Pi")
		}
		return nil
	}
	_, err := codexAccount(entry)
	return err
}

func (a *CodexAuth) Get(ctx context.Context) (string, string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return "", "", err
	}
	_, entry, err := a.loadShared(ctx)
	if err != nil {
		return "", "", err
	}
	if entry.Access != "" && (entry.Expires > time.Now().Add(30*time.Second).UnixMilli() || entry.Refresh == "" && entry.Expires > time.Now().UnixMilli()) {
		account, err := codexAccount(entry)
		return entry.Access, account, err
	}
	if entry.Refresh == "" {
		return "", "", fmt.Errorf("Codex login expired; log in again through Pi")
	}
	if st, err := os.Lstat(a.Path); err != nil || st.Mode()&os.ModeSymlink != 0 {
		return "", "", fmt.Errorf("cannot safely refresh a symlinked auth.json; refresh the login in Pi")
	}
	unlock, valid, err := acquireAuthLock(ctx, a.Path+".lock", a.StaleAfter, a.HeartbeatInterval, a.WaitAfter)
	if err != nil {
		return "", "", err
	}
	defer unlock()
	_, entry, err = a.loadLocked()
	if err != nil {
		return "", "", err
	}
	// Another Pi/psi may have refreshed while this process waited for the lock.
	if entry.Access != "" && entry.Expires > time.Now().Add(30*time.Second).UnixMilli() {
		account, err := codexAccount(entry)
		return entry.Access, account, err
	}
	if entry.Refresh == "" {
		return "", "", fmt.Errorf("Codex refresh token is missing")
	}
	endpoint := a.TokenURL
	if endpoint == "" {
		endpoint = codexTokenURL
	}
	refreshCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	form := url.Values{"grant_type": {"refresh_token"}, "refresh_token": {entry.Refresh}, "client_id": {codexClientID}}
	req, err := http.NewRequestWithContext(refreshCtx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return "", "", fmt.Errorf("invalid Codex token endpoint")
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	client := a.HTTP
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return "", "", ctx.Err()
		}
		return "", "", fmt.Errorf("Codex OAuth refresh request failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", "", fmt.Errorf("Codex OAuth refresh failed (HTTP %d); log in again through Pi", resp.StatusCode)
	}
	// If a complete valid token response arrived before cancellation, retain
	// the rotated refresh token even if the final body read was interrupted.
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024+1))
	var token struct {
		Access    string `json:"access_token"`
		Refresh   string `json:"refresh_token"`
		ExpiresIn int64  `json:"expires_in"`
	}
	if len(data) > 64*1024 || json.Unmarshal(data, &token) != nil || token.Access == "" || token.ExpiresIn <= 0 || token.ExpiresIn > 365*86400 {
		return "", "", fmt.Errorf("invalid Codex OAuth refresh response")
	}
	updated := entry
	updated.Access, updated.Expires = token.Access, time.Now().Add(time.Duration(token.ExpiresIn)*time.Second).UnixMilli()
	if token.Refresh != "" {
		updated.Refresh = token.Refresh
	}
	// A refreshed JWT is authoritative for its account; opaque test/proxy tokens
	// retain the account already stored by Pi.
	if account, err := codexAccount(codexCredential{Access: token.Access}); err == nil {
		updated.AccountID = account
	}
	account, err := codexAccount(updated)
	if err != nil {
		return "", "", err
	}
	// Once rotation succeeds, complete its local commit even if the user has
	// cancelled. Dropping the new refresh token could invalidate Pi's login.
	if !valid() {
		return "", "", fmt.Errorf("auth.json lock changed during OAuth refresh; refusing overwrite")
	}
	all, current, err := a.loadLocked()
	if err != nil {
		return "", "", err
	}
	if current.Access != entry.Access || current.Refresh != entry.Refresh || current.Expires != entry.Expires {
		return "", "", fmt.Errorf("Codex credentials changed during refresh; refusing overwrite")
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(all["openai-codex"], &fields) != nil {
		return "", "", fmt.Errorf("invalid Codex credential object")
	}
	for key, value := range map[string]any{"type": "oauth", "access": updated.Access, "refresh": updated.Refresh, "expires": updated.Expires, "accountId": account} {
		fields[key], _ = json.Marshal(value)
	}
	all["openai-codex"], _ = json.Marshal(fields)
	encoded, err := json.MarshalIndent(all, "", "  ")
	if err != nil {
		return "", "", fmt.Errorf("cannot encode refreshed credentials")
	}
	// Preserve existing permission bits: rename would otherwise replace them
	// with the temporary file mode (Pi writes in place). New files stay 0600.
	var keepMode os.FileMode
	if st, err := os.Stat(a.Path); err == nil && !st.Mode().IsDir() {
		keepMode = st.Mode().Perm()
	}
	f, err := os.CreateTemp(filepath.Dir(a.Path), ".psi-auth-*")
	if err != nil {
		return "", "", fmt.Errorf("cannot persist refreshed credentials")
	}
	defer os.Remove(f.Name())
	if keepMode != 0 {
		chmod := a.Chmod
		if chmod == nil {
			chmod = os.Chmod
		}
		if err := chmod(f.Name(), keepMode); err != nil {
			f.Close()
			return "", "", fmt.Errorf("cannot preserve auth.json permissions")
		}
	}
	if _, err = f.Write(append(encoded, '\n')); err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil || closeErr != nil || !valid() {
		return "", "", fmt.Errorf("cannot safely persist refreshed credentials")
	}
	rename := a.Rename
	if rename == nil {
		rename = os.Rename
	}
	if err := rename(f.Name(), a.Path); err != nil {
		return "", "", fmt.Errorf("cannot replace refreshed auth.json")
	}
	if err := ctx.Err(); err != nil {
		return "", "", err
	}
	return updated.Access, account, nil
}

// authLockWaitLimit is the wait for the shared credential lock. The default
// matches Pi's lock deadline, so a holder that is slow to finish is waited
// for instead of reported as busy.
func authLockWaitLimit(waitAfter time.Duration) time.Duration {
	if waitAfter <= 0 {
		return 30 * time.Second
	}
	return waitAfter
}

// acquireAuthLock preserves auth.json-specific errors around the shared Pi lease.
func acquireAuthLock(ctx context.Context, path string, staleAfter, heartbeat, waitAfter time.Duration) (func(), func() bool, error) {
	unlock, valid, err := lockfile.Acquire(ctx, path, lockfile.Options{
		StaleAfter: staleAfter,
		Heartbeat:  heartbeat,
		Wait:       authLockWaitLimit(waitAfter),
	})
	if err == nil {
		return unlock, valid, nil
	}
	if ctx.Err() != nil {
		return nil, nil, ctx.Err()
	}
	if errors.Is(err, lockfile.ErrBusy) {
		return nil, nil, fmt.Errorf("auth.json is busy; retry or refresh the login in Pi")
	}
	if errors.Is(err, lockfile.ErrInspect) {
		return nil, nil, fmt.Errorf("cannot inspect auth.json lock")
	}
	return nil, nil, fmt.Errorf("cannot lock auth.json: %w", err)
}
