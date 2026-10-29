package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"psi/internal/config"
	"psi/internal/providerregistry"
)

// SelectAPIKey preserves the Anthropic-compatible caller and its no-fallback
// behavior. New API-key providers use SelectProviderAPIKey.
func SelectAPIKey(env map[string]string, authPath string) (key, source string, err error) {
	return SelectProviderAPIKey(env, authPath, "anthropic", "")
}

// SelectProviderAPIKey matches Pi's supported precedence: provider-specific
// auth.json key, models.json key, then the provider's exact environment
// variable. It never executes credential commands and never crosses billing
// sources after an unsupported stored credential is found.
func SelectProviderAPIKey(env map[string]string, authPath, providerID, configuredKey string) (key, source string, err error) {
	spec, ok := providerregistry.Lookup(providerID)
	if !ok || spec.Auth != providerregistry.AuthAPIKey {
		return "", "", fmt.Errorf("provider %q does not use API-key authentication", providerID)
	}
	if authPath == "" {
		agentDir := env["PI_CODING_AGENT_DIR"]
		if agentDir == "" {
			home, _ := os.UserHomeDir()
			agentDir = filepath.Join(home, ".pi", "agent")
		}
		authPath = filepath.Join(agentDir, "auth.json")
	}
	data, readErr := readAuthShared(authPath)
	if readErr != nil && !os.IsNotExist(readErr) {
		if _, statErr := os.Lstat(authPath); os.IsNotExist(statErr) {
			readErr = os.ErrNotExist
		} else {
			return "", "", fmt.Errorf("cannot read auth.json: %w", readErr)
		}
	}
	if readErr == nil {
		var entries map[string]json.RawMessage
		if err := json.Unmarshal(data, &entries); err != nil {
			return "", "", fmt.Errorf("auth.json is malformed")
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
	if key := env[spec.Environment]; key != "" {
		return key, spec.Environment, nil
	}
	if providerID == "anthropic" {
		return "", "", fmt.Errorf("no Anthropic credential: set ANTHROPIC_API_KEY or add an API-key entry to %s", authPath)
	}
	return "", "", fmt.Errorf("no %s credential: set %s, add an API-key entry to auth.json, or configure providers.%s.apiKey in models.json", spec.Name, spec.Environment, providerID)
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
