package app

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"psi/internal/config"
	"psi/internal/provider"
	"psi/internal/terminal"
)

func TestFirstLoginAppliesPerModelThinkingDefault(t *testing.T) {
	t.Setenv("DEEPSEEK_API_KEY", "")
	r := testRuntime(t)
	r.models = &config.ModelsFile{Providers: map[string]config.ProviderMeta{}}
	r.state = StateOffline
	r.settings.ModelThinkingLevels = map[string]string{"deepseek/deepseek-v4-pro": "high"}
	// An explicit startup level applies to the old model, not the login selection.
	r.ov.Thinking, r.ov.ThinkingExplicit = "off", true
	console := authCommandConsole(t)
	r.lifecycle = &terminalLifecycle{console: console}
	if err := r.loginCommand("deepseek", console); err != nil {
		t.Fatal(err)
	}
	console.HandleSecret(terminal.Input{Key: []byte("synthetic-login-key")})
	var output bytes.Buffer
	r.finishLogin(console, &output)
	if r.state != StateIdle || r.authErr != nil || r.ov.Provider != "deepseek" || r.ov.Model != "deepseek-v4-pro" {
		t.Fatal("login did not select a usable provider default")
	}
	if r.ov.Thinking != "high" || r.cfg.Thinking != "high" || r.ov.ThinkingExplicit {
		t.Fatalf("login thinking = %q, configured = %q, explicit = %t; want per-model high", r.ov.Thinking, r.cfg.Thinking, r.ov.ThinkingExplicit)
	}
}

func TestAuthCommandsRejectNonInteractiveAndInvalidRequests(t *testing.T) {
	r := testRuntime(t)
	r.state = StateIdle
	if err := r.loginCommand("deepseek", nil); err == nil || !strings.Contains(err.Error(), "requires an interactive terminal") {
		t.Fatalf("non-interactive login error=%v", err)
	}
	if err := r.logoutCommand("", nil, io.Discard); err == nil || !strings.Contains(err.Error(), "requires an interactive terminal") {
		t.Fatalf("non-interactive logout error=%v", err)
	}

	console := authCommandConsole(t)
	r.lifecycle = &terminalLifecycle{console: console}
	if err := r.loginCommand("deepseek extra", console); err == nil || !strings.Contains(err.Error(), "use /login [provider]") {
		t.Fatalf("login argument error=%v", err)
	}
	if err := r.loginCommand("not-a-provider", console); err == nil || !strings.Contains(err.Error(), "unknown provider") {
		t.Fatalf("unknown provider error=%v", err)
	}
	if err := r.logoutCommand("deepseek", console, io.Discard); err == nil || !strings.Contains(err.Error(), "use /logout") {
		t.Fatalf("logout argument error=%v", err)
	}
	if r.selector != nil || r.login != nil {
		t.Fatalf("invalid requests changed auth UI state: selector=%+v login=%+v", r.selector, r.login)
	}

	r.state = StateWaiting
	if err := r.loginCommand("", console); err == nil || !strings.Contains(err.Error(), "unavailable while a run or picker is active") {
		t.Fatalf("busy login error=%v", err)
	}
}

func TestBareLoginOpensProviderPicker(t *testing.T) {
	r := testRuntime(t)
	r.state = StateIdle
	models, err := config.LoadModels(filepath.Join(r.agentDir, "models.json"))
	if err != nil {
		t.Fatal(err)
	}
	r.models = models
	console := authCommandConsole(t)
	r.lifecycle = &terminalLifecycle{console: console}

	if err := r.loginCommand("", console); err != nil {
		t.Fatal(err)
	}
	if r.selector == nil || r.selector.AuthMode != AuthLogin {
		t.Fatalf("bare /login did not open login selector: %+v", r.selector)
	}
	var got []string
	for _, item := range r.selector.Items {
		got = append(got, item.ID)
	}
	want := []string{"deepseek", "google", "groq", "kimi-coding", "minimax", "mistral", "moonshotai", "opencode-go", "openrouter", "qwen-token-plan", "xai", "xiaomi", "xiaomi-token-plan-ams", "xiaomi-token-plan-cn", "xiaomi-token-plan-sgp", "zai"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("login providers=%v want %v", got, want)
	}
	defaults := map[string]string{
		"deepseek": "deepseek-v4-pro", "google": "gemini-3.1-pro-preview", "kimi-coding": "kimi-for-coding",
		"minimax": "MiniMax-M2.7", "opencode-go": "kimi-k3", "zai": "glm-5.3",
	}
	for id, modelID := range defaults {
		item := r.selector.Items[0]
		for _, candidate := range r.selector.Items {
			if candidate.ID == id {
				item = candidate
				break
			}
		}
		if !strings.Contains(item.FirstMessage, modelID) {
			t.Fatalf("provider row %s missing default model %q: %q", id, modelID, item.FirstMessage)
		}
	}
}

func TestLogoutPickerRemovesOnlyStoredBuiltInAPIKey(t *testing.T) {
	r := testRuntime(t)
	t.Setenv("ANTHROPIC_API_KEY", "fallback-key")
	r.state = StateIdle
	models, err := config.LoadModels(filepath.Join(r.agentDir, "models.json"))
	if err != nil {
		t.Fatal(err)
	}
	r.models = models
	authPath := filepath.Join(r.agentDir, "auth.json")
	writeAppFile(t, authPath, `{"anthropic":{"type":"api_key","key":"anthropic-key"},"deepseek":{"type":"api_key","key":"stored-key"},"openai-codex":{"type":"oauth","access":"oauth-token"},"custom":{"type":"api_key","key":"custom-key"}}`)
	if err := r.configure(config.StartupOverrides{Provider: "anthropic", Model: "claude-sonnet-4-5"}); err != nil {
		t.Fatal(err)
	}
	r.state = StateIdle
	console := authCommandConsole(t)
	r.lifecycle = &terminalLifecycle{console: console}
	var out bytes.Buffer
	if err := r.logoutCommand("", console, &out); err != nil {
		t.Fatal(err)
	}
	if r.selector == nil || r.selector.AuthMode != AuthLogout {
		t.Fatalf("/logout did not open stored-key selector: %+v", r.selector)
	}
	var got []string
	for _, item := range r.selector.Items {
		got = append(got, item.ID)
	}
	if !reflect.DeepEqual(got, []string{"anthropic", "deepseek"}) {
		t.Fatalf("logout picker included non-stored or non-built-in credentials: %v", got)
	}
	r.selector.SetFilter("anthropic")
	selected := r.selector.SelectedItem()
	if selected == nil || selected.ID != "anthropic" {
		t.Fatalf("search did not select Anthropic: %v", selected)
	}
	r.finishLogout(selected.ID, console, &out)
	if strings.Contains(out.String(), "fallback-key") || !strings.Contains(out.String(), "Removed stored API key for Anthropic") {
		t.Fatalf("logout status is incorrect or disclosed a credential: %q", out.String())
	}
	data, err := os.ReadFile(authPath)
	if err != nil {
		t.Fatal(err)
	}
	var entries map[string]json.RawMessage
	if err := json.Unmarshal(data, &entries); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"anthropic", "deepseek", "openai-codex", "custom"} {
		_, exists := entries[id]
		wantExists := id != "anthropic"
		if exists != wantExists {
			t.Errorf("auth entry %q exists=%v want %v", id, exists, wantExists)
		}
	}
	key, source, err := provider.SelectProviderAPIKey(map[string]string{"ANTHROPIC_API_KEY": "fallback-key"}, authPath, "anthropic", "")
	if err != nil || key != "fallback-key" || source != "ANTHROPIC_API_KEY" {
		t.Fatalf("active provider did not reload from environment fallback: key=%q source=%q err=%v", key, source, err)
	}
	if r.authErr != nil || r.state != StateIdle || r.prov == nil {
		t.Fatalf("fallback did not keep the active provider online: authErr=%v state=%s provider=%T", r.authErr, r.state, r.prov)
	}
}

func TestLogoutActiveProviderWithoutFallbackGoesOffline(t *testing.T) {
	t.Setenv("DEEPSEEK_API_KEY", "")
	r := testRuntime(t)
	r.state = StateIdle
	models, err := config.LoadModels(filepath.Join(r.agentDir, "models.json"))
	if err != nil {
		t.Fatal(err)
	}
	r.models = models
	authPath := filepath.Join(r.agentDir, "auth.json")
	writeAppFile(t, authPath, `{"deepseek":{"type":"api_key","key":"stored-key"}}`)
	if err := r.configure(config.StartupOverrides{Provider: "deepseek", Model: "deepseek-v4-pro"}); err != nil {
		t.Fatal(err)
	}
	r.state = StateIdle
	console := authCommandConsole(t)
	r.lifecycle = &terminalLifecycle{console: console}
	var out bytes.Buffer
	r.finishLogout("deepseek", console, &out)
	if r.authErr == nil || r.state != StateOffline || r.prov != nil {
		t.Fatalf("logout without a fallback did not take the runtime offline: authErr=%v state=%s provider=%T", r.authErr, r.state, r.prov)
	}
	if !strings.Contains(out.String(), "Removed stored API key for DeepSeek") {
		t.Fatalf("logout status missing: %q", out.String())
	}
}

func TestLogoutWithNoStoredKeysLeavesAuthFileAbsent(t *testing.T) {
	r := testRuntime(t)
	r.state = StateIdle
	console := authCommandConsole(t)
	r.lifecycle = &terminalLifecycle{console: console}
	var out bytes.Buffer
	if err := r.logoutCommand("", console, &out); err != nil {
		t.Fatal(err)
	}
	if r.selector != nil || !strings.Contains(out.String(), "No stored API keys to remove") {
		t.Fatalf("empty logout result selector=%+v output=%q", r.selector, out.String())
	}
	if _, err := os.Stat(filepath.Join(r.agentDir, "auth.json")); !os.IsNotExist(err) {
		t.Fatalf("empty logout created auth.json: %v", err)
	}
}

func authCommandConsole(t *testing.T) *terminal.Console {
	t.Helper()
	input, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = input.Close() })
	console := terminal.NewConsole(input, io.Discard)
	t.Cleanup(console.Close)
	return console
}
