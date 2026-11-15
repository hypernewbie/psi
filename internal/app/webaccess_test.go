package app

import (
	"net/http"
	"testing"

	"psi/internal/config"
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
