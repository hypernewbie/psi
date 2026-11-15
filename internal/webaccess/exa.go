package webaccess

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

func (c *Client) searchExa(ctx context.Context, query string, limit int, filter domainFilter, recency string) (searchResponse, error) {
	if c.cfg.ExaAPIKey != "" {
		return c.searchExaDirect(ctx, query, limit, filter, recency)
	}
	return c.searchExaMCP(ctx, query, limit, filter, recency)
}

type exaItem struct {
	Title         string   `json:"title"`
	URL           string   `json:"url"`
	PublishedDate string   `json:"publishedDate"`
	Author        string   `json:"author"`
	Text          string   `json:"text"`
	Highlights    []string `json:"highlights"`
}

// searchExaDirect uses Exa's JSON API with a resolved key.
func (c *Client) searchExaDirect(ctx context.Context, query string, limit int, filter domainFilter, recency string) (searchResponse, error) {
	callCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	payload := map[string]any{
		"query":      query,
		"type":       "auto",
		"numResults": limit,
		"contents":   map[string]any{"highlights": true},
	}
	if len(filter.include) > 0 {
		payload["includeDomains"] = filter.include
	}
	if len(filter.exclude) > 0 {
		payload["excludeDomains"] = filter.exclude
	}
	if recency != "" {
		payload["startPublishedDate"] = c.recencyStart(recency)
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return searchResponse{}, fmt.Errorf("%s: cannot encode provider request", classValidation)
	}
	endpoint := strings.TrimRight(c.cfg.ExaBaseURL, "/") + "/search"
	req, err := http.NewRequestWithContext(callCtx, http.MethodPost, endpoint, bytes.NewReader(encoded))
	if err != nil {
		return searchResponse{}, fmt.Errorf("%s: invalid provider endpoint", classValidation)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("x-api-key", c.cfg.ExaAPIKey)
	req.Header.Set("User-Agent", userAgent)

	resp, err := c.provider.Do(req)
	if err != nil {
		return searchResponse{}, classifyProviderError(callCtx, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusTooManyRequests {
		return searchResponse{}, fmt.Errorf("%s: 429", classRateLimit)
	}
	if resp.StatusCode != http.StatusOK {
		return searchResponse{}, fmt.Errorf("%s: %d", classStatus, resp.StatusCode)
	}
	body, err := readProviderBody(callCtx, resp)
	if err != nil {
		return searchResponse{}, err
	}
	var raw map[string]json.RawMessage
	if err := decodeSingleJSON(body, &raw); err != nil {
		return searchResponse{}, fmt.Errorf("%s: malformed provider JSON", classInvalid)
	}
	resultsRaw, ok := raw["results"]
	if !ok || string(resultsRaw) == "null" {
		return searchResponse{}, fmt.Errorf("%s: provider response has no results array", classInvalid)
	}
	var items []exaItem
	if err := json.Unmarshal(resultsRaw, &items); err != nil {
		return searchResponse{}, fmt.Errorf("%s: provider results is not an array", classInvalid)
	}
	results := projectExaItems(items, limit)
	return searchResponse{results: results}, nil
}

func projectExaItems(items []exaItem, limit int) []searchHit {
	if limit < 1 {
		return nil
	}
	var out []searchHit
	for _, item := range items {
		hit, ok := projectExaHit(item)
		if !ok {
			continue
		}
		out = append(out, hit)
		if len(out) == limit {
			break
		}
	}
	return out
}

func projectExaHit(item exaItem) (searchHit, bool) {
	target := strings.TrimSpace(item.URL)
	if target == "" || len(target) > maxURLBytes {
		return searchHit{}, false
	}
	if _, err := validateTarget(target); err != nil {
		return searchHit{}, false
	}
	snippet := strings.Join(item.Highlights, " ")
	if strings.TrimSpace(snippet) == "" {
		snippet = item.Text
	}
	return searchHit{
		Title:   clipMeta(item.Title, 300),
		URL:     clipMeta(target, maxURLBytes),
		Snippet: clipMeta(snippet, 2000),
		Engines: "exa",
		Date:    clipMeta(item.PublishedDate, 64),
	}, true
}

// searchExaMCP uses the keyless JSON-RPC endpoint. It never falls back from a
// failed keyed request, and a failed response is not retried.
func (c *Client) searchExaMCP(ctx context.Context, query string, limit int, filter domainFilter, recency string) (searchResponse, error) {
	requestID, err := randomRequestID()
	if err != nil {
		return searchResponse{}, fmt.Errorf("%s: cannot generate a request ID", classValidation)
	}
	callCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	payload := map[string]any{
		"jsonrpc": "2.0",
		"id":      requestID,
		"method":  "tools/call",
		"params": map[string]any{
			"name":      "web_search_exa",
			"arguments": map[string]any{"query": mcpQuery(query, filter, recency), "numResults": limit},
		},
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return searchResponse{}, fmt.Errorf("%s: cannot encode provider request", classValidation)
	}
	endpoint := c.mcpURL
	req, err := http.NewRequestWithContext(callCtx, http.MethodPost, endpoint, bytes.NewReader(encoded))
	if err != nil {
		return searchResponse{}, fmt.Errorf("%s: invalid provider endpoint", classValidation)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("User-Agent", userAgent)

	resp, err := c.provider.Do(req)
	if err != nil {
		return searchResponse{}, classifyProviderError(callCtx, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusTooManyRequests {
		return searchResponse{}, fmt.Errorf("%s: 429", classRateLimit)
	}
	if resp.StatusCode != http.StatusOK {
		return searchResponse{}, fmt.Errorf("%s: %d", classStatus, resp.StatusCode)
	}
	if err := checkContentEncoding(resp); err != nil {
		return searchResponse{}, err
	}
	contentType := strings.ToLower(resp.Header.Get("Content-Type"))
	var envelope []byte
	if strings.HasPrefix(contentType, "text/event-stream") {
		envelope, err = readSSEEnvelope(resp.Body, requestID, maxProviderBody)
	} else {
		envelope, err = readProviderBody(callCtx, resp)
	}
	if err != nil {
		if callCtx.Err() != nil {
			return searchResponse{}, wrapClass(callCtx, classTimeout, "provider request interrupted")
		}
		return searchResponse{}, err
	}
	text, err := parseExaEnvelope(envelope, requestID)
	if err != nil {
		return searchResponse{}, err
	}
	results := parseExaProse(text, limit)
	if len(results) == 0 {
		return searchResponse{}, fmt.Errorf("%s: provider prose has no results", classInvalid)
	}
	return searchResponse{results: results}, nil
}

func readProviderBody(ctx context.Context, resp *http.Response) ([]byte, error) {
	if err := checkContentEncoding(resp); err != nil {
		return nil, err
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxProviderBody+1))
	if err != nil {
		if ctx.Err() != nil {
			return nil, wrapClass(ctx, classTimeout, "body read interrupted")
		}
		return nil, fmt.Errorf("%s: body read failed", classInvalid)
	}
	if len(body) == 0 {
		return nil, fmt.Errorf("%s: empty provider response", classInvalid)
	}
	if len(body) > maxProviderBody {
		return nil, fmt.Errorf("%s: provider response exceeds 5 MiB", classInvalid)
	}
	return body, nil
}

func randomRequestID() (string, error) {
	var raw [8]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(raw[:]), nil
}

func formatOverview(provider, query string, results []searchHit, unresponsive []string) string {
	var b strings.Builder
	b.WriteString("Search results for: " + clipMeta(query, 300) + "\n")
	b.WriteString("provider: " + provider + "\n")
	b.WriteString(fmt.Sprintf("results: %d\n", len(results)))
	if len(unresponsive) > 0 {
		safe := make([]string, 0, len(unresponsive))
		for _, item := range unresponsive {
			safe = append(safe, clipMeta(item, 200))
		}
		b.WriteString("unresponsive engines: " + strings.Join(safe, "; ") + "\n")
	}
	for i, result := range results {
		b.WriteString(fmt.Sprintf("\n%d. %s\n", i+1, clipMeta(result.Title, 300)))
		b.WriteString("   " + clipMeta(result.URL, maxURLBytes) + "\n")
		if result.Snippet != "" {
			snippet := clipMeta(result.Snippet, 2000)
			if runes := []rune(snippet); len(runes) > 500 {
				snippet = string(runes[:500]) + "..."
			}
			b.WriteString("   " + snippet + "\n")
		}
		var meta []string
		if result.Engines != "" {
			meta = append(meta, "engines: "+clipMeta(result.Engines, 200))
		}
		if result.Date != "" {
			meta = append(meta, "date: "+clipMeta(result.Date, 64))
		}
		if result.Error != "" {
			b.WriteString("   fetch-error: " + clipMeta(result.Error, 200) + "\n")
		}
		if result.Truncated {
			b.WriteString("   source-truncated: true (5 MiB cap)\n")
		}
		if len(meta) > 0 {
			b.WriteString("   " + strings.Join(meta, " | ") + "\n")
		}
	}
	return b.String()
}

func (c *Client) clock() time.Time {
	if c.now != nil {
		return c.now()
	}
	return time.Now()
}

// recencyStart returns the rolling UTC window start used by the direct API.
func (c *Client) recencyStart(recency string) string {
	days := map[string]int{"day": 1, "month": 30, "year": 365}[recency]
	return c.clock().UTC().AddDate(0, 0, -days).Format(time.RFC3339)
}

func mcpQuery(query string, filter domainFilter, recency string) string {
	parts := []string{query}
	if ops := filter.searchOperators(); ops != "" {
		parts = append(parts, ops)
	}
	if phrase := recencyPhrase(recency); phrase != "" {
		parts = append(parts, phrase)
	}
	return strings.Join(parts, " ")
}

func recencyPhrase(recency string) string {
	switch recency {
	case "day":
		return "past 24 hours"
	case "month":
		return "past 30 days"
	case "year":
		return "past 365 days"
	}
	return ""
}
