//go:build unix

package app

import (
	"os"
	"syscall"
	"testing"
)

// shellSuspendCase is Unix-only because Windows has no SIGTSTP.
var shellSuspendCase = &shellExitCase{
	name: "suspend",
	action: func(t *testing.T, _ *os.File, signals chan os.Signal, _ *shellWriter) {
		signals <- syscall.SIGTSTP
	},
	suspendErr: true,
}
