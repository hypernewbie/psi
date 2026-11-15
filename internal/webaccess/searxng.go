package webaccess

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const maxProviderBody = 5 << 20

// searxngResult is the projected subset retained from one SearXNG item.
type searchHit struct {
	Title   string
	URL     string
	Snippet string
	Engines string
	Date    string
	Content string
	Error   string
}

// searchSearXNG issues one JSON request and projects bounded results.
func (c *Client) searchSearXNG(ctx context.Context, query string, limit int, filter domainFilter, recency string) (searchResponse, error) {
	base := c.cfg.SearXNGBaseURL
	if base == "" {
		return searchResponse{}, fmt.Errorf("%s: SearXNG is not configured", classValidation)
	}
	callCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	params := url.Values{}
	if ops := filter.searchOperators(); ops != "" {
		query = strings.TrimSpace(query + " " + ops)
	}
	params.Set("q", query)
	params.Set("format", "json")
	if recency != "" {
		params.Set("time_range", recency)
	}
	endpoint := strings.TrimRight(base, "/") + "/search?" + params.Encode()
	req, err := http.NewRequestWithContext(callCtx, http.MethodGet, endpoint, nil)
	if err != nil {
		return searchResponse{}, fmt.Errorf("%s: invalid provider endpoint", classValidation)
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "application/json")
	for name, values := range c.cfg.SearXNGHeaders {
		for _, value := range values {
			req.Header.Add(name, value)
		}
	}

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
	if enc := resp.Header.Get("Content-Encoding"); enc != "" && !strings.EqualFold(enc, "identity") && !resp.Uncompressed {
		return searchResponse{}, fmt.Errorf("%s: unsupported content encoding", classInvalid)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxProviderBody+1))
	if err != nil {
		if callCtx.Err() != nil {
			return searchResponse{}, wrapClass(callCtx, classTimeout, "body read interrupted")
		}
		return searchResponse{}, fmt.Errorf("%s: body read failed", classInvalid)
	}
	if len(body) == 0 {
		return searchResponse{}, fmt.Errorf("%s: empty provider response", classInvalid)
	}
	if len(body) > maxProviderBody {
		return searchResponse{}, fmt.Errorf("%s: provider response exceeds 5 MiB", classInvalid)
	}

	var raw map[string]json.RawMessage
	if err := decodeSingleJSON(body, &raw); err != nil {
		return searchResponse{}, fmt.Errorf("%s: malformed provider JSON", classInvalid)
	}
	if errRaw, ok := raw["error"]; ok && string(errRaw) != "null" {
		var message string
		if err := json.Unmarshal(errRaw, &message); err == nil && message != "" {
			return searchResponse{}, fmt.Errorf("%s: provider reported an error", classInvalid)
		}
	}
	resultsRaw, ok := raw["results"]
	if !ok || string(resultsRaw) == "null" {
		return searchResponse{}, fmt.Errorf("%s: provider response has no results array", classInvalid)
	}
	var items []json.RawMessage
	if err := json.Unmarshal(resultsRaw, &items); err != nil {
		return searchResponse{}, fmt.Errorf("%s: provider results is not an array", classInvalid)
	}

	var results []searchHit
	for _, item := range items {
		result, ok := projectSearXNGItem(item)
		if !ok {
			continue
		}
		results = append(results, result)
		if len(results) == limit {
			break
		}
	}

	out := searchResponse{results: results}
	if unresponsive, ok := raw["unresponsive_engines"]; ok && len(results) == 0 {
		out.unresponsive = projectUnresponsive(unresponsive)
	}
	return out, nil
}

func projectSearXNGItem(raw json.RawMessage) (searchHit, bool) {
	var item map[string]json.RawMessage
	if err := json.Unmarshal(raw, &item); err != nil || item == nil {
		return searchHit{}, false
	}
	str := func(key string) string {
		value, ok := item[key]
		if !ok || string(value) == "null" {
			return ""
		}
		var s string
		if err := json.Unmarshal(value, &s); err != nil {
			return ""
		}
		return s
	}
	target := strings.TrimSpace(str("url"))
	if target == "" {
		return searchHit{}, false
	}
	u, err := url.Parse(target)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return searchHit{}, false
	}
	if len(target) > maxURLBytes {
		return searchHit{}, false
	}
	engines := ""
	if value, ok := item["engines"]; ok && string(value) != "null" {
		var names []string
		if json.Unmarshal(value, &names) == nil {
			bounded := names[:min(len(names), 8)]
			for i, name := range bounded {
				bounded[i] = clipMeta(name, 64)
			}
			engines = strings.Join(bounded, ", ")
		} else {
			engines = clipMeta(str("engine"), 64)
		}
	}
	return searchHit{
		Title:   clipMeta(str("title"), 300),
		URL:     clipMeta(target, 2048),
		Snippet: clipMeta(str("content"), 2000),
		Engines: engines,
		Date:    clipMeta(str("publishedDate"), 64),
	}, true
}

func projectUnresponsive(raw json.RawMessage) []string {
	var pairs [][]json.RawMessage
	if err := json.Unmarshal(raw, &pairs); err != nil {
		return nil
	}
	var out []string
	for _, pair := range pairs {
		if len(pair) != 2 {
			continue
		}
		var engine, reason string
		if json.Unmarshal(pair[0], &engine) != nil || json.Unmarshal(pair[1], &reason) != nil {
			continue
		}
		if engine == "" {
			continue
		}
		out = append(out, clipMeta(engine+": "+reason, 200))
		if len(out) == 5 {
			break
		}
	}
	return out
}

func decodeSingleJSON(data []byte, target any) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	if err := dec.Decode(target); err != nil {
		return err
	}
	var extra any
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		return fmt.Errorf("trailing data")
	}
	return nil
}

func classifyProviderError(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return wrapClass(ctx, classTimeout, "provider request interrupted")
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return fmt.Errorf("%s: provider request timed out", classTimeout)
	}
	message := err.Error()
	if hasClass(message, classBlocked) {
		return fmt.Errorf("%s: destination is not public", classBlocked)
	}
	if hasClass(message, classDNS) {
		return fmt.Errorf("%s: lookup failed", classDNS)
	}
	return fmt.Errorf("%s: provider request failed", classTransport)
}
