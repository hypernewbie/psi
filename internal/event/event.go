// Package event defines the small internal event stream (Phase 1 Step 11).
package event

// Type enumerates terminal-consumable events. Full tool results never enter
// terminal events; the terminal consumes small status updates only.
type Type string

const (
	TextDelta Type = "text_delta"
	RunStart  Type = "run_start"
	RunEnd    Type = "run_end"
	ToolStart Type = "tool_start"
	ToolEnd   Type = "tool_end"
	Usage     Type = "usage"
	Error     Type = "error"
)

// Event is one bounded, observable unit. Payloads are small by construction.
type Event struct {
	Type       Type
	Text       string // TextDelta fragment
	ToolName   string // ToolStart/ToolEnd
	ToolCallID string
	State      string // running|done|failed|cancelled
	Err        error  // Error/RunEnd failure
}
