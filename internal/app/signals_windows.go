//go:build windows

package app

import (
	"fmt"
	"os"
)

// Windows delivers no SIGWINCH, SIGTSTP, SIGCONT, SIGHUP, or SIGSTOP.
// Resize, suspend, and hangup handling stay Unix-only. Interrupt and
// terminate still stop a run or exit the application.
func handledSignals() []os.Signal {
	return []os.Signal{os.Interrupt}
}

func isResize(os.Signal) bool  { return false }
func isSuspend(os.Signal) bool { return false }
func isResume(os.Signal) bool  { return false }
func isShutdown(sig os.Signal) bool {
	return sig == os.Interrupt
}

// resumeSignal never arrives on Windows. stopSelf reports unsupported
// first, so no code path waits on this value.
func resumeSignal() os.Signal { return os.Interrupt }

func stopSelf() error {
	return fmt.Errorf("suspend unavailable on Windows")
}
