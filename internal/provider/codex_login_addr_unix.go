//go:build !windows

package provider

import (
	"errors"
	"syscall"
)

// codexLoginAddrInUse reports the platform-native "address already in use"
// error. Permission, invalid-address, and other bind failures are not busy.
func codexLoginAddrInUse(err error) bool {
	return errors.Is(err, syscall.EADDRINUSE)
}
