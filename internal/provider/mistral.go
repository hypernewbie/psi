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

// MistralClient uses the native Mistral Chat Completions wire protocol
// (pi-ai's mistral-conversations API): camelCase request keys, typed content
// chunks, and nine-character tool call IDs.
type MistralClient struct{ *Client }

func NewMistralClient(baseURL, apiKey string, maxRetry int, idle time.Duration) *MistralClient {
	base := NewClient("", apiKey, maxRetry, idle)
	base.BaseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	return &MistralClient{Client: base}
}

func (c *MistralClient) Send(ctx context.Context, req Request, onText func(string)) (*Response, error) {
	if c.BaseURL == "" {
		return nil, fmt.Errorf("model endpoint is required for mistral-conversations")
	}
	if strings.TrimSpace(c.APIKey) == "" && !hasNonemptyAuthHeader(c.Headers) {
		return nil, fmt.Errorf("No API key for provider: %s", req.Model.ProviderID())
	}
	body, err := BuildMistralRequest(req)
	if err != nil {
		return nil, err
	}
	if c.SessionID != "" {
		body["promptCacheKey"] = c.SessionID
	}
	raw, err := json.Marshal(toMistralWirePayload(body))
	if err != nil {
		return nil, fmt.Errorf("cannot encode Mistral request")
	}
	return sendWithRetryObserved(ctx, c.MaxRetry, c.RetryObserver, func() (*Response, error) {
		return c.sendMistral(ctx, raw, req, onText)
	})
}

// toMistralWirePayload remaps camelCase keys to the documented snake_case
// wire format; unknown keys pass through unchanged.
func toMistralWirePayload(payload map[string]any) map[string]any {
	wire := make(map[string]any, len(payload)+4)
	for key, value := range payload {
		switch key {
		case "maxTokens":
			wire["max_tokens"] = value
		case "toolChoice":
			wire["tool_choice"] = value
		case "reasoningEffort":
			wire["reasoning_effort"] = value
		case "promptMode":
			wire["prompt_mode"] = value
		case "promptCacheKey":
			wire["prompt_cache_key"] = value
		case "messages":
			wire["messages"] = toMistralWireMessages(value.([]any))
		default:
			wire[key] = value
		}
	}
	return wire
}

func toMistralWireMessages(messages []any) []any {
	out := make([]any, 0, len(messages))
	for _, item := range messages {
		message, ok := item.(map[string]any)
		if !ok {
			out = append(out, item)
			continue
		}
		wire := make(map[string]any, len(message)+2)
		for key, value := range message {
			switch key {
			case "toolCalls":
				wire["tool_calls"] = value
			case "toolCallId":
				wire["tool_call_id"] = value
			default:
				wire[key] = value
			}
		}
		out = append(out, wire)
	}
	return out
}

func (c *MistralClient) sendMistral(ctx context.Context, raw []byte, req Request, onText func(string)) (*Response, error) {
	endpoint := strings.TrimRight(c.BaseURL, "/") + "/v1/chat/completions"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("invalid Mistral endpoint")
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "text/event-stream")
	httpReq.Header.Set("Authorization", "Bearer "+c.APIKey)
	if c.SessionID != "" && !hasHeader(c.Headers, "x-affinity") {
		httpReq.Header.Set("x-affinity", c.SessionID)
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
	result, err := parseMistralSSE(resp.Body, c.IdleTimeout, ctx, onText, req)
	if err != nil {
		message := c.redact(err.Error())
		wrapped := fmt.Errorf("Mistral stream interrupted (not retried): %s", message)
		if result != nil {
			return result, &StreamInterrupt{Partial: result, Err: wrapped}
		}
		return nil, wrapped
	}
	return result, nil
}

type mistralStreamPart struct {
	kind string
	text strings.Builder
	tool *completionToolState
}

func parseMistralSSE(r io.Reader, idle time.Duration, ctx context.Context, onText func(string), req Request) (*Response, error) {
	sr := newStreamReader(ctx, r, idle)
	defer sr.close()
	scanner := bufio.NewScanner(io.LimitReader(sr, maxStreamBytes+1))
	scanner.Buffer(make([]byte, 64*1024), maxStreamBytes+1)
	response := &Response{Model: req.Model.ID, ThinkingLevel: req.Thinking}
	var parts []*mistralStreamPart
	tools := map[string]*completionToolState{}
	toolOrder := []string{}
	finishSeen := false
	var data []string
	total := 0
	doneMarker := false

	partial := func() *Response {
		copy := *response
		copy.Content = nil
		var outText, outThinking strings.Builder
		for _, part := range parts {
			switch part.kind {
			case "text":
				if part.text.Len() > 0 {
					copy.Content = append(copy.Content, Block{Type: "text", Text: part.text.String()})
					outText.WriteString(part.text.String())
				}
			case "thinking":
				if part.text.Len() > 0 {
					value := part.text.String()
					copy.Content = append(copy.Content, Block{Type: "thinking", Thinking: value})
					outThinking.WriteString(value)
				}
			}
		}
		copy.Text = outText.String()
		copy.Thinking = outThinking.String()
		copy.ToolCalls = nil
		return &copy
	}

	appendText := func(delta string) {
		if delta == "" {
			// GLM models on Mistral send empty content deltas around thinking
			// and tool calls; opening a block for them splits thinking into
			// multiple blocks, which Mistral rejects on replay.
			return
		}
		if len(parts) == 0 || parts[len(parts)-1].kind != "text" {
			parts = append(parts, &mistralStreamPart{kind: "text"})
		}
		parts[len(parts)-1].text.WriteString(delta)
		if onText != nil {
			onText(delta)
		}
	}
	appendThinking := func(delta string) {
		if delta == "" {
			return
		}
		if len(parts) == 0 || parts[len(parts)-1].kind != "thinking" {
			parts = append(parts, &mistralStreamPart{kind: "thinking"})
		}
		parts[len(parts)-1].text.WriteString(delta)
	}

	consume := func() error {
		if len(data) == 0 {
			return nil
		}
		encoded := strings.Join(data, "\n")
		data = nil
		if doneMarker {
			return fmt.Errorf("Mistral data followed [DONE]")
		}
		if strings.TrimSpace(encoded) == "[DONE]" {
			doneMarker = true
			return nil
		}
		var chunk struct {
			ID      string `json:"id"`
			Choices []struct {
				FinishReason json.RawMessage `json:"finish_reason"`
				Delta        struct {
					Content   json.RawMessage `json:"content"`
					ToolCalls []struct {
						ID       *string `json:"id"`
						Index    *int    `json:"index"`
						Function struct {
							Name      string          `json:"name"`
							Arguments json.RawMessage `json:"arguments"`
						} `json:"function"`
					} `json:"tool_calls"`
				} `json:"delta"`
			} `json:"choices"`
			Usage json.RawMessage `json:"usage"`
		}
		decoder := json.NewDecoder(strings.NewReader(encoded))
		decoder.UseNumber()
		if !json.Valid([]byte(encoded)) || decoder.Decode(&chunk) != nil {
			return fmt.Errorf("malformed Mistral SSE JSON")
		}
		if chunk.ID != "" && response.ResponseID == "" {
			response.ResponseID = chunk.ID
		}
		if len(chunk.Usage) > 0 && string(chunk.Usage) != "null" {
			usage, err := parseMistralUsage(chunk.Usage)
			if err != nil {
				return err
			}
			response.Usage = usage
		}
		if len(chunk.Choices) == 0 {
			return nil
		}
		choice := chunk.Choices[0]
		if len(choice.FinishReason) > 0 && string(choice.FinishReason) != "null" {
			var reason string
			if json.Unmarshal(choice.FinishReason, &reason) != nil || reason == "" {
				return fmt.Errorf("invalid Mistral finish reason")
			}
			stop, message := mapMistralStopReason(reason)
			response.StopReason, response.RawStopReason = stop, reason
			if message != "" {
				return fmt.Errorf("%s", message)
			}
			finishSeen = true
		}
		if choice.Delta.Content != nil && len(choice.Delta.Content) > 0 && string(choice.Delta.Content) != "null" {
			var asString string
			if json.Unmarshal(choice.Delta.Content, &asString) == nil {
				appendText(asString)
			} else {
				var chunks []struct {
					Type     string `json:"type"`
					Text     string `json:"text"`
					Thinking []struct {
						Text string `json:"text"`
					} `json:"thinking"`
				}
				if json.Unmarshal(choice.Delta.Content, &chunks) != nil {
					return fmt.Errorf("unsupported Mistral content delta")
				}
				for _, item := range chunks {
					switch item.Type {
					case "text":
						appendText(item.Text)
					case "thinking":
						for _, part := range item.Thinking {
							appendThinking(part.Text)
						}
					case "":
						// Typed chunks always carry a type; untyped entries are ignored.
					default:
						return fmt.Errorf("unsupported Mistral content chunk %q", item.Type)
					}
				}
			}
		}
		for _, call := range choice.Delta.ToolCalls {
			callID := ""
			if call.ID != nil && *call.ID != "" && *call.ID != "null" {
				callID = *call.ID
			} else {
				index := 0
				if call.Index != nil {
					index = *call.Index
				}
				callID = deriveMistralToolCallID(fmt.Sprintf("toolcall:%d", index), 0)
			}
			key := callID
			if call.Index != nil {
				key = fmt.Sprintf("index:%d", *call.Index)
			}
			state := tools[key]
			if state == nil {
				state = &completionToolState{id: callID, index: len(toolOrder)}
				tools[key] = state
				toolOrder = append(toolOrder, key)
				parts = append(parts, &mistralStreamPart{kind: "tool", tool: state})
			}
			if call.Function.Name != "" {
				if state.name != "" && state.name != call.Function.Name {
					return fmt.Errorf("Mistral tool name changed during stream")
				}
				state.name = call.Function.Name
			}
			if len(call.Function.Arguments) > 0 && string(call.Function.Arguments) != "null" {
				var delta string
				if json.Unmarshal(call.Function.Arguments, &delta) == nil {
					state.args.WriteString(delta)
				} else {
					state.args.WriteString(string(call.Function.Arguments))
				}
			}
		}
		return nil
	}

	for scanner.Scan() {
		line := scanner.Text()
		total += len(line) + 1
		if total > maxStreamBytes {
			return partial(), fmt.Errorf("Mistral stream exceeds 32 MiB")
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
	if err := consume(); err != nil {
		return partial(), err
	}
	if err := ctx.Err(); err != nil {
		return partial(), err
	}
	if !finishSeen {
		return partial(), fmt.Errorf("Mistral stream ended without a finish reason")
	}
	if response.StopReason == "error" {
		return partial(), fmt.Errorf("Mistral provider returned an error finish reason")
	}
	var outText, outThinking strings.Builder
	for _, part := range parts {
		switch part.kind {
		case "text":
			value := part.text.String()
			response.Content = append(response.Content, Block{Type: "text", Text: value})
			outText.WriteString(value)
		case "thinking":
			value := part.text.String()
			response.Content = append(response.Content, Block{Type: "thinking", Thinking: value})
			outThinking.WriteString(value)
		case "tool":
			state := part.tool
			if state == nil || state.id == "" || state.name == "" {
				return partial(), fmt.Errorf("incomplete Mistral tool call")
			}
			args := state.args.String()
			if args == "" {
				args = "{}"
			}
			var arguments map[string]any
			decoder := json.NewDecoder(strings.NewReader(args))
			decoder.UseNumber()
			if decoder.Decode(&arguments) != nil || arguments == nil {
				return partial(), fmt.Errorf("invalid completed Mistral tool arguments")
			}
			var extra any
			if decoder.Decode(&extra) != io.EOF {
				return partial(), fmt.Errorf("invalid completed Mistral tool arguments")
			}
			block := Block{Type: "toolCall", ID: state.id, Name: state.name, Arguments: arguments}
			response.Content = append(response.Content, block)
			response.ToolCalls = append(response.ToolCalls, block)
		}
	}
	response.Text, response.Thinking = outText.String(), outThinking.String()
	return response, nil
}

// mapMistralStopReason mirrors pi's mapChatStopReason; "error" is a transient
// server failure that stays retryable.
func mapMistralStopReason(reason string) (string, string) {
	switch reason {
	case "stop":
		return "stop", ""
	case "length", "model_length":
		return "length", ""
	case "tool_calls":
		return "toolUse", ""
	case "error":
		return "error", "Provider stopped with: error (server error)"
	default:
		return "error", fmt.Sprintf("Provider stopped with: %s", reason)
	}
}

func parseMistralUsage(raw json.RawMessage) (Usage, error) {
	var usage struct {
		PromptTokens        int64 `json:"prompt_tokens"`
		CompletionTokens    int64 `json:"completion_tokens"`
		TotalTokens         int64 `json:"total_tokens"`
		PromptTokensDetails *struct {
			CachedTokens *int64 `json:"cached_tokens"`
		} `json:"prompt_tokens_details"`
	}
	if json.Unmarshal(raw, &usage) != nil {
		return Usage{}, fmt.Errorf("malformed Mistral usage")
	}
	cached := int64(0)
	if usage.PromptTokensDetails != nil && usage.PromptTokensDetails.CachedTokens != nil {
		cached = max(int64(0), *usage.PromptTokensDetails.CachedTokens)
	}
	cached = min(usage.PromptTokens, cached)
	input := max(int64(0), usage.PromptTokens-cached)
	total := usage.TotalTokens
	if total == 0 {
		total = input + usage.CompletionTokens + cached
	}
	return Usage{Input: input, Output: usage.CompletionTokens, CacheRead: cached, CacheWrite: 0, TotalTokens: total}, nil
}
