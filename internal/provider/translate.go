package provider

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Translate outgoing context to the Anthropic Messages protocol (Step 48).
// Stops with an actionable error for unsupported content instead of dropping it.

type anthropicRequest struct {
	Model     string           `json:"model"`
	MaxTokens int              `json:"max_tokens"`
	System    any              `json:"system,omitempty"`
	Messages  []map[string]any `json:"messages"`
	Tools     []map[string]any `json:"tools,omitempty"`
	Thinking  map[string]any   `json:"thinking,omitempty"`
	Stream    bool             `json:"stream"`
}

// BuildAnthropicRequest converts normalized context. toolChoice is always auto.
func BuildAnthropicRequest(req Request) (map[string]any, error) {
	if req.Model.API != "" && req.Model.API != "anthropic-messages" {
		return nil, fmt.Errorf("unsupported API %q: first release supports anthropic-messages only", req.Model.API)
	}
	if err := validateProviderAPI(req.Model, "anthropic-messages"); err != nil {
		return nil, err
	}
	if req.Model.ID == "" {
		return nil, fmt.Errorf("model is required")
	}
	if req.Thinking == "" {
		req.Thinking = "off"
	}
	budget, err := ThinkingBudget(req.Thinking)
	if err != nil {
		return nil, err
	}
	if req.Thinking != "off" && req.Thinking != "" && !req.Model.Reasoning {
		return nil, fmt.Errorf("model %q does not support thinking level %q", req.Model.ID, req.Thinking)
	}
	maxTokens := req.MaxTokens
	if maxTokens <= 0 {
		maxTokens = req.Model.MaxTokens
	}
	if maxTokens <= 0 {
		maxTokens = 16384
	}
	if budget >= maxTokens && budget > 0 && !req.Model.AdaptiveThinking {
		return nil, fmt.Errorf("thinking budget %d must be less than max output tokens %d", budget, maxTokens)
	}
	effort, err := anthropicEffort(req.Model, req.Thinking)
	if err != nil {
		return nil, err
	}
	managed := req.Model.AdaptiveThinking && req.Model.MidConvoEffort && effort != ""
	var messages []map[string]any
	var errorImages []any
	for i, m := range req.Messages {
		sameModel := (m.Provider == "" || m.Provider == req.Model.ProviderID()) && (m.API == "" || m.API == "anthropic-messages") && (m.Model == "" || m.Model == req.Model.ID)
		if m.Role == "assistant" {
			content := make([]Block, 0, len(m.Content))
			for _, b := range m.Content {
				if b.Type == "thinking" && !sameModel {
					if b.Redacted || b.Thinking == "" {
						continue
					}
					b = Block{Type: "text", Text: b.Thinking}
				}
				if b.Type == "toolCall" {
					if b.ID == "" || b.Name == "" {
						return nil, fmt.Errorf("invalid Anthropic tool call")
					}
					b.ID = codexID(b.ID) // both protocols require bounded identifier characters
				}
				content = append(content, b)
			}
			m.Content = content
			if len(content) == 0 {
				continue
			}
		}
		if m.Role == "toolResult" {
			if m.ToolCallID == "" {
				return nil, fmt.Errorf("tool result is missing its call ID")
			}
			m.ToolCallID = codexID(m.ToolCallID)
		}
		if managed && m.Role == "assistant" && m.Provider == req.Model.ProviderID() && m.API == "anthropic-messages" && validAnthropicEffort(m.ThinkingLevel) {
			messages = append(messages, effortMarker(m.ThinkingLevel))
		}
		for _, b := range m.Content {
			if b.Type == "image" && !req.Model.InputImage {
				return nil, fmt.Errorf("model %q is not configured for image input", req.Model.ID)
			}
		}
		if m.Role == "toolResult" && m.IsError {
			var text []Block
			hadImage := false
			for _, b := range m.Content {
				if b.Type == "image" {
					if !hadImage {
						errorImages = append(errorImages, map[string]any{"type": "text", "text": "Image from failed tool " + m.ToolCallID + " (" + m.ToolName + ")"})
					}
					hadImage = true
					converted, err := convertMessage(Message{Role: "user", Content: []Block{b}}, false)
					if err != nil {
						return nil, err
					}
					errorImages = append(errorImages, converted["content"].([]any)...)
				} else {
					text = append(text, b)
				}
			}
			if len(text) == 0 && hadImage {
				text = append(text, Block{Type: "text", Text: "Tool returned image content with an error."})
			}
			m.Content = text
		}
		conv, err := convertMessage(m, req.Model.AllowEmptySignature)
		if err != nil {
			return nil, fmt.Errorf("message %d: %w", i, err)
		}
		// Anthropic tool results from one batch must share the following user
		// message. Merge consecutive user blocks, preserving their order.
		if m.Role == "assistant" {
			if content, ok := conv["content"].([]any); ok && len(content) == 0 {
				continue
			}
		}
		if len(messages) > 0 && conv["role"] == "user" && messages[len(messages)-1]["role"] == "user" {
			last := messages[len(messages)-1]
			last["content"] = append(last["content"].([]any), conv["content"].([]any)...)
		} else {
			messages = append(messages, conv)
		}
		if m.Role == "toolResult" && (i+1 == len(req.Messages) || req.Messages[i+1].Role != "toolResult") && len(errorImages) > 0 {
			last := messages[len(messages)-1]
			last["content"] = append(last["content"].([]any), errorImages...)
			errorImages = nil
		}
	}
	if managed {
		messages = append(messages, effortMarker(effort))
	}
	out := map[string]any{
		"model":      req.Model.ID,
		"max_tokens": maxTokens,
		"messages":   messages,
		"stream":     true,
	}
	if req.System != "" {
		out["system"] = req.System
	}
	if len(req.Tools) > 0 {
		var tools []map[string]any
		for _, t := range req.Tools {
			schema := t.Schema
			if schema == nil {
				schema = map[string]any{"type": "object", "properties": map[string]any{}}
			}
			tools = append(tools, map[string]any{"name": t.Name, "description": t.Description, "input_schema": schema})
		}
		out["tools"] = tools
	}
	if req.Model.AdaptiveThinking {
		if req.Thinking == "off" && !managed {
			if mapped, exists := req.Model.ThinkingLevelMap["off"]; !exists || mapped != nil {
				out["thinking"] = map[string]any{"type": "disabled"}
			}
		} else {
			thinking := map[string]any{"type": "adaptive", "display": "summarized"}
			out["output_config"] = map[string]any{"effort": effort}
			if managed {
				thinking["block_binding"] = map[string]any{"prefix_mismatch_behavior": "drop_block"}
				out["output_config"] = map[string]any{"effort": "low"}
			}
			out["thinking"] = thinking
		}
	} else if req.Thinking != "off" {
		out["thinking"] = map[string]any{"type": "enabled", "budget_tokens": budget}
	}
	return out, nil
}

func effortMarker(effort string) map[string]any {
	return map[string]any{"role": "system", "content": []any{}, "output_config": map[string]any{"effort": effort}}
}

func validAnthropicEffort(level string) bool {
	switch level {
	case "low", "medium", "high", "xhigh", "max":
		return true
	}
	return false
}

func anthropicEffort(model ModelMeta, level string) (string, error) {
	if level == "" {
		level = "off"
	}
	if !model.AdaptiveThinking {
		return "", nil
	}
	if level == "off" {
		if mapped, exists := model.ThinkingLevelMap["off"]; exists && mapped == nil && model.MidConvoEffort {
			return "high", nil
		}
		return "", nil
	}
	if mapped, exists := model.ThinkingLevelMap[level]; exists {
		if mapped == nil {
			return "", fmt.Errorf("model %q does not support thinking level %q", model.ID, level)
		}
		switch *mapped {
		case "low", "medium", "high", "xhigh", "max":
			return *mapped, nil
		default:
			return "", fmt.Errorf("unsupported Anthropic effort mapping")
		}
	}
	switch level {
	case "minimal", "low":
		return "low", nil
	case "medium":
		return "medium", nil
	default:
		return "high", nil
	}
}

func convertMessage(m Message, allowEmptySignature bool) (map[string]any, error) {
	switch m.Role {
	case "user":
		var blocks []any
		for _, b := range m.Content {
			switch b.Type {
			case "text":
				blocks = append(blocks, map[string]any{"type": "text", "text": b.Text})
			case "image":
				mt := b.MimeType
				switch mt {
				case "image/jpeg", "image/png", "image/gif", "image/webp":
				default:
					return nil, fmt.Errorf("unsupported image mime %q for Anthropic", mt)
				}
				blocks = append(blocks, map[string]any{"type": "image", "source": map[string]any{"type": "base64", "media_type": mt, "data": b.Data}})
			default:
				return nil, fmt.Errorf("unsupported user block %q", b.Type)
			}
		}
		return map[string]any{"role": "user", "content": blocks}, nil
	case "assistant":
		var blocks []any
		for _, b := range m.Content {
			switch b.Type {
			case "text":
				blocks = append(blocks, map[string]any{"type": "text", "text": b.Text})
			case "thinking":
				if b.Redacted {
					if b.ThinkingSignature == "" {
						return nil, fmt.Errorf("redacted thinking is missing its opaque payload")
					}
					blocks = append(blocks, map[string]any{"type": "redacted_thinking", "data": b.ThinkingSignature})
				} else if strings.TrimSpace(b.ThinkingSignature) != "" {
					blocks = append(blocks, map[string]any{"type": "thinking", "thinking": b.Thinking, "signature": b.ThinkingSignature})
				} else if strings.TrimSpace(b.Thinking) != "" {
					if allowEmptySignature {
						blocks = append(blocks, map[string]any{"type": "thinking", "thinking": b.Thinking, "signature": ""})
					} else {
						// Unsigned historical reasoning cannot be replayed as a signed
						// Anthropic thinking block. Preserve its text instead.
						blocks = append(blocks, map[string]any{"type": "text", "text": b.Thinking})
					}
				}
			case "toolCall":
				args, err := json.Marshal(b.Arguments)
				if err != nil {
					return nil, err
				}
				_ = args
				blocks = append(blocks, map[string]any{"type": "tool_use", "id": b.ID, "name": b.Name, "input": b.Arguments})
			default:
				return nil, fmt.Errorf("unsupported assistant block %q", b.Type)
			}
		}
		return map[string]any{"role": "assistant", "content": blocks}, nil
	case "toolResult":
		// Anthropic tool_result blocks live in a user message.
		var inner []any
		for _, b := range m.Content {
			if b.Type != "text" && b.Type != "image" {
				return nil, fmt.Errorf("unsupported tool-result block %q", b.Type)
			}
			if b.Type == "text" {
				inner = append(inner, map[string]any{"type": "text", "text": b.Text})
			} else {
				converted, err := convertMessage(Message{Role: "user", Content: []Block{b}}, false)
				if err != nil {
					return nil, err
				}
				inner = append(inner, converted["content"].([]any)...)
			}
		}
		return map[string]any{"role": "user", "content": []any{map[string]any{
			"type": "tool_result", "tool_use_id": m.ToolCallID, "content": inner, "is_error": m.IsError,
		}}}, nil
	case "system":
		return nil, fmt.Errorf("system messages must be folded into the request system field, not messages[]")
	default:
		return nil, fmt.Errorf("unsupported role %q: refuse lossy continuation", m.Role)
	}
}

func jsonUnmarshal(b []byte, v any) error { return json.Unmarshal(b, v) }
