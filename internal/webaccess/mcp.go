package webaccess

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strings"
)

// readSSEEnvelope reads bounded SSE events and returns the first JSON-RPC
// response whose id matches. It stops after a complete event and never waits
// for stream EOF.
func readSSEEnvelope(body io.Reader, requestID string, limit int) ([]byte, error) {
	reader := bufio.NewReaderSize(body, 64*1024)
	total := 0
	var data []string

	match := func() ([]byte, bool) {
		if len(data) == 0 {
			return nil, false
		}
		payload := strings.Join(data, "\n")
		data = data[:0]
		return []byte(payload), envelopeIDMatches(payload, requestID)
	}

	readLine := func(line string) ([]byte, bool) {
		line = strings.TrimSuffix(line, "\r")
		switch {
		case line == "":
			return match()
		case strings.HasPrefix(line, ":"):
			return nil, false
		case strings.HasPrefix(line, "data:"):
			value := strings.TrimPrefix(line, "data:")
			value = strings.TrimPrefix(value, " ")
			data = append(data, value)
			return nil, false
		default:
			// event:, id:, retry: fields are not needed for matching.
			return nil, false
		}
	}

	for {
		chunk, err := reader.ReadString('\n')
		total += len(chunk)
		if total > limit {
			return nil, fmt.Errorf("%s: provider response exceeds 5 MiB", classInvalid)
		}
		if payload, ok := readLine(strings.TrimSuffix(chunk, "\n")); ok {
			return payload, nil
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				if payload, ok := match(); ok {
					return payload, nil
				}
				return nil, fmt.Errorf("%s: no matching provider response", classInvalid)
			}
			return nil, fmt.Errorf("%s: stream read failed", classInvalid)
		}
	}
}

func envelopeIDMatches(payload, id string) bool {
	var head struct {
		ID json.RawMessage `json:"id"`
	}
	if err := json.Unmarshal([]byte(payload), &head); err != nil {
		return false
	}
	var got string
	if json.Unmarshal(head.ID, &got) == nil && got == id {
		return true
	}
	return false
}

type exaEnvelope struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Error   *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
	Result *struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		IsError bool                       `json:"isError"`
		Meta    map[string]json.RawMessage `json:"_meta"`
	} `json:"result"`
}

func parseExaEnvelope(payload []byte, requestID string) (string, error) {
	var envelope exaEnvelope
	if err := decodeSingleJSON(payload, &envelope); err != nil {
		return "", fmt.Errorf("%s: malformed provider envelope", classInvalid)
	}
	if envelope.JSONRPC != "2.0" {
		return "", fmt.Errorf("%s: unsupported provider protocol", classInvalid)
	}
	var id string
	if json.Unmarshal(envelope.ID, &id) != nil || id != requestID {
		return "", fmt.Errorf("%s: provider response ID mismatch", classInvalid)
	}
	if envelope.Error != nil {
		return "", fmt.Errorf("%s: provider reported an error", classInvalid)
	}
	if envelope.Result == nil {
		return "", fmt.Errorf("%s: provider response has no result", classInvalid)
	}
	if envelope.Result.IsError {
		return "", fmt.Errorf("%s: provider reported an error", classInvalid)
	}
	if rateLimited(envelope.Result.Meta) {
		return "", fmt.Errorf("%s: provider rate limit", classRateLimit)
	}
	var text strings.Builder
	for _, block := range envelope.Result.Content {
		if block.Type == "" || block.Type == "text" {
			if text.Len() > 0 {
				text.WriteString("\n")
			}
			text.WriteString(block.Text)
		}
	}
	if strings.TrimSpace(text.String()) == "" {
		return "", fmt.Errorf("%s: provider returned no text", classInvalid)
	}
	return text.String(), nil
}

func rateLimited(meta map[string]json.RawMessage) bool {
	raw, ok := meta["ai.exa/rateLimited"]
	if !ok {
		return false
	}
	var value bool
	return json.Unmarshal(raw, &value) == nil && value
}

func validResultURL(raw string) bool {
	if raw == "" || len(raw) > maxURLBytes {
		return false
	}
	u, err := url.Parse(raw)
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != ""
}

// parseExaProse projects the basic Title:/URL: result format into bounded hits.
func parseExaProse(text string, limit int) []searchHit {
	var hits []searchHit
	var current searchHit
	var snippet []string
	flush := func() {
		if current.URL != "" {
			current.Snippet = clipMeta(strings.Join(snippet, " "), 2000)
			hits = append(hits, current)
		}
		current = searchHit{}
		snippet = nil
	}
	for _, line := range strings.Split(text, "\n") {
		trimmed := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(trimmed, "Title:"):
			flush()
			current.Title = clipMeta(strings.TrimSpace(strings.TrimPrefix(trimmed, "Title:")), 300)
		case strings.HasPrefix(trimmed, "URL:"):
			target := strings.TrimSpace(strings.TrimPrefix(trimmed, "URL:"))
			if current.URL != "" {
				flush()
			}
			if validResultURL(target) {
				current.URL = clipMeta(target, 2048)
				current.Engines = "exa"
			}
		case strings.HasPrefix(trimmed, "Published:"):
			current.Date = clipMeta(strings.TrimSpace(strings.TrimPrefix(trimmed, "Published:")), 64)
		case strings.HasPrefix(trimmed, "Author:"):
			// Author is not retained.
		default:
			if trimmed != "" && current.URL != "" && len(snippet) < 20 {
				snippet = append(snippet, trimmed)
			}
		}
		if len(hits) == limit {
			break
		}
	}
	flush()
	if len(hits) > limit {
		hits = hits[:limit]
	}
	return hits
}
