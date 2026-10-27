package session

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
)

// UserBlocks also handles generic JSON arrays from imported custom messages.
func UserBlocks(content any) ([]UserContentBlock, error) {
	if content == nil {
		return nil, nil
	}
	if s, ok := content.(string); ok {
		return []UserContentBlock{{Type: "text", Text: &TextContent{Type: "text", Text: s}}}, nil
	}
	if blocks, ok := content.([]UserContentBlock); ok {
		return blocks, nil
	}
	raw, err := json.Marshal(content)
	if err != nil {
		return nil, err
	}
	var blocks []UserContentBlock
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return nil, fmt.Errorf("invalid message content: expected text or an array of blocks")
	}
	return blocks, nil
}

func checkContent(m AgentMessage) error {
	var content any
	switch m.Role {
	case "system":
		if m.System == nil {
			return fmt.Errorf("system message missing payload")
		}
		content = m.System.Content
	case "user":
		if m.User == nil {
			return fmt.Errorf("user message missing payload")
		}
		content = m.User.Content
	case "custom":
		if m.Custom == nil {
			return fmt.Errorf("custom message missing payload")
		}
		content = m.Custom.Content
	case "toolResult":
		if m.ToolResult == nil {
			return fmt.Errorf("tool result missing payload")
		}
		content = m.ToolResult.Content
	case "assistant":
		if m.Assistant == nil {
			return fmt.Errorf("assistant message missing payload")
		}
		if m.Assistant.StopReason == "deferred" || m.Assistant.StopReason == "pending" {
			return fmt.Errorf("unfinished/deferred assistant response: refuse continuation")
		}
		for _, b := range m.Assistant.Content {
			switch {
			case b.Type == "text" && b.Text != nil:
			case b.Type == "thinking" && b.Thinking != nil:
			case b.Type == "toolCall" && b.Call != nil:
			default:
				return fmt.Errorf("unsupported assistant content block %q: refuse lossy continuation", b.Type)
			}
		}
		return nil
	default:
		return nil
	}
	blocks, err := UserBlocks(content)
	if err != nil {
		return err
	}
	for _, b := range blocks {
		if b.Type == "text" && b.Text != nil {
			continue
		}
		if b.Type == "image" && b.Image != nil && m.Role != "system" {
			continue
		}
		return fmt.Errorf("unsupported %s content block %q: refuse lossy continuation", m.Role, b.Type)
	}
	return nil
}

type systemAlias SystemMessage

func (s SystemMessage) MarshalJSON() ([]byte, error) {
	if len(s.Raw) > 0 {
		return s.Raw, nil
	}
	return json.Marshal(systemAlias(s))
}

func (s *SystemMessage) UnmarshalJSON(raw []byte) error {
	var v systemAlias
	if err := json.Unmarshal(raw, &v); err != nil {
		return err
	}
	*s = SystemMessage(v)
	s.Raw = append([]byte(nil), raw...)
	return nil
}

func sectionOrder(s *SystemMessage) []string {
	var raw map[string]json.RawMessage
	if json.Unmarshal(s.Raw, &raw) == nil && raw["sections"] != nil {
		dec := json.NewDecoder(bytes.NewReader(raw["sections"]))
		if token, err := dec.Token(); err == nil && token == json.Delim('{') {
			var keys []string
			for dec.More() {
				token, err := dec.Token()
				if err != nil {
					break
				}
				key, ok := token.(string)
				if !ok {
					break
				}
				var value json.RawMessage
				if dec.Decode(&value) != nil {
					break
				}
				keys = append(keys, key)
			}
			return keys
		}
	}
	var keys []string
	for key := range s.Sections {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func systemRaw(s *SystemMessage, order []string) json.RawMessage {
	raw, _ := json.Marshal(systemAlias(*s))
	var fields map[string]json.RawMessage
	_ = json.Unmarshal(raw, &fields)
	var b bytes.Buffer
	b.WriteByte('{')
	for i, key := range order {
		if i > 0 {
			b.WriteByte(',')
		}
		k, _ := json.Marshal(key)
		v, _ := json.Marshal(s.Sections[key])
		b.Write(k)
		b.WriteByte(':')
		b.Write(v)
	}
	b.WriteByte('}')
	fields["sections"] = b.Bytes()
	out, _ := json.Marshal(fields)
	return out
}
