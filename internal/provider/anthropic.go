package provider

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

type Client struct {
	HTTP        *http.Client
	BaseURL     string
	APIKey      string
	Headers     map[string]string
	MaxRetry    int
	IdleTimeout time.Duration
}

func NewClient(baseURL, apiKey string, maxRetry int, idle time.Duration) *Client {
	if baseURL == "" {
		baseURL = "https://api.anthropic.com"
	}
	baseURL = strings.TrimSuffix(baseURL, "/")
	baseURL = strings.TrimSuffix(baseURL, "/v1")
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.DialContext = (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext
	transport.ResponseHeaderTimeout = 30 * time.Second
	return &Client{HTTP: &http.Client{Transport: transport}, BaseURL: baseURL, APIKey: apiKey, MaxRetry: maxRetry, IdleTimeout: idle}
}

func (c *Client) CloseIdle() { c.HTTP.CloseIdleConnections() }

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

func (c *Client) Send(ctx context.Context, req Request, onText func(string)) (*Response, error) {
	body, err := BuildAnthropicRequest(req)
	if err != nil {
		return nil, err
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	result, err := sendWithRetry(ctx, c.MaxRetry, func() (*Response, error) { return c.doOnce(ctx, raw, onText, req) })
	if err == nil && result != nil {
		result.ThinkingLevel = ""
		if req.Model.AdaptiveThinking && req.Model.MidConvoEffort {
			result.ThinkingLevel, _ = anthropicEffort(req.Model, req.Thinking)
		}
	}
	return result, err
}

func sendWithRetry(ctx context.Context, maxRetry int, send func() (*Response, error)) (*Response, error) {
	for attempt := 0; ; attempt++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		resp, err := send()
		if err == nil {
			return resp, nil
		}
		// Retain the typed partial result even when cancellation caused the
		// interruption. Returning only ctx.Err would discard durable usage.
		var se *StreamInterrupt
		if errors.As(err, &se) {
			if ctx.Err() != nil {
				return se.Partial, &StreamInterrupt{Partial: se.Partial, Err: errors.Join(ctx.Err(), se.Err)}
			}
			return se.Partial, se
		}
		if ctx.Err() != nil {
			return resp, ctx.Err()
		}
		var pe *ProviderError
		if !errors.As(err, &pe) || (pe.Kind != ErrRateLimit && pe.Kind != ErrTransport) || attempt >= maxRetry {
			return nil, err
		}
		delay := pe.RetryAfter
		if delay <= 0 {
			delay = time.Duration(1<<min(attempt, 6)) * 500 * time.Millisecond
		}
		delay = min(delay, 60*time.Second)
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}

func (c *Client) doOnce(ctx context.Context, raw []byte, onText func(string), req Request) (*Response, error) {
	httpReq, err := http.NewRequestWithContext(ctx, "POST", c.BaseURL+"/v1/messages", bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("invalid provider endpoint")
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("x-api-key", c.APIKey)
	httpReq.Header.Set("anthropic-version", "2023-06-01")
	for name, value := range c.Headers {
		httpReq.Header.Set(name, value)
	}
	resp, err := c.HTTP.Do(httpReq)
	if err != nil {
		return nil, &ProviderError{Kind: ErrTransport, Message: "HTTP request failed"}
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		// Error bodies also need idle/cancellation handling.
		r := newStreamReader(ctx, resp.Body, c.IdleTimeout)
		defer r.close()
		resp.Body = structReadCloser{Reader: r, Closer: resp.Body}
		return nil, classifyHTTPError(resp)
	}
	result, err := parseSSE(resp.Body, c.IdleTimeout, ctx, onText, req)
	if err != nil {
		// Once HTTP streaming starts, never invisibly replay an incomplete turn.
		// Deliberately do not wrap a retryable ProviderError here.
		serr := fmt.Errorf("provider stream interrupted (not retried): %s", c.redact(err.Error()))
		if result != nil {
			return result, &StreamInterrupt{Partial: result, Err: serr}
		}
		return nil, serr
	}
	return result, nil
}

type structReadCloser struct {
	io.Reader
	io.Closer
}

func (c *Client) redact(s string) string {
	for _, secret := range append([]string{c.APIKey}, headerValues(c.Headers)...) {
		if secret != "" {
			s = strings.ReplaceAll(s, secret, "[redacted]")
		}
	}
	return s
}

func headerValues(headers map[string]string) []string {
	var values []string
	for _, v := range headers {
		values = append(values, v)
	}
	return values
}

func classifyHTTPError(resp *http.Response) *ProviderError {
	// Do not echo response bodies: gateways can echo request headers or prompts.
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	lower := strings.ToLower(string(b))
	kind := ErrInvalid
	switch {
	case resp.StatusCode == 401 || resp.StatusCode == 403:
		kind = ErrAuth
	case resp.StatusCode == 429:
		kind = ErrRateLimit
	case resp.StatusCode >= 500:
		kind = ErrTransport
	case resp.StatusCode == 400 && (strings.Contains(lower, "prompt is too long") || strings.Contains(lower, "context window") || strings.Contains(lower, "maximum context")):
		kind = ErrContextLimit
	}
	return &ProviderError{Kind: kind, Status: resp.StatusCode, Message: http.StatusText(resp.StatusCode), RetryAfter: parseRetryAfter(resp.Header.Get("retry-after"))}
}

func parseRetryAfter(s string) time.Duration {
	if secs, err := strconv.ParseFloat(s, 64); err == nil && secs >= 0 && secs <= 86400 {
		return time.Duration(secs * float64(time.Second))
	}
	if date, err := http.ParseTime(s); err == nil {
		return max(time.Until(date), 0)
	}
	return 0
}

// A single synchronous reader owns the stream. Cancellation/idle timeout closes
// the body instead of leaving a blocked goroutine behind for every line read.
type streamReader struct {
	ctx     context.Context
	r       io.Reader
	closer  io.Closer
	idle    time.Duration
	stop    func() bool
	timeout atomic.Bool
}

func newStreamReader(ctx context.Context, r io.Reader, idle time.Duration) *streamReader {
	s := &streamReader{ctx: ctx, r: r, idle: idle}
	if closer, ok := r.(io.Closer); ok {
		s.closer = closer
		s.stop = context.AfterFunc(ctx, func() { closer.Close() })
	}
	return s
}

func (s *streamReader) close() {
	if s.stop != nil {
		s.stop()
	}
}

func (s *streamReader) Read(p []byte) (int, error) {
	if err := s.ctx.Err(); err != nil {
		return 0, err
	}
	var timer *time.Timer
	if s.idle > 0 && s.closer != nil {
		timer = time.AfterFunc(s.idle, func() {
			s.timeout.Store(true)
			s.closer.Close()
		})
		defer timer.Stop()
	}
	n, err := s.r.Read(p)
	if s.ctx.Err() != nil {
		return n, s.ctx.Err()
	}
	if s.timeout.Load() {
		return n, fmt.Errorf("idle timeout reading provider stream")
	}
	return n, err
}

const maxStreamBytes = 32 * 1024 * 1024

func parseSSE(r io.Reader, idle time.Duration, ctx context.Context, onText func(string), req Request) (*Response, error) {
	sr := newStreamReader(ctx, r, idle)
	defer sr.close()
	br := bufio.NewReader(sr)
	resp := &Response{Model: req.Model.ID, ThinkingLevel: req.Thinking}
	type pendingBlock struct {
		block                     Block
		text, thinking, sig, args strings.Builder
		open                      bool
	}
	blocks := map[int]*pendingBlock{}
	toolIDs := map[string]bool{}
	var order []int
	started, stopped := false, false
	var eventName string
	var data []string
	total := 0
	emit := func() error {
		defer func() { eventName, data = "", nil }()
		if len(data) == 0 {
			return nil
		}
		var p struct {
			Type    string `json:"type"`
			Index   int    `json:"index"`
			Message struct {
				ID    string         `json:"id"`
				Model string         `json:"model"`
				Usage map[string]any `json:"usage"`
			} `json:"message"`
			ContentBlock struct {
				Type      string         `json:"type"`
				ID        string         `json:"id"`
				Name      string         `json:"name"`
				Text      string         `json:"text"`
				Thinking  string         `json:"thinking"`
				Signature string         `json:"signature"`
				Data      string         `json:"data"`
				Input     map[string]any `json:"input"`
			} `json:"content_block"`
			Delta struct {
				Type        string `json:"type"`
				Text        string `json:"text"`
				Thinking    string `json:"thinking"`
				Signature   string `json:"signature"`
				PartialJSON string `json:"partial_json"`
				StopReason  string `json:"stop_reason"`
			} `json:"delta"`
			Usage map[string]any `json:"usage"`
		}
		encoded := strings.Join(data, "\n")
		if !json.Valid([]byte(encoded)) {
			return fmt.Errorf("malformed SSE data for event %q", eventName)
		}
		dec := json.NewDecoder(strings.NewReader(encoded))
		dec.UseNumber()
		if err := dec.Decode(&p); err != nil {
			return fmt.Errorf("malformed SSE data for event %q", eventName)
		}
		if eventName == "" {
			eventName = p.Type
		}
		switch eventName {
		case "message_start":
			if started {
				return fmt.Errorf("duplicate message_start")
			}
			started = true
			resp.ResponseID = p.Message.ID
			if p.Message.Model != "" {
				resp.Model = p.Message.Model
			}
			mergeUsage(&resp.Usage, p.Message.Usage)
		case "content_block_start":
			if !started || blocks[p.Index] != nil {
				return fmt.Errorf("invalid content block start")
			}
			b := p.ContentBlock
			pb := &pendingBlock{open: true, block: Block{Type: b.Type, ID: b.ID, Name: b.Name, Arguments: b.Input}}
			// Seed amortized buffers with any start-event payload (F08).
			switch b.Type {
			case "text":
				pb.text.WriteString(b.Text)
				if b.Text != "" && onText != nil {
					onText(b.Text)
				}
			case "thinking":
				pb.thinking.WriteString(b.Thinking)
				pb.sig.WriteString(b.Signature)
			case "redacted_thinking":
				pb.block.Type, pb.block.Redacted, pb.thinking, pb.sig = "thinking", true, strings.Builder{}, strings.Builder{}
				pb.sig.WriteString(b.Data)
			case "tool_use":
				if toolIDs[b.ID] {
					return fmt.Errorf("duplicate tool call ID")
				}
				toolIDs[b.ID] = true
				if b.ID == "" || b.Name == "" {
					return fmt.Errorf("tool call missing ID or name")
				}
				pb.block.Type = "toolCall"
			default:
				return fmt.Errorf("unsupported streamed content %q", b.Type)
			}
			blocks[p.Index] = pb
			order = append(order, p.Index)
		case "content_block_delta":
			pb := blocks[p.Index]
			if pb == nil || !pb.open {
				return fmt.Errorf("delta for inactive content block")
			}
			switch p.Delta.Type {
			case "text_delta":
				if pb.block.Type != "text" {
					return fmt.Errorf("text delta in non-text block")
				}
				pb.text.WriteString(p.Delta.Text)
				if onText != nil {
					onText(p.Delta.Text)
				}
			case "thinking_delta":
				if pb.block.Type != "thinking" {
					return fmt.Errorf("thinking delta in non-thinking block")
				}
				pb.thinking.WriteString(p.Delta.Thinking)
			case "signature_delta":
				if pb.block.Type != "thinking" {
					return fmt.Errorf("signature delta in non-thinking block")
				}
				pb.sig.WriteString(p.Delta.Signature)
			case "input_json_delta":
				if pb.block.Type != "toolCall" {
					return fmt.Errorf("input delta in non-tool block")
				}
				pb.args.WriteString(p.Delta.PartialJSON)
			default:
				return fmt.Errorf("unsupported streamed delta %q", p.Delta.Type)
			}
		case "content_block_stop":
			pb := blocks[p.Index]
			if pb == nil || !pb.open {
				return fmt.Errorf("stop for inactive content block")
			}
			pb.open = false
			if pb.block.Type == "toolCall" {
				if pb.args.Len() > 0 {
					if !json.Valid([]byte(pb.args.String())) {
						return fmt.Errorf("invalid completed tool-call arguments")
					}
					dec := json.NewDecoder(strings.NewReader(pb.args.String()))
					dec.UseNumber()
					if err := dec.Decode(&pb.block.Arguments); err != nil || pb.block.Arguments == nil {
						return fmt.Errorf("invalid completed tool-call arguments")
					}
				}
				if pb.block.Arguments == nil {
					pb.block.Arguments = map[string]any{}
				}
			}
		case "message_delta":
			if p.Delta.StopReason != "" {
				resp.StopReason = mapStop(p.Delta.StopReason, false)
			}
			mergeUsage(&resp.Usage, p.Usage)
		case "message_stop":
			stopped = true
		case "error":
			return fmt.Errorf("provider returned an SSE error")
		case "ping":
		default:
			return fmt.Errorf("unsupported SSE event %q", eventName)
		}
		return nil
	}
	for !stopped {
		part, err := br.ReadSlice('\n')
		total += len(part)
		if total > maxStreamBytes {
			return resp, fmt.Errorf("provider stream exceeds 32 MiB")
		}
		// ReadSlice fragments can split a line. Accumulate only within this bound.
		line := append([]byte(nil), part...)
		for err == bufio.ErrBufferFull {
			part, err = br.ReadSlice('\n')
			total += len(part)
			if total > maxStreamBytes {
				return resp, fmt.Errorf("provider stream exceeds 32 MiB")
			}
			line = append(line, part...)
		}
		t := strings.TrimRight(string(line), "\r\n")
		switch {
		case t == "":
			if e := emit(); e != nil {
				return resp, e
			}
		case strings.HasPrefix(t, "event:"):
			eventName = strings.TrimSpace(t[6:])
		case strings.HasPrefix(t, "data:"):
			data = append(data, strings.TrimPrefix(t[5:], " "))
		}
		if err != nil {
			if err != io.EOF {
				return resp, err
			}
			if len(data) > 0 {
				if e := emit(); e != nil {
					return resp, e
				}
			}
			break
		}
	}
	if !started || !stopped || resp.StopReason == "" {
		return resp, fmt.Errorf("incomplete provider response: missing message_start, stop reason, or message_stop")
	}
	var respText, respThinking strings.Builder
	for _, idx := range order {
		pb := blocks[idx]
		if pb.open {
			return resp, fmt.Errorf("incomplete content block %d", idx)
		}
		switch pb.block.Type {
		case "text":
			pb.block.Text = pb.text.String()
			respText.WriteString(pb.block.Text)
		case "thinking":
			pb.block.Thinking = pb.thinking.String()
			pb.block.ThinkingSignature = pb.sig.String()
			respThinking.WriteString(pb.block.Thinking)
		case "toolCall":
			resp.ToolCalls = append(resp.ToolCalls, pb.block)
		}
		resp.Content = append(resp.Content, pb.block)
	}
	resp.Text, resp.Thinking = respText.String(), respThinking.String()
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

func mergeUsage(out *Usage, u map[string]any) {
	for key, dest := range map[string]*int64{"input_tokens": &out.Input, "output_tokens": &out.Output, "cache_read_input_tokens": &out.CacheRead, "cache_creation_input_tokens": &out.CacheWrite} {
		if v, ok := u[key]; ok {
			*dest = int64(num(v))
		}
	}
	if cache, ok := u["cache_creation"].(map[string]any); ok {
		if n, ok := cache["ephemeral_1h_input_tokens"]; ok {
			count := int64(num(n))
			out.CacheWrite1h = &count
		}
	}
	out.TotalTokens = out.Input + out.Output + out.CacheRead + out.CacheWrite
}

func usageFrom(u map[string]any) Usage { var out Usage; mergeUsage(&out, u); return out }

func mapStop(sr string, hasTools bool) string {
	switch sr {
	case "tool_use":
		return "toolUse"
	case "max_tokens":
		return "length"
	case "end_turn", "stop_sequence":
		return "stop"
	default:
		return "error"
	}
}
