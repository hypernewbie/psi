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

// zaiFiveHourFixture lists the weekly window first, then an unrelated
// TIME_LIMIT with the same duration, then the five-hour credit window. Row
// order must not decide the reading.
const zaiFiveHourFixture = `{"success":true,"data":{"limits":[{"type":"CREDIT_LIMIT","unit":6,"number":1,"percentage":40},{"type":"TIME_LIMIT","unit":3,"number":5,"percentage":1},{"type":"CREDIT_LIMIT","unit":3,"number":5,"percentage":27.5}]}}`

const zaiFiveHourTokensFixture = `{"success":true,"data":{"limits":[{"type":"CREDIT_LIMIT","unit":6,"number":1,"percentage":55},{"type":"TOKENS_LIMIT","unit":3,"number":5,"percentage":12.25}]}}`

func TestParsePlanUsage(t *testing.T) {
	for _, tc := range []struct {
		name, provider, body string
		want                 [3]float64
		ok                   bool
	}{
		{"zai five-hour credit", "zai", zaiFiveHourFixture, [3]float64{27.5}, true},
		{"zai five-hour tokens", "zai", zaiFiveHourTokensFixture, [3]float64{12.25}, true},
		{"go", "opencode-go", goUsageFixture, [3]float64{12.5, 40, 99.5}, true},
		{"zai unsuccessful", "zai", `{"success":false,"data":{"limits":[{"type":"TOKENS_LIMIT","unit":3,"number":5,"percentage":5}]}}`, [3]float64{}, false},
		{"zai missing duration", "zai", `{"data":{"limits":[{"type":"TOKENS_LIMIT","percentage":5}]}}`, [3]float64{}, false},
		{"zai weekly only", "zai", `{"data":{"limits":[{"type":"TOKENS_LIMIT","unit":6,"number":1,"percentage":5}]}}`, [3]float64{}, false},
		{"zai ambiguous", "zai", `{"data":{"limits":[{"type":"TOKENS_LIMIT","unit":3,"number":5,"percentage":5},{"type":"CREDIT_LIMIT","unit":3,"number":5,"percentage":6}]}}`, [3]float64{}, false},
		{"zai duplicate with unusable percentage", "zai", `{"data":{"limits":[{"type":"TOKENS_LIMIT","unit":3,"number":5},{"type":"CREDIT_LIMIT","unit":3,"number":5,"percentage":6}]}}`, [3]float64{}, false},
		{"zai missing percentage", "zai", `{"data":{"limits":[{"type":"TOKENS_LIMIT","unit":3,"number":5}]}}`, [3]float64{}, false},
		{"zai out-of-range percentage", "zai", `{"data":{"limits":[{"type":"TOKENS_LIMIT","unit":3,"number":5,"percentage":101}]}}`, [3]float64{}, false},
		{"bad percentage", "opencode-go", strings.Replace(goUsageFixture, `"percent":40`, `"percent":101`, 1), [3]float64{}, false},
		{"missing monthly", "opencode-go", strings.Replace(goUsageFixture, `"monthly"`, `"other"`, 1), [3]float64{}, false},
		{"bad status", "opencode-go", strings.Replace(goUsageFixture, `"ok"`, `"unknown"`, 1), [3]float64{}, false},
		{"bad reset", "opencode-go", strings.Replace(goUsageFixture, `2030-01-02T03:04:05Z`, `never`, 1), [3]float64{}, false},
		{"duplicate key", "zai", `{"data":{"limits":[{"type":"TOKENS_LIMIT","unit":3,"number":5,"percentage":5,"percentage":6}]}}`, [3]float64{}, false},
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

func TestParseZaiPlanWindows(t *testing.T) {
	percent := func(used float64) *float64 { return &used }
	for _, tc := range []struct {
		name             string
		body             string
		fiveHour, weekly *float64
		fiveHourSample   bool
	}{
		{
			name:           "both windows in any order",
			body:           `{"data":{"limits":[{"type":"CREDIT_LIMIT","unit":6,"number":1,"percentage":40},{"type":"TIME_LIMIT","unit":3,"number":5,"percentage":1},{"type":"CREDIT_LIMIT","unit":3,"number":5,"percentage":27.5}]}}`,
			fiveHour:       percent(27.5),
			weekly:         percent(40),
			fiveHourSample: true,
		},
		{
			name:           "zero usage is a reading",
			body:           `{"data":{"limits":[{"type":"CREDIT_LIMIT","unit":3,"number":5,"percentage":0},{"type":"CREDIT_LIMIT","unit":6,"number":1,"percentage":0}]}}`,
			fiveHour:       percent(0),
			weekly:         percent(0),
			fiveHourSample: true,
		},
		{
			name:   "weekly only stays panel data",
			body:   `{"data":{"limits":[{"type":"CREDIT_LIMIT","unit":6,"number":1,"percentage":40}]}}`,
			weekly: percent(40),
		},
		{
			name:           "duplicate weekly does not hide five hours",
			body:           `{"data":{"limits":[{"type":"CREDIT_LIMIT","unit":6,"number":1,"percentage":40},{"type":"CREDIT_LIMIT","unit":6,"number":1},{"type":"CREDIT_LIMIT","unit":3,"number":5,"percentage":27.5}]}}`,
			fiveHour:       percent(27.5),
			fiveHourSample: true,
		},
		{
			name:           "unrepresentable weekly percentage does not hide five hours",
			body:           `{"data":{"limits":[{"type":"CREDIT_LIMIT","unit":6,"number":1,"percentage":1e999},{"type":"CREDIT_LIMIT","unit":3,"number":5,"percentage":27.5}]}}`,
			fiveHour:       percent(27.5),
			fiveHourSample: true,
		},
		{
			name:   "duplicate five hours does not hide weekly",
			body:   `{"data":{"limits":[{"type":"CREDIT_LIMIT","unit":6,"number":1,"percentage":40},{"type":"TOKENS_LIMIT","unit":3,"number":5,"percentage":1},{"type":"CREDIT_LIMIT","unit":3,"number":5,"percentage":2}]}}`,
			weekly: percent(40),
		},
		{
			name: "token weekly and other lengths are ignored",
			body: `{"data":{"limits":[{"type":"TOKENS_LIMIT","unit":6,"number":1,"percentage":40},{"type":"CREDIT_LIMIT","unit":6,"number":5,"percentage":40}]}}`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sample := parsePlanSample("zai", []byte(tc.body))
			if sample.ok != tc.fiveHourSample {
				t.Fatalf("five-hour sample = %t, want %t", sample.ok, tc.fiveHourSample)
			}
			if sample.plan == nil {
				t.Fatal("no plan windows")
			}
			if !samePercent(sample.plan.FiveHour, tc.fiveHour) {
				t.Fatalf("five-hour = %v, want %v", sample.plan.FiveHour, tc.fiveHour)
			}
			if !samePercent(sample.plan.Weekly, tc.weekly) {
				t.Fatalf("weekly = %v, want %v", sample.plan.Weekly, tc.weekly)
			}
		})
	}
}

// samePercent compares presence and value so a missing window never looks
// like a zero reading.
func samePercent(got, want *float64) bool {
	if got == nil || want == nil {
		return got == nil && want == nil
	}
	return *got == *want
}

func TestPlanUsageRequest(t *testing.T) {
	for _, tc := range []struct {
		provider, endpoint, body, agent string
		want                            [3]float64
	}{
		{"zai", zaiUsageEndpoint, zaiFiveHourFixture, "psi-quota", [3]float64{27.5}},
		{"opencode-go", openCodeGoUsageEndpoint, goUsageFixture, openCodeGoUsageAgent, [3]float64{12.5, 40, 99.5}},
	} {
		t.Run(tc.provider, func(t *testing.T) {
			transport := codexUsageTransportFunc(func(req *http.Request) (*http.Response, error) {
				if req.Method != http.MethodGet || req.URL.String() != tc.endpoint || req.Header.Get("Authorization") != "Bearer synthetic-key" || req.Header.Get("User-Agent") != tc.agent || req.Header.Get("Accept") != "application/json" {
					t.Fatalf("unexpected request %s %s", req.Method, req.URL)
				}
				return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(tc.body))}, nil
			})
			got := fetchPlanUsage(context.Background(), tc.provider, "synthetic-key", time.Time{}, newQuotaHTTPClient(transport), readRejectedQuotaBody)
			wantWindows := QuotaWindows{Rolling: tc.want[0], Weekly: tc.want[1], Monthly: tc.want[2]}
			if !got.Sample || got.Windows != wantWindows || got.UsedPercent != tc.want[0] {
				t.Fatalf("result = %+v", got)
			}
			if tc.provider == "zai" && (got.Plan == nil || got.Plan.FiveHour == nil || *got.Plan.FiveHour != tc.want[0] || got.Plan.Weekly == nil || *got.Plan.Weekly != 40) {
				t.Fatalf("plan windows = %+v", got.Plan)
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
		if got := fetchPlanUsage(context.Background(), "zai", key, time.Time{}, client, readRejectedQuotaBody); got.Outcome != QuotaUsageNoCredential {
			t.Fatalf("invalid key = %+v", got)
		}
	}
	if calls != 0 {
		t.Fatal("sent an invalid key")
	}
	got := fetchPlanUsage(context.Background(), "opencode-go", "synthetic", time.Time{}, client, readRejectedQuotaBody)
	if got.Sample || got.Status != 429 || got.RetryAt.Before(got.StartedAt.Add(120*time.Second)) {
		t.Fatalf("rate limit = %+v", got)
	}
}
