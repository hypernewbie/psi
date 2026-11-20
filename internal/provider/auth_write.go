package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"time"
	"unicode/utf8"

	"github.com/hypernewbie/psi/internal/providerregistry"
	"github.com/hypernewbie/psi/internal/secretmem"
)

const (
	maxAuthJSONSize = 4 << 20
	// Interactive credential writes report a busy lock promptly.
	apiKeyAuthLockWait = 5 * time.Second
)

var (
	errAuthDocumentTooLarge = errors.New("auth.json exceeds the size limit")
	errAuthLogoutBusy       = errors.New("auth.json is busy; retry /logout")
)

// CredentialKind is the stored representation used by /logout.
type CredentialKind string

const (
	CredentialAPIKey CredentialKind = "api_key"
	CredentialOAuth  CredentialKind = "oauth"
)

// StoredCredential identifies one removable credential entry for /logout.
type StoredCredential struct {
	ProviderID string
	Kind       CredentialKind
}

// SaveAPIKey atomically stores a literal API key in Pi-compatible auth.json.
// The caller owns key and must wipe it after SaveAPIKey returns.
func SaveAPIKey(ctx context.Context, authPath, providerID string, key []byte) error {
	if _, err := apiKeyProvider(providerID); err != nil {
		return err
	}
	if bytes.IndexByte(key, '\r') >= 0 || bytes.IndexByte(key, '\n') >= 0 {
		return fmt.Errorf("API key must be a single line")
	}
	if !utf8.Valid(key) {
		return fmt.Errorf("API key is invalid UTF-8")
	}
	key = bytes.TrimSpace(key)
	if len(key) == 0 {
		return fmt.Errorf("API key is empty")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	authPath = resolvedWriteAuthPath(authPath)
	if err := os.MkdirAll(filepath.Dir(authPath), 0o700); err != nil {
		return fmt.Errorf("cannot create auth.json directory: %w", err)
	}
	unlock, leaseValid, err := acquireAuthLock(ctx, authPath+".lock", 0, 0, apiKeyAuthLockWait)
	if err != nil {
		return err
	}
	defer unlock()
	target, err := resolveAuthTarget(authPath)
	if err != nil {
		return err
	}
	entries, mode, _, err := readAuthDocument(target)
	if err != nil {
		return err
	}
	defer releaseRawEntries(entries)
	entry, _, err := selectedAPIKeyEntry(entries, providerID)
	if err != nil {
		return err
	}
	defer releaseRawEntries(entry)
	if old, ok := entry["type"]; ok {
		secretmem.Wipe(old[:cap(old)])
	}
	entry["type"], _ = json.Marshal("api_key")
	encodedKey, err := encodeCredentialLiteral(key)
	if err != nil {
		return err
	}
	if old, ok := entry["key"]; ok {
		secretmem.Wipe(old[:cap(old)])
	}
	entry["key"] = json.RawMessage(encodedKey)
	if old, ok := entries[providerID]; ok {
		secretmem.Wipe(old[:cap(old)])
	}
	entries[providerID], err = json.Marshal(entry)
	if err != nil {
		return fmt.Errorf("cannot encode auth.json")
	}
	valid := func() error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if !leaseValid() {
			return fmt.Errorf("auth.json lock changed; refusing to write")
		}
		return nil
	}
	if err := writeAuthDocument(target, entries, mode, valid); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return err
	}
	return nil
}

// LogoutCredential removes one stored API-key or OAuth credential without
// touching other provider entries or creating a missing auth.json. The
// selected kind must still match the entry on disk; a refresh may have
// replaced it while the picker was open. OpenAI and Codex OAuth removal can
// attempt one bounded revocation request and report the outcome separately.
// Global Kimi OAuth removal is local only.
func LogoutCredential(ctx context.Context, authPath string, selected StoredCredential, client *http.Client) (LogoutResult, error) {
	return logoutCredential(ctx, authPath, selected, client, authFileHooks{})
}

func logoutCredential(ctx context.Context, authPath string, selected StoredCredential, client *http.Client, hooks authFileHooks) (LogoutResult, error) {
	result := LogoutResult{}
	spec, ok := providerregistry.Lookup(selected.ProviderID)
	if !ok || !credentialKindAllowed(spec.Auth, selected.Kind) {
		return result, fmt.Errorf("unsupported credential for %s", selected.ProviderID)
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	authPath = resolvedWriteAuthPath(authPath)
	if _, err := os.Lstat(authPath); errors.Is(err, os.ErrNotExist) {
		return result, nil
	} else if err != nil {
		return result, fmt.Errorf("cannot inspect auth.json")
	}
	lockCtx, cancelLock := context.WithTimeout(ctx, apiKeyAuthLockWait)
	defer cancelLock()
	unlockLogical, logicalValid, err := acquireLogoutLock(ctx, lockCtx, authPath+".lock")
	if err != nil {
		return result, err
	}
	defer unlockLogical()
	target, err := resolveAuthTarget(authPath)
	if err != nil {
		return result, err
	}
	if !target.exists {
		return result, ctx.Err()
	}
	// Pi locks the resolved file. A shared auth.json needs both leases when the
	// logical and resolved lock paths name different physical directories.
	// Taking the same lease twice would make logout wait on itself.
	var resolvedValid func() bool
	resolvedLock := target.path + ".lock"
	if !sameAuthLockLocation(authPath+".lock", resolvedLock) {
		unlock, valid, err := acquireLogoutLock(ctx, lockCtx, resolvedLock)
		if err != nil {
			return result, err
		}
		defer unlock()
		resolvedValid = valid
	}
	cancelLock()
	checkLeases := func() error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if !logicalValid() {
			return fmt.Errorf("auth.json lock changed; refusing to write")
		}
		if resolvedValid != nil && !resolvedValid() {
			return fmt.Errorf("auth.json target lock changed; refusing to write")
		}
		return ctx.Err()
	}
	if err := target.recheck(); err != nil {
		return result, err
	}
	if err := checkLeases(); err != nil {
		return result, err
	}
	entries, mode, _, err := readAuthDocument(target)
	if err != nil {
		return result, err
	}
	defer releaseRawEntries(entries)
	raw, present := entries[selected.ProviderID]
	if !present {
		return result, nil
	}
	entry, err := decodeStoredEntry(raw, selected.ProviderID)
	if err != nil {
		return result, err
	}
	defer releaseRawEntries(entry)
	kind, err := classifyStoredCredential(entry, spec)
	if err != nil {
		return result, err
	}
	if kind != selected.Kind {
		return result, fmt.Errorf("stored credential for %s changed; reopen /logout", selected.ProviderID)
	}

	valid := checkLeases
	if kind == CredentialOAuth {
		// Only OpenAI credentials can be sent to the OpenAI revocation endpoint.
		switch selected.ProviderID {
		case "openai", "openai-codex":
			request, usable := selectRevokeRequest(selected.ProviderID, entry)
			if !usable {
				result.Revocation = RevocationNoUsableToken
			} else {
				if err := target.recheck(); err != nil {
					return result, err
				}
				if err := checkLeases(); err != nil {
					return result, err
				}
				result.Revocation = revokeOAuthToken(ctx, request, client)
			}
		}
		// The writer calls this before staging and again before replacement.
		// A non-cooperating writer can change the file without replacing it.
		valid = func() error {
			if err := checkLeases(); err != nil {
				return err
			}
			current, currentMode, exists, err := readAuthDocument(target)
			defer releaseRawEntries(current)
			matches := exists && authSnapshotMatches(entries, mode, current, currentMode)
			if leaseErr := checkLeases(); leaseErr != nil {
				return leaseErr
			}
			if err != nil {
				return err
			}
			if !matches {
				return fmt.Errorf("auth.json changed during logout; reopen /logout")
			}
			return nil
		}
	}
	// The write map borrows raw values; entries remains the sole owner.
	remaining := maps.Clone(entries)
	delete(remaining, selected.ProviderID)
	if err := writeAuthDocumentWithHooks(target, remaining, mode, valid, hooks); err != nil {
		if ctx.Err() != nil {
			return result, ctx.Err()
		}
		return result, err
	}
	result.Removed = true
	return result, nil
}

func acquireLogoutLock(ctx, lockCtx context.Context, path string) (func(), func() bool, error) {
	unlock, valid, err := acquireAuthLock(lockCtx, path, 0, 0, 0)
	if err == nil {
		return unlock, valid, nil
	}
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return nil, nil, errAuthLogoutBusy
	}
	return nil, nil, err
}

// sameAuthLockLocation reports whether two lock paths name the same lease. A
// missing path, a symlink, or a non-directory does not count as a match; those
// cases fall through to the normal lease acquisition, which fails closed.
func sameAuthLockLocation(a, b string) bool {
	if absA, errA := filepath.Abs(a); errA == nil {
		if absB, errB := filepath.Abs(b); errB == nil && absA == absB {
			return true
		}
	}
	infoA, errA := os.Lstat(a)
	infoB, errB := os.Lstat(b)
	if errA != nil || errB != nil || !infoA.IsDir() || !infoB.IsDir() {
		return false
	}
	statA, errA := statAuthIdentity(a)
	statB, errB := statAuthIdentity(b)
	if errA != nil || errB != nil {
		return false
	}
	return os.SameFile(statA, statB)
}

// StoredCredentials returns sorted built-in credentials that /logout can
// remove. It reads auth.json once, excludes custom providers, and lists one
// entry per provider because a provider stores either an API key or OAuth.
func StoredCredentials(authPath string) ([]StoredCredential, error) {
	entries, err := AuthEntries(resolvedWriteAuthPath(authPath))
	if err != nil {
		return nil, err
	}
	defer ReleaseAuthEntries(entries)
	var credentials []StoredCredential
	for _, providerID := range providerregistry.IDs() {
		spec, ok := providerregistry.Lookup(providerID)
		if !ok {
			continue
		}
		raw, present := entries[providerID]
		if !present {
			continue
		}
		entry, err := decodeStoredEntry(raw, providerID)
		if err != nil {
			return nil, err
		}
		kind, err := classifyStoredCredential(entry, spec)
		releaseRawEntries(entry)
		if err != nil {
			return nil, err
		}
		if kind != "" {
			credentials = append(credentials, StoredCredential{ProviderID: providerID, Kind: kind})
		}
	}
	return credentials, nil
}

// credentialKindAllowed maps the registry auth kind to the entries logout may
// list and remove. Custom and unknown kinds fail closed in the caller.
func credentialKindAllowed(auth providerregistry.AuthKind, kind CredentialKind) bool {
	switch auth {
	case providerregistry.AuthAPIKey:
		return kind == CredentialAPIKey
	case providerregistry.AuthAPIKeyOrOAuth:
		return kind == CredentialAPIKey || kind == CredentialOAuth
	case providerregistry.AuthCodexOAuth, providerregistry.AuthKimiGlobalOAuth:
		return kind == CredentialOAuth
	default:
		return false
	}
}

// classifyStoredCredential borrows entry. Unsupported kinds are ignored;
// an allowed OAuth entry stays removable without a usable token.
func classifyStoredCredential(entry map[string]json.RawMessage, spec providerregistry.Spec) (CredentialKind, error) {
	credentialType, err := storedEntryType(entry, spec.ID)
	if err != nil {
		return "", err
	}
	kind := CredentialKind(credentialType)
	if kind == "" {
		kind = CredentialAPIKey
	}
	if !credentialKindAllowed(spec.Auth, kind) {
		return "", nil
	}
	if kind == CredentialAPIKey {
		present, err := storedAPIKeyPresent(entry, spec.ID)
		if err != nil || !present {
			return "", err
		}
	}
	return kind, nil
}

// storedAPIKeyPresent borrows entry and inspects the JSON string token directly
// so the logout listing does not create a second secret string from the key.
// The caller releases the decoded entry once.
func storedAPIKeyPresent(entry map[string]json.RawMessage, providerID string) (bool, error) {
	rawKey, present := entry["key"]
	if !present {
		return false, nil
	}
	value := bytes.TrimSpace(rawKey)
	if bytes.Equal(value, []byte("null")) {
		return false, nil
	}
	if len(value) < 2 || value[0] != '"' || value[len(value)-1] != '"' {
		return false, fmt.Errorf("malformed auth.json credential for %s", providerID)
	}
	return len(value) > 2, nil
}

func apiKeyProvider(providerID string) (providerregistry.Spec, error) {
	spec, ok := providerregistry.Lookup(providerID)
	if !ok || spec.Auth != providerregistry.AuthAPIKey {
		return providerregistry.Spec{}, fmt.Errorf("unsupported API-key provider %q", providerID)
	}
	return spec, nil
}

func resolvedWriteAuthPath(authPath string) string {
	if authPath != "" {
		return authPath
	}
	return resolveAuthPath(map[string]string{
		"PSI_CODING_AGENT_DIR": os.Getenv("PSI_CODING_AGENT_DIR"),
		"PI_CODING_AGENT_DIR":  os.Getenv("PI_CODING_AGENT_DIR"),
	}, "")
}

// readAuthDocument reads the captured target identity. A missing target is
// reported through exists=false without opening a path that may have appeared.
func readAuthDocument(target authTarget) (map[string]json.RawMessage, os.FileMode, bool, error) {
	if !target.exists {
		return map[string]json.RawMessage{}, 0o600, false, nil
	}
	file, err := os.Open(target.path)
	if err != nil {
		return nil, 0, false, fmt.Errorf("cannot read auth.json")
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !opened.Mode().IsRegular() || !os.SameFile(target.info, opened) {
		return nil, 0, false, fmt.Errorf("auth.json changed while being read")
	}
	data, err := io.ReadAll(io.LimitReader(file, maxAuthJSONSize+1))
	if err != nil || len(data) > maxAuthJSONSize || !utf8.Valid(data) {
		secretmem.Wipe(data[:cap(data)])
		return nil, 0, false, fmt.Errorf("auth.json is malformed or exceeds the size limit")
	}
	defer secretmem.Wipe(data[:cap(data)])
	data = stripAuthBOM(data)
	var entries map[string]json.RawMessage
	if json.Unmarshal(data, &entries) != nil || entries == nil {
		releaseRawEntries(entries)
		return nil, 0, false, fmt.Errorf("auth.json must be a JSON object")
	}
	return entries, opened.Mode().Perm(), true, nil
}

func selectedAPIKeyEntry(entries map[string]json.RawMessage, providerID string) (map[string]json.RawMessage, string, error) {
	raw, present := entries[providerID]
	if !present {
		return map[string]json.RawMessage{}, "", nil
	}
	entry, err := decodeStoredEntry(raw, providerID)
	if err != nil {
		return nil, "", err
	}
	credentialType, err := storedEntryType(entry, providerID)
	if err != nil {
		releaseRawEntries(entry)
		return nil, "", err
	}
	if credentialType == "oauth" || credentialType == "oauth_token" {
		releaseRawEntries(entry)
		return nil, "", fmt.Errorf("refusing to overwrite the OAuth credential for %s", providerID)
	}
	if credentialType != "" && credentialType != "api_key" {
		releaseRawEntries(entry)
		return nil, "", fmt.Errorf("unsupported credential type for %s", providerID)
	}
	return entry, credentialType, nil
}

func decodeStoredEntry(raw json.RawMessage, providerID string) (map[string]json.RawMessage, error) {
	var entry map[string]json.RawMessage
	if json.Unmarshal(raw, &entry) != nil || entry == nil {
		releaseRawEntries(entry)
		return nil, fmt.Errorf("auth.json credential for %s must be an object", providerID)
	}
	return entry, nil
}

func storedEntryType(entry map[string]json.RawMessage, providerID string) (string, error) {
	raw, ok := entry["type"]
	if !ok {
		return "", nil
	}
	var credentialType string
	if err := json.Unmarshal(raw, &credentialType); err != nil {
		return "", fmt.Errorf("malformed auth.json credential for %s", providerID)
	}
	return credentialType, nil
}

func writeAuthDocument(target authTarget, entries map[string]json.RawMessage, mode os.FileMode, valid func() error) error {
	return writeAuthDocumentWithHooks(target, entries, mode, valid, authFileHooks{})
}

// writeAuthDocumentWithHooks adds the filesystem fault seams used by
// deterministic commit tests. A nil hook selects the os default.
func writeAuthDocumentWithHooks(target authTarget, entries map[string]json.RawMessage, mode os.FileMode, valid func() error, hooks authFileHooks) error {
	encoded, err := json.MarshalIndent(entries, "", "  ")
	if err != nil {
		return fmt.Errorf("cannot encode auth.json")
	}
	defer secretmem.Wipe(encoded[:cap(encoded)])
	if len(encoded)+1 > maxAuthJSONSize {
		return errAuthDocumentTooLarge
	}
	if valid != nil {
		if err := valid(); err != nil {
			return err
		}
	}
	if err := target.recheck(); err != nil {
		return err
	}
	dir := filepath.Dir(target.path)
	temp, err := os.CreateTemp(dir, ".psi-auth-*")
	if err != nil {
		return fmt.Errorf("cannot create auth.json temporary file")
	}
	tempPath := temp.Name()
	closed := false
	defer func() {
		if !closed {
			_ = temp.Close()
		}
		_ = os.Remove(tempPath)
	}()
	chmod := hooks.Chmod
	if chmod != nil {
		if err := chmod(tempPath, mode); err != nil {
			return fmt.Errorf("cannot set auth.json permissions")
		}
	} else if err := temp.Chmod(mode); err != nil {
		return fmt.Errorf("cannot set auth.json permissions")
	}
	if _, err := temp.Write(encoded); err != nil {
		return fmt.Errorf("cannot write auth.json")
	}
	if _, err := temp.Write([]byte{'\n'}); err != nil {
		return fmt.Errorf("cannot write auth.json")
	}
	if err := temp.Sync(); err != nil {
		return fmt.Errorf("cannot sync auth.json")
	}
	if err := temp.Close(); err != nil {
		closed = true
		return fmt.Errorf("cannot close auth.json")
	}
	closed = true
	if valid != nil {
		if err := valid(); err != nil {
			return err
		}
	}
	if err := target.recheck(); err != nil {
		return err
	}
	rename := hooks.Rename
	if rename == nil {
		rename = os.Rename
	}
	if err := rename(tempPath, target.path); err != nil {
		return fmt.Errorf("cannot replace auth.json")
	}
	if dirFile, err := os.Open(dir); err == nil {
		_ = dirFile.Sync()
		_ = dirFile.Close()
	}
	return nil
}
