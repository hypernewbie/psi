package app

import (
	"math"

	"psi/internal/event"
	"psi/internal/pibridge"
)

// bridgeSnapshot publishes pane identity and context telemetry. The client
// owns the runtime id, the running state, and the snapshot revision.
func bridgeSnapshot(r *runtime) pibridge.Snapshot {
	snap := pibridge.Snapshot{
		Provider: r.ov.Provider,
		Model:    r.ov.Model,
		Thinking: r.ov.Thinking,
	}
	// Tokens are only ever sampled together with a positive window, so an
	// absent window means no estimate exists: publishing a lone present zero
	// would turn unknown into a measurement.
	if safeContextValue(r.contextWindow) && r.contextWindow > 0 && safeContextValue(r.contextTokens) {
		snap.ContextTokens, snap.HasContextTokens = r.contextTokens, true
		snap.ContextWindow, snap.HasContextWindow = r.contextWindow, true
	}
	if r.generation > 0 {
		snap.Generation = uint64(r.generation)
	}
	if r.store != nil {
		snap.SessionID = r.store.Header().ID
		snap.LeafID = r.store.LeafID()
	}
	// Readiness is copied, never inferred: an idle app that owns a pending
	// compaction or an active one is not ready for another.
	// Unresolved bridge preparation can still install a configuration, so it
	// counts against readiness the same way an active run does.
	prepWork := r.bridgeWork != nil && r.bridgeWork()
	snap.HasCompact = true
	queued := r.compactPending
	if r.queuedInputs != nil && r.queuedInputs() > 0 {
		queued = true
	}
	snap.Compact = pibridge.CompactReadiness{
		Idle:       r.state == StateIdle && !r.compactActive && !r.compactPending && !r.reloadPending && !prepWork,
		HasPending: queued,
		Active:     r.compactActive,
		Pending:    r.compactPending,
	}
	// Reload readiness rides the same snapshot. The run id is the client's
	// live counter and the revision counts every user message and
	// compaction boundary, so a moved-on session reads stale.
	snap.HasReload = true
	snap.Reload = pibridge.ReloadReadiness{
		RunID:          r.bridge.RunID(),
		HasRunID:       true,
		ContextRev:     r.contextRev,
		HasContextRev:  true,
		Idle:           r.state == StateIdle && !r.compactActive && !r.compactPending && !r.reloadPending && !prepWork,
		HasPending:     queued,
		CompactActive:  r.compactActive,
		CompactPending: r.compactPending,
		ReloadPending:  r.reloadPending,
		HighWater:      r.reloadHigh,
		HasHighWater:   true,
	}
	return snap
}

// bumpContextRev counts one context change for the reload fence. It runs on
// the loop next to the mutation it describes, then republishes readiness.
func (r *runtime) bumpContextRev() {
	if r == nil {
		return
	}
	r.contextRev++
	r.syncBridge()
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

// safeContextValue rejects the values a consumer must never see as a
// measurement: negative, non-finite, or beyond exact integer precision.
func safeContextValue(v float64) bool {
	return v >= 0 && v <= 1<<53 && !math.IsInf(v, 0) && !math.IsNaN(v)
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
