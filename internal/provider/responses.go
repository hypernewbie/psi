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
	"time"

	"psi/internal/providerregistry"
)

// ResponsesClient speaks the generic OpenAI Responses protocol. It uses API-key
// bearer authentication and is separate from the ChatGPT Codex client.
type ResponsesClient struct{ *Client }

func NewResponsesClient(baseURL, apiKey string, maxRetry int, idle time.Duration) *ResponsesClient {
	base := NewClient("", apiKey, maxRetry, idle)
	base.BaseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	return &ResponsesClient{Client: base}
}

func (c *ResponsesClient) Send(ctx context.Context, req Request, onText func(string)) (*Response, error) {
	if req.Model.API != "openai-responses" || req.Model.ProviderID() != "opencode-go" {
		return nil, fmt.Errorf("openai-responses is only enabled for the registered OpenCode Go provider")
	}
	baseURL := c.BaseURL
	if baseURL == "" {
		baseURL = providerregistry.DefaultEndpoint(req.Model.ProviderID(), req.Model.API)
	}
	if baseURL == "" {
		return nil, fmt.Errorf("model endpoint is required for openai-responses")
	}
	if strings.TrimSpace(c.APIKey) == "" && !hasNonemptyAuthHeader(c.Headers) {
		return nil, fmt.Errorf("No API key for provider: %s", req.Model.ProviderID())
	}
	compat, err := resolveResponsesCompat(req.Model)
	if err != nil {
		return nil, err
	}
	body, err := BuildResponsesRequest(req)
	if err != nil {
		return nil, err
	}
	if c.SessionID != "" {
		body["prompt_cache_key"] = clampResponsesCacheKey(c.SessionID)
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("cannot encode OpenAI Responses request")
	}
	return sendWithRetryObserved(ctx, c.MaxRetry, c.RetryObserver, func() (*Response, error) {
		return c.sendResponses(ctx, baseURL, compat, raw, req, onText)
	})
}

func clampResponsesCacheKey(value string) string {
	runes := []rune(value)
	if len(runes) > 64 {
		runes = runes[:64]
	}
	return string(runes)
}

func responsesEndpoint(baseURL string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil || (parsed.Scheme != "https" && parsed.Scheme != "http") || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.Opaque != "" {
		return "", fmt.Errorf("invalid OpenAI Responses endpoint")
	}
	return strings.TrimRight(strings.TrimSpace(baseURL), "/") + "/responses", nil
}

func (c *ResponsesClient) sendResponses(ctx context.Context, baseURL string, compat responsesCompat, body []byte, req Request, onText func(string)) (*Response, error) {
	endpoint, err := responsesEndpoint(baseURL)
	if err != nil {
		return nil, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("invalid OpenAI Responses endpoint")
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "text/event-stream")
	if c.APIKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+c.APIKey)
	}
	if c.SessionID != "" {
		switch compat.sessionAffinityFormat {
		case "openai":
			httpReq.Header.Set("session_id", c.SessionID)
			httpReq.Header.Set("x-client-request-id", c.SessionID)
		case "openai-nosession":
			httpReq.Header.Set("x-client-request-id", c.SessionID)
		case "openrouter":
			httpReq.Header.Set("x-session-id", c.SessionID)
		}
		if req.Model.ProviderID() == "opencode-go" && !hasHeader(c.Headers, "x-opencode-session") {
			httpReq.Header.Set("x-opencode-session", c.SessionID)
		}
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
	result, err := parseResponsesSSE(resp.Body, c.IdleTimeout, ctx, onText, req)
	if err != nil {
		message := c.redact(err.Error())
		wrapped := fmt.Errorf("OpenAI Responses stream interrupted (not retried): %s", message)
		if result != nil {
			return result, &StreamInterrupt{Partial: result, Err: wrapped}
		}
		return nil, wrapped
	}
	return result, nil
}

type responsesOutputItem struct {
	Type      string `json:"type"`
	ID        string `json:"id"`
	CallID    string `json:"call_id"`
	Name      string `json:"name"`
	Namespace string `json:"namespace"`
	Arguments string `json:"arguments"`
	Phase     string `json:"phase"`
	Encrypted string `json:"encrypted_content"`
	Summary   []struct {
		Text string `json:"text"`
	} `json:"summary"`
	Content []struct {
		Type    string `json:"type"`
		Text    string `json:"text"`
		Refusal string `json:"refusal"`
	} `json:"content"`
}

type responsesFinal struct {
	ID         string            `json:"id"`
	Model      string            `json:"model"`
	Status     string            `json:"status"`
	Output     []json.RawMessage `json:"output"`
	Incomplete struct {
		Reason string `json:"reason"`
	} `json:"incomplete_details"`
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
	Usage *struct {
		Input        *int64 `json:"input_tokens"`
		Output       *int64 `json:"output_tokens"`
		Total        *int64 `json:"total_tokens"`
		InputDetails *struct {
			Cached  *int64 `json:"cached_tokens"`
			Written *int64 `json:"cache_write_tokens"`
		} `json:"input_tokens_details"`
		OutputDetails *struct {
			Reasoning *int64 `json:"reasoning_tokens"`
		} `json:"output_tokens_details"`
	} `json:"usage"`
}

type responsesSlot struct {
	item                 responsesOutputItem
	raw                  json.RawMessage
	done                 bool
	text, thinking, args strings.Builder
	streamedText         bool
}

func parseResponsesSSE(r io.Reader, idle time.Duration, ctx context.Context, onText func(string), req Request) (*Response, error) {
	reader := newStreamReader(ctx, r, idle)
	defer reader.close()
	scanner := bufio.NewScanner(io.LimitReader(reader, maxStreamBytes+1))
	scanner.Buffer(make([]byte, 64*1024), maxStreamBytes+1)
	response := &Response{Model: req.Model.ID}
	slots := make(map[int]*responsesSlot)
	var terminal *responsesFinal
	var terminalType string
	var data []string
	totalBytes := 0
	partial := func() *Response {
		copy := *response
		copy.Content = nil
		copy.ToolCalls = nil
		var text, thinking strings.Builder
		indices := sortedResponseIndices(slots)
		for _, index := range indices {
			slot := slots[index]
			if slot.item.Type != "message" && slot.item.Type != "reasoning" {
				continue
			}
			var block Block
			if slot.done {
				block, _ = responsesBlockFromSlot(slot)
			} else if slot.item.Type == "message" {
				block = Block{Type: "text", Text: slot.text.String()}
			} else {
				block = Block{Type: "thinking", Thinking: slot.thinking.String()}
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
		if terminal != nil {
			return fmt.Errorf("OpenAI Responses data arrived after completion")
		}
		if strings.TrimSpace(encoded) == "[DONE]" {
			return fmt.Errorf("unexpected OpenAI Responses [DONE] marker")
		}
		var event struct {
			Type        string          `json:"type"`
			OutputIndex *int            `json:"output_index"`
			Item        json.RawMessage `json:"item"`
			Delta       string          `json:"delta"`
			Arguments   string          `json:"arguments"`
			Response    *responsesFinal `json:"response"`
			Code        string          `json:"code"`
		}
		if !json.Valid([]byte(encoded)) || json.Unmarshal([]byte(encoded), &event) != nil || event.Type == "" {
			return fmt.Errorf("malformed OpenAI Responses SSE JSON")
		}
		switch event.Type {
		case "error":
			return fmt.Errorf("OpenAI Responses provider returned an error event")
		case "response.failed":
			if event.Response == nil {
				return fmt.Errorf("OpenAI Responses failure event lacks a response")
			}
			terminal, terminalType = event.Response, event.Type
			response.ResponseID = event.Response.ID
			if event.Response.Model != "" {
				response.Model = event.Response.Model
			}
			response.RawStopReason = event.Response.Status
			setResponsesUsage(response, event.Response.Usage)
			return fmt.Errorf("OpenAI Responses provider reported a failed response")
		case "response.completed", "response.incomplete":
			if event.Response == nil {
				return fmt.Errorf("OpenAI Responses terminal event lacks a response")
			}
			terminal, terminalType = event.Response, event.Type
			if terminal.ID != "" {
				response.ResponseID = terminal.ID
			}
			if terminal.Model != "" {
				response.Model = terminal.Model
			}
			setResponsesUsage(response, terminal.Usage)
		case "response.created":
			if event.Response == nil || event.Response.ID == "" {
				return fmt.Errorf("invalid OpenAI Responses created event")
			}
			response.ResponseID = event.Response.ID
			if event.Response.Model != "" {
				response.Model = event.Response.Model
			}
		case "response.output_item.added", "response.output_item.done":
			if event.OutputIndex == nil || *event.OutputIndex < 0 || *event.OutputIndex >= 4096 {
				return fmt.Errorf("invalid OpenAI Responses output index")
			}
			item, err := decodeResponsesItem(event.Item)
			if err != nil {
				return err
			}
			slot := slots[*event.OutputIndex]
			if event.Type == "response.output_item.added" {
				if slot != nil {
					return fmt.Errorf("duplicate OpenAI Responses output item")
				}
				slot = &responsesSlot{item: item, raw: append(json.RawMessage(nil), event.Item...)}
				if item.Type == "function_call" {
					slot.args.WriteString(item.Arguments)
				}
				slots[*event.OutputIndex] = slot
			} else {
				if slot == nil {
					slot = &responsesSlot{item: item}
					slots[*event.OutputIndex] = slot
				} else if slot.done || slot.item.Type != item.Type || slot.item.ID != item.ID {
					return fmt.Errorf("mismatched OpenAI Responses output item")
				}
				slot.item = item
				slot.raw = append(json.RawMessage(nil), event.Item...)
				slot.done = true
				if err := finalizeResponsesSlot(slot, onText); err != nil {
					return err
				}
			}
		case "response.output_text.delta", "response.refusal.delta", "response.reasoning_summary_text.delta", "response.reasoning_text.delta", "response.reasoning_summary_part.done", "response.function_call_arguments.delta", "response.function_call_arguments.done":
			if event.OutputIndex == nil || *event.OutputIndex < 0 || slots[*event.OutputIndex] == nil || slots[*event.OutputIndex].done {
				return fmt.Errorf("OpenAI Responses delta has no active output item")
			}
			slot := slots[*event.OutputIndex]
			switch event.Type {
			case "response.output_text.delta", "response.refusal.delta":
				if slot.item.Type != "message" {
					return fmt.Errorf("OpenAI Responses text delta has wrong item type")
				}
				slot.text.WriteString(event.Delta)
				slot.streamedText = true
				if onText != nil {
					onText(event.Delta)
				}
			case "response.reasoning_summary_text.delta", "response.reasoning_text.delta":
				if slot.item.Type != "reasoning" {
					return fmt.Errorf("OpenAI Responses reasoning delta has wrong item type")
				}
				slot.thinking.WriteString(event.Delta)
			case "response.reasoning_summary_part.done":
				if slot.item.Type != "reasoning" {
					return fmt.Errorf("OpenAI Responses reasoning summary has wrong item type")
				}
				slot.thinking.WriteString("\n\n")
			case "response.function_call_arguments.delta":
				if slot.item.Type != "function_call" {
					return fmt.Errorf("OpenAI Responses argument delta has wrong item type")
				}
				slot.args.WriteString(event.Delta)
			case "response.function_call_arguments.done":
				if slot.item.Type != "function_call" {
					return fmt.Errorf("OpenAI Responses final arguments have wrong item type")
				}
				replaceResponsesArguments(&slot.args, event.Arguments)
			}
		default:
			// Other lifecycle, content-part, and annotation events do not alter the
			// supported text, reasoning, function-call, or settlement state.
		}
		return nil
	}

	for scanner.Scan() {
		line := strings.TrimSuffix(scanner.Text(), "\r")
		totalBytes += len(scanner.Text()) + 1
		if totalBytes > maxStreamBytes {
			return partial(), fmt.Errorf("OpenAI Responses stream exceeds size limit")
		}
		if line == "" {
			if err := consume(); err != nil {
				return partial(), err
			}
			if terminal != nil {
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
	if terminal == nil {
		return partial(), fmt.Errorf("OpenAI Responses stream ended before a terminal response event")
	}
	if terminalType == "response.failed" {
		return partial(), fmt.Errorf("OpenAI Responses provider reported a failed response")
	}
	if terminal.Status == "" {
		if terminalType == "response.incomplete" {
			terminal.Status = "incomplete"
		} else {
			terminal.Status = "completed"
		}
	}
	response.RawStopReason = terminal.Status
	if terminal.Incomplete.Reason != "" {
		response.RawStopReason += "." + terminal.Incomplete.Reason
	}
	if terminal.Status != "completed" && terminal.Status != "incomplete" && terminal.Status != "queued" && terminal.Status != "in_progress" {
		return partial(), fmt.Errorf("OpenAI Responses response did not complete successfully")
	}
	if terminal.Status == "incomplete" && terminal.Incomplete.Reason != "max_output_tokens" {
		return partial(), fmt.Errorf("OpenAI Responses response was incomplete")
	}
	for _, slot := range slots {
		if slot.done {
			continue
		}
		if terminal.Status == "completed" || (slot.item.Type == "function_call" && terminal.Status != "incomplete") {
			return partial(), fmt.Errorf("OpenAI Responses response contains an unfinished output item")
		}
	}
	if err := backfillResponsesReasoning(slots, terminal.Output); err != nil {
		return partial(), err
	}
	response.StopReason = "stop"
	if terminal.Status == "incomplete" {
		response.StopReason = "length"
	}
	indices := sortedResponseIndices(slots)
	seenItemIDs, seenCallIDs := make(map[string]bool), make(map[string]bool)
	for _, index := range indices {
		slot := slots[index]
		if !slot.done && slot.item.Type == "function_call" {
			continue
		}
		block, err := responsesBlockFromSlot(slot)
		if err != nil {
			return partial(), err
		}
		if slot.item.ID != "" {
			if seenItemIDs[slot.item.ID] {
				return partial(), fmt.Errorf("duplicate OpenAI Responses output item ID")
			}
			seenItemIDs[slot.item.ID] = true
		}
		if block.Type == "toolCall" {
			callID, itemID := splitResponsesToolID(block.ID)
			if callID == "" || itemID == "" || seenCallIDs[callID] {
				return partial(), fmt.Errorf("invalid or duplicate completed OpenAI Responses tool call")
			}
			seenCallIDs[callID] = true
			response.ToolCalls = append(response.ToolCalls, block)
		}
		response.Content = append(response.Content, block)
		if block.Type == "text" {
			response.Text += block.Text
		} else if block.Type == "thinking" {
			response.Thinking += block.Thinking
		}
	}
	if len(response.ToolCalls) > 0 && response.StopReason == "stop" {
		response.StopReason = "toolUse"
	}
	return response, nil
}

func decodeResponsesItem(raw json.RawMessage) (responsesOutputItem, error) {
	var item responsesOutputItem
	if len(raw) == 0 || !json.Valid(raw) || json.Unmarshal(raw, &item) != nil || item.ID == "" {
		return item, fmt.Errorf("invalid OpenAI Responses output item")
	}
	switch item.Type {
	case "message", "reasoning":
	case "function_call":
		if item.CallID == "" || item.Name == "" || strings.Contains(item.CallID, "|") || strings.Contains(item.ID, "|") {
			return item, fmt.Errorf("invalid OpenAI Responses function call")
		}
	default:
		return item, fmt.Errorf("unsupported OpenAI Responses output item %q", item.Type)
	}
	return item, nil
}

func finalizeResponsesSlot(slot *responsesSlot, onText func(string)) error {
	switch slot.item.Type {
	case "message":
		var final strings.Builder
		for _, content := range slot.item.Content {
			switch content.Type {
			case "output_text":
				final.WriteString(content.Text)
			case "refusal":
				final.WriteString(content.Refusal)
			default:
				return fmt.Errorf("unsupported OpenAI Responses message content")
			}
		}
		if slot.text.Len() > 0 && slot.text.String() != final.String() {
			return fmt.Errorf("OpenAI Responses completed text differs from its deltas")
		}
		if !slot.streamedText && final.Len() > 0 && onText != nil {
			onText(final.String())
		}
	case "function_call":
		if slot.item.Arguments != "" {
			replaceResponsesArguments(&slot.args, slot.item.Arguments)
		}
		if !json.Valid([]byte(slot.args.String())) {
			return fmt.Errorf("invalid completed OpenAI Responses tool arguments")
		}
		var arguments map[string]json.RawMessage
		decoder := json.NewDecoder(strings.NewReader(slot.args.String()))
		decoder.UseNumber()
		if decoder.Decode(&arguments) != nil || arguments == nil {
			return fmt.Errorf("OpenAI Responses tool arguments must be an object")
		}
	case "reasoning":
	default:
		return fmt.Errorf("unsupported OpenAI Responses output item")
	}
	return nil
}

func replaceResponsesArguments(buffer *strings.Builder, value string) {
	current := buffer.String()
	if strings.HasPrefix(value, current) {
		buffer.WriteString(value[len(current):])
	} else {
		buffer.Reset()
		buffer.WriteString(value)
	}
}

func responsesBlockFromSlot(slot *responsesSlot) (Block, error) {
	switch slot.item.Type {
	case "message":
		if !slot.done {
			return Block{Type: "text", Text: slot.text.String()}, nil
		}
		var text strings.Builder
		for _, content := range slot.item.Content {
			if content.Type == "output_text" {
				text.WriteString(content.Text)
			} else if content.Type == "refusal" {
				text.WriteString(content.Refusal)
			}
		}
		id, _ := json.Marshal(map[string]any{"v": 1, "id": slot.item.ID})
		if slot.item.Phase != "" {
			var signature map[string]any
			_ = json.Unmarshal(id, &signature)
			signature["phase"] = slot.item.Phase
			id, _ = json.Marshal(signature)
		}
		return Block{Type: "text", Text: text.String(), TextSignature: string(id)}, nil
	case "reasoning":
		if !slot.done {
			return Block{Type: "thinking", Thinking: slot.thinking.String()}, nil
		}
		var summary []string
		for _, item := range slot.item.Summary {
			summary = append(summary, item.Text)
		}
		thinking := strings.Join(summary, "\n\n")
		if thinking == "" {
			var content []string
			for _, item := range slot.item.Content {
				content = append(content, item.Text)
			}
			thinking = strings.Join(content, "\n\n")
		}
		if thinking == "" {
			thinking = slot.thinking.String()
		}
		return Block{Type: "thinking", Thinking: thinking, ThinkingSignature: string(slot.raw)}, nil
	case "function_call":
		var arguments map[string]any
		decoder := json.NewDecoder(strings.NewReader(slot.args.String()))
		decoder.UseNumber()
		if decoder.Decode(&arguments) != nil || arguments == nil {
			return Block{}, fmt.Errorf("OpenAI Responses tool arguments must be an object")
		}
		return Block{Type: "toolCall", ID: slot.item.CallID + "|" + slot.item.ID, Name: slot.item.Name, Arguments: arguments, Namespace: slot.item.Namespace}, nil
	default:
		return Block{}, fmt.Errorf("unsupported OpenAI Responses output item")
	}
}

func setResponsesUsage(response *Response, usage *responsesFinalUsage) {
	if usage == nil {
		return
	}
	input, output, total, cached, written := int64(0), int64(0), int64(0), int64(0), int64(0)
	if usage.Input != nil {
		input = *usage.Input
	}
	if usage.Output != nil {
		output = *usage.Output
	}
	if usage.Total != nil {
		total = *usage.Total
	}
	if usage.InputDetails != nil {
		if usage.InputDetails.Cached != nil {
			cached = *usage.InputDetails.Cached
		}
		if usage.InputDetails.Written != nil {
			written = *usage.InputDetails.Written
		}
	}
	if input < 0 || output < 0 || total < 0 || cached < 0 || written < 0 {
		return
	}
	response.Usage = Usage{Input: max(0, input-cached-written), Output: output, TotalTokens: total, CacheRead: cached, CacheWrite: written}
	if usage.OutputDetails != nil && usage.OutputDetails.Reasoning != nil && *usage.OutputDetails.Reasoning >= 0 {
		reasoning := *usage.OutputDetails.Reasoning
		response.Usage.Reasoning = &reasoning
	}
}

type responsesFinalUsage = struct {
	Input        *int64 `json:"input_tokens"`
	Output       *int64 `json:"output_tokens"`
	Total        *int64 `json:"total_tokens"`
	InputDetails *struct {
		Cached  *int64 `json:"cached_tokens"`
		Written *int64 `json:"cache_write_tokens"`
	} `json:"input_tokens_details"`
	OutputDetails *struct {
		Reasoning *int64 `json:"reasoning_tokens"`
	} `json:"output_tokens_details"`
}

func backfillResponsesReasoning(slots map[int]*responsesSlot, finalOutput []json.RawMessage) error {
	byID := make(map[string]*responsesSlot)
	for _, slot := range slots {
		if slot.item.Type == "reasoning" {
			byID[slot.item.ID] = slot
		}
	}
	for _, raw := range finalOutput {
		var final responsesOutputItem
		if json.Unmarshal(raw, &final) != nil {
			return fmt.Errorf("invalid OpenAI Responses terminal output")
		}
		if final.Type != "reasoning" || final.Encrypted == "" {
			continue
		}
		slot := byID[final.ID]
		if slot == nil || slot.item.Encrypted != "" {
			continue
		}
		var fields map[string]json.RawMessage
		if json.Unmarshal(slot.raw, &fields) != nil {
			return fmt.Errorf("invalid OpenAI Responses reasoning metadata")
		}
		encrypted, _ := json.Marshal(final.Encrypted)
		fields["encrypted_content"] = encrypted
		updated, err := json.Marshal(fields)
		if err != nil {
			return fmt.Errorf("invalid OpenAI Responses reasoning metadata")
		}
		slot.raw = updated
		slot.item.Encrypted = final.Encrypted
	}
	return nil
}

func sortedResponseIndices(slots map[int]*responsesSlot) []int {
	indices := make([]int, 0, len(slots))
	for index := range slots {
		indices = append(indices, index)
	}
	for i := 1; i < len(indices); i++ {
		for j := i; j > 0 && indices[j] < indices[j-1]; j-- {
			indices[j], indices[j-1] = indices[j-1], indices[j]
		}
	}
	return indices
}
