package webaccess

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strings"
	"testing"
	"testing/iotest"

	"psi/internal/tools"
)

func testConfig() Config {
	return Config{
		Provider:        "auto",
		ExaBaseURL:      "https://api.exa.ai",
		SearchEnabled:   true,
		FetchEnabled:    true,
		ContentEnabled:  true,
		FetchMode:       "readable",
		FetchModes:      []string{"readable", "raw"},
		MaxInlineChars:  DefaultInlineChars,
		CacheMaxEntries: DefaultCacheEntries,
		CacheMaxBytes:   DefaultCacheBytes,
		ExaAvailable:    true,
		AllowRanges:     []netip.Prefix{netip.MustParsePrefix("127.0.0.1/32")},
	}
}

func testClient(cfg Config) *Client {
	return New(cfg)
}

func TestFetchReadableMarkdown(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, `<!doctype html><html><head><title>Example Title</title><style>.x{}</style></head>
<body><nav>skip nav</nav><h1>Heading</h1><p>Hello <b>world</b>.</p>
<ul><li>one</li><li>two<ul><li>nested</li></ul></li></ul>
<pre><code>fmt.Println("hi")</code></pre>
<p><a href="/next">Next page</a></p><script>alert(1)</script><footer>skip footer</footer></body></html>`)
	}))
	defer server.Close()

	client := testClient(testConfig())
	page, err := client.fetchPage(context.Background(), " "+server.URL+"/doc ", "readable")
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if page.Title != "Example Title" {
		t.Fatalf("title = %q", page.Title)
	}
	for _, want := range []string{"# Heading", "Hello **world**.", "- one", "  - nested", "```", "[Next page](" + server.URL + "/next)"} {
		if !strings.Contains(page.Text, want) {
			t.Errorf("markdown missing %q:\n%s", want, page.Text)
		}
	}
	for _, bad := range []string{"skip nav", "skip footer", "alert(1)", ".x{}"} {
		if strings.Contains(page.Text, bad) {
			t.Errorf("markdown kept %q:\n%s", bad, page.Text)
		}
	}
	if page.URL != server.URL+"/doc" || page.FinalURL != server.URL+"/doc" {
		t.Fatalf("urls = %q %q", page.URL, page.FinalURL)
	}
}

func TestFetchRedirectResolvesLinksAgainstFinalURL(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/start" {
			http.Redirect(w, r, "/docs/index.html", http.StatusFound)
			return
		}
		w.Header().Set("Content-Type", "text/html")
		io.WriteString(w, `<p><a href="next.html">Next</a><a href="../up.html">Up</a></p>`)
	}))
	defer server.Close()
	client := testClient(testConfig())
	defer client.CloseIdle()
	page, err := client.fetchPage(context.Background(), server.URL+"/start", "readable")
	if err != nil {
		t.Fatal(err)
	}
	if page.URL != server.URL+"/start" || page.FinalURL != server.URL+"/docs/index.html" {
		t.Fatalf("redirect metadata: original=%q final=%q", page.URL, page.FinalURL)
	}
	for _, want := range []string{"[Next](" + server.URL + "/docs/next.html)", "[Up](" + server.URL + "/up.html)"} {
		if !strings.Contains(page.Text, want) {
			t.Fatalf("link did not use the final page URL; want %q in %q", want, page.Text)
		}
	}
}

func TestFetchRawPreservesMarkup(t *testing.T) {
	const body = "<html><body>\n  <p>keep   spaces</p>\n</body></html>\n"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		fmt.Fprint(w, body)
	}))
	defer server.Close()
	client := testClient(testConfig())
	page, err := client.fetchPage(context.Background(), server.URL, "raw")
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if page.Text != body {
		t.Fatalf("raw text changed: %q", page.Text)
	}
}

func TestFetchDeclaredCharset(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=iso-8859-1")
		w.Write([]byte("<html><body><p>caf\xe9</p></body></html>"))
	}))
	defer server.Close()
	page, err := testClient(testConfig()).fetchPage(context.Background(), server.URL, "raw")
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if !strings.Contains(page.Text, "café") {
		t.Fatalf("text = %q", page.Text)
	}
}

func TestFetchMetaCharset(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte(`<html><head><meta charset="windows-1252"></head><body>caf` + "\xe9" + `</body></html>`))
	}))
	defer server.Close()
	page, err := testClient(testConfig()).fetchPage(context.Background(), server.URL, "raw")
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if !strings.Contains(page.Text, "café") {
		t.Fatalf("text = %q", page.Text)
	}
}

func TestFetchInvalidUTF8Replaced(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Write([]byte("bad:\xff byte"))
	}))
	defer server.Close()
	page, err := testClient(testConfig()).fetchPage(context.Background(), server.URL, "raw")
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if !strings.Contains(page.Text, "\uFFFD") {
		t.Fatalf("text = %q", page.Text)
	}
}

func TestFetchRefusesBinaryAndEmpty(t *testing.T) {
	for _, tc := range []struct {
		name        string
		contentType string
		body        string
	}{
		{"pdf", "application/pdf", "%PDF-1.7"},
		{"empty", "text/plain", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if tc.contentType != "" {
					w.Header().Set("Content-Type", tc.contentType)
				}
				io.WriteString(w, tc.body)
			}))
			defer server.Close()
			_, err := testClient(testConfig()).fetchPage(context.Background(), server.URL, "raw")
			if err == nil || !strings.Contains(err.Error(), classInvalid) {
				t.Fatalf("err = %v", err)
			}
		})
	}
}

func TestFetchSniffsMissingContentType(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header()["Content-Type"] = nil
		w.WriteHeader(http.StatusOK)
		io.WriteString(w, "<html><body><h1>Sniffed</h1></body></html>")
	}))
	defer server.Close()
	page, err := testClient(testConfig()).fetchPage(context.Background(), server.URL, "readable")
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if !strings.Contains(page.Text, "# Sniffed") {
		t.Fatalf("text = %q", page.Text)
	}
}

func TestFetchStatusErrors(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusNotFound)
	}))
	defer server.Close()
	_, err := testClient(testConfig()).fetchPage(context.Background(), server.URL, "raw")
	if err == nil || !strings.Contains(err.Error(), classStatus) || !strings.Contains(err.Error(), "404") {
		t.Fatalf("err = %v", err)
	}
}

func TestFetchUnsupportedContentEncoding(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.Header().Set("Content-Encoding", "br")
		io.WriteString(w, "compressed?")
	}))
	defer server.Close()
	_, err := testClient(testConfig()).fetchPage(context.Background(), server.URL, "raw")
	if err == nil || !strings.Contains(err.Error(), "unsupported content encoding") {
		t.Fatalf("err = %v", err)
	}
}

func TestFetchCorruptGzipFailsWithoutPartialContent(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
			t.Errorf("transport should offer gzip")
		}
		w.Header().Set("Content-Type", "text/plain")
		w.Header().Set("Content-Encoding", "gzip")
		io.WriteString(w, "not gzip at all")
	}))
	defer server.Close()
	_, err := testClient(testConfig()).fetchPage(context.Background(), server.URL, "raw")
	if err == nil || !strings.Contains(err.Error(), classInvalid) {
		t.Fatalf("err = %v", err)
	}
}

func TestFetchReadErrorIsInvalidResponse(t *testing.T) {
	client := testClient(testConfig())
	client.page = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: 200,
			Header:     http.Header{"Content-Type": []string{"text/plain"}},
			Body:       io.NopCloser(iotest.ErrReader(errors.New("boom"))),
			Request:    r,
		}, nil
	})}
	_, err := client.fetchPage(context.Background(), "https://example.com/", "raw")
	if err == nil || !strings.Contains(err.Error(), classInvalid) {
		t.Fatalf("err = %v", err)
	}
}

func TestFetchSourceCapIsTruncated(t *testing.T) {
	big := strings.Repeat("a", maxBodyBytes+64)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		io.WriteString(w, big)
	}))
	defer server.Close()
	page, err := testClient(testConfig()).fetchPage(context.Background(), server.URL, "raw")
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if !page.Truncated || len(page.Text) != maxBodyBytes {
		t.Fatalf("truncated=%v len=%d", page.Truncated, len(page.Text))
	}
}

func TestFetchRedirectToDeniedTarget(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://127.0.0.2:1/other", http.StatusFound)
	}))
	defer server.Close()
	_, err := testClient(testConfig()).fetchPage(context.Background(), server.URL, "raw")
	if err == nil || !strings.Contains(err.Error(), classBlocked) {
		t.Fatalf("err = %v", err)
	}
}

func TestFetchRedirectToInvalidTargetClass(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "ftp://example.com/other", http.StatusFound)
	}))
	defer server.Close()
	_, err := testClient(testConfig()).fetchPage(context.Background(), server.URL, "raw")
	if err == nil || !strings.Contains(err.Error(), classValidation) {
		t.Fatalf("err = %v", err)
	}
}

func TestRedirectPolicy(t *testing.T) {
	base, _ := http.NewRequest(http.MethodGet, "https://example.com/start", nil)
	via := []*http.Request{base}
	for i := 0; i < 4; i++ {
		next, _ := http.NewRequest(http.MethodGet, "https://example.com/next", nil)
		via = append(via, next)
	}
	req, _ := http.NewRequest(http.MethodGet, "https://example.com/final", nil)
	if err := pageRedirect(req, via); err != nil {
		t.Fatalf("fifth redirect should be allowed: %v", err)
	}
	req, _ = http.NewRequest(http.MethodGet, "https://example.com/final", nil)
	if err := pageRedirect(req, append(via, req)); err == nil {
		t.Fatal("sixth redirect must be refused")
	}

	httpsReq, _ := http.NewRequest(http.MethodGet, "https://example.com/", nil)
	httpReq, _ := http.NewRequest(http.MethodGet, "http://example.com/", nil)
	if err := pageRedirect(httpReq, []*http.Request{httpsReq}); err == nil {
		t.Fatal("https to http must be refused")
	}
	ftp, _ := http.NewRequest(http.MethodGet, "ftp://example.com/", nil)
	if err := pageRedirect(ftp, []*http.Request{httpsReq}); err == nil {
		t.Fatal("unsupported scheme must be refused")
	}
}

func TestRedactionRemovesSecretsOnce(t *testing.T) {
	cfg := testConfig()
	cfg.ExaAPIKey = "sk-secret-value"
	cfg.SearXNGHeaders = http.Header{"X-Api-Key": []string{" header-secret "}}
	red := newRedactor(cfg)
	out := red.redact("key=sk-secret-value header=header-secret spaced= header-secret  marker=[redacted]", 4096)
	if strings.Contains(out, "sk-secret-value") || strings.Contains(out, " header-secret") {
		t.Fatalf("secret survived: %q", out)
	}
	if !strings.Contains(out, "[redacted]") {
		t.Fatalf("missing marker: %q", out)
	}
	if strings.Count(out, "[redacted]") < 2 {
		t.Fatalf("markers were rescanned: %q", out)
	}
}

func TestBoundResultLimits(t *testing.T) {
	long := strings.Repeat("x", resultMaxBytes+100)
	out, truncated := boundResult(long)
	if !truncated || len(out) > resultMaxBytes || !strings.HasSuffix(out, "[truncated]") {
		t.Fatalf("bytes: truncated=%v len=%d", truncated, len(out))
	}
	many := strings.Repeat("line\n", resultMaxLines+10)
	out, truncated = boundResult(many)
	if !truncated || lineCount(out) > resultMaxLines {
		t.Fatalf("lines: truncated=%v lines=%d", truncated, lineCount(out))
	}
	if !validUTF8(out) {
		t.Fatal("bounded output split a rune")
	}
}

func validUTF8(s string) bool {
	return strings.ToValidUTF8(s, "") == s
}

func TestArgIntBoundaries(t *testing.T) {
	for _, tc := range []struct {
		value any
		ok    bool
	}{
		{json.Number("0"), true},
		{json.Number("9007199254740991"), true},
		{json.Number("9007199254740992"), false},
		{json.Number("1.5"), false},
		{"12", false},
		{float64(12), true},
		{math.NaN(), false},
		{math.Inf(1), false},
		{-1, false},
	} {
		_, err := argInt(tc.value, "offset")
		if (err == nil) != tc.ok {
			t.Errorf("%v: err=%v want ok=%v", tc.value, err, tc.ok)
		}
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestFormatPageHasStableBoundary(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		io.WriteString(w, "payload line\n")
	}))
	defer server.Close()
	client := testClient(testConfig())
	page, err := client.fetchPage(context.Background(), server.URL, "raw")
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	text, _ := client.formatPage(page, "")
	if !strings.Contains(text, "\ncontent:\npayload line") {
		t.Fatalf("format:\n%s", text)
	}
}

func TestDisabledFetchHandlerMakesNoRequest(t *testing.T) {
	cfg := testConfig()
	cfg.FetchEnabled = false
	client := testClient(cfg)
	called := false
	client.page = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		called = true
		return nil, errors.New("unexpected request")
	})}
	result := client.Dispatch(context.Background(), tools.Invocation{
		Name:      ToolFetch,
		Arguments: map[string]any{"url": "https://example.com/"},
	})
	if !result.IsError || called {
		t.Fatalf("disabled handler: error=%v request=%v", result.IsError, called)
	}
}

func TestFetchDispatchReturnsBoundedText(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		io.WriteString(w, "<html><head><title>T</title></head><body><h1>H</h1></body></html>")
	}))
	defer server.Close()
	client := testClient(testConfig())
	result := client.Dispatch(context.Background(), tools.Invocation{
		Name:      ToolFetch,
		Arguments: map[string]any{"url": server.URL},
	})
	if result.IsError || len(result.Content) != 1 || !strings.Contains(result.Content[0].Text, "# H") {
		t.Fatalf("result = %+v", result)
	}
	if result.Details == nil {
		t.Fatal("missing details")
	}
}

func TestUnknownFetchArgumentsRejected(t *testing.T) {
	client := testClient(testConfig())
	result := client.Dispatch(context.Background(), tools.Invocation{
		Name:      ToolFetch,
		Arguments: map[string]any{"url": "https://example.com/", "prompt": "x"},
	})
	if !result.IsError || !strings.Contains(result.Content[0].Text, "unknown argument") {
		t.Fatalf("result = %+v", result)
	}
}

func TestFetchRejectsPathologicalHTMLNesting(t *testing.T) {
	var b strings.Builder
	b.WriteString("<html><body>")
	for i := 0; i < maxHTMLDepth+10; i++ {
		b.WriteString("<div>")
	}
	b.WriteString("deep")
	for i := 0; i < maxHTMLDepth+10; i++ {
		b.WriteString("</div>")
	}
	b.WriteString("</body></html>")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		io.WriteString(w, b.String())
	}))
	defer server.Close()
	result := testClient(testConfig()).Dispatch(context.Background(), toolsInvocation(ToolFetch, map[string]any{"url": server.URL}))
	if !result.IsError || !strings.Contains(result.Content[0].Text, classInvalid) {
		t.Fatalf("result = %s", result.Content[0].Text)
	}
}

func TestFetchHostNamedTLSStillTransport(t *testing.T) {
	client := testClient(testConfig())
	client.page = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return nil, &url.Error{Op: "Get", URL: r.URL.String(), Err: fmt.Errorf("dial failed")}
	})}
	_, err := client.fetchPage(context.Background(), "https://tls.example/", "raw")
	if err == nil || !strings.Contains(err.Error(), classTransport) || strings.Contains(err.Error(), "tls failure") {
		t.Fatalf("err = %v", err)
	}
}

func TestFetchMetadataRedactsConfiguredSecrets(t *testing.T) {
	const secret = "sk-live-secret"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		fmt.Fprint(w, "body")
	}))
	defer server.Close()
	cfg := testConfig()
	cfg.ExaAPIKey = secret
	page, err := testClient(cfg).fetchPage(context.Background(), server.URL+"/?token="+secret, "raw")
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if strings.Contains(page.URL, secret) || strings.Contains(page.FinalURL, secret) {
		t.Fatalf("url metadata leaked: %q %q", page.URL, page.FinalURL)
	}
}

func TestFetchMarksDecodedTextTruncation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=iso-8859-1")
		_, _ = w.Write(bytes.Repeat([]byte{0xff}, maxBodyBytes))
	}))
	defer server.Close()
	page, err := testClient(testConfig()).fetchPage(context.Background(), server.URL, "raw")
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if !page.Truncated || len(page.Text) > maxBodyBytes {
		t.Fatalf("truncated=%v len=%d", page.Truncated, len(page.Text))
	}
}

func TestValidateTargetRejectsUnusablePorts(t *testing.T) {
	for _, raw := range []string{
		"http://example.com:0/",
		"http://example.com:/",
		"http://example.com:65536/",
		"http://example.com:-1/",
	} {
		if _, err := validateTarget(raw); err == nil || !strings.Contains(err.Error(), classValidation) {
			t.Errorf("%s accepted: %v", raw, err)
		}
	}
}

func TestFetchWrappedDialTimeoutKeepsClass(t *testing.T) {
	client := testClient(testConfig())
	client.page = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return nil, fmt.Errorf("timeout: dial interrupted")
	})}
	if _, err := client.fetchPage(context.Background(), "https://example.com/", "readable"); err == nil || !strings.Contains(err.Error(), classTimeout) {
		t.Fatalf("err = %v, want %s", err, classTimeout)
	}
}

func TestFetchDoesNotSuggestDisabledContentTool(t *testing.T) {
	cfg := testConfig()
	cfg.ContentEnabled = false
	cfg.MaxInlineChars = minInlineChars
	client := testClient(cfg)
	text, _ := client.formatPage(pageData{
		FinalURL: "https://example.com/",
		Mode:     "readable",
		Text:     strings.Repeat("a", minInlineChars+1),
	}, "response-id")
	if strings.Contains(text, "continue:") {
		t.Fatalf("disabled content tool received a continuation instruction:\n%s", text)
	}
}

func TestErrorResultsAreBounded(t *testing.T) {
	client := testClient(testConfig())
	key := strings.Repeat("k", resultMaxBytes+100)
	result := client.Dispatch(context.Background(), toolsInvocation(ToolFetch, map[string]any{key: true}))
	if !result.IsError {
		t.Fatal("unknown argument accepted")
	}
	text := result.Content[0].Text
	if len(text) > resultMaxBytes || !strings.HasSuffix(text, "[truncated]") {
		t.Fatalf("error result not bounded: len=%d", len(text))
	}
}
