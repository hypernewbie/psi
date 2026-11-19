package provider

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func codexDeviceJSONResponse(status int, body string) *http.Response {
	return openAITokenResponse(status, body)
}

func readCodexDeviceRequest(t *testing.T, r *http.Request) map[string]string {
	t.Helper()
	data, err := io.ReadAll(r.Body)
	if err != nil {
		t.Fatal(err)
	}
	var payload map[string]string
	if err := json.Unmarshal(data, &payload); err != nil {
		t.Fatalf("request body %q: %v", data, err)
	}
	if r.Header.Get("Content-Type") != "application/json" || r.Header.Get("Accept") != "application/json" {
		t.Fatalf("request headers = %v", r.Header)
	}
	return payload
}

// The device path requests a code, polls immediately, accepts pending as 403,
// 404, a string error, and error.code, adds five seconds after slow_down, then
// exchanges once with the device redirect. The user code is only presented to
// the interactive view and never persisted. L1-L4.
func TestCodexLoginDevice(t *testing.T) {
	authPath := filepath.Join(t.TempDir(), "auth.json")
	access := codexLoginTestAccess("acct-device")
	var starts, polls, exchanges atomic.Int32
	var startBody, pollBody map[string]string
	var gotForm url.Values
	pollIndex := 0
	transport := &openAIFakeToken{handler: func(r *http.Request) (*http.Response, error) {
		switch r.URL.String() {
		case codexDeviceUserCodeURL:
			starts.Add(1)
			startBody = readCodexDeviceRequest(t, r)
			return codexDeviceJSONResponse(http.StatusOK, `{"user_code":"ABCD-EFGH","device_auth_id":"device-1","interval":"0"}`), nil
		case codexDeviceTokenURL:
			polls.Add(1)
			pollBody = readCodexDeviceRequest(t, r)
			pollIndex++
			switch pollIndex {
			case 1:
				// Pending depends only on the status, even if a proxy
				// serves an oversized error page.
				return codexDeviceJSONResponse(http.StatusForbidden, strings.Repeat("x", codexDeviceBodyLimit+1)), nil
			case 2:
				return codexDeviceJSONResponse(http.StatusNotFound, ""), nil
			case 3:
				return codexDeviceJSONResponse(http.StatusTooManyRequests, `{"error":"slow_down"}`), nil
			case 4:
				return codexDeviceJSONResponse(http.StatusBadRequest, `{"error":{"code":"deviceauth_authorization_pending"}}`), nil
			default:
				return codexDeviceJSONResponse(http.StatusOK, `{"authorization_code":"device-code","code_verifier":"device-verifier"}`), nil
			}
		case codexTokenURL:
			exchanges.Add(1)
			body, _ := io.ReadAll(r.Body)
			form, err := url.ParseQuery(string(body))
			if err != nil {
				return nil, err
			}
			gotForm = form
			return openAITokenResponse(http.StatusOK, codexLoginTestTokenBody(access, "device-refresh")), nil
		default:
			return nil, errors.New("unexpected URL " + r.URL.String())
		}
	}}
	var waits []time.Duration
	login := &CodexDeviceLogin{
		Path: authPath,
		HTTP: &http.Client{Transport: transport},
		now:  fixedOpenAINow,
		Wait: func(_ context.Context, delay time.Duration) error {
			waits = append(waits, delay)
			return nil
		},
	}
	var readyCode, readyURL string
	err := login.Run(context.Background(), func(userCode, verificationURL string) {
		readyCode, readyURL = userCode, verificationURL
	})
	if err != nil {
		t.Fatalf("device login: %v", err)
	}
	if starts.Load() != 1 || polls.Load() != 5 || exchanges.Load() != 1 {
		t.Fatalf("starts=%d polls=%d exchanges=%d", starts.Load(), polls.Load(), exchanges.Load())
	}
	if readyCode != "ABCD-EFGH" || readyURL != codexDeviceVerifyURL {
		t.Fatalf("ready = %q %q", readyCode, readyURL)
	}
	if startBody["client_id"] != codexClientID {
		t.Fatalf("start body = %v", startBody)
	}
	if pollBody["device_auth_id"] != "device-1" || pollBody["user_code"] != "ABCD-EFGH" {
		t.Fatalf("poll body = %v", pollBody)
	}
	// slow_down adds five seconds to the one-second floor, not to a
	// jittered wait. Each scheduled wait must add up to 20% jitter.
	minWaits := []time.Duration{time.Second, time.Second, 6 * time.Second, 6 * time.Second}
	if len(waits) != len(minWaits) {
		t.Fatalf("waits = %v, want %d waits", waits, len(minWaits))
	}
	for i, floor := range minWaits {
		if ceiling := floor + floor/5; waits[i] <= floor || waits[i] > ceiling {
			t.Fatalf("wait %d = %v, want (%v, %v]", i, waits[i], floor, ceiling)
		}
	}
	if gotForm.Get("grant_type") != "authorization_code" || gotForm.Get("client_id") != codexClientID ||
		gotForm.Get("code") != "device-code" || gotForm.Get("code_verifier") != "device-verifier" ||
		gotForm.Get("redirect_uri") != codexDeviceRedirectURI {
		t.Fatalf("exchange form = %v", gotForm)
	}
	entry := readCodexLoginEntry(t, authPath)
	if len(entry) != 5 || entry["type"] != "oauth" || entry["access"] != access ||
		entry["refresh"] != "device-refresh" || entry["accountId"] != "acct-device" {
		t.Fatalf("stored entry = %v", entry)
	}
	data, err := os.ReadFile(authPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, transient := range []string{"ABCD-EFGH", "device-code", "device-verifier", "device-1"} {
		if strings.Contains(string(data), transient) {
			t.Fatalf("transient device secret persisted in %s", data)
		}
	}
}

// A start failure stops before polling, exchange, or write.
func TestCodexLoginDeviceStartFailures(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
		want   error
	}{
		{name: "unavailable", status: http.StatusNotFound, body: "", want: errCodexDeviceUnavailable},
		{name: "other status", status: http.StatusBadGateway, body: "", want: errCodexDeviceNetwork},
		{name: "rate limited", status: http.StatusTooManyRequests, body: strings.Repeat("x", codexDeviceBodyLimit+1), want: errCodexDeviceRateLimited},
		{name: "malformed", status: http.StatusOK, body: `{"user_code":`, want: errCodexDeviceInvalid},
		{name: "missing user code", status: http.StatusOK, body: `{"device_auth_id":"d","interval":1}`, want: errCodexDeviceInvalid},
		{name: "missing device id", status: http.StatusOK, body: `{"user_code":"u","interval":1}`, want: errCodexDeviceInvalid},
		{name: "missing interval", status: http.StatusOK, body: `{"user_code":"u","device_auth_id":"d"}`, want: errCodexDeviceInvalid},
		{name: "negative interval", status: http.StatusOK, body: `{"user_code":"u","device_auth_id":"d","interval":-1}`, want: errCodexDeviceInvalid},
		{name: "bad interval string", status: http.StatusOK, body: `{"user_code":"u","device_auth_id":"d","interval":"soon"}`, want: errCodexDeviceInvalid},
		{name: "infinite interval", status: http.StatusOK, body: `{"user_code":"u","device_auth_id":"d","interval":1e999}`, want: errCodexDeviceInvalid},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			authPath := filepath.Join(t.TempDir(), "auth.json")
			var polls, exchanges atomic.Int32
			login := &CodexDeviceLogin{
				Path: authPath,
				HTTP: &http.Client{Transport: &openAIFakeToken{handler: func(r *http.Request) (*http.Response, error) {
					switch r.URL.String() {
					case codexDeviceUserCodeURL:
						return codexDeviceJSONResponse(tc.status, tc.body), nil
					case codexDeviceTokenURL:
						polls.Add(1)
					case codexTokenURL:
						exchanges.Add(1)
					}
					return nil, errors.New("unexpected request")
				}}},
				now: fixedOpenAINow,
			}
			ready := false
			err := login.Run(context.Background(), func(string, string) { ready = true })
			if !errors.Is(err, tc.want) {
				t.Fatalf("error = %v, want %v", err, tc.want)
			}
			if ready || polls.Load() != 0 || exchanges.Load() != 0 {
				t.Fatalf("ready=%v polls=%d exchanges=%d", ready, polls.Load(), exchanges.Load())
			}
			if _, statErr := os.Stat(authPath); !os.IsNotExist(statErr) {
				t.Fatalf("auth file exists after failed start: %v", statErr)
			}
		})
	}
}

// Unexpected poll statuses, redirects, and malformed successes stop without
// exchange or write. A 429 without a usable poll code reports rate limiting.
func TestCodexLoginDevicePollFailures(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
		want   error
	}{
		{name: "unknown status", status: http.StatusBadGateway, body: "", want: errCodexDeviceInvalid},
		{name: "redirect with pending body", status: http.StatusTemporaryRedirect, body: `{"error":"deviceauth_authorization_pending"}`, want: errCodexDeviceInvalid},
		{name: "unexpected success with slow down body", status: http.StatusAccepted, body: `{"error":"slow_down"}`, want: errCodexDeviceInvalid},
		{name: "rate limited", status: http.StatusTooManyRequests, body: `{"error":"other"}`, want: errCodexDeviceRateLimited},
		{name: "malformed success", status: http.StatusOK, body: `{"authorization_code":`, want: errCodexDeviceInvalid},
		{name: "missing code", status: http.StatusOK, body: `{"code_verifier":"v"}`, want: errCodexDeviceInvalid},
		{name: "missing verifier", status: http.StatusOK, body: `{"authorization_code":"c"}`, want: errCodexDeviceInvalid},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			authPath := filepath.Join(t.TempDir(), "auth.json")
			var polls, exchanges atomic.Int32
			login := &CodexDeviceLogin{
				Path: authPath,
				HTTP: &http.Client{Transport: &openAIFakeToken{handler: func(r *http.Request) (*http.Response, error) {
					switch r.URL.String() {
					case codexDeviceUserCodeURL:
						return codexDeviceJSONResponse(http.StatusOK, `{"user_code":"u","device_auth_id":"d","interval":1}`), nil
					case codexDeviceTokenURL:
						if polls.Add(1) != 1 {
							t.Fatal("retried an ambiguous poll response")
						}
						resp := codexDeviceJSONResponse(tc.status, tc.body)
						if tc.status == http.StatusTemporaryRedirect {
							resp.Header.Set("Location", "https://redirect.example/poll")
						}
						return resp, nil
					case "https://redirect.example/poll":
						t.Fatal("forwarded the device poll to a redirect target")
					case codexTokenURL:
						exchanges.Add(1)
					}
					return nil, errors.New("unexpected request")
				}}},
				now:  fixedOpenAINow,
				Wait: func(context.Context, time.Duration) error { return nil },
			}
			err := login.Run(context.Background(), func(string, string) {})
			if !errors.Is(err, tc.want) {
				t.Fatalf("error = %v, want %v", err, tc.want)
			}
			if polls.Load() != 1 || exchanges.Load() != 0 {
				t.Fatalf("polls=%d exchanges=%d", polls.Load(), exchanges.Load())
			}
			if _, statErr := os.Stat(authPath); !os.IsNotExist(statErr) {
				t.Fatalf("auth file exists after failed poll: %v", statErr)
			}
		})
	}
}

// Cancellation during a poll wait stops before another poll. A response that
// arrives after the poll deadline is not exchanged.
func TestCodexLoginDeviceCancelAndExpiry(t *testing.T) {
	t.Run("cancel before showing code", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		login := &CodexDeviceLogin{
			Path: filepath.Join(t.TempDir(), "auth.json"),
			HTTP: &http.Client{Transport: &openAIFakeToken{handler: func(r *http.Request) (*http.Response, error) {
				if r.URL.String() != codexDeviceUserCodeURL {
					t.Fatal("requested another endpoint after cancel")
				}
				cancel()
				return codexDeviceJSONResponse(http.StatusOK, `{"user_code":"u","device_auth_id":"d","interval":1}`), nil
			}}},
		}
		err := login.Run(ctx, func(string, string) { t.Fatal("displayed a device code after cancel") })
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("error = %v, want context.Canceled", err)
		}
	})

	t.Run("cancel during wait", func(t *testing.T) {
		authPath := filepath.Join(t.TempDir(), "auth.json")
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		var polls, exchanges atomic.Int32
		login := &CodexDeviceLogin{
			Path: authPath,
			HTTP: &http.Client{Transport: &openAIFakeToken{handler: func(r *http.Request) (*http.Response, error) {
				switch r.URL.String() {
				case codexDeviceUserCodeURL:
					return codexDeviceJSONResponse(http.StatusOK, `{"user_code":"u","device_auth_id":"d","interval":5}`), nil
				case codexDeviceTokenURL:
					polls.Add(1)
					return codexDeviceJSONResponse(http.StatusForbidden, ""), nil
				case codexTokenURL:
					exchanges.Add(1)
				}
				return nil, errors.New("unexpected request")
			}}},
			now: fixedOpenAINow,
			Wait: func(context.Context, time.Duration) error {
				cancel()
				return context.Canceled
			},
		}
		err := login.Run(ctx, func(string, string) {})
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("error = %v", err)
		}
		if polls.Load() != 1 || exchanges.Load() != 0 {
			t.Fatalf("polls=%d exchanges=%d", polls.Load(), exchanges.Load())
		}
		if _, statErr := os.Stat(authPath); !os.IsNotExist(statErr) {
			t.Fatalf("auth file exists after cancel: %v", statErr)
		}
	})

	t.Run("large interval and slow down stop at expiry", func(t *testing.T) {
		authPath := filepath.Join(t.TempDir(), "auth.json")
		now := fixedOpenAINow()
		var polls, exchanges atomic.Int32
		var waits []time.Duration
		login := &CodexDeviceLogin{
			Path: authPath,
			HTTP: &http.Client{Transport: &openAIFakeToken{handler: func(r *http.Request) (*http.Response, error) {
				switch r.URL.String() {
				case codexDeviceUserCodeURL:
					return codexDeviceJSONResponse(http.StatusOK, `{"user_code":"u","device_auth_id":"d","interval":1e30}`), nil
				case codexDeviceTokenURL:
					polls.Add(1)
					return codexDeviceJSONResponse(http.StatusTooManyRequests, `{"error":"slow_down"}`), nil
				case codexTokenURL:
					exchanges.Add(1)
				}
				return nil, errors.New("unexpected request")
			}}},
			now: func() time.Time { return now },
			Wait: func(_ context.Context, delay time.Duration) error {
				waits = append(waits, delay)
				now = now.Add(delay)
				return nil
			},
		}
		err := login.Run(context.Background(), func(string, string) {})
		if !errors.Is(err, errCodexDeviceExpired) {
			t.Fatalf("error = %v", err)
		}
		if polls.Load() != 1 || exchanges.Load() != 0 {
			t.Fatalf("polls=%d exchanges=%d", polls.Load(), exchanges.Load())
		}
		if len(waits) != 1 || waits[0] != 900*time.Second {
			t.Fatalf("waits = %v", waits)
		}
		if _, statErr := os.Stat(authPath); !os.IsNotExist(statErr) {
			t.Fatalf("auth file exists after expiry: %v", statErr)
		}
	})

	for _, tc := range []struct {
		name string
		err  error
	}{
		{name: "success after deadline"},
		{name: "request timeout at deadline", err: context.DeadlineExceeded},
	} {
		t.Run(tc.name, func(t *testing.T) {
			authPath := filepath.Join(t.TempDir(), "auth.json")
			now := fixedOpenAINow()
			var polls, exchanges atomic.Int32
			login := &CodexDeviceLogin{
				Path: authPath,
				HTTP: &http.Client{Transport: &openAIFakeToken{handler: func(r *http.Request) (*http.Response, error) {
					switch r.URL.String() {
					case codexDeviceUserCodeURL:
						return codexDeviceJSONResponse(http.StatusOK, `{"user_code":"u","device_auth_id":"d","interval":1}`), nil
					case codexDeviceTokenURL:
						polls.Add(1)
						now = now.Add(codexDevicePollTimeout)
						if tc.err != nil {
							return nil, tc.err
						}
						return codexDeviceJSONResponse(http.StatusOK, `{"authorization_code":"c","code_verifier":"v"}`), nil
					case codexTokenURL:
						exchanges.Add(1)
					}
					return nil, errors.New("unexpected request")
				}}},
				now: func() time.Time { return now },
			}
			err := login.Run(context.Background(), func(string, string) {})
			if !errors.Is(err, errCodexDeviceExpired) {
				t.Fatalf("error = %v", err)
			}
			if polls.Load() != 1 || exchanges.Load() != 0 {
				t.Fatalf("polls=%d exchanges=%d", polls.Load(), exchanges.Load())
			}
			if _, statErr := os.Stat(authPath); !os.IsNotExist(statErr) {
				t.Fatalf("auth file exists after expiry: %v", statErr)
			}
		})
	}
}

// On failure a RoundTripper may still own the request body until it closes it.
func TestCodexDeviceRequestBodyLifetime(t *testing.T) {
	var body io.ReadCloser
	client := &http.Client{Transport: &openAIFakeToken{handler: func(r *http.Request) (*http.Response, error) {
		body = r.Body
		return nil, errors.New("synthetic device transport failure")
	}}}
	_, _, err := doCodexDeviceJSON(context.Background(), client, codexDeviceTokenURL, map[string]string{"user_code": "synthetic-code"})
	if !errors.Is(err, errCodexDeviceNetwork) || body == nil {
		t.Fatalf("request error = %v, want network failure with a transport-owned body", err)
	}
	defer body.Close()
	var payload map[string]string
	if err := json.NewDecoder(body).Decode(&payload); err != nil || payload["user_code"] != "synthetic-code" {
		t.Fatalf("transport-owned request body changed before close: %v", err)
	}
}

// Each poll request ends at the earlier of its own HTTP timeout and the poll
// deadline. A slow response leaves less than two seconds for the next wait
// and request.
func TestCodexLoginDevicePollDeadlineCapsRequest(t *testing.T) {
	authPath := filepath.Join(t.TempDir(), "auth.json")
	now := fixedOpenAINow()
	var polls, exchanges atomic.Int32
	var sawDeadline bool
	login := &CodexDeviceLogin{
		Path: authPath,
		HTTP: &http.Client{Transport: &openAIFakeToken{handler: func(r *http.Request) (*http.Response, error) {
			switch r.URL.String() {
			case codexDeviceUserCodeURL:
				return codexDeviceJSONResponse(http.StatusOK, `{"user_code":"u","device_auth_id":"d","interval":1}`), nil
			case codexDeviceTokenURL:
				if polls.Add(1) == 1 {
					now = now.Add(codexDevicePollTimeout - 2*time.Second)
					return codexDeviceJSONResponse(http.StatusForbidden, ""), nil
				}
				if deadline, ok := r.Context().Deadline(); ok {
					remaining := deadline.Sub(now)
					sawDeadline = remaining > 0 && remaining <= time.Second
				}
				return codexDeviceJSONResponse(http.StatusOK, `{"authorization_code":"c","code_verifier":"v"}`), nil
			case codexTokenURL:
				exchanges.Add(1)
				return openAITokenResponse(http.StatusOK, codexLoginTestTokenBody(codexLoginTestAccess("acct"), "refresh")), nil
			}
			return nil, errors.New("unexpected request")
		}}},
		now: func() time.Time { return now },
		Wait: func(_ context.Context, delay time.Duration) error {
			now = now.Add(delay)
			return nil
		},
	}
	if err := login.Run(context.Background(), func(string, string) {}); err != nil {
		t.Fatalf("device login: %v", err)
	}
	if polls.Load() != 2 || exchanges.Load() != 1 || !sawDeadline {
		t.Fatalf("polls=%d exchanges=%d sawDeadline=%v", polls.Load(), exchanges.Load(), sawDeadline)
	}
}
