package provider

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestProviderAPIKeyPrecedenceAndProviderIsolation(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "auth.json")
	env := map[string]string{
		"MINIMAX_API_KEY":  "env-minimax",
		"OPENCODE_API_KEY": "env-opencode",
		"DEEPSEEK_API_KEY": "env-deepseek",
		"ZAI_API_KEY":      "env-zai",
		"GEMINI_API_KEY":   "env-google",
		"CONFIG_KEY":       "config-minimax",
	}
	if key, source, err := SelectProviderAPIKey(env, path, "minimax", "$CONFIG_KEY"); err != nil || key != "config-minimax" || source != "models.json:minimax" {
		t.Fatalf("models.json precedence: %q %q %v", key, source, err)
	}
	stored := `{"minimax":{"type":"api_key","key":"stored-minimax"},"opencode-go":{"type":"api_key","key":"stored-opencode"},"deepseek":{"type":"api_key","key":"stored-deepseek"},"zai":{"type":"api_key","key":"stored-zai"},"google":{"type":"api_key","key":"stored-google"}}`
	if err := os.WriteFile(path, []byte(stored), 0600); err != nil {
		t.Fatal(err)
	}
	for _, providerID := range []string{"minimax", "opencode-go", "deepseek", "zai", "google"} {
		key, source, err := SelectProviderAPIKey(env, path, providerID, "$CONFIG_KEY")
		want := map[string]string{"minimax": "stored-minimax", "opencode-go": "stored-opencode", "deepseek": "stored-deepseek", "zai": "stored-zai", "google": "stored-google"}[providerID]
		if err != nil || key != want || source != "auth.json:"+providerID {
			t.Fatalf("stored key for %s: %q %q %v", providerID, key, source, err)
		}
	}
	if err := os.WriteFile(path, []byte(`{}`), 0600); err != nil {
		t.Fatal(err)
	}
	for _, providerID := range []string{"minimax", "opencode-go", "deepseek", "zai", "google"} {
		key, source, err := SelectProviderAPIKey(env, path, providerID, "$CONFIG_KEY")
		if err != nil || key != "config-minimax" || source != "models.json:"+providerID {
			t.Fatalf("configured key for %s: %q %q %v", providerID, key, source, err)
		}
	}
	for _, tc := range []struct{ providerID, environment, key string }{
		{"minimax", "MINIMAX_API_KEY", "env-minimax"},
		{"opencode-go", "OPENCODE_API_KEY", "env-opencode"},
		{"deepseek", "DEEPSEEK_API_KEY", "env-deepseek"},
		{"zai", "ZAI_API_KEY", "env-zai"},
		{"google", "GEMINI_API_KEY", "env-google"},
	} {
		key, source, err := SelectProviderAPIKey(env, path, tc.providerID, "")
		if err != nil || key != tc.key || source != tc.environment {
			t.Fatalf("environment for %s: %q %q %v", tc.providerID, key, source, err)
		}
	}
	if _, _, err := SelectProviderAPIKey(env, path, "openai-codex", ""); err == nil || !strings.Contains(err.Error(), "API-key") {
		t.Fatalf("Codex accepted API-key resolution: %v", err)
	}
}

func TestProviderUnsupportedStoredCredentialsNeverFallback(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "auth.json")
	env := map[string]string{"GEMINI_API_KEY": "fallback-secret"}
	for _, raw := range []string{
		`{"google":{"type":"oauth","access":"token"}}`,
		`{"google":{"type":"oauth_token","access":"token"}}`,
		`{"google":{"type":"unsupported","key":"ignored"}}`,
		`{"google":"not-an-object"}`,
		`{"google":{"type":"api_key","key":""}}`,
	} {
		if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
			t.Fatal(err)
		}
		if key, _, err := SelectProviderAPIKey(env, path, "google", "configured-secret"); err == nil || key != "" {
			t.Fatalf("stored credential %s fell back to %q (%v)", raw, key, err)
		}
	}
	if err := os.WriteFile(path, []byte(`{}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := SelectProviderAPIKey(env, path, "google", "!print secret"); err == nil || !strings.Contains(err.Error(), "commands are not supported") {
		t.Fatalf("credential command was accepted: %v", err)
	}
	if err := os.WriteFile(path, []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := SelectProviderAPIKey(env, path, "google", "configured-secret"); err == nil || !strings.Contains(err.Error(), "malformed") {
		t.Fatalf("malformed auth.json fell back: %v", err)
	}
}
