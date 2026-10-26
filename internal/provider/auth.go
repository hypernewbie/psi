package provider

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Credential selection (Phase 6 Step 46).
//
// Precedence: ANTHROPIC_API_KEY env > auth.json API-key entries.
// OAuth entries are identified before any network request and fail with an
// actionable error (no silent fallback across billing paths).
// Subscription OAuth refresh lives in oauth.go and is required only when the
// operator selects subscription access (Step 47).

func SelectAPIKey(env map[string]string, authPath string) (key, source string, err error) {
	if v, ok := env["ANTHROPIC_API_KEY"]; ok && v != "" {
		return v, "ANTHROPIC_API_KEY", nil
	}
	if authPath == "" {
		home, _ := os.UserHomeDir()
		authPath = filepath.Join(home, ".pi", "agent", "auth.json")
	}
	data, readErr := os.ReadFile(authPath)
	if readErr != nil {
		return "", "", fmt.Errorf("no Anthropic credential: set ANTHROPIC_API_KEY (auth file missing: %s)", authPath)
	}
	// Minimal auth.json scan: look for anthropic api_key entries; detect oauth.
	text := string(data)
	if strings.Contains(text, `"oauth"`) && strings.Contains(text, "anthropic") {
		// Distinguish: if an api_key for anthropic exists, prefer it; else fail clearly.
		if !strings.Contains(text, "api_key") {
			return "", "", fmt.Errorf("auth.json contains an Anthropic OAuth entry: subscription refresh is required for this path (see docs); no silent API-key fallback")
		}
	}
	// Parse generic map and extract anthropic api key.
	var parsed map[string]any
	if err := jsonUnmarshal(data, &parsed); err != nil {
		return "", "", fmt.Errorf("auth.json malformed: %w", err)
	}
	for k, v := range parsed {
		if !strings.Contains(strings.ToLower(k), "anthropic") {
			continue
		}
		if m, ok := v.(map[string]any); ok {
			if key, ok := m["key"].(string); ok && key != "" {
				// Credential type check: oauth entries carry type oauth.
				if t, _ := m["type"].(string); t == "oauth" || t == "oauth_token" {
					return "", "", fmt.Errorf("auth.json Anthropic entry %q is OAuth: subscription path requires refresh; no silent fallback", k)
				}
				return key, "auth.json:" + k, nil
			}
			if key, ok := m["api_key"].(string); ok && key != "" {
				return key, "auth.json:" + k, nil
			}
			if key, ok := m["apiKey"].(string); ok && key != "" {
				return key, "auth.json:" + k, nil
			}
		}
		if s, ok := v.(string); ok && strings.HasPrefix(s, "sk-ant-") {
			return s, "auth.json:" + k, nil
		}
	}
	return "", "", fmt.Errorf("no Anthropic credential: set ANTHROPIC_API_KEY or add an API-key entry to %s", authPath)
}
