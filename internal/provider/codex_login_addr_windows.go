//go:build windows

package provider

import (
	"errors"

	"golang.org/x/sys/windows"
)

// codexLoginAddrInUse reports the platform-native "address already in use"
// error. Windows network errors use WSAEADDRINUSE, not syscall.EADDRINUSE.
func codexLoginAddrInUse(err error) bool {
	return errors.Is(err, windows.WSAEADDRINUSE)
}
