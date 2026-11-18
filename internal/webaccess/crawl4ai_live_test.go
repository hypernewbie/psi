//go:build acceptance

package webaccess

import (
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"psi/internal/tools"
)

// TestCrawl4AILive requires an existing plugin config with a real Crawl4AI
// endpoint. It replaces only the direct page transport with a controlled 403.
func TestCrawl4AILive(t *testing.T) {
	if os.Getenv("PSI_LIVE_WEB") != "1" {
		t.Skip("set PSI_LIVE_WEB=1 to run live acceptance")
	}
	configPath := os.Getenv("PSI_WEB_CONFIG_PATH")
	expectedBase := os.Getenv("PSI_CRAWL4AI_EXPECT_BASE_URL")
	if configPath == "" || expectedBase == "" || filepath.Base(configPath) != "web-search.json" {
		t.Fatal("Crawl4AI live setup failed: validation: required inputs are missing")
	}
	info, err := os.Stat(configPath)
	if err != nil || info.IsDir() {
		t.Fatal("Crawl4AI live setup failed: validation: config file is unavailable")
	}

	env := OSEnv()
	baseLookup := env.LookupEnv
	env.LookupEnv = func(key string) (string, bool) {
		if key == "PI_CODING_AGENT_DIR" {
			return filepath.Dir(configPath), true
		}
		return baseLookup(key)
	}
	cfg, _ := Load(env)
	if !cfg.Crawl4AIAvailable || !cfg.Crawl4AIAllowed || !cfg.FetchEnabled || !cfg.ContentEnabled || !contains(cfg.FetchModes, "readable") {
		t.Fatal("Crawl4AI live preflight failed: validation: fallback is not enabled")
	}
	if strings.TrimRight(cfg.Crawl4AIBaseURL, "/") != strings.TrimRight(expectedBase, "/") {
		t.Fatal("Crawl4AI live preflight failed: validation: configured endpoint differs")
	}

	client := New(cfg)
	t.Cleanup(client.CloseIdle)
	client.page.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusForbidden,
			Header:     http.Header{},
			Body:       io.NopCloser(strings.NewReader("controlled direct denial")),
			Request:    r,
		}, nil
	})

	result := client.Dispatch(context.Background(), tools.Invocation{
		Name:      ToolFetch,
		Arguments: map[string]any{"url": "https://example.com", "mode": "readable"},
	})
	if result.IsError {
		t.Fatal("Crawl4AI live extraction failed: fixed failure class only")
	}
	details := batchDetails(t, result)
	id, _ := details["responseId"].(string)
	if id == "" {
		t.Fatal("Crawl4AI live extraction returned no responseId")
	}
	entry, ok := client.store.Get(id)
	if !ok || len(entry.Pages) != 1 || strings.TrimSpace(entry.Pages[0].Text) == "" {
		t.Fatal("Crawl4AI live extraction retained no page text")
	}
	storedText := entry.Pages[0].Text
	storedRunes := []rune(storedText)

	retrieved := client.Dispatch(context.Background(), tools.Invocation{
		Name:      ToolContent,
		Arguments: map[string]any{"responseId": id},
	})
	if retrieved.IsError {
		t.Fatal("Crawl4AI live retrieval failed: fixed failure class only")
	}
	_, payload, ok := strings.Cut(retrieved.Content[0].Text, "\ncontent:\n")
	if !ok {
		t.Fatal("Crawl4AI live retrieval lost its content boundary")
	}
	pageDetails := batchDetails(t, retrieved)
	offset, okOffset := pageDetails["offset"].(int)
	length, okLength := pageDetails["length"].(int)
	if !okOffset || !okLength || offset != 0 || length != len(storedRunes) {
		t.Fatal("Crawl4AI live retrieval returned invalid paging metadata")
	}
	end := length
	if next, ok := pageDetails["nextOffset"].(int); ok {
		end = next
	}
	if end <= offset || end > length {
		t.Fatal("Crawl4AI live retrieval returned an invalid page range")
	}
	if payload != string(storedRunes[offset:end]) {
		t.Fatalf("Crawl4AI live retrieval mismatch: payload=%d scalars range=%d:%d", utf8.RuneCountInString(payload), offset, end)
	}
	if _, more := pageDetails["nextOffset"]; !more && payload != storedText {
		t.Fatal("Crawl4AI live retrieval did not return the complete stored page")
	}
	if utf8.RuneCountInString(payload) == 0 {
		t.Fatal("Crawl4AI live retrieval returned an empty payload")
	}
	t.Logf("Crawl4AI live extraction passed: retained=%d scalars, returned=%d scalars", len(storedRunes), utf8.RuneCountInString(payload))
}
