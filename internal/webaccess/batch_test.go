package webaccess

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hypernewbie/psi/internal/tools"
)

func batchDetails(t *testing.T, result tools.Result) map[string]any {
	t.Helper()
	details, ok := result.Details.(map[string]any)
	if !ok {
		t.Fatalf("details type = %T", result.Details)
	}
	return details
}

func batchServer(t *testing.T, handler func(w http.ResponseWriter, r *http.Request)) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(handler))
	t.Cleanup(server.Close)
	return server
}

func TestBatchArgumentValidation(t *testing.T) {
	cases := map[string]map[string]any{
		"fetch both":          {"url": "https://a.example/", "urls": []string{"https://b.example/"}},
		"fetch neither":       {},
		"fetch empty":         {"urls": []string{}},
		"fetch too many":      {"urls": make([]string, 21)},
		"fetch blank entry":   {"urls": []string{"https://a.example/", " "}},
		"search both":         {"query": "a", "queries": []string{"b"}},
		"search empty":        {"queries": []string{}},
		"search too many":     {"queries": []string{"a", "b", "c", "d"}},
		"search bad number":   {"query": "a", "numResults": 1.5},
		"search zero results": {"query": "a", "numResults": 0},
		"search bad recency":  {"query": "a", "recencyFilter": "week"},
		"search bad domain":   {"query": "a", "domainFilter": []string{"https://a.example/"}},
		"search bad wildcard": {"query": "a", "domainFilter": []string{"*.a.example"}},
	}
	for name, args := range cases {
		t.Run(name, func(t *testing.T) {
			client := testClient(testConfig())
			tool := ToolFetch
			if strings.Contains(name, "search") {
				tool = ToolSearch
			}
			result := client.Dispatch(context.Background(), toolsInvocation(tool, args))
			if !result.IsError || !strings.Contains(result.Content[0].Text, classValidation) {
				t.Fatalf("expected validation error, got:\n%s", result.Content[0].Text)
			}
		})
	}

	var validHits int64
	valid := batchServer(t, func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&validHits, 1)
		fmt.Fprint(w, "<html><body><p>ok</p></body></html>")
	})
	client := testClient(testConfig())
	result := client.Dispatch(context.Background(), toolsInvocation(ToolFetch, map[string]any{"urls": []string{valid.URL, valid.URL + "/two"}}))
	if result.IsError {
		t.Fatalf("valid batch rejected: %s", result.Content[0].Text)
	}
	hitsBefore := atomic.LoadInt64(&validHits)
	result = client.Dispatch(context.Background(), toolsInvocation(ToolFetch, map[string]any{"urls": []string{valid.URL, "ftp://a.example/"}}))
	if !result.IsError || !strings.Contains(result.Content[0].Text, "urls[2]") || !strings.Contains(result.Content[0].Text, "url must use http or https") {
		t.Fatalf("invalid batch target not rejected before HTTP:\n%s", result.Content[0].Text)
	}
	if got := atomic.LoadInt64(&validHits); got != hitsBefore {
		t.Fatalf("valid target was fetched before the batch was rejected: %d -> %d", hitsBefore, got)
	}
	result = client.Dispatch(context.Background(), toolsInvocation(ToolFetch, map[string]any{"urls": []string{valid.URL, ""}}))
	if !result.IsError {
		t.Fatalf("blank URL accepted:\n%s", result.Content[0].Text)
	}
}

func TestBatchFetchConcurrencyLimit(t *testing.T) {
	var active, peak int64
	arrived := make(chan struct{}, 10)
	release := make(chan struct{})
	server := batchServer(t, func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt64(&active, 1)
		for {
			old := atomic.LoadInt64(&peak)
			if n <= old || atomic.CompareAndSwapInt64(&peak, old, n) {
				break
			}
		}
		if n >= maxWorkers {
			select {
			case arrived <- struct{}{}:
			default:
			}
		}
		<-release
		atomic.AddInt64(&active, -1)
		fmt.Fprint(w, "<html><body><p>page "+r.URL.Path+"</p></body></html>")
	})
	var releaseOnce sync.Once
	defer func() { releaseOnce.Do(func() { close(release) }) }()

	urls := make([]string, 6)
	for i := range urls {
		urls[i] = fmt.Sprintf("%s/p%d", server.URL, i)
	}
	client := testClient(testConfig())
	done := make(chan toolsResultAlias, 1)
	go func() {
		result := client.Dispatch(context.Background(), toolsInvocation(ToolFetch, map[string]any{"urls": urls}))
		done <- toolsResultAlias{isError: result.IsError, text: result.Content[0].Text}
	}()
	select {
	case <-arrived:
	case <-time.After(5 * time.Second):
		t.Fatal("three workers never ran together")
	}
	if got := atomic.LoadInt64(&active); got != maxWorkers {
		t.Fatalf("active = %d, want %d", got, maxWorkers)
	}
	releaseOnce.Do(func() { close(release) })
	select {
	case result := <-done:
		if result.isError {
			t.Fatalf("batch failed: %s", result.text)
		}
		for i := range urls {
			if !strings.Contains(result.text, fmt.Sprintf("/p%d", i)) {
				t.Fatalf("missing page %d:\n%s", i, result.text)
			}
		}
	case <-time.After(5 * time.Second):
		t.Fatal("batch did not finish")
	}
	if got := atomic.LoadInt64(&peak); got > maxWorkers {
		t.Fatalf("peak concurrency = %d", got)
	}
}

func TestBatchFetchOrderedPartialResults(t *testing.T) {
	secondDone := make(chan struct{})
	server := batchServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/first":
			<-secondDone
			fmt.Fprint(w, "<html><body><p>first body</p></body></html>")
		case "/second":
			fmt.Fprint(w, "<html><body><p>second body</p></body></html>")
			close(secondDone)
		case "/bad":
			w.WriteHeader(http.StatusInternalServerError)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})
	client := testClient(testConfig())
	result := client.Dispatch(context.Background(), toolsInvocation(ToolFetch, map[string]any{
		"urls": []string{server.URL + "/first", server.URL + "/bad", server.URL + "/second"},
	}))
	if result.IsError {
		t.Fatalf("partial batch marked failed: %s", result.Content[0].Text)
	}
	text := result.Content[0].Text
	first, second := strings.Index(text, "/first"), strings.Index(text, "/second")
	if first < 0 || second < 0 || first > second {
		t.Fatalf("order not preserved:\n%s", text)
	}
	if !strings.Contains(text, "first body") || !strings.Contains(text, "second body") {
		t.Fatalf("content missing:\n%s", text)
	}
	if !strings.Contains(text, "ok: 2") || !strings.Contains(text, "failed: 1") || !strings.Contains(text, "http status: 500") {
		t.Fatalf("counts/errors missing:\n%s", text)
	}
	id, ok := batchDetails(t, result)["responseId"].(string)
	if !ok || id == "" {
		t.Fatalf("partial batch not stored: %+v", result.Details)
	}
	page := getContent(t, client, map[string]any{"responseId": id, "url": server.URL + "/first"})
	if page.IsError || !strings.Contains(page.Content[0].Text, "incomplete: true") {
		t.Fatalf("partial batch retrieval not marked incomplete: %s", page.Content[0].Text)
	}

	allBad := client.Dispatch(context.Background(), toolsInvocation(ToolFetch, map[string]any{
		"urls": []string{server.URL + "/bad", server.URL + "/missing"},
	}))
	if !allBad.IsError || !strings.Contains(allBad.Content[0].Text, "all 2 requests failed") {
		t.Fatalf("all-failed batch:\n%s", allBad.Content[0].Text)
	}
}

func TestBatchFetchCancellationStopsAdmission(t *testing.T) {
	var requests int64
	started := make(chan struct{}, 3)
	server := batchServer(t, func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&requests, 1)
		started <- struct{}{}
		<-r.Context().Done()
	})
	cfg := testConfig()
	cfg.Provider = "searxng"
	client := testClient(cfg)
	urls := make([]string, 6)
	for i := range urls {
		urls[i] = fmt.Sprintf("%s/p%d", server.URL, i)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan toolsResultAlias, 1)
	go func() {
		result := client.Dispatch(ctx, toolsInvocation(ToolFetch, map[string]any{"urls": urls}))
		done <- toolsResultAlias{isError: result.IsError, text: result.Content[0].Text}
	}()
	for i := 0; i < maxWorkers; i++ {
		select {
		case <-started:
		case <-time.After(5 * time.Second):
			t.Fatal("requests did not start")
		}
	}
	cancel()
	select {
	case result := <-done:
		if !result.isError {
			t.Fatalf("cancelled batch must fail:\n%s", result.text)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("cancellation did not stop the batch")
	}
	if got := atomic.LoadInt64(&requests); got > maxWorkers {
		t.Fatalf("queued requests were admitted after cancellation: %d", got)
	}
}

func TestDomainFilterBoundaries(t *testing.T) {
	filter, err := parseDomainFilters([]string{"a.example", "-private.a.example"})
	if err != nil {
		t.Fatal(err)
	}
	cases := map[string]bool{
		"https://a.example/":               true,
		"https://sub.a.example/x":          true,
		"https://private.a.example/":       false,
		"https://nota.example/":            false,
		"https://a.example.evil.test/":     false,
		"https://deep.sub.a.example/path/": true,
	}
	for target, want := range cases {
		if got := filter.allows(target); got != want {
			t.Fatalf("%s: allows = %v, want %v", target, got, want)
		}
	}
	empty := domainFilter{}
	if !empty.allows("https://anything.example/") {
		t.Fatal("empty filter must allow all")
	}
}

func TestSearchDomainFilterAppliedBeforeLimit(t *testing.T) {
	server := batchServer(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"results":[
			{"title":"off","url":"https://b.example/1"},
			{"title":"on1","url":"https://a.example/1"},
			{"title":"off2","url":"https://b.example/2"},
			{"title":"on2","url":"https://sub.a.example/2"}
		]}`)
	})
	cfg := searxConfig(t, server.URL)
	client := testClient(cfg)
	result := client.Dispatch(context.Background(), toolsInvocation(ToolSearch, map[string]any{
		"query": "q", "domainFilter": []string{"a.example"}, "numResults": 2,
	}))
	if result.IsError {
		t.Fatalf("search: %s", result.Content[0].Text)
	}
	text := result.Content[0].Text
	if strings.Contains(text, "https://b.example/") || !strings.Contains(text, "https://a.example/1") || !strings.Contains(text, "https://sub.a.example/2") {
		t.Fatalf("filtered results:\n%s", text)
	}
}

func TestSearXNGRequestShaping(t *testing.T) {
	var gotQuery, gotRange string
	server := batchServer(t, func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.Query().Get("q")
		gotRange = r.URL.Query().Get("time_range")
		fmt.Fprint(w, `{"results":[]}`)
	})
	client := testClient(searxConfig(t, server.URL))
	result := client.Dispatch(context.Background(), toolsInvocation(ToolSearch, map[string]any{
		"query":         "go",
		"domainFilter":  []string{"a.example", "b.example", "-c.example"},
		"recencyFilter": "day",
	}))
	if result.IsError {
		t.Fatalf("search: %s", result.Content[0].Text)
	}
	want := "go (site:a.example OR site:b.example) -site:c.example"
	if gotQuery != want {
		t.Fatalf("q = %q, want %q", gotQuery, want)
	}
	if gotRange != "day" {
		t.Fatalf("time_range = %q", gotRange)
	}
}

func TestExaDirectFilterAndRecency(t *testing.T) {
	var body map[string]any
	server := batchServer(t, func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&body)
		fmt.Fprint(w, `{"results":[]}`)
	})
	cfg := exaConfig(server.URL, "k")
	client := testClient(cfg)
	client.now = func() time.Time {
		return time.Date(2026, 3, 31, 12, 0, 0, 0, time.UTC)
	}
	result := client.Dispatch(context.Background(), toolsInvocation(ToolSearch, map[string]any{
		"query":         "q",
		"provider":      "exa",
		"domainFilter":  []string{"a.example", "-b.example"},
		"recencyFilter": "month",
	}))
	if result.IsError {
		t.Fatalf("search: %s", result.Content[0].Text)
	}
	if fmt.Sprint(body["includeDomains"]) != "[a.example]" || fmt.Sprint(body["excludeDomains"]) != "[b.example]" {
		t.Fatalf("domains = %+v", body)
	}
	if body["startPublishedDate"] != "2026-03-01T12:00:00Z" {
		t.Fatalf("startPublishedDate = %v", body["startPublishedDate"])
	}
}

func TestExaMCPFilterAndRecencyOperators(t *testing.T) {
	server := batchServer(t, func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		params := body["params"].(map[string]any)
		arguments := params["arguments"].(map[string]any)
		query := arguments["query"].(string)
		if !strings.Contains(query, "(site:a.example OR site:b.example)") || !strings.Contains(query, "-site:c.example") || !strings.Contains(query, "past 24 hours") {
			t.Errorf("mcp query = %q", query)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%q,"result":{"content":[{"type":"text","text":"Title: A\nURL: https://a.example/\n"}]}}`, body["id"])
	})
	cfg := exaConfig("https://api.exa.ai", "")
	cfg.Provider = "auto"
	client := testClient(cfg)
	client.mcpURL = server.URL
	result := client.Dispatch(context.Background(), toolsInvocation(ToolSearch, map[string]any{
		"query":         "q",
		"domainFilter":  []string{"a.example", "b.example", "-c.example"},
		"recencyFilter": "day",
	}))
	if result.IsError {
		t.Fatalf("search: %s", result.Content[0].Text)
	}
}

func inlineFixture(t *testing.T) (*httptest.Server, *int64) {
	t.Helper()
	var pageHits int64
	var server *httptest.Server
	server = batchServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/search" {
			fmt.Fprintf(w, `{"results":[
				{"title":"One","url":"%s/page1","content":"snippet one"},
				{"title":"Two","url":"%s/page2","content":"snippet two"}
			]}`, server.URL, server.URL)
			return
		}
		atomic.AddInt64(&pageHits, 1)
		if r.URL.Path == "/page2" {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		fmt.Fprint(w, "<html><head><title>Page One</title></head><body><p>full page one</p></body></html>")
	})
	return server, &pageHits
}

func TestInlineContentStoredAndRetrievable(t *testing.T) {
	server, pageHits := inlineFixture(t)
	cfg := searxConfig(t, server.URL)
	client := testClient(cfg)

	plain := client.Dispatch(context.Background(), toolsInvocation(ToolSearch, map[string]any{"query": "q"}))
	if plain.IsError {
		t.Fatalf("plain search: %s", plain.Content[0].Text)
	}
	if got := atomic.LoadInt64(pageHits); got != 0 {
		t.Fatalf("plain search fetched pages: %d", got)
	}

	withContent := client.Dispatch(context.Background(), toolsInvocation(ToolSearch, map[string]any{"query": "q", "includeContent": true}))
	if withContent.IsError {
		t.Fatalf("content search: %s", withContent.Content[0].Text)
	}
	if got := atomic.LoadInt64(pageHits); got != 2 {
		t.Fatalf("page fetches = %d, want 2", got)
	}
	if batchDetails(t, withContent)["fetched"] != 1 {
		t.Fatalf("details = %+v", withContent.Details)
	}
	if !strings.Contains(withContent.Content[0].Text, "fetch-error: http status: 500") {
		t.Fatalf("per-page error missing:\n%s", withContent.Content[0].Text)
	}
	if !strings.Contains(withContent.Content[0].Text, "incomplete: true") {
		t.Fatalf("partial snapshot not marked incomplete:\n%s", withContent.Content[0].Text)
	}
	id := extractResponseID(t, withContent.Content[0].Text)
	overview := getContent(t, client, map[string]any{"responseId": id})
	if overview.IsError || strings.Contains(overview.Content[0].Text, "page: 0 of") {
		t.Fatalf("overview paging metadata: %s", overview.Content[0].Text)
	}
	page := getContent(t, client, map[string]any{"responseId": id, "url": server.URL + "/page1"})
	if page.IsError || !strings.Contains(page.Content[0].Text, "full page one") {
		t.Fatalf("retrieval: %s", page.Content[0].Text)
	}
	failed := getContent(t, client, map[string]any{"responseId": id, "url": server.URL + "/page2"})
	if !failed.IsError || !strings.Contains(failed.Content[0].Text, "http status: 500") {
		t.Fatalf("failed-page retrieval: %s", failed.Content[0].Text)
	}
}

func TestInlineContentUsesConfiguredRawMode(t *testing.T) {
	const body = "<p>keep   raw markup</p>\n"
	for _, modes := range [][]string{{"raw"}, {"readable", "raw"}} {
		t.Run(strings.Join(modes, ","), func(t *testing.T) {
			var server *httptest.Server
			server = batchServer(t, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/search" {
					fmt.Fprintf(w, `{"results":[{"url":%q}]}`, server.URL+"/page")
					return
				}
				w.Header().Set("Content-Type", "text/html")
				io.WriteString(w, body)
			})
			cfg := searxConfig(t, server.URL)
			cfg.FetchModes = modes
			cfg.FetchMode = "raw"
			client := testClient(cfg)
			defer client.CloseIdle()
			result := client.Dispatch(context.Background(), toolsInvocation(ToolSearch, map[string]any{"query": "q", "includeContent": true}))
			if result.IsError {
				t.Fatalf("search: %s", result.Content[0].Text)
			}
			page := getContent(t, client, map[string]any{"responseId": extractResponseID(t, result.Content[0].Text), "urlIndex": 0})
			if page.IsError {
				t.Fatalf("stored content: %s", page.Content[0].Text)
			}
			if got := extractPayload(t, page.Content[0].Text); got != body {
				t.Fatalf("configured raw mode changed page text: %q", got)
			}
		})
	}
}

func TestInlineContentRedactsEachFieldOnce(t *testing.T) {
	const secret = "redacted" // A second pass would match inside the formatter's own marker.
	for _, title := range []string{"", secret} {
		t.Run("provider title="+title, func(t *testing.T) {
			var server *httptest.Server
			server = batchServer(t, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/search" {
					fmt.Fprintf(w, `{"results":[{"title":%q,"url":%q,"content":%q,"engines":[%q],"publishedDate":%q}]}`,
						title, server.URL+"/page", secret, secret, secret)
					return
				}
				w.Header().Set("Content-Type", "text/html")
				io.WriteString(w, "<html><head><title>"+secret+"</title></head><body><p>"+secret+"</p></body></html>")
			})
			cfg := searxConfig(t, server.URL)
			cfg.SearXNGHeaders = http.Header{"X-Test-Key": []string{secret}}
			client := testClient(cfg)
			defer client.CloseIdle()
			result := client.Dispatch(context.Background(), toolsInvocation(ToolSearch, map[string]any{"query": "q", "includeContent": true}))
			if result.IsError || strings.Contains(result.Content[0].Text, "[[redacted]]") {
				t.Fatalf("search redacted a marker again: %s", result.Content[0].Text)
			}
			page := getContent(t, client, map[string]any{"responseId": extractResponseID(t, result.Content[0].Text), "urlIndex": 0})
			if page.IsError {
				t.Fatalf("stored content: %s", page.Content[0].Text)
			}
			text := page.Content[0].Text
			if extractPayload(t, text) != "[redacted]" || !strings.Contains(text, "\ntitle: [redacted]\n") {
				t.Fatalf("stored page title/content were not redacted exactly once: %s", text)
			}
		})
	}
}

func TestInlineContentRequiresFetchBeforeSearch(t *testing.T) {
	var providerHits int64
	server := batchServer(t, func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&providerHits, 1)
		fmt.Fprint(w, `{"results":[]}`)
	})
	cfg := searxConfig(t, server.URL)
	cfg.FetchEnabled = false
	client := testClient(cfg)
	result := client.Dispatch(context.Background(), toolsInvocation(ToolSearch, map[string]any{"query": "q", "includeContent": true}))
	if !result.IsError || !strings.Contains(result.Content[0].Text, classValidation) {
		t.Fatalf("result: %s", result.Content[0].Text)
	}
	if got := atomic.LoadInt64(&providerHits); got != 0 {
		t.Fatalf("provider called despite invalid request: %d", got)
	}
}

func TestSearchBatchBounds(t *testing.T) {
	server := batchServer(t, func(w http.ResponseWriter, r *http.Request) {
		var b strings.Builder
		b.WriteString(`{"results":[`)
		for i := 0; i < 20; i++ {
			if i > 0 {
				b.WriteString(",")
			}
			query := r.URL.Query().Get("q")
			fmt.Fprintf(&b, `{"title":"%s-%d","url":"https://%s-e%d.example/%s/%d","content":"%s"}`,
				query, i, query, i, strings.Repeat("u", 3000), i, strings.Repeat("s", 4000))
		}
		b.WriteString(`]}`)
		fmt.Fprint(w, b.String())
	})
	client := testClient(searxConfig(t, server.URL))
	result := client.Dispatch(context.Background(), toolsInvocation(ToolSearch, map[string]any{
		"queries": []string{"q1", "q2", "q3"}, "numResults": 20,
	}))
	if result.IsError {
		t.Fatalf("search: %s", result.Content[0].Text)
	}
	text := result.Content[0].Text
	if len(text) > 50<<10 || strings.Count(text, "\n") > 2000 {
		t.Fatalf("result not bounded: %d bytes %d lines", len(text), strings.Count(text, "\n"))
	}
	details, _ := json.Marshal(result.Details)
	if len(details) > 50<<10 {
		t.Fatalf("details not bounded: %d bytes", len(details))
	}
	if batchDetails(t, result)["results"] != 60 {
		t.Fatalf("results = %v", result.Details)
	}
	if omitted, _ := batchDetails(t, result)["omitted"].(int); omitted < 1 {
		t.Fatalf("omitted result sections not reported: %+v", result.Details)
	}
	if !strings.Contains(text, "section(s) omitted") {
		t.Fatalf("omitted notice missing:\n%.500s", text)
	}
}

func TestSearchBatchOrderAndDedupe(t *testing.T) {
	server := batchServer(t, func(w http.ResponseWriter, r *http.Request) {
		query := r.URL.Query().Get("q")
		fmt.Fprintf(w, `{"results":[
			{"title":"%s-own","url":"https://%s.example/","content":"own"},
			{"title":"shared","url":"https://shared.example/","content":"shared"}
		]}`, query, query)
	})
	client := testClient(searxConfig(t, server.URL))
	result := client.Dispatch(context.Background(), toolsInvocation(ToolSearch, map[string]any{
		"queries": []string{"q1", "q2", "q3"}, "numResults": 5,
	}))
	if result.IsError {
		t.Fatalf("search: %s", result.Content[0].Text)
	}
	text := result.Content[0].Text
	first := strings.Index(text, "q1-own")
	second := strings.Index(text, "q2-own")
	third := strings.Index(text, "q3-own")
	if first < 0 || second < 0 || third < 0 || !(first < second && second < third) {
		t.Fatalf("query order: %d %d %d\n%s", first, second, third, text)
	}
	if got := strings.Count(text, "https://shared.example/"); got != 1 {
		t.Fatalf("shared result count = %d\n%s", got, text)
	}
	if batchDetails(t, result)["results"] != 4 {
		t.Fatalf("results = %v", result.Details)
	}
}

func TestFindTextWhitespaceFallback(t *testing.T) {
	server := batchServer(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "<html><body><pre>alpha\n   beta\tgamma\ndelta</pre></body></html>")
	})
	client := testClient(testConfig())
	fetched := client.Dispatch(context.Background(), toolsInvocation(ToolFetch, map[string]any{"url": server.URL}))
	if fetched.IsError {
		t.Fatalf("fetch: %s", fetched.Content[0].Text)
	}
	id := extractResponseID(t, fetched.Content[0].Text)
	for needle, wantOffset := range map[string]string{"beta gamma": "13", "alpha beta": "4", "beta   gamma": "13"} {
		page := getContent(t, client, map[string]any{"responseId": id, "findText": needle})
		if page.IsError {
			t.Fatalf("%q: %s", needle, page.Content[0].Text)
		}
		text := page.Content[0].Text
		if !strings.Contains(text, "offset: "+wantOffset) {
			t.Fatalf("%q: offset:\n%s", needle, text)
		}
		if !strings.Contains(text, "beta") || !strings.Contains(text, "gamma") || !strings.Contains(text, "delta") {
			t.Fatalf("%q: window:\n%s", needle, text)
		}
	}
}

func TestExaRecencyWindows(t *testing.T) {
	cases := []struct {
		now     time.Time
		recency string
		want    string
	}{
		{time.Date(2026, 3, 31, 12, 0, 0, 0, time.UTC), "day", "2026-03-30T12:00:00Z"},
		{time.Date(2026, 3, 31, 12, 0, 0, 0, time.UTC), "month", "2026-03-01T12:00:00Z"},
		{time.Date(2026, 1, 15, 0, 0, 0, 0, time.UTC), "year", "2025-01-15T00:00:00Z"},
		{time.Date(2024, 3, 1, 6, 30, 0, 0, time.UTC), "year", "2023-03-02T06:30:00Z"},
	}
	for _, tc := range cases {
		client := testClient(testConfig())
		client.now = func() time.Time { return tc.now }
		if got := client.recencyStart(tc.recency); got != tc.want {
			t.Fatalf("%s at %s: got %s want %s", tc.recency, tc.now, got, tc.want)
		}
	}
}

func TestInlineContentRespectsDomainFilter(t *testing.T) {
	var pageHits int64
	var server *httptest.Server
	server = batchServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/search" {
			fmt.Fprintf(w, `{"results":[
				{"title":"keep","url":"http://127.0.0.1:%s/page1"},
				{"title":"drop","url":"https://blocked.example/page2"}
			]}`, strings.Split(server.URL, ":")[2])
			return
		}
		atomic.AddInt64(&pageHits, 1)
		fmt.Fprint(w, "<html><body><p>kept page</p></body></html>")
	})
	client := testClient(searxConfig(t, server.URL))
	result := client.Dispatch(context.Background(), toolsInvocation(ToolSearch, map[string]any{
		"query": "q", "includeContent": true, "domainFilter": []string{"127.0.0.1"},
	}))
	if result.IsError {
		t.Fatalf("search: %s", result.Content[0].Text)
	}
	if got := atomic.LoadInt64(&pageHits); got != 1 {
		t.Fatalf("page fetches = %d, want 1", got)
	}
	if strings.Contains(result.Content[0].Text, "blocked.example") {
		t.Fatalf("filtered result survived:\n%s", result.Content[0].Text)
	}
}

func TestSearchCancellationMarksIncomplete(t *testing.T) {
	started := make(chan struct{}, 1)
	var server *httptest.Server
	server = batchServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/search" {
			fmt.Fprintf(w, `{"results":[{"title":"slow","url":"%s/page"}]}`, server.URL)
			return
		}
		started <- struct{}{}
		<-r.Context().Done()
	})
	client := testClient(searxConfig(t, server.URL))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan toolsResultAlias, 1)
	go func() {
		result := client.Dispatch(ctx, toolsInvocation(ToolSearch, map[string]any{"query": "q", "includeContent": true}))
		done <- toolsResultAlias{isError: result.IsError, text: result.Content[0].Text}
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("inline fetch never started")
	}
	cancel()
	select {
	case result := <-done:
		if result.isError {
			t.Fatalf("partial search failed: %s", result.text)
		}
		if !strings.Contains(result.text, "incomplete: true") || !strings.Contains(result.text, "cancellation") {
			t.Fatalf("partial result not marked:\n%s", result.text)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("search did not join the cancelled worker")
	}
}

func TestFindTextUnicodeWhitespaceOffset(t *testing.T) {
	text := []rune("开头\u00a0\u3000emojis 🚀\n  mixed\tcase")
	got, ok := findTextOffset(text, "EMOJIS 🚀 mixed CASE")
	if !ok || got != 4 {
		t.Fatalf("got %d ok=%v", got, ok)
	}
}

func TestProviderErrorsAreNotRetried(t *testing.T) {
	for _, status := range []int{http.StatusTooManyRequests, http.StatusServiceUnavailable} {
		var hits int64
		server := batchServer(t, func(w http.ResponseWriter, r *http.Request) {
			atomic.AddInt64(&hits, 1)
			w.WriteHeader(status)
		})
		client := testClient(searxConfig(t, server.URL))
		result := client.Dispatch(context.Background(), toolsInvocation(ToolSearch, map[string]any{"query": "q"}))
		if !result.IsError {
			t.Fatalf("%d: expected error", status)
		}
		if got := atomic.LoadInt64(&hits); got != 1 {
			t.Fatalf("%d: provider requests = %d, want 1", status, got)
		}
	}
}

func TestSearchInlineContentKeepsLongResultURL(t *testing.T) {
	const suffix = 3000
	var pagePath string
	var server *httptest.Server
	server = batchServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/search" {
			fmt.Fprintf(w, `{"results":[{"title":"long","url":"%s/page/%s"}]}`, server.URL, strings.Repeat("x", suffix))
			return
		}
		pagePath = r.URL.Path
		fmt.Fprint(w, "<html><body><p>long page</p></body></html>")
	})
	client := testClient(searxConfig(t, server.URL))
	result := client.Dispatch(context.Background(), toolsInvocation(ToolSearch, map[string]any{
		"query": "q", "includeContent": true,
	}))
	if result.IsError {
		t.Fatalf("search: %s", result.Content[0].Text)
	}
	if len(pagePath) != len("/page/")+suffix {
		t.Fatalf("fetched path length = %d, want %d", len(pagePath), len("/page/")+suffix)
	}
	if got := batchDetails(t, result)["fetched"]; got != 1 {
		t.Fatalf("fetched = %v, want 1", got)
	}
	target := server.URL + "/page/" + strings.Repeat("x", suffix)
	page := getContent(t, client, map[string]any{
		"responseId": extractResponseID(t, result.Content[0].Text), "url": target,
	})
	if page.IsError || !strings.Contains(page.Content[0].Text, "long page") {
		t.Fatalf("long URL lookup: %s", page.Content[0].Text)
	}
}

func TestBatchFetchReportsOmittedSections(t *testing.T) {
	server := batchServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		fmt.Fprint(w, strings.Repeat("x", 20000))
	})
	cfg := testConfig()
	cfg.MaxInlineChars = 20000
	client := testClient(cfg)
	urls := make([]string, 4)
	for i := range urls {
		urls[i] = fmt.Sprintf("%s/%d", server.URL, i)
	}
	result := client.Dispatch(context.Background(), toolsInvocation(ToolFetch, map[string]any{"urls": urls}))
	if result.IsError {
		t.Fatalf("batch: %s", result.Content[0].Text)
	}
	text := result.Content[0].Text
	if !strings.Contains(text, "section(s) omitted") {
		t.Fatalf("omitted sections not reported:\n%s", text)
	}
	details := batchDetails(t, result)
	omitted, ok := details["omitted"].(int)
	if !ok || omitted < 1 || omitted >= len(urls) {
		t.Fatalf("omitted = %#v, details = %+v", details["omitted"], details)
	}
	if len(text) > resultMaxBytes || strings.Count(text, "\n")+1 > resultMaxLines {
		t.Fatalf("bounded output too large: %d bytes", len(text))
	}
}

func TestBatchFetchMarksPreviewTruncation(t *testing.T) {
	server := batchServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		fmt.Fprint(w, strings.Repeat("y", 50))
	})
	cfg := testConfig()
	cfg.MaxInlineChars = 20
	client := testClient(cfg)
	result := client.Dispatch(context.Background(), toolsInvocation(ToolFetch, map[string]any{
		"urls": []string{server.URL + "/preview", server.URL + "/other"},
	}))
	if result.IsError {
		t.Fatalf("batch: %s", result.Content[0].Text)
	}
	text := result.Content[0].Text
	if !strings.Contains(text, "preview-truncated: true (20 scalar limit)") {
		t.Fatalf("preview truncation not reported:\n%s", text)
	}
	if !strings.Contains(text, "urlIndex 0") {
		t.Fatalf("continuation missing:\n%s", text)
	}
}

func TestInlineContentUsesPageHeaderProfile(t *testing.T) {
	const language = "en-US,en;q=0.9"
	var server *httptest.Server
	server = batchServer(t, func(w http.ResponseWriter, r *http.Request) {
		ua, accept, lang := r.UserAgent(), r.Header.Get("Accept"), r.Header.Get("Accept-Language")
		if r.URL.Path == "/search" {
			if ua != userAgent || accept != "application/json" || lang != "" {
				t.Errorf("SearXNG User-Agent, Accept, Accept-Language = %q, %q, %q", ua, accept, lang)
			}
			fmt.Fprintf(w, `{"results":[{"url":%q}]}`, server.URL+"/page")
			return
		}
		if !strings.Contains(ua, "Firefox/") || accept != browserAccept || lang != language {
			t.Errorf("page User-Agent, Accept, Accept-Language = %q, %q, %q", ua, accept, lang)
		}
		w.Header().Set("Content-Type", "text/plain")
		fmt.Fprint(w, "full inline body")
	})
	cfg := searxConfig(t, server.URL)
	cfg.FetchHeaderProfile = "firefox"
	cfg.FetchAcceptLanguage = language
	client := testClient(cfg)
	defer client.CloseIdle()
	result := client.Dispatch(context.Background(), toolsInvocation(ToolSearch, map[string]any{"query": "q", "includeContent": true}))
	if result.IsError {
		t.Fatalf("search: %s", result.Content[0].Text)
	}
	page := getContent(t, client, map[string]any{"responseId": extractResponseID(t, result.Content[0].Text), "urlIndex": 0})
	if page.IsError {
		t.Fatalf("stored content: %s", page.Content[0].Text)
	}
	if got := extractPayload(t, page.Content[0].Text); got != "full inline body" {
		t.Fatalf("stored page text = %q", got)
	}
}
