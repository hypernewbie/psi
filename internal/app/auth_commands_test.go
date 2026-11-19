package app

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"psi/internal/config"
	"psi/internal/provider"
	"psi/internal/session"
	"psi/internal/terminal"
)

func TestSaveLoginKeyWipesCallerBuffer(t *testing.T) {
	authPath := filepath.Join(t.TempDir(), "auth.json")
	backing := make([]byte, 0, 64)
	key := append(backing, "!sk-wipe-$-key"...)
	if err := saveLoginKey(context.Background(), authPath, "deepseek", key); err != nil {
		t.Fatal(err)
	}
	for i, b := range key[:cap(key)] {
		if b != 0 {
			t.Fatalf("key byte %d survived successful save", i)
		}
	}

	failureBacking := make([]byte, 0, 64)
	failureKey := append(failureBacking, "!sk-wipe-$-key"...)
	if err := saveLoginKey(context.Background(), authPath, "missing-provider", failureKey); err == nil {
		t.Fatal("unsupported provider was accepted")
	}
	for i, b := range failureKey[:cap(failureKey)] {
		if b != 0 {
			t.Fatalf("key byte %d survived failed save", i)
		}
	}
}

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
	want := []string{"deepseek", "google", "groq", "kimi-coding", "minimax", "mistral", "moonshotai", "openai", "openai-codex", "opencode-go", "openrouter", "qwen-token-plan", "xai", "xiaomi", "xiaomi-token-plan-ams", "xiaomi-token-plan-cn", "xiaomi-token-plan-sgp", "zai"}
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

func TestActiveAPIKeyChangesReachProviderRequests(t *testing.T) {
	for _, configuredKey := range []string{"", "configured-fallback"} {
		name := "environment fallback"
		if configuredKey != "" {
			name = "configured fallback precedes environment"
		}
		t.Run(name, func(t *testing.T) {
			const environmentKey = "environment-fallback"
			const replacementKey = "!replacement-$-key"
			t.Setenv("DEEPSEEK_API_KEY", environmentKey)
			headers := make(chan string, 3)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				headers <- req.Header.Get("Authorization")
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
			}))
			defer server.Close()

			r := testRuntime(t)
			r.models = &config.ModelsFile{Providers: map[string]config.ProviderMeta{"deepseek": {
				APIKey: configuredKey,
				Models: []config.ModelMeta{{ID: "synthetic-deepseek", API: "openai-completions", BaseURL: server.URL, MaxTokens: 1234, Input: []string{"text"}}},
			}}}
			writeAppFile(t, filepath.Join(r.agentDir, "auth.json"), `{"deepseek":{"type":"api_key","key":"stored-key"}}`)
			if err := r.configure(config.StartupOverrides{Provider: "deepseek", Model: "synthetic-deepseek", Thinking: "off"}); err != nil {
				t.Fatal(err)
			}
			r.publishLive()
			r.state = StateIdle
			console := authCommandConsole(t)
			r.lifecycle = &terminalLifecycle{console: console}
			send := func(stage, key string) {
				t.Helper()
				if r.authErr != nil || r.state != StateIdle || r.prov == nil {
					t.Fatalf("%s: active provider is unavailable", stage)
				}
				live := r.liveSettings()
				if live.AuthErr != nil || live.Provider == nil {
					t.Fatalf("%s: live provider is unavailable", stage)
				}
				response, err := live.Provider.Send(t.Context(), provider.Request{Model: live.Model, Thinking: live.Thinking, Messages: []provider.Message{{Role: "user", Content: []provider.Block{{Type: "text", Text: "synthetic"}}}}}, nil)
				if err != nil || response == nil || response.Text != "ok" {
					t.Fatalf("%s: provider request failed: %v", stage, err)
				}
				select {
				case header := <-headers:
					if header != "Bearer "+key {
						t.Fatalf("%s: provider request used the wrong credential", stage)
					}
				default:
					t.Fatalf("%s: no provider request reached the server", stage)
				}
			}
			send("before login", "stored-key")
			selection := r.ov
			if err := r.loginCommand("deepseek", console); err != nil {
				t.Fatal(err)
			}
			if submitted, cancelled, err := console.HandleSecret(terminal.Input{Key: []byte(replacementKey)}); err != nil || submitted || cancelled {
				t.Fatal("could not enter replacement key")
			}
			if submitted, cancelled, err := console.HandleSecret(terminal.Input{Key: []byte{'\r'}}); err != nil || !submitted || cancelled {
				t.Fatal("could not submit replacement key")
			}
			r.finishLogin(console, io.Discard)
			if r.ov != selection {
				t.Fatal("replacing the active key changed the model selection")
			}
			send("after login", replacementKey)
			r.finishLogout("deepseek", console, io.Discard)
			fallback := configuredKey
			if fallback == "" {
				fallback = environmentKey
			}
			send("after logout", fallback)
		})
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
	r.publishLive()
	r.state = StateIdle
	console := authCommandConsole(t)
	r.lifecycle = &terminalLifecycle{console: console}
	var out bytes.Buffer
	r.finishLogout("deepseek", console, &out)
	if r.authErr == nil || r.state != StateOffline || r.prov != nil {
		t.Fatalf("logout without a fallback did not take the runtime offline: authErr=%v state=%s provider=%T", r.authErr, r.state, r.prov)
	}
	if live := r.liveSettings(); live.AuthErr == nil || live.Provider != nil {
		t.Fatal("logout without a fallback left the live provider online")
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

func TestFirstLoginRetainsSavedKeyWhenLaterPersistenceFails(t *testing.T) {
	for _, sessionFailure := range []bool{true, false} {
		name := "settings write"
		wantNotice := "but could not save defaults:"
		if sessionFailure {
			name = "session write"
			wantNotice = "but could not select the default model:"
		}
		t.Run(name, func(t *testing.T) {
			t.Setenv("DEEPSEEK_API_KEY", "")
			r := testRuntime(t)
			r.models = &config.ModelsFile{Providers: map[string]config.ProviderMeta{}}
			r.state = StateOffline
			if _, err := r.store.AppendMessage(session.AgentMessage{Role: "user", User: &session.UserMessage{Role: "user", Content: "synthetic prior input", Timestamp: 1}}); err != nil {
				t.Fatal(err)
			}
			oldSelection, oldProvider := r.ov, r.prov
			console := authCommandConsole(t)
			r.lifecycle = &terminalLifecycle{console: console}
			if err := r.loginCommand("deepseek", console); err != nil {
				t.Fatal(err)
			}
			const key = "!saved-$-key"
			console.HandleSecret(terminal.Input{Key: []byte(key)})
			settingsPath := filepath.Join(r.agentDir, "settings.json")
			if sessionFailure {
				r.store.FailWritesForTest(errPoison)
			} else {
				writeAppFile(t, settingsPath, `{"defaultProvider":`)
			}
			var output bytes.Buffer
			r.finishLogin(console, &output)
			resolved, _, err := provider.SelectProviderAPIKey(envMap(), filepath.Join(r.agentDir, "auth.json"), "deepseek", "")
			if err != nil || resolved != key {
				t.Fatal("later persistence failure discarded the saved key")
			}
			if len(console.EndSecret()) != 0 || r.login != nil || console.Editor.Text() != "" {
				t.Fatal("later persistence failure retained secret input")
			}
			if !strings.Contains(output.String(), wantNotice) || strings.Contains(output.String(), "Credentials saved to") {
				t.Fatal("later persistence failure did not report the partial result")
			}
			if sessionFailure {
				if r.state != StateStorageBlocked || r.store.WriteError() == nil || r.authErr == nil || r.ov != oldSelection || r.prov != oldProvider {
					t.Fatal("failed session write installed the candidate or cleared storage failure")
				}
				if _, err := os.Stat(settingsPath); !os.IsNotExist(err) {
					t.Fatal("failed session write attempted to persist defaults")
				}
				return
			}
			if r.state != StateIdle || r.authErr != nil || r.prov == nil || r.ov.Provider != "deepseek" || r.ov.Model != "deepseek-v4-pro" {
				t.Fatal("failed defaults write discarded the usable current selection")
			}
			selection, err := r.store.ModelState()
			if err != nil || selection.Provider != r.ov.Provider || selection.ModelID != r.ov.Model {
				t.Fatal("failed defaults write lost the persisted session selection")
			}
			data, err := os.ReadFile(settingsPath)
			if err != nil || string(data) != `{"defaultProvider":` {
				t.Fatal("failed defaults write changed the malformed settings")
			}
		})
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
