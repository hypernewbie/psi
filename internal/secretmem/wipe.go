// Package secretmem provides explicit cleanup for application-owned secret
// byte buffers.
package secretmem

import "runtime"

// Wipe clears exactly the supplied byte range. The caller must own the range
// and finish all live uses before calling Wipe.
func Wipe(b []byte) {
	clear(b)
	// KeepAlive prevents the compiler from dropping the explicit clear when
	// the backing buffer has no later observable use.
	runtime.KeepAlive(b)
}
