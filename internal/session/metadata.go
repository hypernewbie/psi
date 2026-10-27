package session

import (
	"bytes"
	"fmt"

	"github.com/segmentio/encoding/json"
)

// Metadata contains no text, arguments, image data, signatures, or tool output.
// The JSON decoder validates and skips those fields without retaining them.
func metadataEntry(data []byte) (*Entry, error) {
	e, header, err := metadataRecord(data)
	if err == nil && header != nil {
		return nil, fmt.Errorf("unexpected session header")
	}
	return e, err
}

func metadataRecord(data []byte) (*Entry, *SessionHeader, error) {
	// Protocol identity uses exact property names (F05): alias-case keys
	// such as "ID" or "ROLE" never populate canonical fields, matching Pi.
	obj, err := exactObject(data)
	if err != nil {
		return nil, nil, err
	}
	recType, _ := exactString(obj, "type")
	recID, _ := exactString(obj, "id")
	if recID == "" {
		return nil, nil, fmt.Errorf("entry missing id")
	}
	if recType == "session" {
		var version *int
		if raw, ok := obj["version"]; ok {
			var v int
			if err := json.Unmarshal(raw, &v); err == nil {
				version = &v
			}
		}
		cwd, _ := exactString(obj, "cwd")
		timestamp, _ := exactString(obj, "timestamp")
		var parent *string
		if ps, ok := exactString(obj, "parentSession"); ok {
			parent = &ps
		}
		return nil, &SessionHeader{Type: recType, Version: version, ID: recID, Timestamp: timestamp, Cwd: cwd, ParentSession: parent}, nil
	}
	if recType == "" {
		return nil, nil, fmt.Errorf("entry missing type")
	}
	// Accept/reject agreement by construction: decode through the same
	// eager path, then project to small metadata (F05/P3-018). Structural
	// strictness can no longer differ between readers.
	full, err := UnmarshalEntry(data)
	if err != nil {
		return nil, nil, err
	}
	return thinFromFull(full), nil, nil
}

func thinStringField(raw json.RawMessage, key string) string {
	if len(raw) == 0 {
		return ""
	}
	obj, err := exactObject(raw)
	if err != nil {
		return ""
	}
	s, _ := exactString(obj, key)
	return s
}

// thinFromFull projects a decoded entry to small index metadata. Payload
// text, reasoning, images, and signatures stay on disk for lazy reads;
// only identity, tool call IDs, and accounting travel in memory.
func thinFromFull(full *Entry) *Entry {
	e := &Entry{Kind: full.Kind, Base: full.Base}
	switch full.Kind {
	case "message":
		m := AgentMessage{Role: full.Message.Message.Role}
		switch m.Role {
		case "user":
			m.User = &UserMessage{Role: m.Role}
		case "system":
			m.System = &SystemMessage{Role: m.Role}
		case "assistant":
			a := full.Message.Message.Assistant
			var calls []ContentBlock
			text := false
			for _, b := range a.Content {
				switch b.Type {
				case "text":
					text = true
				case "toolCall":
					if b.Call != nil {
						calls = append(calls, ContentBlock{Type: "toolCall", Call: &ToolCall{Type: "toolCall", ID: b.Call.ID, Name: b.Call.Name}})
					}
				}
			}
			m.Assistant = &AssistantMessage{Role: m.Role, Provider: a.Provider, Model: a.Model, API: a.API, ResponseModel: a.ResponseModel, Usage: a.Usage, Content: calls}
			if text {
				m.Assistant.Content = append(m.Assistant.Content, ContentBlock{Type: "text", Text: &TextContent{Type: "text"}})
			}
		case "toolResult":
			t := full.Message.Message.ToolResult
			details, _ := json.Marshal(map[string]string{"psiOutcome": thinStringField(t.Details, "psiOutcome")})
			m.ToolResult = &ToolResultMessage{Role: m.Role, ToolCallID: t.ToolCallID, ToolName: t.ToolName, IsError: t.IsError, Details: details}
		case "bashExecution":
			b := full.Message.Message.BashExecution
			m.BashExecution = &BashExecutionMessage{Role: m.Role, Cancelled: b.Cancelled, ExitCode: b.ExitCode}
		case "custom":
			m.Custom = &CustomMessage{Role: m.Role, CustomType: full.Message.Message.Custom.CustomType}
		case "branchSummary":
			m.BranchSummary = &BranchSummaryMessage{Role: m.Role}
		case "compactionSummary":
			m.CompactionSummary = &CompactionSummaryMessage{Role: m.Role}
		}
		e.Message = &SessionMessageEntry{Type: full.Kind, ID: full.ID(), ParentID: full.ParentID(), Timestamp: full.Base.Timestamp, Message: m}
	case "model_change":
		mc := full.ModelChange
		e.ModelChange = &ModelChangeEntry{Provider: mc.Provider, ModelID: mc.ModelID}
	case "thinking_level_change":
		e.ThinkingLevelChange = &ThinkingLevelChangeEntry{ThinkingLevel: full.ThinkingLevelChange.ThinkingLevel}
	case "compaction":
		c := full.Compaction
		e.Compaction = &CompactionEntry{FirstKeptEntryID: c.FirstKeptEntryID, Usage: c.Usage}
	case "context_edit":
		e.ContextEdit = &ContextEditEntry{TargetID: full.ContextEdit.TargetID}
	case "custom":
		e.Custom = &CustomEntry{CustomType: full.Custom.CustomType}
		if full.Custom.CustomType == "psi.input" {
			e.provenanceTarget = thinStringField(full.Custom.Data, "targetId")
		}
	case "custom_message":
		cm := full.CustomMessage
		e.CustomMessage = &CustomMessageEntry{CustomType: cm.CustomType, Display: cm.Display}
		if cm.CustomType == "psi.input" {
			e.provenanceTarget = thinStringField(cm.Details, "targetId")
		}
	case "branch_summary":
		e.BranchSummary = &BranchSummaryEntry{}
	case "usage":
		e.Usage = &UsageEntry{}
		e.Usage.Usage = full.Usage.Usage
	case "label":
		e.Label = &LabelEntry{}
	case "session_info":
		e.SessionInfo = &SessionInfoEntry{Name: full.SessionInfo.Name}
	default:
		return nil
	}
	return e
}

func trimRecord(data []byte) []byte {
	return bytes.TrimSuffix(bytes.TrimSuffix(data, []byte("\n")), []byte("\r"))
}
