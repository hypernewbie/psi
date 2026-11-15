package webaccess

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func exaConfig(base string, key string) Config {
	cfg := testConfig()
	cfg.Provider = "exa"
	cfg.ExaBaseURL = base
	cfg.ExaAPIKey = key
	cfg.ExaAvailable = true
	return cfg
}

func TestExaDirectRequestShape(t *testing.T) {
	var gotPath, gotKey, gotIntegration, gotAccept string
	var gotBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotKey, gotIntegration, gotAccept = r.URL.Path, r.Header.Get("x-api-key"), r.Header.Get("x-exa-integration"), r.Header.Get("Accept")
		json.NewDecoder(r.Body).Decode(&gotBody)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"results":[
			{"title":"A","url":"https://a.example/","publishedDate":"2026-02-03","highlights":["hl one","hl two"]},
			{"title":"B","url":"https://b.example/","highlights":[]},
			{"title":"bad","url":"ftp://c.example/"}
		]}`)
	}))
	defer server.Close()
	client := testClient(exaConfig(server.URL+"/api/v1/", "exa-secret"))
	result := client.Dispatch(context.Background(), toolsInvocation(ToolSearch, map[string]any{"query": "q", "provider": "exa", "numResults": 2}))
	if result.IsError {
		t.Fatalf("search: %s", result.Content[0].Text)
	}
	if gotPath != "/api/v1/search" || gotKey != "exa-secret" || gotIntegration != "" {
		t.Fatalf("path=%q key=%q integration=%q", gotPath, gotKey, gotIntegration)
	}
	if gotAccept != "application/json" {
		t.Fatalf("accept = %q", gotAccept)
	}
	if gotBody["query"] != "q" || gotBody["type"] != "auto" || gotBody["numResults"] != float64(2) {
		t.Fatalf("body = %+v", gotBody)
	}
	contents, _ := gotBody["contents"].(map[string]any)
	if contents["highlights"] != true {
		t.Fatalf("contents = %+v", gotBody["contents"])
	}
	text := result.Content[0].Text
	if !strings.Contains(text, "hl one hl two") || !strings.Contains(text, "https://b.example/") {
		t.Fatalf("projection:\n%s", text)
	}
	if strings.Contains(text, "ftp://c.example/") {
		t.Fatalf("invalid URL survived:\n%s", text)
	}
}

func TestExaDirectInvalidResponses(t *testing.T) {
	for name, body := range map[string]string{
		"missing results": `{}`,
		"null results":    `{"results":null}`,
		"object results":  `{"results":{}}`,
		"trailing":        `{"results":[]}x`,
	} {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				io.WriteString(w, body)
			}))
			defer server.Close()
			result := testClient(exaConfig(server.URL, "k")).Dispatch(context.Background(), toolsInvocation(ToolSearch, map[string]any{"query": "q", "provider": "exa"}))
			if !result.IsError {
				t.Fatalf("expected error, got:\n%s", result.Content[0].Text)
			}
		})
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"results":[]}`)
	}))
	defer server.Close()
	result := testClient(exaConfig(server.URL, "k")).Dispatch(context.Background(), toolsInvocation(ToolSearch, map[string]any{"query": "q", "provider": "exa"}))
	if result.IsError {
		t.Fatalf("empty results must succeed: %s", result.Content[0].Text)
	}
}

func TestExaDirectRateLimitAndCredentialRedaction(t *testing.T) {
	for _, status := range []int{http.StatusTooManyRequests, http.StatusUnauthorized, http.StatusInternalServerError} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(status)
			fmt.Fprintf(w, "secret echo %s", r.Header.Get("x-api-key"))
		}))
		client := testClient(exaConfig(server.URL, "exa-secret"))
		result := client.Dispatch(context.Background(), toolsInvocation(ToolSearch, map[string]any{"query": "q", "provider": "exa"}))
		server.Close()
		if !result.IsError {
			t.Fatalf("%d: expected error", status)
		}
		if strings.Contains(result.Content[0].Text, "exa-secret") {
			t.Fatalf("%d: credential leaked: %s", status, result.Content[0].Text)
		}
	}
}

func mcpSSE(id string, texts ...string) string {
	content := make([]string, 0, len(texts))
	for _, text := range texts {
		encoded, _ := json.Marshal(text)
		content = append(content, `{"type":"text","text":`+string(encoded)+`}`)
	}
	envelope := `{"jsonrpc":"2.0","id":"` + id + `","result":{"content":[` + strings.Join(content, ",") + `],"isError":false}}`
	var b strings.Builder
	b.WriteString(": keep-alive comment\n\n")
	b.WriteString("event: message\r\n")
	const prefix = `{"jsonrpc":"2.0",`
	b.WriteString("data: " + prefix + "\r\n")
	b.WriteString("data: " + strings.TrimPrefix(envelope, prefix) + "\r\n")
	b.WriteString("\r\n")
	b.WriteString("event: message\ndata: {\"jsonrpc\":\"2.0\",\"id\":\"other\",\"result\":{}}\n\n")
	return b.String()
}

func TestExaMCPSSEParsing(t *testing.T) {
	prose := "Title: First\nURL: https://first.example/\nPublished: 2026-03-04\nHighlights:\n  useful text\n"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		params := body["params"].(map[string]any)
		if params["name"] != "web_search_exa" {
			t.Errorf("params = %+v", params)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, mcpSSE(body["id"].(string), prose))
	}))
	defer server.Close()
	client := testClient(exaConfig("https://api.exa.ai", ""))
	client.mcpURL = server.URL + "/mcp?tools=web_search_exa"
	result := client.Dispatch(context.Background(), toolsInvocation(ToolSearch, map[string]any{"query": "q", "numResults": 3}))
	if result.IsError {
		t.Fatalf("mcp search: %s", result.Content[0].Text)
	}
	if !strings.Contains(result.Content[0].Text, "https://first.example/") || !strings.Contains(result.Content[0].Text, "useful text") {
		t.Fatalf("prose projection:\n%s", result.Content[0].Text)
	}
	if !strings.Contains(result.Content[0].Text, "provider: exa") {
		t.Fatalf("provider:\n%s", result.Content[0].Text)
	}
}

func TestExaMCPLargeFrameAndIdleStream(t *testing.T) {
	prose := "Title: Big\nURL: https://big.example/\nHighlights:\n" + strings.Repeat("x", 70*1024) + "\n"
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, mcpSSE(body["id"].(string), prose))
		w.(http.Flusher).Flush()
		<-release // keep the stream open after a complete response
	}))
	defer server.Close()
	defer close(release)
	client := testClient(exaConfig("https://api.exa.ai", ""))
	client.mcpURL = server.URL
	done := make(chan toolsResultAlias, 1)
	go func() {
		result := client.Dispatch(context.Background(), toolsInvocation(ToolSearch, map[string]any{"query": "q", "numResults": 1}))
		done <- toolsResultAlias{isError: result.IsError, text: result.Content[0].Text}
	}()
	select {
	case result := <-done:
		if result.isError {
			t.Fatalf("search: %s", result.text)
		}
		if !strings.Contains(result.text, "https://big.example/") {
			t.Fatalf("result:\n%.300s", result.text)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("client waited for stream EOF")
	}
}

type toolsResultAlias struct {
	isError bool
	text    string
}

func TestExaMCPEnvelopeErrors(t *testing.T) {
	cases := map[string]string{
		"jsonrpc error": `{"jsonrpc":"2.0","id":"ID","error":{"code":-1,"message":"bad"}}`,
		"isError":       `{"jsonrpc":"2.0","id":"ID","result":{"content":[],"isError":true}}`,
		"rate limited":  `{"jsonrpc":"2.0","id":"ID","result":{"content":[{"type":"text","text":"x"}],"_meta":{"ai.exa/rateLimited":true}}}`,
		"wrong id":      `{"jsonrpc":"2.0","id":"OTHER","result":{"content":[{"type":"text","text":"x"}]}}`,
		"wrong version": `{"jsonrpc":"1.0","id":"ID","result":{"content":[{"type":"text","text":"x"}]}}`,
		"no text":       `{"jsonrpc":"2.0","id":"ID","result":{"content":[{"type":"text","text":""}]}}`,
		"malformed":     `not json`,
	}
	for name, template := range cases {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body map[string]any
				json.NewDecoder(r.Body).Decode(&body)
				payload := strings.ReplaceAll(template, "ID", body["id"].(string))
				w.Header().Set("Content-Type", "application/json")
				io.WriteString(w, payload)
			}))
			defer server.Close()
			client := testClient(exaConfig("https://api.exa.ai", ""))
			client.mcpURL = server.URL
			result := client.Dispatch(context.Background(), toolsInvocation(ToolSearch, map[string]any{"query": "q"}))
			if !result.IsError {
				t.Fatalf("%s should fail:\n%s", name, result.Content[0].Text)
			}
		})
	}
}

func TestExaRoutingAndNoKeyedFallback(t *testing.T) {
	var mcpRequests int
	mcp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mcpRequests++
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%q,"result":{"content":[]}}`, body["id"])
	}))
	defer mcp.Close()

	direct := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer direct.Close()

	// auto with no SearXNG selects Exa; with a key it uses the direct API.
	cfg := exaConfig(direct.URL, "k")
	cfg.Provider = "auto"
	client := testClient(cfg)
	if got, err := client.selectSearchProvider(""); err != nil || got != "exa" {
		t.Fatalf("auto provider = %q err=%v", got, err)
	}
	result := client.Dispatch(context.Background(), toolsInvocation(ToolSearch, map[string]any{"query": "q"}))
	if !result.IsError {
		t.Fatal("direct failure expected")
	}
	if mcpRequests != 0 {
		t.Fatalf("keyed failure fell back to keyless: %d", mcpRequests)
	}

	// No key: keyless path only.
	cfg = exaConfig("https://api.exa.ai", "")
	cfg.Provider = "auto"
	client = testClient(cfg)
	client.mcpURL = mcp.URL
	result = client.Dispatch(context.Background(), toolsInvocation(ToolSearch, map[string]any{"query": "q"}))
	if result.IsError && !strings.Contains(result.Content[0].Text, "no text") {
		t.Fatalf("keyless: %s", result.Content[0].Text)
	}
	if mcpRequests != 1 {
		t.Fatalf("mcp requests = %d", mcpRequests)
	}
}

func TestExaMCPCancellationMidStream(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "event: message\ndata: {\"jsonrpc\":\"2.0\",\"id\":\"")
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer server.Close()
	client := testClient(exaConfig("https://api.exa.ai", ""))
	client.mcpURL = server.URL
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	result := client.Dispatch(ctx, toolsInvocation(ToolSearch, map[string]any{"query": "q"}))
	if !result.IsError {
		t.Fatal("cancelled stream must fail")
	}
	if !strings.Contains(result.Content[0].Text, classTimeout) && !strings.Contains(result.Content[0].Text, classCancelled) && !strings.Contains(result.Content[0].Text, classInvalid) {
		t.Fatalf("result: %s", result.Content[0].Text)
	}
}

func TestSearchRedactsProviderEchoes(t *testing.T) {
	direct := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"results":[{"title":"exa-secret\u0000tail","url":"https://a.example/?k=exa-secret","highlights":["echo exa-secret"]}]}`)
	}))
	defer direct.Close()
	client := testClient(exaConfig(direct.URL, "exa-secret"))
	result := client.Dispatch(context.Background(), toolsInvocation(ToolSearch, map[string]any{"query": "q", "provider": "exa"}))
	if result.IsError {
		t.Fatalf("search: %s", result.Content[0].Text)
	}
	if strings.Contains(result.Content[0].Text, "exa-secret") || strings.Contains(result.Content[0].Text, "\x00") {
		t.Fatalf("provider echo or control byte leaked:\n%s", result.Content[0].Text)
	}
	if !strings.Contains(result.Content[0].Text, "[redacted]") {
		t.Fatalf("redaction marker missing:\n%s", result.Content[0].Text)
	}

	searx := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"results":[{"title":"header-secret","url":"https://b.example/","content":"header-secret"}]}`)
	}))
	defer searx.Close()
	cfg := searxConfig(t, searx.URL)
	cfg.SearXNGHeaders = map[string][]string{"X-Token": {"header-secret"}}
	client = testClient(cfg)
	client.redactor = newRedactor(cfg)
	result = client.Dispatch(context.Background(), toolsInvocation(ToolSearch, map[string]any{"query": "q"}))
	if result.IsError {
		t.Fatalf("searxng search: %s", result.Content[0].Text)
	}
	if strings.Contains(result.Content[0].Text, "header-secret") {
		t.Fatalf("header echo leaked:\n%s", result.Content[0].Text)
	}
}

func TestExaMCPMultipleTextBlocks(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%q,"result":{"content":[
			{"type":"text","text":"Title: First\nURL: https://first.example/"},
			{"type":"text","text":"useful highlight"},
			{"type":"text","text":"Title: Second\nURL: https://second.example/"}
		]}}`, body["id"])
	}))
	defer server.Close()
	client := testClient(exaConfig("https://api.exa.ai", ""))
	client.mcpURL = server.URL
	result := client.Dispatch(context.Background(), toolsInvocation(ToolSearch, map[string]any{"query": "q"}))
	if result.IsError {
		t.Fatalf("search: %s", result.Content[0].Text)
	}
	text := result.Content[0].Text
	if !strings.Contains(text, "https://first.example/") || !strings.Contains(text, "https://second.example/") {
		t.Fatalf("blocks lost:\n%s", text)
	}
	if !strings.Contains(text, "useful highlight") {
		t.Fatalf("joined text lost highlight:\n%s", text)
	}
}
