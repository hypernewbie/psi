package provider

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf16"
)

// BuildResponsesRequest builds the generic OpenAI Responses API request. It
// does not implement the ChatGPT Codex subscription protocol.
func BuildResponsesRequest(req Request) (map[string]any, error) {
	if req.Model.API != "openai-responses" || req.Model.ID == "" {
		return nil, fmt.Errorf("model and openai-responses API are required")
	}
	if err := validateProviderAPI(req.Model, "openai-responses"); err != nil {
		return nil, err
	}
	compat, err := resolveResponsesCompat(req.Model)
	if err != nil {
		return nil, err
	}
	if err := validateResponsesTools(req); err != nil {
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
	input, err := convertResponsesMessages(req, compat)
	if err != nil {
		return nil, err
	}
	body := map[string]any{"model": req.Model.ID, "input": input, "stream": true, "store": false}
	if req.MaxTokens > 0 && compat.supportsMaxOutputTokens {
		body["max_output_tokens"] = max(16, req.MaxTokens)
	}
	if !compat.supportsMidConvoSystemMessages || !compat.supportsAdditionalTools {
		if tools := responseTools(req.Tools, compat.supportsStrictMode); len(tools) > 0 {
			body["tools"] = tools
		}
	}
	if req.Model.Reasoning {
		level := clampResponsesThinking(req.Model, req.Thinking)
		if level == "off" {
			mapped, present := req.Model.ThinkingLevelMap["off"]
			if !present {
				body["reasoning"] = map[string]any{"effort": "none"}
			} else if mapped != nil {
				body["reasoning"] = map[string]any{"effort": *mapped}
			}
		} else {
			if mapped, present := req.Model.ThinkingLevelMap[level]; present {
				if mapped == nil {
					return nil, fmt.Errorf("model %q does not support thinking level %q", req.Model.ID, level)
				}
				level = *mapped
			}
			body["reasoning"] = map[string]any{"effort": level, "summary": "auto"}
			body["include"] = []string{"reasoning.encrypted_content"}
		}
	}
	return body, nil
}

type responsesCompat struct {
	supportsDeveloperRole          bool
	supportsMidConvoSystemMessages bool
	supportsAdditionalTools        bool
	supportsMaxOutputTokens        bool
	supportsStrictMode             bool
	sessionAffinityFormat          string
}

func resolveResponsesCompat(model ModelMeta) (responsesCompat, error) {
	compat := responsesCompat{supportsDeveloperRole: true, supportsMaxOutputTokens: true, sessionAffinityFormat: "openai"}
	for field := range model.Compat {
		switch field {
		case "supportsDeveloperRole", "supportsMidConvoSystemMessages", "supportsAdditionalTools", "supportsMaxOutputTokens", "supportsStrictMode", "sessionAffinityFormat":
		default:
			return responsesCompat{}, fmt.Errorf("model %q uses unsupported openai-responses compatibility field %q", model.ID, field)
		}
	}
	for field, destination := range map[string]*bool{
		"supportsDeveloperRole":          &compat.supportsDeveloperRole,
		"supportsMidConvoSystemMessages": &compat.supportsMidConvoSystemMessages,
		"supportsAdditionalTools":        &compat.supportsAdditionalTools,
		"supportsMaxOutputTokens":        &compat.supportsMaxOutputTokens,
		"supportsStrictMode":             &compat.supportsStrictMode,
	} {
		if model.Compat.IsNull(field) {
			continue
		}
		if value, hasValue, present := model.Compat.Bool(field); present {
			if !hasValue {
				return responsesCompat{}, fmt.Errorf("model %q has invalid openai-responses compatibility field %q", model.ID, field)
			}
			*destination = value
		}
	}
	if !model.Compat.IsNull("sessionAffinityFormat") {
		if value, hasValue, present := model.Compat.String("sessionAffinityFormat"); present {
			if !hasValue {
				return responsesCompat{}, fmt.Errorf("model %q has invalid openai-responses compatibility field %q", model.ID, "sessionAffinityFormat")
			}
			compat.sessionAffinityFormat = value
		}
	}
	switch compat.sessionAffinityFormat {
	case "openai", "openai-nosession", "openrouter":
	default:
		return responsesCompat{}, fmt.Errorf("model %q uses unsupported Responses session affinity format %q", model.ID, compat.sessionAffinityFormat)
	}
	return compat, nil
}

func clampResponsesThinking(model ModelMeta, requested string) string {
	levels := []string{"off", "minimal", "low", "medium", "high", "xhigh", "max"}
	if !model.Reasoning {
		return "off"
	}
	available := make(map[string]bool, len(levels))
	for _, level := range levels {
		mapped, exists := model.ThinkingLevelMap[level]
		if exists && mapped == nil {
			continue
		}
		if (level == "xhigh" || level == "max") && !exists {
			continue
		}
		available[level] = true
	}
	for index, level := range levels {
		if level != requested {
			continue
		}
		for i := index; i < len(levels); i++ {
			if available[levels[i]] {
				return levels[i]
			}
		}
		for i := index - 1; i >= 0; i-- {
			if available[levels[i]] {
				return levels[i]
			}
		}
	}
	for _, level := range levels {
		if available[level] {
			return level
		}
	}
	return "off"
}

func validateResponsesTools(req Request) error {
	validate := func(tools []ToolDef) error {
		for _, tool := range tools {
			if tool.Name == "" || tool.Schema == nil {
				return fmt.Errorf("invalid OpenAI Responses tool definition")
			}
		}
		return nil
	}
	if err := validate(req.Tools); err != nil {
		return err
	}
	for _, message := range req.Messages {
		if err := validate(message.ToolsAdded); err != nil {
			return err
		}
	}
	return nil
}

func responseTools(tools []ToolDef, supportsStrictMode bool) []any {
	out := make([]any, 0, len(tools))
	for _, tool := range tools {
		declaration := map[string]any{"type": "function", "name": tool.Name, "description": tool.Description}
		declaration["parameters"] = tool.Schema
		if supportsStrictMode {
			declaration["strict"] = false
		}
		out = append(out, declaration)
	}
	return out
}

func convertResponsesMessages(req Request, compat responsesCompat) ([]any, error) {
	messages := transformResponsesMessages(req.Messages, req.Model)
	input := make([]any, 0, len(messages)+1)
	instructionRole := "system"
	if req.Model.Reasoning && compat.supportsDeveloperRole {
		instructionRole = "developer"
	}
	hasSystem := false
	for _, message := range messages {
		if message.Role == "system" {
			hasSystem = true
			break
		}
	}
	changesToolsInPlace := compat.supportsMidConvoSystemMessages && compat.supportsAdditionalTools
	if !compat.supportsMidConvoSystemMessages || !hasSystem {
		if changesToolsInPlace && !hasSystem && len(req.Tools) > 0 {
			input = append(input, map[string]any{"type": "additional_tools", "role": "developer", "tools": responseTools(req.Tools, compat.supportsStrictMode)})
		}
		if req.System != "" {
			input = append(input, map[string]any{"role": instructionRole, "content": req.System})
		}
	}
	declaredAdditionalTools := changesToolsInPlace && !hasSystem && len(req.Tools) > 0
	unavailable := responsesUnavailableTools{set: make(map[string]bool)}
	msgIndex := 0
	for _, message := range messages {
		leadingSystem := msgIndex == 0 && message.Role == "system"
		switch message.Role {
		case "system":
			if !compat.supportsMidConvoSystemMessages {
				continue
			}
			if changesToolsInPlace {
				for _, tool := range message.ToolsAdded {
					unavailable.remove(tool.Name)
				}
				if len(message.ToolsAdded) > 0 {
					if declaredAdditionalTools {
						input = append(input, map[string]any{"role": instructionRole, "content": responsesIncrementalToolNotice(unavailable.list())})
					}
					declaredAdditionalTools = true
					input = append(input, map[string]any{"type": "additional_tools", "role": "developer", "tools": responseTools(message.ToolsAdded, compat.supportsStrictMode)})
				}
			}
			var parts []string
			if text := responsesSystemMessageText(message, leadingSystem); text != "" {
				parts = append(parts, text)
			}
			if changesToolsInPlace && len(message.ToolsRemoved) > 0 {
				added := make(map[string]bool, len(message.ToolsAdded))
				for _, tool := range message.ToolsAdded {
					added[tool.Name] = true
				}
				var removed []string
				for _, name := range message.ToolsRemoved {
					if name == "" || added[name] {
						continue
					}
					removed = append(removed, name)
					unavailable.add(name)
				}
				if len(removed) > 0 {
					parts = append(parts, responsesToolRemovalNotice(removed))
				}
			}
			if len(parts) > 0 {
				input = append(input, map[string]any{"role": instructionRole, "content": strings.Join(parts, "\n\n")})
			}
		case "user":
			content := make([]any, 0, len(message.Content))
			for _, block := range message.Content {
				switch block.Type {
				case "text":
					content = append(content, map[string]any{"type": "input_text", "text": block.Text})
				case "image":
					if !req.Model.InputImage {
						content = append(content, map[string]any{"type": "input_text", "text": "(image omitted: model does not support images)"})
					} else {
						content = append(content, map[string]any{"type": "input_image", "detail": "auto", "image_url": responsesImageURL(block)})
					}
				default:
					return nil, fmt.Errorf("unsupported OpenAI Responses user block %q", block.Type)
				}
			}
			if len(content) == 0 {
				continue
			}
			input = append(input, map[string]any{"role": "user", "content": content})
		case "assistant":
			inputStart := len(input)
			textBlockIndex := 0
			sameModel := message.Provider == req.Model.ProviderID() && message.API == req.Model.API && message.Model == req.Model.ID
			for _, block := range message.Content {
				switch block.Type {
				case "thinking":
					if !sameModel || block.ThinkingSignature == "" {
						continue
					}
					var signature struct {
						Type string `json:"type"`
					}
					if json.Unmarshal([]byte(block.ThinkingSignature), &signature) != nil || signature.Type != "reasoning" || !json.Valid([]byte(block.ThinkingSignature)) {
						return nil, fmt.Errorf("invalid OpenAI Responses reasoning replay metadata")
					}
					input = append(input, json.RawMessage(block.ThinkingSignature))
				case "text":
					id, phase := responsesTextID(block.TextSignature)
					if !sameModel {
						id, phase = "", ""
					}
					if id == "" {
						if textBlockIndex == 0 {
							id = fmt.Sprintf("msg_pi_%d", msgIndex)
						} else {
							id = fmt.Sprintf("msg_pi_%d_%d", msgIndex, textBlockIndex)
						}
					}
					textBlockIndex++
					if len(utf16.Encode([]rune(id))) > 64 {
						id = "msg_" + responsesShortHash(id)
					}
					item := map[string]any{"type": "message", "role": "assistant", "content": []any{map[string]any{"type": "output_text", "text": block.Text, "annotations": []any{}}}, "status": "completed", "id": id}
					if phase != "" {
						item["phase"] = phase
					}
					input = append(input, item)
				case "toolCall":
					callID, itemID := splitResponsesToolID(block.ID)
					if !sameModel {
						callID = normalizeResponsesID(callID)
					}
					if callID == "" || block.Name == "" || block.Arguments == nil {
						return nil, fmt.Errorf("invalid OpenAI Responses tool call")
					}
					if !sameModel || !strings.HasPrefix(itemID, "fc_") {
						itemID = ""
					}
					arguments, err := json.Marshal(block.Arguments)
					if err != nil {
						return nil, fmt.Errorf("invalid OpenAI Responses tool arguments")
					}
					item := map[string]any{"type": "function_call", "call_id": callID, "name": block.Name, "arguments": string(arguments)}
					if itemID != "" {
						item["id"] = itemID
					}
					if sameModel && block.Namespace != "" {
						item["namespace"] = block.Namespace
					}
					input = append(input, item)
				default:
					return nil, fmt.Errorf("unsupported OpenAI Responses assistant block %q", block.Type)
				}
			}
			if len(input) == inputStart {
				continue
			}
		case "toolResult":
			callID, _ := splitResponsesToolID(message.ToolCallID)
			if callID == "" {
				return nil, fmt.Errorf("OpenAI Responses tool result is missing its call ID")
			}
			output, err := responsesToolOutput(message.Content, req.Model.InputImage)
			if err != nil {
				return nil, err
			}
			input = append(input, map[string]any{"type": "function_call_output", "call_id": callID, "output": output})
		default:
			return nil, fmt.Errorf("unsupported OpenAI Responses message role %q", message.Role)
		}
		if !leadingSystem {
			msgIndex++
		}
	}
	return input, nil
}

func transformResponsesMessages(messages []Message, model ModelMeta) []Message {
	idMap := make(map[string]string)
	var output []Message
	var pending []Message
	var heldSystems []Message
	answered := make(map[string]bool)
	closePending := func() {
		for _, message := range pending {
			for _, block := range message.Content {
				if block.Type == "toolCall" && !answered[block.ID] {
					output = append(output, Message{Role: "toolResult", ToolCallID: block.ID, ToolName: block.Name, IsError: true, Content: []Block{{Type: "text", Text: "No result provided"}}})
				}
			}
		}
		pending = nil
		answered = make(map[string]bool)
		output = append(output, heldSystems...)
		heldSystems = nil
	}
	for _, original := range messages {
		message := original
		switch message.Role {
		case "system":
			if len(pending) > 0 {
				heldSystems = append(heldSystems, message)
			} else {
				output = append(output, message)
			}
		case "user":
			closePending()
			message.Content = downgradeResponsesImages(message.Content, false, model.InputImage)
			output = append(output, message)
		case "assistant":
			closePending()
			if message.StopReason == "error" || message.StopReason == "aborted" {
				continue
			}
			sameModel := message.Provider == model.ProviderID() && message.API == model.API && message.Model == model.ID
			blocks := make([]Block, 0, len(message.Content))
			for _, block := range message.Content {
				switch block.Type {
				case "text":
					if !sameModel {
						block.TextSignature = ""
					}
					blocks = append(blocks, block)
				case "thinking":
					if sameModel {
						blocks = append(blocks, block)
					}
				case "toolCall":
					if !sameModel {
						id := normalizeResponsesID(block.ID)
						idMap[block.ID] = id
						block.ID, block.ThoughtSignature = id, ""
					}
					blocks = append(blocks, block)
				}
			}
			message.Content = blocks
			output = append(output, message)
			var toolCalls []Block
			for _, block := range blocks {
				if block.Type == "toolCall" {
					toolCalls = append(toolCalls, block)
				}
			}
			if len(toolCalls) > 0 {
				pending = []Message{{Role: "assistant", Content: toolCalls}}
			}
		case "toolResult":
			if normalized, ok := idMap[message.ToolCallID]; ok {
				message.ToolCallID = normalized
			}
			answered[message.ToolCallID] = true
			message.Content = downgradeResponsesImages(message.Content, true, model.InputImage)
			output = append(output, message)
		default:
			output = append(output, message)
		}
	}
	closePending()
	return output
}

func downgradeResponsesImages(blocks []Block, toolResult, supportsImages bool) []Block {
	if supportsImages {
		return blocks
	}
	placeholder := "(image omitted: model does not support images)"
	if toolResult {
		placeholder = "(tool image omitted: model does not support images)"
	}
	var output []Block
	previousPlaceholder := false
	for _, block := range blocks {
		if block.Type == "image" {
			if !previousPlaceholder {
				output = append(output, Block{Type: "text", Text: placeholder})
				previousPlaceholder = true
			}
			continue
		}
		output = append(output, block)
		previousPlaceholder = block.Type == "text" && block.Text == placeholder
	}
	return output
}

func responsesSystemMessageText(message Message, leading bool) string {
	var parts []string
	var content []string
	for _, block := range message.Content {
		if block.Type == "text" {
			content = append(content, block.Text)
		}
	}
	text := strings.Join(content, "\n")
	if text != "" {
		parts = append(parts, text)
	}
	for _, section := range message.Sections {
		if leading {
			if section.Value != nil && *section.Value != "" {
				parts = append(parts, *section.Value)
			}
		} else if section.Value == nil {
			parts = append(parts, fmt.Sprintf("Removed system prompt section %q.", section.Name))
		} else {
			parts = append(parts, fmt.Sprintf("Updated system prompt section %q:\n\n%s", section.Name, *section.Value))
		}
	}
	return strings.Join(parts, "\n\n")
}

type responsesUnavailableTools struct {
	order []string
	set   map[string]bool
}

func (u *responsesUnavailableTools) add(name string) {
	if name == "" || u.set[name] {
		return
	}
	u.set[name] = true
	u.order = append(u.order, name)
}

func (u *responsesUnavailableTools) remove(name string) {
	if !u.set[name] {
		return
	}
	delete(u.set, name)
	for i, current := range u.order {
		if current == name {
			u.order = append(u.order[:i], u.order[i+1:]...)
			break
		}
	}
}

func (u responsesUnavailableTools) list() []string { return append([]string(nil), u.order...) }

func responsesIncrementalToolNotice(unavailable []string) string {
	const notice = "This is an incremental tools update. Previously declared tools remain available unless explicitly marked unavailable. A tool declared again here is available again, and its latest definition replaces the earlier one."
	if len(unavailable) == 0 {
		return notice
	}
	return notice + "\nStill unavailable: " + strings.Join(unavailable, ", ") + "."
}

func responsesToolRemovalNotice(names []string) string {
	lines := make([]string, 0, len(names)+1)
	lines = append(lines, "The following tools are no longer available. Do not call them:")
	for _, name := range names {
		lines = append(lines, "- "+name)
	}
	return strings.Join(lines, "\n")
}

func responsesImageURL(block Block) string { return "data:" + block.MimeType + ";base64," + block.Data }

func responsesToolOutput(blocks []Block, supportsImages bool) (any, error) {
	var text []string
	var images []any
	for _, block := range blocks {
		switch block.Type {
		case "text":
			text = append(text, block.Text)
		case "image":
			if supportsImages {
				images = append(images, map[string]any{"type": "input_image", "detail": "auto", "image_url": responsesImageURL(block)})
			}
		default:
			return nil, fmt.Errorf("unsupported OpenAI Responses tool-result block %q", block.Type)
		}
	}
	joined := strings.Join(text, "\n")
	if len(images) == 0 {
		if strings.TrimSpace(joined) != "" {
			return joined, nil
		}
		if blocksContainImage(blocks) {
			return "(see attached image)", nil
		}
		return "(no tool output)", nil
	}
	var content []any
	if strings.TrimSpace(joined) != "" {
		content = append(content, map[string]any{"type": "input_text", "text": joined})
	}
	content = append(content, images...)
	return content, nil
}

func blocksContainImage(blocks []Block) bool {
	for _, block := range blocks {
		if block.Type == "image" {
			return true
		}
	}
	return false
}

func responsesTextID(signature string) (string, string) {
	if signature == "" {
		return "", ""
	}
	if strings.HasPrefix(signature, "{") {
		var parsed struct {
			Version int    `json:"v"`
			ID      string `json:"id"`
			Phase   string `json:"phase"`
		}
		if json.Unmarshal([]byte(signature), &parsed) == nil && parsed.Version == 1 && parsed.ID != "" {
			if parsed.Phase == "commentary" || parsed.Phase == "final_answer" {
				return parsed.ID, parsed.Phase
			}
			return parsed.ID, ""
		}
	}
	return signature, ""
}

func splitResponsesToolID(id string) (string, string) {
	parts := strings.SplitN(id, "|", 3)
	if len(parts) < 2 {
		return id, ""
	}
	return parts[0], parts[1]
}

func normalizeResponsesID(id string) string {
	var builder strings.Builder
	for _, unit := range utf16.Encode([]rune(id)) {
		if builder.Len() == 64 {
			break
		}
		if unit >= 'a' && unit <= 'z' || unit >= 'A' && unit <= 'Z' || unit >= '0' && unit <= '9' || unit == '_' || unit == '-' {
			builder.WriteByte(byte(unit))
		} else {
			builder.WriteByte('_')
		}
	}
	return strings.TrimRight(builder.String(), "_")
}

func responsesShortHash(value string) string {
	var h1, h2 uint32 = 0xdeadbeef, 0x41c6ce57
	for _, unit := range utf16.Encode([]rune(value)) {
		h1 = (h1 ^ uint32(unit)) * 2654435761
		h2 = (h2 ^ uint32(unit)) * 1597334677
	}
	first := (h1^(h1>>16))*2246822507 ^ (h2^(h2>>13))*3266489909
	second := (h2^(h2>>16))*2246822507 ^ (first^(first>>13))*3266489909
	return strconv.FormatUint(uint64(second), 36) + strconv.FormatUint(uint64(first), 36)
}
