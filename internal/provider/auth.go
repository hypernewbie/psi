package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync/atomic"
	"time"

	"psi/internal/config"
	"psi/internal/providerregistry"
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
	spec, builtin := providerregistry.Lookup(providerID)
	if builtin && spec.Auth != providerregistry.AuthAPIKey {
		return "", "", fmt.Errorf("provider %q does not use API-key authentication", providerID)
	}
	authPath = resolveAuthPath(env, authPath)
	entries, err := AuthEntries(authPath)
	if err != nil {
		return "", "", err
	}
	return apiKeyFromEntries(env, entries, authPath, providerID, configuredKey)
}

// resolveAuthPath uses the agent directory when the caller has no path.
func resolveAuthPath(env map[string]string, authPath string) string {
	if authPath != "" {
		return authPath
	}
	agentDir := env["PI_CODING_AGENT_DIR"]
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
func AuthEntries(authPath string) (map[string]json.RawMessage, error) {
	authReads.Add(1)
	data, readErr := readAuthShared(authPath)
	if readErr != nil {
		if os.IsNotExist(readErr) {
			return nil, nil
		}
		if _, statErr := os.Lstat(authPath); os.IsNotExist(statErr) {
			return nil, nil
		}
		return nil, fmt.Errorf("cannot read auth.json: %w", readErr)
	}
	var entries map[string]json.RawMessage
	if err := json.Unmarshal(data, &entries); err != nil {
		return nil, fmt.Errorf("auth.json is malformed")
	}
	return entries, nil
}

// apiKeyFromEntries resolves one provider's API key from one auth.json
// snapshot. It keeps the precedence and the no-fallback rules of
// SelectProviderAPIKey.
func apiKeyFromEntries(env map[string]string, entries map[string]json.RawMessage, authPath, providerID, configuredKey string) (key, source string, err error) {
	spec, builtin := providerregistry.Lookup(providerID)
	if builtin && spec.Auth != providerregistry.AuthAPIKey {
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
		return key, "auth.json:" + providerID, nil
	}
	if providerID == "anthropic" {
		if _, present := entries["anthropic-oauth"]; present {
			return "", "", fmt.Errorf("Anthropic OAuth subscription credentials are unsupported; no silent API-key fallback")
		}
	}
	if configuredKey != "" {
		key, err := config.ResolveCredentialWithEnv(configuredKey, envLookup(env))
		if err != nil {
			return "", "", credentialError(providerID, err)
		}
		if key == "" {
			return "", "", fmt.Errorf("models.json %s API key is empty", providerID)
		}
		return key, "models.json:" + providerID, nil
	}
	if builtin {
		if key := env[spec.Environment]; key != "" {
			return key, spec.Environment, nil
		}
	}
	if providerID == "anthropic" {
		return "", "", fmt.Errorf("no Anthropic credential: set ANTHROPIC_API_KEY or add an API-key entry to %s", authPath)
	}
	if !builtin {
		return "", "", fmt.Errorf("no %s credential: add an API-key entry to auth.json or configure providers.%s.apiKey in models.json", providerID, providerID)
	}
	return "", "", fmt.Errorf("no %s credential: set %s, add an API-key entry to auth.json, or configure providers.%s.apiKey in models.json", spec.Name, spec.Environment, providerID)
}

// CredentialState is one provider's resolved credential.
type CredentialState struct {
	// Usable reports that the provider has a credential this session can use.
	Usable bool
	// Source names the credential, such as "auth.json:anthropic" or
	// "ANTHROPIC_API_KEY".
	Source string
	// Err explains why the provider has no usable credential.
	Err error
}

// Credentials resolves every provider's credential from one consistent
// auth.json read. Reading once keeps the model lists fast and gives every
// provider the same view of the login file, so a busy lock cannot stall the
// list once per provider. The provider list is the effective set: the
// built-in providers plus the providers models.json declares.
func Credentials(env map[string]string, authPath string, configured map[string]string, providerIDs []string) map[string]CredentialState {
	authPath = resolveAuthPath(env, authPath)
	entries, readErr := AuthEntries(authPath)
	out := map[string]CredentialState{}
	for _, providerID := range providerIDs {
		spec, builtin := providerregistry.Lookup(providerID)
		if builtin {
			switch spec.Auth {
			case providerregistry.AuthCodexOAuth:
				if readErr != nil {
					out[providerID] = CredentialState{Err: readErr}
					continue
				}
				if err := codexCredentialCheck(entries); err != nil {
					out[providerID] = CredentialState{Err: err}
					continue
				}
				out[providerID] = CredentialState{Usable: true, Source: "auth.json:" + providerID}
			case providerregistry.AuthAPIKey:
				if readErr != nil {
					out[providerID] = CredentialState{Err: readErr}
					continue
				}
				_, source, err := apiKeyFromEntries(env, entries, authPath, providerID, configured[providerID])
				if err != nil {
					out[providerID] = CredentialState{Err: err}
					continue
				}
				out[providerID] = CredentialState{Usable: true, Source: source}
			}
			continue
		}
		// A provider that models.json declares uses an API key only.
		if readErr != nil {
			out[providerID] = CredentialState{Err: readErr}
			continue
		}
		_, source, err := apiKeyFromEntries(env, entries, authPath, providerID, configured[providerID])
		if err != nil {
			out[providerID] = CredentialState{Err: err}
			continue
		}
		out[providerID] = CredentialState{Usable: true, Source: source}
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
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	unlock, _, err := acquireAuthLock(ctx, authPath+".lock", 0, 0)
	if err != nil {
		return nil, err
	}
	defer unlock()
	return os.ReadFile(authPath)
}
