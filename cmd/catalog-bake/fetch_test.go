package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

type failingReader struct {
	data []byte
}

func (r *failingReader) Read(p []byte) (int, error) {
	if len(r.data) > 0 {
		n := copy(p, r.data)
		r.data = r.data[n:]
		return n, nil
	}
	return 0, errors.New("connection reset by peer")
}

type unreadBody struct {
	t      *testing.T
	closed bool
}

func (b *unreadBody) Read([]byte) (int, error) {
	b.t.Error("non-200 response body was read")
	return 0, io.ErrUnexpectedEOF
}

func (b *unreadBody) Close() error {
	b.closed = true
	return nil
}

func testFetchConfig(client *http.Client, waits *[]time.Duration) fetchConfig {
	return fetchConfig{
		client: client,
		now:    func() time.Time { return time.Unix(1_700_000_000, 0).UTC() },
		wait: func(_ context.Context, delay time.Duration) error {
			*waits = append(*waits, delay)
			return nil
		},
	}
}

func response(status int, body string, headers map[string]string) *http.Response {
	header := http.Header{}
	for name, value := range headers {
		header.Set(name, value)
	}
	return &http.Response{
		StatusCode: status,
		Header:     header,
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

func TestFetchCatalog(t *testing.T) {
	const validBody = `{"data":[{"id":"synthetic/model"}]}`
	validTarget := fetchTarget{name: "source", url: "https://example.invalid/catalog", validate: validateOpenRouterBody}

	t.Run("retry after delta seconds", func(t *testing.T) {
		body := &unreadBody{t: t}
		requests := 0
		var waits []time.Duration
		client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			requests++
			if requests == 1 {
				return &http.Response{StatusCode: http.StatusTooManyRequests, Header: http.Header{"Retry-After": {"2"}}, Body: body}, nil
			}
			if !body.closed {
				t.Error("first response body was not closed before retry")
			}
			return response(http.StatusOK, validBody, nil), nil
		})}
		result, err := fetchTargets(context.Background(), testFetchConfig(client, &waits), []fetchTarget{validTarget})
		if err != nil {
			t.Fatal(err)
		}
		if requests != 2 {
			t.Fatalf("requests = %d, want 2", requests)
		}
		if len(waits) != 1 || waits[0] != 2*time.Second {
			t.Fatalf("waits = %v, want [2s]", waits)
		}
		if string(result["source"].body) != validBody {
			t.Fatal("successful body was not retained")
		}
	})

	t.Run("retry after http date", func(t *testing.T) {
		now := time.Unix(1_700_000_000, 0).UTC()
		requests := 0
		var waits []time.Duration
		client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			requests++
			if requests == 1 {
				return response(http.StatusServiceUnavailable, "", map[string]string{"Retry-After": now.Add(3 * time.Second).Format(http.TimeFormat)}), nil
			}
			return response(http.StatusOK, validBody, nil), nil
		})}
		cfg := testFetchConfig(client, &waits)
		cfg.now = func() time.Time { return now }
		if _, err := fetchTargets(context.Background(), cfg, []fetchTarget{validTarget}); err != nil {
			t.Fatal(err)
		}
		if len(waits) != 1 || waits[0] != 3*time.Second {
			t.Fatalf("waits = %v, want [3s]", waits)
		}
	})

	t.Run("interrupted response headers recover", func(t *testing.T) {
		requests := 0
		client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			requests++
			if requests == 1 {
				return nil, io.ErrUnexpectedEOF
			}
			return response(http.StatusOK, validBody, nil), nil
		})}
		var waits []time.Duration
		results, err := fetchTargets(context.Background(), testFetchConfig(client, &waits), []fetchTarget{validTarget})
		if err != nil || requests != 2 || string(results["source"].body) != validBody {
			t.Fatalf("expected one retry followed by the complete body; requests = %d, error = %v", requests, err)
		}
	})

	t.Run("interrupted read recovers", func(t *testing.T) {
		requests := 0
		var waits []time.Duration
		client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			requests++
			if requests == 1 {
				return &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(&failingReader{data: []byte("partial")})}, nil
			}
			return response(http.StatusOK, validBody, nil), nil
		})}
		result, err := fetchTargets(context.Background(), testFetchConfig(client, &waits), []fetchTarget{validTarget})
		if err != nil {
			t.Fatal(err)
		}
		if requests != 2 || string(result["source"].body) != validBody {
			t.Fatalf("requests = %d, body = %q", requests, result["source"].body)
		}
	})

	t.Run("transient errors exhaust", func(t *testing.T) {
		requests := 0
		var waits []time.Duration
		client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			requests++
			return response(http.StatusBadGateway, "", nil), nil
		})}
		_, err := fetchTargets(context.Background(), testFetchConfig(client, &waits), []fetchTarget{validTarget})
		if err == nil || !strings.Contains(err.Error(), "HTTP 502") {
			t.Fatalf("error = %v, want HTTP 502", err)
		}
		if requests != 3 {
			t.Fatalf("requests = %d, want 3", requests)
		}
		if len(waits) != 2 || waits[0] != 250*time.Millisecond || waits[1] != time.Second {
			t.Fatalf("waits = %v, want [250ms 1s]", waits)
		}
	})

	for _, header := range []string{"30", "9223372037", "999999999999999999999"} {
		t.Run("retry cap refuses "+header, func(t *testing.T) {
			requests := 0
			var waits []time.Duration
			client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				requests++
				return response(http.StatusTooManyRequests, "", map[string]string{"Retry-After": header}), nil
			})}
			_, err := fetchTargets(context.Background(), testFetchConfig(client, &waits), []fetchTarget{validTarget})
			if err == nil || !strings.Contains(err.Error(), "HTTP 429") || !strings.Contains(err.Error(), "10s cap") {
				t.Fatalf("error = %v, want HTTP 429 retry cap refusal", err)
			}
			if requests != 1 || len(waits) != 0 {
				t.Fatalf("requests = %d, waits = %v", requests, waits)
			}
		})
	}

	for _, tc := range []struct{ name, location, want string }{
		{"redirect", "https://example.invalid/other", "HTTP 302"},
		{"malformed redirect", ":", "request failed"},
	} {
		t.Run(tc.name+" is permanent", func(t *testing.T) {
			requests := 0
			client := newCatalogHTTPClient()
			client.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
				requests++
				return response(http.StatusFound, "", map[string]string{"Location": tc.location}), nil
			})
			var waits []time.Duration
			_, err := fetchTargets(context.Background(), testFetchConfig(client, &waits), []fetchTarget{validTarget})
			if err == nil || !strings.Contains(err.Error(), tc.want) || requests != 1 || len(waits) != 0 {
				t.Fatalf("error = %v, requests = %d, waits = %v; want %q with no retry", err, requests, waits, tc.want)
			}
		})
	}

	t.Run("permanent status is not retried", func(t *testing.T) {
		requests := 0
		client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			requests++
			return &http.Response{StatusCode: http.StatusNotFound, Body: &unreadBody{t: t}}, nil
		})}
		var waits []time.Duration
		_, err := fetchTargets(context.Background(), testFetchConfig(client, &waits), []fetchTarget{validTarget})
		if err == nil || !strings.Contains(err.Error(), "HTTP 404") {
			t.Fatalf("error = %v, want HTTP 404", err)
		}
		if requests != 1 || len(waits) != 0 {
			t.Fatalf("requests = %d, waits = %v", requests, waits)
		}
	})

	t.Run("oversize is not retried", func(t *testing.T) {
		requests := 0
		client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			requests++
			return response(http.StatusOK, string(bytes.Repeat([]byte("x"), maxInputBytes+1)), nil), nil
		})}
		var waits []time.Duration
		_, err := fetchTargets(context.Background(), testFetchConfig(client, &waits), []fetchTarget{validTarget})
		if err == nil || !strings.Contains(err.Error(), "exceeds") {
			t.Fatalf("error = %v, want oversize refusal", err)
		}
		if requests != 1 {
			t.Fatalf("requests = %d, want 1", requests)
		}
	})

	t.Run("complete invalid json is not retried", func(t *testing.T) {
		requests := 0
		client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			requests++
			return response(http.StatusOK, `{"data":`, nil), nil
		})}
		var waits []time.Duration
		_, err := fetchTargets(context.Background(), testFetchConfig(client, &waits), []fetchTarget{validTarget})
		if err == nil || !strings.Contains(err.Error(), "invalid JSON") {
			t.Fatalf("error = %v, want invalid JSON", err)
		}
		if requests != 1 || len(waits) != 0 {
			t.Fatalf("requests = %d, waits = %v", requests, waits)
		}
	})

	t.Run("cancellation stops during wait", func(t *testing.T) {
		requests := 0
		client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			requests++
			return response(http.StatusServiceUnavailable, "", nil), nil
		})}
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		waits := 0
		cfg := fetchConfig{
			client: client,
			now:    func() time.Time { return time.Unix(1_700_000_000, 0).UTC() },
			wait: func(ctx context.Context, _ time.Duration) error {
				waits++
				cancel()
				return ctx.Err()
			},
		}
		if _, err := fetchTargets(ctx, cfg, []fetchTarget{validTarget}); !errors.Is(err, context.Canceled) {
			t.Fatalf("error = %v, want context.Canceled", err)
		}
		if requests != 1 || waits != 1 {
			t.Fatalf("requests = %d, waits = %d, want 1 and 1", requests, waits)
		}
	})

	t.Run("second source failure yields no inputs", func(t *testing.T) {
		first := fetchTarget{name: "first", url: "https://example.invalid/first", validate: validateOpenRouterBody}
		second := fetchTarget{name: "second", url: "https://example.invalid/second", validate: validateOpenRouterBody}
		client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
			if strings.HasSuffix(request.URL.Path, "first") {
				return response(http.StatusOK, validBody, nil), nil
			}
			return response(http.StatusInternalServerError, "", nil), nil
		})}
		var waits []time.Duration
		result, err := fetchTargets(context.Background(), testFetchConfig(client, &waits), []fetchTarget{first, second})
		if err == nil || !strings.Contains(err.Error(), "second") {
			t.Fatalf("error = %v, want source-specific failure", err)
		}
		if result != nil {
			t.Fatalf("failed fetch returned inputs: %v", result)
		}
	})
}

func TestParseRetryAfter(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	cases := map[string]time.Duration{
		"":                          0,
		"0":                         0,
		"-5":                        0,
		"+2":                        0,
		"12":                        12 * time.Second,
		"not-a-number":              0,
		now.Format(http.TimeFormat): 0,
		now.Add(90 * time.Second).Format(http.TimeFormat): 90 * time.Second,
	}
	for value, want := range cases {
		if got := parseRetryAfter(value, now); got != want {
			t.Errorf("parseRetryAfter(%q) = %s, want %s", value, got, want)
		}
	}
}

func TestFetchErrorDoesNotIncludeBody(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return response(http.StatusBadRequest, "secret response text", nil), nil
	})}
	var waits []time.Duration
	_, err := fetchTargets(context.Background(), testFetchConfig(client, &waits), []fetchTarget{
		{name: "source", url: "https://example.invalid", validate: validateOpenRouterBody},
	})
	if err == nil {
		t.Fatal("expected an error")
	}
	if strings.Contains(err.Error(), "secret") {
		t.Fatalf("error leaked response body: %v", err)
	}
	if !strings.Contains(err.Error(), "source") {
		t.Fatalf("error lacks source name: %v", err)
	}
}
