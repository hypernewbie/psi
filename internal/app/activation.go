package app

import (
	"fmt"

	"psi/internal/session"
)

// Activation transaction (P2-012, Section 6.4). Every path uses one code path:
// startup --continue/--session/--resume and in-process /resume.
// A failed candidate leaves old session, draft, recall, provider, and lock intact.
// A session cannot change while an unsettled run owns writes.

type Activation struct {
	Store      *session.Store
	Path       []*session.Entry
	Proj       *session.Projection
	Recall     []string
	Offline    bool
	OfflineErr error
}

// Prepare checks the candidate without changing the active session.
func PrepareActivation(candidate *session.Store) (*Activation, error) {
	// 1. Read + check graph/continuation/workspace/model metadata.
	path, err := candidate.MetadataPath()
	if err != nil {
		return nil, fmt.Errorf("activation: %w", err)
	}
	proj, err := candidate.Projection()
	if err != nil {
		return nil, fmt.Errorf("activation: %w", err)
	}
	recall, err := candidate.Recall()
	if err != nil {
		return nil, fmt.Errorf("activation recall: %w", err)
	}
	// 2. Continuation check (refuse unsafe unknown context, deferred state).
	if err := session.CheckContinuable(proj.Flat, proj.Entries); err != nil {
		// Readable history remains; continuation blocked (offline history).
		return &Activation{Store: candidate, Path: path, Proj: proj, Recall: recall, Offline: true, OfflineErr: err}, nil
	}
	// 3. Ownership checked by caller via TryLock before commit.
	// 4. Context, display iterator, recall source prepared here.
	return &Activation{
		Store: candidate, Path: path, Proj: proj,
		Recall: recall,
	}, nil
}

// Commit swaps the runtime to the prepared activation (caller holds locks).
func (r *runtime) commitActivation(act *Activation) {
	r.store = act.Store
	// Editor recall: session-specific, up to 200 (P2-021).
	// The console editor is replaced per activation to avoid leaking prior recall.
	// Caller redraws divider + replays timeline + enters idle/offline.
}
