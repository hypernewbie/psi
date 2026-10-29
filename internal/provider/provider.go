// Package provider implements the supported provider wire protocols.
package provider

import (
	"context"
	"fmt"

	"psi/internal/modelmeta"
	"psi/internal/providerregistry"
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
	AdaptiveThinking    bool
	MidConvoEffort      bool
	AllowEmptySignature bool
	Compat              modelmeta.Compat
	ID                  string
	API                 string
	Provider            string
	BaseURL             string
	ContextWindow       int
	MaxTokens           int
	Reasoning           bool
	InputImage          bool
	ThinkingLevelMap    map[string]*string
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
	Role                 string
	Content              []Block
	ToolCallID           string
	ToolName             string
	IsError              bool
	Provider, API, Model string // source of opaque assistant replay metadata
	ThinkingLevel        string // effective provider effort for historical replay
	StopReason           string
	Sections             []SystemSection
	ToolsAdded           []ToolDef
	ToolsRemoved         []string
}

type Block struct {
	Type          string // text|image|toolCall|thinking
	Text          string
	TextSignature string
	Namespace     string
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
	ThoughtSignature  string
	Redacted          bool
}

// ToolDef is one declared tool.
type ToolDef struct {
	Name        string
	Description string
	Schema      map[string]any
}

type SystemSection struct {
	Name  string
	Value *string
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
	RawStopReason string
	ThinkingLevel string
}

type Usage struct {
	CacheWrite1h *int64
	Reasoning    *int64
	Input        int64
	Output       int64
	CacheRead    int64
	CacheWrite   int64
	TotalTokens  int64
}

// StreamInterrupt carries known partial response metadata when a stream
// fails after the backend supplied usage or other response fields (F12).
// It never promotes unfinished tool calls: partial Content holds only
// completed blocks, and known usage stays distinct from unknown final usage.
type StreamInterrupt struct {
	Partial *Response
	Err     error
}

func (e *StreamInterrupt) Error() string { return e.Err.Error() }
func (e *StreamInterrupt) Unwrap() error { return e.Err }

// KnownUsage reports whether the partial response carries provider-reported
// counts worth persisting exactly once. Zero usage means unknown, never free.
func (e *StreamInterrupt) KnownUsage() bool {
	if e == nil || e.Partial == nil {
		return false
	}
	u := e.Partial.Usage
	return u.Input != 0 || u.Output != 0 || u.CacheRead != 0 || u.CacheWrite != 0 || u.TotalTokens != 0
}

// ProviderID returns the session provider name, including for callers that
// predate explicit provider metadata.
func (m ModelMeta) ProviderID() string {
	if m.Provider != "" {
		return m.Provider
	}
	if m.API == "openai-codex-responses" {
		return "openai-codex"
	}
	return "anthropic"
}

func BuildRequest(req Request) (map[string]any, error) {
	if req.Model.API == "" {
		if req.Model.Provider != "" {
			return nil, fmt.Errorf("model %q must declare its provider API", req.Model.ID)
		}
		req.Model.API = "anthropic-messages" // legacy psi model metadata
	}
	if !providerregistry.SupportsAPI(req.Model.ProviderID(), req.Model.API) {
		return nil, fmt.Errorf("provider %q does not support API %q", req.Model.ProviderID(), req.Model.API)
	}
	switch req.Model.API {
	case "anthropic-messages":
		return BuildAnthropicRequest(req)
	case "openai-codex-responses":
		return BuildCodexRequest(req)
	case "openai-completions":
		return BuildCompletionsRequest(req)
	case "google-generative-ai":
		return BuildGoogleRequest(req)
	case "openai-responses":
		return BuildResponsesRequest(req)
	default:
		return nil, fmt.Errorf("unsupported provider API %q", req.Model.API)
	}
}

func validateProviderAPI(model ModelMeta, api string) error {
	if model.API == "" && model.Provider != "" {
		return fmt.Errorf("model %q must declare its provider API", model.ID)
	}
	if !providerregistry.SupportsAPI(model.ProviderID(), api) {
		return fmt.Errorf("provider %q does not support API %q", model.ProviderID(), api)
	}
	return nil
}

// Provider streams text deltas and returns one completed message.
type Provider interface {
	Send(ctx context.Context, req Request, onText func(string)) (*Response, error)
	CloseIdle()
}
