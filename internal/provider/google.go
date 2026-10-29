package provider

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"time"

	"psi/internal/providerregistry"
)

// GoogleClient speaks the Google Generative AI REST streaming protocol. It is
// intentionally separate from the OpenAI and Anthropic SSE state machines.
type GoogleClient struct{ *Client }

func NewGoogleClient(baseURL, apiKey string, maxRetry int, idle time.Duration) *GoogleClient {
	base := NewClient("", apiKey, maxRetry, idle)
	base.BaseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	return &GoogleClient{Client: base}
}

func (c *GoogleClient) Send(ctx context.Context, req Request, onText func(string)) (*Response, error) {
	baseURL := c.BaseURL
	if baseURL == "" {
		baseURL = providerregistry.DefaultEndpoint(req.Model.ProviderID(), "google-generative-ai")
		if baseURL == "" {
			return nil, fmt.Errorf("model endpoint is required for google-generative-ai")
		}
	}
	if strings.TrimSpace(c.APIKey) == "" && !hasGoogleAPIKeyHeader(c.Headers) {
		return nil, fmt.Errorf("No API key for provider: %s", req.Model.ProviderID())
	}
	if len(req.Messages) == 0 {
		return nil, fmt.Errorf("Google Generative AI request requires conversation content")
	}
	body, err := BuildGoogleRequest(req)
	if err != nil {
		return nil, err
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("cannot encode Google Generative AI request")
	}
	return sendWithRetryObserved(ctx, c.MaxRetry, c.RetryObserver, func() (*Response, error) {
		return c.sendGoogle(ctx, baseURL, raw, req, onText)
	})
}

func hasGoogleAPIKeyHeader(headers map[string]string) bool {
	for name, value := range headers {
		if strings.EqualFold(name, "x-goog-api-key") && strings.TrimSpace(value) != "" {
			return true
		}
	}
	return false
}

func (c *GoogleClient) sendGoogle(ctx context.Context, baseURL string, body []byte, req Request, onText func(string)) (*Response, error) {
	endpoint, err := googleStreamEndpoint(baseURL, req.Model.ID)
	if err != nil {
		return nil, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("invalid Google Generative AI endpoint")
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "*/*")
	if c.APIKey != "" {
		httpReq.Header.Set("x-goog-api-key", c.APIKey)
	}
	if req.Model.ProviderID() == "opencode-go" && c.SessionID != "" && !hasHeader(c.Headers, "x-opencode-session") {
		httpReq.Header.Set("x-opencode-session", c.SessionID)
	}
	for name, value := range c.Headers {
		httpReq.Header.Set(name, value)
	}
	resp, err := c.HTTP.Do(httpReq)
	if err != nil {
		return nil, &ProviderError{Kind: ErrTransport, Message: "HTTP request failed"}
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		reader := newStreamReader(ctx, resp.Body, c.IdleTimeout)
		defer reader.close()
		resp.Body = structReadCloser{Reader: reader, Closer: resp.Body}
		return nil, classifyHTTPError(resp)
	}
	result, err := parseGoogleSSE(resp.Body, c.IdleTimeout, ctx, onText, req)
	if err != nil {
		message := c.redact(err.Error())
		wrapped := fmt.Errorf("Google Generative AI stream interrupted (not retried): %s", message)
		if result != nil {
			return result, &StreamInterrupt{Partial: result, Err: wrapped}
		}
		return nil, wrapped
	}
	return result, nil
}

func googleStreamEndpoint(baseURL, modelID string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil || (parsed.Scheme != "https" && parsed.Scheme != "http") || parsed.Host == "" ||
		parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.Opaque != "" {
		return "", fmt.Errorf("invalid Google Generative AI endpoint")
	}
	if strings.ContainsAny(modelID, "/\\?#") || strings.TrimSpace(modelID) == "" || modelID == "." || modelID == ".." {
		return "", fmt.Errorf("invalid Google Generative AI model ID")
	}
	return strings.TrimRight(strings.TrimSpace(baseURL), "/") + "/models/" + url.PathEscape(modelID) + ":streamGenerateContent?alt=sse", nil
}

type googleSSEChunk struct {
	ResponseID string          `json:"responseId"`
	Error      json.RawMessage `json:"error"`
	Candidates []struct {
		Content struct {
			Parts []json.RawMessage `json:"parts"`
		} `json:"content"`
		FinishReason json.RawMessage `json:"finishReason"`
	} `json:"candidates"`
	UsageMetadata *struct {
		PromptTokenCount        *int64 `json:"promptTokenCount"`
		CandidatesTokenCount    *int64 `json:"candidatesTokenCount"`
		CachedContentTokenCount *int64 `json:"cachedContentTokenCount"`
		ThoughtsTokenCount      *int64 `json:"thoughtsTokenCount"`
		TotalTokenCount         *int64 `json:"totalTokenCount"`
	} `json:"usageMetadata"`
}

func parseGoogleSSE(r io.Reader, idle time.Duration, ctx context.Context, onText func(string), req Request) (*Response, error) {
	reader := newStreamReader(ctx, r, idle)
	defer reader.close()
	scanner := bufio.NewScanner(io.LimitReader(reader, maxStreamBytes+1))
	scanner.Buffer(make([]byte, 64*1024), maxStreamBytes+1)
	response := &Response{Model: req.Model.ID}
	var data []string
	totalBytes := 0
	finishSeen := false
	finishRaw := ""
	toolIDs := make(map[string]bool)
	var generatedID uint64

	partial := func() *Response {
		copy := *response
		copy.Content = nil
		copy.ToolCalls = nil
		var text, thinking strings.Builder
		for _, block := range response.Content {
			if block.Type == "toolCall" {
				continue
			}
			copy.Content = append(copy.Content, block)
			if block.Type == "text" {
				text.WriteString(block.Text)
			} else if block.Type == "thinking" {
				thinking.WriteString(block.Thinking)
			}
		}
		copy.Text, copy.Thinking = text.String(), thinking.String()
		return &copy
	}

	consume := func() error {
		if len(data) == 0 {
			return nil
		}
		encoded := strings.Join(data, "\n")
		data = nil
		if strings.TrimSpace(encoded) == "[DONE]" {
			return fmt.Errorf("unexpected Google Generative AI [DONE] marker")
		}
		if !json.Valid([]byte(encoded)) {
			return fmt.Errorf("malformed Google Generative AI SSE JSON")
		}
		decoder := json.NewDecoder(strings.NewReader(encoded))
		decoder.UseNumber()
		var chunk googleSSEChunk
		if err := decoder.Decode(&chunk); err != nil {
			return fmt.Errorf("malformed Google Generative AI SSE JSON")
		}
		if len(chunk.Error) > 0 && string(chunk.Error) != "null" {
			return fmt.Errorf("Google Generative AI stream reported an error")
		}
		if response.ResponseID == "" && chunk.ResponseID != "" {
			response.ResponseID = chunk.ResponseID
		}
		if chunk.UsageMetadata != nil {
			usage, err := googleUsage(*chunk.UsageMetadata)
			if err != nil {
				return err
			}
			response.Usage = usage
		}
		if len(chunk.Candidates) == 0 {
			return nil
		}
		candidate := chunk.Candidates[0]
		finishedBefore := finishSeen
		if len(candidate.FinishReason) > 0 && string(candidate.FinishReason) != "null" {
			var reason string
			if json.Unmarshal(candidate.FinishReason, &reason) != nil || reason == "" {
				return fmt.Errorf("invalid Google Generative AI finish reason")
			}
			if finishSeen && finishRaw != reason {
				return fmt.Errorf("Google Generative AI finish reason changed during stream")
			}
			finishSeen, finishRaw = true, reason
			response.RawStopReason = reason
			response.StopReason = googleStopReason(reason)
		}
		if finishedBefore && len(candidate.Content.Parts) > 0 {
			return fmt.Errorf("Google Generative AI content followed finish reason")
		}
		for _, rawPart := range candidate.Content.Parts {
			var part map[string]json.RawMessage
			if json.Unmarshal(rawPart, &part) != nil || part == nil {
				return fmt.Errorf("malformed Google Generative AI response part")
			}
			for key, value := range part {
				if key == "text" || key == "thought" || key == "thoughtSignature" || key == "functionCall" ||
					(len(value) == 0 || string(value) == "null") {
					continue
				}
				return fmt.Errorf("unsupported Google Generative AI response part %q", key)
			}
			var text string
			if rawText, ok := part["text"]; ok && string(rawText) != "null" {
				if json.Unmarshal(rawText, &text) != nil {
					return fmt.Errorf("invalid Google Generative AI text part")
				}
				thought := false
				if rawThought, ok := part["thought"]; ok && string(rawThought) != "null" {
					if json.Unmarshal(rawThought, &thought) != nil {
						return fmt.Errorf("invalid Google Generative AI thought marker")
					}
				}
				var signature string
				if rawSignature, ok := part["thoughtSignature"]; ok && string(rawSignature) != "null" {
					if json.Unmarshal(rawSignature, &signature) != nil {
						return fmt.Errorf("invalid Google Generative AI thought signature")
					}
				}
				if thought {
					appendGoogleText(response, "thinking", text, signature)
				} else {
					appendGoogleText(response, "text", text, signature)
					if text != "" && onText != nil {
						onText(text)
					}
				}
			}
			if rawCall, ok := part["functionCall"]; ok && string(rawCall) != "null" {
				var call struct {
					ID   string          `json:"id"`
					Name string          `json:"name"`
					Args json.RawMessage `json:"args"`
				}
				if json.Unmarshal(rawCall, &call) != nil || call.Name == "" {
					return fmt.Errorf("invalid Google Generative AI function call")
				}
				id := call.ID
				if id == "" || toolIDs[id] {
					for {
						generatedID = googleToolCallCounter.Add(1)
						id = fmt.Sprintf("%s_%d_%d", call.Name, time.Now().UnixMilli(), generatedID)
						if !toolIDs[id] {
							break
						}
					}
				}
				toolIDs[id] = true
				arguments := map[string]any{}
				if len(call.Args) > 0 && string(call.Args) != "null" {
					argDecoder := json.NewDecoder(bytes.NewReader(call.Args))
					argDecoder.UseNumber()
					if err := argDecoder.Decode(&arguments); err != nil || arguments == nil {
						return fmt.Errorf("invalid Google Generative AI function arguments")
					}
					var trailing any
					if argDecoder.Decode(&trailing) != io.EOF {
						return fmt.Errorf("invalid Google Generative AI function arguments")
					}
				}
				var signature string
				if rawSignature, ok := part["thoughtSignature"]; ok && string(rawSignature) != "null" {
					if json.Unmarshal(rawSignature, &signature) != nil {
						return fmt.Errorf("invalid Google Generative AI thought signature")
					}
				}
				block := Block{Type: "toolCall", ID: id, Name: call.Name, Arguments: arguments, ThoughtSignature: signature}
				response.Content = append(response.Content, block)
				response.ToolCalls = append(response.ToolCalls, block)
			}
		}
		return nil
	}

	for scanner.Scan() {
		line := strings.TrimSuffix(scanner.Text(), "\r")
		totalBytes += len(line) + 1
		if totalBytes > maxStreamBytes {
			return partial(), fmt.Errorf("Google Generative AI stream exceeds 32 MiB")
		}
		if line == "" {
			if err := consume(); err != nil {
				return partial(), err
			}
		} else if strings.HasPrefix(line, "data:") {
			data = append(data, strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
		}
	}
	if err := scanner.Err(); err != nil {
		return partial(), err
	}
	if totalBytes > maxStreamBytes {
		return partial(), fmt.Errorf("Google Generative AI stream exceeds 32 MiB")
	}
	if err := consume(); err != nil {
		return partial(), err
	}
	if err := ctx.Err(); err != nil {
		return partial(), err
	}
	if !finishSeen {
		return partial(), fmt.Errorf("Google Generative AI stream ended without a finish reason")
	}
	if response.StopReason == "error" {
		return partial(), fmt.Errorf("Provider stopped with: %s", response.RawStopReason)
	}
	if response.StopReason == "stop" && len(response.ToolCalls) > 0 {
		response.StopReason = "toolUse"
	}
	var text, thinking strings.Builder
	for _, block := range response.Content {
		if block.Type == "text" {
			text.WriteString(block.Text)
		} else if block.Type == "thinking" {
			thinking.WriteString(block.Thinking)
		}
	}
	response.Text, response.Thinking = text.String(), thinking.String()
	return response, nil
}

func appendGoogleText(response *Response, kind, delta, signature string) {
	if len(response.Content) > 0 && response.Content[len(response.Content)-1].Type == kind {
		block := &response.Content[len(response.Content)-1]
		if kind == "text" {
			block.Text += delta
			if signature != "" {
				block.TextSignature = signature
			}
		} else {
			block.Thinking += delta
			if signature != "" {
				block.ThinkingSignature = signature
			}
		}
		return
	}
	block := Block{Type: kind}
	if kind == "text" {
		block.Text, block.TextSignature = delta, signature
	} else {
		block.Thinking, block.ThinkingSignature = delta, signature
	}
	response.Content = append(response.Content, block)
}

func googleStopReason(reason string) string {
	switch reason {
	case "STOP":
		return "stop"
	case "MAX_TOKENS":
		return "length"
	default:
		return "error"
	}
}

func googleUsage(raw struct {
	PromptTokenCount        *int64 `json:"promptTokenCount"`
	CandidatesTokenCount    *int64 `json:"candidatesTokenCount"`
	CachedContentTokenCount *int64 `json:"cachedContentTokenCount"`
	ThoughtsTokenCount      *int64 `json:"thoughtsTokenCount"`
	TotalTokenCount         *int64 `json:"totalTokenCount"`
}) (Usage, error) {
	readValue := func(value *int64) int64 {
		if value == nil {
			return 0
		}
		return *value
	}
	prompt, completion := readValue(raw.PromptTokenCount), readValue(raw.CandidatesTokenCount)
	cached, thoughts, total := readValue(raw.CachedContentTokenCount), readValue(raw.ThoughtsTokenCount), readValue(raw.TotalTokenCount)
	if prompt < 0 || completion < 0 || cached < 0 || thoughts < 0 || total < 0 || cached > prompt {
		return Usage{}, fmt.Errorf("invalid Google Generative AI usage metadata")
	}
	if completion > int64(^uint64(0)>>1)-thoughts {
		return Usage{}, fmt.Errorf("invalid Google Generative AI usage metadata")
	}
	return Usage{Input: prompt - cached, Output: completion + thoughts, CacheRead: cached, TotalTokens: total, Reasoning: &thoughts}, nil
}

var googleToolCallCounter atomic.Uint64
