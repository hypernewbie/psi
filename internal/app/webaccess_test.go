package app

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync/atomic"
	"testing"

	"psi/internal/config"
	"psi/internal/tools"
	"psi/internal/webaccess"
)

func testWebConfig() webaccess.Config {
	return webaccess.Config{
		Provider:        "auto",
		ExaBaseURL:      "https://api.exa.ai",
		SearchEnabled:   true,
		FetchEnabled:    true,
		ContentEnabled:  true,
		FetchMode:       "readable",
		FetchModes:      []string{"readable", "raw"},
		MaxInlineChars:  webaccess.DefaultInlineChars,
		CacheMaxEntries: webaccess.DefaultCacheEntries,
		CacheMaxBytes:   webaccess.DefaultCacheBytes,
		ExaAvailable:    true,
		SearXNGHeaders:  http.Header{},
	}
}

func TestWebDefinitionsRespectSelectionAndEnablement(t *testing.T) {
	client := webaccess.New(testWebConfig())

	r := &runtime{
		web:      client,
		settings: config.Settings{DefaultTools: []string{"read", "fetch_content"}},
	}
	defs := r.webDefinitions()
	if len(defs) != 1 || defs[0].Name != webaccess.ToolFetch {
		t.Fatalf("definitions = %+v", defs)
	}

	r.settings.DefaultTools = []string{"read"}
	if defs := r.webDefinitions(); len(defs) != 0 {
		t.Fatalf("unselected web tool advertised: %+v", defs)
	}

	r.settings.DefaultTools = []string{"fetch_content"}
	disabled := testWebConfig()
	disabled.FetchEnabled = false
	r.web = webaccess.New(disabled)
	if defs := r.webDefinitions(); len(defs) != 0 {
		t.Fatalf("disabled web tool advertised: %+v", defs)
	}
	if handlers := r.webHandlers(); len(handlers) != 0 {
		t.Fatalf("disabled web handler registered: %v", handlers)
	}
}

func TestWebHandlersMatchAdvertisedTools(t *testing.T) {
	client := webaccess.New(testWebConfig())
	r := &runtime{
		web:      client,
		settings: config.Settings{DefaultTools: []string{"fetch_content"}},
	}
	handlers := r.webHandlers()
	if len(handlers) != 1 || handlers[webaccess.ToolFetch] == nil {
		t.Fatalf("handlers = %v", handlers)
	}
}

func TestWebSearchInlineContentRespectsCLISelection(t *testing.T) {
	for _, fetchSelected := range []bool{false, true} {
		name := "fetch excluded"
		if fetchSelected {
			name = "fetch selected"
		}
		t.Run(name, func(t *testing.T) {
			const extracted = "SELECTED-INLINE-EXTRACTED"
			var searches, pages, extractions atomic.Int32
			page := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				pages.Add(1)
				http.Error(w, "denied", http.StatusForbidden)
			}))
			defer page.Close()
			extractor := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				extractions.Add(1)
				io.WriteString(w, `{"success":true,"markdown":"`+extracted+`"}`)
			}))
			defer extractor.Close()
			search := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				searches.Add(1)
				json.NewEncoder(w).Encode(map[string]any{"results": []map[string]string{{"url": page.URL + "/page"}}})
			}))
			defer search.Close()
			cfg := testWebConfig()
			cfg.SearXNGBaseURL = search.URL
			cfg.SearXNGAvailable = true
			cfg.Crawl4AIBaseURL = extractor.URL
			cfg.Crawl4AIAvailable = true
			cfg.Crawl4AIAllowed = true
			cfg.AllowRanges = []netip.Prefix{netip.MustParsePrefix("127.0.0.1/32")}
			client := webaccess.New(cfg)
			defer client.CloseIdle()
			selected := []string{webaccess.ToolSearch}
			if fetchSelected {
				selected = append(selected, webaccess.ToolFetch, webaccess.ToolContent)
			}
			r := &runtime{web: client, settings: config.Settings{DefaultTools: selected}}
			handlers := r.webHandlers()
			handler := handlers[webaccess.ToolSearch]
			result := handler(context.Background(), tools.Invocation{
				Name: webaccess.ToolSearch, Arguments: map[string]any{"query": "q", "includeContent": true},
			})
			wantRequests := int32(0)
			if fetchSelected {
				wantRequests = 1
			}
			wantError := !fetchSelected
			if result.IsError != wantError || searches.Load() != wantRequests || pages.Load() != wantRequests || extractions.Load() != wantRequests {
				t.Fatalf("inline search: error=%t search requests=%d page requests=%d extractor requests=%d", result.IsError, searches.Load(), pages.Load(), extractions.Load())
			}
			if !fetchSelected {
				if !strings.Contains(result.Content[0].Text, "includeContent requires fetch_content") {
					t.Fatalf("missing actionable permission error: %s", result.Content[0].Text)
				}
			} else {
				details, _ := result.Details.(map[string]any)
				id, _ := details["responseId"].(string)
				if details["fetched"] != 1 || id == "" {
					t.Fatalf("selected inline fetch did not retain extracted content: %+v", result.Details)
				}
				content := handlers[webaccess.ToolContent](context.Background(), tools.Invocation{
					Name: webaccess.ToolContent, Arguments: map[string]any{"responseId": id, "urlIndex": 0},
				})
				if content.IsError || !strings.Contains(content.Content[0].Text, extracted) {
					t.Fatalf("selected inline extraction not retrievable: %s", content.Content[0].Text)
				}
			}
			plain := handler(context.Background(), tools.Invocation{Name: webaccess.ToolSearch, Arguments: map[string]any{"query": "q"}})
			if plain.IsError || searches.Load() != wantRequests+1 || pages.Load() != wantRequests || extractions.Load() != wantRequests {
				t.Fatal("fetch selection changed ordinary search behavior")
			}
		})
	}
}

func TestFilterPromptToolsDropsDisabledWebTools(t *testing.T) {
	disabled := testWebConfig()
	disabled.FetchEnabled = false
	client := webaccess.New(disabled)
	names := filterPromptTools([]string{"read", "fetch_content"}, client)
	if len(names) != 1 || names[0] != "read" {
		t.Fatalf("prompt tools = %v", names)
	}
	names = filterPromptTools([]string{"read", "fetch_content"}, webaccess.New(testWebConfig()))
	if len(names) != 2 {
		t.Fatalf("enabled prompt tools = %v", names)
	}
	if names := filterPromptTools([]string{"read", "fetch_content"}, nil); len(names) != 2 {
		t.Fatalf("nil client must not filter: %v", names)
	}
}

func TestNilWebClientHasNoDefinitions(t *testing.T) {
	r := &runtime{settings: config.Settings{DefaultTools: []string{"fetch_content"}}}
	if defs := r.webDefinitions(); len(defs) != 0 {
		t.Fatalf("nil client advertised tools: %+v", defs)
	}
	if handlers := r.webHandlers(); len(handlers) != 0 {
		t.Fatalf("nil client registered handlers: %v", handlers)
	}
}
