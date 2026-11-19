package webaccess

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const crawl4AIExtractionTimeout = 60 * time.Second

// extractCrawl4AI asks the configured service for readable Markdown. The
// returned page uses the validated requested URL because the service's "url"
// field is not an established final-URL contract.
func (c *Client) extractCrawl4AI(ctx context.Context, target *url.URL) (pageData, error) {
	callCtx, cancel := context.WithTimeout(ctx, crawl4AIExtractionTimeout)
	defer cancel()

	payload, err := json.Marshal(struct {
		URL string `json:"url"`
		F   string `json:"f"`
	}{URL: target.String(), F: "fit"})
	if err != nil {
		return pageData{}, fmt.Errorf("%s: extraction request is invalid", classInvalid)
	}
	req, err := http.NewRequestWithContext(callCtx, http.MethodPost, c.cfg.Crawl4AIBaseURL+"/md", strings.NewReader(string(payload)))
	if err != nil {
		return pageData{}, fmt.Errorf("%s: extraction request is invalid", classInvalid)
	}
	req.Header.Set("Content-Type", "application/json")
	if c.cfg.Crawl4AIToken != "" {
		req.Header.Set("Authorization", "Bearer "+c.cfg.Crawl4AIToken)
	}

	resp, err := c.extractor.Do(req)
	if err != nil {
		return pageData{}, classifyExtractorTransport(callCtx, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusTooManyRequests {
		return pageData{}, fmt.Errorf("%s: 429", classRateLimit)
	}
	// A range response cannot contain a complete extraction envelope.
	if resp.StatusCode < 200 || resp.StatusCode > 299 || resp.StatusCode == http.StatusPartialContent {
		return pageData{}, fmt.Errorf("%s: %d", classStatus, resp.StatusCode)
	}
	if err := checkContentEncoding(resp); err != nil {
		return pageData{}, err
	}

	body, readErr := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes+1))
	if readErr != nil {
		if callCtx.Err() != nil {
			return pageData{}, wrapClass(callCtx, classTimeout, "body read interrupted")
		}
		return pageData{}, fmt.Errorf("%s: body read failed", classInvalid)
	}
	if len(body) > maxBodyBytes {
		return pageData{}, fmt.Errorf("%s: response exceeds 5 MiB", classInvalid)
	}
	root, err := decodeObject(body)
	if err != nil {
		return pageData{}, fmt.Errorf("%s: response must be one JSON object", classInvalid)
	}

	rawSuccess, ok := root["success"]
	if !ok || jsonNull(rawSuccess) {
		return pageData{}, fmt.Errorf("%s: response has no success field", classInvalid)
	}
	var success bool
	if err := json.Unmarshal(rawSuccess, &success); err != nil || !success {
		return pageData{}, fmt.Errorf("%s: extraction was not successful", classInvalid)
	}
	rawMarkdown, ok := root["markdown"]
	if !ok || jsonNull(rawMarkdown) {
		return pageData{}, fmt.Errorf("%s: response has no markdown field", classInvalid)
	}
	var markdown string
	if err := json.Unmarshal(rawMarkdown, &markdown); err != nil {
		return pageData{}, fmt.Errorf("%s: markdown must be one string", classInvalid)
	}
	markdown = strings.TrimSpace(markdown)
	if markdown == "" {
		return pageData{}, fmt.Errorf("%s: response has no markdown text", classInvalid)
	}

	// Reserve one whole marker of lookahead. Without it, a short secret can
	// expand into a marker that does not fit and is silently dropped at the cap.
	text := c.redact(markdown, maxBodyBytes+len(redactionMarker))
	if len(text) > maxBodyBytes {
		return pageData{}, fmt.Errorf("%s: redacted markdown exceeds 5 MiB", classInvalid)
	}
	title := crawl4AITitle(markdown)
	if title != "" {
		title = c.redact(title, 512)
	}

	requested := c.redact(target.String(), maxURLBytes)
	return pageData{
		URL:      requested,
		FinalURL: requested,
		Title:    title,
		Mode:     "readable",
		Text:     text,
	}, nil
}

func classifyExtractorTransport(ctx context.Context, err error) error {
	var urlErr *url.Error
	if errors.As(err, &urlErr) && hasClass(urlErr.Err.Error(), classInvalid) {
		return wrapClass(ctx, classInvalid, "extractor redirect body cannot be replayed")
	}
	return classifyTransport(ctx, err)
}

// crawl4AITitle returns the first single-hash heading followed by whitespace.
// Leading spaces and tabs are allowed; double-hash headings are ignored.
func crawl4AITitle(markdown string) string {
	for line := range strings.SplitSeq(markdown, "\n") {
		line = strings.TrimLeft(line, " \t")
		if !strings.HasPrefix(line, "#") || strings.HasPrefix(line, "##") {
			continue
		}
		rest := line[1:]
		if rest == "" || (rest[0] != ' ' && rest[0] != '\t') {
			continue
		}
		return strings.TrimSpace(rest)
	}
	return ""
}
