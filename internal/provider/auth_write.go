package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"time"
	"unicode/utf8"

	"psi/internal/providerregistry"
	"psi/internal/secretmem"
)

const (
	maxAuthJSONSize = 4 << 20
	// Interactive credential writes report a busy lock promptly.
	apiKeyAuthLockWait = 5 * time.Second
)

var errAuthDocumentTooLarge = errors.New("auth.json exceeds the size limit")

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

// RemoveAPIKey removes one stored API-key credential without touching other
// provider entries or creating a missing auth.json.
func RemoveAPIKey(ctx context.Context, authPath, providerID string) (bool, error) {
	if _, err := apiKeyProvider(providerID); err != nil {
		return false, err
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	authPath = resolvedWriteAuthPath(authPath)
	if _, err := os.Lstat(authPath); errors.Is(err, os.ErrNotExist) {
		return false, nil
	} else if err != nil {
		return false, fmt.Errorf("cannot inspect auth.json")
	}
	unlock, leaseValid, err := acquireAuthLock(ctx, authPath+".lock", 0, 0, apiKeyAuthLockWait)
	if err != nil {
		return false, err
	}
	defer unlock()
	target, err := resolveAuthTarget(authPath)
	if err != nil {
		return false, err
	}
	if !target.exists {
		return false, nil
	}
	entries, mode, exists, err := readAuthDocument(target)
	if err != nil {
		return false, err
	}
	defer releaseRawEntries(entries)
	if !exists {
		return false, nil
	}
	raw, present := entries[providerID]
	if !present {
		return false, nil
	}
	entry, err := decodeStoredEntry(raw, providerID)
	if err != nil {
		return false, err
	}
	defer releaseRawEntries(entry)
	credentialType, err := storedEntryType(entry, providerID)
	if err != nil {
		return false, err
	}
	if credentialType == "oauth" || credentialType == "oauth_token" {
		return false, fmt.Errorf("refusing to remove the OAuth credential for %s", providerID)
	}
	if credentialType != "" && credentialType != "api_key" {
		return false, fmt.Errorf("unsupported credential type for %s", providerID)
	}
	delete(entries, providerID)
	secretmem.Wipe(raw[:cap(raw)])
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
			return false, ctx.Err()
		}
		return false, err
	}
	return true, nil
}

// StoredAPIKeyProviders returns sorted built-in providers with a stored key.
// It reads auth.json once and excludes OAuth and custom-provider entries.
func StoredAPIKeyProviders(authPath string) ([]string, error) {
	entries, err := AuthEntries(resolvedWriteAuthPath(authPath))
	if err != nil {
		return nil, err
	}
	defer ReleaseAuthEntries(entries)
	if len(entries) == 0 {
		return nil, nil
	}
	var providers []string
	for _, providerID := range providerregistry.IDs() {
		spec, ok := providerregistry.Lookup(providerID)
		if !ok || spec.Auth != providerregistry.AuthAPIKey {
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
		hasKey, err := storedAPIKeyPresent(entry, providerID)
		if err != nil {
			return nil, err
		}
		if hasKey {
			providers = append(providers, providerID)
		}
	}
	sort.Strings(providers)
	return providers, nil
}

// storedAPIKeyPresent owns entry for the duration of the inspection and
// releases its decoded raw-message allocations before returning. It inspects
// the JSON string token directly so the logout listing does not create a
// second secret string from the key.
func storedAPIKeyPresent(entry map[string]json.RawMessage, providerID string) (bool, error) {
	defer releaseRawEntries(entry)
	credentialType, err := storedEntryType(entry, providerID)
	if err != nil {
		return false, err
	}
	if credentialType != "" && credentialType != "api_key" {
		return false, nil
	}
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
	return entries, target.info.Mode().Perm(), true, nil
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
