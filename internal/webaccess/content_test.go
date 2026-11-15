package webaccess

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"psi/internal/tools"
)

func extractResponseID(t *testing.T, text string) string {
	t.Helper()
	for _, line := range strings.Split(text, "\n") {
		if strings.HasPrefix(line, "responseId: ") {
			id := strings.TrimSpace(strings.TrimPrefix(line, "responseId: "))
			if id != "" {
				return id
			}
		}
	}
	t.Fatalf("missing responseId in:\n%s", text)
	return ""
}

func extractPayload(t *testing.T, text string) string {
	t.Helper()
	const boundary = "\ncontent:\n"
	idx := strings.LastIndex(text, boundary)
	if idx < 0 {
		t.Fatalf("missing content boundary in:\n%s", text)
	}
	return text[idx+len(boundary):]
}

func fetchContentID(t *testing.T, client *Client, url string) string {
	t.Helper()
	result := client.Dispatch(context.Background(), tools.Invocation{
		Name:      ToolFetch,
		Arguments: map[string]any{"url": url, "mode": "raw"},
	})
	if result.IsError {
		t.Fatalf("fetch failed: %s", result.Content[0].Text)
	}
	return extractResponseID(t, result.Content[0].Text)
}

func getContent(t *testing.T, client *Client, args map[string]any) tools.Result {
	t.Helper()
	return client.Dispatch(context.Background(), tools.Invocation{Name: ToolContent, Arguments: args})
}

func TestFetchStorePageRoundTrip(t *testing.T) {
	const body = "alpha\nbeta  spaced\n日本語 🚀🚀 tail\n"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		io.WriteString(w, body)
	}))
	defer server.Close()

	cfg := testConfig()
	cfg.MaxInlineChars = 8
	client := testClient(cfg)
	id := fetchContentID(t, client, server.URL)

	var rebuilt strings.Builder
	offset := 0
	for page := 0; page < 100; page++ {
		result := getContent(t, client, map[string]any{"responseId": id, "offset": offset, "limit": 8})
		if result.IsError {
			t.Fatalf("page %d: %s", page, result.Content[0].Text)
		}
		text := result.Content[0].Text
		payload := extractPayload(t, text)
		rebuilt.WriteString(payload)
		runes := len([]rune(payload))
		if runes == 0 && offset < len([]rune(body)) {
			t.Fatalf("no progress at offset %d:\n%s", offset, text)
		}
		offset += runes
		if offset >= len([]rune(body)) {
			break
		}
	}
	if rebuilt.String() != body {
		t.Fatalf("round trip mismatch:\n got %q\nwant %q", rebuilt.String(), body)
	}
}

func TestContentOffsetAndFindRules(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		io.WriteString(w, "Hello CAFÉ world\nsecond line")
	}))
	defer server.Close()
	client := testClient(testConfig())
	id := fetchContentID(t, client, server.URL)

	result := getContent(t, client, map[string]any{"responseId": id, "findText": "café"})
	if result.IsError {
		t.Fatalf("find: %s", result.Content[0].Text)
	}
	if payload := extractPayload(t, result.Content[0].Text); !strings.HasPrefix(payload, "CAFÉ world") {
		t.Fatalf("find payload = %q", payload)
	}
	if !strings.Contains(result.Content[0].Text, "offset: 6") {
		t.Fatalf("find offset:\n%s", result.Content[0].Text)
	}
	if res := getContent(t, client, map[string]any{"responseId": "  " + id + "  "}); res.IsError {
		t.Fatalf("padded responseId: %s", res.Content[0].Text)
	}

	for name, args := range map[string]map[string]any{
		"find with offset":   {"responseId": id, "findText": "x", "offset": 1},
		"url and index":      {"responseId": id, "url": "x", "urlIndex": 0},
		"missing id":         {"responseId": "nope"},
		"find missing":       {"responseId": id, "findText": "absent"},
		"offset past end":    {"responseId": id, "offset": 10000},
		"url not in entry":   {"responseId": id, "url": "https://other.example/"},
		"index out of range": {"responseId": id, "urlIndex": 3},
		"unknown argument":   {"responseId": id, "bogus": true},
	} {
		res := getContent(t, client, args)
		if !res.IsError {
			t.Errorf("%s should fail", name)
		}
	}

	length := len([]rune("Hello CAFÉ world\nsecond line"))
	result = getContent(t, client, map[string]any{"responseId": id, "offset": length})
	if result.IsError {
		t.Fatalf("offset at end should succeed: %s", result.Content[0].Text)
	}
	if payload := extractPayload(t, result.Content[0].Text); payload != "" {
		t.Fatalf("offset at end payload = %q", payload)
	}
}

func TestContentNumberValidation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		io.WriteString(w, "0123456789")
	}))
	defer server.Close()
	client := testClient(testConfig())
	id := fetchContentID(t, client, server.URL)

	result := getContent(t, client, map[string]any{"responseId": id, "offset": json.Number("2"), "limit": json.Number("3")})
	if result.IsError {
		t.Fatalf("json.Number: %s", result.Content[0].Text)
	}
	if payload := extractPayload(t, result.Content[0].Text); payload != "234" {
		t.Fatalf("payload = %q", payload)
	}
	for name, value := range map[string]any{
		"numeric string": "2",
		"nan":            math.NaN(),
		"inf":            math.Inf(1),
		"fraction":       1.5,
		"too large":      float64(1 << 53),
		"negative":       -1,
	} {
		res := getContent(t, client, map[string]any{"responseId": id, "offset": value})
		if !res.IsError {
			t.Errorf("%s should fail", name)
		}
	}
	// A large valid limit clamps instead of overflowing.
	res := getContent(t, client, map[string]any{"responseId": id, "limit": json.Number("9007199254740991")})
	if res.IsError {
		t.Fatalf("large limit: %s", res.Content[0].Text)
	}
	if notice := res.Content[0].Text; !strings.Contains(notice, "length: 10") {
		t.Fatalf("clamped output:\n%s", notice)
	}
}

func TestContentDisabledHandler(t *testing.T) {
	cfg := testConfig()
	cfg.ContentEnabled = false
	client := testClient(cfg)
	result := getContent(t, client, map[string]any{"responseId": "x"})
	if !result.IsError || !strings.Contains(result.Content[0].Text, "disabled") {
		t.Fatalf("result = %+v", result)
	}
}

func TestContentEvictedID(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		io.WriteString(w, "body")
	}))
	defer server.Close()
	cfg := testConfig()
	cfg.CacheMaxEntries = 1
	client := testClient(cfg)
	first := fetchContentID(t, client, server.URL)
	_ = fetchContentID(t, client, server.URL)
	result := getContent(t, client, map[string]any{"responseId": first})
	if !result.IsError || !strings.Contains(result.Content[0].Text, "unknown responseId") {
		t.Fatalf("result = %+v", result)
	}
}

func TestRedactionAppliesBeforeStorage(t *testing.T) {
	const secret = "sk-live-secret"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		fmt.Fprintf(w, "before %s after", secret)
	}))
	defer server.Close()
	cfg := testConfig()
	cfg.ExaAPIKey = secret
	client := testClient(cfg)
	id := fetchContentID(t, client, server.URL)
	result := getContent(t, client, map[string]any{"responseId": id})
	if result.IsError {
		t.Fatalf("content: %s", result.Content[0].Text)
	}
	payload := extractPayload(t, result.Content[0].Text)
	if strings.Contains(payload, secret) || !strings.Contains(payload, "[redacted]") {
		t.Fatalf("payload = %q", payload)
	}
}

func TestContentByteAndLineCaps(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		io.WriteString(w, strings.Repeat("z", resultMaxBytes+1000))
	}))
	defer server.Close()
	cfg := testConfig()
	cfg.MaxInlineChars = maxInlineChars
	client := testClient(cfg)
	id := fetchContentID(t, client, server.URL)

	result := getContent(t, client, map[string]any{"responseId": id})
	if result.IsError {
		t.Fatalf("content: %s", result.Content[0].Text)
	}
	text := result.Content[0].Text
	if len(text) > resultMaxBytes {
		t.Fatalf("page bytes = %d", len(text))
	}
	if !strings.Contains(text, "next-offset:") {
		t.Fatalf("missing continuation:\n%.300s", text)
	}
	payload := extractPayload(t, text)
	if len(payload) == 0 {
		t.Fatal("no progress on a long single line")
	}
	if !strings.HasPrefix(strings.Repeat("z", len(payload)), payload) {
		t.Fatal("payload is not the stored prefix")
	}
}

func TestContentDetailsStaySmall(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		io.WriteString(w, strings.Repeat("d", 1000))
	}))
	defer server.Close()
	client := testClient(testConfig())
	id := fetchContentID(t, client, server.URL)
	result := getContent(t, client, map[string]any{"responseId": id, "limit": 10})
	encoded, err := json.Marshal(result.Details)
	if err != nil {
		t.Fatal(err)
	}
	if len(encoded) > 50*1024 {
		t.Fatalf("details = %d bytes", len(encoded))
	}
}

func TestFindTextLongRepeatedPrefix(t *testing.T) {
	text := []rune(strings.Repeat("a", 1<<16) + "target")
	if got, ok := findTextOffset(text, "target"); !ok || got != 1<<16 {
		t.Fatalf("match at end: got %d ok=%v", got, ok)
	}
	if _, ok := findTextOffset(text, strings.Repeat("a", 4095)+"b"); ok {
		t.Fatal("near miss with a long repeated prefix matched")
	}
}

func TestFindTextWhitespaceCanBeLongerThanText(t *testing.T) {
	if got, ok := findTextOffset([]rune("a b"), "a  b"); !ok || got != 0 {
		t.Fatalf("got %d ok=%v", got, ok)
	}
}

type cancelAfterCheck struct {
	context.Context
	checks int
}

func (c *cancelAfterCheck) Err() error {
	c.checks++
	if c.checks > 1 {
		return context.Canceled
	}
	return nil
}

func TestFindTextChecksContextBetweenPasses(t *testing.T) {
	ctx := &cancelAfterCheck{Context: context.Background()}
	if _, _, err := findTextOffsetContext(ctx, []rune("abcdef"), "missing"); err == nil {
		t.Fatal("cancellation between find passes was ignored")
	}
}

func TestContentRetrievalKeepsSourceTruncationFlag(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		io.WriteString(w, strings.Repeat("a", maxBodyBytes+1))
	}))
	defer server.Close()
	client := testClient(testConfig())
	id := fetchContentID(t, client, server.URL)
	result := getContent(t, client, map[string]any{"responseId": id, "limit": 1})
	if result.IsError || !strings.Contains(result.Content[0].Text, "source-truncated: true") {
		t.Fatalf("truncation flag lost: %s", result.Content[0].Text)
	}
}
