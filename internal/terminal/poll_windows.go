//go:build windows

package terminal

import (
	"os"

	"golang.org/x/sys/windows"
)

// waitInputReady reports whether the input handle is signaled.
// WaitForSingleObject serves console input handles. Other handle kinds
// return their wait error to the caller.
func waitInputReady(f *os.File, timeoutMs int) (bool, error) {
	r, err := windows.WaitForSingleObject(windows.Handle(f.Fd()), uint32(timeoutMs))
	if err != nil {
		return false, err
	}
	switch uint32(r) {
	case windows.WAIT_OBJECT_0:
		return true, nil
	case uint32(windows.WAIT_TIMEOUT):
		return false, nil
	default:
		return false, windows.Errno(r)
	}
}
