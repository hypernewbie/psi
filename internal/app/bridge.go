package app

import (
	"psi/internal/event"
	"psi/internal/pibridge"
)

// bridgeSnapshot publishes pane identity only. The client owns the runtime
// id, the running state, and the snapshot revision.
func bridgeSnapshot(r *runtime) pibridge.Snapshot {
	snap := pibridge.Snapshot{
		Provider: r.ov.Provider,
		Model:    r.ov.Model,
		Thinking: r.ov.Thinking,
	}
	if r.generation > 0 {
		snap.Generation = uint64(r.generation)
	}
	if r.store != nil {
		snap.SessionID = r.store.Header().ID
		snap.LeafID = r.store.LeafID()
	}
	return snap
}

// syncBridge starts the bridge once the runtime generation is valid and
// republishes identity on every later call. There is exactly one FromEnv
// attempt: a plain pane must not probe the environment every frame.
func (r *runtime) syncBridge() {
	if r == nil || r.generation < 1 {
		return
	}
	if r.bridge == nil {
		if r.bridgeTried {
			return
		}
		r.bridgeTried = true
		r.bridge = pibridge.FromEnv(bridgeSnapshot(r))
		if r.bridge == nil {
			return
		}
		r.bridge.Start()
	}
	r.bridge.Sync(bridgeSnapshot(r))
}

func (r *runtime) closeBridge() {
	if r == nil || r.bridge == nil {
		return
	}
	r.bridge.Close()
	r.bridge = nil
}

func (r *runtime) bridgeRunStarted() {
	if r == nil || r.bridge == nil {
		return
	}
	r.bridge.RunStarted()
}

func (r *runtime) bridgeRunSettled(outcome pibridge.Outcome) {
	if r == nil || r.bridge == nil {
		return
	}
	r.bridge.RunSettled(outcome)
}

// runOutcomeOf maps one settlement event to the wire outcome. Interruption
// wins over Err: cancellation also sets an error, and phi must see ABORTED.
func runOutcomeOf(ev event.Event) pibridge.Outcome {
	switch {
	case ev.Outcome == event.OutcomeInterrupted:
		return pibridge.OutcomeAborted
	case ev.Err != nil:
		return pibridge.OutcomeError
	case ev.Outcome == event.OutcomeDurable:
		return pibridge.OutcomeCompleted
	default:
		return pibridge.OutcomeUnspecified
	}
}
