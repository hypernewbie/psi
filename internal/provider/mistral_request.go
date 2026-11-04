package provider

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Mistral tool call IDs are constrained to nine alphanumeric characters.
const mistralToolCallIDLength = 9

// BuildMistralRequest builds the native Mistral Chat Completions payload.
// The wire protocol mirrors pi-ai's mistral-conversations stream: camelCase
// keys here are remapped to snake_case at encoding time.
func BuildMistralRequest(req Request) (map[string]any, error) {
	if req.Model.API != "mistral-conversations" || req.Model.ID == "" {
		return nil, fmt.Errorf("model and mistral-conversations API are required")
	}
	if err := validateProviderAPI(req.Model, "mistral-conversations"); err != nil {
		return nil, err
	}
	for field := range req.Model.Compat {
		return nil, fmt.Errorf("model %q uses unsupported mistral-conversations compatibility field %q", req.Model.ID, field)
	}
	if req.Thinking == "" {
		req.Thinking = "off"
	}
	if !containsLevel(req.Thinking) {
		return nil, fmt.Errorf("unsupported thinking level %q", req.Thinking)
	}
	if req.Thinking != "off" && !req.Model.Reasoning {
		return nil, fmt.Errorf("model %q does not support thinking", req.Model.ID)
	}
	normalized := transformReplayMessages(req.Messages, req.Model, normalizeMistralToolCallIDFactory(nil))
	messages, err := convertMistralMessages(req, normalized)
	if err != nil {
		return nil, err
	}
	payload := map[string]any{"model": req.Model.ID, "stream": true, "messages": messages}
	if len(req.Tools) > 0 {
		tools := make([]any, 0, len(req.Tools))
		for _, tool := range req.Tools {
			schema := tool.Schema
			if schema == nil {
				schema = map[string]any{"type": "object", "properties": map[string]any{}}
			}
			tools = append(tools, map[string]any{"type": "function", "function": map[string]any{
				"name": tool.Name, "description": tool.Description, "parameters": schema, "strict": false,
			}})
		}
		payload["tools"] = tools
	}
	maxTokens := req.MaxTokens
	if maxTokens <= 0 {
		maxTokens = req.Model.MaxTokens
	}
	if maxTokens > 0 {
		payload["maxTokens"] = max(16, maxTokens)
	}
	// Models with a thinking level map use reasoning_effort; other reasoning
	// models use prompt_mode. Effective effort falls back to "high".
	level, ok := clampModelThinkingLevel(req.Model, req.Thinking)
	if !ok {
		return nil, fmt.Errorf("model %q has no supported thinking level", req.Model.ID)
	}
	reasoning := level != "off"
	if req.Model.Reasoning && len(req.Model.ThinkingLevelMap) > 0 {
		if reasoning {
			effort := "high"
			if mapped, present := req.Model.ThinkingLevelMap[level]; present && mapped != nil && *mapped != "" {
				effort = *mapped
			}
			payload["reasoningEffort"] = effort
		} else if off, present := req.Model.ThinkingLevelMap["off"]; present && off != nil && *off != "" {
			payload["reasoningEffort"] = *off
		}
	} else if req.Model.Reasoning && reasoning {
		payload["promptMode"] = "reasoning"
	}
	return payload, nil
}

// mistralToolCallIDNormalizer keeps a bijective mapping between transcript
// tool call IDs and Mistral's nine-character alphanumeric IDs.
type mistralToolCallIDNormalizer struct {
	forward map[string]string
	reverse map[string]string
}

func newMistralToolCallIDNormalizer() *mistralToolCallIDNormalizer {
	return &mistralToolCallIDNormalizer{forward: map[string]string{}, reverse: map[string]string{}}
}

func (n *mistralToolCallIDNormalizer) normalize(id string) string {
	if id == "" {
		return ""
	}
	if existing, ok := n.forward[id]; ok {
		return existing
	}
	for attempt := 0; ; attempt++ {
		candidate := deriveMistralToolCallID(id, attempt)
		if owner, taken := n.reverse[candidate]; !taken || owner == id {
			n.forward[id] = candidate
			n.reverse[candidate] = id
			return candidate
		}
	}
}

func deriveMistralToolCallID(id string, attempt int) string {
	normalized := sanitizeMistralID(id)
	if attempt == 0 && len(normalized) == mistralToolCallIDLength {
		return normalized
	}
	seed := normalized
	if seed == "" {
		seed = id
	}
	if attempt > 0 {
		seed = fmt.Sprintf("%s:%d", seed, attempt)
	}
	hash := sanitizeMistralID(piShortHash(seed))
	return hash[:min(mistralToolCallIDLength, len(hash))]
}

func sanitizeMistralID(id string) string {
	var b strings.Builder
	for _, r := range id {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// normalizeMistralToolCallIDFactory adapts the normalizer for callers that do
// not share a single stream instance.
func normalizeMistralToolCallIDFactory(n *mistralToolCallIDNormalizer) func(string) string {
	if n == nil {
		n = newMistralToolCallIDNormalizer()
	}
	return func(id string) string { return n.normalize(id) }
}

func convertMistralMessages(req Request, messages []Message) ([]any, error) {
	normalizer := newMistralToolCallIDNormalizer()
	normalize := normalizer.normalize
	out := make([]any, 0, len(messages)+1)
	if req.System != "" {
		out = append(out, map[string]any{"role": "system", "content": req.System})
	}
	for i := 0; i < len(messages); i++ {
		message := messages[i]
		switch message.Role {
		case "user":
			converted, err := mistralUserContent(message.Content, req.Model.InputImage)
			if err != nil {
				return nil, fmt.Errorf("user message %d: %w", i, err)
			}
			if converted != nil {
				out = append(out, map[string]any{"role": "user", "content": converted})
			}
		case "assistant":
			if message.StopReason == "error" || message.StopReason == "aborted" {
				continue
			}
			sameModel := message.Provider == req.Model.ProviderID() && message.API == req.Model.API && message.Model == req.Model.ID
			assistant, err := mistralAssistantMessage(message, req.Model, sameModel, normalize)
			if err != nil {
				return nil, fmt.Errorf("assistant message %d: %w", i, err)
			}
			if assistant != nil {
				out = append(out, assistant)
			}
		case "toolResult":
			text, hasImage := toolResultText(message.Content, req.Model.InputImage)
			content := []any{map[string]any{"type": "text", "text": mistralToolResultText(message, text, hasImage, req.Model.InputImage)}}
			if hasImage && req.Model.InputImage {
				for _, block := range message.Content {
					if block.Type == "image" {
						content = append(content, map[string]any{"type": "image_url", "imageUrl": mistralImageDataURL(block)})
					}
				}
			}
			callID, err := normalizeChecked(normalize, message.ToolCallID)
			if err != nil {
				return nil, fmt.Errorf("tool result %d: %w", i, err)
			}
			out = append(out, map[string]any{"role": "tool", "toolCallId": callID, "name": message.ToolName, "content": content})
		default:
			return nil, fmt.Errorf("unsupported mistral message role %q", message.Role)
		}
	}
	return out, nil
}

func normalizeChecked(normalize func(string) string, id string) (string, error) {
	if id == "" {
		return "", fmt.Errorf("tool call is missing its ID")
	}
	return normalize(id), nil
}

func mistralUserContent(blocks []Block, supportsImages bool) (any, error) {
	if len(blocks) == 1 && blocks[0].Type == "text" {
		return blocks[0].Text, nil
	}
	var chunks []any
	hadImages := false
	for _, block := range blocks {
		switch block.Type {
		case "text":
			chunks = append(chunks, map[string]any{"type": "text", "text": block.Text})
		case "image":
			hadImages = true
			if supportsImages {
				chunks = append(chunks, map[string]any{"type": "image_url", "imageUrl": mistralImageDataURL(block)})
			}
		default:
			return nil, fmt.Errorf("unsupported user block %q", block.Type)
		}
	}
	if len(chunks) > 0 {
		return chunks, nil
	}
	if hadImages && !supportsImages {
		return "(image omitted: model does not support images)", nil
	}
	return nil, nil
}

func mistralAssistantMessage(message Message, model ModelMeta, sameModel bool, normalize func(string) string) (map[string]any, error) {
	var content []any
	var toolCalls []any
	for _, block := range message.Content {
		switch block.Type {
		case "text":
			if strings.TrimSpace(block.Text) != "" {
				content = append(content, map[string]any{"type": "text", "text": block.Text})
			}
		case "thinking":
			// Cross-provider thinking replays as plain text; Mistral thinking
			// chunks are only valid for this provider's own messages.
			if sameModel && strings.TrimSpace(block.Thinking) != "" && !block.Redacted {
				content = append(content, map[string]any{"type": "thinking", "thinking": []any{map[string]any{"type": "text", "text": block.Thinking}}})
			} else if !sameModel && strings.TrimSpace(block.Thinking) != "" && len(content) == 0 {
				content = append(content, map[string]any{"type": "text", "text": block.Thinking})
			}
		case "toolCall":
			if block.Name == "" {
				return nil, fmt.Errorf("tool call is missing its name")
			}
			id, err := normalizeChecked(normalize, block.ID)
			if err != nil {
				return nil, err
			}
			args := block.Arguments
			if args == nil {
				args = map[string]any{}
			}
			encoded, err := json.Marshal(args)
			if err != nil {
				return nil, fmt.Errorf("tool call arguments are invalid")
			}
			toolCalls = append(toolCalls, map[string]any{"id": id, "type": "function", "index": 0,
				"function": map[string]any{"name": block.Name, "arguments": string(encoded)}})
		default:
			return nil, fmt.Errorf("unsupported assistant block %q", block.Type)
		}
	}
	if len(content) == 0 && len(toolCalls) == 0 {
		return nil, nil
	}
	assistant := map[string]any{"role": "assistant", "prefix": false}
	if len(content) > 0 {
		assistant["content"] = content
	}
	if len(toolCalls) > 0 {
		assistant["toolCalls"] = toolCalls
	}
	return assistant, nil
}

// mistralToolResultText mirrors pi's buildToolResultText, including the
// error prefix and image placeholders.
func mistralToolResultText(message Message, text string, hasImage, supportsImages bool) string {
	trimmed := strings.TrimSpace(text)
	prefix := ""
	if message.IsError {
		prefix = "[tool error] "
	}
	if trimmed != "" {
		suffix := ""
		if hasImage && !supportsImages {
			suffix = "\n[tool image omitted: model does not support images]"
		}
		return prefix + trimmed + suffix
	}
	if hasImage {
		if supportsImages {
			if message.IsError {
				return "[tool error] (see attached image)"
			}
			return "(see attached image)"
		}
		if message.IsError {
			return "[tool error] (image omitted: model does not support images)"
		}
		return "(image omitted: model does not support images)"
	}
	if message.IsError {
		return "[tool error] (no tool output)"
	}
	return "(no tool output)"
}

func mistralImageDataURL(block Block) string {
	return "data:" + block.MimeType + ";base64," + block.Data
}
