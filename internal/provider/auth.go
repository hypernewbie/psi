package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/hypernewbie/psi/internal/config"
	"github.com/hypernewbie/psi/internal/providerregistry"
	"github.com/hypernewbie/psi/internal/secretmem"
)

// authReads counts shared auth.json reads. The model lists resolve every
// provider from one read, and the tests keep that guarantee.
var authReads atomic.Int64

// SelectProviderAPIKey preserves the Anthropic-compatible caller and its no-fallback
// behavior. New API-key providers use SelectProviderAPIKey.
func SelectAPIKey(env map[string]string, authPath string) (key, source string, err error) {
	return SelectProviderAPIKey(env, authPath, "anthropic", "")
}

// SelectProviderAPIKey matches Pi's supported precedence: provider-specific
// auth.json key, models.json key, then the provider's exact environment
// variable. A provider that models.json declares has no environment variable:
// it uses its stored key or its configured key. The function never executes
// credential commands and never crosses billing sources after an unsupported
// stored credential is found.
func SelectProviderAPIKey(env map[string]string, authPath, providerID, configuredKey string) (key, source string, err error) {
	return SelectProviderAPIKeyInput(env, authPath, providerID, legacyAPIKeyInput(configuredKey))
}

// SelectProviderAPIKeyInput resolves one provider's API key with the
// models.json field presence and strictness that the caller observed. The
// background context is unchanged; callers that own a bounded usage job use
// SelectProviderAPIKeyInputContext so its deadline also bounds the auth lease.
func SelectProviderAPIKeyInput(env map[string]string, authPath, providerID string, input APIKeyInput) (key, source string, err error) {
	return SelectProviderAPIKeyInputContext(context.Background(), env, authPath, providerID, input)
}

// SelectProviderAPIKeyInputContext resolves one provider's API key with the
// caller's cancellation and deadline, which cap the shared auth lease wait.
func SelectProviderAPIKeyInputContext(ctx context.Context, env map[string]string, authPath, providerID string, input APIKeyInput) (key, source string, err error) {
	spec, builtin := providerregistry.Lookup(providerID)
	if builtin && spec.Auth != providerregistry.AuthAPIKey && spec.Auth != providerregistry.AuthAPIKeyOrOAuth {
		return "", "", fmt.Errorf("provider %q does not use API-key authentication", providerID)
	}
	authPath = resolveAuthPath(env, authPath)
	entries, err := AuthEntriesContext(ctx, authPath)
	if err != nil {
		return "", "", err
	}
	defer ReleaseAuthEntries(entries)
	return apiKeyFromEntriesInput(env, entries, authPath, providerID, input)
}

// APIKeyInput describes the models.json apiKey field for one provider.
// Present distinguishes an omitted field from an explicit empty or null one.
// Strict rejects a consulted blank value, and a whitespace-only resolution,
// instead of treating it as an absent credential.
type APIKeyInput struct {
	Value   string
	Present bool
	Strict  bool
}

// legacyAPIKeyInput keeps the callers that only pass a string on the old
// policy: an empty configured value is omitted, never invalid.
func legacyAPIKeyInput(value string) APIKeyInput {
	return APIKeyInput{Value: value, Present: value != ""}
}

// MissingAPIKeyError reports genuine absence after every configured source was
// consulted. Callers may grant a keyless request only for this typed result.
type MissingAPIKeyError struct {
	Message string
}

func (e *MissingAPIKeyError) Error() string { return e.Message }

// resolveAuthPath uses the agent directory when the caller has no path.
func resolveAuthPath(env map[string]string, authPath string) string {
	if authPath != "" {
		return authPath
	}
	agentDir := env["PSI_CODING_AGENT_DIR"]
	if agentDir == "" {
		agentDir = env["PI_CODING_AGENT_DIR"]
	}
	if agentDir == "" {
		home, _ := os.UserHomeDir()
		agentDir = filepath.Join(home, ".pi", "agent")
	}
	return filepath.Join(agentDir, "auth.json")
}

// AuthEntries reads auth.json once through the shared lease and returns its
// provider entries. A missing file yields no entries and no error, so
// providers can fall back to their models.json key or environment variable.
// A busy or malformed file returns an error instead of a partial view.
// The caller owns the returned raw-message allocations and must call
// ReleaseAuthEntries after the last credential read.
func AuthEntries(authPath string) (map[string]json.RawMessage, error) {
	return AuthEntriesContext(context.Background(), authPath)
}

// AuthEntriesContext is AuthEntries with the caller's cancellation and
// deadline, which bound the shared auth lease wait.
func AuthEntriesContext(ctx context.Context, authPath string) (map[string]json.RawMessage, error) {
	authReads.Add(1)
	data, readErr := readAuthSharedContext(ctx, authPath)
	if readErr != nil {
		if authPathAbsent(authPath, readErr) {
			return nil, nil
		}
		return nil, fmt.Errorf("cannot read auth.json: %w", readErr)
	}
	defer secretmem.Wipe(data[:cap(data)])
	data = stripAuthBOM(data)
	var entries map[string]json.RawMessage
	if !utf8.Valid(data) || json.Unmarshal(data, &entries) != nil || entries == nil {
		releaseRawEntries(entries)
		return nil, fmt.Errorf("auth.json is malformed")
	}
	return entries, nil
}

// authPathAbsent distinguishes a missing file from a dangling symlink or
// another lock/read failure. Only an absent file permits credential fallback.
func authPathAbsent(path string, err error) bool {
	if !errors.Is(err, os.ErrNotExist) {
		return false
	}
	_, statErr := os.Lstat(path)
	return errors.Is(statErr, os.ErrNotExist)
}

// apiKeyFromEntriesInput resolves one provider's API key from one auth.json
// snapshot. It keeps the precedence and the no-fallback rules of
// SelectProviderAPIKey. A stored or configured credential is validated before
// any lower-priority source is consulted.
func apiKeyFromEntriesInput(env map[string]string, entries map[string]json.RawMessage, authPath, providerID string, input APIKeyInput) (key, source string, err error) {
	spec, builtin := providerregistry.Lookup(providerID)
	if builtin && spec.Auth != providerregistry.AuthAPIKey && spec.Auth != providerregistry.AuthAPIKeyOrOAuth {
		return "", "", fmt.Errorf("provider %q does not use API-key authentication", providerID)
	}
	if raw, present := entries[providerID]; present {
		var entry struct {
			Type string `json:"type"`
			Key  string `json:"key"`
		}
		if err := json.Unmarshal(raw, &entry); err != nil {
			if providerID == "anthropic" {
				return "", "", fmt.Errorf("auth.json Anthropic credential must be an API-key object")
			}
			return "", "", fmt.Errorf("auth.json credential for %s must be an API-key object", providerID)
		}
		if entry.Type == "oauth" || entry.Type == "oauth_token" {
			if providerID == "anthropic" {
				return "", "", fmt.Errorf("Anthropic OAuth subscription credentials are unsupported; no API-key fallback (select API-key access explicitly in auth.json)")
			}
			return "", "", fmt.Errorf("%s OAuth credentials are unsupported for API-key access; no fallback", providerID)
		}
		if entry.Type != "" && entry.Type != "api_key" {
			if providerID == "anthropic" {
				return "", "", fmt.Errorf("unsupported Anthropic credential type")
			}
			return "", "", fmt.Errorf("unsupported %s credential type", providerID)
		}
		if entry.Key == "" {
			if providerID == "anthropic" {
				return "", "", fmt.Errorf("auth.json Anthropic API key is empty")
			}
			return "", "", fmt.Errorf("auth.json %s API key is empty", providerID)
		}
		key, err := config.ResolveCredentialWithEnv(entry.Key, envLookup(env))
		if err != nil {
			return "", "", credentialError(providerID, err)
		}
		if input.Strict && strings.TrimSpace(key) == "" {
			return "", "", credentialError(providerID, fmt.Errorf("auth.json %s API key is blank", providerID))
		}
		return key, "auth.json:" + providerID, nil
	}
	if providerID == "anthropic" {
		if _, present := entries["anthropic-oauth"]; present {
			return "", "", fmt.Errorf("Anthropic OAuth subscription credentials are unsupported; no silent API-key fallback")
		}
	}
	if input.Present {
		if input.Strict && strings.TrimSpace(input.Value) == "" {
			return "", "", fmt.Errorf("models.json %s API key is empty", providerID)
		}
		key, err := config.ResolveCredentialWithEnv(input.Value, envLookup(env))
		if err != nil {
			return "", "", credentialError(providerID, err)
		}
		if key == "" || (input.Strict && strings.TrimSpace(key) == "") {
			return "", "", fmt.Errorf("models.json %s API key is empty", providerID)
		}
		return key, "models.json:" + providerID, nil
	}
	if builtin {
		if value, present := env[spec.Environment]; present && (value != "" || input.Strict) {
			if input.Strict && strings.TrimSpace(value) == "" {
				return "", "", fmt.Errorf("%s is blank", spec.Environment)
			}
			return value, spec.Environment, nil
		}
	}
	if providerID == "anthropic" {
		return "", "", &MissingAPIKeyError{Message: fmt.Sprintf("no Anthropic credential: set ANTHROPIC_API_KEY or add an API-key entry to %s", authPath)}
	}
	if !builtin {
		return "", "", &MissingAPIKeyError{Message: fmt.Sprintf("no %s credential: add an API-key entry to auth.json or configure providers.%s.apiKey in models.json", providerID, providerID)}
	}
	return "", "", &MissingAPIKeyError{Message: fmt.Sprintf("no %s credential: set %s, add an API-key entry to auth.json, or configure providers.%s.apiKey in models.json", spec.Name, spec.Environment, providerID)}
}

// CredentialState is one provider's resolved credential.
type CredentialState struct {
	// Usable reports that the provider has a credential this session can use.
	Usable bool
	// Source names the credential, such as "auth.json:anthropic" or
	// "ANTHROPIC_API_KEY".
	Source string
	// OAuth reports that the usable credential is an OAuth login rather than
	// an API key. The UI reads this flag directly instead of parsing Source.
	OAuth bool
	// Err explains why the provider has no usable credential.
	Err error
}

// CredentialRequest asks for one provider credential with the models.json key
// input that applies to its models.
type CredentialRequest struct {
	ProviderID string
	Key        APIKeyInput
}

// CredentialsBatch resolves each request from one consistent auth.json read
// and returns the states in request order. Reading once keeps the model lists
// fast and gives every provider the same view of the login file, so a busy
// lock cannot stall the list once per provider. Each request may use a
// different key input because one provider can declare models with different
// credential policies.
func CredentialsBatch(env map[string]string, authPath string, requests []CredentialRequest) []CredentialState {
	authPath = resolveAuthPath(env, authPath)
	entries, readErr := AuthEntries(authPath)
	if readErr == nil {
		defer ReleaseAuthEntries(entries)
	}
	out := make([]CredentialState, len(requests))
	for i, request := range requests {
		providerID := request.ProviderID
		spec, builtin := providerregistry.Lookup(providerID)
		if builtin {
			switch spec.Auth {
			case providerregistry.AuthCodexOAuth:
				if readErr != nil {
					out[i] = CredentialState{Err: readErr}
					continue
				}
				if err := codexCredentialCheck(entries); err != nil {
					out[i] = CredentialState{Err: err}
					continue
				}
				out[i] = CredentialState{Usable: true, Source: "auth.json:" + providerID}
			case providerregistry.AuthAPIKeyOrOAuth:
				if readErr != nil {
					out[i] = CredentialState{Err: readErr}
					continue
				}
				out[i] = openAICredentialStateAt(env, entries, authPath, request.Key, time.Now())
			case providerregistry.AuthAPIKey:
				if readErr != nil {
					out[i] = CredentialState{Err: readErr}
					continue
				}
				_, source, err := apiKeyFromEntriesInput(env, entries, authPath, providerID, request.Key)
				if err != nil {
					out[i] = CredentialState{Err: err}
					continue
				}
				out[i] = CredentialState{Usable: true, Source: source}
			}
			continue
		}
		// A provider that models.json declares uses an API key only.
		if readErr != nil {
			out[i] = CredentialState{Err: readErr}
			continue
		}
		_, source, err := apiKeyFromEntriesInput(env, entries, authPath, providerID, request.Key)
		if err != nil {
			out[i] = CredentialState{Err: err}
			continue
		}
		out[i] = CredentialState{Usable: true, Source: source}
	}
	return out
}

func credentialError(providerID string, err error) error {
	if providerID == "anthropic" {
		return fmt.Errorf("Anthropic credential: %w", err)
	}
	return fmt.Errorf("%s credential: %w", providerID, err)
}

func envLookup(env map[string]string) func(string) (string, bool) {
	return func(name string) (string, bool) {
		value, ok := env[name]
		return value, ok
	}
}

// readAuthShared reads auth.json through the shared lease so a reader never
// inspects a partial writer document (F01). Missing files still fall back to
// environment credentials upstream. The wait is bounded for startup use.
func readAuthShared(authPath string) ([]byte, error) {
	return readAuthSharedContext(context.Background(), authPath)
}

// readAuthSharedContext bounds the shared auth lease by the earlier of the
// caller's deadline and the five-second startup bound. Context cancellation
// bounds the lease wait only; an OS read already in progress may still return.
func readAuthSharedContext(ctx context.Context, authPath string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	unlock, _, err := acquireAuthLock(ctx, authPath+".lock", 0, 0, 0)
	if err != nil {
		return nil, err
	}
	defer unlock()
	return readAuthFile(authPath)
}

// readAuthFile reads a bounded regular file under the caller's auth lease.
// Read-only callers may follow an auth.json symlink.
func readAuthFile(authPath string) ([]byte, error) {
	fileInfo, err := os.Stat(authPath)
	if err != nil {
		return nil, err
	}
	if !fileInfo.Mode().IsRegular() {
		return nil, fmt.Errorf("auth.json must be a regular file")
	}
	file, err := os.Open(authPath)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !opened.Mode().IsRegular() || !os.SameFile(fileInfo, opened) {
		return nil, fmt.Errorf("auth.json changed while being read")
	}
	data, err := io.ReadAll(io.LimitReader(file, maxAuthJSONSize+1))
	if err != nil {
		secretmem.Wipe(data[:cap(data)])
		return nil, err
	}
	if len(data) > maxAuthJSONSize {
		secretmem.Wipe(data[:cap(data)])
		return nil, fmt.Errorf("auth.json exceeds size limit")
	}
	return data, nil
}
