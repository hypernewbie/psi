//go:build windows

package terminal

import (
	"os"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

var procPeekNamedPipe = windows.NewLazySystemDLL("kernel32.dll").NewProc("PeekNamedPipe")

// peekNamedPipe reports how many bytes a pipe handle has ready to read.
func peekNamedPipe(h windows.Handle, avail *uint32) error {
	r, _, err := procPeekNamedPipe.Call(
		uintptr(h), 0, 0, 0, uintptr(unsafe.Pointer(avail)), 0,
	)
	if r == 0 {
		return err
	}
	return nil
}

// waitInputReady reports whether the input handle has data to read.
// WaitForSingleObject serves synchronous console input handles. Pipe handles
// created by the Go runtime are opened overlapped, and overlapped handles are
// always signaled, so pipes are gated with PeekNamedPipe instead of a wait.
func waitInputReady(f *os.File, timeoutMs int) (bool, error) {
	h := windows.Handle(f.Fd())
	if fileType, err := windows.GetFileType(h); err == nil && fileType == windows.FILE_TYPE_PIPE {
		deadline := time.Now().Add(time.Duration(timeoutMs) * time.Millisecond)
		var avail uint32
		for {
			if err := peekNamedPipe(h, &avail); err != nil {
				return false, err
			}
			if avail > 0 {
				return true, nil
			}
			if timeoutMs <= 0 || time.Now().After(deadline) {
				return false, nil
			}
			time.Sleep(time.Millisecond)
		}
	}
	r, err := windows.WaitForSingleObject(h, uint32(timeoutMs))
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
