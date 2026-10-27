package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// CodexClient uses HTTP/SSE and full-history replay. It intentionally does not
// need websocket continuation, Node, or a running Pi process.
type CodexClient struct {
	*Client
	Auth      *CodexAuth
	SessionID string
}

func NewCodexClient(baseURL, authPath string, maxRetry int, idle time.Duration) *CodexClient {
	if strings.TrimSpace(baseURL) == "" {
		baseURL = "https://chatgpt.com/backend-api"
	}
	base := NewClient("", "", maxRetry, idle)
	base.BaseURL = strings.TrimRight(baseURL, "/")
	return &CodexClient{Client: base, Auth: &CodexAuth{Path: authPath, HTTP: base.HTTP}}
}

func (c *CodexClient) Send(ctx context.Context, req Request, onText func(string)) (*Response, error) {
	body, err := BuildCodexRequest(req)
	if err != nil {
		return nil, err
	}
	if c.SessionID != "" {
		body["prompt_cache_key"] = c.SessionID
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("cannot encode Codex request")
	}
	return sendWithRetry(ctx, c.MaxRetry, func() (*Response, error) {
		token, account, err := c.Auth.Get(ctx)
		if err != nil {
			return nil, err
		}
		return c.sendCodex(ctx, raw, token, account, req, onText)
	})
}

func (c *CodexClient) sendCodex(ctx context.Context, raw []byte, token, account string, req Request, onText func(string)) (*Response, error) {
	endpoint := c.BaseURL
	if !strings.HasSuffix(endpoint, "/codex/responses") {
		if strings.HasSuffix(endpoint, "/codex") {
			endpoint += "/responses"
		} else {
			endpoint += "/codex/responses"
		}
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("invalid Codex endpoint")
	}
	for name, value := range c.Headers {
		httpReq.Header.Set(name, value)
	}
	httpReq.Header.Set("Authorization", "Bearer "+token)
	httpReq.Header.Set("chatgpt-account-id", account)
	httpReq.Header.Set("originator", "pi")
	httpReq.Header.Set("OpenAI-Beta", "responses=experimental")
	httpReq.Header.Set("Accept", "text/event-stream")
	httpReq.Header.Set("Content-Type", "application/json")
	if c.SessionID != "" {
		httpReq.Header.Set("session-id", c.SessionID)
		httpReq.Header.Set("x-client-request-id", c.SessionID)
	}
	resp, err := c.HTTP.Do(httpReq)
	if err != nil {
		return nil, &ProviderError{Kind: ErrTransport, Message: "Codex HTTP request failed"}
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		r := newStreamReader(ctx, resp.Body, c.IdleTimeout)
		defer r.close()
		resp.Body = structReadCloser{Reader: r, Closer: resp.Body}
		return nil, classifyHTTPError(resp)
	}
	result, err := parseCodexSSE(resp.Body, c.IdleTimeout, ctx, onText, req)
	if err != nil {
		message := c.redact(strings.ReplaceAll(err.Error(), token, "[redacted]"))
		serr := fmt.Errorf("Codex stream interrupted (not retried): %s", message)
		if result != nil {
			return result, &StreamInterrupt{Partial: result, Err: serr}
		}
		return nil, serr
	}
	return result, nil
}
