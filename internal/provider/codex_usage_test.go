package provider

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type codexUsageTransportFunc func(*http.Request) (*http.Response, error)

func (f codexUsageTransportFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

type codexUsageTestBody struct {
	io.Reader
	closed bool
}

func (b *codexUsageTestBody) Close() error {
	b.closed = true
	return nil
}

func codexUsageTestResponse(status int, body string) (*http.Response, *codexUsageTestBody) {
	tracked := &codexUsageTestBody{Reader: strings.NewReader(body)}
	return &http.Response{StatusCode: status, Body: tracked, Header: make(http.Header)}, tracked
}

func codexUsageJWT(t *testing.T, claims any) string {
	t.Helper()
	payload, err := json.Marshal(claims)
	if err != nil {
		t.Fatal(err)
	}
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none","typ":"JWT"}`))
	signature := base64.RawURLEncoding.EncodeToString([]byte("synthetic-signature"))
	return header + "." + base64.RawURLEncoding.EncodeToString(payload) + "." + signature
}

func codexUsageDoc(access, accountID string, expires time.Time) string {
	return fmt.Sprintf(`{"openai-codex":{"type":"oauth","access":%q,"refresh":"synthetic-refresh","expires":%d,"accountId":%q},"anthropic":{"type":"api_key","key":"synthetic-other"}}`,
		access, expires.UnixMilli(), accountID)
}

func codexUsageAuthPath(t *testing.T, access, accountID string, expires time.Time) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "auth.json")
	writeAuthDoc(t, path, codexUsageDoc(access, accountID, expires))
	return path
}

func codexUsageTestResult(transport http.RoundTripper, credential CodexUsageCredential, notBefore ...time.Time) CodexUsageResult {
	start := time.Time{}
	if len(notBefore) > 0 {
		start = notBefore[0]
	}
	return fetchCodexUsage(context.Background(), "", credential, start, newCodexQuotaHTTPClient(transport))
}

func TestReadCodexUsageCredentialUsesOnlyFreshOAuthEntry(t *testing.T) {
	now := time.UnixMilli(1_800_000_000_000)
	jwtAccess := codexUsageJWT(t, map[string]any{
		"https://api.openai.com/auth": map[string]any{"chatgpt_account_id": "jwt-account"},
	})
	cases := []struct {
		name      string
		access    string
		accountID string
		expires   time.Time
		want      bool
		wantAcct  string
	}{
		{"stored account", "synthetic-access", "stored-account", now.Add(time.Hour), true, "stored-account"},
		{"jwt account", jwtAccess, "", now.Add(time.Hour), true, "jwt-account"},
		{"expired", "synthetic-access", "stored-account", now, false, ""},
		{"missing access", "", "stored-account", now.Add(time.Hour), false, ""},
		{"blank account", "synthetic-access", "", now.Add(time.Hour), false, ""},
		{"unsafe access", "access with space", "stored-account", now.Add(time.Hour), false, ""},
		{"unsafe account", "synthetic-access", "account with space", now.Add(time.Hour), false, ""},
		{"oversized access", strings.Repeat("a", codexQuotaBodyLimit+1), "stored-account", now.Add(time.Hour), false, ""},
		{"oversized account", "synthetic-access", strings.Repeat("a", codexQuotaAccountMax+1), now.Add(time.Hour), false, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := codexUsageAuthPath(t, tc.access, tc.accountID, tc.expires)
			credential, ok := ReadCodexUsageCredential(path, now)
			if ok != tc.want {
				t.Fatalf("ReadCodexUsageCredential = %t, want %t", ok, tc.want)
			}
			if ok && (credential.Access != tc.access || credential.AccountID != tc.wantAcct || !credential.Expires.Equal(tc.expires) || credential.EntryHash == ([32]byte{})) {
				t.Fatalf("credential = access %q account %q expires %v", credential.Access, credential.AccountID, credential.Expires)
			}
		})
	}
}

func TestReadCodexUsageCredentialRejectsAPIKeyAndMalformedFiles(t *testing.T) {
	now := time.UnixMilli(1_800_000_000_000)
	dir := t.TempDir()
	for name, doc := range map[string]string{
		"api key":   `{"openai-codex":{"type":"api_key","key":"synthetic-key"}}`,
		"malformed": `{"openai-codex":`,
		"no entry":  `{"anthropic":{"type":"api_key","key":"synthetic-other"}}`,
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(dir, name+".json")
			writeAuthDoc(t, path, doc)
			if credential, ok := ReadCodexUsageCredential(path, now); ok {
				t.Fatalf("credential = %+v, want rejection", credential)
			}
		})
	}
}

func TestReadCodexUsageCredentialFollowsRegularSymlinkWithoutWrites(t *testing.T) {
	now := time.UnixMilli(1_800_000_000_000)
	dir := t.TempDir()
	target := filepath.Join(dir, "target.json")
	writeAuthDoc(t, target, codexUsageDoc("synthetic-access", "stored-account", now.Add(time.Hour)))
	link := filepath.Join(dir, "auth.json")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	ResetAuthReads()
	if _, ok := ReadCodexUsageCredential(link, now); !ok {
		t.Fatal("regular-target symlink was rejected")
	}
	if reads := AuthReads(); reads != 0 {
		t.Fatalf("shared auth reads = %d, want 0", reads)
	}
	after, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Fatal("quota credential read changed auth.json")
	}
	if _, err := os.Stat(target + ".lock"); !os.IsNotExist(err) {
		t.Fatal("quota credential read created a lease")
	}
}

func TestReadCodexUsageCredentialDoesNotWaitForAuthLease(t *testing.T) {
	now := time.UnixMilli(1_800_000_000_000)
	path := codexUsageAuthPath(t, "synthetic-access", "stored-account", now.Add(time.Hour))
	unlock, _, err := acquireAuthLock(context.Background(), path+".lock", 0, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()

	done := make(chan bool, 1)
	go func() {
		_, ok := ReadCodexUsageCredential(path, now)
		done <- ok
	}()
	select {
	case ok := <-done:
		if !ok {
			t.Fatal("credential was unreadable while another process held the lease")
		}
	case <-time.After(time.Second):
		t.Fatal("quota credential read waited for the auth lease")
	}
}

func TestReadCodexUsageCredentialHashTracksSelectedEntry(t *testing.T) {
	now := time.UnixMilli(1_800_000_000_000)
	path := filepath.Join(t.TempDir(), "auth.json")
	firstDoc := codexUsageDoc("synthetic-access", "stored-account", now.Add(time.Hour))
	writeAuthDoc(t, path, firstDoc)
	first, ok := ReadCodexUsageCredential(path, now)
	if !ok {
		t.Fatal("first credential was not readable")
	}

	changedDoc := strings.Replace(firstDoc, `"key":"synthetic-other"`, `"key":"changed-other"`, 1)
	writeAuthDoc(t, path, changedDoc)
	second, ok := ReadCodexUsageCredential(path, now)
	if !ok {
		t.Fatal("second credential was not readable")
	}
	if second.EntryHash != first.EntryHash {
		t.Fatal("unrelated entry changed the selected entry hash")
	}

	replacedDoc := codexUsageDoc("replacement-access", "stored-account", now.Add(2*time.Hour))
	writeAuthDoc(t, path, replacedDoc)
	third, ok := ReadCodexUsageCredential(path, now)
	if !ok {
		t.Fatal("replacement credential was not readable")
	}
	if third.EntryHash == first.EntryHash {
		t.Fatal("replacement credential kept the old entry hash")
	}
}

func TestFetchCodexUsageSendsOneBoundedRequest(t *testing.T) {
	var calls int
	var seen *http.Request
	transport := codexUsageTransportFunc(func(req *http.Request) (*http.Response, error) {
		calls++
		seen = req
		response, _ := codexUsageTestResponse(http.StatusOK, `{"rate_limit":{"primary_window":{"used_percent":42}}}`)
		return response, nil
	})
	result := codexUsageTestResult(transport, CodexUsageCredential{
		Access: "synthetic-access", AccountID: "stored-account", Expires: time.Now().Add(time.Hour),
	})
	if calls != 1 || !result.Sample || result.UsedPercent != 42 || result.Status != http.StatusOK {
		t.Fatalf("calls=%d result=%+v", calls, result)
	}
	if seen == nil || seen.Method != http.MethodGet || seen.URL.String() != codexQuotaEndpoint {
		t.Fatalf("request = %v", seen)
	}
	for name, want := range map[string]string{
		"Authorization":      "Bearer synthetic-access",
		"Chatgpt-Account-Id": "stored-account",
		"Accept":             "application/json",
		"User-Agent":         codexQuotaUserAgent,
	} {
		if got := seen.Header.Get(name); got != want {
			t.Errorf("%s = %q, want %q", name, got, want)
		}
	}
	for _, name := range []string{"Cookie", "OpenAI-Beta", "originator", "session-id", "x-client-request-id"} {
		if got := seen.Header.Get(name); got != "" {
			t.Errorf("unexpected inherited header %s = %q", name, got)
		}
	}
	if _, ok := seen.Context().Deadline(); !ok {
		t.Error("request context has no deadline")
	}
}

func TestFetchCodexUsageSkipsStartBeforeNotBefore(t *testing.T) {
	calls := 0
	result := codexUsageTestResult(codexUsageTransportFunc(func(*http.Request) (*http.Response, error) {
		calls++
		return nil, errors.New("unexpected request")
	}), CodexUsageCredential{
		Access: "synthetic-access", AccountID: "stored-account", Expires: time.Now().Add(time.Hour),
	}, time.Now().Add(time.Minute))
	if calls != 0 || result.Sample || result.StartedAt != (time.Time{}) {
		t.Fatalf("calls=%d result=%+v", calls, result)
	}
}

func TestFetchCodexUsageRejectsExpiredOrUnsafeCredentialWithoutRequest(t *testing.T) {
	for _, credential := range []CodexUsageCredential{
		{},
		{Access: "synthetic-access", AccountID: "stored-account"},
		{Access: "synthetic-access", AccountID: "stored-account", Expires: time.Now().Add(-time.Second)},
		{Access: "access\nvalue", AccountID: "stored-account", Expires: time.Now().Add(time.Hour)},
		{Access: "synthetic-access", AccountID: "account with space", Expires: time.Now().Add(time.Hour)},
	} {
		called := false
		result := codexUsageTestResult(codexUsageTransportFunc(func(*http.Request) (*http.Response, error) {
			called = true
			return nil, errors.New("unexpected request")
		}), credential)
		if called || result.Outcome != CodexUsageNoCredential {
			t.Fatalf("credential %+v: called=%t result=%+v", credential, called, result)
		}
	}
}

func TestFetchCodexUsageParsesOnlyUnambiguousPrimaryReading(t *testing.T) {
	cases := []struct {
		name string
		body string
		used float64
		ok   bool
	}{
		{"zero", `{"rate_limit":{"primary_window":{"used_percent":0}}}`, 0, true},
		{"hundred", `{"rate_limit":{"primary_window":{"used_percent":100}}}`, 100, true},
		{"fraction", `{"rate_limit":{"primary_window":{"used_percent":12.5}}}`, 12.5, true},
		{"extra", `{"rate_limit":{"primary_window":{"used_percent":5,"limit_window_seconds":3600}}}`, 5, true},
		{"missing rate", `{}`, 0, false},
		{"null rate", `{"rate_limit":null}`, 0, false},
		{"missing primary", `{"rate_limit":{}}`, 0, false},
		{"null primary", `{"rate_limit":{"primary_window":null}}`, 0, false},
		{"missing used", `{"rate_limit":{"primary_window":{}}}`, 0, false},
		{"null used", `{"rate_limit":{"primary_window":{"used_percent":null}}}`, 0, false},
		{"string used", `{"rate_limit":{"primary_window":{"used_percent":"5"}}}`, 0, false},
		{"negative", `{"rate_limit":{"primary_window":{"used_percent":-1}}}`, 0, false},
		{"over hundred", `{"rate_limit":{"primary_window":{"used_percent":101}}}`, 0, false},
		{"overflow", `{"rate_limit":{"primary_window":{"used_percent":1e999}}}`, 0, false},
		{"duplicate used", `{"rate_limit":{"primary_window":{"used_percent":1,"used_percent":2}}}`, 0, false},
		{"duplicate rate", `{"rate_limit":{"primary_window":{"used_percent":1}},"rate_limit":{}}`, 0, false},
		{"nested duplicate", `{"rate_limit":{"primary_window":{"used_percent":1},"primary_window":{}}}`, 0, false},
		{"trailing", `{"rate_limit":{"primary_window":{"used_percent":1}}} true`, 0, false},
		{"top array", `[]`, 0, false},
		{"top null", `null`, 0, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := &codexUsageTestBody{Reader: strings.NewReader(tc.body)}
			result := codexUsageTestResult(codexUsageTransportFunc(func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: http.StatusOK, Body: body, Header: make(http.Header)}, nil
			}), CodexUsageCredential{Access: "synthetic-access", AccountID: "stored-account", Expires: time.Now().Add(time.Hour)})
			if result.Sample != tc.ok || result.UsedPercent != tc.used {
				t.Fatalf("result = %+v, want sample=%t used=%v", result, tc.ok, tc.used)
			}
			if !body.closed {
				t.Fatal("response body was not closed")
			}
		})
	}
}

func TestParseCodexRetryAfter(t *testing.T) {
	received := time.Date(2027, 1, 15, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name      string
		values    []string
		want      time.Time
		permanent bool
	}{
		{"seconds", []string{"120"}, received.Add(2 * time.Minute), false},
		{"zero seconds", []string{"0"}, received, false},
		{"one day", []string{"86400"}, received.Add(24 * time.Hour), false},
		{"over one day", []string{"86401"}, time.Time{}, true},
		{"overflow", []string{"18446744073709551616"}, time.Time{}, true},
		{"http date", []string{received.Add(2 * time.Hour).Format(http.TimeFormat)}, received.Add(2 * time.Hour), false},
		{"past date", []string{received.Add(-time.Hour).Format(http.TimeFormat)}, time.Time{}, false},
		{"date over one day", []string{received.Add(25 * time.Hour).Format(http.TimeFormat)}, time.Time{}, true},
		{"invalid", []string{"later"}, time.Time{}, false},
		{"negative", []string{"-1"}, time.Time{}, false},
		{"empty", []string{""}, time.Time{}, false},
		{"multiple values", []string{"120", "240"}, time.Time{}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, permanent := parseCodexRetryAfter(tc.values, received)
			if !got.Equal(tc.want) || permanent != tc.permanent {
				t.Fatalf("parseCodexRetryAfter(%q) = %v/%t, want %v/%t", tc.values, got, permanent, tc.want, tc.permanent)
			}
		})
	}
}

func TestCodexQuotaRetryStatusGate(t *testing.T) {
	received := time.Unix(1_800_000_000, 0)
	if retry, stop := codexQuotaRetry(http.StatusTooManyRequests, []string{"120"}, received); !retry.Equal(received.Add(2*time.Minute)) || stop {
		t.Fatalf("429 retry = %v/%t", retry, stop)
	}
	if retry, stop := codexQuotaRetry(http.StatusInternalServerError, []string{"86401"}, received); !retry.IsZero() || !stop {
		t.Fatalf("500 stop = %v/%t", retry, stop)
	}
	if retry, stop := codexQuotaRetry(http.StatusUnauthorized, []string{"86401"}, received); !retry.IsZero() || stop {
		t.Fatalf("401 retry = %v/%t", retry, stop)
	}
}

func TestFetchCodexUsageReturnsRetryMetadata(t *testing.T) {
	credential := CodexUsageCredential{Access: "synthetic-access", AccountID: "synthetic-account", Expires: time.Now().Add(time.Hour)}
	transport := codexUsageTransportFunc(func(*http.Request) (*http.Response, error) {
		header := make(http.Header)
		header.Set("Retry-After", "120")
		return &http.Response{StatusCode: http.StatusTooManyRequests, Header: header, Body: io.NopCloser(strings.NewReader(`{}`))}, nil
	})
	before := time.Now()
	result := fetchCodexUsage(context.Background(), "", credential, time.Time{}, newCodexQuotaHTTPClient(transport))
	after := time.Now()
	if result.Outcome != CodexUsageTemporarilyUnavailable || result.Status != http.StatusTooManyRequests {
		t.Fatalf("retry result = %+v", result)
	}
	if result.RetryAt.Before(before.Add(120*time.Second)) || result.RetryAt.After(after.Add(120*time.Second)) || result.StopPolling {
		t.Fatalf("retry metadata = %v stop=%t", result.RetryAt, result.StopPolling)
	}

	transport = codexUsageTransportFunc(func(*http.Request) (*http.Response, error) {
		header := make(http.Header)
		header.Set("Retry-After", "86401")
		return &http.Response{StatusCode: http.StatusInternalServerError, Header: header, Body: io.NopCloser(strings.NewReader(`{}`))}, nil
	})
	result = fetchCodexUsage(context.Background(), "", credential, time.Time{}, newCodexQuotaHTTPClient(transport))
	if !result.StopPolling || !result.RetryAt.IsZero() {
		t.Fatalf("permanent retry metadata = %v stop=%t", result.RetryAt, result.StopPolling)
	}
}

func TestFetchCodexUsageStatusAndTransportOutcomes(t *testing.T) {
	for _, tc := range []struct {
		status  int
		outcome CodexUsageOutcome
	}{
		{http.StatusBadRequest, CodexUsageUnsupported},
		{http.StatusUnauthorized, CodexUsageUnsupported},
		{http.StatusForbidden, CodexUsageUnsupported},
		{http.StatusNotFound, CodexUsageUnsupported},
		{http.StatusRequestTimeout, CodexUsageTemporarilyUnavailable},
		{http.StatusTooManyRequests, CodexUsageTemporarilyUnavailable},
		{http.StatusInternalServerError, CodexUsageTemporarilyUnavailable},
		{http.StatusFound, CodexUsageUnsupported},
	} {
		t.Run(fmt.Sprint(tc.status), func(t *testing.T) {
			body := &codexUsageTestBody{Reader: strings.NewReader("ignored")}
			result := codexUsageTestResult(codexUsageTransportFunc(func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: tc.status, Body: body, Header: make(http.Header)}, nil
			}), CodexUsageCredential{Access: "synthetic-access", AccountID: "stored-account", Expires: time.Now().Add(time.Hour)})
			if result.Outcome != tc.outcome || result.Status != tc.status || result.Sample {
				t.Fatalf("result = %+v, want outcome=%s status=%d", result, tc.outcome, tc.status)
			}
			if !body.closed {
				t.Fatal("response body was not closed")
			}
		})
	}

	result := codexUsageTestResult(codexUsageTransportFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("synthetic transport failure")
	}), CodexUsageCredential{Access: "synthetic-access", AccountID: "stored-account", Expires: time.Now().Add(time.Hour)})
	if result.Outcome != CodexUsageTemporarilyUnavailable || result.Status != 0 || result.Sample {
		t.Fatalf("transport error result = %+v", result)
	}
}

func TestFetchCodexUsageBodyLimitReadErrorAndCancellation(t *testing.T) {
	overflowBody := &codexUsageTestBody{Reader: strings.NewReader(strings.Repeat("x", codexQuotaBodyLimit+1))}
	result := codexUsageTestResult(codexUsageTransportFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Body: overflowBody, Header: make(http.Header)}, nil
	}), CodexUsageCredential{Access: "synthetic-access", AccountID: "stored-account", Expires: time.Now().Add(time.Hour)})
	if result.Sample || result.Status != http.StatusOK || !overflowBody.closed {
		t.Fatalf("overflow result = %+v closed=%t", result, overflowBody.closed)
	}

	readErr := errors.New("synthetic read failure")
	errorBody := &codexUsageTestBody{Reader: io.MultiReader(strings.NewReader("{"), codexUsageErrorReader{err: readErr})}
	result = codexUsageTestResult(codexUsageTransportFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Body: errorBody, Header: make(http.Header)}, nil
	}), CodexUsageCredential{Access: "synthetic-access", AccountID: "stored-account", Expires: time.Now().Add(time.Hour)})
	if result.Outcome != CodexUsageTemporarilyUnavailable || result.Status != http.StatusOK || !errorBody.closed {
		t.Fatalf("read error result = %+v closed=%t", result, errorBody.closed)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	result = fetchCodexUsage(ctx, "", CodexUsageCredential{Access: "synthetic-access", AccountID: "stored-account", Expires: time.Now().Add(time.Hour)}, time.Time{}, newCodexQuotaHTTPClient(codexUsageTransportFunc(func(req *http.Request) (*http.Response, error) {
		<-req.Context().Done()
		return nil, req.Context().Err()
	})))
	if result.Sample || result.Outcome != CodexUsageTemporarilyUnavailable {
		t.Fatalf("cancel result = %+v", result)
	}
}

type codexUsageErrorReader struct{ err error }

func (r codexUsageErrorReader) Read([]byte) (int, error) { return 0, r.err }

func TestFetchCodexUsageRedirectAndTransportPolicy(t *testing.T) {
	client := newCodexQuotaHTTPClient(codexUsageTransportFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("unused")
	}))
	request, err := http.NewRequest(http.MethodGet, codexQuotaEndpoint, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := client.CheckRedirect(request, nil); !errors.Is(err, http.ErrUseLastResponse) {
		t.Fatalf("redirect error = %v", err)
	}
	if client.Jar != nil {
		t.Fatal("usage client kept a cookie jar")
	}
	transport := newCodexQuotaTransport()
	if transport.Proxy != nil || !transport.DisableKeepAlives || transport.MaxResponseHeaderBytes != codexQuotaHeaderLimit {
		t.Fatalf("transport = proxy set %t keepalive %t headers %d", transport.Proxy != nil, transport.DisableKeepAlives, transport.MaxResponseHeaderBytes)
	}
	if transport.TLSClientConfig != nil && transport.TLSClientConfig.InsecureSkipVerify {
		t.Fatal("usage transport disabled TLS verification")
	}
}

func TestFetchCodexUsageDiscardsSampleAfterCredentialReplacement(t *testing.T) {
	now := time.Now()
	path := filepath.Join(t.TempDir(), "auth.json")
	writeAuthDoc(t, path, codexUsageDoc("old-access", "stored-account", now.Add(time.Hour)))
	credential, ok := ReadCodexUsageCredential(path, now)
	if !ok {
		t.Fatal("old credential was not readable")
	}
	body := &codexUsageTestBody{Reader: strings.NewReader(`{"rate_limit":{"primary_window":{"used_percent":42}}}`)}
	transport := codexUsageTransportFunc(func(*http.Request) (*http.Response, error) {
		replacement := codexUsageDoc("new-access", "stored-account", now.Add(2*time.Hour))
		temporary := path + ".tmp"
		if err := os.WriteFile(temporary, []byte(replacement), 0600); err != nil {
			t.Error(err)
		}
		if err := os.Rename(temporary, path); err != nil {
			t.Error(err)
		}
		return &http.Response{StatusCode: http.StatusOK, Body: body, Header: make(http.Header)}, nil
	})
	result := fetchCodexUsage(context.Background(), path, credential, time.Time{}, newCodexQuotaHTTPClient(transport))
	if result.Sample || result.Outcome != CodexUsageNoCredential {
		t.Fatalf("replacement result = %+v", result)
	}
	if !body.closed {
		t.Fatal("replacement response body was not closed")
	}
}

func TestFetchCodexUsageDoesNotWriteAuth(t *testing.T) {
	now := time.Now()
	path := filepath.Join(t.TempDir(), "auth.json")
	writeAuthDoc(t, path, codexUsageDoc("synthetic-access", "stored-account", now.Add(time.Hour)))
	credential, ok := ReadCodexUsageCredential(path, now)
	if !ok {
		t.Fatal("credential was not readable")
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	body := &codexUsageTestBody{Reader: strings.NewReader(`{"rate_limit":{"primary_window":{"used_percent":42}}}`)}
	result := fetchCodexUsage(context.Background(), path, credential, time.Time{}, newCodexQuotaHTTPClient(codexUsageTransportFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Body: body, Header: make(http.Header)}, nil
	})))
	if !result.Sample {
		t.Fatalf("result = %+v", result)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Fatal("usage fetch changed auth.json")
	}
	if _, err := os.Stat(path + ".lock"); !os.IsNotExist(err) {
		t.Fatal("usage fetch created a lease")
	}
}

func TestFetchCodexUsageReadsNoCredentialWithoutRequest(t *testing.T) {
	called := false
	result := codexUsageTestResult(codexUsageTransportFunc(func(*http.Request) (*http.Response, error) {
		called = true
		return nil, errors.New("unexpected request")
	}), CodexUsageCredential{})
	if called || result.Outcome != CodexUsageNoCredential {
		t.Fatalf("called=%t result=%+v", called, result)
	}
}
