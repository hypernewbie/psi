package app

// State table (P2-007, Section 3.2). One reducer owns UI state and activation.
// Overlapping booleans are replaced by explicit states with defined draft/cursor/session/output effects.

type State string

const (
	StateStarting       State = "starting"
	StateReplaying      State = "replaying"
	StateIdle           State = "idle"
	StateSelecting      State = "selecting"
	StateWaiting        State = "waiting"
	StateStreaming      State = "streaming"
	StateRunningTool    State = "running_tool"
	StateCompacting     State = "compacting"
	StateCancelling     State = "cancelling"
	StateOffline        State = "offline"
	StateStorageBlocked State = "storage_blocked"
	StateExiting        State = "exiting"
)

// Allowed actions per state. Submission is rejected during replay, cancellation,
// unsettled replacement, and active runs (cancellation only).
func (s State) CanSubmit() bool { return s == StateIdle || s == StateOffline }

// CanCancel reports whether Ctrl+C cancels an owned operation.
func (s State) CanCancel() bool {
	switch s {
	case StateWaiting, StateStreaming, StateRunningTool, StateCompacting, StateReplaying, StateSelecting:
		return true
	}
	return false
}

// DraftEffect describes what happens to draft/cursor on transition.
type Transition struct {
	From, To       State
	PreserveDraft  bool
	PreserveCursor bool
	PreserveRecall bool
	ClearSelection bool
	AppendDivider  bool
	ReplayTimeline bool
}

var transitions = map[State]map[State]Transition{
	StateStarting: {StateReplaying: {From: StateStarting, To: StateReplaying, ReplayTimeline: true}},
	StateReplaying: {
		StateIdle:    {From: StateReplaying, To: StateIdle, PreserveRecall: true},
		StateOffline: {From: StateReplaying, To: StateOffline, PreserveRecall: true},
	},
	StateIdle: {
		StateSelecting:      {From: StateIdle, To: StateSelecting, PreserveDraft: true, PreserveCursor: true, PreserveRecall: true},
		StateWaiting:        {From: StateIdle, To: StateWaiting, PreserveDraft: false},
		StateCompacting:     {From: StateIdle, To: StateCompacting, PreserveDraft: true, PreserveCursor: true},
		StateExiting:        {From: StateIdle, To: StateExiting},
		StateReplaying:      {From: StateIdle, To: StateReplaying, AppendDivider: true, ReplayTimeline: true},
		StateStorageBlocked: {From: StateIdle, To: StateStorageBlocked, PreserveDraft: true, PreserveRecall: true},
	},
}
