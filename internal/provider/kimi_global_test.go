package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hypernewbie/psi/internal/providerregistry"
)

type kimiFakeClock struct {
	mu    sync.Mutex
	now   time.Time
	waits []time.Duration
}

func (c *kimiFakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *kimiFakeClock) Wait(ctx context.Context, delay time.Duration) error {
	c.mu.Lock()
	c.waits = append(c.waits, delay)
	c.now = c.now.Add(delay)
	c.mu.Unlock()
	return ctx.Err()
}

func (c *kimiFakeClock) recordedWaits() []time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]time.Duration(nil), c.waits...)
}

func kimiDeviceStartBody(interval, expiresIn string) string {
	return fmt.Sprintf(`{"device_code":"kimi-device-secret","user_code":"KIMI-CODE","verification_uri":%q,"verification_uri_complete":"https://www.kimi.ai/code/authorize_device?code=ignored","interval":%s,"expires_in":%s}`, kimiGlobalVerifyURL, interval, expiresIn)
}

const kimiDeviceSuccessBody = `{"access_token":"kimi-access","refresh_token":"kimi-refresh","expires_in":3600,"token_type":"Bearer"}`

func runKimiDevice(t *testing.T, authPath, startBody string, poll func(int) *http.Response, ctx context.Context, wait func(context.Context, time.Duration) error, ready func(string, string)) (error, *kimiFakeClock, *atomic.Int32) {
	t.Helper()
	clock := &kimiFakeClock{now: fixedOpenAINow()}
	var polls atomic.Int32
	transport := &openAIFakeToken{handler: func(r *http.Request) (*http.Response, error) {
		switch r.URL.String() {
		case kimiGlobalStartURL:
			return openAITokenResponse(http.StatusOK, startBody), nil
		case kimiGlobalTokenURL:
			return poll(int(polls.Add(1))), nil
		default:
			t.Fatalf("unexpected Kimi device URL %s", r.URL)
			return nil, errors.New("unexpected Kimi device request")
		}
	}}
	if wait == nil {
		wait = clock.Wait
	}
	login := &KimiGlobalLogin{
		Path: authPath,
		HTTP: &http.Client{Transport: transport},
		Wait: wait,
		now:  clock.Now,
	}
	err := login.Run(ctx, ready)
	return err, clock, &polls
}

func kimiJSON(status int, body string) *http.Response {
	return openAITokenResponse(status, body)
}

// kimiFailingBody returns a partial response and then a transport read error.
type kimiFailingBody struct {
	wrote bool
}

func (b *kimiFailingBody) Read(p []byte) (int, error) {
	if !b.wrote {
		b.wrote = true
		return copy(p, `{"access_token":"partial`), nil
	}
	return 0, errors.New("connection reset")
}

func (b *kimiFailingBody) Close() error { return nil }

func TestKimiGlobalDeviceFailure(t *testing.T) {
	validStart := kimiDeviceStartBody("1", "60")
	for _, tc := range []struct {
		name string
		body string
	}{
		{name: "wrong verification URI", body: strings.Replace(validStart, kimiGlobalVerifyURL, "https://example.com/authorize", 1)},
		{name: "user code with a newline", body: strings.Replace(validStart, "KIMI-CODE", `BAD\nCODE`, 1)},
		{name: "missing device code", body: strings.Replace(validStart, `"device_code":"kimi-device-secret",`, "", 1)},
		{name: "missing user code", body: strings.Replace(validStart, `"user_code":"KIMI-CODE",`, "", 1)},
		{name: "negative interval", body: kimiDeviceStartBody("-1", "60")},
		{name: "fractional interval", body: kimiDeviceStartBody("1.5", "60")},
		{name: "zero lifetime", body: kimiDeviceStartBody("1", "0")},
		{name: "negative lifetime", body: kimiDeviceStartBody("1", "-5")},
		{name: "fractional lifetime", body: kimiDeviceStartBody("1", "60.5")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			authPath := filepath.Join(t.TempDir(), "auth.json")
			var readyCalled bool
			err, _, polls := runKimiDevice(t, authPath, tc.body, func(int) *http.Response {
				t.Fatal("polled after an invalid grant")
				return nil
			}, context.Background(), nil, func(string, string) { readyCalled = true })
			if !errors.Is(err, errKimiGlobalInvalid) || readyCalled || polls.Load() != 0 {
				t.Fatalf("error=%v ready=%v polls=%d", err, readyCalled, polls.Load())
			}
			if _, statErr := os.Stat(authPath); !os.IsNotExist(statErr) {
				t.Fatalf("auth file exists after invalid start: %v", statErr)
			}
		})
	}

	t.Run("transient poll recovers without polling early", func(t *testing.T) {
		authPath := filepath.Join(t.TempDir(), "auth.json")
		responses := []*http.Response{
			kimiJSON(http.StatusServiceUnavailable, `{"error":"temporarily_unavailable"}`),
			kimiJSON(http.StatusBadRequest, `{"error":"authorization_pending"}`),
			kimiJSON(http.StatusOK, kimiDeviceSuccessBody),
		}
		responses[0].Header.Set("Retry-After", "3")
		ready := 0
		err, clock, polls := runKimiDevice(t, authPath, kimiDeviceStartBody("1", "60"), func(n int) *http.Response {
			return responses[n-1]
		}, context.Background(), nil, func(code, uri string) {
			ready++
			if code != "KIMI-CODE" || uri != kimiGlobalVerifyURL {
				t.Fatalf("ready = %q, %q", code, uri)
			}
		})
		if err != nil || ready != 1 || polls.Load() != 3 {
			t.Fatalf("error=%v ready=%d polls=%d", err, ready, polls.Load())
		}
		waits := clock.recordedWaits()
		want := []time.Duration{time.Second, 3 * time.Second, time.Second}
		if fmt.Sprint(waits) != fmt.Sprint(want) {
			t.Fatalf("waits = %v, want %v", waits, want)
		}
		entry := readKimiGlobalEntry(t, authPath)
		if len(entry) != 4 || entry["type"] != "oauth" || entry["access"] != "kimi-access" || entry["refresh"] != "kimi-refresh" {
			t.Fatalf("stored entry = %v", entry)
		}
		if got := int64(entry["expires"].(float64)); got != clock.Now().UnixMilli()+3600_000 {
			t.Fatalf("expires = %d", got)
		}
	})

	t.Run("truncated poll body is retried", func(t *testing.T) {
		authPath := filepath.Join(t.TempDir(), "auth.json")
		first := &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: &kimiFailingBody{}}
		responses := []*http.Response{first, kimiJSON(http.StatusOK, kimiDeviceSuccessBody)}
		err, _, polls := runKimiDevice(t, authPath, kimiDeviceStartBody("1", "60"), func(n int) *http.Response {
			return responses[n-1]
		}, context.Background(), nil, nil)
		if err != nil || polls.Load() != 2 {
			t.Fatalf("error=%v polls=%d", err, polls.Load())
		}
		if entry := readKimiGlobalEntry(t, authPath); entry["access"] != "kimi-access" {
			t.Fatalf("stored entry = %v", entry)
		}
	})

	t.Run("slow_down increases the next interval", func(t *testing.T) {
		authPath := filepath.Join(t.TempDir(), "auth.json")
		responses := []*http.Response{
			kimiJSON(http.StatusBadRequest, `{"error":"slow_down","interval":2}`),
			kimiJSON(http.StatusBadRequest, `{"error":"authorization_pending"}`),
			kimiJSON(http.StatusOK, kimiDeviceSuccessBody),
		}
		err, clock, polls := runKimiDevice(t, authPath, kimiDeviceStartBody("1", "120"), func(n int) *http.Response {
			return responses[n-1]
		}, context.Background(), nil, nil)
		if err != nil || polls.Load() != 3 {
			t.Fatalf("error=%v polls=%d", err, polls.Load())
		}
		waits := clock.recordedWaits()
		want := []time.Duration{time.Second, 6 * time.Second, 6 * time.Second}
		if fmt.Sprint(waits) != fmt.Sprint(want) {
			t.Fatalf("waits = %v, want %v", waits, want)
		}
	})

	t.Run("longer retry-after cannot extend the deadline", func(t *testing.T) {
		authPath := filepath.Join(t.TempDir(), "auth.json")
		first := kimiJSON(http.StatusTooManyRequests, `{"error":"rate_limited"}`)
		first.Header.Set("Retry-After", "60")
		err, _, polls := runKimiDevice(t, authPath, kimiDeviceStartBody("1", "10"), func(int) *http.Response {
			return first
		}, context.Background(), nil, nil)
		if !errors.Is(err, errKimiGlobalExpired) || polls.Load() != 1 {
			t.Fatalf("error=%v polls=%d, want expiry after one poll", err, polls.Load())
		}
	})

	t.Run("repeated transient failures hit the retry cap", func(t *testing.T) {
		authPath := filepath.Join(t.TempDir(), "auth.json")
		err, clock, polls := runKimiDevice(t, authPath, kimiDeviceStartBody("1", "60"), func(int) *http.Response {
			return kimiJSON(http.StatusInternalServerError, `{"error":"server_error"}`)
		}, context.Background(), nil, nil)
		if !errors.Is(err, errKimiGlobalTransient) || polls.Load() != 4 {
			t.Fatalf("error=%v polls=%d", err, polls.Load())
		}
		waits := clock.recordedWaits()
		want := []time.Duration{time.Second, time.Second, 2 * time.Second, 4 * time.Second}
		if fmt.Sprint(waits) != fmt.Sprint(want) {
			t.Fatalf("waits = %v, want %v", waits, want)
		}
	})

	t.Run("cancellation leaves the existing entry intact", func(t *testing.T) {
		authPath := filepath.Join(t.TempDir(), "auth.json")
		const existing = `{"kimi-coding-global":{"type":"oauth","access":"old-access","refresh":"old-refresh","expires":1800000000000},"other":{"keep":"me"}}`
		writeAuthDoc(t, authPath, existing)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		err, _, polls := runKimiDevice(t, authPath, kimiDeviceStartBody("1", "60"), func(int) *http.Response {
			t.Fatal("polled after cancellation")
			return nil
		}, ctx, func(waitCtx context.Context, _ time.Duration) error {
			cancel()
			return waitCtx.Err()
		}, nil)
		if !errors.Is(err, context.Canceled) || polls.Load() != 0 {
			t.Fatalf("error=%v polls=%d", err, polls.Load())
		}
		if got := readFileString(t, authPath); got != existing {
			t.Fatalf("cancelled login changed auth.json: %s", got)
		}
	})

	t.Run("selected entry replacement after start is refused", func(t *testing.T) {
		authPath := filepath.Join(t.TempDir(), "auth.json")
		writeAuthDoc(t, authPath, `{"kimi-coding-global":{"type":"oauth","access":"old-access","refresh":"old-refresh","expires":1800000000000}}`)
		replacement := `{"kimi-coding-global":{"type":"oauth","access":"replacement-access","refresh":"replacement-refresh","expires":1800000000001}}`
		err, _, polls := runKimiDevice(t, authPath, kimiDeviceStartBody("1", "60"), func(int) *http.Response {
			return kimiJSON(http.StatusOK, kimiDeviceSuccessBody)
		}, context.Background(), nil, func(string, string) {
			writeAuthDoc(t, authPath, replacement)
		})
		if !errors.Is(err, errKimiGlobalConflict) || polls.Load() != 1 {
			t.Fatalf("error=%v polls=%d", err, polls.Load())
		}
		if got := readFileString(t, authPath); got != replacement {
			t.Fatalf("conflicting login changed auth.json: %s", got)
		}
	})

	for _, tc := range []struct {
		name   string
		status int
		body   string
		want   error
	}{
		{name: "missing refresh", status: http.StatusOK, body: `{"access_token":"kimi-access","expires_in":3600}`, want: errKimiGlobalInvalid},
		{name: "missing access", status: http.StatusOK, body: `{"refresh_token":"kimi-refresh","expires_in":3600}`, want: errKimiGlobalInvalid},
		{name: "zero lifetime", status: http.StatusOK, body: `{"access_token":"kimi-access","refresh_token":"kimi-refresh","expires_in":0}`, want: errKimiGlobalInvalid},
		{name: "oversized success", status: http.StatusOK, body: strings.Repeat("x", kimiGlobalBodyLimit+1), want: errKimiGlobalInvalid},
		{name: "denied", status: http.StatusBadRequest, body: `{"error":"access_denied","error_description":"do not expose this"}`, want: errKimiGlobalDenied},
		{name: "expired token", status: http.StatusBadRequest, body: `{"error":"expired_token"}`, want: errKimiGlobalExpired},
	} {
		t.Run(tc.name, func(t *testing.T) {
			authPath := filepath.Join(t.TempDir(), "auth.json")
			err, _, polls := runKimiDevice(t, authPath, kimiDeviceStartBody("1", "60"), func(int) *http.Response {
				return kimiJSON(tc.status, tc.body)
			}, context.Background(), nil, nil)
			if !errors.Is(err, tc.want) || polls.Load() != 1 {
				t.Fatalf("error=%v polls=%d, want %v", err, polls.Load(), tc.want)
			}
			if strings.Contains(err.Error(), "do not expose") {
				t.Fatalf("error exposed the OAuth description: %v", err)
			}
			if _, statErr := os.Stat(authPath); !os.IsNotExist(statErr) {
				t.Fatalf("failed login created auth.json: %v", statErr)
			}
		})
	}

	t.Run("successful save merges an unrelated edit and keeps mode", func(t *testing.T) {
		authPath := filepath.Join(t.TempDir(), "auth.json")
		writeAuthDoc(t, authPath, `{"other":{"keep":"old"},"kimi-coding":{"type":"api_key","key":"china-key"}}`)
		if err := os.Chmod(authPath, 0o640); err != nil {
			t.Fatal(err)
		}
		var readyCodes []string
		err, _, polls := runKimiDevice(t, authPath, kimiDeviceStartBody("1", "60"), func(int) *http.Response {
			return kimiJSON(http.StatusOK, kimiDeviceSuccessBody)
		}, context.Background(), nil, func(code, _ string) {
			readyCodes = append(readyCodes, code)
			writeAuthDoc(t, authPath, `{"other":{"keep":"changed"},"kimi-coding":{"type":"api_key","key":"china-key"}}`)
			if chmodErr := os.Chmod(authPath, 0o640); chmodErr != nil {
				t.Fatal(chmodErr)
			}
		})
		if err != nil || polls.Load() != 1 || len(readyCodes) != 1 {
			t.Fatalf("error=%v polls=%d ready=%v", err, polls.Load(), readyCodes)
		}
		info, err := os.Stat(authPath)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o640 {
			t.Fatalf("auth.json mode = %o, want 640", info.Mode().Perm())
		}
		var doc map[string]map[string]any
		data := readFileString(t, authPath)
		if err := json.Unmarshal([]byte(data), &doc); err != nil {
			t.Fatal(err)
		}
		if doc["other"]["keep"] != "changed" || doc["kimi-coding"]["key"] != "china-key" {
			t.Fatalf("unrelated entries were not merged: %v", doc)
		}
		global := doc[providerregistry.KimiGlobalProviderID]
		if len(global) != 4 || global["type"] != "oauth" || global["access"] != "kimi-access" || global["refresh"] != "kimi-refresh" {
			t.Fatalf("stored entry = %v", global)
		}
		for _, transient := range []string{"KIMI-CODE", "kimi-device-secret"} {
			if strings.Contains(data, transient) {
				t.Fatalf("transient device value persisted: %s", data)
			}
		}
	})
}

func readKimiGlobalEntry(t *testing.T, path string) map[string]any {
	t.Helper()
	var doc map[string]map[string]any
	if err := json.Unmarshal([]byte(readFileString(t, path)), &doc); err != nil {
		t.Fatal(err)
	}
	return doc[providerregistry.KimiGlobalProviderID]
}

func readFileString(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func kimiGlobalTestModel() ModelMeta {
	return ModelMeta{
		ID: "kimi-for-coding", API: "openai-completions",
		Provider: providerregistry.KimiGlobalProviderID, BaseURL: KimiGlobalBaseURL,
		ContextWindow: 8192, MaxTokens: 1024,
	}
}

func kimiGlobalSSE(text string) string {
	return fmt.Sprintf(`data: {"id":"chatcmpl-kimi","model":"kimi-for-coding","choices":[{"index":0,"delta":{"content":%q}}]}

data: {"id":"chatcmpl-kimi","model":"kimi-for-coding","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}

data: [DONE]

`, text)
}

type kimiTestTransport struct {
	mu       sync.Mutex
	requests []string
	handle   func(*http.Request) *http.Response
}

func (t *kimiTestTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	t.mu.Lock()
	t.requests = append(t.requests, r.URL.String())
	t.mu.Unlock()
	if t.handle == nil {
		return nil, errors.New("unexpected Kimi request")
	}
	return t.handle(r), nil
}

func (t *kimiTestTransport) requestURLs() []string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]string(nil), t.requests...)
}

func newKimiGlobalAuth(t *testing.T, doc string) (*KimiGlobalAuth, string) {
	t.Helper()
	authPath := filepath.Join(t.TempDir(), "auth.json")
	if doc != "" {
		writeAuthDoc(t, authPath, doc)
	}
	return &KimiGlobalAuth{Path: authPath, now: fixedOpenAINow}, authPath
}

func TestKimiGlobalCompletionsAuthBoundary(t *testing.T) {
	fresh := fmt.Sprintf(`{"kimi-coding-global":{"type":"oauth","access":"kimi-bearer","refresh":"kimi-refresh","expires":%d},"kimi-coding":{"type":"api_key","key":"china-key"}}`, fixedOpenAINow().UnixMilli()+3600_000)
	expired := fmt.Sprintf(`{"kimi-coding-global":{"type":"oauth","access":"expired-bearer","refresh":"kimi-refresh","expires":%d},"kimi-coding":{"type":"api_key","key":"china-key"}}`, fixedOpenAINow().UnixMilli()-1000)

	t.Run("fresh OAuth sends the bearer to the exact route", func(t *testing.T) {
		auth, _ := newKimiGlobalAuth(t, fresh)
		transport := &kimiTestTransport{handle: func(r *http.Request) *http.Response {
			if r.URL.String() != kimiGlobalChatCompletionsURL {
				t.Fatalf("endpoint = %s", r.URL)
			}
			if r.Header.Get("Authorization") != "Bearer kimi-bearer" {
				t.Fatalf("Authorization = %q", r.Header.Get("Authorization"))
			}
			if r.Header.Get("User-Agent") != "psi/test" {
				t.Fatalf("User-Agent = %q", r.Header.Get("User-Agent"))
			}
			return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(kimiGlobalSSE("KIMI_OK")))}
		}}
		client := NewCompletionsClient(KimiGlobalBaseURL, "", 0, 0)
		client.HTTP = &http.Client{Transport: transport}
		client.ResolveKimiGlobalAuth = auth.Get
		client.UserAgent = "test"
		response, err := client.Send(context.Background(), Request{Model: kimiGlobalTestModel(), Thinking: "off"}, nil)
		if err != nil || response == nil || response.Text != "KIMI_OK" {
			t.Fatalf("response=%+v error=%v", response, err)
		}
	})

	for _, tc := range []struct {
		name    string
		baseURL string
		headers map[string]string
		doc     string
	}{
		{name: "changed endpoint", baseURL: "https://api.kimi.ai/coding/v1/other", doc: fresh},
		{name: "auth header override", baseURL: KimiGlobalBaseURL, headers: map[string]string{"authorization": "Bearer other"}, doc: fresh},
		{name: "Authorization case variant", baseURL: KimiGlobalBaseURL, headers: map[string]string{"Authorization": "Bearer other"}, doc: fresh},
		{name: "expired OAuth with a key available", baseURL: KimiGlobalBaseURL, doc: expired},
		{name: "removed OAuth with a key available", baseURL: KimiGlobalBaseURL, doc: `{"kimi-coding":{"type":"api_key","key":"china-key"}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			auth, _ := newKimiGlobalAuth(t, tc.doc)
			t.Setenv("KIMI_API_KEY", "environment-key")
			transport := &kimiTestTransport{}
			client := NewCompletionsClient(tc.baseURL, "", 0, 0)
			client.HTTP = &http.Client{Transport: transport}
			client.Headers = tc.headers
			client.ResolveKimiGlobalAuth = auth.Get
			_, err := client.Send(context.Background(), Request{Model: kimiGlobalTestModel(), Thinking: "off"}, nil)
			if err == nil {
				t.Fatal("request succeeded without a usable Global Kimi credential")
			}
			if len(transport.requestURLs()) != 0 {
				t.Fatalf("sent a request despite the refusal: %v", transport.requestURLs())
			}
			if strings.Contains(err.Error(), "kimi-bearer") || strings.Contains(err.Error(), "expired-bearer") || strings.Contains(err.Error(), "environment-key") {
				t.Fatalf("error exposed a secret: %v", err)
			}
			if tc.doc != "" && (strings.Contains(tc.name, "expired") || strings.Contains(tc.name, "removed")) && !strings.Contains(err.Error(), "/login kimi-coding-global") {
				t.Fatalf("credential failure lacks the login instruction: %v", err)
			}
			if strings.Contains(tc.name, "header") && !errors.Is(err, errKimiGlobalHeaders) {
				t.Fatalf("header refusal = %v", err)
			}
			if strings.Contains(tc.name, "endpoint") && !errors.Is(err, errKimiGlobalEndpoint) {
				t.Fatalf("endpoint refusal = %v", err)
			}
		})
	}

	t.Run("redirect never forwards the bearer", func(t *testing.T) {
		auth, _ := newKimiGlobalAuth(t, fresh)
		transport := &kimiTestTransport{handle: func(r *http.Request) *http.Response {
			if r.Header.Get("Authorization") != "Bearer kimi-bearer" {
				t.Fatalf("Authorization = %q", r.Header.Get("Authorization"))
			}
			response := &http.Response{StatusCode: http.StatusTemporaryRedirect, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(""))}
			response.Header.Set("Location", "https://api.kimi.ai/coding/v1/redirected")
			return response
		}}
		client := NewCompletionsClient(KimiGlobalBaseURL, "", 0, 0)
		client.HTTP = &http.Client{Transport: transport}
		client.ResolveKimiGlobalAuth = auth.Get
		client.UserAgent = "test"
		_, err := client.Send(context.Background(), Request{Model: kimiGlobalTestModel(), Thinking: "off"}, nil)
		if err == nil {
			t.Fatal("redirect was accepted")
		}
		if got := transport.requestURLs(); len(got) != 1 || got[0] != kimiGlobalChatCompletionsURL {
			t.Fatalf("requests = %v, want only the original route", got)
		}
		if strings.Contains(err.Error(), "kimi-bearer") {
			t.Fatalf("redirect error exposed the bearer: %v", err)
		}
	})

	t.Run("echoed token stays out of the error", func(t *testing.T) {
		auth, _ := newKimiGlobalAuth(t, fresh)
		transport := &kimiTestTransport{handle: func(r *http.Request) *http.Response {
			response := &http.Response{
				StatusCode: http.StatusTooManyRequests,
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader(`{"error":{"message":"rate limit for Bearer kimi-bearer"}}`)),
			}
			response.Header.Set("Retry-After", "2")
			return response
		}}
		client := NewCompletionsClient(KimiGlobalBaseURL, "", 0, 0)
		client.HTTP = &http.Client{Transport: transport}
		client.ResolveKimiGlobalAuth = auth.Get
		client.UserAgent = "test"
		_, err := client.Send(context.Background(), Request{Model: kimiGlobalTestModel(), Thinking: "off"}, nil)
		var providerErr *ProviderError
		if !errors.As(err, &providerErr) {
			t.Fatalf("error = %v, want ProviderError", err)
		}
		if providerErr.Kind != ErrRateLimit || providerErr.Status != http.StatusTooManyRequests || providerErr.RetryAfter != 2*time.Second {
			t.Fatalf("structured error = %+v", providerErr)
		}
		if providerErr.Detail != "" || providerErr.Reason != "" || providerErr.Message != "Too Many Requests" {
			t.Fatalf("provider detail escaped: %+v", providerErr)
		}
		if strings.Contains(err.Error(), "kimi-bearer") {
			t.Fatalf("error exposed the bearer: %v", err)
		}
	})

	t.Run("rejected bearer asks for a new login", func(t *testing.T) {
		auth, _ := newKimiGlobalAuth(t, fresh)
		transport := &kimiTestTransport{handle: func(r *http.Request) *http.Response {
			return &http.Response{StatusCode: http.StatusUnauthorized, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"error":"invalid_token"}`))}
		}}
		client := NewCompletionsClient(KimiGlobalBaseURL, "", 0, 0)
		client.HTTP = &http.Client{Transport: transport}
		client.ResolveKimiGlobalAuth = auth.Get
		client.UserAgent = "test"
		_, err := client.Send(context.Background(), Request{Model: kimiGlobalTestModel(), Thinking: "off"}, nil)
		var providerErr *ProviderError
		if !errors.As(err, &providerErr) || providerErr.Kind != ErrAuth || providerErr.Status != http.StatusUnauthorized {
			t.Fatalf("error = %v, want auth ProviderError", err)
		}
		if !strings.Contains(err.Error(), "/login kimi-coding-global") || strings.Contains(err.Error(), "kimi-bearer") {
			t.Fatalf("rejected bearer error = %v", err)
		}
	})
}

func TestPsiUserAgentIsHeaderSafe(t *testing.T) {
	for _, tc := range []struct {
		version, want string
	}{
		{"", "psi"},
		{"   ", "psi"},
		{"0.4.0", "psi/0.4.0"},
		{"0.4.0 beta", "psi"},
		{"0.4.0\nX-Injected: yes", "psi"},
		{"0.4.0/arm64", "psi/0.4.0/arm64"},
	} {
		if got := PsiUserAgent(tc.version); got != tc.want {
			t.Errorf("PsiUserAgent(%q) = %q, want %q", tc.version, got, tc.want)
		}
	}
}

func TestKimiGlobalRetryAfterBounds(t *testing.T) {
	for _, tc := range []struct {
		value string
		want  time.Duration
	}{
		{"", 0},
		{"2", 2 * time.Second},
		{"-1", 0},
		{"NaN", 0},
		{"bogus", 0},
		{"86401", kimiGlobalMaxLifetime},
		{"1e999", kimiGlobalMaxLifetime},
		{"-1e999", 0},
	} {
		if got := kimiGlobalRetryAfter(tc.value); got != tc.want {
			t.Errorf("kimiGlobalRetryAfter(%q) = %v, want %v", tc.value, got, tc.want)
		}
	}
}

func TestKimiGlobalDurationBounds(t *testing.T) {
	for _, tc := range []struct {
		name     string
		value    any
		fallback time.Duration
		positive bool
		want     time.Duration
		ok       bool
	}{
		{name: "absent lifetime", fallback: kimiGlobalDefaultLifetime, positive: true, want: kimiGlobalDefaultLifetime, ok: true},
		{name: "zero interval", value: json.Number("0"), fallback: kimiGlobalDefaultInterval, want: 0, ok: true},
		{name: "one second", value: json.Number("1"), fallback: time.Second, want: time.Second, ok: true},
		{name: "integral float", value: json.Number("1.0"), fallback: time.Second, want: time.Second, ok: true},
		{name: "fractional", value: json.Number("1.5"), fallback: time.Second, ok: false},
		{name: "negative", value: json.Number("-1"), fallback: time.Second, ok: false},
		{name: "string", value: "5", fallback: time.Second, ok: false},
		{name: "infinite", value: json.Number("1e999"), fallback: time.Second, ok: false},
		{name: "zero lifetime", value: json.Number("0"), fallback: kimiGlobalDefaultLifetime, positive: true, ok: false},
		{name: "capped lifetime", value: json.Number("999999"), fallback: kimiGlobalDefaultLifetime, positive: true, want: kimiGlobalMaxLifetime, ok: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := kimiGlobalDuration(tc.value, tc.fallback, tc.positive)
			if got != tc.want || ok != tc.ok {
				t.Fatalf("duration = %v,%v; want %v,%v", got, ok, tc.want, tc.ok)
			}
		})
	}
}
