//go:build acceptance

package app

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

// This built-binary check uses only synthetic configuration and loopback
// fixtures. It does not require PSI_LIVE_WEB or a provider credential.
func TestWebAccessLoopbackFetchExclusionBinary(t *testing.T) {
	if os.Getenv("PSI_WEB_BINARY") == "" {
		t.Skip("set PSI_WEB_BINARY to the built psi executable")
	}
	for _, selection := range []struct {
		name string
		args []string
	}{
		{"replacement", []string{"--tools", "web_search"}},
		{"exclusion", []string{"--exclude-tools", "fetch_content"}},
	} {
		t.Run(selection.name, func(t *testing.T) {
			var requests atomic.Int32
			var search *httptest.Server
			search = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				if r.URL.Path == "/search" {
					fmt.Fprintf(w, `{"results":[{"url":%q}]}`, search.URL+"/page")
					return
				}
				w.Header().Set("Content-Type", "text/plain")
				fmt.Fprint(w, "page text")
			}))
			defer search.Close()
			configPath := filepath.Join(t.TempDir(), "web-search.json")
			writeAppFile(t, configPath, fmt.Sprintf(`{"searxngBaseUrl":%q,"ssrf":{"allowRanges":["127.0.0.1/32"]}}`, search.URL))
			t.Setenv("PSI_WEB_CONFIG_PATH", configPath)
			env := prepareAcceptance(t, func(turn int, _ []string) (string, string) {
				if turn == 0 {
					return "web_search", `{"query":"q","includeContent":true}`
				}
				return "", ""
			})
			args := append([]string{"--mode", "rpc", "--provider", "anthropic", "--model", "web-fixture",
				"--deny-project", "--no-session", "--no-context-files", "--no-skills"}, selection.args...)
			env.run(t, args...)
			turns := env.snapshotTurns()
			if len(turns) != 2 || !contains(turns[0].tools, "web_search") || contains(turns[0].tools, "fetch_content") {
				t.Fatalf("unexpected advertised tools or turn count: %+v", turns)
			}
			if len(turns[1].results) != 1 || !strings.Contains(turns[1].results[0], "includeContent requires fetch_content") {
				t.Fatalf("expected inline-fetch permission error, got %+v", turns[1].results)
			}
			if requests.Load() != 0 {
				t.Fatalf("excluded fetch made %d provider/page requests", requests.Load())
			}
		})
	}
}

func TestWebAccessLoopbackCrawl4AIBinary(t *testing.T) {
	if os.Getenv("PSI_WEB_BINARY") == "" {
		t.Skip("set PSI_WEB_BINARY to the built psi executable")
	}
	const (
		tokenCanary = "invented-crawl4ai-token"
		markdown    = "BINARY-CRAWL4AI-UNIQUE"
	)
	for _, tc := range []struct {
		name           string
		providers      string
		wantExtraction bool
	}{
		{"disabled routing", `["http"]`, false},
		{"enabled routing", `["http","crawl4ai"]`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var pageCalls, extractorCalls atomic.Int32
			page := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				pageCalls.Add(1)
				http.Error(w, "denied", http.StatusForbidden)
			}))
			defer page.Close()
			extractor := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				extractorCalls.Add(1)
				if r.Method != http.MethodPost || r.URL.Path != "/base/md" {
					t.Errorf("extractor request = %s %s", r.Method, r.URL.Path)
				}
				if got := r.Header.Get("Authorization"); got != "Bearer "+tokenCanary {
					t.Errorf("extractor bearer header did not match")
				}
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprintf(w, `{"success":true,"markdown":%q}`, markdown)
			}))
			defer extractor.Close()

			configPath := filepath.Join(t.TempDir(), "web-search.json")
			writeAppFile(t, configPath, fmt.Sprintf(`{
				"crawl4aiBaseUrl":%q,"crawl4aiApiToken":%q,
				"fetchRouting":{"providers":%s},
				"ssrf":{"allowRanges":["127.0.0.1/32"]}
			}`, extractor.URL+"/base", tokenCanary, tc.providers))
			t.Setenv("PSI_WEB_CONFIG_PATH", configPath)

			target := page.URL + "/doc"
			env := prepareAcceptance(t, func(turn int, results []string) (string, string) {
				switch turn {
				case 0:
					return "fetch_content", fmt.Sprintf(`{"url":%q,"mode":"readable"}`, target)
				case 1:
					if !tc.wantExtraction {
						return "", ""
					}
					id := responseIDValue(results[0])
					if id == "" {
						t.Errorf("enabled fallback returned no responseId: %.300q", results[0])
						return "", ""
					}
					return "get_search_content", fmt.Sprintf(`{"responseId":%q}`, id)
				}
				return "", ""
			})
			env.run(t, "--mode", "rpc", "--tools", "fetch_content,get_search_content",
				"--provider", "anthropic", "--model", "web-fixture",
				"--deny-project", "--no-session", "--no-context-files", "--no-skills")

			turns := env.snapshotTurns()
			if len(turns) < 2 || len(turns[1].results) != 1 {
				t.Fatalf("expected a fetch result, got %+v", turns)
			}
			fetchResult := turns[1].results[0]
			if !tc.wantExtraction {
				if !strings.Contains(fetchResult, "fetch_content failed") || !strings.Contains(fetchResult, "http status: 403") {
					t.Fatalf("disabled fallback result = %.400q", fetchResult)
				}
				if got := extractorCalls.Load(); got != 0 {
					t.Fatalf("disabled routing made %d extractor calls", got)
				}
				return
			}

			if strings.Contains(fetchResult, "fetch_content failed") || !strings.Contains(fetchResult, markdown) {
				t.Fatalf("enabled fallback result = %.500q", fetchResult)
			}
			if strings.Contains(fetchResult, tokenCanary) {
				t.Fatal("enabled fallback result leaked the extractor token")
			}
			if len(turns) != 3 || len(turns[2].results) < 2 {
				t.Fatalf("expected a retrieval result, got %+v", turns)
			}
			retrieved := turns[2].results[len(turns[2].results)-1]
			_, payload, ok := strings.Cut(retrieved, "\ncontent:\n")
			if !ok || payload != markdown {
				t.Fatalf("retained payload differs from extracted Markdown: %.400q", retrieved)
			}
			if got := pageCalls.Load(); got != 1 {
				t.Fatalf("page calls = %d, want 1", got)
			}
			if got := extractorCalls.Load(); got != 1 {
				t.Fatalf("extractor calls = %d, want 1", got)
			}
		})
	}
}

func TestWebAccessLoopbackHeaderProfileBinary(t *testing.T) {
	if os.Getenv("PSI_WEB_BINARY") == "" {
		t.Skip("set PSI_WEB_BINARY to the built psi executable")
	}
	const (
		wantUA     = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.5 Safari/605.1.15"
		wantAccept = "text/html,application/xhtml+xml;q=0.9,text/plain;q=0.8,text/markdown;q=0.7,*/*;q=0.1"
		wantLang   = "en-US,en;q=0.9"
		body       = "safari-profile-page"
	)
	var requests atomic.Int32
	page := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.Header.Get("User-Agent") != wantUA || r.Header.Get("Accept") != wantAccept || r.Header.Get("Accept-Language") != wantLang {
			http.Error(w, "unexpected headers", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "text/plain")
		fmt.Fprint(w, body)
	}))
	defer page.Close()
	configPath := filepath.Join(t.TempDir(), "web-search.json")
	writeAppFile(t, configPath, `{"ssrf":{"allowRanges":["127.0.0.1/32"]},"fetch":{"headerProfile":"safari","acceptLanguage":"en-US,en;q=0.9"}}`)
	t.Setenv("PSI_WEB_CONFIG_PATH", configPath)
	env := prepareAcceptance(t, func(turn int, _ []string) (string, string) {
		if turn == 0 {
			return "fetch_content", fmt.Sprintf(`{"url":%q,"mode":"readable"}`, page.URL+"/page")
		}
		return "", ""
	})
	env.run(t, "--mode", "rpc", "--provider", "anthropic", "--model", "web-fixture",
		"--deny-project", "--no-session", "--no-context-files", "--no-skills",
		"--tools", "fetch_content")
	turns := env.snapshotTurns()
	if len(turns) != 2 || !contains(turns[0].tools, "fetch_content") {
		t.Fatalf("unexpected advertised tools or turn count: %+v", turns)
	}
	if len(turns[1].results) != 1 || !strings.Contains(turns[1].results[0], body) {
		t.Fatalf("expected page text %q, got %+v", body, turns[1].results)
	}
	if got := requests.Load(); got != 1 {
		t.Fatalf("page requests = %d, want 1", got)
	}
}
