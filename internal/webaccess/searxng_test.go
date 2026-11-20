package webaccess

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"testing/iotest"

	"github.com/hypernewbie/psi/internal/tools"
)

func searxConfig(t *testing.T, serverURL string) Config {
	cfg := testConfig()
	cfg.Provider = "auto"
	cfg.SearXNGBaseURL = serverURL
	cfg.SearXNGAvailable = true
	cfg.MaxInlineChars = 2000
	return cfg
}

func searchArgsFor(query string) map[string]any {
	return map[string]any{"query": query, "provider": "searxng"}
}

func TestSearXNGRequestShapeAndProjection(t *testing.T) {
	var gotPath, gotQuery, gotFormat, gotHeader string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotQuery, gotFormat = r.URL.Path, r.URL.Query().Get("q"), r.URL.Query().Get("format")
		gotHeader = r.Header.Get("X-Api-Key")
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"results":[
			{"title":"One","url":"https://one.example/a","content":"snippet one","engines":["alpha","beta"],"publishedDate":"2026-01-02"},
			{"title":null,"url":"https://two.example/b","content":null,"engines":null},
			{"title":"Bad","url":"not a url"},
			{"title":"Port zero","url":"http://zero.example:0/"},
			{"title":"Credentials","url":"http://user:pass@secret.example/"},
			{"title":"Overlong","url":"https://three.example/`+strings.Repeat("x", maxURLBytes)+`"}
		]}`)
	}))
	defer server.Close()

	cfg := searxConfig(t, server.URL+"/base/")
	cfg.SearXNGHeaders = http.Header{"X-Api-Key": []string{"provider-secret"}}
	client := testClient(cfg)
	result := client.Dispatch(context.Background(), toolsInvocation(ToolSearch, searchArgsFor("go net/http")))
	if result.IsError {
		t.Fatalf("search failed: %s", result.Content[0].Text)
	}
	if gotPath != "/base/search" || gotQuery != "go net/http" || gotFormat != "json" {
		t.Fatalf("request path=%q query=%q format=%q", gotPath, gotQuery, gotFormat)
	}
	if gotHeader != "provider-secret" {
		t.Fatalf("provider header missing: %q", gotHeader)
	}
	text := result.Content[0].Text
	if !strings.Contains(text, "One") || !strings.Contains(text, "https://two.example/b") {
		t.Fatalf("results:\n%s", text)
	}
	for _, bad := range []string{"not a url", "Port zero", "Credentials", "Overlong"} {
		if strings.Contains(text, bad) {
			t.Fatalf("malformed URL %q survived:\n%s", bad, text)
		}
	}
	if !strings.Contains(text, "engines: alpha, beta") {
		t.Fatalf("engines:\n%s", text)
	}
}

func TestSearXNGResultLimitAndEmptyResults(t *testing.T) {
	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		items := make([]string, 0, 10)
		for i := 0; i < 10; i++ {
			items = append(items, fmt.Sprintf(`{"title":"R%d","url":"https://r%d.example/"}`, i, i))
		}
		fmt.Fprintf(w, `{"results":[%s]}`, strings.Join(items, ","))
	}))
	defer server.Close()
	client := testClient(searxConfig(t, server.URL))
	result := client.Dispatch(context.Background(), toolsInvocation(ToolSearch, map[string]any{"query": "q", "provider": "searxng", "numResults": 3}))
	if result.IsError {
		t.Fatalf("search: %s", result.Content[0].Text)
	}
	if strings.Count(result.Content[0].Text, "https://r") != 3 {
		t.Fatalf("limit not applied:\n%s", result.Content[0].Text)
	}

	empty := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"results":[],"unresponsive_engines":[["google","rate limited"],["bing","captcha"]]}`)
	}))
	defer empty.Close()
	result = testClient(searxConfig(t, empty.URL)).Dispatch(context.Background(), toolsInvocation(ToolSearch, searchArgsFor("none")))
	if result.IsError {
		t.Fatalf("empty search should succeed: %s", result.Content[0].Text)
	}
	if !strings.Contains(result.Content[0].Text, "unresponsive engines: google: rate limited; bing: captcha") {
		t.Fatalf("degradation missing:\n%s", result.Content[0].Text)
	}
	if calls != 1 {
		t.Fatalf("calls = %d", calls)
	}
}

func TestSearXNGInvalidResponses(t *testing.T) {
	cases := map[string]string{
		"http error object": `{"error":"engine failure"}`,
		"missing results":   `{}`,
		"null results":      `{"results":null}`,
		"object results":    `{"results":{}}`,
		"null root":         `null`,
		"trailing data":     `{"results":[]} extra`,
		"malformed":         `{"results":`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				io.WriteString(w, body)
			}))
			defer server.Close()
			result := testClient(searxConfig(t, server.URL)).Dispatch(context.Background(), toolsInvocation(ToolSearch, searchArgsFor("q")))
			if !result.IsError {
				t.Fatalf("expected error, got:\n%s", result.Content[0].Text)
			}
			if strings.Contains(result.Content[0].Text, body) && body != "" {
				t.Fatalf("raw provider body leaked: %s", result.Content[0].Text)
			}
		})
	}
}

func TestSearXNGStatusAndOversize(t *testing.T) {
	for _, status := range []int{http.StatusForbidden, http.StatusTooManyRequests, http.StatusInternalServerError} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(status)
				io.WriteString(w, `{"results":[]}`)
			}))
			defer server.Close()
			result := testClient(searxConfig(t, server.URL)).Dispatch(context.Background(), toolsInvocation(ToolSearch, searchArgsFor("q")))
			if !result.IsError || !strings.Contains(result.Content[0].Text, fmt.Sprint(status)) {
				t.Fatalf("result = %+v", result)
			}
		})
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, strings.Repeat(" ", maxProviderBody+1))
	}))
	defer server.Close()
	result := testClient(searxConfig(t, server.URL)).Dispatch(context.Background(), toolsInvocation(ToolSearch, searchArgsFor("q")))
	if !result.IsError || !strings.Contains(result.Content[0].Text, "5 MiB") {
		t.Fatalf("oversize result = %+v", result)
	}
}

func TestSearXNGReadError(t *testing.T) {
	client := testClient(searxConfig(t, "https://searx.example"))
	client.provider = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(iotest.ErrReader(fmt.Errorf("boom"))), Request: r}, nil
	})}
	result := client.Dispatch(context.Background(), toolsInvocation(ToolSearch, searchArgsFor("q")))
	if !result.IsError || !strings.Contains(result.Content[0].Text, classInvalid) {
		t.Fatalf("result = %+v", result)
	}
}

func TestSearXNGHeadersStayOnProviderOrigin(t *testing.T) {
	var otherRequests int
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		otherRequests++
	}))
	defer other.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL+"/steal", http.StatusFound)
	}))
	defer server.Close()
	cfg := searxConfig(t, server.URL)
	cfg.SearXNGHeaders = http.Header{"X-Api-Key": []string{"provider-secret"}}
	result := testClient(cfg).Dispatch(context.Background(), toolsInvocation(ToolSearch, searchArgsFor("q")))
	if !result.IsError {
		t.Fatal("cross-origin redirect must fail")
	}
	if otherRequests != 0 {
		t.Fatalf("second origin received %d requests", otherRequests)
	}
}

func TestSearchRoutingAndNoFallback(t *testing.T) {
	// auto picks the configured SearXNG origin.
	var hits int
	searx := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		fmt.Fprint(w, `{"results":[]}`)
	}))
	defer searx.Close()
	cfg := searxConfig(t, searx.URL)
	client := testClient(cfg)
	if _, err := client.selectSearchProvider(""); err != nil || hits != 0 {
		t.Fatalf("select provider err = %v hits = %d", err, hits)
	}
	result := client.Dispatch(context.Background(), toolsInvocation(ToolSearch, map[string]any{"query": "q"}))
	if result.IsError || hits != 1 {
		t.Fatalf("auto search failed: %+v hits=%d", result, hits)
	}

	// A provider error does not trigger a second request.
	failing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer failing.Close()
	client = testClient(searxConfig(t, failing.URL))
	result = client.Dispatch(context.Background(), toolsInvocation(ToolSearch, searchArgsFor("q")))
	if !result.IsError || hits != 2 {
		t.Fatalf("fallback happened: %+v hits=%d", result, hits)
	}

	// Explicit selection of an unavailable provider errors.
	cfg = testConfig()
	cfg.SearXNGBaseURL = ""
	cfg.SearXNGAvailable = false
	cfg.ExaAvailable = false
	client = testClient(cfg)
	if _, err := client.selectSearchProvider("searxng"); err == nil {
		t.Fatal("missing SearXNG must error")
	}
	if _, err := client.selectSearchProvider("exa"); err == nil {
		t.Fatal("missing Exa must error")
	}
}

func TestSearchStoredOverviewRoundTrip(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"results":[{"title":"Go docs","url":"https://go.dev/doc/","content":"official docs","engines":["google"]}]}`)
	}))
	defer server.Close()
	client := testClient(searxConfig(t, server.URL))
	search := client.Dispatch(context.Background(), toolsInvocation(ToolSearch, searchArgsFor("go")))
	if search.IsError {
		t.Fatalf("search: %s", search.Content[0].Text)
	}
	id := extractResponseID(t, search.Content[0].Text)
	page := getContent(t, client, map[string]any{"responseId": id})
	if page.IsError {
		t.Fatalf("overview retrieval: %s", page.Content[0].Text)
	}
	if !strings.Contains(page.Content[0].Text, "https://go.dev/doc/") {
		t.Fatalf("overview:\n%s", page.Content[0].Text)
	}
	if !strings.Contains(page.Content[0].Text, "kind: search") {
		t.Fatalf("kind:\n%s", page.Content[0].Text)
	}
}

func TestSearchDisabledMakesNoRequest(t *testing.T) {
	cfg := searxConfig(t, "https://searx.example")
	cfg.SearchEnabled = false
	client := testClient(cfg)
	called := false
	client.provider = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		called = true
		return nil, fmt.Errorf("unexpected")
	})}
	result := client.Dispatch(context.Background(), toolsInvocation(ToolSearch, searchArgsFor("q")))
	if !result.IsError || called {
		t.Fatalf("disabled search: error=%v called=%v", result.IsError, called)
	}
}

func TestSearchArgumentValidation(t *testing.T) {
	client := testClient(searxConfig(t, "https://searx.example"))
	for name, args := range map[string]map[string]any{
		"missing query":  {"provider": "searxng"},
		"blank query":    {"query": "   "},
		"big query":      {"query": strings.Repeat("q", 4097)},
		"zero results":   {"query": "q", "numResults": 0},
		"too many":       {"query": "q", "numResults": 21},
		"bad provider":   {"query": "q", "provider": "bing"},
		"includeContent": {"query": "q", "includeContent": true},
	} {
		result := client.Dispatch(context.Background(), toolsInvocation(ToolSearch, args))
		if !result.IsError {
			t.Errorf("%s should fail", name)
		}
	}
}

func TestManyEngineNamesStayBounded(t *testing.T) {
	engines := make([]string, 0, 40)
	for i := 0; i < 40; i++ {
		engines = append(engines, strings.Repeat("e", 200))
	}
	encoded, _ := json.Marshal(engines)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"results":[{"title":"t","url":"https://x.example/","engines":%s}]}`, encoded)
	}))
	defer server.Close()
	client := testClient(searxConfig(t, server.URL))
	result := client.Dispatch(context.Background(), toolsInvocation(ToolSearch, searchArgsFor("q")))
	if result.IsError {
		t.Fatalf("search: %s", result.Content[0].Text)
	}
	id := extractResponseID(t, result.Content[0].Text)
	entry, ok := client.store.Get(id)
	if !ok {
		t.Fatal("entry missing")
	}
	if len(entry.Results[0].Engines) > 600 {
		t.Fatalf("engines len = %d", len(entry.Results[0].Engines))
	}
}

func toolsInvocation(name string, args map[string]any) tools.Invocation {
	return tools.Invocation{Name: name, Arguments: args}
}

func TestProviderTimeoutClassification(t *testing.T) {
	client := testClient(searxConfig(t, "https://searx.example"))
	client.provider = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return nil, &url.Error{Op: "Get", URL: r.URL.String(), Err: context.DeadlineExceeded}
	})}
	result := client.Dispatch(context.Background(), toolsInvocation(ToolSearch, map[string]any{"query": "q"}))
	if !result.IsError || !strings.Contains(result.Content[0].Text, classTimeout) {
		t.Fatalf("result = %s", result.Content[0].Text)
	}
}

func TestSearchRedactsUnresponsiveEngineDetails(t *testing.T) {
	for _, tc := range []struct {
		name, secret, reason string
	}{
		{"whole secret", "header-secret", "header-secret"},
		{"clipping boundary", "header-secret", strings.Repeat("r", 200-len("google: ")-len("[redacted]")) + "header-secret"},
		{"marker substring", "redacted", "redacted"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				fmt.Fprintf(w, `{"results":[],"unresponsive_engines":[["google",%q]]}`, tc.reason)
			}))
			defer server.Close()
			cfg := searxConfig(t, server.URL)
			cfg.SearXNGHeaders = map[string][]string{"X-Token": {tc.secret}}
			client := testClient(cfg)
			defer client.CloseIdle()
			result := client.Dispatch(context.Background(), toolsInvocation(ToolSearch, map[string]any{"query": "q"}))
			if result.IsError {
				t.Fatalf("search: %s", result.Content[0].Text)
			}
			text := result.Content[0].Text
			if strings.Contains(text, "header-sec") || strings.Contains(text, "[[redacted]]") || strings.Count(text, "[redacted]") != 1 {
				t.Fatalf("unresponsive detail was not redacted exactly once before clipping: %s", text)
			}
		})
	}
}

func TestProviderDNSClassification(t *testing.T) {
	client := testClient(searxConfig(t, "https://searx.example"))
	client.provider = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return nil, fmt.Errorf("%s: lookup failed", classDNS)
	})}
	result := client.Dispatch(context.Background(), toolsInvocation(ToolSearch, map[string]any{"query": "q"}))
	if !result.IsError || !strings.Contains(result.Content[0].Text, classDNS) {
		t.Fatalf("result = %s", result.Content[0].Text)
	}
}

func TestProviderHostNamedDNSStillTransport(t *testing.T) {
	client := testClient(searxConfig(t, "https://dns.example"))
	client.provider = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return nil, &url.Error{Op: "Get", URL: r.URL.String(), Err: fmt.Errorf("dial failed")}
	})}
	result := client.Dispatch(context.Background(), toolsInvocation(ToolSearch, map[string]any{"query": "q"}))
	if !result.IsError || !strings.Contains(result.Content[0].Text, classTransport) || strings.Contains(result.Content[0].Text, classDNS) {
		t.Fatalf("result = %s", result.Content[0].Text)
	}
}

func TestProviderWrappedDialTimeoutKeepsClass(t *testing.T) {
	client := testClient(searxConfig(t, "https://searx.example"))
	client.provider = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return nil, fmt.Errorf("timeout: dial interrupted")
	})}
	result := client.Dispatch(context.Background(), toolsInvocation(ToolSearch, map[string]any{"query": "q"}))
	if !result.IsError || !strings.Contains(result.Content[0].Text, classTimeout) {
		t.Fatalf("result = %s", result.Content[0].Text)
	}
}

func TestProviderRedirectRejectsInvalidTargets(t *testing.T) {
	origin := &url.URL{Scheme: "https", Host: "searx.example"}
	req := &http.Request{URL: &url.URL{
		Scheme: "https",
		User:   url.UserPassword("user", "secret"),
		Host:   "searx.example",
		Path:   "/redirected",
	}}
	via := []*http.Request{{URL: origin}}
	if err := providerRedirect(req, via); err == nil {
		t.Fatal("provider redirect with credentials was accepted")
	}
}
