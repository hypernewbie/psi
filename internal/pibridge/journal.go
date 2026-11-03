// Package pibridge is the Pi side of the Phi <-> Pi integration bridge.
//
// It publishes lifecycle edges and authoritative state snapshots to a Phi
// server over one bidirectional gRPC stream. The wire contract is Phi's
// bonus/pi-phi/PROTOCOL.md, pinned at revision a207d6b4; the bindings in
// pb/ are generated from proto/bridge.proto and must be regenerated from
// the same Phi revision.
package pibridge

import (
	"google.golang.org/protobuf/proto"

	pibridgepb "psi/internal/pibridge/pb"
)

// journalLimit mirrors Phi's maxJournalRecords.
const journalLimit = 64

// journal is the bounded lifecycle record store sent on attach. Sequences
// advance monotonically; overflow drops the oldest record and latches a gap
// that only a new runtime generation clears. Phi rejects a record whose
// sequence or run id is zero.
type journal struct {
	seq     uint64
	records []*pibridgepb.LifecycleRecord
	gap     bool
}

// open records an unsettled run and returns the live edge to publish.
func (j *journal) open(runID uint64) *pibridgepb.LifecycleRecord {
	j.seq++
	rec := &pibridgepb.LifecycleRecord{EventSequence: j.seq, RunId: runID}
	j.records = append(j.records, rec)
	j.trim()
	return rec
}

// settle replaces the run's unsettled record with one that advances the
// sequence, so Phi's append never ignores it as a duplicate.
func (j *journal) settle(runID uint64, outcome pibridgepb.RunOutcome) *pibridgepb.LifecycleRecord {
	for i := len(j.records) - 1; i >= 0; i-- {
		if j.records[i].RunId == runID && !j.records[i].Settled {
			j.records = append(j.records[:i], j.records[i+1:]...)
			break
		}
	}
	j.seq++
	rec := &pibridgepb.LifecycleRecord{
		EventSequence: j.seq,
		RunId:         runID,
		Settled:       true,
		Outcome:       outcome,
	}
	j.records = append(j.records, rec)
	j.trim()
	return rec
}

func (j *journal) trim() {
	for len(j.records) > journalLimit {
		j.records = j.records[1:]
		j.gap = true
	}
}

// snapshot copies the retained records; callers never alias the store.
func (j *journal) snapshot() []*pibridgepb.LifecycleRecord {
	out := make([]*pibridgepb.LifecycleRecord, len(j.records))
	for i, rec := range j.records {
		// Never copy the generated struct: it embeds a message state mutex.
		out[i] = proto.Clone(rec).(*pibridgepb.LifecycleRecord)
	}
	return out
}

// bounds returns the first and last retained sequence, zero when empty.
func (j *journal) bounds() (uint64, uint64) {
	if len(j.records) == 0 {
		return 0, 0
	}
	return j.records[0].EventSequence, j.records[len(j.records)-1].EventSequence
}

func (j *journal) reset() {
	j.seq = 0
	j.records = nil
	j.gap = false
}
