package provider

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf16"
)

var (
	googleGemini3ThinkingID = regexp.MustCompile(`gemini-3(?:\.\d+)?-(?:pro|flash)`)
	googleGeminiLaterID     = regexp.MustCompile(`gemini-(?:[4-9]|[1-9][0-9])`)
	googleGeminiMajorID     = regexp.MustCompile(`^gemini(?:-live)?-([0-9]+)`)
	googleSignatureBase64   = regexp.MustCompile(`^[A-Za-z0-9+/]+={0,2}$`)
)

var googleSchemaNameMaps = map[string]bool{
	"properties": true, "patternProperties": true, "$defs": true,
	"definitions": true, "dependentSchemas": true, "dependencies": true,
}

var googleSchemaSubschemas = map[string]bool{
	"additionalProperties": true, "unevaluatedProperties": true, "propertyNames": true,
	"items": true, "prefixItems": true, "additionalItems": true, "contains": true,
	"allOf": true, "anyOf": true, "oneOf": true, "not": true, "if": true,
	"then": true, "else": true, "contentSchema": true,
}

// BuildGoogleRequest creates the Google Generative AI REST request. Its wire
// schema is distinct from Anthropic and OpenAI even when the same tools exist.
func BuildGoogleRequest(req Request) (map[string]any, error) {
	if req.Model.API != "google-generative-ai" || req.Model.ID == "" {
		return nil, fmt.Errorf("model and google-generative-ai API are required")
	}
	if err := validateProviderAPI(req.Model, "google-generative-ai"); err != nil {
		return nil, err
	}
	for field := range req.Model.Compat {
		return nil, fmt.Errorf("model %q uses unsupported google-generative-ai compatibility field %q", req.Model.ID, field)
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
	if req.Thinking != "off" {
		level, ok := clampGoogleThinkingLevel(req.Model, req.Thinking)
		if !ok {
			return nil, fmt.Errorf("model %q has no supported Google thinking level for %q", req.Model.ID, req.Thinking)
		}
		req.Thinking = level
	}

	contents, err := convertGoogleMessages(req)
	if err != nil {
		return nil, err
	}
	maxTokens := req.MaxTokens
	if maxTokens <= 0 {
		maxTokens = req.Model.MaxTokens
	}
	if maxTokens <= 0 {
		maxTokens = 16384
	}
	generationConfig := map[string]any{"maxOutputTokens": maxTokens}
	body := map[string]any{"contents": contents, "generationConfig": generationConfig}
	if req.System != "" {
		body["systemInstruction"] = map[string]any{"role": "user", "parts": []any{map[string]any{"text": req.System}}}
	}
	if len(req.Tools) > 0 {
		declarations := make([]any, 0, len(req.Tools))
		for _, tool := range req.Tools {
			if tool.Name == "" {
				return nil, fmt.Errorf("Google function declaration is missing its name")
			}
			schema := tool.Schema
			if schema == nil {
				schema = map[string]any{"type": "object", "properties": map[string]any{}}
			}
			declarations = append(declarations, map[string]any{
				"name": tool.Name, "description": tool.Description,
				"parametersJsonSchema": rewriteGoogleConstAsEnum(schema, false),
			})
		}
		body["tools"] = []any{map[string]any{"functionDeclarations": declarations}}
	}
	if req.Model.Reasoning {
		if req.Thinking == "off" {
			thinking, err := googleDisabledThinkingConfig(req.Model)
			if err != nil {
				return nil, err
			}
			generationConfig["thinkingConfig"] = thinking
		} else {
			level, err := resolveGoogleThinkingLevel(req.Model, req.Thinking)
			if err != nil {
				return nil, err
			}
			thinking := map[string]any{"includeThoughts": true}
			if googleUsesThinkingLevel(req.Model.ID) {
				thinking["thinkingLevel"] = strings.ToUpper(level)
			} else {
				thinking["thinkingBudget"] = googleThinkingBudget(req.Model.ID, level)
			}
			generationConfig["thinkingConfig"] = thinking
		}
	}
	return body, nil
}

func googleUsesThinkingLevel(modelID string) bool {
	id := strings.ToLower(modelID)
	return googleGemini3ThinkingID.MatchString(id) || googleGeminiLaterID.MatchString(id) ||
		id == "gemini-flash-latest" || id == "gemini-flash-lite-latest" ||
		strings.Contains(id, "gemma-4") || strings.Contains(id, "gemma4")
}

func resolveGoogleThinkingLevel(model ModelMeta, level string) (string, error) {
	resolved := level
	if mapped, exists := model.ThinkingLevelMap[level]; exists {
		if mapped == nil {
			return "", fmt.Errorf("model %q does not support thinking level %q", model.ID, level)
		}
		resolved = strings.ToLower(*mapped)
	}
	switch resolved {
	case "minimal", "low", "medium", "high":
		return resolved, nil
	default:
		var mapping string
		if mapped := model.ThinkingLevelMap[level]; mapped != nil {
			mapping = *mapped
		}
		return "", fmt.Errorf("unsupported Google thinking level mapping for %s/%s: %s -> %s", model.ProviderID(), model.ID, level, mapping)
	}
}

func googleDisabledThinkingConfig(model ModelMeta) (map[string]any, error) {
	if !googleUsesThinkingLevel(model.ID) {
		return map[string]any{"thinkingBudget": 0}, nil
	}
	level, ok := clampGoogleThinkingLevel(model, "off")
	if !ok {
		return nil, fmt.Errorf("model %q has unsupported Google disabled-thinking fallback", model.ID)
	}
	if level == "off" {
		return map[string]any{"thinkingBudget": 0}, nil
	}
	resolved, err := resolveGoogleThinkingLevel(model, level)
	if err != nil {
		return nil, err
	}
	return map[string]any{"thinkingLevel": strings.ToUpper(resolved)}, nil
}

func clampGoogleThinkingLevel(model ModelMeta, requested string) (string, bool) {
	return clampModelThinkingLevel(model, requested)
}

func googleThinkingBudget(modelID, level string) int {
	budgets := map[string]map[string]int{
		"2.5-pro":        {"minimal": 128, "low": 2048, "medium": 8192, "high": 32768},
		"2.5-flash-lite": {"minimal": 512, "low": 2048, "medium": 8192, "high": 24576},
		"2.5-flash":      {"minimal": 128, "low": 2048, "medium": 8192, "high": 24576},
	}
	id := strings.ToLower(modelID)
	for _, family := range []string{"2.5-pro", "2.5-flash-lite", "2.5-flash"} {
		if strings.Contains(id, family) {
			if value, ok := budgets[family][level]; ok {
				return value
			}
			return -1
		}
	}
	return -1
}

func googleRequiresToolCallID(modelID string) bool {
	if strings.HasPrefix(modelID, "claude-") || strings.HasPrefix(modelID, "gpt-oss-") {
		return true
	}
	match := googleGeminiMajorID.FindStringSubmatch(strings.ToLower(modelID))
	if len(match) != 2 {
		return false
	}
	major, err := strconv.Atoi(match[1])
	return err == nil && major >= 3
}

func normalizeGoogleToolID(id string) string {
	units := utf16.Encode([]rune(id))
	var b strings.Builder
	for _, unit := range units {
		if b.Len() == 64 {
			break
		}
		if unit >= 'a' && unit <= 'z' || unit >= 'A' && unit <= 'Z' || unit >= '0' && unit <= '9' || unit == '_' || unit == '-' {
			b.WriteByte(byte(unit))
		} else {
			b.WriteByte('_')
		}
	}
	return b.String()
}

func googleSignatureValid(signature string) bool {
	return signature != "" && len(signature)%4 == 0 && googleSignatureBase64.MatchString(signature)
}

func rewriteGoogleConstAsEnum(schema any, nameMap bool) any {
	if list, ok := schema.([]any); ok {
		out := make([]any, len(list))
		for i, value := range list {
			out[i] = rewriteGoogleConstAsEnum(value, false)
		}
		return out
	}
	object, ok := schema.(map[string]any)
	if !ok {
		return schema
	}
	out := make(map[string]any, len(object)+1)
	for key, value := range object {
		switch {
		case nameMap || googleSchemaSubschemas[key]:
			out[key] = rewriteGoogleConstAsEnum(value, false)
		case googleSchemaNameMaps[key]:
			out[key] = rewriteGoogleConstAsEnum(value, true)
		default:
			out[key] = value
		}
	}
	if !nameMap {
		if constant, exists := out["const"]; exists {
			if _, hasEnum := out["enum"]; !hasEnum {
				switch constant.(type) {
				case string, json.Number, float32, float64, int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64:
					delete(out, "const")
					out["enum"] = []any{constant}
				}
			}
		}
	}
	return out
}

type googlePendingCall struct {
	id, normalizedID, name string
}

func convertGoogleMessages(req Request) ([]any, error) {
	contents := make([]any, 0, len(req.Messages))
	toolIDs := make(map[string]string)
	normalizedIDs := make(map[string]string)
	allCallIDs := make(map[string]bool)
	pending := make(map[string]googlePendingCall)
	var pendingOrder []string
	includeIDs := googleRequiresToolCallID(req.Model.ID)
	multimodalResults := googleSupportsMultimodalFunctionResponse(req.Model.ID)

	appendResult := func(call googlePendingCall, name string, blocks []Block, isError bool) error {
		if name == "" || name != call.name {
			return fmt.Errorf("Google function response name does not match its call")
		}
		text, images, err := googleToolResultContent(blocks, req.Model.InputImage)
		if err != nil {
			return err
		}
		responseText := ""
		if strings.TrimSpace(text) != "" {
			responseText = text
		} else if len(images) > 0 {
			responseText = "(see attached image)"
		}
		value := map[string]any{"output": responseText}
		if isError {
			value = map[string]any{"error": responseText}
		}
		functionResponse := map[string]any{"name": name, "response": value}
		if len(images) > 0 && multimodalResults {
			functionResponse["parts"] = images
		}
		if includeIDs {
			functionResponse["id"] = call.normalizedID
		}
		part := map[string]any{"functionResponse": functionResponse}
		last, _ := contentsLastFunctionResponse(contents)
		if last != nil {
			last["parts"] = append(last["parts"].([]any), part)
		} else {
			contents = append(contents, map[string]any{"role": "user", "parts": []any{part}})
		}
		if len(images) > 0 && !multimodalResults {
			parts := []any{map[string]any{"text": "Tool result image:"}}
			parts = append(parts, images...)
			contents = append(contents, map[string]any{"role": "user", "parts": parts})
		}
		return nil
	}

	flushPending := func() error {
		for _, id := range pendingOrder {
			call, ok := pending[id]
			if !ok {
				continue
			}
			if err := appendResult(call, call.name, []Block{{Type: "text", Text: "No result provided"}}, true); err != nil {
				return err
			}
			delete(pending, id)
		}
		pendingOrder = nil
		return nil
	}

	for index, message := range req.Messages {
		switch message.Role {
		case "user":
			if err := flushPending(); err != nil {
				return nil, err
			}
			parts, err := googleUserParts(message.Content, req.Model.InputImage)
			if err != nil {
				return nil, fmt.Errorf("user message %d: %w", index, err)
			}
			if len(parts) > 0 {
				contents = append(contents, map[string]any{"role": "user", "parts": parts})
			}
		case "assistant":
			if err := flushPending(); err != nil {
				return nil, err
			}
			if message.StopReason == "error" || message.StopReason == "aborted" {
				continue
			}
			sameModel := message.Provider == req.Model.ProviderID() && message.API == req.Model.API && message.Model == req.Model.ID
			parts := make([]any, 0, len(message.Content))
			for _, block := range message.Content {
				switch block.Type {
				case "text":
					part := map[string]any{"text": block.Text}
					if sameModel && googleSignatureValid(block.TextSignature) {
						part["thoughtSignature"] = block.TextSignature
					}
					if strings.TrimSpace(block.Text) == "" && part["thoughtSignature"] == nil {
						continue
					}
					parts = append(parts, part)
				case "thinking":
					if sameModel {
						part := map[string]any{"text": block.Thinking, "thought": true}
						if googleSignatureValid(block.ThinkingSignature) {
							part["thoughtSignature"] = block.ThinkingSignature
						}
						if strings.TrimSpace(block.Thinking) == "" && part["thoughtSignature"] == nil {
							continue
						}
						parts = append(parts, part)
					}
				case "toolCall":
					if block.ID == "" || block.Name == "" || allCallIDs[block.ID] {
						return nil, fmt.Errorf("assistant message %d has an invalid or duplicate Google function call", index)
					}
					allCallIDs[block.ID] = true
					normalized := block.ID
					if includeIDs {
						normalized = normalizeGoogleToolID(block.ID)
						if normalized == "" {
							return nil, fmt.Errorf("Google function call ID is empty after normalization")
						}
						if previous, collision := normalizedIDs[normalized]; collision && previous != block.ID {
							return nil, fmt.Errorf("Google function call IDs collide after normalization")
						}
						normalizedIDs[normalized] = block.ID
					}
					toolIDs[block.ID] = normalized
					functionCall := map[string]any{"name": block.Name, "args": block.Arguments}
					if block.Arguments == nil {
						functionCall["args"] = map[string]any{}
					}
					if includeIDs {
						functionCall["id"] = normalized
					}
					part := map[string]any{"functionCall": functionCall}
					if sameModel && googleSignatureValid(block.ThoughtSignature) {
						part["thoughtSignature"] = block.ThoughtSignature
					}
					parts = append(parts, part)
					pending[block.ID] = googlePendingCall{id: block.ID, normalizedID: normalized, name: block.Name}
					pendingOrder = append(pendingOrder, block.ID)
				default:
					return nil, fmt.Errorf("unsupported Google assistant block %q", block.Type)
				}
			}
			if len(parts) > 0 {
				contents = append(contents, map[string]any{"role": "model", "parts": parts})
			}
		case "toolResult":
			call, ok := pending[message.ToolCallID]
			if !ok {
				return nil, fmt.Errorf("Google function response has no matching function call")
			}
			if mapped, exists := toolIDs[message.ToolCallID]; exists {
				call.normalizedID = mapped
			}
			if err := appendResult(call, message.ToolName, message.Content, message.IsError); err != nil {
				return nil, fmt.Errorf("tool result %d: %w", index, err)
			}
			delete(pending, message.ToolCallID)
		default:
			return nil, fmt.Errorf("unsupported Google message role %q", message.Role)
		}
	}
	if err := flushPending(); err != nil {
		return nil, err
	}
	return contents, nil
}

func googleUserParts(blocks []Block, supportsImages bool) ([]any, error) {
	parts := make([]any, 0, len(blocks))
	previousPlaceholder := false
	for _, block := range blocks {
		switch block.Type {
		case "text":
			parts = append(parts, map[string]any{"text": block.Text})
			previousPlaceholder = block.Text == "(image omitted: model does not support images)"
		case "image":
			if supportsImages {
				parts = append(parts, googleInlineData(block))
				previousPlaceholder = false
			} else if !previousPlaceholder {
				parts = append(parts, map[string]any{"text": "(image omitted: model does not support images)"})
				previousPlaceholder = true
			}
		default:
			return nil, fmt.Errorf("unsupported Google user block %q", block.Type)
		}
	}
	return parts, nil
}

func googleToolResultContent(blocks []Block, supportsImages bool) (string, []any, error) {
	var text []string
	var images []any
	previousPlaceholder := false
	for _, block := range blocks {
		switch block.Type {
		case "text":
			text = append(text, block.Text)
			previousPlaceholder = block.Text == "(tool image omitted: model does not support images)"
		case "image":
			if supportsImages {
				images = append(images, googleInlineData(block))
				previousPlaceholder = false
			} else if !previousPlaceholder {
				text = append(text, "(tool image omitted: model does not support images)")
				previousPlaceholder = true
			}
		default:
			return "", nil, fmt.Errorf("unsupported Google tool-result block %q", block.Type)
		}
	}
	return strings.Join(text, "\n"), images, nil
}

func googleInlineData(block Block) map[string]any {
	return map[string]any{"inlineData": map[string]any{"mimeType": block.MimeType, "data": block.Data}}
}

func googleSupportsMultimodalFunctionResponse(modelID string) bool {
	match := googleGeminiMajorID.FindStringSubmatch(strings.ToLower(modelID))
	if len(match) != 2 {
		return true
	}
	major, err := strconv.Atoi(match[1])
	return err != nil || major >= 3
}

func contentsLastFunctionResponse(contents []any) (map[string]any, bool) {
	if len(contents) == 0 {
		return nil, false
	}
	last, ok := contents[len(contents)-1].(map[string]any)
	if !ok || last["role"] != "user" {
		return nil, false
	}
	parts, ok := last["parts"].([]any)
	if !ok || len(parts) == 0 {
		return nil, false
	}
	for _, part := range parts {
		p, ok := part.(map[string]any)
		if !ok {
			return nil, false
		}
		if _, ok := p["functionResponse"]; !ok {
			return nil, false
		}
	}
	return last, true
}
