package webaccess

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSearchRedactsJoinedEngineNames(t *testing.T) {
	const secret = "engine-one, engine-two"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"results":[{"url":"https://example.com/","engines":["engine-one","engine-two"]}]}`)
	}))
	defer server.Close()
	cfg := searxConfig(t, server.URL)
	cfg.SearXNGHeaders = http.Header{"X-Test-Key": []string{secret}}
	client := testClient(cfg)
	defer client.CloseIdle()
	result := client.Dispatch(context.Background(), toolsInvocation(ToolSearch, map[string]any{"query": "q"}))
	text := result.Content[0].Text
	if result.IsError || strings.Contains(text, secret) || !strings.Contains(text, "engines: [redacted]") {
		t.Fatalf("joined engine metadata did not redact the configured value: %s", text)
	}
}

func TestProviderMetadataRedactsBeforeClipping(t *testing.T) {
	const secret = "metadata-boundary-secret"
	// Leave exactly enough space for a redaction marker, but not the secret.
	titlePrefix := strings.Repeat("t", 300-len("[redacted]"))
	datePrefix := strings.Repeat("d", 64-len("[redacted]"))
	enginePrefix := strings.Repeat("e", 200-len("[redacted]"))
	for _, provider := range []string{"searxng", "exa direct", "exa keyless"} {
		t.Run(provider, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if provider == "exa keyless" {
					var request map[string]json.RawMessage
					if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
						t.Error(err)
						http.Error(w, "bad request", http.StatusBadRequest)
						return
					}
					text := fmt.Sprintf("Title: %s\nURL: https://example.com/\nPublished: %s\n", titlePrefix+secret, datePrefix+secret)
					json.NewEncoder(w).Encode(map[string]any{
						"jsonrpc": "2.0", "id": request["id"],
						"result": map[string]any{"content": []map[string]string{{"type": "text", "text": text}}},
					})
					return
				}
				json.NewEncoder(w).Encode(map[string]any{"results": []map[string]any{{
					"url": "https://example.com/", "title": titlePrefix + secret,
					"publishedDate": datePrefix + secret, "engines": []string{enginePrefix + secret},
				}}})
			}))
			defer server.Close()
			cfg := searxConfig(t, server.URL)
			cfg.SearXNGHeaders = http.Header{"X-Test-Key": []string{secret}}
			if provider != "searxng" {
				cfg.Provider = "exa"
				cfg.ExaBaseURL = server.URL
				if provider == "exa direct" {
					cfg.ExaAPIKey = secret
				}
			}
			client := testClient(cfg)
			defer client.CloseIdle()
			client.mcpURL = server.URL
			result := client.Dispatch(context.Background(), toolsInvocation(ToolSearch, map[string]any{"query": "q"}))
			if result.IsError {
				t.Fatalf("search: %s", result.Content[0].Text)
			}
			text := result.Content[0].Text
			prefixes := []string{titlePrefix, datePrefix}
			if provider == "searxng" {
				prefixes = append(prefixes, enginePrefix)
			}
			for _, prefix := range prefixes {
				if !strings.Contains(text, prefix+"[redacted]") {
					t.Fatalf("metadata lost its redaction marker at the clipping boundary: %s", text)
				}
			}
			if strings.Contains(text, "metadata-b") {
				t.Fatalf("configured secret prefix survived clipping: %s", text)
			}
		})
	}
}
