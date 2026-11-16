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
