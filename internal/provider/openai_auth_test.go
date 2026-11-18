package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"psi/internal/modelmeta"
)

// openAIDoc builds a synthetic auth.json with an openai OAuth entry, unknown
// fields, and an unrelated provider. Empty clientID omits the field.
func openAIDoc(access, refresh string, expires int64, clientID string) string {
	client := ""
	if clientID != "" {
		client = fmt.Sprintf(`,"clientId":%q`, clientID)
	}
	return fmt.Sprintf(`{"openai":{"type":"oauth","access":%q,"refresh":%q,"expires":%d%s,"scopes":["openid","%s"],"unknown":{"keep":true}},"unrelated":{"type":"api_key","key":"dummy-other-key"}}`,
		access, refresh, expires, client, openAIDirectScope)
}

func fixedOpenAINow() time.Time { return time.UnixMilli(1700000000000) }

type openAIFakeToken struct {
	calls    atomic.Int32
	handler  func(*http.Request) (*http.Response, error)
	requests []*http.Request
}

func (f *openAIFakeToken) RoundTrip(r *http.Request) (*http.Response, error) {
	f.calls.Add(1)
	f.requests = append(f.requests, r)
	if f.handler != nil {
		return f.handler(r)
	}
	return nil, fmt.Errorf("unexpected token request to %s", r.URL.String())
}

func openAITokenResponse(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}
}

func TestOpenAICredentialPrecedenceAndAbsentFallback(t *testing.T) {
	// Stored API key wins over configured and environment keys.
	dir := t.TempDir()
	path := filepath.Join(dir, "auth.json")
	writeAuthDoc(t, path, `{"openai":{"type":"api_key","key":"stored-openai-key"}}`)
	env := map[string]string{"OPENAI_API_KEY": "env-openai-key"}
	auth := &OpenAIAuth{Path: path, Env: env, ConfiguredKey: "configured-openai-key"}
	if err := auth.Check(); err != nil {
		t.Fatalf("stored API key Check: %v", err)
	}
	if key, err := auth.Get(context.Background()); err != nil || key != "stored-openai-key" {
		t.Fatalf("stored API key Get = %q, %v", key, err)
	}

	// Absent entry: configured key, then environment.
	writeAuthDoc(t, path, `{}`)
	if key, err := auth.Get(context.Background()); err != nil || key != "configured-openai-key" {
		t.Fatalf("configured fallback = %q, %v", key, err)
	}
	auth.ConfiguredKey = ""
	if key, err := auth.Get(context.Background()); err != nil || key != "env-openai-key" {
		t.Fatalf("environment fallback = %q, %v", key, err)
	}
	if err := auth.Check(); err != nil {
		t.Fatalf("environment Check: %v", err)
	}

	// Missing file and missing parent directory in both Check and Get.
	// Neither creates credential state.
	missingParent := filepath.Join(t.TempDir(), "no-such-dir", "auth.json")
	absent := &OpenAIAuth{Path: missingParent, Env: env}
	if err := absent.Check(); err != nil {
		t.Fatalf("missing parent Check: %v", err)
	}
	if key, err := absent.Get(context.Background()); err != nil || key != "env-openai-key" {
		t.Fatalf("missing parent Get = %q, %v", key, err)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(missingParent))); !os.IsNotExist(err) {
		t.Fatal("absent credential lookup created state")
	}
	missingFile := filepath.Join(t.TempDir(), "auth.json")
	absentFile := &OpenAIAuth{Path: missingFile, Env: env, ConfiguredKey: "configured-key"}
	if err := absentFile.Check(); err != nil {
		t.Fatalf("missing file Check: %v", err)
	}
	if key, err := absentFile.Get(context.Background()); err != nil || key != "configured-key" {
		t.Fatalf("missing file Get = %q, %v", key, err)
	}
	if _, err := os.Stat(missingFile); !os.IsNotExist(err) {
		t.Fatal("missing file lookup created auth.json")
	}

	// Stored invalid entries and OAuth failures never trigger fallback.
	for _, doc := range []string{
		`{"openai":{"type":"unsupported","key":"ignored"}}`,
		`{"openai":"not-an-object"}`,
		`{"openai":{"type":"api_key","key":""}}`,
		`{"openai":{"type":"oauth","access":"expired","refresh":"","expires":1,"clientId":"cid"}}`,
		`{"openai":{"type":"oauth","access":"expired","refresh":"  ","expires":1,"clientId":"cid"}}`,
		`{"openai":{"type":"oauth","access":"expired","refresh":"r","expires":1,"clientId":"\t"}}`,
		`{"openai":{"type":"oauth","access":"expired","refresh":"r","expires":"not-a-number","clientId":"cid"}}`,
	} {
		writeAuthDoc(t, path, doc)
		blocked := &OpenAIAuth{Path: path, Env: env, ConfiguredKey: "configured-fallback", HTTP: &http.Client{Transport: tokenTransportFunc(func(*http.Request) (*http.Response, error) {
			t.Error("invalid stored credential attempted refresh")
			return nil, errors.New("unexpected refresh")
		})}}
		if err := blocked.Check(); err == nil {
			t.Fatalf("invalid entry %s passed Check", doc)
		}
		if key, err := blocked.Get(context.Background()); err == nil || key != "" {
			t.Fatalf("invalid entry %s fell back to %q (%v)", doc, key, err)
		}
	}
}

func TestOpenAIDanglingAuthSymlinkDoesNotPermitFallback(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "auth.json")
	if err := os.Symlink(filepath.Join(dir, "missing.json"), path); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	auth := &OpenAIAuth{Path: path, ConfiguredKey: "synthetic-fallback-key"}
	if err := auth.Check(); err == nil {
		t.Error("local check treated the broken symlink as absent")
	}
	if key, err := auth.Get(context.Background()); err == nil || key != "" {
		t.Error("submission treated the broken symlink as absent")
	}
}

func TestOpenAIOAuthRefreshBoundary(t *testing.T) {
	now := fixedOpenAINow()
	boundary := now.UnixMilli() + openAIRefreshWindowMs
	for _, tc := range []struct {
		name, access string
		expires      string
		wantRefresh  bool
	}{
		{"at boundary", "synthetic-access", fmt.Sprint(boundary), true},
		{"fractionally fresh", "synthetic-access", fmt.Sprintf("%d.5", boundary), false},
		{"blank access despite fresh expiry", " \t", fmt.Sprintf("%d.5", boundary), true},
		{"far past expiry", "synthetic-access", "-9223372036854775808", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "auth.json")
			writeAuthDoc(t, path, fmt.Sprintf(`{"openai":{"type":"oauth","access":%q,"refresh":"synthetic-refresh","expires":%s,"clientId":"synthetic-client"}}`, tc.access, tc.expires))
			fake := &openAIFakeToken{handler: func(req *http.Request) (*http.Response, error) {
				if req.URL.String() != openAITokenURL {
					t.Error("unexpected token URL")
				}
				return openAITokenResponse(200, `{"access_token":"rotated-access","refresh_token":"rotated-refresh","scope":"`+openAIDirectScope+`","expires_in":3600.0005}`), nil
			}}
			auth := &OpenAIAuth{Path: path, now: func() time.Time { return now }, HTTP: &http.Client{Transport: fake}}
			if err := auth.Check(); err != nil || fake.calls.Load() != 0 {
				t.Fatalf("local check exchanged credentials or rejected refreshable login: %v", err)
			}
			key, err := auth.Get(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if !tc.wantRefresh {
				if key != tc.access || fake.calls.Load() != 0 {
					t.Fatal("fresh access token was not reused")
				}
				return
			}
			if key != "rotated-access" || fake.calls.Load() != 1 {
				t.Fatal("due credential was not refreshed exactly once")
			}
			entries, err := AuthEntries(path)
			if err != nil {
				t.Fatal(err)
			}
			var entry struct{ Expires float64 }
			want := float64(now.UnixMilli()+3600*1000-openAIExpiryMarginMs) + 0.5
			if err := json.Unmarshal(entries["openai"], &entry); err != nil || entry.Expires != want {
				t.Fatalf("persisted expiry = %v, want %v (%v)", entry.Expires, want, err)
			}
		})
	}
}

func TestOpenAICheckDoesNotContactTokenEndpoint(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auth.json")
	writeAuthDoc(t, path, openAIDoc("expired-access", "stored-refresh", 1, "issued-client"))
	fake := &openAIFakeToken{handler: func(*http.Request) (*http.Response, error) {
		t.Error("Check contacted the token endpoint")
		return openAITokenResponse(500, `{}`), nil
	}}
	auth := &OpenAIAuth{Path: path, HTTP: &http.Client{Transport: fake}}
	if err := auth.Check(); err != nil {
		t.Fatalf("refreshable OAuth Check: %v", err)
	}
	if fake.calls.Load() != 0 {
		t.Fatal("credential check performed a refresh")
	}
}

func TestOpenAIRefreshPersistsAndReusesRotation(t *testing.T) {
	now := fixedOpenAINow()
	path := filepath.Join(t.TempDir(), "auth.json")
	writeAuthDoc(t, path, openAIDoc("expired-access", "stored-refresh", 1, "issued-client"))
	fake := &openAIFakeToken{}
	fake.handler = func(r *http.Request) (*http.Response, error) {
		if r.URL.String() != openAITokenURL {
			t.Errorf("refresh posted to %s", r.URL.String())
		}
		if err := r.ParseForm(); err != nil || r.Form.Get("grant_type") != "refresh_token" || r.Form.Get("client_id") != "issued-client" || r.Form.Get("refresh_token") != "stored-refresh" || r.Form.Get("resource") != openAIResource {
			t.Error("incorrect refresh protocol")
		}
		return openAITokenResponse(200, `{"access_token":"rotated-access","refresh_token":"rotated-refresh","scope":"openid `+openAIDirectScope+`","expires_in":3600}`), nil
	}
	auth := &OpenAIAuth{Path: path, HTTP: &http.Client{Transport: fake}}
	auth.now = func() time.Time { return now }
	token, err := auth.Get(context.Background())
	if err != nil || token != "rotated-access" {
		t.Fatalf("refresh Get = %q, %v", token, err)
	}
	data, _ := os.ReadFile(path)
	var all map[string]json.RawMessage
	if json.Unmarshal(data, &all) != nil {
		t.Fatal("auth.json was corrupted")
	}
	if !bytes.Contains(all["unrelated"], []byte("dummy-other-key")) || !bytes.Contains(all["openai"], []byte(`"unknown"`)) {
		t.Fatal("refresh discarded unrelated or unknown credential data")
	}
	var updated struct {
		Type     string   `json:"type"`
		Access   string   `json:"access"`
		Refresh  string   `json:"refresh"`
		Expires  int64    `json:"expires"`
		ClientID string   `json:"clientId"`
		Scopes   []string `json:"scopes"`
	}
	if json.Unmarshal(all["openai"], &updated) != nil {
		t.Fatal("openai entry was corrupted")
	}
	wantExpires := now.UnixMilli() + 3600*1000 - openAIExpiryMarginMs
	if updated.Type != "oauth" || updated.Access != "rotated-access" || updated.Refresh != "rotated-refresh" || updated.Expires != wantExpires || updated.ClientID != "issued-client" {
		t.Fatalf("persisted rotation = %+v, want expires %d", updated, wantExpires)
	}
	if len(updated.Scopes) != 2 || updated.Scopes[1] != openAIDirectScope {
		t.Fatalf("persisted scopes = %v", updated.Scopes)
	}
	if st, _ := os.Stat(path); st.Mode().Perm() != 0o600 {
		t.Fatal("refreshed auth is not private")
	}
	// A later caller reuses the persisted credential without another exchange.
	later := &OpenAIAuth{Path: path, HTTP: &http.Client{Transport: fake}}
	later.now = func() time.Time { return now }
	if token, err := later.Get(context.Background()); err != nil || token != "rotated-access" || fake.calls.Load() != 1 {
		t.Fatalf("reuse Get = %q, %v, exchanges = %d", token, err, fake.calls.Load())
	}
	if _, err := os.Stat(path + ".lock"); !os.IsNotExist(err) {
		t.Fatal("auth lock leaked")
	}
}

func TestOpenAICancelDuringExchangePersistsThenCancels(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auth.json")
	writeAuthDoc(t, path, openAIDoc("expired-access", "stored-refresh", 1, "issued-client"))
	started := make(chan struct{})
	release := make(chan struct{})
	var responsesPosts atomic.Int32
	transport := tokenTransportFunc(func(r *http.Request) (*http.Response, error) {
		switch r.URL.String() {
		case openAITokenURL:
			close(started)
			select {
			case <-release:
			case <-time.After(10 * time.Second):
				return nil, fmt.Errorf("test deadlock waiting for release")
			}
			return openAITokenResponse(200, `{"access_token":"cancel-rotated-access","refresh_token":"cancel-rotated-refresh","scope":"`+openAIDirectScope+`","expires_in":3600}`), nil
		case "https://api.openai.com/v1/responses":
			responsesPosts.Add(1)
			return nil, fmt.Errorf("Responses POST must not follow cancellation")
		default:
			return nil, fmt.Errorf("unexpected URL %s", r.URL.String())
		}
	})
	shared := &http.Client{Transport: transport}
	auth := &OpenAIAuth{Path: path, HTTP: shared}
	client := NewResponsesClient("https://api.openai.com/v1", "", 0, time.Second)
	client.HTTP = shared
	client.ResolveAuth = auth.Get
	model := ModelMeta{ID: "gpt-6.1-sol", API: "openai-responses", Provider: "openai", BaseURL: "https://api.openai.com/v1", MaxTokens: 1024, ContextWindow: 8192}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := client.Send(ctx, Request{Model: model, Thinking: "off", Messages: []Message{{Role: "user", Content: []Block{{Type: "text", Text: "hi"}}}}}, nil)
		done <- err
	}()
	select {
	case <-started:
	case <-time.After(10 * time.Second):
		t.Fatal("exchange did not start")
	}
	cancel()
	close(release)
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancelled send = %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("cancelled send did not return")
	}
	if responsesPosts.Load() != 0 {
		t.Fatal("cancelled caller submitted a Responses request")
	}
	data, _ := os.ReadFile(path)
	if !bytes.Contains(data, []byte("cancel-rotated-refresh")) {
		t.Fatal("cancellation discarded a successfully returned rotation")
	}
}

func TestOpenAIRefreshFailuresDoNotFallbackOrRewrite(t *testing.T) {
	newFixture := func(t *testing.T) (string, *OpenAIAuth, *openAIFakeToken) {
		path := filepath.Join(t.TempDir(), "auth.json")
		writeAuthDoc(t, path, openAIDoc("expired-access", "stored-refresh", 1, "issued-client"))
		fake := &openAIFakeToken{}
		auth := &OpenAIAuth{Path: path, HTTP: &http.Client{Transport: fake}, Env: map[string]string{"OPENAI_API_KEY": "env-fallback"}, ConfiguredKey: "configured-fallback"}
		return path, auth, fake
	}
	t.Run("failed exchange", func(t *testing.T) {
		path, auth, fake := newFixture(t)
		before, _ := os.ReadFile(path)
		fake.handler = func(*http.Request) (*http.Response, error) {
			return openAITokenResponse(401, `{"error":"stored-refresh must not appear"}`), nil
		}
		if key, err := auth.Get(context.Background()); err == nil || key != "" {
			t.Fatalf("failed exchange fell back to %q (%v)", key, err)
		} else if strings.Contains(err.Error(), "stored-refresh") || strings.Contains(err.Error(), "expired-access") {
			t.Fatalf("exchange failure leaked a credential: %v", err)
		}
		after, _ := os.ReadFile(path)
		if !bytes.Equal(before, after) {
			t.Fatal("failed exchange rewrote credentials")
		}
		if fake.calls.Load() != 1 {
			t.Fatalf("exchanges = %d, want one attempt", fake.calls.Load())
		}
	})
	t.Run("rename fault", func(t *testing.T) {
		path, auth, fake := newFixture(t)
		before, _ := os.ReadFile(path)
		fake.handler = func(*http.Request) (*http.Response, error) {
			return openAITokenResponse(200, `{"access_token":"new-access","refresh_token":"new-refresh","scope":"`+openAIDirectScope+`","expires_in":3600}`), nil
		}
		auth.Rename = func(string, string) error { return fmt.Errorf("injected rename failure") }
		if key, err := auth.Get(context.Background()); err == nil || key != "" {
			t.Fatalf("rename fault fell back to %q (%v)", key, err)
		}
		after, _ := os.ReadFile(path)
		if !bytes.Equal(before, after) {
			t.Fatal("failed commit rewrote credentials")
		}
		if leftovers, _ := filepath.Glob(filepath.Join(filepath.Dir(path), ".psi-auth-*")); len(leftovers) != 0 {
			t.Fatalf("temp files remain: %v", leftovers)
		}
	})
}

func TestOpenAIRefreshResponseValidation(t *testing.T) {
	validScope := "openid " + openAIDirectScope
	for _, tc := range []struct {
		name   string
		status int
		body   string
	}{
		{"missing replacement refresh", 200, `{"access_token":"a","scope":"` + validScope + `","expires_in":3600}`},
		{"blank replacement refresh", 200, `{"access_token":"a","refresh_token":"  ","scope":"` + validScope + `","expires_in":3600}`},
		{"missing scope", 200, `{"access_token":"a","refresh_token":"r","expires_in":3600}`},
		{"scope without direct use", 200, `{"access_token":"a","refresh_token":"r","scope":"openid profile","expires_in":3600}`},
		{"substring-only scope", 200, `{"access_token":"a","refresh_token":"r","scope":"openid ` + openAIDirectScope + `.evil","expires_in":3600}`},
		{"missing lifetime", 200, `{"access_token":"a","refresh_token":"r","scope":"` + validScope + `"}`},
		{"zero lifetime", 200, `{"access_token":"a","refresh_token":"r","scope":"` + validScope + `","expires_in":0}`},
		{"negative lifetime", 200, `{"access_token":"a","refresh_token":"r","scope":"` + validScope + `","expires_in":-5}`},
		{"string lifetime", 200, `{"access_token":"a","refresh_token":"r","scope":"` + validScope + `","expires_in":"3600"}`},
		{"missing access", 200, `{"refresh_token":"r","scope":"` + validScope + `","expires_in":3600}`},
		{"malformed JSON", 200, `{"access_token":`},
		{"non-2xx with valid JSON", 400, `{"access_token":"a","refresh_token":"r","scope":"` + validScope + `","expires_in":3600}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "auth.json")
			writeAuthDoc(t, path, openAIDoc("expired-access", "stored-refresh", 1, "issued-client"))
			before, _ := os.ReadFile(path)
			fake := &openAIFakeToken{handler: func(*http.Request) (*http.Response, error) {
				return openAITokenResponse(tc.status, tc.body), nil
			}}
			auth := &OpenAIAuth{Path: path, HTTP: &http.Client{Transport: fake}, Env: map[string]string{"OPENAI_API_KEY": "env-fallback"}}
			if key, err := auth.Get(context.Background()); err == nil || key != "" {
				t.Fatalf("invalid response fell back to %q (%v)", key, err)
			}
			after, _ := os.ReadFile(path)
			if !bytes.Equal(before, after) {
				t.Fatal("invalid response rewrote credentials")
			}
			if fake.calls.Load() != 1 {
				t.Fatalf("exchanges = %d, want one attempt", fake.calls.Load())
			}
		})
	}
	t.Run("valid replaces scopes and preserves client", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "auth.json")
		writeAuthDoc(t, path, `{"openai":{"type":"oauth","access":"expired","refresh":"stored-refresh","expires":1,"clientId":"original-client","scopes":["stale-scope"]}}`)
		fake := &openAIFakeToken{handler: func(*http.Request) (*http.Response, error) {
			return openAITokenResponse(200, `{"access_token":"a","refresh_token":"r","scope":"openid `+openAIDirectScope+` extra","expires_in":60}`), nil
		}}
		auth := &OpenAIAuth{Path: path, HTTP: &http.Client{Transport: fake}}
		if _, err := auth.Get(context.Background()); err != nil {
			t.Fatal(err)
		}
		data, _ := os.ReadFile(path)
		var doc map[string]json.RawMessage
		if json.Unmarshal(data, &doc) != nil {
			t.Fatal("auth.json was corrupted")
		}
		var entry struct {
			ClientID string   `json:"clientId"`
			Scopes   []string `json:"scopes"`
		}
		if json.Unmarshal(doc["openai"], &entry) != nil {
			t.Fatal("openai entry was corrupted")
		}
		if entry.ClientID != "original-client" {
			t.Fatalf("client ID = %q", entry.ClientID)
		}
		if len(entry.Scopes) != 3 || entry.Scopes[1] != openAIDirectScope || entry.Scopes[0] != "openid" {
			t.Fatalf("scopes = %v", entry.Scopes)
		}
	})
}

func TestOpenAISharedLoginProtections(t *testing.T) {
	newExpired := func(t *testing.T) string {
		path := filepath.Join(t.TempDir(), "auth.json")
		writeAuthDoc(t, path, openAIDoc("expired-access", "stored-refresh", 1, "issued-client"))
		return path
	}
	t.Run("same-provider change during exchange", func(t *testing.T) {
		path := newExpired(t)
		fake := &openAIFakeToken{handler: func(*http.Request) (*http.Response, error) {
			data, _ := os.ReadFile(path)
			var all map[string]json.RawMessage
			_ = json.Unmarshal(data, &all)
			var fields map[string]json.RawMessage
			_ = json.Unmarshal(all["openai"], &fields)
			fields["access"], _ = json.Marshal("concurrent-access")
			all["openai"], _ = json.Marshal(fields)
			encoded, _ := json.Marshal(all)
			_ = os.WriteFile(path, encoded, 0o600)
			return openAITokenResponse(200, `{"access_token":"stale-rotation","refresh_token":"stale-refresh","scope":"`+openAIDirectScope+`","expires_in":3600}`), nil
		}}
		auth := &OpenAIAuth{Path: path, HTTP: &http.Client{Transport: fake}}
		if _, err := auth.Get(context.Background()); err == nil || !strings.Contains(err.Error(), "refusing overwrite") {
			t.Fatalf("concurrent change was overwritten: %v", err)
		}
		data, _ := os.ReadFile(path)
		if !bytes.Contains(data, []byte("concurrent-access")) || bytes.Contains(data, []byte("stale-rotation")) {
			t.Fatal("concurrent same-provider change was lost")
		}
	})
	t.Run("lease loss", func(t *testing.T) {
		path := newExpired(t)
		fake := &openAIFakeToken{handler: func(*http.Request) (*http.Response, error) {
			_ = os.Remove(path + ".lock")
			return openAITokenResponse(200, `{"access_token":"a","refresh_token":"r","scope":"`+openAIDirectScope+`","expires_in":3600}`), nil
		}}
		auth := &OpenAIAuth{Path: path, HTTP: &http.Client{Transport: fake}}
		if _, err := auth.Get(context.Background()); err == nil {
			t.Fatal("lease loss was ignored")
		}
		data, _ := os.ReadFile(path)
		if bytes.Contains(data, []byte(`"access":"a"`)) {
			t.Fatal("lease loss allowed an overwrite")
		}
	})
	t.Run("symlink refused before exchange", func(t *testing.T) {
		dir := t.TempDir()
		target := filepath.Join(dir, "real-auth.json")
		writeAuthDoc(t, target, openAIDoc("expired-access", "stored-refresh", 1, "issued-client"))
		link := filepath.Join(dir, "auth.json")
		if err := os.Symlink(target, link); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
		fake := &openAIFakeToken{handler: func(*http.Request) (*http.Response, error) {
			t.Error("symlinked auth performed an exchange")
			return openAITokenResponse(500, `{}`), nil
		}}
		auth := &OpenAIAuth{Path: link, HTTP: &http.Client{Transport: fake}}
		if _, err := auth.Get(context.Background()); err == nil {
			t.Fatal("symlink was accepted")
		}
		if fake.calls.Load() != 0 {
			t.Fatal("symlink triggered a token exchange")
		}
	})
	t.Run("unrelated write after temp creation refused", func(t *testing.T) {
		path := newExpired(t)
		fake := &openAIFakeToken{handler: func(*http.Request) (*http.Response, error) {
			return openAITokenResponse(200, `{"access_token":"late-rotation","refresh_token":"late-refresh","scope":"`+openAIDirectScope+`","expires_in":3600}`), nil
		}}
		auth := &OpenAIAuth{Path: path, HTTP: &http.Client{Transport: fake}}
		auth.Chmod = func(name string, mode os.FileMode) error {
			// A separate writer modifies another provider after refresh
			// takes its merge snapshot but before the rename.
			var doc map[string]json.RawMessage
			if err := json.Unmarshal([]byte(openAIDoc("expired-access", "stored-refresh", 1, "issued-client")), &doc); err != nil {
				return err
			}
			doc["other"] = json.RawMessage(`{"type":"api_key","key":"concurrent-other-key"}`)
			encoded, err := json.Marshal(doc)
			if err != nil {
				return err
			}
			if err := os.WriteFile(path, encoded, 0o600); err != nil {
				return err
			}
			return os.Chmod(name, mode)
		}
		if _, err := auth.Get(context.Background()); err == nil || !strings.Contains(err.Error(), "refusing overwrite") {
			t.Fatalf("late change was overwritten: %v", err)
		}
		data, _ := os.ReadFile(path)
		if !bytes.Contains(data, []byte("concurrent-other-key")) || bytes.Contains(data, []byte("late-rotation")) {
			t.Fatal("late concurrent change was lost")
		}
		if leftovers, _ := filepath.Glob(filepath.Join(filepath.Dir(path), ".psi-auth-*")); len(leftovers) != 0 {
			t.Fatalf("temp files remain: %v", leftovers)
		}
	})
}

func TestOpenAIHTTP429With1308StaysPrivate(t *testing.T) {
	const authToken = "synthetic-auth-token"
	const escapeText = "\x1b[31mredacted-reset"
	body := `{"code":1308,"message":"limit reached for ` + authToken + ` ` + escapeText + `, resets later"}`
	new429 := func() *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = io.WriteString(w, body)
		}))
	}
	// Built-in openai uses a static status message.
	openaiServer := new429()
	defer openaiServer.Close()
	client := NewResponsesClient(openaiServer.URL, authToken, 0, time.Second)
	defer client.CloseIdle()
	model := ModelMeta{ID: "gpt-6.1-sol", API: "openai-responses", Provider: "openai", BaseURL: openaiServer.URL, MaxTokens: 128, ContextWindow: 4096}
	_, err := client.Send(context.Background(), Request{Model: model, Thinking: "off", Messages: []Message{{Role: "user", Content: []Block{{Type: "text", Text: "hi"}}}}}, nil)
	if err == nil {
		t.Fatal("expected an error")
	}
	if strings.Contains(err.Error(), authToken) || strings.Contains(err.Error(), "1308") || strings.Contains(err.Error(), "resets later") || strings.Contains(err.Error(), escapeText) {
		t.Fatalf("OpenAI error leaked the provider body: %v", err)
	}
	var providerErr *ProviderError
	if !errors.As(err, &providerErr) || providerErr.Message != "Too Many Requests" {
		t.Fatalf("OpenAI message = %v", err)
	}
	if strings.Contains(providerErr.Detail, authToken) {
		t.Fatal("OpenAI private detail leaked the token text")
	}

	// Other providers keep the shared Zhipu raw-body exception.
	otherServer := new429()
	defer otherServer.Close()
	other := NewResponsesClient(otherServer.URL, "synthetic-key", 0, time.Second)
	defer other.CloseIdle()
	otherModel := ModelMeta{ID: "m", API: "openai-responses", Provider: "opencode-go", BaseURL: otherServer.URL, MaxTokens: 128, ContextWindow: 4096, Compat: modelmeta.Compat{"sessionAffinityFormat": json.RawMessage(`"openai-nosession"`)}, Custom: false}
	// opencode-go with a nonstandard base still exercises the shared path.
	_, err = other.Send(context.Background(), Request{Model: otherModel, Thinking: "off", Messages: []Message{{Role: "user", Content: []Block{{Type: "text", Text: "hi"}}}}}, nil)
	if err == nil || !strings.Contains(err.Error(), "1308") {
		t.Fatalf("other-provider Zhipu exception changed: %v", err)
	}
}

func TestOpenAISubscriptionRequestProfile(t *testing.T) {
	for _, tc := range []struct {
		name      string
		token     string
		wantLimit bool
	}{
		{"subscription omits limit", "synthetic-oauth-token", false},
		{"api key retains limit", "sk-synthetic-key", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var gotAuth, gotSession, gotRequestID string
			var gotBody map[string]any
			transport := tokenTransportFunc(func(r *http.Request) (*http.Response, error) {
				if r.URL.String() != "https://api.openai.com/v1/responses" {
					return nil, fmt.Errorf("unexpected URL %s", r.URL.String())
				}
				gotAuth = r.Header.Get("Authorization")
				gotSession = r.Header.Get("session_id")
				gotRequestID = r.Header.Get("x-client-request-id")
				data, _ := io.ReadAll(r.Body)
				if json.Unmarshal(data, &gotBody) != nil {
					return nil, fmt.Errorf("request body was not JSON")
				}
				stream := responsesEvent("response.completed", map[string]any{"response": map[string]any{"id": "r", "status": "completed", "output": []any{}}})
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(stream)), Header: make(http.Header)}, nil
			})
			client := NewResponsesClient("https://api.openai.com/v1", "", 0, time.Second)
			defer client.CloseIdle()
			client.HTTP = &http.Client{Transport: transport}
			client.SessionID = "synthetic-session"
			client.ResolveAuth = func(context.Context) (string, error) { return tc.token, nil }
			model := ModelMeta{ID: "gpt-6.1-sol", API: "openai-responses", Provider: "openai", BaseURL: "https://api.openai.com/v1", MaxTokens: 512, ContextWindow: 4096}
			resp, err := client.Send(context.Background(), Request{Model: model, Thinking: "off", Messages: []Message{{Role: "user", Content: []Block{{Type: "text", Text: "hi"}}}}}, nil)
			if err != nil || resp == nil {
				t.Fatalf("subscription request failed: %v", err)
			}
			if gotAuth != "Bearer "+tc.token {
				t.Fatalf("bearer = %q", gotAuth)
			}
			if gotSession != "synthetic-session" || gotRequestID != "synthetic-session" {
				t.Fatalf("session headers = %q %q", gotSession, gotRequestID)
			}
			if gotBody["store"] != false {
				t.Fatalf("store = %v", gotBody["store"])
			}
			_, hasLimit := gotBody["max_output_tokens"]
			if hasLimit != tc.wantLimit {
				t.Fatalf("max_output_tokens present = %v, want %v (%v)", hasLimit, tc.wantLimit, gotBody)
			}
			if client.APIKey != "" {
				t.Fatal("callback token mutated the shared key")
			}
		})
	}
}

func TestOpenAIRetryUsesFreshTokenSnapshot(t *testing.T) {
	tokens := []string{"sk-first-token", "oauth-second-token"}
	var authCalls atomic.Int32
	var bearers []string
	var bodies []map[string]any
	transport := tokenTransportFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.String() != "https://api.openai.com/v1/responses" {
			return nil, fmt.Errorf("unexpected URL %s", r.URL.String())
		}
		bearers = append(bearers, r.Header.Get("Authorization"))
		data, _ := io.ReadAll(r.Body)
		var decoded map[string]any
		_ = json.Unmarshal(data, &decoded)
		bodies = append(bodies, decoded)
		if len(bearers) == 1 {
			resp := &http.Response{StatusCode: 503, Body: io.NopCloser(strings.NewReader("busy")), Header: make(http.Header)}
			resp.Header.Set("Retry-After", "0.001")
			return resp, nil
		}
		stream := responsesEvent("response.completed", map[string]any{"response": map[string]any{"id": "r", "status": "completed", "output": []any{}}})
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(stream)), Header: make(http.Header)}, nil
	})
	client := NewResponsesClient("https://api.openai.com/v1", "static-key-unmutated", 1, time.Second)
	defer client.CloseIdle()
	client.HTTP = &http.Client{Transport: transport}
	client.ResolveAuth = func(context.Context) (string, error) {
		call := authCalls.Add(1)
		return tokens[call-1], nil
	}
	model := ModelMeta{ID: "gpt-6.1-sol", API: "openai-responses", Provider: "openai", BaseURL: "https://api.openai.com/v1", MaxTokens: 512, ContextWindow: 4096}
	if _, err := client.Send(context.Background(), Request{Model: model, Thinking: "off", Messages: []Message{{Role: "user", Content: []Block{{Type: "text", Text: "hi"}}}}}, nil); err != nil {
		t.Fatalf("retry send: %v", err)
	}
	if authCalls.Load() != 2 || len(bearers) != 2 {
		t.Fatalf("auth calls = %d, attempts = %d", authCalls.Load(), len(bearers))
	}
	if bearers[0] != "Bearer sk-first-token" || bearers[1] != "Bearer oauth-second-token" {
		t.Fatalf("bearers = %v", bearers)
	}
	if _, hasLimit := bodies[0]["max_output_tokens"]; !hasLimit {
		t.Fatal("first attempt lost its API-key limit")
	}
	if _, hasLimit := bodies[1]["max_output_tokens"]; hasLimit {
		t.Fatal("second attempt kept a limit for its subscription token")
	}
	if client.APIKey != "static-key-unmutated" {
		t.Fatal("retry mutated the shared key")
	}
}

func TestOpenAIInvalidRequestFailsBeforeAuth(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auth.json")
	writeAuthDoc(t, path, openAIDoc("expired-access", "stored-refresh", 1, "issued-client"))
	before, _ := os.ReadFile(path)
	var exchanges, posts atomic.Int32
	transport := tokenTransportFunc(func(r *http.Request) (*http.Response, error) {
		switch {
		case r.URL.String() == openAITokenURL:
			exchanges.Add(1)
		case strings.HasSuffix(r.URL.String(), "/responses"):
			posts.Add(1)
		}
		return nil, fmt.Errorf("request must not reach the network")
	})
	shared := &http.Client{Transport: transport}
	auth := &OpenAIAuth{Path: path, HTTP: shared}
	newClient := func(baseURL string) *ResponsesClient {
		client := NewResponsesClient(baseURL, "", 0, time.Second)
		client.HTTP = shared
		client.ResolveAuth = auth.Get
		return client
	}
	// Invalid endpoint: the client base URL fails validation before auth.
	goodModel := ModelMeta{ID: "gpt-6.1-sol", API: "openai-responses", Provider: "openai", BaseURL: "https://api.openai.com/v1", MaxTokens: 128, ContextWindow: 1024}
	if _, err := newClient("invalid-endpoint").Send(context.Background(), Request{Model: goodModel, Thinking: "off"}, nil); err == nil {
		t.Fatal("invalid endpoint was accepted")
	}
	// Invalid request: unsupported thinking level.
	if _, err := newClient("").Send(context.Background(), Request{Model: goodModel, Thinking: "invalid-level"}, nil); err == nil {
		t.Fatal("invalid thinking level was accepted")
	}
	// Unencodable tool schema: json.Marshal fails only at encoding time.
	badTool := ToolDef{Name: "bad", Description: "bad", Schema: map[string]any{"type": "object", "bad": func() {}}}
	if _, err := newClient("").Send(context.Background(), Request{Model: goodModel, Thinking: "off", Tools: []ToolDef{badTool}}, nil); err == nil {
		t.Fatal("unencodable schema was accepted")
	}
	if exchanges.Load() != 0 || posts.Load() != 0 {
		t.Fatalf("invalid requests caused exchanges=%d posts=%d", exchanges.Load(), posts.Load())
	}
	after, _ := os.ReadFile(path)
	if !bytes.Equal(before, after) {
		t.Fatal("invalid request rewrote credentials")
	}
}
