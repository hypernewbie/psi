package provider

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

type completionsCompat struct {
	maxTokensField                              string
	supportsStore, supportsDeveloperRole        bool
	supportsUsageInStreaming, supportsFinish    bool
	supportsStrictMode, supportsReasoningEffort bool
	requiresToolResultName, requiresAssistant   bool
	requiresThinkingAsText, requiresReasoning   bool
	thinkingFormat                              string
	zaiToolStream                               bool
}

var completionsCompatFields = map[string]bool{
	"maxTokensField":                              true,
	"supportsStore":                               true,
	"supportsDeveloperRole":                       true,
	"supportsUsageInStreaming":                    true,
	"supportsFinishReason":                        true,
	"supportsStrictMode":                          true,
	"supportsReasoningEffort":                     true,
	"requiresToolResultName":                      true,
	"requiresAssistantAfterToolResult":            true,
	"requiresThinkingAsText":                      true,
	"requiresReasoningContentOnAssistantMessages": true,
	"thinkingFormat":                              true,
	"zaiToolStream":                               true,
	"supportsMidConvoSystemMessages":              true,
	"supportsMidConvoToolAdditions":               true,
}

func resolveCompletionsCompat(model ModelMeta) (completionsCompat, error) {
	for key := range model.Compat {
		if !completionsCompatFields[key] {
			return completionsCompat{}, fmt.Errorf("model %q uses unsupported openai-completions compatibility field %q", model.ID, key)
		}
	}
	boolValue := func(key string, fallback bool) (bool, error) {
		value, hasValue, present := model.Compat.Bool(key)
		if !present || model.Compat.IsNull(key) {
			return fallback, nil
		}
		if !hasValue {
			return false, fmt.Errorf("model %q has invalid compatibility field %q", model.ID, key)
		}
		return value, nil
	}
	stringValue := func(key, fallback string) (string, error) {
		value, hasValue, present := model.Compat.String(key)
		if !present || model.Compat.IsNull(key) {
			return fallback, nil
		}
		if !hasValue {
			return "", fmt.Errorf("model %q has invalid compatibility field %q", model.ID, key)
		}
		return value, nil
	}
	defaultThinking := "openai"
	defaultMaxTokens := "max_completion_tokens"
	switch model.ProviderID() {
	case "deepseek":
		defaultThinking, defaultMaxTokens = "deepseek", "max_tokens"
	case "zai":
		defaultThinking, defaultMaxTokens = "zai", "max_tokens"
	case "opencode-go":
	default:
		return completionsCompat{}, fmt.Errorf("openai-completions is not supported for provider %q", model.ProviderID())
	}
	compat := completionsCompat{}
	var err error
	if compat.maxTokensField, err = stringValue("maxTokensField", defaultMaxTokens); err != nil {
		return compat, err
	}
	if compat.maxTokensField != "max_tokens" && compat.maxTokensField != "max_completion_tokens" {
		return compat, fmt.Errorf("model %q uses unsupported maxTokensField %q", model.ID, compat.maxTokensField)
	}
	if compat.supportsStore, err = boolValue("supportsStore", false); err != nil {
		return compat, err
	}
	if compat.supportsDeveloperRole, err = boolValue("supportsDeveloperRole", false); err != nil {
		return compat, err
	}
	if compat.supportsUsageInStreaming, err = boolValue("supportsUsageInStreaming", true); err != nil {
		return compat, err
	}
	if compat.supportsFinish, err = boolValue("supportsFinishReason", true); err != nil {
		return compat, err
	}
	if compat.supportsStrictMode, err = boolValue("supportsStrictMode", false); err != nil {
		return compat, err
	}
	if compat.requiresToolResultName, err = boolValue("requiresToolResultName", false); err != nil {
		return compat, err
	}
	if compat.requiresAssistant, err = boolValue("requiresAssistantAfterToolResult", false); err != nil {
		return compat, err
	}
	if compat.requiresThinkingAsText, err = boolValue("requiresThinkingAsText", false); err != nil {
		return compat, err
	}
	if compat.requiresReasoning, err = boolValue("requiresReasoningContentOnAssistantMessages", model.ProviderID() == "deepseek"); err != nil {
		return compat, err
	}
	if compat.supportsReasoningEffort, err = boolValue("supportsReasoningEffort", model.ProviderID() != "zai"); err != nil {
		return compat, err
	}
	if compat.zaiToolStream, err = boolValue("zaiToolStream", false); err != nil {
		return compat, err
	}
	if compat.thinkingFormat, err = stringValue("thinkingFormat", defaultThinking); err != nil {
		return compat, err
	}
	if compat.thinkingFormat != "deepseek" && compat.thinkingFormat != "zai" && compat.thinkingFormat != "openai" {
		return compat, fmt.Errorf("model %q uses unsupported thinkingFormat %q", model.ID, compat.thinkingFormat)
	}
	for _, key := range []string{"supportsMidConvoSystemMessages", "supportsMidConvoToolAdditions"} {
		enabled, err := boolValue(key, false)
		if err != nil {
			return compat, err
		}
		if enabled {
			return compat, fmt.Errorf("model %q requires unsupported compatibility feature %q", model.ID, key)
		}
	}
	return compat, nil
}

// BuildCompletionsRequest translates the normalized conversation to the
// OpenAI Chat Completions wire format used by DeepSeek, Z.AI, and OpenCode Go.
func BuildCompletionsRequest(req Request) (map[string]any, error) {
	if req.Model.API != "openai-completions" || req.Model.ID == "" {
		return nil, fmt.Errorf("model and openai-completions API are required")
	}
	if err := validateProviderAPI(req.Model, "openai-completions"); err != nil {
		return nil, err
	}
	compat, err := resolveCompletionsCompat(req.Model)
	if err != nil {
		return nil, err
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
	if mapped, exists := req.Model.ThinkingLevelMap[req.Thinking]; exists && mapped == nil && req.Thinking != "off" {
		return nil, fmt.Errorf("model %q does not support thinking level %q", req.Model.ID, req.Thinking)
	}
	maxTokens := req.MaxTokens
	if maxTokens <= 0 {
		maxTokens = req.Model.MaxTokens
	}
	if maxTokens <= 0 {
		maxTokens = 16384
	}
	messages, err := convertCompletionsMessages(req, compat)
	if err != nil {
		return nil, err
	}
	params := map[string]any{"model": req.Model.ID, "messages": messages, "stream": true}
	params[compat.maxTokensField] = maxTokens
	if compat.supportsUsageInStreaming {
		params["stream_options"] = map[string]any{"include_usage": true}
	}
	if compat.supportsStore {
		params["store"] = false
	}
	if req.Model.Reasoning {
		switch compat.thinkingFormat {
		case "deepseek":
			if req.Thinking != "off" {
				params["thinking"] = map[string]any{"type": "enabled"}
				if compat.supportsReasoningEffort {
					if effort, ok := mappedEffort(req.Model, req.Thinking); ok {
						params["reasoning_effort"] = effort
					}
				}
			} else if off, exists := req.Model.ThinkingLevelMap["off"]; !exists || off != nil {
				params["thinking"] = map[string]any{"type": "disabled"}
			}
		case "zai":
			if req.Thinking == "off" {
				params["thinking"] = map[string]any{"type": "disabled"}
			} else {
				params["thinking"] = map[string]any{"type": "enabled", "clear_thinking": false}
				if compat.supportsReasoningEffort {
					if effort, ok := mappedEffort(req.Model, req.Thinking); ok {
						params["reasoning_effort"] = effort
					}
				}
			}
		case "openai":
			if compat.supportsReasoningEffort {
				if req.Thinking != "off" {
					if effort, ok := mappedEffort(req.Model, req.Thinking); ok {
						params["reasoning_effort"] = effort
					}
				} else if off, ok := req.Model.ThinkingLevelMap["off"]; ok && off != nil {
					params["reasoning_effort"] = *off
				}
			}
		}
	}
	if len(req.Tools) > 0 {
		wireTools := make([]any, 0, len(req.Tools))
		for _, tool := range req.Tools {
			schema := tool.Schema
			if schema == nil {
				schema = map[string]any{"type": "object", "properties": map[string]any{}}
			}
			function := map[string]any{"name": tool.Name, "description": tool.Description, "parameters": schema}
			if compat.supportsStrictMode {
				function["strict"] = false
			}
			wireTools = append(wireTools, map[string]any{"type": "function", "function": function})
		}
		params["tools"] = wireTools
		if compat.zaiToolStream {
			params["tool_stream"] = true
		}
	} else if hasCompletionsToolHistory(req.Messages) {
		params["tools"] = []any{}
	}
	return params, nil
}

func hasCompletionsToolHistory(messages []Message) bool {
	for _, message := range messages {
		if message.Role == "toolResult" {
			return true
		}
		if message.Role == "assistant" {
			for _, block := range message.Content {
				if block.Type == "toolCall" {
					return true
				}
			}
		}
	}
	return false
}

func containsLevel(level string) bool {
	for _, candidate := range ValidLevels {
		if candidate == level {
			return true
		}
	}
	return false
}

func mappedEffort(model ModelMeta, level string) (string, bool) {
	if mapped, ok := model.ThinkingLevelMap[level]; ok {
		if mapped == nil {
			return "", false
		}
		return *mapped, true
	}
	return level, true
}

func convertCompletionsMessages(req Request, compat completionsCompat) ([]any, error) {
	messages := make([]any, 0, len(req.Messages)+1)
	instructionRole := "system"
	if req.Model.Reasoning && compat.supportsDeveloperRole {
		instructionRole = "developer"
	}
	if req.System != "" {
		messages = append(messages, map[string]any{"role": instructionRole, "content": req.System})
	}
	toolIDMap := map[string]string{}
	normalizedIDs := map[string]string{}
	normalizeID := func(id string) (string, error) {
		if id == "" {
			return "", fmt.Errorf("tool call is missing its ID")
		}
		if prior, ok := toolIDMap[id]; ok {
			return prior, nil
		}
		result := id
		if strings.Contains(id, "|") {
			separator := strings.IndexByte(id, '|')
			callID := sanitizeToolID(id[:separator])
			itemID := sanitizeToolID(id[separator+1:])
			combined := callID
			if itemID != "" {
				combined += "_" + itemID
			}
			if len(combined) > 40 {
				hash := piShortHash(id)
				prefixLen := 40 - len(hash) - 1
				if prefixLen < 1 {
					prefixLen = 1
				}
				combined = callID[:min(len(callID), prefixLen)] + "_" + hash
			}
			result = combined
		}
		if prior, exists := normalizedIDs[result]; exists && prior != id {
			return "", fmt.Errorf("tool call IDs collide after normalization")
		}
		normalizedIDs[result] = id
		toolIDMap[id] = result
		return result, nil
	}
	for i := 0; i < len(req.Messages); i++ {
		message := req.Messages[i]
		switch message.Role {
		case "user":
			content, err := completionsUserContent(message.Content, req.Model.InputImage, "(image omitted: model does not support images)")
			if err != nil {
				return nil, fmt.Errorf("user message %d: %w", i, err)
			}
			if content != nil {
				messages = append(messages, map[string]any{"role": "user", "content": content})
			}
		case "assistant":
			if message.StopReason == "error" || message.StopReason == "aborted" {
				continue
			}
			sameModel := message.Provider == req.Model.ProviderID() && message.API == req.Model.API && message.Model == req.Model.ID
			assistant, err := completionsAssistantMessage(message, req.Model, compat, sameModel, normalizeID)
			if err != nil {
				return nil, fmt.Errorf("assistant message %d: %w", i, err)
			}
			if assistant != nil {
				messages = append(messages, assistant)
			}
		case "toolResult":
			var images []any
			j := i
			for ; j < len(req.Messages) && req.Messages[j].Role == "toolResult"; j++ {
				tool := req.Messages[j]
				callID, err := normalizeID(tool.ToolCallID)
				if err != nil {
					return nil, fmt.Errorf("tool result %d: %w", j, err)
				}
				text, hasImage := toolResultText(tool.Content, req.Model.InputImage)
				toolMessage := map[string]any{"role": "tool", "content": text, "tool_call_id": callID}
				if compat.requiresToolResultName && tool.ToolName != "" {
					toolMessage["name"] = tool.ToolName
				}
				messages = append(messages, toolMessage)
				if hasImage && req.Model.InputImage {
					for _, block := range tool.Content {
						if block.Type == "image" {
							images = append(images, completionsImage(block))
						}
					}
				}
			}
			i = j - 1
			if len(images) > 0 {
				if compat.requiresAssistant {
					messages = append(messages, map[string]any{"role": "assistant", "content": "I have processed the tool results."})
				}
				content := []any{map[string]any{"type": "text", "text": "Attached image(s) from tool result:"}}
				content = append(content, images...)
				messages = append(messages, map[string]any{"role": "user", "content": content})
			} else if j < len(req.Messages) && req.Messages[j].Role == "user" && compat.requiresAssistant {
				messages = append(messages, map[string]any{"role": "assistant", "content": "I have processed the tool results."})
			}
		default:
			return nil, fmt.Errorf("unsupported completion message role %q", message.Role)
		}
	}
	return messages, nil
}

func completionsAssistantMessage(message Message, model ModelMeta, compat completionsCompat, sameModel bool, normalizeID func(string) (string, error)) (map[string]any, error) {
	var textParts []string
	var thinking []Block
	var tools []any
	for _, block := range message.Content {
		switch block.Type {
		case "text":
			if strings.TrimSpace(block.Text) != "" {
				textParts = append(textParts, block.Text)
			}
		case "thinking":
			if sameModel && strings.TrimSpace(block.Thinking) != "" && !block.Redacted {
				thinking = append(thinking, block)
			}
		case "toolCall":
			if block.Name == "" {
				return nil, fmt.Errorf("tool call is missing its name")
			}
			id, err := normalizeID(block.ID)
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
			tools = append(tools, map[string]any{"id": id, "type": "function", "function": map[string]any{"name": block.Name, "arguments": string(encoded)}})
		default:
			return nil, fmt.Errorf("unsupported assistant block %q", block.Type)
		}
	}
	content := strings.Join(textParts, "")
	if compat.requiresThinkingAsText && len(thinking) > 0 {
		var reasons []string
		for _, block := range thinking {
			reasons = append(reasons, block.Thinking)
		}
		content = strings.Join(reasons, "\n\n") + func() string {
			if content == "" {
				return ""
			}
			return "\n\n" + content
		}()
		thinking = nil
	}
	assistant := map[string]any{"role": "assistant", "content": nil}
	if content != "" {
		assistant["content"] = content
	}
	if len(tools) > 0 {
		assistant["tool_calls"] = tools
	}
	if len(thinking) > 0 {
		signature := thinking[0].ThinkingSignature
		if model.ProviderID() == "opencode-go" && signature == "reasoning" {
			signature = "reasoning_content"
		}
		switch signature {
		case "reasoning", "reasoning_content", "reasoning_text":
			var parts []string
			for _, block := range thinking {
				parts = append(parts, block.Thinking)
			}
			assistant[signature] = strings.Join(parts, "\n")
		}
	}
	if compat.requiresReasoning && model.Reasoning {
		if _, exists := assistant["reasoning_content"]; !exists {
			assistant["reasoning_content"] = ""
		}
	}
	if assistant["content"] == nil && len(tools) == 0 {
		return nil, nil
	}
	return assistant, nil
}

func completionsUserContent(blocks []Block, supportsImages bool, placeholder string) (any, error) {
	parts := make([]any, 0, len(blocks))
	previousPlaceholder := false
	for _, block := range blocks {
		switch block.Type {
		case "text":
			if block.Text == "" {
				continue
			}
			parts = append(parts, map[string]any{"type": "text", "text": block.Text})
			previousPlaceholder = block.Text == placeholder
		case "image":
			if supportsImages {
				parts = append(parts, completionsImage(block))
				previousPlaceholder = false
			} else if !previousPlaceholder {
				parts = append(parts, map[string]any{"type": "text", "text": placeholder})
				previousPlaceholder = true
			}
		default:
			return nil, fmt.Errorf("unsupported user block %q", block.Type)
		}
	}
	if len(parts) == 0 {
		return nil, nil
	}
	if len(parts) == 1 {
		if text, ok := parts[0].(map[string]any); ok && text["type"] == "text" {
			return text["text"], nil
		}
	}
	return parts, nil
}

func completionsImage(block Block) map[string]any {
	return map[string]any{"type": "image_url", "image_url": map[string]any{"url": "data:" + block.MimeType + ";base64," + block.Data}}
}

func toolResultText(blocks []Block, supportsImages bool) (string, bool) {
	var text []string
	hasImage := false
	previousPlaceholder := false
	for _, block := range blocks {
		if block.Type == "image" {
			if supportsImages {
				hasImage, previousPlaceholder = true, false
			} else if !previousPlaceholder {
				text = append(text, "(tool image omitted: model does not support images)")
				previousPlaceholder = true
			}
		} else if block.Type == "text" {
			text = append(text, block.Text)
			previousPlaceholder = block.Text == "(tool image omitted: model does not support images)"
		}
	}
	joined := strings.Join(text, "\n")
	if strings.TrimSpace(joined) == "" {
		if hasImage {
			return "(see attached image)", true
		}
		return "(no tool output)", false
	}
	return joined, hasImage
}

func sanitizeToolID(id string) string {
	var b strings.Builder
	for _, r := range id {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-' {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
	}
	return b.String()
}

func piShortHash(s string) string {
	const (
		p1 uint32 = 2654435761
		p2 uint32 = 1597334677
		p3 uint32 = 2246822507
		p4 uint32 = 3266489909
	)
	h1, h2 := uint32(0xdeadbeef), uint32(0x41c6ce57)
	for _, unit := range utf16Units(s) {
		h1 = (h1 ^ unit) * p1
		h2 = (h2 ^ unit) * p2
	}
	h1 = (h1^(h1>>16))*p3 ^ (h2^(h2>>13))*p4
	h2 = (h2^(h2>>16))*p3 ^ (h1^(h1>>13))*p4
	return strconv.FormatUint(uint64(h2), 36) + strconv.FormatUint(uint64(h1), 36)
}

func utf16Units(s string) []uint32 {
	units := make([]uint32, 0, len(s))
	for _, r := range s {
		if r <= 0xffff {
			units = append(units, uint32(r))
		} else {
			r -= 0x10000
			units = append(units, uint32(0xd800+(r>>10)), uint32(0xdc00+(r&0x3ff)))
		}
	}
	return units
}
