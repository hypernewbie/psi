package session

import (
	"encoding/json"
	"fmt"
)

// FileEntry is either a SessionHeader or an Entry.
type FileEntry struct {
	IsHeader bool
	Header   *SessionHeader
	Entry    *Entry
}

// UnmarshalFileEntry decodes one JSONL record.
func UnmarshalFileEntry(data []byte) (*FileEntry, error) {
	obj, err := exactObject(data)
	if err != nil {
		return nil, err
	}
	recType, _ := exactString(obj, "type")
	if recType == "session" {
		var h SessionHeader
		if err := json.Unmarshal(data, &h); err != nil {
			return nil, err
		}
		if h.ID == "" {
			return nil, fmt.Errorf("session header missing id")
		}
		return &FileEntry{IsHeader: true, Header: &h}, nil
	}
	e, err := UnmarshalEntry(data)
	if err != nil {
		return nil, err
	}
	return &FileEntry{Entry: e}, nil
}

// UnmarshalEntry decodes a non-header entry, preserving raw bytes.
func UnmarshalEntry(data []byte) (*Entry, error) {
	obj, err := exactObject(data)
	if err != nil {
		return nil, err
	}
	recType, _ := exactString(obj, "type")
	recID, _ := exactString(obj, "id")
	if recType == "" {
		return nil, fmt.Errorf("entry missing type")
	}
	if recType == "session" {
		return nil, fmt.Errorf("unexpected session header")
	}
	if recID == "" {
		return nil, fmt.Errorf("entry missing id")
	}
	var parentID *string
	if ps, ok := exactString(obj, "parentId"); ok {
		parentID = &ps
	}
	var timestamp string
	timestamp, _ = exactString(obj, "timestamp")
	raw := append(json.RawMessage(nil), data...)
	e := &Entry{Kind: recType, Base: entryBase{
		Type: recType, ID: recID, ParentID: parentID, Timestamp: timestamp,
	}, Raw: raw}
	switch recType {
	case "message":
		var v SessionMessageEntry
		if err := json.Unmarshal(data, &v); err != nil {
			return nil, err
		}
		e.Message = &v
	case "thinking_level_change":
		var v ThinkingLevelChangeEntry
		if err := json.Unmarshal(data, &v); err != nil {
			return nil, err
		}
		e.ThinkingLevelChange = &v
	case "model_change":
		var v ModelChangeEntry
		if err := json.Unmarshal(data, &v); err != nil {
			return nil, err
		}
		e.ModelChange = &v
	case "usage":
		var v UsageEntry
		if err := json.Unmarshal(data, &v); err != nil {
			return nil, err
		}
		e.Usage = &v
	case "compaction":
		var v CompactionEntry
		if err := json.Unmarshal(data, &v); err != nil {
			return nil, err
		}
		e.Compaction = &v
	case "branch_summary":
		var v BranchSummaryEntry
		if err := json.Unmarshal(data, &v); err != nil {
			return nil, err
		}
		e.BranchSummary = &v
	case "custom":
		var v CustomEntry
		if err := json.Unmarshal(data, &v); err != nil {
			return nil, err
		}
		e.Custom = &v
	case "custom_message":
		var v CustomMessageEntry
		if err := json.Unmarshal(data, &v); err != nil {
			return nil, err
		}
		e.CustomMessage = &v
	case "context_edit":
		var v ContextEditEntry
		if err := json.Unmarshal(data, &v); err != nil {
			return nil, err
		}
		e.ContextEdit = &v
	case "label":
		var v LabelEntry
		if err := json.Unmarshal(data, &v); err != nil {
			return nil, err
		}
		e.Label = &v
	case "session_info":
		var v SessionInfoEntry
		if err := json.Unmarshal(data, &v); err != nil {
			return nil, err
		}
		e.SessionInfo = &v
	default:
		return nil, fmt.Errorf("unknown entry type %q", recType)
	}
	return e, nil
}

// ID returns the entry id.
func (e *Entry) ID() string { return e.Base.ID }

// ParentID returns the parent id (nil for roots).
func (e *Entry) ParentID() *string { return e.Base.ParentID }

// MarshalJSON emits the original raw bytes when present (preserves unknown fields).
func (e *Entry) MarshalJSON() ([]byte, error) {
	if len(e.Raw) > 0 {
		return e.Raw, nil
	}
	switch e.Kind {
	case "message":
		return json.Marshal(e.Message)
	case "thinking_level_change":
		return json.Marshal(e.ThinkingLevelChange)
	case "model_change":
		return json.Marshal(e.ModelChange)
	case "usage":
		return json.Marshal(e.Usage)
	case "compaction":
		return json.Marshal(e.Compaction)
	case "branch_summary":
		return json.Marshal(e.BranchSummary)
	case "custom":
		return json.Marshal(e.Custom)
	case "custom_message":
		return json.Marshal(e.CustomMessage)
	case "context_edit":
		return json.Marshal(e.ContextEdit)
	case "label":
		return json.Marshal(e.Label)
	case "session_info":
		return json.Marshal(e.SessionInfo)
	}
	return nil, fmt.Errorf("unknown entry kind %q", e.Kind)
}

// MarshalEntryForAppend marshals a freshly built entry (no raw) deterministically.
func MarshalEntryForAppend(v any) ([]byte, error) {
	return json.Marshal(v)
}
