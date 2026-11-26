package provider

import (
	"context"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestDeepSeekUsage(t *testing.T) {
	available := true
	unavailable := false
	for _, tc := range []struct {
		name string
		body string
		want *DeepSeekBalance
	}{
		{
			name: "currencies and independent amounts",
			body: `{"is_available":true,"balance_infos":[null,{"currency":"CNY","total_balance":"0","granted_balance":false,"topped_up_balance":"0.0"},{"currency":"USD","total_balance":"009.995","granted_balance":"-0.125","topped_up_balance":"1e2"},{"currency":"ABC","total_balance":"999999999999999999999.995"}]}`,
			want: &DeepSeekBalance{Available: &available, Balances: []DeepSeekCurrencyBalance{
				{Currency: "CNY", Total: "0.00", ToppedUp: "0.00"}, {Currency: "USD", Total: "10.00", Granted: "-0.13"},
			}},
		},
		{
			name: "explicit unavailable beside a positive total",
			body: `{"is_available":false,"balance_infos":[{"currency":"USD","total_balance":"20"}]}`,
			want: &DeepSeekBalance{Available: &unavailable, Balances: []DeepSeekCurrencyBalance{{Currency: "USD", Total: "20.00"}}},
		},
		{name: "unavailable without amounts", body: `{"is_available":false,"balance_infos":{}}`, want: &DeepSeekBalance{Available: &unavailable}},
		{name: "invalid amounts and availability are not zero or false", body: `{"is_available":"false","balance_infos":[{"currency":"USD","total_balance":0},{"currency":"USD","total_balance":" 0"},{"currency":"usd","total_balance":"0"}]}`},
		{name: "field names are exact", body: `{"IS_AVAILABLE":false,"balance_infos":[{"currency":"USD","Total_balance":"20"}]}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := newUsageHTTPClient(codexUsageTransportFunc(func(req *http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"application/json; charset=utf-8"}}, Body: io.NopCloser(strings.NewReader(tc.body))}, nil
			}))
			got := fetchDeepSeekBalance(context.Background(), "test-key", time.Time{}, client)
			if got.Sample != (tc.want != nil) || !reflect.DeepEqual(got.Balance, tc.want) {
				t.Fatalf("sample = %t, balance = %+v, want %+v", got.Sample, got.Balance, tc.want)
			}
		})
	}
}

type unreadUsageBody struct{ t *testing.T }

func (body unreadUsageBody) Read([]byte) (int, error) {
	body.t.Fatal("read rejected HTTP body")
	return 0, io.EOF
}

func (unreadUsageBody) Close() error { return nil }

func TestZaiUsageRateLimitDoesNotReadBody(t *testing.T) {
	client := newUsageHTTPClient(codexUsageTransportFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusTooManyRequests,
			Header:     http.Header{"Retry-After": {"120"}},
			Body:       unreadUsageBody{t},
		}, nil
	}))
	got := fetchZaiUsage(context.Background(), "test-key", time.Time{}, client)
	if got.Status != http.StatusTooManyRequests || got.Outcome != QuotaUsageTemporarilyUnavailable ||
		got.Sample || got.RetryAt.Before(got.StartedAt.Add(2*time.Minute)) {
		t.Fatalf("rate-limited Z.AI lookup: %+v", got)
	}
}

func TestUsageTransport(t *testing.T) {
	production := newUsageHTTPClient(nil)
	policy := production.Transport.(*http.Transport)
	if policy.Proxy != nil || !policy.DisableKeepAlives || policy.MaxResponseHeaderBytes != quotaHeaderLimit ||
		!policy.Protocols.HTTP1() || policy.Protocols.HTTP2() || policy.Protocols.UnencryptedHTTP2() ||
		policy.TLSClientConfig != nil && policy.TLSClientConfig.InsecureSkipVerify {
		t.Fatalf("unexpected usage transport policy: %+v", policy)
	}
	for _, tc := range []struct {
		name, media, body, retry string
		status                   int
		stop                     bool
	}{
		{name: "redirect not followed", status: http.StatusFound},
		{name: "rate limit without reading the body", status: http.StatusTooManyRequests, retry: "120"},
		{name: "long retry stops polling", status: http.StatusServiceUnavailable, retry: "86401", stop: true},
		{name: "wrong media type", status: http.StatusOK, media: "text/plain"},
		{name: "duplicate key", status: http.StatusOK, media: "application/json", body: `{"is_available":true,"is_available":false}`},
		{name: "trailing JSON", status: http.StatusOK, media: "application/json", body: `{"is_available":false} null`},
		{name: "body limit", status: http.StatusOK, media: "application/json", body: `{"is_available":false}` + strings.Repeat(" ", quotaBodyLimit)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			client := newUsageHTTPClient(codexUsageTransportFunc(func(req *http.Request) (*http.Response, error) {
				calls++
				if req.Method != http.MethodGet || req.URL.String() != "https://api.deepseek.com/user/balance" || req.Header.Get("Accept") != "application/json" || req.Header.Get("Authorization") != "Bearer test-key" || req.Header.Get("User-Agent") != "psi-quota" {
					t.Fatalf("unexpected usage request: %v %s", req.Method, req.URL)
				}
				body := io.ReadCloser(unreadUsageBody{t})
				if tc.body != "" {
					body = io.NopCloser(strings.NewReader(tc.body))
				}
				return &http.Response{StatusCode: tc.status, Header: http.Header{"Location": {"https://other.example"}, "Content-Type": {tc.media}, "Retry-After": {tc.retry}}, Body: body}, nil
			}))
			got := fetchDeepSeekBalance(context.Background(), "test-key", time.Time{}, client)
			wantRetryAt := tc.retry != "" && !tc.stop
			if calls != 1 || got.Status != tc.status || got.Sample || got.StopPolling != tc.stop || !got.RetryAt.IsZero() != wantRetryAt {
				t.Fatalf("calls=%d result=%+v", calls, got)
			}
			if wantRetryAt && got.RetryAt.Before(got.StartedAt.Add(2*time.Minute)) {
				t.Fatal("Retry-After moved the next request earlier")
			}
		})
	}
}
