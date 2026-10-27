package provider

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"psi/internal/config"
)

// SelectAPIKey matches Pi's supported precedence: stored Anthropic credential,
// then the environment. Unsupported subscription credentials never silently
// fall back to a separately billed API key. No credential commands are run.
func SelectAPIKey(env map[string]string, authPath string) (key, source string, err error) {
	if authPath == "" {
		agentDir := env["PI_CODING_AGENT_DIR"]
		if agentDir == "" {
			home, _ := os.UserHomeDir()
			agentDir = filepath.Join(home, ".pi", "agent")
		}
		authPath = filepath.Join(agentDir, "auth.json")
	}
	data, readErr := os.ReadFile(authPath)
	if readErr != nil && !os.IsNotExist(readErr) {
		return "", "", fmt.Errorf("cannot read auth.json: %w", readErr)
	}
	if readErr == nil {
		var entries map[string]json.RawMessage
		if err := json.Unmarshal(data, &entries); err != nil {
			return "", "", fmt.Errorf("auth.json is malformed")
		}
		if raw, ok := entries["anthropic"]; ok {
			var entry struct {
				Type string `json:"type"`
				Key  string `json:"key"`
			}
			if err := json.Unmarshal(raw, &entry); err != nil {
				return "", "", fmt.Errorf("auth.json Anthropic credential must be an API-key object")
			}
			if entry.Type == "oauth" || entry.Type == "oauth_token" {
				return "", "", fmt.Errorf("Anthropic OAuth subscription credentials are unsupported; no API-key fallback (select API-key access explicitly in auth.json)")
			}
			if entry.Type != "" && entry.Type != "api_key" {
				return "", "", fmt.Errorf("unsupported Anthropic credential type")
			}
			if entry.Key == "" {
				return "", "", fmt.Errorf("auth.json Anthropic API key is empty")
			}
			key, err := config.ResolveCredentialWithEnv(entry.Key, func(name string) (string, bool) { v, ok := env[name]; return v, ok })
			if err != nil {
				return "", "", fmt.Errorf("Anthropic credential: %w", err)
			}
			return key, "auth.json:anthropic", nil
		}
		if _, ok := entries["anthropic-oauth"]; ok {
			return "", "", fmt.Errorf("Anthropic OAuth subscription credentials are unsupported; no silent API-key fallback")
		}
	}
	if key := env["ANTHROPIC_API_KEY"]; key != "" {
		return key, "ANTHROPIC_API_KEY", nil
	}
	return "", "", fmt.Errorf("no Anthropic credential: set ANTHROPIC_API_KEY or add an API-key entry to %s", authPath)
}
