package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"psi/internal/config"
	"psi/internal/providerregistry"
)

const (
	maxAuthJSONSize = 4 << 20
	// Interactive credential writes report a busy lock promptly.
	apiKeyAuthLockWait = 5 * time.Second
)

// SaveAPIKey atomically stores a literal API key in Pi-compatible auth.json.
func SaveAPIKey(ctx context.Context, authPath, providerID, key string) error {
	if _, err := apiKeyProvider(providerID); err != nil {
		return err
	}
	if strings.ContainsAny(key, "\r\n") {
		return fmt.Errorf("API key must be a single line")
	}
	if !utf8.ValidString(key) {
		return fmt.Errorf("API key is invalid UTF-8")
	}
	key = strings.TrimSpace(key)
	if key == "" {
		return fmt.Errorf("API key is empty")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	authPath = resolvedWriteAuthPath(authPath)
	if err := os.MkdirAll(filepath.Dir(authPath), 0o700); err != nil {
		return fmt.Errorf("cannot create auth.json directory: %w", err)
	}
	if _, _, err := inspectAuthPath(authPath); err != nil {
		return err
	}
	unlock, leaseValid, err := acquireAuthLock(ctx, authPath+".lock", 0, 0, apiKeyAuthLockWait)
	if err != nil {
		return err
	}
	defer unlock()
	if _, _, err := inspectAuthPath(authPath); err != nil {
		return err
	}
	entries, mode, _, err := readAuthDocument(authPath)
	if err != nil {
		return err
	}
	entry, _, err := selectedAPIKeyEntry(entries, providerID)
	if err != nil {
		return err
	}
	entry["type"], _ = json.Marshal("api_key")
	entry["key"], _ = json.Marshal(config.EscapeCredentialLiteral(key))
	entries[providerID], err = json.Marshal(entry)
	if err != nil {
		return fmt.Errorf("cannot encode auth.json")
	}
	valid := func() bool { return ctx.Err() == nil && leaseValid() }
	if err := writeAuthDocument(authPath, entries, mode, valid); err != nil {
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
	if info, _, err := inspectAuthPath(authPath); err != nil {
		return false, err
	} else if info == nil {
		return false, nil
	}
	unlock, leaseValid, err := acquireAuthLock(ctx, authPath+".lock", 0, 0, apiKeyAuthLockWait)
	if err != nil {
		return false, err
	}
	defer unlock()
	info, _, err := inspectAuthPath(authPath)
	if err != nil {
		return false, err
	}
	if info == nil {
		return false, nil
	}
	entries, mode, exists, err := readAuthDocument(authPath)
	if err != nil {
		return false, err
	}
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
	valid := func() bool { return ctx.Err() == nil && leaseValid() }
	if err := writeAuthDocument(authPath, entries, mode, valid); err != nil {
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
		credentialType, err := storedEntryType(entry, providerID)
		if err != nil {
			return nil, err
		}
		if credentialType != "" && credentialType != "api_key" {
			continue
		}
		var key string
		if rawKey, present := entry["key"]; present && json.Unmarshal(rawKey, &key) != nil {
			return nil, fmt.Errorf("malformed auth.json credential for %s", providerID)
		}
		if key != "" {
			providers = append(providers, providerID)
		}
	}
	sort.Strings(providers)
	return providers, nil
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

// inspectAuthPath rejects symlinks and non-regular files before opening the
// target. A missing target is reported through exists=false.
func inspectAuthPath(path string) (os.FileInfo, os.FileMode, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, 0, nil
	}
	if err != nil {
		return nil, 0, fmt.Errorf("cannot inspect auth.json")
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return nil, 0, fmt.Errorf("refusing to write a symlinked auth.json")
	}
	if !info.Mode().IsRegular() {
		return nil, 0, fmt.Errorf("auth.json must be a regular file")
	}
	return info, info.Mode().Perm(), nil
}

func readAuthDocument(path string) (map[string]json.RawMessage, os.FileMode, bool, error) {
	info, mode, err := inspectAuthPath(path)
	if err != nil {
		return nil, 0, false, err
	}
	if info == nil {
		return map[string]json.RawMessage{}, 0o600, false, nil
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, 0, false, fmt.Errorf("cannot read auth.json")
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !opened.Mode().IsRegular() || !os.SameFile(info, opened) {
		return nil, 0, false, fmt.Errorf("auth.json changed while being read")
	}
	data, err := io.ReadAll(io.LimitReader(file, maxAuthJSONSize+1))
	if err != nil || len(data) > maxAuthJSONSize || !utf8.Valid(data) {
		return nil, 0, false, fmt.Errorf("auth.json is malformed or exceeds the size limit")
	}
	data = stripAuthBOM(data)
	var entries map[string]json.RawMessage
	if json.Unmarshal(data, &entries) != nil || entries == nil {
		return nil, 0, false, fmt.Errorf("auth.json must be a JSON object")
	}
	return entries, mode, true, nil
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
		return nil, "", err
	}
	if credentialType == "oauth" || credentialType == "oauth_token" {
		return nil, "", fmt.Errorf("refusing to overwrite the OAuth credential for %s", providerID)
	}
	if credentialType != "" && credentialType != "api_key" {
		return nil, "", fmt.Errorf("unsupported credential type for %s", providerID)
	}
	return entry, credentialType, nil
}

func decodeStoredEntry(raw json.RawMessage, providerID string) (map[string]json.RawMessage, error) {
	var entry map[string]json.RawMessage
	if json.Unmarshal(raw, &entry) != nil || entry == nil {
		return nil, fmt.Errorf("auth.json credential for %s must be an object", providerID)
	}
	return entry, nil
}

func storedEntryType(entry map[string]json.RawMessage, providerID string) (string, error) {
	var credentialType string
	if raw, ok := entry["type"]; ok && json.Unmarshal(raw, &credentialType) != nil {
		return "", fmt.Errorf("malformed auth.json credential for %s", providerID)
	}
	return credentialType, nil
}

func writeAuthDocument(path string, entries map[string]json.RawMessage, mode os.FileMode, valid func() bool) error {
	encoded, err := json.MarshalIndent(entries, "", "  ")
	if err != nil {
		return fmt.Errorf("cannot encode auth.json")
	}
	encoded = append(encoded, '\n')
	if len(encoded) > maxAuthJSONSize {
		return fmt.Errorf("auth.json exceeds the size limit")
	}
	dir := filepath.Dir(path)
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
	if err := temp.Chmod(mode); err != nil {
		return fmt.Errorf("cannot set auth.json permissions")
	}
	if _, err := temp.Write(encoded); err != nil {
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
	if !valid() {
		return fmt.Errorf("auth.json lock changed; refusing to write")
	}
	if err := os.Rename(tempPath, path); err != nil {
		return fmt.Errorf("cannot replace auth.json")
	}
	if dirFile, err := os.Open(dir); err == nil {
		_ = dirFile.Sync()
		_ = dirFile.Close()
	}
	return nil
}
