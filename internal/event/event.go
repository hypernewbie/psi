// Package event defines the ordered internal event stream (P2-008).
// Reference: temp/pi-mono/packages/coding-agent/src/core/agent-session.ts event flow.
// Full tool arguments and result bodies never enter terminal events.
package event

// Type enumerates terminal-consumable events.
type Type string

const (
	TextDelta         Type = "text_delta"
	AssistantStart    Type = "assistant_start"
	RunStart          Type = "run_start"
	RunEnd            Type = "run_end"
	SteeringDelivered Type = "steering_delivered"
	ToolStart         Type = "tool_start"
	ToolEnd           Type = "tool_end"
	Retry             Type = "retry"
	CompactionStart   Type = "compaction_start"
	CompactionEnd     Type = "compaction_end"
	Usage             Type = "usage"
	Error             Type = "error"
)

// Event is one bounded, observable unit with explicit ordering.
// SessionID + Generation reject stale events after replacement.
// RunID + AttemptID + Seq order provider/tool actions.
// ProvisionalID vs CommittedID distinguish transient deltas from durable completion.
type Event struct {
	Type         Type
	Text         string
	ToolName     string
	ToolCallID   string
	State        string
	Summary      ToolSummary
	RetryAttempt int
	RetryMaximum int
	RetryDelayMs int64
	RetryClass   string
	Err          error

	SessionID     string
	Generation    int64
	RunID         string
	AttemptID     string
	Seq           int64
	ProvisionalID string
	CommittedID   string
	// Outcome class: transient, durable, interrupted, unsaved.
	Outcome string
}

const (
	CompactionManual    = "manual"
	CompactionThreshold = "threshold"
	CompactionOverflow  = "overflow"
)

const (
	OutcomeTransient   = "transient"
	OutcomeDurable     = "durable"
	OutcomeInterrupted = "interrupted"
	OutcomeUnsaved     = "unsaved"
)
