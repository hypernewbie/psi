//go:build unix

package terminal

import (
	"os"
	"syscall"

	"golang.org/x/sys/unix"
)

// waitInputReady reports whether the input file has data to read.
// A false return with no error is the 20ms tick that flushes an
// incomplete Escape prefix.
func waitInputReady(f *os.File, timeoutMs int) (bool, error) {
	fds := []unix.PollFd{{Fd: int32(f.Fd()), Events: unix.POLLIN}}
	n, err := unix.Poll(fds, timeoutMs)
	if err == syscall.EINTR {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return n > 0, nil
}
