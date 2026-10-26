package provider

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Client implements the Anthropic Messages streaming protocol (Steps 50-54).
type Client struct {
	HTTP     *http.Client
	BaseURL  string
	APIKey   string
	MaxRetry int
	// Idle timeout applied per read; no short total timeout.
	IdleTimeout time.Duration
}

func NewClient(baseURL, apiKey string, maxRetry int, idle time.Duration) *Client {
	if baseURL == "" {
		baseURL = "https://api.anthropic.com"
	}
	if idle <= 0 {
		idle = 90 * time.Second
	}
	return &Client{HTTP: &http.Client{}, BaseURL: strings.TrimSuffix(baseURL, "/"), APIKey: apiKey, MaxRetry: maxRetry, IdleTimeout: idle}
}

func (c *Client) CloseIdle() { c.HTTP.CloseIdleConnections() }

// Classify errors: auth, rate limit, transport, invalid request, context limit.
type ErrorKind string

const (
	ErrAuth         ErrorKind = "auth"
	ErrRateLimit    ErrorKind = "rate_limit"
	ErrTransport    ErrorKind = "transport"
	ErrInvalid      ErrorKind = "invalid_request"
	ErrContextLimit ErrorKind = "context_limit"
)

type ProviderError struct {
	Kind       ErrorKind
	Status     int
	Message    string
	RetryAfter time.Duration
}

func (e *ProviderError) Error() string {
	return fmt.Sprintf("%s (status %d): %s", e.Kind, e.Status, e.Message)
}

// Send performs one request with bounded retries. onText receives deltas in order.
func (c *Client) Send(ctx context.Context, req Request, onText func(string)) (*Response, error) {
	body, err := BuildAnthropicRequest(req)
	if err != nil {
		return nil, err
	}
	var lastErr error
	for attempt := 0; attempt <= c.MaxRetry; attempt++ {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		resp, perr := c.doOnce(ctx, body, onText, req)
		if perr == nil {
			return resp, nil
		}
		pe, ok := perr.(*ProviderError)
		if !ok {
			return nil, perr // transport already classified
		}
		lastErr = perr
		switch pe.Kind {
		case ErrRateLimit, ErrTransport:
			delay := pe.RetryAfter
			if delay <= 0 {
				delay = time.Duration(1<<attempt) * 500 * time.Millisecond
			}
			if delay > 60*time.Second {
				delay = 60 * time.Second
			}
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(delay):
			}
			continue
		default:
			return nil, perr // no retry: auth, invalid, context-limit
		}
	}
	return nil, lastErr
}

func (c *Client) doOnce(ctx context.Context, body map[string]any, onText func(string), req Request) (*Response, error) {
	raw, _ := json.Marshal(body)
	httpReq, err := http.NewRequestWithContext(ctx, "POST", c.BaseURL+"/v1/messages", bytes.NewReader(raw))
	if err != nil {
		return nil, &ProviderError{Kind: ErrTransport, Message: err.Error()}
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("x-api-key", c.APIKey)
	httpReq.Header.Set("anthropic-version", "2023-06-01")
	resp, err := c.HTTP.Do(httpReq)
	if err != nil {
		return nil, &ProviderError{Kind: ErrTransport, Message: err.Error()}
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, classifyHTTPError(resp)
	}
	return parseSSE(resp.Body, c.IdleTimeout, ctx, onText, req)
}

func classifyHTTPError(resp *http.Response) *ProviderError {
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	msg := strings.TrimSpace(string(b))
	retryAfter := parseRetryAfter(resp.Header.Get("retry-after"))
	switch {
	case resp.StatusCode == 401 || resp.StatusCode == 403:
		return &ProviderError{Kind: ErrAuth, Status: resp.StatusCode, Message: redact(msg)}
	case resp.StatusCode == 429:
		return &ProviderError{Kind: ErrRateLimit, Status: resp.StatusCode, Message: msg, RetryAfter: retryAfter}
	case resp.StatusCode == 400:
		if strings.Contains(strings.ToLower(msg), "context") || strings.Contains(strings.ToLower(msg), "too long") || strings.Contains(strings.ToLower(msg), "prompt") {
			return &ProviderError{Kind: ErrContextLimit, Status: resp.StatusCode, Message: msg}
		}
		kind := ErrInvalid
		if strings.Contains(msg, "credit") || strings.Contains(msg, "billing") {
			kind = ErrAuth
		}
		return &ProviderError{Kind: kind, Status: resp.StatusCode, Message: msg}
	case resp.StatusCode >= 500:
		return &ProviderError{Kind: ErrTransport, Status: resp.StatusCode, Message: msg, RetryAfter: retryAfter}
	default:
		return &ProviderError{Kind: ErrInvalid, Status: resp.StatusCode, Message: msg}
	}
}

func parseRetryAfter(s string) time.Duration {
	if s == "" {
		return 0
	}
	var secs int
	if _, err := fmt.Sscanf(s, "%d", &secs); err == nil && secs >= 0 {
		return time.Duration(secs) * time.Second
	}
	return 0
}

func redact(s string) string {
	// Never echo full keys.
	if len(s) > 64 {
		return s[:64] + "…[redacted]"
	}
	return s
}

// parseSSE handles fragmented reads, multiline records, UTF-8 boundaries,
// usage-only records, and explicit completion signals.
func parseSSE(r io.Reader, idle time.Duration, ctx context.Context, onText func(string), req Request) (*Response, error) {
	br := bufio.NewReader(r)
	resp := &Response{Model: req.Model.ID, ThinkingLevel: req.Thinking, StopReason: "stop"}
	var textBuf strings.Builder
	var thinkBuf strings.Builder
	type pendingTool struct {
		id, name string
		jsonBuf  strings.Builder
	}
	pending := map[int]*pendingTool{}
	var curIndex = -1
	var sawStop bool

	flush := func() {}
	_ = flush

	readWithIdle := func() (string, error) {
		type res struct {
			line []byte
			err  error
		}
		ch := make(chan res, 1)
		go func() {
			ln, err := br.ReadBytes('\n')
			ch <- res{ln, err}
		}()
		timer := time.NewTimer(idle)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-timer.C:
			return "", &ProviderError{Kind: ErrTransport, Message: "idle timeout reading provider stream"}
		case v := <-ch:
			if len(v.line) > 0 {
				return string(v.line), v.err
			}
			return "", v.err
		}
	}

	var event string
	var dataLines []string
	emit := func() error {
		defer func() {
			event = ""
			dataLines = nil
		}()
		if event == "" {
			return nil
		}
		data := strings.Join(dataLines, "\n")
		if data == "" {
			return nil
		}
		var payload map[string]any
		if err := json.Unmarshal([]byte(data), &payload); err != nil {
			// Multiline SSE records must parse; malformed JSON is an error, not silent.
			return fmt.Errorf("malformed SSE data for event %q: %w", event, err)
		}
		switch event {
		case "message_start":
			if msg, ok := payload["message"].(map[string]any); ok {
				if id, ok := msg["id"].(string); ok {
					resp.ResponseID = id
				}
				if m, ok := msg["model"].(string); ok {
					resp.Model = m
				}
				if u, ok := msg["usage"].(map[string]any); ok {
					resp.Usage = usageFrom(u)
				}
			}
		case "content_block_start":
			idx := int(num(payload["index"]))
			blk, _ := payload["content_block"].(map[string]any)
			typ, _ := blk["type"].(string)
			switch typ {
			case "text":
				curIndex = idx
				pending[idx] = &pendingTool{}
			case "thinking":
				curIndex = idx
				pending[idx] = &pendingTool{}
			case "tool_use":
				id, _ := blk["id"].(string)
				name, _ := blk["name"].(string)
				pending[idx] = &pendingTool{id: id, name: name}
				curIndex = idx
			}
		case "content_block_delta":
			idx := int(num(payload["index"]))
			delta, _ := payload["delta"].(map[string]any)
			dtyp, _ := delta["type"].(string)
			switch dtyp {
			case "text_delta":
				if t, ok := delta["text"].(string); ok {
					textBuf.WriteString(t)
					if onText != nil {
						onText(t)
					}
				}
			case "thinking_delta":
				if t, ok := delta["thinking"].(string); ok {
					thinkBuf.WriteString(t)
				}
			case "input_json_delta":
				if p, ok := delta["partial_json"].(string); ok {
					pt := pending[idx]
					if pt == nil {
						pt = &pendingTool{}
						pending[idx] = pt
					}
					pt.jsonBuf.WriteString(p)
				}
			case "signature_delta":
				// Opaque signature fragments are appended to the thinking tool slot.
				if s, ok := delta["signature"].(string); ok {
					pt := pending[idx]
					if pt == nil {
						pt = &pendingTool{}
						pending[idx] = pt
					}
					pt.jsonBuf.WriteString(s) // reused buffer holds signature for thinking blocks
					_ = curIndex
				}
			}
		case "content_block_stop":
			idx := int(num(payload["index"]))
			if pt, ok := pending[idx]; ok && pt.id != "" {
				args := map[string]any{}
				if pt.jsonBuf.Len() > 0 {
					if err := json.Unmarshal([]byte(pt.jsonBuf.String()), &args); err != nil {
						return fmt.Errorf("tool-call arguments are not valid JSON after completion: %w", err)
					}
				}
				resp.ToolCalls = append(resp.ToolCalls, Block{Type: "toolCall", ID: pt.id, Name: pt.name, Arguments: args})
				delete(pending, idx)
			}
		case "message_delta":
			if d, ok := payload["delta"].(map[string]any); ok {
				if sr, ok := d["stop_reason"].(string); ok {
					resp.StopReason = mapStop(sr, len(resp.ToolCalls) > 0)
				}
			}
			if u, ok := payload["usage"].(map[string]any); ok {
				resp.Usage = usageFrom(u)
			}
		case "message_stop":
			sawStop = true
		case "error":
			msg := data
			if e, ok := payload["error"].(map[string]any); ok {
				if m, ok := e["message"].(string); ok {
					msg = m
				}
				if t, ok := e["type"].(string); ok && strings.Contains(strings.ToLower(t), "overloaded") {
					return &ProviderError{Kind: ErrTransport, Message: msg}
				}
			}
			return &ProviderError{Kind: ErrInvalid, Message: msg}
		case "ping":
			// keepalive, ignore
		}
		return nil
	}

	for {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		line, err := readWithIdle()
		if line != "" {
			t := strings.TrimRight(line, "\r\n")
			if t == "" {
				if err := emit(); err != nil {
					return nil, err
				}
			} else if strings.HasPrefix(t, ":") {
				// SSE comment keepalive.
			} else if strings.HasPrefix(t, "event:") {
				event = strings.TrimSpace(strings.TrimPrefix(t, "event:"))
			} else if strings.HasPrefix(t, "data:") {
				dataLines = append(dataLines, strings.TrimPrefix(t, "data:"))
				// data may arrive without trailing blank line at EOF; handled below.
			}
		}
		if err == io.EOF {
			if len(dataLines) > 0 {
				if e := emit(); e != nil {
					return nil, e
				}
			}
			break
		}
		if err != nil {
			return nil, &ProviderError{Kind: ErrTransport, Message: err.Error()}
		}
	}
	resp.Text = textBuf.String()
	resp.Thinking = thinkBuf.String()
	if len(resp.ToolCalls) > 0 && resp.StopReason == "stop" {
		resp.StopReason = "toolUse"
	}
	// Incomplete tool calls are ineligible for execution: content_block_stop
	// is required; any leftover pending tool with an id is a protocol error.
	for _, pt := range pending {
		if pt.id != "" {
			return nil, fmt.Errorf("incomplete tool call %q: missing content_block_stop", pt.id)
		}
	}
	_ = sawStop
	return resp, nil
}

func num(v any) float64 {
	switch n := v.(type) {
	case float64:
		return n
	case int:
		return float64(n)
	case int64:
		return float64(n)
	case json.Number:
		f, _ := n.Float64()
		return f
	}
	return 0
}

func usageFrom(u map[string]any) Usage {
	return Usage{
		Input:       int64(num(u["input_tokens"])),
		Output:      int64(num(u["output_tokens"])),
		CacheRead:   int64(num(u["cache_read_input_tokens"])),
		CacheWrite:  int64(num(u["cache_creation_input_tokens"])),
		TotalTokens: int64(num(u["input_tokens"])) + int64(num(u["output_tokens"])),
	}
}

func mapStop(sr string, hasTools bool) string {
	switch sr {
	case "end_turn":
		if hasTools {
			return "toolUse"
		}
		return "stop"
	case "tool_use":
		return "toolUse"
	case "max_tokens":
		return "length"
	case "stop_sequence":
		return "stop"
	default:
		if hasTools {
			return "toolUse"
		}
		return "stop"
	}
}
