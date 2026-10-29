package provider

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strings"
)

// BuildCodexRequest uses ChatGPT's subscription Responses endpoint, not the
// separately billed OpenAI API. store:false requires replaying opaque reasoning.
func BuildCodexRequest(req Request) (map[string]any, error) {
	if req.Model.API != "openai-codex-responses" || req.Model.ID == "" {
		return nil, fmt.Errorf("Codex model and API are required")
	}
	if err := validateProviderAPI(req.Model, "openai-codex-responses"); err != nil {
		return nil, err
	}
	level := req.Thinking
	if level == "" {
		level = "off"
	}
	if _, err := ThinkingBudget(level); err != nil {
		return nil, err
	}
	if level != "off" && !req.Model.Reasoning {
		return nil, fmt.Errorf("model %q does not support thinking", req.Model.ID)
	}
	input := []any{}
	for i, m := range req.Messages {
		sameAPI := (m.Provider == "" || m.Provider == req.Model.ProviderID()) && (m.API == "" || m.API == req.Model.API)
		sameModel := sameAPI && (m.Model == "" || m.Model == req.Model.ID)
		switch m.Role {
		case "user":
			content, err := codexInputContent(m.Content, req.Model.InputImage)
			if err != nil {
				return nil, err
			}
			input = append(input, map[string]any{"role": "user", "content": content})
		case "assistant":
			for j, b := range m.Content {
				switch b.Type {
				case "thinking":
					if sameModel && b.ThinkingSignature != "" {
						var item struct {
							Type string `json:"type"`
						}
						if err := json.Unmarshal([]byte(b.ThinkingSignature), &item); err != nil || item.Type != "reasoning" {
							return nil, fmt.Errorf("invalid Codex reasoning replay metadata")
						}
						input = append(input, json.RawMessage(b.ThinkingSignature))
					} else if b.Thinking != "" && !b.Redacted {
						input = append(input, codexAssistantText(b.Thinking, fmt.Sprintf("msg_psi_%d_%d", i, j), ""))
					}
				case "text":
					id, phase := fmt.Sprintf("msg_psi_%d_%d", i, j), ""
					if sameAPI && b.TextSignature != "" {
						var sig struct {
							V     int    `json:"v"`
							ID    string `json:"id"`
							Phase string `json:"phase"`
						}
						if strings.HasPrefix(b.TextSignature, "{") {
							if err := json.Unmarshal([]byte(b.TextSignature), &sig); err != nil || sig.V != 1 || sig.ID == "" {
								return nil, fmt.Errorf("invalid Codex text replay metadata")
							}
							id, phase = sig.ID, sig.Phase
						} else {
							id = b.TextSignature
						}
					}
					input = append(input, codexAssistantText(b.Text, codexID(id), phase))
				case "toolCall":
					parts := strings.SplitN(b.ID, "|", 2)
					if parts[0] == "" || b.Name == "" {
						return nil, fmt.Errorf("invalid Codex tool call")
					}
					args := b.Arguments
					if args == nil {
						args = map[string]any{}
					}
					encoded, err := json.Marshal(args)
					if err != nil {
						return nil, fmt.Errorf("invalid tool arguments")
					}
					item := map[string]any{"type": "function_call", "call_id": codexID(parts[0]), "name": b.Name, "arguments": string(encoded)}
					if sameModel && len(parts) == 2 && strings.HasPrefix(parts[1], "fc_") {
						item["id"] = codexID(parts[1])
					}
					if sameModel && b.Namespace != "" {
						item["namespace"] = b.Namespace
					}
					input = append(input, item)
				default:
					return nil, fmt.Errorf("unsupported Codex assistant block %q", b.Type)
				}
			}
		case "toolResult":
			if m.ToolCallID == "" {
				return nil, fmt.Errorf("tool result is missing its call ID")
			}
			content, err := codexInputContent(m.Content, req.Model.InputImage)
			if err != nil {
				return nil, err
			}
			var output any = content
			var text []string
			hasImage := false
			for _, b := range m.Content {
				if b.Type == "image" {
					hasImage = true
				} else {
					text = append(text, b.Text)
				}
			}
			if !hasImage {
				output = strings.Join(text, "\n")
			}
			input = append(input, map[string]any{"type": "function_call_output", "call_id": codexID(strings.SplitN(m.ToolCallID, "|", 2)[0]), "output": output})
		default:
			return nil, fmt.Errorf("unsupported Codex message role %q", m.Role)
		}
	}
	instructions := req.System
	if instructions == "" {
		instructions = "You are a helpful assistant."
	}
	out := map[string]any{"model": req.Model.ID, "store": false, "stream": true, "instructions": instructions, "input": input,
		"include": []string{"reasoning.encrypted_content"}, "text": map[string]any{"verbosity": "low"}, "tool_choice": "auto", "parallel_tool_calls": true}
	if req.Model.Reasoning {
		effort := level
		if level == "off" {
			effort = "none"
		}
		if mapped, ok := req.Model.ThinkingLevelMap[level]; ok {
			if mapped == nil {
				if level != "off" {
					return nil, fmt.Errorf("model %q does not support thinking level %q", req.Model.ID, level)
				}
				effort = ""
			} else {
				effort = *mapped
			}
		} else if level == "xhigh" || level == "max" {
			return nil, fmt.Errorf("model %q needs a thinkingLevelMap entry for %q", req.Model.ID, level)
		}
		if effort != "" {
			out["reasoning"] = map[string]any{"effort": effort, "summary": "auto"}
		}
	}
	if len(req.Tools) > 0 {
		var tools []any
		for _, t := range req.Tools {
			schema := t.Schema
			if schema == nil {
				schema = map[string]any{"type": "object", "properties": map[string]any{}}
			}
			tools = append(tools, map[string]any{"type": "function", "name": t.Name, "description": t.Description, "parameters": schema, "strict": false})
		}
		out["tools"] = tools
	}
	return out, nil
}

func codexAssistantText(text, id, phase string) map[string]any {
	m := map[string]any{"type": "message", "role": "assistant", "status": "completed", "id": id,
		"content": []any{map[string]any{"type": "output_text", "text": text, "annotations": []any{}}}}
	if phase != "" {
		m["phase"] = phase
	}
	return m
}

func codexInputContent(blocks []Block, images bool) ([]any, error) {
	out := []any{}
	for _, b := range blocks {
		switch b.Type {
		case "text":
			out = append(out, map[string]any{"type": "input_text", "text": b.Text})
		case "image":
			if !images {
				return nil, fmt.Errorf("selected model is not configured for image input")
			}
			switch b.MimeType {
			case "image/png", "image/jpeg", "image/gif", "image/webp":
			default:
				return nil, fmt.Errorf("unsupported Codex image MIME type")
			}
			out = append(out, map[string]any{"type": "input_image", "detail": "auto", "image_url": "data:" + b.MimeType + ";base64," + b.Data})
		default:
			return nil, fmt.Errorf("unsupported Codex input block %q", b.Type)
		}
	}
	return out, nil
}

func codexID(id string) string {
	valid := len(id) > 0 && len(id) <= 64
	for _, r := range id {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-') {
			valid = false
			break
		}
	}
	if valid {
		return id
	}
	return fmt.Sprintf("psi_%x", sha256.Sum256([]byte(id)))[:64]
}
