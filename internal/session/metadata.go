package session

import (
	"bytes"
	"fmt"

	"github.com/segmentio/encoding/json"
)

// Metadata contains no text, arguments, image data, signatures, or tool output.
// The JSON decoder validates and skips those fields without retaining them.
type contentMetadata struct {
	Calls []ContentBlock
	Text  bool
}

func (c *contentMetadata) UnmarshalJSON(data []byte) error {
	if len(data) == 0 || data[0] != '[' {
		c.Text = len(data) > 0 && data[0] == '"'
		return nil
	}
	var blocks []struct {
		Type string `json:"type"`
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	if err := json.Unmarshal(data, &blocks); err != nil {
		return err
	}
	for _, b := range blocks {
		if b.Type == "text" {
			c.Text = true
		}
		if b.Type == "toolCall" {
			c.Calls = append(c.Calls, ContentBlock{Type: "toolCall", Call: &ToolCall{Type: "toolCall", ID: b.ID, Name: b.Name}})
		}
	}
	return nil
}

type objectMetadata struct {
	TargetID string `json:"targetId"`
	Outcome  string `json:"psiOutcome"`
}

func (m *objectMetadata) UnmarshalJSON(data []byte) error {
	if len(data) > 0 && data[0] == '{' {
		type value objectMetadata
		var p value
		if json.Unmarshal(data, &p) == nil {
			*m = objectMetadata(p)
		}
	}
	return nil
}

func metadataEntry(data []byte) (*Entry, error) {
	e, header, err := metadataRecord(data)
	if err == nil && header != nil {
		return nil, fmt.Errorf("unexpected session header")
	}
	return e, err
}

func metadataRecord(data []byte) (*Entry, *SessionHeader, error) {
	var p struct {
		Version       *int           `json:"version"`
		Cwd           string         `json:"cwd"`
		ParentSession *string        `json:"parentSession"`
		Type          string         `json:"type"`
		ID            string         `json:"id"`
		ParentID      *string        `json:"parentId"`
		Timestamp     string         `json:"timestamp"`
		Provider      string         `json:"provider"`
		ModelID       string         `json:"modelId"`
		Thinking      string         `json:"thinkingLevel"`
		FirstKept     string         `json:"firstKeptEntryId"`
		CustomType    string         `json:"customType"`
		TargetID      string         `json:"targetId"`
		Display       bool           `json:"display"`
		Name          *string        `json:"name"`
		Data          objectMetadata `json:"data"`
		Details       objectMetadata `json:"details"`
		Usage         *Usage         `json:"usage"`
		Message       struct {
			Role          string          `json:"role"`
			Provider      string          `json:"provider"`
			Model         string          `json:"model"`
			API           string          `json:"api"`
			ResponseModel *string         `json:"responseModel"`
			Usage         Usage           `json:"usage"`
			Content       contentMetadata `json:"content"`
			CallID        string          `json:"toolCallId"`
			ToolName      string          `json:"toolName"`
			IsError       bool            `json:"isError"`
			Cancelled     bool            `json:"cancelled"`
			ExitCode      *int            `json:"exitCode"`
			Details       objectMetadata  `json:"details"`
		} `json:"message"`
	}
	if err := json.Unmarshal(data, &p); err != nil {
		return nil, nil, err
	}
	if p.ID == "" {
		return nil, nil, fmt.Errorf("entry missing id")
	}
	if p.Type == "session" {
		return nil, &SessionHeader{Type: p.Type, Version: p.Version, ID: p.ID, Timestamp: p.Timestamp, Cwd: p.Cwd, ParentSession: p.ParentSession}, nil
	}
	e := &Entry{Kind: p.Type, Base: entryBase{Type: p.Type, ID: p.ID, ParentID: p.ParentID, Timestamp: p.Timestamp}}
	switch p.Type {
	case "message":
		m := AgentMessage{Role: p.Message.Role}
		switch m.Role {
		case "user":
			m.User = &UserMessage{Role: m.Role}
		case "system":
			m.System = &SystemMessage{Role: m.Role}
		case "assistant":
			m.Assistant = &AssistantMessage{Role: m.Role, Provider: p.Message.Provider, Model: p.Message.Model, API: p.Message.API, ResponseModel: p.Message.ResponseModel, Usage: p.Message.Usage, Content: p.Message.Content.Calls}
			if p.Message.Content.Text {
				m.Assistant.Content = append(m.Assistant.Content, ContentBlock{Type: "text", Text: &TextContent{Type: "text"}})
			}
		case "toolResult":
			details, _ := json.Marshal(map[string]string{"psiOutcome": p.Message.Details.Outcome})
			m.ToolResult = &ToolResultMessage{Role: m.Role, ToolCallID: p.Message.CallID, ToolName: p.Message.ToolName, IsError: p.Message.IsError, Details: details}
		case "bashExecution":
			m.BashExecution = &BashExecutionMessage{Role: m.Role, Cancelled: p.Message.Cancelled, ExitCode: p.Message.ExitCode}
		}
		e.Message = &SessionMessageEntry{Type: p.Type, ID: p.ID, ParentID: p.ParentID, Timestamp: p.Timestamp, Message: m}
	case "model_change":
		e.ModelChange = &ModelChangeEntry{Provider: p.Provider, ModelID: p.ModelID}
	case "thinking_level_change":
		e.ThinkingLevelChange = &ThinkingLevelChangeEntry{ThinkingLevel: p.Thinking}
	case "compaction":
		e.Compaction = &CompactionEntry{FirstKeptEntryID: p.FirstKept, Usage: p.Usage}
	case "context_edit":
		e.ContextEdit = &ContextEditEntry{TargetID: p.TargetID}
	case "custom":
		e.Custom = &CustomEntry{CustomType: p.CustomType}
		if p.CustomType == "psi.input" {
			e.provenanceTarget = p.Data.TargetID
		}
	case "custom_message":
		e.CustomMessage = &CustomMessageEntry{CustomType: p.CustomType, Display: p.Display}
		if p.CustomType == "psi.input" {
			e.provenanceTarget = p.Details.TargetID
		}
	case "branch_summary":
		e.BranchSummary = &BranchSummaryEntry{}
	case "usage":
		e.Usage = &UsageEntry{}
		if p.Usage != nil {
			e.Usage.Usage = *p.Usage
		}
	case "label":
		e.Label = &LabelEntry{}
	case "session_info":
		e.SessionInfo = &SessionInfoEntry{Name: p.Name}
	default:
		return nil, nil, fmt.Errorf("unknown entry type %q", p.Type)
	}
	return e, nil, nil
}

func trimRecord(data []byte) []byte {
	return bytes.TrimSuffix(bytes.TrimSuffix(data, []byte("\n")), []byte("\r"))
}
