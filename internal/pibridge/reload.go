package pibridge

import (
	pibridgepb "psi/internal/pibridge/pb"
)

// ReloadExpect is the identity a reload request was admitted against.
// Absent counters mean the caller pinned nothing, never an implicit zero.
type ReloadExpect struct {
	Session       string
	Leaf          string
	Model         ModelRef
	HasModel      bool
	RunID         uint64
	HasRunID      bool
	ContextRev    uint64
	HasContextRev bool
}

// ReloadExpectation returns the identity the caller pinned.
func (r Request) ReloadExpectation() ReloadExpect {
	return ReloadExpect{
		Session:       r.reloadSession,
		Leaf:          r.reloadLeaf,
		Model:         r.reloadModel,
		HasModel:      r.hasReloadModel,
		RunID:         uint64Value(r.reloadRunID),
		HasRunID:      r.reloadRunID != nil,
		ContextRev:    uint64Value(r.reloadContextRev),
		HasContextRev: r.reloadContextRev != nil,
	}
}

func uint64Value(v *uint64) uint64 {
	if v == nil {
		return 0
	}
	return *v
}

// ReloadReadiness is one sampled view of native-reload admission state.
// Counter presence distinguishes known zero from unknown.
type ReloadReadiness struct {
	RunID          uint64
	HasRunID       bool
	ContextRev     uint64
	HasContextRev  bool
	Idle           bool
	HasPending     bool
	Active         bool
	CompactActive  bool
	CompactPending bool
	ReloadPending  bool
	HighWater      uint64
	HasHighWater   bool
}

// ReplyReload answers one native reload. Dispatched reports callback entry
// only, never reload completion; refused carries a safe reason. The reply
// never invents a vocabulary outside the contract.
func (r Request) ReplyReload(dispatched bool, reason string) bool {
	outcome := pibridgepb.ReloadDispatchOutcome_RELOAD_DISPATCH_OUTCOME_REFUSED
	if dispatched {
		outcome = pibridgepb.ReloadDispatchOutcome_RELOAD_DISPATCH_OUTCOME_DISPATCHED
	}
	return r.send(func(m *pibridgepb.PiToPhi) {
		m.Payload = &pibridgepb.PiToPhi_ReloadAck{ReloadAck: &pibridgepb.ReloadAck{
			RequestId: r.requestID,
			Outcome:   outcome,
			Reason:    allowReloadReason(reason),
		}}
	})
}

// allowReloadReason coerces one host reason to the contract vocabulary.
// Unknown reasons become empty, never an error.
func allowReloadReason(reason string) string {
	switch reason {
	case "", "duplicate", "stale", "busy", "queued", "compaction-active",
		"pending", "unavailable":
		return reason
	default:
		return ""
	}
}
