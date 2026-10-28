//go:build unix

package app

import (
	"os"
	"syscall"
)

// handledSignals lists the process signals the event loop serves.
func handledSignals() []os.Signal {
	return []os.Signal{syscall.SIGINT, syscall.SIGTERM, syscall.SIGWINCH, syscall.SIGTSTP, syscall.SIGCONT, syscall.SIGHUP}
}

func isResize(sig os.Signal) bool  { return sig == syscall.SIGWINCH }
func isSuspend(sig os.Signal) bool { return sig == syscall.SIGTSTP }
func isResume(sig os.Signal) bool  { return sig == syscall.SIGCONT }
func isShutdown(sig os.Signal) bool {
	return sig == syscall.SIGTERM || sig == syscall.SIGHUP
}

// resumeSignal wakes suspend after the shell continues the process.
func resumeSignal() os.Signal { return syscall.SIGCONT }

// stopSelf suspends the process until the shell continues it.
// SIGSTOP cannot race a changed SIGTSTP disposition or be ignored by
// an orphaned process group. SIGCONT resumes exactly after this call.
func stopSelf() error {
	return syscall.Kill(os.Getpid(), syscall.SIGSTOP)
}
