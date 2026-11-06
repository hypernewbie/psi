package provider

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
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

// Credentials resolves every provider from one auth.json read. The model
// lists call it once per list, so a busy login file must not cost one lock
// wait per provider, and the resolution must keep the per-provider precedence
// and the no-fallback rules.
func TestCredentialsResolveEveryProviderFromOneSnapshot(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "auth.json")
	env := map[string]string{
		"ANTHROPIC_API_KEY": "env-anthropic",
		"ZAI_API_KEY":       "env-zai",
		"GEMINI_API_KEY":    "env-google",
	}
	configured := map[string]string{"minimax": "configured-minimax"}

	// No auth.json: the environment and models.json keys still apply.
	ResetAuthReads()
	states := Credentials(env, path, configured)
	if reads := AuthReads(); reads != 1 {
		t.Fatalf("credential resolution read auth.json %d times, want one read for every provider", reads)
	}
	if state := states["anthropic"]; !state.Usable || state.Source != "ANTHROPIC_API_KEY" {
		t.Fatalf("anthropic without auth.json = %+v", state)
	}
	if state := states["minimax"]; !state.Usable || state.Source != "models.json:minimax" {
		t.Fatalf("minimax without auth.json = %+v", state)
	}
	if state := states["openai-codex"]; state.Usable || state.Err == nil {
		t.Fatalf("codex without a login = %+v", state)
	}

	// A stored key wins over the environment, and an unsupported stored
	// credential never falls back to the environment.
	stored := `{"anthropic":{"type":"api_key","key":"stored-anthropic"},"google":{"type":"oauth","access":"token"},"openai-codex":{"type":"oauth","access":"dummy-access","refresh":"dummy-refresh","accountId":"synthetic-account","expires":` + fmt.Sprint(time.Now().Add(time.Hour).UnixMilli()) + `}}`
	if err := os.WriteFile(path, []byte(stored), 0600); err != nil {
		t.Fatal(err)
	}
	states = Credentials(env, path, configured)
	if reads := AuthReads(); reads != 2 {
		t.Fatalf("credential resolution read auth.json %d times, want one read per call", reads)
	}
	if state := states["anthropic"]; !state.Usable || state.Source != "auth.json:anthropic" {
		t.Fatalf("anthropic with a stored key = %+v", state)
	}
	if state := states["google"]; state.Usable || state.Err == nil || !strings.Contains(state.Err.Error(), "OAuth") {
		t.Fatalf("google fell back from an unsupported stored credential = %+v", state)
	}
	if state := states["openai-codex"]; !state.Usable || state.Source != "auth.json:openai-codex" {
		t.Fatalf("codex with a stored login = %+v", state)
	}

	// A malformed file is an error for every provider, not a partial view.
	if err := os.WriteFile(path, []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	states = Credentials(env, path, configured)
	for _, providerID := range []string{"anthropic", "minimax", "zai", "openai-codex"} {
		if state := states[providerID]; state.Usable || state.Err == nil || !strings.Contains(state.Err.Error(), "malformed") {
			t.Fatalf("%s with a malformed auth.json = %+v", providerID, state)
		}
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
