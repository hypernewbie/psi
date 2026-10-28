//go:build linux

package terminal

import (
	"golang.org/x/sys/unix"
)

// RestoreForSuspend restores the saved cooked state before suspend and
// clears the BSD PENDIN flag. macOS sets PENDIN when a suspended
// foreground group leaves input semantics pending; it is driver
// bookkeeping, never an application setting, and raw-mode reacquire
// clears it on resume regardless. It is exported for the application
// suspend path.
func (t *Term) RestoreForSuspend() {
	if t == nil {
		return
	}
	t.Restore()
	st, err := unix.IoctlGetTermios(t.fd, unix.TCGETS)
	if err != nil {
		return
	}
	st.Lflag &^= unix.PENDIN
	_ = unix.IoctlSetTermios(t.fd, unix.TCSETS, st)
}
