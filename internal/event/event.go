// Package event defines the ordered internal event stream (P2-008).
// Reference: temp/pi-mono/packages/coding-agent/src/core/agent-session.ts event flow.
// Full tool arguments and result bodies never enter terminal events.
package event

// Type enumerates terminal-consumable events.
type Type string

const (
	TextDelta         Type = "text_delta"
	ThinkingDelta     Type = "thinking_delta"
	AssistantStart    Type = "assistant_start"
	RunStart          Type = "run_start"
	RunEnd            Type = "run_end"
	SteeringDelivered Type = "steering_delivered"
	FollowUpDelivered Type = "follow_up_delivered"
	ToolStart         Type = "tool_start"
	ToolEnd           Type = "tool_end"
	// WebProgressUpdate is a transient, display-only update for one running
	// web call. It is never persisted and never enters the model context.
	WebProgressUpdate              Type = "web_progress"
	Retry                          Type = "retry"
	SummarizationRetryScheduled    Type = "summarization_retry_scheduled"
	SummarizationRetryAttemptStart Type = "summarization_retry_attempt_start"
	SummarizationRetryFinished     Type = "summarization_retry_finished"
	CompactionStart                Type = "compaction_start"
	CompactionEnd                  Type = "compaction_end"
	Usage                          Type = "usage"
	Error                          Type = "error"

	// AutoRetryStart and AutoRetryEnd are Pi's auto_retry_start and
	// auto_retry_end: a failed assistant response is retried after
	// RetryDelayMs, then one event reports the outcome of every retry.
	// AutoRetryStart carries the cause of the failed response in Text and its
	// durable session entry in CommittedID, which the retry omits from the
	// model context. AutoRetryEnd has a nil Err for success, the error of the
	// response that ended the retries, or the cancellation of a retry.
	// They are separate from Retry (provider-layer) and Summarization*
	// (compaction) retries, which never report themselves as auto-retry.
	AutoRetryStart Type = "auto_retry_start"
	AutoRetryEnd   Type = "auto_retry_end"
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
	Progress     *WebProgress
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
	CompactionAdaptive  = "adaptive"
	CompactionEndTurn   = "end-of-turn"
)

const (
	OutcomeTransient   = "transient"
	OutcomeDurable     = "durable"
	OutcomeInterrupted = "interrupted"
	OutcomeUnsaved     = "unsaved"
)
