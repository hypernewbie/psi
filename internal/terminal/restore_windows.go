//go:build windows

package terminal

// RestoreForSuspend matches the Unix suspend path. Windows has no
// suspend; stopSelf reports unsupported before this runs.
func (t *Term) RestoreForSuspend() {
	t.Restore()
}
