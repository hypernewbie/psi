// Package provider implements one provider path completely (Phase 6).
//
// First and only release provider: Anthropic Messages API over HTTP+SSE.
// Reference: temp/pi-mono/packages/ai/src/api/anthropic-messages.ts and
// packages/ai/src/providers/anthropic.ts at pinned revision d5b3de2.
package provider

import (
	"context"
	"fmt"
)

// Thinking levels supported in the first release.
var ValidLevels = []string{"off", "minimal", "low", "medium", "high", "xhigh", "max"}

// Budget maps Pi thinking levels to Anthropic budget_tokens (older models).
// Adaptive models use effort mapping; this table is the bounded, tested path.
func ThinkingBudget(level string) (int, error) {
	switch level {
	case "off":
		return 0, nil
	case "minimal":
		return 1024, nil
	case "low":
		return 2048, nil
	case "medium":
		return 4096, nil
	case "high":
		return 8192, nil
	case "xhigh":
		return 16384, nil
	case "max":
		return 32000, nil
	default:
		return 0, fmt.Errorf("unsupported thinking level %q", level)
	}
}

// Metadata for the selected model (from models.json subset).
type ModelMeta struct {
	ID            string
	API           string // must be anthropic-messages when set
	ContextWindow int
	MaxTokens     int
	Reasoning     bool
	InputImage    bool
}

// Request is the normalized provider request (Pi context + active tools).
type Request struct {
	Model     ModelMeta
	System    string
	Messages  []Message
	Tools     []ToolDef
	Thinking  string
	MaxTokens int
}

// Message is a normalized Pi message (system/user/assistant/toolResult only;
// ConvertToLLM output).
type Message struct {
	Role       string
	Content    []Block
	ToolCallID string
	ToolName   string
	IsError    bool
}

type Block struct {
	Type string // text|image|toolCall|thinking
	Text string
	// Image.
	Data     string
	MimeType string
	// Tool call.
	ID        string
	Name      string
	Arguments map[string]any
	// Thinking (opaque replay).
	Thinking          string
	ThinkingSignature string
	Redacted          bool
}

// ToolDef is one declared tool.
type ToolDef struct {
	Name        string
	Description string
	Schema      map[string]any
}

// Response is one authoritative completed assistant message.
type Response struct {
	// Content retains original block order and opaque thinking signatures.
	Content       []Block
	Text          string
	Thinking      string
	ToolCalls     []Block
	StopReason    string
	Usage         Usage
	Model         string
	ResponseID    string
	ThinkingLevel string
}

type Usage struct {
	Input       int64
	Output      int64
	CacheRead   int64
	CacheWrite  int64
	TotalTokens int64
}

// Provider streams text deltas and returns one completed message.
type Provider interface {
	Send(ctx context.Context, req Request, onText func(string)) (*Response, error)
	CloseIdle()
}
