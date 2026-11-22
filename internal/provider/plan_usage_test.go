package provider

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

const goUsageFixture = `{"usage":{"rolling":{"percent":12.5,"status":"ok","resetsAt":"2030-01-02T03:04:05Z"},"weekly":{"percent":40,"status":"ok","resetsAt":"2030-01-08T00:00:00Z"},"monthly":{"percent":99.5,"status":"rate-limited","resetsAt":"2030-02-01T00:00:00Z"}}}`

func TestParsePlanUsage(t *testing.T) {
	for _, tc := range []struct {
		name, provider, body string
		want                 [3]float64
		ok                   bool
	}{
		{"zai", "zai", `{"success":true,"data":{"limits":[{"type":"TIME_LIMIT","percentage":2},{"type":"TOKENS_LIMIT","percentage":27.5}]}}`, [3]float64{27.5}, true},
		{"go", "opencode-go", goUsageFixture, [3]float64{12.5, 40, 99.5}, true},
		{"zai unsuccessful", "zai", `{"success":false,"data":{"limits":[{"type":"TOKENS_LIMIT","percentage":5}]}}`, [3]float64{}, false},
		{"zai ambiguous", "zai", `{"data":{"limits":[{"type":"TOKENS_LIMIT","percentage":5},{"type":"TOKENS_LIMIT","percentage":6}]}}`, [3]float64{}, false},
		{"missing percentage", "zai", `{"data":{"limits":[{"type":"TOKENS_LIMIT"}]}}`, [3]float64{}, false},
		{"bad percentage", "opencode-go", strings.Replace(goUsageFixture, `"percent":40`, `"percent":101`, 1), [3]float64{}, false},
		{"missing monthly", "opencode-go", strings.Replace(goUsageFixture, `"monthly"`, `"other"`, 1), [3]float64{}, false},
		{"bad status", "opencode-go", strings.Replace(goUsageFixture, `"ok"`, `"unknown"`, 1), [3]float64{}, false},
		{"bad reset", "opencode-go", strings.Replace(goUsageFixture, `2030-01-02T03:04:05Z`, `never`, 1), [3]float64{}, false},
		{"duplicate key", "zai", `{"data":{"limits":[{"type":"TOKENS_LIMIT","percentage":5,"percentage":6}]}}`, [3]float64{}, false},
		{"trailing object", "opencode-go", goUsageFixture + ` {}`, [3]float64{}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := parsePlanUsage(tc.provider, []byte(tc.body))
			if ok != tc.ok || (ok && got != tc.want) {
				t.Fatalf("parse = %v/%t, want %v/%t", got, ok, tc.want, tc.ok)
			}
		})
	}
}

func TestPlanUsageRequest(t *testing.T) {
	for _, tc := range []struct {
		provider, endpoint, body, agent string
		want                            [3]float64
	}{
		{"zai", zaiUsageEndpoint, `{"data":{"limits":[{"type":"TOKENS_LIMIT","percentage":27.5}]}}`, "psi-quota", [3]float64{27.5}},
		{"opencode-go", openCodeGoUsageEndpoint, goUsageFixture, openCodeGoUsageAgent, [3]float64{12.5, 40, 99.5}},
	} {
		t.Run(tc.provider, func(t *testing.T) {
			transport := codexUsageTransportFunc(func(req *http.Request) (*http.Response, error) {
				if req.Method != http.MethodGet || req.URL.String() != tc.endpoint || req.Header.Get("Authorization") != "Bearer synthetic-key" || req.Header.Get("User-Agent") != tc.agent || req.Header.Get("Accept") != "application/json" {
					t.Fatalf("unexpected request %s %s", req.Method, req.URL)
				}
				return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(tc.body))}, nil
			})
			got := fetchPlanUsage(context.Background(), tc.provider, "synthetic-key", time.Time{}, newQuotaHTTPClient(transport))
			wantWindows := QuotaWindows{Rolling: tc.want[0], Weekly: tc.want[1], Monthly: tc.want[2]}
			if !got.Sample || got.Windows != wantWindows || got.UsedPercent != tc.want[0] {
				t.Fatalf("result = %+v", got)
			}
		})
	}
}

func TestPlanUsageFailsClosed(t *testing.T) {
	calls := 0
	transport := codexUsageTransportFunc(func(req *http.Request) (*http.Response, error) {
		calls++
		return &http.Response{StatusCode: 429, Header: http.Header{"Retry-After": {"120"}}, Body: io.NopCloser(strings.NewReader("private error"))}, nil
	})
	client := newQuotaHTTPClient(transport)
	for _, key := range []string{"", "bad\nkey"} {
		if got := fetchPlanUsage(context.Background(), "zai", key, time.Time{}, client); got.Outcome != QuotaUsageNoCredential {
			t.Fatalf("invalid key = %+v", got)
		}
	}
	if calls != 0 {
		t.Fatal("sent an invalid key")
	}
	got := fetchPlanUsage(context.Background(), "opencode-go", "synthetic", time.Time{}, client)
	if got.Sample || got.Status != 429 || got.RetryAt.Before(got.StartedAt.Add(120*time.Second)) {
		t.Fatalf("rate limit = %+v", got)
	}
}
