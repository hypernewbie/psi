package lockfile

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"sync/atomic"
	"time"
)

// Options sets the lease policy. Non-positive values use Pi's defaults.
type Options struct {
	StaleAfter time.Duration
	Heartbeat  time.Duration
	Wait       time.Duration
}

var ErrBusy = errors.New("lock is busy")
var ErrInspect = errors.New("cannot inspect lock")

// Acquire takes a Pi-compatible mkdir lease. The directory stays empty for
// proper-lockfile's rmdir recovery. A replaced lease is never removed by its
// former owner.
func Acquire(ctx context.Context, path string, opts Options) (unlock func(), valid func() bool, err error) {
	staleAfter := opts.StaleAfter
	if staleAfter <= 0 {
		staleAfter = 30 * time.Second
	}
	heartbeat := opts.Heartbeat
	if heartbeat <= 0 {
		heartbeat = 2 * time.Second
	}
	wait := opts.Wait
	if wait <= 0 {
		wait = 5 * time.Second
	}
	waitCtx, cancel := context.WithTimeout(ctx, wait)
	defer cancel()
	for {
		if err := waitCtx.Err(); err != nil {
			if ctx.Err() != nil {
				return nil, nil, ctx.Err()
			}
			return nil, nil, fmt.Errorf("%w: %s", ErrBusy, path)
		}
		err := os.Mkdir(path, 0o700)
		if err == nil {
			break
		}
		// Windows can report access denied during a directory handoff.
		// Retry within the same deadline; ownership still requires mkdir success.
		if !os.IsExist(err) && !(runtime.GOOS == "windows" && os.IsPermission(err)) {
			return nil, nil, fmt.Errorf("create lock %s: %w", path, err)
		}
		recoverStaleLock(path, staleAfter, time.Now())
		timer := time.NewTimer(50 * time.Millisecond)
		select {
		case <-waitCtx.Done():
			timer.Stop()
		case <-timer.C:
		}
	}

	dir, err := os.Open(path)
	if err != nil {
		return nil, nil, fmt.Errorf("%w: %s: %v", ErrInspect, path, err)
	}
	owner, err := dir.Stat()
	if err != nil {
		dir.Close()
		return nil, nil, fmt.Errorf("%w: %s: %v", ErrInspect, path, err)
	}
	var compromised atomic.Bool
	var leaseMu sync.Mutex
	mtime := owner.ModTime()
	validLocked := func() bool {
		st, err := os.Lstat(path)
		return !compromised.Load() && err == nil && os.SameFile(owner, st) && st.ModTime().Equal(mtime)
	}
	valid = func() bool {
		leaseMu.Lock()
		defer leaseMu.Unlock()
		return validLocked()
	}
	stop, done := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(heartbeat)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case now := <-ticker.C:
				leaseMu.Lock()
				ok := validLocked()
				if ok {
					ok = os.Chtimes(path, now, now) == nil
				}
				if ok {
					st, err := os.Lstat(path)
					ok = err == nil && os.SameFile(owner, st)
					if ok {
						mtime = st.ModTime() // retain filesystem timestamp precision
					}
				}
				leaseMu.Unlock()
				if !ok {
					compromised.Store(true)
					return
				}
			}
		}
	}()
	var once sync.Once
	unlock = func() {
		once.Do(func() {
			close(stop)
			<-done
			stillValid := valid()
			// Windows cannot remove a directory with an open handle in it.
			dir.Close()
			if stillValid {
				_ = os.Remove(path)
			}
		})
	}
	return unlock, valid, nil
}

func recoverStaleLock(path string, staleAfter time.Duration, now time.Time) {
	before, err := os.Lstat(path)
	if err != nil || !before.IsDir() || now.Sub(before.ModTime()) <= staleAfter {
		return
	}
	// Re-check identity and type before removal so a fresh replacement or
	// non-directory lock path is never deleted as a stale lease.
	after, err := os.Lstat(path)
	if err != nil || !after.IsDir() || !os.SameFile(before, after) || now.Sub(after.ModTime()) <= staleAfter {
		return
	}
	// Older psi builds left an owner file in the directory. Remove only that
	// known entry, never arbitrary contents.
	_ = os.Remove(filepath.Join(path, "owner"))
	_ = os.Remove(path)
}
