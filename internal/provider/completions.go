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

// CompletionsClient uses the OpenAI Chat Completions wire protocol. It shares
// the bounded transport, retry policy, and cancellation reader, but has its own
// request path and stream settlement rules.
type CompletionsClient struct{ *Client }

func NewCompletionsClient(baseURL, apiKey string, maxRetry int, idle time.Duration) *CompletionsClient {
	base := NewClient("", apiKey, maxRetry, idle)
	base.BaseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	return &CompletionsClient{Client: base}
}

func (c *CompletionsClient) Send(ctx context.Context, req Request, onText func(string)) (*Response, error) {
	if c.BaseURL == "" {
		return nil, fmt.Errorf("model endpoint is required for openai-completions")
	}
	if strings.TrimSpace(c.APIKey) == "" && !hasNonemptyAuthHeader(c.Headers) {
		return nil, fmt.Errorf("No API key for provider: %s", req.Model.ProviderID())
	}
	body, err := BuildCompletionsRequest(req)
	if err != nil {
		return nil, err
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("cannot encode OpenAI Completions request")
	}
	return sendWithRetryObserved(ctx, c.MaxRetry, c.RetryObserver, func() (*Response, error) {
		return c.sendCompletions(ctx, raw, req, onText)
	})
}

func (c *CompletionsClient) sendCompletions(ctx context.Context, raw []byte, req Request, onText func(string)) (*Response, error) {
	endpoint := strings.TrimRight(c.BaseURL, "/") + "/chat/completions"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("invalid OpenAI Completions endpoint")
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "text/event-stream")
	httpReq.Header.Set("Authorization", "Bearer "+c.APIKey)
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
	result, err := parseCompletionsSSE(resp.Body, c.IdleTimeout, ctx, onText, req)
	if err != nil {
		message := c.redact(err.Error())
		wrapped := fmt.Errorf("OpenAI Completions stream interrupted (not retried): %s", message)
		if result != nil {
			return result, &StreamInterrupt{Partial: result, Err: wrapped}
		}
		return nil, wrapped
	}
	return result, nil
}

func hasNonemptyAuthHeader(headers map[string]string) bool {
	for name, value := range headers {
		if (strings.EqualFold(name, "authorization") || strings.EqualFold(name, "cf-aig-authorization")) && strings.TrimSpace(value) != "" {
			return true
		}
	}
	return false
}

type completionToolState struct {
	index         int
	explicitIndex bool
	id            string
	name          string
	args          strings.Builder
}

type completionStreamPart struct {
	kind  string
	index int
	tool  *completionToolState
}

func parseCompletionsSSE(r io.Reader, idle time.Duration, ctx context.Context, onText func(string), req Request) (*Response, error) {
	sr := newStreamReader(ctx, r, idle)
	defer sr.close()
	scanner := bufio.NewScanner(io.LimitReader(sr, maxStreamBytes+1))
	scanner.Buffer(make([]byte, 64*1024), maxStreamBytes+1)
	response := &Response{Model: req.Model.ID, ThinkingLevel: req.Thinking}
	var text, thinking strings.Builder
	var parts []completionStreamPart
	textStarted, thinkingStarted := false, false
	thinkingField := ""
	tools := map[int]*completionToolState{}
	toolIDs := map[string]*completionToolState{}
	nextToolIndex := 0
	var data []string
	total := 0
	doneMarker := false
	finishSeen := false
	toolArgumentBytes := 0
	compat, compatErr := resolveCompletionsCompat(req.Model)
	if compatErr != nil {
		return response, compatErr
	}

	partial := func() *Response {
		copy := *response
		copy.Content = nil
		var outText, outThinking strings.Builder
		for _, part := range parts {
			switch part.kind {
			case "text":
				if text.Len() > 0 {
					copy.Content = append(copy.Content, Block{Type: "text", Text: text.String()})
					outText.WriteString(text.String())
				}
			case "thinking":
				if thinking.Len() > 0 {
					copy.Content = append(copy.Content, Block{Type: "thinking", Thinking: thinking.String(), ThinkingSignature: thinkingField})
					outThinking.WriteString(thinking.String())
				}
			}
		}
		copy.Text, copy.Thinking = outText.String(), outThinking.String()
		copy.ToolCalls = nil
		return &copy
	}

	consume := func() error {
		if len(data) == 0 {
			return nil
		}
		encoded := strings.Join(data, "\n")
		data = nil
		if doneMarker {
			return fmt.Errorf("OpenAI Completions data followed [DONE]")
		}
		if strings.TrimSpace(encoded) == "[DONE]" {
			doneMarker = true
			return nil
		}
		var chunk struct {
			ID      string `json:"id"`
			Model   string `json:"model"`
			Choices []struct {
				Index        int                        `json:"index"`
				Delta        map[string]json.RawMessage `json:"delta"`
				FinishReason json.RawMessage            `json:"finish_reason"`
			} `json:"choices"`
			Usage json.RawMessage `json:"usage"`
		}
		decoder := json.NewDecoder(strings.NewReader(encoded))
		decoder.UseNumber()
		if !json.Valid([]byte(encoded)) || decoder.Decode(&chunk) != nil {
			return fmt.Errorf("malformed OpenAI Completions SSE JSON")
		}
		if chunk.ID != "" && response.ResponseID == "" {
			response.ResponseID = chunk.ID
		}
		if chunk.Model != "" && chunk.Model != req.Model.ID && response.Model == req.Model.ID {
			response.Model = chunk.Model
		}
		if len(chunk.Usage) > 0 && string(chunk.Usage) != "null" {
			usage, err := parseCompletionsUsage(chunk.Usage)
			if err != nil {
				return err
			}
			response.Usage = usage
		}
		if len(chunk.Choices) == 0 {
			return nil
		}
		choice := chunk.Choices[0]
		wasFinished := finishSeen
		if len(choice.FinishReason) > 0 && string(choice.FinishReason) != "null" {
			var reason string
			if json.Unmarshal(choice.FinishReason, &reason) != nil || reason == "" {
				return fmt.Errorf("invalid OpenAI Completions finish reason")
			}
			stop, err := mapCompletionsStopReason(reason)
			if err != nil {
				return err
			}
			response.StopReason = stop
			finishSeen = true
		}
		if wasFinished && len(choice.Delta) > 0 {
			return fmt.Errorf("OpenAI Completions content followed finish reason")
		}
		if choice.Delta == nil {
			return nil
		}
		for _, field := range []string{"reasoning_content", "reasoning", "reasoning_text"} {
			raw, ok := choice.Delta[field]
			if !ok || len(raw) == 0 || string(raw) == "null" {
				continue
			}
			var delta string
			if json.Unmarshal(raw, &delta) != nil {
				return fmt.Errorf("invalid OpenAI Completions reasoning delta")
			}
			if delta == "" {
				continue
			}
			if !thinkingStarted {
				thinkingStarted = true
				thinkingField = field
				if req.Model.ProviderID() == "opencode-go" && field == "reasoning" {
					thinkingField = "reasoning_content"
				}
				parts = append(parts, completionStreamPart{kind: "thinking"})
			}
			thinking.WriteString(delta)
			break
		}
		if raw, ok := choice.Delta["content"]; ok && len(raw) > 0 && string(raw) != "null" {
			var delta string
			if json.Unmarshal(raw, &delta) != nil {
				return fmt.Errorf("unsupported OpenAI Completions content delta")
			}
			if delta != "" {
				if !textStarted {
					textStarted = true
					parts = append(parts, completionStreamPart{kind: "text"})
				}
				text.WriteString(delta)
				if onText != nil {
					onText(delta)
				}
			}
		}
		if raw, ok := choice.Delta["tool_calls"]; ok && len(raw) > 0 && string(raw) != "null" {
			var deltas []struct {
				Index    json.RawMessage `json:"index"`
				ID       string          `json:"id"`
				Type     string          `json:"type"`
				Function struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				} `json:"function"`
			}
			if json.Unmarshal(raw, &deltas) != nil {
				return fmt.Errorf("malformed OpenAI Completions tool delta")
			}
			for _, delta := range deltas {
				index, hasIndex := 0, len(delta.Index) > 0 && string(delta.Index) != "null"
				if hasIndex && (json.Unmarshal(delta.Index, &index) != nil || index < 0 || index >= 4096) {
					return fmt.Errorf("invalid OpenAI Completions tool index")
				}
				if delta.Type != "" && delta.Type != "function" {
					return fmt.Errorf("unsupported OpenAI Completions tool type")
				}
				state := (*completionToolState)(nil)
				if hasIndex {
					state = tools[index]
				}
				if delta.ID != "" {
					byID := toolIDs[delta.ID]
					if state != nil && byID != nil && state != byID {
						return fmt.Errorf("OpenAI Completions tool index and ID disagree")
					}
					if state == nil {
						state = byID
					}
				}
				if state == nil {
					if len(tools) >= 4096 || (!hasIndex && delta.ID == "") {
						return fmt.Errorf("OpenAI Completions tool delta lacks a usable index or ID")
					}
					if !hasIndex {
						for tools[nextToolIndex] != nil {
							nextToolIndex++
						}
						index = nextToolIndex
						nextToolIndex++
					}
					state = &completionToolState{index: index, explicitIndex: hasIndex}
					tools[index] = state
					parts = append(parts, completionStreamPart{kind: "tool", index: index, tool: state})
				} else if hasIndex {
					if state.explicitIndex && state.index != index {
						return fmt.Errorf("OpenAI Completions tool index changed during stream")
					}
					if !state.explicitIndex {
						if prior := tools[index]; prior != nil && prior != state {
							return fmt.Errorf("duplicate OpenAI Completions tool index")
						}
						delete(tools, state.index)
						state.index, state.explicitIndex = index, true
						tools[index] = state
					}
				}
				if delta.ID != "" {
					if state.id != "" && state.id != delta.ID {
						return fmt.Errorf("OpenAI Completions tool ID changed during stream")
					}
					if prior := toolIDs[delta.ID]; prior != nil && prior != state {
						return fmt.Errorf("duplicate OpenAI Completions tool ID")
					}
					state.id = delta.ID
					toolIDs[delta.ID] = state
				}
				if delta.Function.Name != "" {
					if state.name != "" && state.name != delta.Function.Name {
						return fmt.Errorf("OpenAI Completions tool name changed during stream")
					}
					state.name = delta.Function.Name
				}
				if delta.Function.Arguments != "" {
					toolArgumentBytes += len(delta.Function.Arguments)
					if toolArgumentBytes > maxStreamBytes {
						return fmt.Errorf("OpenAI Completions tool arguments exceed stream limit")
					}
					state.args.WriteString(delta.Function.Arguments)
				}
			}
		}
		return nil
	}

	for scanner.Scan() {
		line := scanner.Text()
		total += len(line) + 1
		if total > maxStreamBytes {
			return partial(), fmt.Errorf("OpenAI Completions stream exceeds 32 MiB")
		}
		if line == "" {
			if err := consume(); err != nil {
				return partial(), err
			}
			if doneMarker {
				break
			}
		} else if strings.HasPrefix(line, "data:") {
			data = append(data, strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
		}
	}
	if err := scanner.Err(); err != nil {
		return partial(), err
	}
	if total > maxStreamBytes {
		return partial(), fmt.Errorf("OpenAI Completions stream exceeds 32 MiB")
	}
	if err := consume(); err != nil {
		return partial(), err
	}
	if err := ctx.Err(); err != nil {
		return partial(), err
	}
	if !finishSeen {
		if compat.supportsFinish {
			return partial(), fmt.Errorf("OpenAI Completions stream ended without finish_reason")
		}
		if len(tools) > 0 {
			response.StopReason = "toolUse"
		} else {
			response.StopReason = "stop"
		}
	}
	if response.StopReason == "error" {
		return partial(), fmt.Errorf("OpenAI Completions provider returned an error finish reason")
	}
	var outText, outThinking strings.Builder
	for _, part := range parts {
		switch part.kind {
		case "text":
			value := text.String()
			response.Content = append(response.Content, Block{Type: "text", Text: value})
			outText.WriteString(value)
		case "thinking":
			value := thinking.String()
			response.Content = append(response.Content, Block{Type: "thinking", Thinking: value, ThinkingSignature: thinkingField})
			outThinking.WriteString(value)
		case "tool":
			state := part.tool
			if state == nil || state.id == "" || state.name == "" {
				return partial(), fmt.Errorf("incomplete OpenAI Completions tool call")
			}
			args := state.args.String()
			if args == "" {
				args = "{}"
			}
			var arguments map[string]any
			decoder := json.NewDecoder(strings.NewReader(args))
			decoder.UseNumber()
			if decoder.Decode(&arguments) != nil || arguments == nil {
				return partial(), fmt.Errorf("invalid completed OpenAI Completions tool arguments")
			}
			var extra any
			if decoder.Decode(&extra) != io.EOF {
				return partial(), fmt.Errorf("invalid completed OpenAI Completions tool arguments")
			}
			block := Block{Type: "toolCall", ID: state.id, Name: state.name, Arguments: arguments}
			response.Content = append(response.Content, block)
			response.ToolCalls = append(response.ToolCalls, block)
		}
	}
	response.Text, response.Thinking = outText.String(), outThinking.String()
	return response, nil
}

func mapCompletionsStopReason(reason string) (string, error) {
	switch reason {
	case "stop", "end":
		return "stop", nil
	case "length":
		return "length", nil
	case "function_call", "tool_calls":
		return "toolUse", nil
	case "content_filter", "network_error":
		return "error", nil
	default:
		return "error", fmt.Errorf("unknown OpenAI Completions finish reason")
	}
}

func parseCompletionsUsage(raw json.RawMessage) (Usage, error) {
	var usage struct {
		PromptTokens         int64  `json:"prompt_tokens"`
		CompletionTokens     int64  `json:"completion_tokens"`
		PromptCacheHitTokens *int64 `json:"prompt_cache_hit_tokens"`
		CachedTokens         *int64 `json:"cached_tokens"`
		PromptTokensDetails  struct {
			CachedTokens     *int64 `json:"cached_tokens"`
			CacheWriteTokens *int64 `json:"cache_write_tokens"`
		} `json:"prompt_tokens_details"`
		CompletionTokensDetails struct {
			ReasoningTokens *int64 `json:"reasoning_tokens"`
		} `json:"completion_tokens_details"`
	}
	if json.Unmarshal(raw, &usage) != nil {
		return Usage{}, fmt.Errorf("malformed OpenAI Completions usage")
	}
	read := int64(0)
	switch {
	case usage.PromptTokensDetails.CachedTokens != nil:
		read = *usage.PromptTokensDetails.CachedTokens
	case usage.PromptCacheHitTokens != nil:
		read = *usage.PromptCacheHitTokens
	case usage.CachedTokens != nil:
		read = *usage.CachedTokens
	}
	written := int64(0)
	if usage.PromptTokensDetails.CacheWriteTokens != nil {
		written = *usage.PromptTokensDetails.CacheWriteTokens
	}
	input := max(int64(0), usage.PromptTokens-read-written)
	result := Usage{Input: input, Output: usage.CompletionTokens, CacheRead: read, CacheWrite: written,
		TotalTokens: input + usage.CompletionTokens + read + written, Reasoning: usage.CompletionTokensDetails.ReasoningTokens}
	return result, nil
}
