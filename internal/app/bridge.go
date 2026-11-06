package app

import (
	"fmt"
	"math"

	"psi/internal/agent"
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
	// Attachment readiness is sampled, never inferred: it exists only for a
	// bridge that actually retained a valid root, and image availability is
	// present only when the live model is known.
	if r.bridge != nil && r.bridge.AttachmentsAvailable() {
		snap.HasAttachment = true
		// An occupied recovery batch refuses new prompts, so it is pending
		// work for this capability even though no run owns it.
		recovered := len(r.recovery) > 0
		ready := pibridge.AttachmentReadiness{
			Idle:             r.state == StateIdle && !r.compactActive && !r.compactPending && !r.reloadPending && !prepWork && !recovered,
			HasPending:       queued || recovered,
			CompactionActive: r.compactActive,
			BridgePending:    prepWork || r.compactPending || r.reloadPending,
		}
		if r.cfg.Model.ID != "" {
			ready.ImageAvailable = r.cfg.Model.InputImage
			ready.HasImageAvailable = true
		}
		snap.Attachment = ready
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

// noteRecovery moves admitted structured input into the recovery batch. The
// content stays structured, so an image is never flattened into editor text,
// and the batch is never replayed without a new user action.
func (r *runtime) noteRecovery(inputs ...agent.UserInput) int {
	if r == nil {
		return 0
	}
	added := 0
	for _, input := range inputs {
		if !input.Structured() || input.Empty() {
			continue
		}
		r.recovery = append(r.recovery, input)
		added++
	}
	return added
}

// takeSteering splits a stopped run's queued input into structured recovery
// and the ordinary text the editor already restores.
func (r *runtime) takeSteering(loop *agent.Loop) []string {
	if loop == nil {
		return nil
	}
	var texts []string
	var structured []agent.UserInput
	for _, input := range loop.TakeSteering() {
		if input.Structured() {
			structured = append(structured, input)
			continue
		}
		texts = append(texts, input.Display)
	}
	r.noteRecovery(structured...)
	if len(structured) > 0 && r.recoveryNotify != nil {
		r.recoveryNotify("An undelivered attachment prompt was not sent. Phi keeps a saved copy you can restore.")
	}
	return texts
}

// recoveryBytes is the decoded image total retained for undelivered input.
func (r *runtime) recoveryBytes() uint64 {
	var total uint64
	for _, input := range r.recovery {
		total += input.ImageBytes
	}
	if r.inFlight != nil {
		total += r.inFlight.ImageBytes
	}
	return total
}

// clearRecovery drops the batch when the session it belonged to is replaced.
func (r *runtime) clearRecovery() {
	if r == nil || len(r.recovery) == 0 {
		return
	}
	count := len(r.recovery)
	r.recovery = nil
	if r.recoveryNotify != nil {
		r.recoveryNotify(fmt.Sprintf("%d undelivered attachment prompt(s) were discarded when the session changed", count))
	}
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
