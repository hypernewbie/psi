package session

import (
	"encoding/json"
	"fmt"
	"sort"
)

// UserBlocks also handles generic JSON arrays from imported custom messages.
// Generic content is read with exact property names, like Pi's typed access:
// an alias-case key never populates a block field.
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
	items, ok := content.([]any)
	if !ok {
		return nil, fmt.Errorf("invalid message content: expected text or an array of blocks")
	}
	out := make([]UserContentBlock, 0, len(items))
	for _, item := range items {
		obj, ok := item.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("invalid message content: expected text or an array of blocks")
		}
		block := UserContentBlock{}
		blockType, _ := obj["type"].(string)
		block.Type = blockType
		switch blockType {
		case "text":
			text, ok := obj["text"].(string)
			if !ok {
				if _, present := obj["text"]; present {
					return nil, fmt.Errorf("invalid message content: text block has no text")
				}
			}
			block.Text = &TextContent{Type: "text", Text: text}
		case "image":
			data, dataOK := obj["data"].(string)
			mime, mimeOK := obj["mimeType"].(string)
			if !dataOK || !mimeOK {
				return nil, fmt.Errorf("invalid message content: image block is incomplete")
			}
			block.Image = &ImageContent{Type: "image", Data: data, MimeType: mime}
		}
		out = append(out, block)
	}
	return out, nil
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
	// SystemMessage is also nested in compaction records: read the protocol
	// fields with exact names, like Pi's typed property access.
	obj, err := splitObject(raw)
	if err != nil {
		return err
	}
	var v systemAlias
	if err := decodeExactObject(obj, &v); err != nil {
		return err
	}
	*s = SystemMessage(v)
	s.Raw = append([]byte(nil), raw...)
	return nil
}

// sectionRank fixes the render order of known sections independently of JSON
// object key order.
var sectionRank = map[string]int{
	"preamble":        0,
	"tools":           1,
	"rules":           2,
	"docs":            3,
	"addendum":        4,
	"project_context": 5,
	"skills":          6,
	"cwd":             7,
	"loop":            8,
}

// OrderedSectionNames lists section names in prompt order: ranked names first,
// then unknown names in lexical order. Tombstones are included; callers skip
// nil values when rendering.
func OrderedSectionNames[V any](sections map[string]V) []string {
	names := make([]string, 0, len(sections))
	for name := range sections {
		names = append(names, name)
	}
	sort.Slice(names, func(i, j int) bool {
		ri, iRanked := sectionRank[names[i]]
		rj, jRanked := sectionRank[names[j]]
		if iRanked != jRanked {
			return iRanked
		}
		if iRanked {
			return ri < rj
		}
		return names[i] < names[j]
	})
	return names
}
