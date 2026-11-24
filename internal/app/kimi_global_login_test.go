package app

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hypernewbie/psi/internal/config"
	"github.com/hypernewbie/psi/internal/provider"
	"github.com/hypernewbie/psi/internal/providerregistry"
)

// TestKimiGlobalTrialREPL proves the synthetic end-to-end trial on the real
// production URLs: device start, one pending poll, a successful poll, a
// streamed Completions reply with the stored bearer, then local logout. The
// transport is injected; no live host is contacted.
func TestKimiGlobalTrialREPL(t *testing.T) {
	clearProviderAPIKeys(t)
	r := testRuntime(t)
	r.models = openAILoginTestModels(t)
	r.settings.MaxRetries = 0
	r.version = "test"
	r.kimiWait = func(context.Context, time.Duration) error { return nil }
	authPath := filepath.Join(r.agentDir, "auth.json")
	writeAppFile(t, authPath, `{"kimi-coding":{"type":"api_key","key":"china-key"}}`)

	var starts, polls, inferences, browserCalls atomic.Int32
	var requestMu sync.Mutex
	var grants, tokenRequests, completionRequests []string
	record := func(target string, values ...string) {
		requestMu.Lock()
		defer requestMu.Unlock()
		switch target {
		case "grant":
			grants = append(grants, values...)
		case "token":
			tokenRequests = append(tokenRequests, values...)
		case "completion":
			completionRequests = append(completionRequests, values...)
		}
	}
	const verificationLink = "https://www.kimi.ai/code/authorize_device?user_code=KIMI-CODE"
	browserTargets := make(chan string, 1)
	browserOpened := make(chan struct{})
	r.kimiBrowser = func(_ context.Context, target string) error {
		browserCalls.Add(1)
		browserTargets <- target
		close(browserOpened)
		return nil
	}
	r.kimiTransport = roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		switch req.URL.String() {
		case "https://auth.kimi.ai/api/oauth/device_authorization":
			starts.Add(1)
			if req.Header.Get("User-Agent") != "psi/test" {
				t.Errorf("grant User-Agent = %q", req.Header.Get("User-Agent"))
			}
			form := readKimiForm(t, req)
			record("grant", form.Get("client_id"))
			return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"device_code":"kimi-device-secret","user_code":"KIMI-CODE","verification_uri":"https://www.kimi.ai/code/authorize_device","verification_uri_complete":"https://www.kimi.ai/code/authorize_device?user_code=KIMI-CODE","interval":1,"expires_in":900}`))}, nil
		case "https://auth.kimi.ai/api/oauth/token":
			n := polls.Add(1)
			if req.Header.Get("User-Agent") != "psi/test" {
				t.Errorf("poll User-Agent = %q", req.Header.Get("User-Agent"))
			}
			form := readKimiForm(t, req)
			record("token", form.Get("client_id"), form.Get("device_code"), form.Get("grant_type"))
			if n == 1 {
				return &http.Response{StatusCode: http.StatusBadRequest, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"error":"authorization_pending"}`))}, nil
			}
			if n != 2 {
				t.Errorf("unexpected poll %d", n)
			}
			// The server can issue a token only after the browser handoff.
			select {
			case <-browserOpened:
			case <-req.Context().Done():
				return nil, req.Context().Err()
			}
			return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"access_token":"kimi-access","refresh_token":"kimi-refresh","expires_in":3600,"token_type":"Bearer"}`))}, nil
		case "https://api.kimi.ai/coding/v1/chat/completions":
			inferences.Add(1)
			if req.Header.Get("Authorization") != "Bearer kimi-access" {
				t.Errorf("Authorization = %q", req.Header.Get("Authorization"))
			}
			if req.Header.Get("User-Agent") != "psi/test" {
				t.Errorf("inference User-Agent = %q", req.Header.Get("User-Agent"))
			}
			body, _ := io.ReadAll(req.Body)
			record("completion", string(body), req.Header.Get("Authorization"), req.Header.Get("User-Agent"))
			return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("data: {\"id\":\"chatcmpl-kimi\",\"model\":\"kimi-for-coding\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"kimi-trial-reply\"}}]}\n\ndata: {\"id\":\"chatcmpl-kimi\",\"model\":\"kimi-for-coding\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n"))}, nil
		default:
			t.Errorf("unexpected Kimi URL %s", req.URL)
			return nil, fmt.Errorf("unexpected Kimi URL")
		}
	})

	if err := r.configure(config.StartupOverrides{Provider: providerregistry.KimiGlobalProviderID, Thinking: "off", ThinkingExplicit: true}); err != nil {
		t.Fatal(err)
	}
	if r.authErr == nil {
		t.Fatal("Global Kimi was usable before login")
	}
	if r.ov.Provider != providerregistry.KimiGlobalProviderID || r.ov.Model != "kimi-for-coding" {
		t.Fatalf("startup selection = %s/%s", r.ov.Provider, r.ov.Model)
	}
	settled := make(chan struct{}, 1)
	r.testRunSettled = func() {
		select {
		case settled <- struct{}{}:
		default:
		}
	}
	_, out, inputWrite, result := startLoginTestREPL(t, r)
	waitAppOutput(t, out, "kimi-for-coding")
	if _, err := inputWrite.Write([]byte("/login kimi-coding-global\r")); err != nil {
		t.Fatal(err)
	}
	waitAppOutput(t, out, "KIMI-CODE")
	waitAppOutput(t, out, verificationLink)
	if browserCalls.Load() != 0 {
		t.Fatal("device sign-in launched a browser before Enter")
	}
	if !strings.Contains(out.String(), "Kimi Code (Global)") {
		t.Fatalf("panel heading missing: %q", out.String())
	}
	if _, err := inputWrite.Write([]byte("\r")); err != nil {
		t.Fatal(err)
	}
	select {
	case target := <-browserTargets:
		if target != verificationLink {
			t.Fatalf("browser target = %q", target)
		}
	case <-time.After(60 * time.Second):
		t.Fatal("Enter did not open the verification page")
	}
	waitAppOutput(t, out, "Signed in to Kimi Code (Global). Model: kimi-coding-global/kimi-for-coding")
	if starts.Load() != 1 || polls.Load() != 2 {
		t.Fatalf("starts=%d polls=%d", starts.Load(), polls.Load())
	}

	authData, err := os.ReadFile(authPath)
	if err != nil {
		t.Fatal(err)
	}
	var entries map[string]map[string]any
	if err := json.Unmarshal(authData, &entries); err != nil {
		t.Fatal(err)
	}
	global, ok := entries[providerregistry.KimiGlobalProviderID]
	if !ok || len(global) != 4 {
		t.Fatalf("stored Global entry = %v", entries[providerregistry.KimiGlobalProviderID])
	}
	if global["type"] != "oauth" || global["access"] != "kimi-access" || global["refresh"] != "kimi-refresh" {
		t.Fatalf("stored Global fields = %v", global)
	}
	expires, ok := global["expires"].(float64)
	if !ok || int64(expires) <= time.Now().UnixMilli() {
		t.Fatalf("stored expiry = %v", global["expires"])
	}
	if entries["kimi-coding"]["key"] != "china-key" {
		t.Fatalf("China entry changed: %v", entries)
	}

	if _, err := inputWrite.Write([]byte("Say PSI_KIMI_OK\r")); err != nil {
		t.Fatal(err)
	}
	waitAppOutput(t, out, "kimi-trial-reply")
	select {
	case <-settled:
	case <-time.After(60 * time.Second):
		t.Fatal("streamed reply did not settle before the next command")
	}
	if inferences.Load() != 1 {
		t.Fatalf("inferences=%d", inferences.Load())
	}
	requestMu.Lock()
	if len(grants) != 1 || grants[0] != "17e5f671-d194-4dfb-9706-5516cb48c098" {
		t.Fatalf("grant forms = %v", grants)
	}
	if len(tokenRequests) != 6 || tokenRequests[0] != "17e5f671-d194-4dfb-9706-5516cb48c098" || tokenRequests[1] != "kimi-device-secret" || tokenRequests[2] != "urn:ietf:params:oauth:grant-type:device_code" {
		t.Fatalf("token form fields = %v", tokenRequests)
	}
	if len(completionRequests) != 3 || !strings.Contains(completionRequests[0], `"model":"kimi-for-coding"`) || completionRequests[1] != "Bearer kimi-access" || completionRequests[2] != "psi/test" {
		t.Fatalf("completion request = %v", completionRequests)
	}
	requestMu.Unlock()

	if _, err := inputWrite.Write([]byte("/logout\r")); err != nil {
		t.Fatal(err)
	}
	// The shared picker must contain both the China key and the Global login.
	waitAppOutput(t, out, "Stored credentials · 2 matches")
	if _, err := inputWrite.Write([]byte("\r")); err != nil {
		t.Fatal(err)
	}
	waitAppOutput(t, out, "Removed stored Kimi Code (Global) login")
	authData, err = os.ReadFile(authPath)
	if err != nil {
		t.Fatal(err)
	}
	entries = nil
	if err := json.Unmarshal(authData, &entries); err != nil {
		t.Fatal(err)
	}
	if _, present := entries[providerregistry.KimiGlobalProviderID]; present {
		t.Fatalf("Global entry survived logout: %s", authData)
	}
	if entries["kimi-coding"]["key"] != "china-key" {
		t.Fatalf("China entry changed after logout: %s", authData)
	}

	if _, err := inputWrite.Write([]byte("Say PSI_KIMI_AGAIN\r")); err != nil {
		t.Fatal(err)
	}
	waitAppOutput(t, out, "no Kimi Code (Global) credential")
	if inferences.Load() != 1 {
		t.Fatalf("offline submit made %d inference calls", inferences.Load())
	}
	if strings.Contains(out.String(), "kimi-access") || strings.Contains(out.String(), "kimi-refresh") || strings.Contains(out.String(), "kimi-device-secret") {
		t.Fatalf("transient secrets entered terminal output: %q", out.String())
	}
	if err := inputWrite.Close(); err != nil {
		t.Fatal(err)
	}
	if err := <-result; err != nil {
		t.Fatalf("repl: %v", err)
	}
	// Read runtime state only after the REPL goroutine has returned.
	if r.authErr == nil || r.prov != nil || r.state != StateOffline {
		t.Fatalf("Global logout did not leave the session offline: authErr=%v provider=%v state=%s", r.authErr, r.prov, r.state)
	}

	sessionData, err := os.ReadFile(r.store.File())
	if err != nil {
		t.Fatal(err)
	}
	sessionText := string(sessionData)
	if !strings.Contains(sessionText, "Say PSI_KIMI_OK") || !strings.Contains(sessionText, "kimi-trial-reply") {
		t.Fatalf("prompt or reply missing from the session: %s", sessionText)
	}
	for _, secret := range []string{"kimi-access", "kimi-refresh", "kimi-device-secret", "KIMI-CODE", "authorize_device", "auth.kimi.ai"} {
		if strings.Contains(sessionText, secret) {
			t.Fatalf("session contains %q", secret)
		}
	}
}

func readKimiForm(t *testing.T, r *http.Request) url.Values {
	t.Helper()
	if r.Header.Get("Content-Type") != "application/x-www-form-urlencoded" {
		t.Errorf("Content-Type = %q", r.Header.Get("Content-Type"))
	}
	data, err := io.ReadAll(r.Body)
	if err != nil {
		t.Fatal(err)
	}
	form, err := url.ParseQuery(string(data))
	if err != nil {
		t.Fatal(err)
	}
	return form
}

// An expired Global login with missing token fields is still listed and
// removable. Only the entry type is enough for local cleanup.
func TestLogoutListsExpiredKimiGlobalLogin(t *testing.T) {
	r := testRuntime(t)
	r.models = openAILoginTestModels(t)
	authPath := filepath.Join(r.agentDir, "auth.json")
	writeAppFile(t, authPath, `{"kimi-coding-global":{"type":"oauth","access":"","refresh":"","expires":1},"kimi-coding":{"type":"api_key","key":"china-key"}}`)
	if err := r.configure(config.StartupOverrides{Provider: providerregistry.KimiGlobalProviderID, Thinking: "off", ThinkingExplicit: true}); err != nil {
		t.Fatal(err)
	}
	if r.authErr == nil {
		t.Fatal("expired Global login was usable")
	}
	r.state = StateIdle
	console := authCommandConsole(t)
	r.lifecycle = &terminalLifecycle{console: console}
	var out bytes.Buffer
	if err := r.logoutCommand("", console, &out); err != nil {
		t.Fatal(err)
	}
	if r.selector == nil || r.selector.CredentialKinds[providerregistry.KimiGlobalProviderID] != provider.CredentialOAuth {
		t.Fatalf("/logout did not list the Global login: %+v", r.selector)
	}
	found := false
	for _, item := range r.selector.Items {
		if item.ID == providerregistry.KimiGlobalProviderID {
			found = true
			if !strings.Contains(item.FirstMessage, "local removal only") {
				t.Fatalf("Global row label = %q", item.FirstMessage)
			}
		}
	}
	if !found {
		t.Fatalf("Global row missing: %v", r.selector.Items)
	}
	r.finishLogout(provider.StoredCredential{ProviderID: providerregistry.KimiGlobalProviderID, Kind: provider.CredentialOAuth}, console, &out)
	if !strings.Contains(out.String(), "Removed stored Kimi Code (Global) login") || strings.Contains(out.String(), "china-key") {
		t.Fatalf("logout status = %q", out.String())
	}
	data, err := os.ReadFile(authPath)
	if err != nil {
		t.Fatal(err)
	}
	var entries map[string]map[string]any
	if err := json.Unmarshal(data, &entries); err != nil {
		t.Fatal(err)
	}
	if _, present := entries[providerregistry.KimiGlobalProviderID]; present {
		t.Fatalf("expired Global entry survived: %s", data)
	}
	if entries["kimi-coding"]["key"] != "china-key" {
		t.Fatalf("China entry changed: %s", data)
	}
	if r.authErr == nil || r.prov != nil || r.state != StateOffline {
		t.Fatalf("active Global selection did not go offline: authErr=%v provider=%v state=%s", r.authErr, r.prov, r.state)
	}
}

// The Global trial refuses header, key, and endpoint overrides before resolving
// any header value. These are configuration boundaries, not request retries.
func TestKimiGlobalConfigureRefusesOverrides(t *testing.T) {
	for _, tc := range []struct {
		name     string
		provider config.ProviderMeta
		want     string
	}{
		{
			name:     "authHeader",
			provider: config.ProviderMeta{AuthHeader: true},
			want:     "headers or authHeader",
		},
		{
			name:     "provider headers",
			provider: config.ProviderMeta{Headers: map[string]string{"X-Test": "$NOT_RESOLVED"}},
			want:     "headers or authHeader",
		},
		{
			name:     "models.json key",
			provider: config.ProviderMeta{APIKey: "configured-key"},
			want:     "not a models.json API key",
		},
		{
			name:     "provider endpoint",
			provider: config.ProviderMeta{BaseURL: "https://example.com/v1"},
			want:     "official Kimi endpoint",
		},
		{
			name: "model endpoint override",
			provider: config.ProviderMeta{ModelOverrides: map[string]json.RawMessage{
				"kimi-for-coding": json.RawMessage(`{"baseUrl":"https://example.com/v1"}`),
			}},
			want: "official Kimi endpoint",
		},
		{
			name: "model header override",
			provider: config.ProviderMeta{ModelOverrides: map[string]json.RawMessage{
				"kimi-for-coding": json.RawMessage(`{"headers":{"X-Test":"v"}}`),
			}},
			want: "headers or authHeader",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := testRuntime(t)
			r.models = &config.ModelsFile{Providers: map[string]config.ProviderMeta{
				providerregistry.KimiGlobalProviderID: tc.provider,
			}}
			err := r.configure(config.StartupOverrides{Provider: providerregistry.KimiGlobalProviderID, Thinking: "off", ThinkingExplicit: true})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("configure error = %v, want %q", err, tc.want)
			}
		})
	}
}
