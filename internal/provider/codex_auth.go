package provider

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const codexTokenURL = "https://auth.openai.com/oauth/token"
const codexClientID = "app_EMoamEEZ73f0CkXaXp7hrann"

// CodexAuth reuses Pi's existing subscription login. Refresh updates only that
// provider and uses the same auth.json.lock directory protocol as proper-lockfile.
// There is deliberately no API-key fallback and no interactive login flow.
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
	unlock, _, err := acquireAuthLock(ctx, a.Path+".lock", a.StaleAfter, a.HeartbeatInterval)
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
	_, entry, err := a.loadShared(ctx)
	if err != nil {
		return err
	}
	if entry.Expires <= time.Now().UnixMilli() {
		if entry.Refresh == "" {
			return fmt.Errorf("Codex login expired; log in again through Pi")
		}
		return nil
	}
	_, err = codexAccount(entry)
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
	unlock, valid, err := acquireAuthLock(ctx, a.Path+".lock", a.StaleAfter, a.HeartbeatInterval)
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

// acquireAuthLock implements the shared Pi-compatible lease: atomic mkdir,
// heartbeat ownership, and bounded stale recovery. staleAfter <= 0 selects the
// Pi async policy (30 seconds). A fresh lease is never stolen and a replaced
// lease is never deleted by a former owner.
func acquireAuthLock(ctx context.Context, path string, staleAfter, heartbeat time.Duration) (func(), func() bool, error) {
	if staleAfter <= 0 {
		staleAfter = 30 * time.Second
	}
	if heartbeat <= 0 {
		heartbeat = 2 * time.Second
	}
	waitCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	for {
		if err := waitCtx.Err(); err != nil {
			if ctx.Err() != nil {
				return nil, nil, ctx.Err()
			}
			return nil, nil, fmt.Errorf("auth.json is busy; retry or refresh the login in Pi")
		}
		err := os.Mkdir(path, 0700)
		if err == nil {
			break
		}
		if !os.IsExist(err) {
			return nil, nil, fmt.Errorf("cannot lock auth.json")
		}
		// Another holder may own the lease. Reclaim only a provably stale,
		// unchanged lease; never steal a fresh or replaced one.
		before, statErr := os.Stat(path)
		if statErr == nil {
			if time.Since(before.ModTime()) > staleAfter {
				// Re-check identity before removal so a fresh replacement
				// created between stat and remove is never deleted.
				if after, err := os.Stat(path); err == nil && os.SameFile(before, after) &&
					time.Since(after.ModTime()) > staleAfter {
					// Older psi builds left an owner file in the directory.
					// Remove only that known entry, never arbitrary contents.
					_ = os.Remove(filepath.Join(path, "owner"))
					_ = os.Remove(path)
				}
			}
		}
		timer := time.NewTimer(50 * time.Millisecond)
		select {
		case <-waitCtx.Done():
			timer.Stop()
		case <-timer.C:
		}
	}
	// Keep the directory empty for proper-lockfile's rmdir-based recovery.
	// An open descriptor pins its inode, preventing identity reuse while a
	// former owner is still alive, without adding a non-Pi owner file.
	dir, err := os.Open(path)
	if err != nil {
		return nil, nil, fmt.Errorf("cannot inspect auth.json lock")
	}
	owner, err := dir.Stat()
	if err != nil {
		dir.Close()
		return nil, nil, fmt.Errorf("cannot inspect auth.json lock")
	}
	var compromised atomic.Bool
	var leaseMu sync.Mutex
	mtime := owner.ModTime()
	validLocked := func() bool {
		st, err := os.Stat(path)
		return !compromised.Load() && err == nil && os.SameFile(owner, st) && st.ModTime().Equal(mtime)
	}
	valid := func() bool {
		leaseMu.Lock()
		defer leaseMu.Unlock()
		return validLocked()
	}
	stop, done := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(heartbeat)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case now := <-ticker.C:
				leaseMu.Lock()
				ok := validLocked()
				if ok {
					ok = os.Chtimes(path, now, now) == nil
				}
				if ok {
					st, err := os.Stat(path)
					ok = err == nil && os.SameFile(owner, st)
					if ok {
						mtime = st.ModTime() // retain filesystem timestamp precision
					}
				}
				leaseMu.Unlock()
				if !ok {
					compromised.Store(true)
					return
				}
			}
		}
	}()
	var once sync.Once
	unlock := func() {
		once.Do(func() {
			close(stop)
			<-done
			stillValid := valid()
			// Windows cannot remove a directory with an open handle in
			// it, so release the descriptor before removing the lease.
			dir.Close()
			if stillValid {
				_ = os.Remove(path)
			}
		})
	}
	return unlock, valid, nil
}
