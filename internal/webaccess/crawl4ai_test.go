package webaccess

import (
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/iotest"
	"time"
)

func crawl4AIConfig(base string) Config {
	cfg := testConfig()
	cfg.Crawl4AIBaseURL = base
	cfg.Crawl4AIAllowed = true
	cfg.Crawl4AIAvailable = true
	return cfg
}

func TestCrawl4AIMVP(t *testing.T) {
	const (
		tokenCanary   = "crawl4ai-token-canary"
		exaCanary     = "exa-key-canary"
		searxCanary   = "searx-header-canary"
		langCanary    = "page-lang-canary"
		markdown      = "# **Title**\n\nCRAWL4AI-BODY-UNIQUE"
		pagePath      = "/doc"
		extractorPath = "/api/md"
	)
	var pageCalls, extractorCalls atomic.Int32
	target := ""
	page := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		pageCalls.Add(1)
		if r.Header.Get("Authorization") != "" {
			t.Errorf("page received an Authorization header")
		}
		if r.Header.Get("Accept-Language") != langCanary {
			t.Errorf("page did not receive its configured language header")
		}
		http.Error(w, "direct denied", http.StatusForbidden)
	}))
	defer page.Close()
	target = page.URL + pagePath
	extractor := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		extractorCalls.Add(1)
		if r.Method != http.MethodPost || r.URL.Path != extractorPath {
			t.Errorf("extractor request = %s %s", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer "+tokenCanary {
			t.Errorf("extractor bearer header did not match")
		}
		for _, name := range []string{"X-Searx-Canary", "Accept-Language", "Cookie", "X-Api-Key"} {
			if got := r.Header.Get(name); got != "" {
				t.Errorf("extractor received forbidden header %s", name)
			}
		}
		if r.Header.Get("User-Agent") == browserUserAgents["chrome"] {
			t.Error("extractor received the page browser profile")
		}
		var body struct {
			URL string `json:"url"`
			F   string `json:"f"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("extractor body: %v", err)
			return
		}
		if body.URL != target || body.F != "fit" {
			t.Errorf("extractor payload URL/F did not match the normalized target")
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"success":true,"markdown":`+strconvQuote(markdown)+`}`)
	}))
	defer extractor.Close()

	cfg := crawl4AIConfig(extractor.URL + "/api")
	cfg.Crawl4AIToken = tokenCanary
	cfg.ExaAPIKey = exaCanary
	cfg.SearXNGHeaders = http.Header{"X-Searx-Canary": {searxCanary}}
	cfg.FetchAcceptLanguage = langCanary
	cfg.FetchHeaderProfile = "chrome"
	client := testClient(cfg)
	defer client.CloseIdle()

	result := client.Dispatch(context.Background(), toolsInvocation(ToolFetch, map[string]any{
		"url": "  " + target + "  ", "mode": "readable",
	}))
	if result.IsError {
		t.Fatalf("fallback failed: %s", result.Content[0].Text)
	}
	text := result.Content[0].Text
	for _, want := range []string{"CRAWL4AI-BODY-UNIQUE", "title: **Title**", "mode: readable"} {
		if !strings.Contains(text, want) {
			t.Fatalf("result missing %q:\n%s", want, text)
		}
	}
	for _, canary := range []string{tokenCanary, exaCanary, searxCanary} {
		if strings.Contains(text, canary) {
			t.Fatalf("result leaked a configured canary")
		}
	}
	details := batchDetails(t, result)
	id, _ := details["responseId"].(string)
	if id == "" {
		t.Fatal("fallback result was not retained")
	}
	retrieved := getContent(t, client, map[string]any{"responseId": id})
	if retrieved.IsError || !strings.Contains(retrieved.Content[0].Text, "CRAWL4AI-BODY-UNIQUE") {
		t.Fatalf("retained fallback text not retrievable: %s", retrieved.Content[0].Text)
	}
	if got := pageCalls.Load(); got != 1 {
		t.Fatalf("page calls = %d, want 1", got)
	}
	if got := extractorCalls.Load(); got != 1 {
		t.Fatalf("extractor calls = %d, want 1", got)
	}
}

func TestCrawl4AIUnauthenticated(t *testing.T) {
	var extractorCalls atomic.Int32
	extractor := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		extractorCalls.Add(1)
		if got := r.Header.Get("Authorization"); got != "" {
			t.Error("unauthenticated extractor received Authorization")
		}
		io.WriteString(w, `{"success":true,"markdown":"unauthenticated-ok"}`)
	}))
	defer extractor.Close()
	page := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "denied", http.StatusForbidden)
	}))
	defer page.Close()
	client := testClient(crawl4AIConfig(extractor.URL))
	result := client.Dispatch(context.Background(), toolsInvocation(ToolFetch, map[string]any{"url": page.URL, "mode": "readable"}))
	if result.IsError || !strings.Contains(result.Content[0].Text, "unauthenticated-ok") || extractorCalls.Load() != 1 {
		t.Fatalf("unauthenticated extraction failed: %s", result.Content[0].Text)
	}
}

func TestCrawl4AIBypass(t *testing.T) {
	eligibleBase := func(t *testing.T, pageStatus int) (*httptest.Server, *atomic.Int32, Config) {
		t.Helper()
		extractorCalls := &atomic.Int32{}
		extractor := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			extractorCalls.Add(1)
			io.WriteString(w, `{"success":true,"markdown":"extracted"}`)
		}))
		t.Cleanup(extractor.Close)
		page := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if pageStatus == http.StatusOK {
				w.Header().Set("Content-Type", "text/plain")
				io.WriteString(w, "short direct result")
				return
			}
			http.Error(w, "denied", pageStatus)
		}))
		t.Cleanup(page.Close)
		cfg := crawl4AIConfig(extractor.URL)
		return page, extractorCalls, cfg
	}

	cases := []struct {
		name       string
		pageStatus int
		mode       string
		mutate     func(*Config)
		want       string
	}{
		{"short direct success wins", http.StatusOK, "readable", func(*Config) {}, "short direct result"},
		{"raw never extracts", http.StatusForbidden, "raw", func(*Config) {}, "http status: 403"},
		{"missing base", http.StatusForbidden, "readable", func(c *Config) {
			c.Crawl4AIBaseURL = ""
			c.Crawl4AIAvailable = false
		}, "http status: 403"},
		{"excluded routing", http.StatusForbidden, "readable", func(c *Config) { c.Crawl4AIAllowed = false }, "http status: 403"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			page, extractorCalls, cfg := eligibleBase(t, tc.pageStatus)
			tc.mutate(&cfg)
			client := testClient(cfg)
			defer client.CloseIdle()
			result := client.Dispatch(context.Background(), toolsInvocation(ToolFetch, map[string]any{
				"url": page.URL + "/page", "mode": tc.mode,
			}))
			if !strings.Contains(result.Content[0].Text, tc.want) {
				t.Fatalf("result = %s, want %q", result.Content[0].Text, tc.want)
			}
			if got := extractorCalls.Load(); got != 0 {
				t.Fatalf("extractor calls = %d, want 0", got)
			}
		})
	}
}

func TestCrawl4AISafetyGate(t *testing.T) {
	var extractorCalls atomic.Int32
	extractor := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		extractorCalls.Add(1)
		io.WriteString(w, `{"success":true,"markdown":"unexpected"}`)
	}))
	defer extractor.Close()
	cfg := crawl4AIConfig(extractor.URL)
	cfg.AllowRanges = nil
	client := testClient(cfg)
	defer client.CloseIdle()

	cases := []struct {
		name string
		url  string
		ctx  context.Context
	}{
		{"malformed target", "ftp://example.com/page", context.Background()},
		{"blocked target", "http://10.0.0.1/page", context.Background()},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			result := client.Dispatch(tc.ctx, toolsInvocation(ToolFetch, map[string]any{
				"url": tc.url, "mode": "readable",
			}))
			if !result.IsError {
				t.Fatalf("target unexpectedly succeeded: %s", result.Content[0].Text)
			}
		})
	}

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	result := client.Dispatch(cancelled, toolsInvocation(ToolFetch, map[string]any{
		"url": "https://example.com/page", "mode": "readable",
	}))
	if !result.IsError || !strings.Contains(result.Content[0].Text, classCancelled) {
		t.Fatalf("cancelled caller result = %s", result.Content[0].Text)
	}
	if got := extractorCalls.Load(); got != 0 {
		t.Fatalf("safety exclusions made %d extractor calls", got)
	}
}

func TestCrawl4AITransportIsolation(t *testing.T) {
	var extractorCalls, otherExtractorCalls, searxCalls atomic.Int32
	extractor := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		extractorCalls.Add(1)
		io.WriteString(w, `{"success":true,"markdown":"isolated extraction"}`)
	}))
	defer extractor.Close()
	otherExtractor := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		otherExtractorCalls.Add(1)
		io.WriteString(w, `{"success":true,"markdown":"wrong service"}`)
	}))
	defer otherExtractor.Close()
	searx := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		searxCalls.Add(1)
		io.WriteString(w, `{"results":[]}`)
	}))
	defer searx.Close()

	cfg := crawl4AIConfig(extractor.URL)
	cfg.AllowRanges = nil
	cfg.SearXNGBaseURL = searx.URL
	client := New(cfg)
	defer client.CloseIdle()
	client.page.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusForbidden, Header: http.Header{}, Body: io.NopCloser(strings.NewReader("denied")), Request: r}, nil
	})
	page, err := client.fetchPage(context.Background(), "https://example.com/page", "readable")
	if err != nil {
		t.Fatalf("configured loopback extractor was blocked: %v", err)
	}
	if page.Text != "isolated extraction" {
		t.Fatalf("extractor page text = %q", page.Text)
	}

	// Denial checks use a client whose page transport was not replaced.
	normal := New(cfg)
	defer normal.CloseIdle()
	denied := []struct {
		name string
		get  func() (*http.Response, error)
	}{
		{"page pool to extractor", func() (*http.Response, error) { return normal.page.Get(extractor.URL) }},
		{"extractor pool to another port", func() (*http.Response, error) { return normal.extractor.Get(otherExtractor.URL) }},
		{"extractor pool to SearXNG", func() (*http.Response, error) { return normal.extractor.Get(searx.URL) }},
		{"provider pool to extractor", func() (*http.Response, error) { return normal.provider.Get(extractor.URL) }},
	}
	for _, tc := range denied {
		t.Run(tc.name, func(t *testing.T) {
			resp, err := tc.get()
			if err == nil {
				resp.Body.Close()
				t.Fatal("loopback destination was not blocked")
			}
			if got := searxCalls.Load(); got != 0 {
				t.Fatalf("SearXNG origin received %d denied requests", got)
			}
			if got := otherExtractorCalls.Load(); got != 0 {
				t.Fatalf("other extractor origin received %d denied requests", got)
			}
		})
	}
	if got := extractorCalls.Load(); got != 1 {
		t.Fatalf("extractor calls = %d, want the single allowed extraction", got)
	}
}

func TestCrawl4AIEnvelope(t *testing.T) {
	target, err := validateTarget("https://example.com/page")
	if err != nil {
		t.Fatal(err)
	}
	valid := `{"success":true,"markdown":"body"}`
	cases := []struct {
		name   string
		status int
		body   string
		class  string
	}{
		{"valid", http.StatusOK, valid, ""},
		{"unsolicited partial", http.StatusPartialContent, valid, classStatus},
		{"unauthorized", http.StatusUnauthorized, `{"error":"SERVICE-ERROR-CANARY"}`, classStatus},
		{"rate limited", http.StatusTooManyRequests, `{"error":"SERVICE-ERROR-CANARY"}`, classRateLimit},
		{"unavailable", http.StatusServiceUnavailable, `{"error":"SERVICE-ERROR-CANARY"}`, classStatus},
		{"null root", http.StatusOK, `null`, classInvalid},
		{"array root", http.StatusOK, `[]`, classInvalid},
		{"malformed", http.StatusOK, `{"success":`, classInvalid},
		{"trailing", http.StatusOK, valid + ` true`, classInvalid},
		{"missing success", http.StatusOK, `{"markdown":"body"}`, classInvalid},
		{"null success", http.StatusOK, `{"success":null,"markdown":"body"}`, classInvalid},
		{"false success", http.StatusOK, `{"success":false,"markdown":"body"}`, classInvalid},
		{"case variant success", http.StatusOK, `{"Success":true,"markdown":"body"}`, classInvalid},
		{"wrong success type", http.StatusOK, `{"success":"true","markdown":"body"}`, classInvalid},
		{"missing markdown", http.StatusOK, `{"success":true}`, classInvalid},
		{"null markdown", http.StatusOK, `{"success":true,"markdown":null}`, classInvalid},
		{"wrong markdown type", http.StatusOK, `{"success":true,"markdown":7}`, classInvalid},
		{"empty markdown", http.StatusOK, `{"success":true,"markdown":" \n\t "}`, classInvalid},
		{"duplicate null success", http.StatusOK, `{"success":true,"success":null,"markdown":"body"}`, classInvalid},
		{"duplicate null markdown", http.StatusOK, `{"success":true,"markdown":"body","markdown":null}`, classInvalid},
		{"service error stays internal", http.StatusOK, `{"success":false,"error":"SERVICE-ERROR-CANARY"}`, classInvalid},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			client := testClient(crawl4AIConfig("https://crawl.example"))
			closed := atomic.Bool{}
			client.extractor = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				return &http.Response{
					StatusCode: tc.status,
					Header:     http.Header{"Content-Type": {"application/json"}},
					Body:       &trackingReadCloser{Reader: strings.NewReader(tc.body), closed: &closed},
					Request:    r,
				}, nil
			})}
			_, err := client.extractCrawl4AI(context.Background(), target)
			if tc.class == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
			} else if err == nil || !hasClass(err.Error(), tc.class) {
				t.Fatalf("error = %v, want class %s", err, tc.class)
			}
			if err != nil && strings.Contains(err.Error(), "SERVICE-ERROR-CANARY") {
				t.Fatal("service error text leaked into the error")
			}
			if !closed.Load() {
				t.Fatal("response body was not closed")
			}
		})
	}
}

func TestCrawl4AIRedactionSize(t *testing.T) {
	target, err := validateTarget("https://example.com/page")
	if err != nil {
		t.Fatal(err)
	}
	newClient := func(token, body string) *Client {
		cfg := crawl4AIConfig("https://crawl.example")
		cfg.Crawl4AIToken = token
		client := testClient(cfg)
		client.extractor = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": {"application/json"}},
				Body:       io.NopCloser(strings.NewReader(body)),
				Request:    r,
			}, nil
		})}
		return client
	}

	const token = "Z"
	prefix := strings.Repeat("a", maxBodyBytes-10*len(redactionMarker))
	exact := `{"success":true,"markdown":` + strconvQuote(prefix+strings.Repeat(token, 10)) + `}`
	page, err := newClient(token, exact).extractCrawl4AI(context.Background(), target)
	if err != nil {
		t.Fatalf("exact redacted cap rejected: %v", err)
	}
	if len(page.Text) != maxBodyBytes {
		t.Fatalf("redacted length = %d, want %d", len(page.Text), maxBodyBytes)
	}
	overflow := `{"success":true,"markdown":` + strconvQuote(prefix+strings.Repeat(token, 11)) + `}`
	if _, err := newClient(token, overflow).extractCrawl4AI(context.Background(), target); err == nil || !hasClass(err.Error(), classInvalid) {
		t.Fatalf("redaction overflow error = %v", err)
	}
}

func TestCrawl4AIPreservesDirectError(t *testing.T) {
	page := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "denied", http.StatusForbidden)
	}))
	defer page.Close()
	var extractorCalls atomic.Int32
	extractor := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		extractorCalls.Add(1)
		io.WriteString(w, `{"success":false,"error":"SERVICE-ERROR-CANARY"}`)
	}))
	defer extractor.Close()
	client := testClient(crawl4AIConfig(extractor.URL))
	result := client.Dispatch(context.Background(), toolsInvocation(ToolFetch, map[string]any{
		"url": page.URL, "mode": "readable",
	}))
	text := result.Content[0].Text
	if !result.IsError || !strings.Contains(text, "http status: 403") {
		t.Fatalf("direct error not preserved: %s", text)
	}
	if strings.Contains(text, "SERVICE-ERROR-CANARY") {
		t.Fatal("service error text reached the public result")
	}
	if got := extractorCalls.Load(); got != 1 {
		t.Fatalf("extractor calls = %d, want 1", got)
	}
}

func TestCrawl4AITitle(t *testing.T) {
	cases := map[string]string{
		"## **No**\n  # **Yes**":     "**Yes**",
		"#Title":                     "",
		"#\tTabbing":                 "Tabbing",
		"  #\t **Asterisks** \t ":    "**Asterisks**",
		"text\n# First\n# Second":    "First",
		"```\n# Not a heading\n```":  "Not a heading",
		"#":                          "",
		"### ###":                    "",
		"not a heading":              "",
		"\t## skip\n\t# keep **it**": "keep **it**",
	}
	for input, want := range cases {
		if got := crawl4AITitle(input); got != want {
			t.Errorf("crawl4AITitle(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestCrawl4AIRedirects(t *testing.T) {
	cases := []struct {
		name     string
		statuses []int
	}{
		{"301 then 307 then 308", []int{301, 307, 308}},
		{"302 then 308", []int{302, 308}},
		{"307 then 302", []int{307, 302}},
		{"303 then 301", []int{303, 301}},
		{"303 then 307 then 308", []int{303, 307, 308}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			const token = "redirect-token-canary"
			var mu sync.Mutex
			var methods, bodies, contentTypes, auth []string
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				index := int(requests.Add(1)) - 1
				body, _ := io.ReadAll(r.Body)
				mu.Lock()
				methods = append(methods, r.Method)
				bodies = append(bodies, string(body))
				contentTypes = append(contentTypes, r.Header.Get("Content-Type"))
				auth = append(auth, r.Header.Get("Authorization"))
				mu.Unlock()
				if index < len(tc.statuses) {
					w.Header().Set("Location", fmt.Sprintf("/r%d", index+1))
					w.WriteHeader(tc.statuses[index])
					return
				}
				w.Header().Set("Content-Type", "application/json")
				io.WriteString(w, `{"success":true,"markdown":"redirect-ok"}`)
			}))
			defer server.Close()

			cfg := crawl4AIConfig(server.URL)
			cfg.AllowRanges = nil
			cfg.Crawl4AIToken = token
			client := testClient(cfg)
			page, err := client.extractCrawl4AI(context.Background(), mustURL(server.URL+"/md"))
			if err != nil {
				t.Fatalf("redirect chain failed: %v", err)
			}
			if page.Text != "redirect-ok" {
				t.Fatalf("page text = %q", page.Text)
			}
			mu.Lock()
			defer mu.Unlock()
			if len(methods) != len(tc.statuses)+1 {
				t.Fatalf("requests = %d, want %d", len(methods), len(tc.statuses)+1)
			}
			bodylessAfter303 := tc.statuses[0] == http.StatusSeeOther
			wantBody := `{"url":"` + server.URL + `/md","f":"fit"}`
			for i := range methods {
				wantMethod := http.MethodPost
				wantBodyValue, wantType := wantBody, "application/json"
				if i > 0 && bodylessAfter303 {
					wantMethod = http.MethodGet
					wantBodyValue, wantType = "", ""
				}
				if methods[i] != wantMethod || bodies[i] != wantBodyValue || auth[i] != "Bearer "+token {
					t.Fatalf("hop %d method=%s body=%d auth=%t, want method=%s body=%d auth=true", i+1, methods[i], len(bodies[i]), auth[i] != "", wantMethod, len(wantBodyValue))
				}
				if contentTypes[i] != wantType {
					t.Fatalf("hop %d Content-Type = %q, want %q", i+1, contentTypes[i], wantType)
				}
			}
		})
	}
}

func TestCrawl4AIRedirectClosesBodies(t *testing.T) {
	var closed [3]atomic.Bool
	calls := 0
	client := testClient(crawl4AIConfig("https://crawl.example"))
	client.extractor = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		index := calls
		calls++
		header := http.Header{"Content-Type": {"application/json"}}
		status := http.StatusOK
		payload := `{"success":true,"markdown":"closed"}`
		if index == 0 {
			status = http.StatusMovedPermanently
			header.Set("Location", "/r1")
			payload = "redirect one"
		} else if index == 1 {
			status = http.StatusFound
			header.Set("Location", "/r2")
			payload = "redirect two"
		}
		if index > 2 {
			t.Fatal("unexpected extra extraction request")
		}
		return &http.Response{StatusCode: status, Header: header, Body: &trackingReadCloser{Reader: strings.NewReader(payload), closed: &closed[index]}, Request: r}, nil
	})}
	if _, err := client.extractCrawl4AI(context.Background(), mustURL("https://example.com/page")); err != nil {
		t.Fatal(err)
	}
	for i := range closed {
		if !closed[i].Load() {
			t.Fatalf("redirect response body %d was not closed", i+1)
		}
	}
}

func TestCrawl4AIRedirectLimit(t *testing.T) {
	chain := func(t *testing.T, redirects int) (*httptest.Server, *atomic.Int32) {
		t.Helper()
		var calls atomic.Int32
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			index := int(calls.Add(1)) - 1
			if index < redirects {
				http.Redirect(w, r, fmt.Sprintf("/r%d", index+1), http.StatusFound)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, `{"success":true,"markdown":"chain-ok"}`)
		}))
		t.Cleanup(server.Close)
		return server, &calls
	}

	server, calls := chain(t, 5)
	client := testClient(crawl4AIConfig(server.URL))
	if _, err := client.extractCrawl4AI(context.Background(), mustURL(server.URL+"/md")); err != nil {
		t.Fatalf("five redirects rejected: %v", err)
	}
	if got := calls.Load(); got != 6 {
		t.Fatalf("five-redirect requests = %d, want 6", got)
	}

	server, calls = chain(t, 6)
	client = testClient(crawl4AIConfig(server.URL))
	_, err := client.extractCrawl4AI(context.Background(), mustURL(server.URL+"/md"))
	if err == nil || !hasClass(err.Error(), classBlocked) {
		t.Fatalf("sixth redirect error = %v", err)
	}
	if got := calls.Load(); got != 6 {
		t.Fatalf("six-redirect requests = %d, want 6 before refusal", got)
	}
}

func TestCrawl4AICrossOriginRedirect(t *testing.T) {
	var targetCalls atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		targetCalls.Add(1)
		io.WriteString(w, `{"success":true,"markdown":"wrong-origin"}`)
	}))
	defer target.Close()
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+"/md", http.StatusFound)
	}))
	defer source.Close()
	cfg := crawl4AIConfig(source.URL)
	cfg.Crawl4AIToken = "cross-origin-token"
	client := testClient(cfg)
	_, err := client.extractCrawl4AI(context.Background(), mustURL("https://example.com/page"))
	if err == nil || !hasClass(err.Error(), classBlocked) {
		t.Fatalf("cross-origin redirect error = %v", err)
	}
	if got := targetCalls.Load(); got != 0 {
		t.Fatalf("cross-origin destination received %d requests", got)
	}
}

func TestExtractorRedirectReplay(t *testing.T) {
	const body = `{"url":"https://example.com/page","f":"fit"}`
	t.Run("replays post and closes replaced body", func(t *testing.T) {
		closed := atomic.Bool{}
		req := httptest.NewRequest(http.MethodGet, "https://crawl.example/r1", nil)
		req.Body = &trackingReadCloser{Reader: strings.NewReader("redirect response body"), closed: &closed}
		req.Response = &http.Response{StatusCode: http.StatusMovedPermanently}
		original := &http.Request{
			Method:        http.MethodPost,
			URL:           req.URL,
			ContentLength: int64(len(body)),
			GetBody: func() (io.ReadCloser, error) {
				return io.NopCloser(strings.NewReader(body)), nil
			},
		}
		if err := extractorRedirect(req, []*http.Request{original}); err != nil {
			t.Fatalf("redirect replay failed: %v", err)
		}
		if !closed.Load() {
			t.Fatal("replaced request body was not closed")
		}
		if req.Method != http.MethodPost || req.ContentLength != int64(len(body)) || req.GetBody == nil {
			t.Fatal("replayed request metadata is incomplete")
		}
		got, _ := io.ReadAll(req.Body)
		if string(got) != body {
			t.Fatal("replayed body differs")
		}
	})

	for _, tc := range []struct {
		name    string
		getBody func() (io.ReadCloser, error)
	}{
		{"missing GetBody", nil},
		{"failing GetBody", func() (io.ReadCloser, error) { return nil, errors.New("replay failed") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "https://crawl.example/r1", nil)
			req.Response = &http.Response{StatusCode: http.StatusFound}
			original := &http.Request{Method: http.MethodPost, URL: req.URL, GetBody: tc.getBody}
			err := extractorRedirect(req, []*http.Request{original})
			if err == nil || !hasClass(err.Error(), classInvalid) {
				t.Fatalf("replay error = %v", err)
			}
		})
	}
}

func TestExtractorReplayErrorPreserved(t *testing.T) {
	err := classifyExtractorTransport(context.Background(), &url.Error{Op: "Post", URL: "https://crawl.example", Err: errors.New("invalid response: extractor redirect body cannot be replayed")})
	if err == nil || !hasClass(err.Error(), classInvalid) || strings.Contains(err.Error(), "crawl.example") {
		t.Fatalf("replay error = %v", err)
	}
}

func TestCrawl4AIFailurePrecedence(t *testing.T) {
	t.Run("direct timeout permits extraction", func(t *testing.T) {
		var extractorCalls atomic.Int32
		extractor := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			extractorCalls.Add(1)
			io.WriteString(w, `{"success":true,"markdown":"timeout fallback"}`)
		}))
		defer extractor.Close()
		client := testClient(crawl4AIConfig(extractor.URL))
		client.page.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
			return nil, &url.Error{Op: "Get", URL: r.URL.String(), Err: context.DeadlineExceeded}
		})
		page, err := client.fetchPage(context.Background(), "https://example.com/page", "readable")
		if err != nil || page.Text != "timeout fallback" || extractorCalls.Load() != 1 {
			t.Fatalf("timeout fallback: page=%q err=%v calls=%d", page.Text, err, extractorCalls.Load())
		}
	})

	for _, tc := range []struct {
		name string
		err  error
	}{
		{"dns", &url.Error{Op: "Post", URL: "https://crawl.example", Err: &net.DNSError{Err: "no such host", Name: "crawl.example"}}},
		{"tls", &url.Error{Op: "Post", URL: "https://crawl.example", Err: errors.New("tls: handshake failure")}},
		{"timeout", &url.Error{Op: "Post", URL: "https://crawl.example", Err: context.DeadlineExceeded}},
	} {
		t.Run("extraction "+tc.name+" preserves direct error", func(t *testing.T) {
			page := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				http.Error(w, "denied", http.StatusForbidden)
			}))
			defer page.Close()
			client := testClient(crawl4AIConfig("https://crawl.example"))
			client.extractor = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				return nil, tc.err
			})}
			_, err := client.fetchPage(context.Background(), page.URL, "readable")
			if err == nil || !strings.Contains(err.Error(), "http status: 403") {
				t.Fatalf("direct error was not preserved: %v", err)
			}
		})
	}
}

func TestCrawl4AICancellationPrecedence(t *testing.T) {
	t.Run("cancel before direct success", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		client := testClient(crawl4AIConfig("https://crawl.example"))
		client.page.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
			cancel()
			return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"text/plain"}}, Body: io.NopCloser(strings.NewReader("direct")), Request: r}, nil
		})
		_, err := client.fetchPage(ctx, "https://example.com/page", "readable")
		if err == nil || !hasClass(err.Error(), classCancelled) {
			t.Fatalf("cancelled direct success error = %v", err)
		}
	})

	for _, result := range []string{"success", "failure"} {
		t.Run("cancel before extraction "+result, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			page := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				http.Error(w, "denied", http.StatusForbidden)
			}))
			defer page.Close()
			var attempts atomic.Int32
			client := testClient(crawl4AIConfig("https://crawl.example"))
			client.extractor = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				attempts.Add(1)
				cancel()
				status := http.StatusOK
				body := `{"success":true,"markdown":"cancelled extraction"}`
				if result == "failure" {
					status = http.StatusServiceUnavailable
					body = `{"error":"cancelled"}`
				}
				return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
			})}
			_, err := client.fetchPage(ctx, page.URL, "readable")
			if err == nil || !hasClass(err.Error(), classCancelled) {
				t.Fatalf("cancelled extraction %s error = %v", result, err)
			}
			if attempts.Load() != 1 {
				t.Fatalf("cancelled extraction attempts = %d, want 1", attempts.Load())
			}
		})
	}

	t.Run("expired caller prevents extraction", func(t *testing.T) {
		var extractorCalls atomic.Int32
		extractor := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			extractorCalls.Add(1)
		}))
		defer extractor.Close()
		client := testClient(crawl4AIConfig(extractor.URL))
		ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
		defer cancel()
		_, err := client.fetchPage(ctx, "https://example.com/page", "readable")
		if err == nil || !hasClass(err.Error(), classTimeout) || extractorCalls.Load() != 0 {
			t.Fatalf("expired caller: err=%v calls=%d", err, extractorCalls.Load())
		}
	})
}

func TestCrawl4AIInFlightCancellation(t *testing.T) {
	started := make(chan struct{})
	requestCancelled := make(chan struct{})
	page := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "denied", http.StatusForbidden)
	}))
	defer page.Close()
	client := testClient(crawl4AIConfig("https://crawl.example"))
	client.extractor = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		close(started)
		<-r.Context().Done()
		close(requestCancelled)
		return nil, r.Context().Err()
	})}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan error, 1)
	go func() {
		_, err := client.fetchPage(ctx, page.URL, "readable")
		result <- err
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("extraction did not start")
	}
	cancel()
	select {
	case <-requestCancelled:
	case <-time.After(5 * time.Second):
		t.Fatal("extractor request context was not cancelled")
	}
	select {
	case err := <-result:
		if err == nil || !hasClass(err.Error(), classCancelled) {
			t.Fatalf("in-flight cancellation error = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("cancelled extraction did not return")
	}
}

func TestCrawl4AIBudgets(t *testing.T) {
	client := testClient(crawl4AIConfig("https://crawl.example"))
	pageTransport, ok := client.page.Transport.(*http.Transport)
	if !ok || pageTransport.ResponseHeaderTimeout != 30*time.Second || pageTransport.Proxy != nil {
		t.Fatal("page transport budget or proxy changed")
	}
	providerTransport, ok := client.provider.Transport.(*http.Transport)
	if !ok || providerTransport.ResponseHeaderTimeout != 30*time.Second || providerTransport.Proxy != nil {
		t.Fatal("provider transport budget or proxy changed")
	}
	extractorTransport, ok := client.extractor.Transport.(*http.Transport)
	if !ok || extractorTransport.ResponseHeaderTimeout != crawl4AIExtractionTimeout || extractorTransport.Proxy != nil {
		t.Fatal("extractor transport budget or proxy is wrong")
	}

	deadlineCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	callerDeadline, _ := deadlineCtx.Deadline()
	var requestedDeadline time.Time
	client.extractor = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		var ok bool
		requestedDeadline, ok = r.Context().Deadline()
		if !ok {
			t.Error("extraction request has no deadline")
		}
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"success":true,"markdown":"budget"}`)), Request: r}, nil
	})}
	target := mustURL("https://example.com/page")
	if _, err := client.extractCrawl4AI(deadlineCtx, target); err != nil {
		t.Fatal(err)
	}
	if !requestedDeadline.Equal(callerDeadline) {
		t.Fatal("extraction deadline was not bounded by the caller")
	}

	var backgroundDeadline time.Time
	client.extractor = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		backgroundDeadline, _ = r.Context().Deadline()
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"success":true,"markdown":"budget"}`)), Request: r}, nil
	})}
	if _, err := client.extractCrawl4AI(context.Background(), target); err != nil {
		t.Fatal(err)
	}
	if remaining := time.Until(backgroundDeadline); remaining <= 0 || remaining > crawl4AIExtractionTimeout {
		t.Fatal("extraction deadline is not the 60-second budget")
	}
}

func TestCrawl4AIBodyReadErrorAndEncoding(t *testing.T) {
	t.Run("body read error closes and stays internal", func(t *testing.T) {
		closed := atomic.Bool{}
		client := testClient(crawl4AIConfig("https://crawl.example"))
		client.extractor = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: &trackingReadCloser{Reader: iotest.ErrReader(errors.New("READ-ERROR-CANARY")), closed: &closed}, Request: r}, nil
		})}
		_, err := client.extractCrawl4AI(context.Background(), mustURL("https://example.com/page"))
		if err == nil || !hasClass(err.Error(), classInvalid) || strings.Contains(err.Error(), "READ-ERROR-CANARY") {
			t.Fatalf("read error = %v", err)
		}
		if !closed.Load() {
			t.Fatal("body read error did not close the body")
		}
	})

	t.Run("unsupported encoding", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Encoding", "br")
			io.WriteString(w, `{"success":true,"markdown":"encoded"}`)
		}))
		defer server.Close()
		client := testClient(crawl4AIConfig(server.URL))
		_, err := client.extractCrawl4AI(context.Background(), mustURL("https://example.com/page"))
		if err == nil || !hasClass(err.Error(), classInvalid) {
			t.Fatalf("unsupported encoding error = %v", err)
		}
	})
}

func TestCrawl4AITitleAndRedaction(t *testing.T) {
	const token = "SECRET-TOKEN"
	target := mustURL("https://example.com/page?key=" + token)
	markdown := "# " + strings.Repeat("a", 500) + token + "\n\nbody " + token
	cfg := crawl4AIConfig("https://crawl.example")
	cfg.Crawl4AIToken = token
	cfg.MaxInlineChars = 8
	client := testClient(cfg)
	client.page.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusForbidden, Header: http.Header{}, Body: io.NopCloser(strings.NewReader("denied")), Request: r}, nil
	})
	client.extractor = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		wanted := `{"url":"` + target.String() + `","f":"fit"}`
		body, _ := io.ReadAll(r.Body)
		if string(body) != wanted {
			t.Error("extraction URL was redacted before transmission")
		}
		payload, _ := json.Marshal(map[string]any{"success": true, "markdown": markdown})
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(string(payload))), Request: r}, nil
	})}
	page, err := client.extractCrawl4AI(context.Background(), target)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(page.Text, token) || strings.Contains(page.Title, token) || strings.Contains(page.URL, token) || strings.Contains(page.FinalURL, token) {
		t.Fatalf("full token leaked: text=%t title=%t url=%t final=%t", strings.Contains(page.Text, token), strings.Contains(page.Title, token), strings.Contains(page.URL, token), strings.Contains(page.FinalURL, token))
	}
	if strings.Contains(page.Text, token[:4]) || strings.Contains(page.Title, token[:4]) {
		t.Fatal("token prefix leaked from extraction fields")
	}
	if !strings.Contains(page.Text, redactionMarker) {
		t.Fatal("markdown token was not redacted")
	}

	result := client.Dispatch(context.Background(), toolsInvocation(ToolFetch, map[string]any{"url": target.String(), "mode": "readable"}))
	if result.IsError || strings.Contains(result.Content[0].Text, token) {
		t.Fatalf("public result leaked or failed: %s", result.Content[0].Text)
	}
	if strings.Contains(result.Content[0].Text, token[:4]) {
		t.Fatal("public result leaked a token prefix")
	}
	details := batchDetails(t, result)
	id, _ := details["responseId"].(string)
	detailURL, _ := details["url"].(string)
	detailTitle, _ := details["title"].(string)
	if id == "" || strings.Contains(detailURL, token) || strings.Contains(detailTitle, token) || strings.Contains(detailURL, token[:4]) || strings.Contains(detailTitle, token[:4]) {
		t.Fatal("public details leaked a token or lost the response ID")
	}
	retrieved := getContent(t, client, map[string]any{"responseId": id})
	if retrieved.IsError || strings.Contains(retrieved.Content[0].Text, token) || strings.Contains(retrieved.Content[0].Text, token[:4]) {
		t.Fatalf("retained content leaked a token or failed: %s", retrieved.Content[0].Text)
	}
}

func TestCrawl4AIGzipCap(t *testing.T) {
	for _, tc := range []struct {
		name   string
		size   int
		accept bool
	}{
		{"exact cap after gzip", maxBodyBytes, true},
		{"cap plus one after gzip", maxBodyBytes + 1, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := envelopeOfSize(t, tc.size)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Encoding", "gzip")
				w.Header().Set("Content-Type", "application/json")
				zw := gzip.NewWriter(w)
				_, _ = io.WriteString(zw, body)
				_ = zw.Close()
			}))
			defer server.Close()
			client := testClient(crawl4AIConfig(server.URL))
			_, err := client.extractCrawl4AI(context.Background(), mustURL("https://example.com/page"))
			if tc.accept && err != nil {
				t.Fatalf("at-cap gzip rejected: %v", err)
			}
			if !tc.accept && (err == nil || !hasClass(err.Error(), classInvalid)) {
				t.Fatalf("over-cap gzip error = %v", err)
			}
		})
	}

	t.Run("corrupt gzip", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Encoding", "gzip")
			io.WriteString(w, "not a gzip stream")
		}))
		defer server.Close()
		client := testClient(crawl4AIConfig(server.URL))
		_, err := client.extractCrawl4AI(context.Background(), mustURL("https://example.com/page"))
		if err == nil || !hasClass(err.Error(), classInvalid) {
			t.Fatalf("corrupt gzip error = %v", err)
		}
	})
}

func TestCrawl4AIBatch(t *testing.T) {
	var extractorCalls atomic.Int32
	direct := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		io.WriteString(w, "DIRECT-ONE")
	}))
	defer direct.Close()
	page := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "denied", http.StatusForbidden)
	}))
	defer page.Close()
	extractor := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		extractorCalls.Add(1)
		var body struct {
			URL string `json:"url"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("extractor body: %v", err)
		}
		if strings.HasSuffix(body.URL, "/fail") {
			http.Error(w, "failed", http.StatusServiceUnavailable)
			return
		}
		io.WriteString(w, `{"success":true,"markdown":"EXTRACTED-TWO"}`)
	}))
	defer extractor.Close()
	client := testClient(crawl4AIConfig(extractor.URL))
	urls := []string{direct.URL + "/a", page.URL + "/ok", page.URL + "/fail"}
	result := client.Dispatch(context.Background(), toolsInvocation(ToolFetch, map[string]any{"urls": urls, "mode": "readable"}))
	if result.IsError {
		t.Fatalf("mixed batch failed: %s", result.Content[0].Text)
	}
	text := result.Content[0].Text
	for _, want := range []string{"ok: 2", "failed: 1", "incomplete: true"} {
		if !strings.Contains(text, want) {
			t.Fatalf("batch result missing %q:\n%s", want, text)
		}
	}
	one := strings.Index(text, "1. url: "+direct.URL+"/a")
	two := strings.Index(text, "2. url: "+page.URL+"/ok")
	three := strings.Index(text, "3. url: "+page.URL+"/fail")
	if one < 0 || two < one || three < two {
		t.Fatalf("batch order changed:\n%s", text)
	}
	id, _ := batchDetails(t, result)["responseId"].(string)
	entry, ok := client.store.Get(id)
	if !ok || !entry.Incomplete || len(entry.Pages) != 3 {
		t.Fatalf("stored batch = %+v", entry)
	}
	if entry.Pages[0].Text != "DIRECT-ONE" || entry.Pages[1].Text != "EXTRACTED-TWO" || !strings.Contains(entry.Pages[2].Error, "http status: 403") {
		t.Fatal("stored batch text or original per-URL error changed")
	}
	if got := extractorCalls.Load(); got != 2 {
		t.Fatalf("extractor calls = %d, want 2", got)
	}
}

func TestCrawl4AIInlineSearch(t *testing.T) {
	t.Run("extracted and retained", func(t *testing.T) {
		var searches, pages, extractions atomic.Int32
		page := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			pages.Add(1)
			http.Error(w, "denied", http.StatusForbidden)
		}))
		defer page.Close()
		extractor := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			extractions.Add(1)
			io.WriteString(w, `{"success":true,"markdown":"INLINE-EXTRACTED"}`)
		}))
		defer extractor.Close()
		var search *httptest.Server
		search = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			searches.Add(1)
			fmt.Fprintf(w, `{"results":[{"url":%q,"title":"Result"}]}`, page.URL+"/page")
		}))
		defer search.Close()
		cfg := crawl4AIConfig(extractor.URL)
		cfg.SearXNGBaseURL = search.URL
		cfg.SearXNGAvailable = true
		cfg.SearXNGRequested = true
		client := testClient(cfg)
		result := client.Dispatch(context.Background(), toolsInvocation(ToolSearch, map[string]any{"query": "q", "includeContent": true}))
		if result.IsError || batchDetails(t, result)["fetched"] != 1 {
			t.Fatalf("inline search = %s", result.Content[0].Text)
		}
		if searches.Load() != 1 || pages.Load() != 1 || extractions.Load() != 1 {
			t.Fatalf("inline requests: search=%d page=%d extractor=%d", searches.Load(), pages.Load(), extractions.Load())
		}
		id, _ := batchDetails(t, result)["responseId"].(string)
		retrieved := getContent(t, client, map[string]any{"responseId": id, "urlIndex": 0})
		if retrieved.IsError || !strings.Contains(retrieved.Content[0].Text, "INLINE-EXTRACTED") {
			t.Fatalf("inline retained text not retrievable: %s", retrieved.Content[0].Text)
		}
	})

	t.Run("failed extraction marks incomplete", func(t *testing.T) {
		var extractions atomic.Int32
		page := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "denied", http.StatusForbidden)
		}))
		defer page.Close()
		extractor := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			extractions.Add(1)
			http.Error(w, "failed", http.StatusServiceUnavailable)
		}))
		defer extractor.Close()
		search := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprintf(w, `{"results":[{"url":%q,"title":"Result"}]}`, page.URL+"/page")
		}))
		defer search.Close()
		cfg := crawl4AIConfig(extractor.URL)
		cfg.SearXNGBaseURL = search.URL
		cfg.SearXNGAvailable = true
		cfg.SearXNGRequested = true
		client := testClient(cfg)
		result := client.Dispatch(context.Background(), toolsInvocation(ToolSearch, map[string]any{"query": "q", "includeContent": true}))
		if result.IsError || batchDetails(t, result)["incomplete"] != true {
			t.Fatalf("failed inline extraction should keep search successful but incomplete: %s", result.Content[0].Text)
		}
		if extractions.Load() != 1 {
			t.Fatalf("extractor calls = %d, want 1", extractions.Load())
		}
	})

	t.Run("raw mode skips extraction", func(t *testing.T) {
		var extractions atomic.Int32
		page := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "denied", http.StatusForbidden)
		}))
		defer page.Close()
		extractor := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			extractions.Add(1)
			io.WriteString(w, `{"success":true,"markdown":"wrong"}`)
		}))
		defer extractor.Close()
		search := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprintf(w, `{"results":[{"url":%q}]}`, page.URL+"/page")
		}))
		defer search.Close()
		cfg := crawl4AIConfig(extractor.URL)
		cfg.FetchMode = "raw"
		cfg.SearXNGBaseURL = search.URL
		cfg.SearXNGAvailable = true
		cfg.SearXNGRequested = true
		client := testClient(cfg)
		result := client.Dispatch(context.Background(), toolsInvocation(ToolSearch, map[string]any{"query": "q", "includeContent": true}))
		if result.IsError || batchDetails(t, result)["incomplete"] != true {
			t.Fatalf("raw inline result = %s", result.Content[0].Text)
		}
		if extractions.Load() != 0 {
			t.Fatalf("raw mode made %d extraction requests", extractions.Load())
		}
	})
}

func mustURL(raw string) *url.URL {
	u, err := url.Parse(raw)
	if err != nil {
		panic(err)
	}
	return u
}

type trackingReadCloser struct {
	io.Reader
	closed *atomic.Bool
}

func (r *trackingReadCloser) Close() error {
	r.closed.Store(true)
	return nil
}

func strconvQuote(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

func envelopeOfSize(t *testing.T, size int) string {
	t.Helper()
	const prefix = `{"success":true,"markdown":"`
	const suffix = `"}`
	if size < len(prefix)+len(suffix) {
		t.Fatalf("envelope size %d is too small", size)
	}
	return prefix + strings.Repeat("a", size-len(prefix)-len(suffix)) + suffix
}
