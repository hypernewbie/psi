package tools

import (
	"errors"
	"io/fs"
	"path/filepath"
	"sync"
	"syscall"
)

// MutationQueue serializes file mutations per resolved path, mirroring Pi's
// file-mutation-queue: registration happens in call order, mutations to
// distinct files overlap, and the queue slot is held until the operation
// settles even after cancellation or a panic. Reference:
// temp/pi-mono/packages/coding-agent/src/core/tools/file-mutation-queue.ts
type MutationQueue struct {
	// regMu serializes key resolution and tail registration so two concurrent
	// calls for one file acquire their slots in registration order.
	regMu sync.Mutex
	mu    sync.Mutex
	tails map[string]chan struct{}
}

// NewMutationQueue returns an empty queue.
func NewMutationQueue() *MutationQueue {
	return &MutationQueue{tails: map[string]chan struct{}{}}
}

var mutationClosed = func() chan struct{} {
	c := make(chan struct{})
	close(c)
	return c
}()

// mutationKey matches Pi's queue key: the resolved real path when it exists,
// the resolved absolute path when the lookup reports a missing path
// (ENOENT/ENOTDIR). Other lookup errors propagate so the caller can fail
// truthfully without running the mutation.
func mutationKey(cwd, path string) (string, error) {
	resolved := resolvePath(cwd, path)
	real, err := filepath.EvalSymlinks(resolved)
	if err == nil {
		return real, nil
	}
	if errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ENOTDIR) {
		return resolved, nil
	}
	return "", err
}

// mutationSlot is one registered place in a file's mutation chain.
type mutationSlot struct {
	q    *MutationQueue
	key  string
	prev chan struct{}
	mine chan struct{}
}

// register resolves the key and takes the next slot for it. Registration is
// serialized in call order; the returned slot has not waited yet.
func (q *MutationQueue) register(cwd, path string) (*mutationSlot, error) {
	if q == nil {
		return &mutationSlot{}, nil
	}
	q.regMu.Lock()
	defer q.regMu.Unlock()
	key, err := mutationKey(cwd, path)
	if err != nil {
		return nil, err
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	prev := q.tails[key]
	if prev == nil {
		prev = mutationClosed
	}
	mine := make(chan struct{})
	q.tails[key] = mine
	return &mutationSlot{q: q, key: key, prev: prev, mine: mine}, nil
}

// run waits for the previously registered mutation on the same key to settle,
// then runs fn while holding the slot. The slot is released when fn returns
// or panics. The wait itself is not abortable: like Pi, cancellation is the
// mutation's concern once it owns the slot.
func (s *mutationSlot) run(fn func() Result) Result {
	if s.q == nil {
		return fn()
	}
	<-s.prev
	defer func() {
		close(s.mine)
		s.q.mu.Lock()
		if s.q.tails[s.key] == s.mine {
			delete(s.q.tails, s.key)
		}
		s.q.mu.Unlock()
	}()
	return fn()
}

// withSlot registers path and runs fn inside the slot in one step.
func (q *MutationQueue) withSlot(cwd, path string, fn func() Result) Result {
	slot, err := q.register(cwd, path)
	if err != nil {
		return errResult(err.Error())
	}
	return slot.run(fn)
}
