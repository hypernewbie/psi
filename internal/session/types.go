// Package session implements Pi-compatible v3 session storage and context projection.
//
// Reference: temp/pi-mono/packages/coding-agent/src/core/session-manager.ts
// and messages.ts at pinned revision d5b3de2. Session version 3 only.
// v1/v2 are diagnosed and refused; future versions are refused.
package session

import (
	"encoding/json"
	"fmt"
)

// CurrentSessionVersion matches Pi CURRENT_SESSION_VERSION at the pinned revision.
const CurrentSessionVersion = 3

// MaxRecordBytes bounds a single JSONL record. Pi streams with a 1 MiB buffer
// and must handle image-bearing records; Go's scanner default (64 KiB) is too
// small, so we read explicitly. 32 MiB is a deliberate bound: larger records
// are refused with an actionable error instead of unbounded memory growth.
const MaxRecordBytes = 32 * 1024 * 1024

// ---- Content blocks (packages/ai/src/types.ts) ----

type TextContent struct {
	Type          string  `json:"type"`
	Text          string  `json:"text"`
	TextSignature *string `json:"textSignature,omitempty"`
}

type ThinkingContent struct {
	Type              string  `json:"type"`
	Thinking          string  `json:"thinking"`
	ThinkingSignature *string `json:"thinkingSignature,omitempty"`
	Redacted          *bool   `json:"redacted,omitempty"`
}

type ImageContent struct {
	Type     string `json:"type"`
	Data     string `json:"data"`
	MimeType string `json:"mimeType"`
}

type ToolCall struct {
	Type             string         `json:"type"`
	ID               string         `json:"id"`
	Name             string         `json:"name"`
	Arguments        map[string]any `json:"arguments"`
	ThoughtSignature *string        `json:"thoughtSignature,omitempty"`
	Namespace        *string        `json:"namespace,omitempty"`
	ArgumentsError   *string        `json:"argumentsError,omitempty"`
}

// ContentBlock is a discriminated union holder for assistant content.
// We keep raw JSON for blocks psi does not interpret so signatures survive.
type ContentBlock struct {
	Raw json.RawMessage
	// Decoded view (one of TextContent, ThinkingContent, ToolCall).
	Type string
	Text *TextContent
	// Thinking content (opaque replay data preserved).
	Thinking *ThinkingContent
	Call     *ToolCall
}

func (b ContentBlock) MarshalJSON() ([]byte, error) {
	if len(b.Raw) > 0 {
		return b.Raw, nil
	}
	switch b.Type {
	case "text":
		if b.Text != nil {
			return json.Marshal(b.Text)
		}
	case "thinking":
		if b.Thinking != nil {
			return json.Marshal(b.Thinking)
		}
	case "toolCall":
		if b.Call != nil {
			return json.Marshal(b.Call)
		}
	}
	return nil, fmt.Errorf("empty content block")
}

func (b *ContentBlock) UnmarshalJSON(data []byte) error {
	b.Raw = append(b.Raw[:0], data...)
	var probe struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(data, &probe); err != nil {
		return err
	}
	b.Type = probe.Type
	switch probe.Type {
	case "text":
		var t TextContent
		if err := json.Unmarshal(data, &t); err != nil {
			return err
		}
		b.Text = &t
	case "thinking":
		var t ThinkingContent
		if err := json.Unmarshal(data, &t); err != nil {
			return err
		}
		b.Thinking = &t
	case "toolCall":
		var t ToolCall
		if err := json.Unmarshal(data, &t); err != nil {
			return err
		}
		b.Call = &t
	default:
		// Unknown block: keep raw only.
	}
	return nil
}

// UserContentBlock holds text or image blocks for user messages and tool results.
type UserContentBlock struct {
	Raw   json.RawMessage
	Type  string
	Text  *TextContent
	Image *ImageContent
}

func (b UserContentBlock) MarshalJSON() ([]byte, error) {
	if len(b.Raw) > 0 {
		return b.Raw, nil
	}
	switch b.Type {
	case "text":
		if b.Text != nil {
			return json.Marshal(b.Text)
		}
	case "image":
		if b.Image != nil {
			return json.Marshal(b.Image)
		}
	}
	return nil, fmt.Errorf("empty user content block")
}

func (b *UserContentBlock) UnmarshalJSON(data []byte) error {
	b.Raw = append(b.Raw[:0], data...)
	var probe struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(data, &probe); err != nil {
		return err
	}
	b.Type = probe.Type
	switch probe.Type {
	case "text":
		var t TextContent
		if err := json.Unmarshal(data, &t); err != nil {
			return err
		}
		b.Text = &t
	case "image":
		var im ImageContent
		if err := json.Unmarshal(data, &im); err != nil {
			return err
		}
		b.Image = &im
	}
	return nil
}

// ---- Message types ----

type UsageCost struct {
	Input             float64 `json:"input"`
	Output            float64 `json:"output"`
	CacheRead         float64 `json:"cacheRead"`
	CacheWrite        float64 `json:"cacheWrite"`
	Total             float64 `json:"total"`
	TotalFromProvider *bool   `json:"totalFromProvider,omitempty"`
}

type Usage struct {
	Input        float64   `json:"input"`
	Output       float64   `json:"output"`
	CacheRead    float64   `json:"cacheRead"`
	CacheWrite   float64   `json:"cacheWrite"`
	CacheWrite1h *float64  `json:"cacheWrite1h,omitempty"`
	Reasoning    *float64  `json:"reasoning,omitempty"`
	TotalTokens  float64   `json:"totalTokens"`
	Cost         UsageCost `json:"cost"`
}

// Tool is a minimal declaration (name + description + JSON schema as raw).
// The provider adapter translates these; psi never executes unknown tools.
type Tool struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
}

type ToolReference struct {
	Name      *string `json:"name,omitempty"`
	Namespace *string `json:"namespace,omitempty"`
}

type SystemMessage struct {
	Role         string             `json:"role"`
	Content      any                `json:"content"` // string or []TextContent (kept generic, decoded on demand)
	Sections     map[string]*string `json:"sections,omitempty"`
	ToolsAdded   []Tool             `json:"toolsAdded,omitempty"`
	ToolsRemoved []ToolReference    `json:"toolsRemoved,omitempty"`
	Timestamp    int64              `json:"timestamp"`
	// Raw preserves unknown fields.
	Raw json.RawMessage `json:"-"`
}

type UserMessage struct {
	Role      string          `json:"role"`
	Content   any             `json:"content"` // string or []UserContentBlock
	Timestamp int64           `json:"timestamp"`
	Raw       json.RawMessage `json:"-"`
}

type AssistantMessage struct {
	Role                  string         `json:"role"`
	Content               []ContentBlock `json:"content"`
	API                   string         `json:"api"`
	Provider              string         `json:"provider"`
	Model                 string         `json:"model"`
	ResponseModel         *string        `json:"responseModel,omitempty"`
	ResponseID            *string        `json:"responseId,omitempty"`
	ProviderThinkingLevel *string        `json:"providerThinkingLevel,omitempty"`
	ThinkingLevel         *string        `json:"thinkingLevel,omitempty"`
	Usage                 Usage          `json:"usage"`
	StopReason            string         `json:"stopReason"`
	ErrorMessage          *string        `json:"errorMessage,omitempty"`
	RawStopReason         *string        `json:"rawStopReason,omitempty"`
	EndTurn               *bool          `json:"endTurn,omitempty"`
	Timestamp             int64          `json:"timestamp"`
	DurationMs            *float64       `json:"durationMs,omitempty"`
	// Opaque extras (diagnostics, deferred, signatures at message level).
	Raw json.RawMessage `json:"-"`
}

type ToolResultMessage struct {
	Role       string             `json:"role"`
	ToolCallID string             `json:"toolCallId"`
	ToolName   string             `json:"toolName"`
	Content    []UserContentBlock `json:"content"`
	Details    json.RawMessage    `json:"details,omitempty"`
	Usage      *Usage             `json:"usage,omitempty"`
	IsError    bool               `json:"isError"`
	Timestamp  int64              `json:"timestamp"`
	DurationMs *float64           `json:"durationMs,omitempty"`
	Raw        json.RawMessage    `json:"-"`
}

// BashExecutionMessage is a historical custom agent message (role bashExecution).
type BashExecutionMessage struct {
	Role               string          `json:"role"`
	Command            string          `json:"command"`
	Output             string          `json:"output"`
	ExitCode           *int            `json:"exitCode"`
	Cancelled          bool            `json:"cancelled"`
	Truncated          bool            `json:"truncated"`
	FullOutputPath     *string         `json:"fullOutputPath,omitempty"`
	Timestamp          int64           `json:"timestamp"`
	ExcludeFromContext *bool           `json:"excludeFromContext,omitempty"`
	Raw                json.RawMessage `json:"-"`
}

// CustomMessage is an extension-injected message (role custom).
type CustomMessage struct {
	Role       string          `json:"role"`
	CustomType string          `json:"customType"`
	Content    any             `json:"content"` // string or []blocks
	Display    bool            `json:"display"`
	Details    json.RawMessage `json:"details,omitempty"`
	Timestamp  int64           `json:"timestamp"`
	Raw        json.RawMessage `json:"-"`
}

type BranchSummaryMessage struct {
	Role      string  `json:"role"`
	Summary   string  `json:"summary"`
	FromID    *string `json:"fromId"`
	Timestamp int64   `json:"timestamp"`
}

type CompactionSummaryMessage struct {
	Role         string  `json:"role"`
	Summary      string  `json:"summary"`
	TokensBefore float64 `json:"tokensBefore"`
	Timestamp    int64   `json:"timestamp"`
}

// AgentMessage is the union; Role discriminates.
type AgentMessage struct {
	Role string
	// Exactly one of these is set.
	System            *SystemMessage
	User              *UserMessage
	Assistant         *AssistantMessage
	ToolResult        *ToolResultMessage
	BashExecution     *BashExecutionMessage
	Custom            *CustomMessage
	BranchSummary     *BranchSummaryMessage
	CompactionSummary *CompactionSummaryMessage
	// Raw record for unknown roles (preserved, never executed).
	Raw json.RawMessage
}

// MarshalJSON emits the concrete message.
func (m AgentMessage) MarshalJSON() ([]byte, error) {
	switch {
	case m.System != nil:
		if len(m.System.Raw) > 0 {
			return m.System.Raw, nil
		}
		return json.Marshal(m.System)
	case m.User != nil:
		if len(m.User.Raw) > 0 {
			return m.User.Raw, nil
		}
		return json.Marshal(m.User)
	case m.Assistant != nil:
		if len(m.Assistant.Raw) > 0 {
			return m.Assistant.Raw, nil
		}
		return json.Marshal(m.Assistant)
	case m.ToolResult != nil:
		if len(m.ToolResult.Raw) > 0 {
			return m.ToolResult.Raw, nil
		}
		return json.Marshal(m.ToolResult)
	case m.BashExecution != nil:
		if len(m.BashExecution.Raw) > 0 {
			return m.BashExecution.Raw, nil
		}
		return json.Marshal(m.BashExecution)
	case m.Custom != nil:
		if len(m.Custom.Raw) > 0 {
			return m.Custom.Raw, nil
		}
		return json.Marshal(m.Custom)
	case m.BranchSummary != nil:
		return json.Marshal(m.BranchSummary)
	case m.CompactionSummary != nil:
		return json.Marshal(m.CompactionSummary)
	default:
		if len(m.Raw) > 0 {
			return m.Raw, nil
		}
		return nil, fmt.Errorf("empty agent message")
	}
}

func (m *AgentMessage) UnmarshalJSON(data []byte) error {
	var probe struct {
		Role string `json:"role"`
	}
	if err := json.Unmarshal(data, &probe); err != nil {
		return err
	}
	m.Role = probe.Role
	raw := append(json.RawMessage(nil), data...)
	m.Raw = raw
	switch probe.Role {
	case "system":
		var s SystemMessage
		if err := json.Unmarshal(data, &s); err != nil {
			return err
		}
		s.Raw = raw
		m.System = &s
	case "user":
		var u UserMessage
		if err := json.Unmarshal(data, &u); err != nil {
			return err
		}
		u.Raw = raw
		m.User = &u
	case "assistant":
		var a AssistantMessage
		if err := json.Unmarshal(data, &a); err != nil {
			return err
		}
		a.Raw = raw
		m.Assistant = &a
	case "toolResult":
		var t ToolResultMessage
		if err := json.Unmarshal(data, &t); err != nil {
			return err
		}
		t.Raw = raw
		m.ToolResult = &t
	case "bashExecution":
		var b BashExecutionMessage
		if err := json.Unmarshal(data, &b); err != nil {
			return err
		}
		b.Raw = raw
		m.BashExecution = &b
	case "custom":
		var c CustomMessage
		if err := json.Unmarshal(data, &c); err != nil {
			return err
		}
		c.Raw = raw
		m.Custom = &c
	case "branchSummary":
		var b BranchSummaryMessage
		if err := json.Unmarshal(data, &b); err != nil {
			return err
		}
		m.BranchSummary = &b
	case "compactionSummary":
		var c CompactionSummaryMessage
		if err := json.Unmarshal(data, &c); err != nil {
			return err
		}
		m.CompactionSummary = &c
	default:
		// Unknown role: preserved in Raw, Role kept for diagnostics.
		// Continuation must refuse unsafe unknown context (checked in projection).
		m.Role = probe.Role
	}
	return nil
}

// ---- Session entries ----

type SessionHeader struct {
	Type          string  `json:"type"`
	Version       *int    `json:"version,omitempty"`
	ID            string  `json:"id"`
	Timestamp     string  `json:"timestamp"`
	Cwd           string  `json:"cwd,omitempty"`
	ParentSession *string `json:"parentSession,omitempty"`
}

type entryBase struct {
	Type      string  `json:"type"`
	ID        string  `json:"id"`
	ParentID  *string `json:"parentId"`
	Timestamp string  `json:"timestamp"`
}

// Entry is the union of all v3 session entries.
type Entry struct {
	Kind string // type discriminator
	Base entryBase
	// Exactly one payload set (except header handled separately).
	Message             *SessionMessageEntry
	ThinkingLevelChange *ThinkingLevelChangeEntry
	ModelChange         *ModelChangeEntry
	Usage               *UsageEntry
	Compaction          *CompactionEntry
	BranchSummary       *BranchSummaryEntry
	Custom              *CustomEntry
	CustomMessage       *CustomMessageEntry
	ContextEdit         *ContextEditEntry
	Label               *LabelEntry
	SessionInfo         *SessionInfoEntry
	Raw                 json.RawMessage
}

type SessionMessageEntry struct {
	Type      string       `json:"type"`
	ID        string       `json:"id"`
	ParentID  *string      `json:"parentId"`
	Timestamp string       `json:"timestamp"`
	Message   AgentMessage `json:"message"`
}

type ThinkingLevelChangeEntry struct {
	Type          string  `json:"type"`
	ID            string  `json:"id"`
	ParentID      *string `json:"parentId"`
	Timestamp     string  `json:"timestamp"`
	ThinkingLevel string  `json:"thinkingLevel"`
}

type ModelChangeEntry struct {
	Type      string  `json:"type"`
	ID        string  `json:"id"`
	ParentID  *string `json:"parentId"`
	Timestamp string  `json:"timestamp"`
	Provider  string  `json:"provider"`
	ModelID   string  `json:"modelId"`
}

type UsageEntry struct {
	Type      string  `json:"type"`
	ID        string  `json:"id"`
	ParentID  *string `json:"parentId"`
	Timestamp string  `json:"timestamp"`
	Kind2     string  `json:"kind"`
	Provider  string  `json:"provider"`
	Model     string  `json:"model"`
	Usage     Usage   `json:"usage"`
	Note      *string `json:"note,omitempty"`
}

type CompactionEntry struct {
	Type             string          `json:"type"`
	ID               string          `json:"id"`
	ParentID         *string         `json:"parentId"`
	Timestamp        string          `json:"timestamp"`
	Summary          string          `json:"summary"`
	FirstKeptEntryID string          `json:"firstKeptEntryId"`
	TokensBefore     float64         `json:"tokensBefore"`
	Details          json.RawMessage `json:"details,omitempty"`
	Usage            *Usage          `json:"usage,omitempty"`
	FromHook         *bool           `json:"fromHook,omitempty"`
	SystemMessage    *SystemMessage  `json:"systemMessage,omitempty"`
}

type BranchSummaryEntry struct {
	Type      string          `json:"type"`
	ID        string          `json:"id"`
	ParentID  *string         `json:"parentId"`
	Timestamp string          `json:"timestamp"`
	FromID    string          `json:"fromId"`
	Summary   string          `json:"summary"`
	Details   json.RawMessage `json:"details,omitempty"`
	Usage     *Usage          `json:"usage,omitempty"`
	FromHook  *bool           `json:"fromHook,omitempty"`
}

type CustomEntry struct {
	Type       string          `json:"type"`
	ID         string          `json:"id"`
	ParentID   *string         `json:"parentId"`
	Timestamp  string          `json:"timestamp"`
	CustomType string          `json:"customType"`
	Data       json.RawMessage `json:"data,omitempty"`
}

type CustomMessageEntry struct {
	Type       string          `json:"type"`
	ID         string          `json:"id"`
	ParentID   *string         `json:"parentId"`
	Timestamp  string          `json:"timestamp"`
	CustomType string          `json:"customType"`
	Content    any             `json:"content"`
	Details    json.RawMessage `json:"details,omitempty"`
	Display    bool            `json:"display"`
}

type ContextEditEntry struct {
	Type        string          `json:"type"`
	ID          string          `json:"id"`
	ParentID    *string         `json:"parentId"`
	Timestamp   string          `json:"timestamp"`
	TargetID    string          `json:"targetId"`
	Replacement json.RawMessage `json:"replacement"` // null or {"content": ...}
}

type LabelEntry struct {
	Type      string  `json:"type"`
	ID        string  `json:"id"`
	ParentID  *string `json:"parentId"`
	Timestamp string  `json:"timestamp"`
	TargetID  string  `json:"targetId"`
	Label     *string `json:"label"`
}

type SessionInfoEntry struct {
	Type      string  `json:"type"`
	ID        string  `json:"id"`
	ParentID  *string `json:"parentId"`
	Timestamp string  `json:"timestamp"`
	Name      *string `json:"name,omitempty"`
}
