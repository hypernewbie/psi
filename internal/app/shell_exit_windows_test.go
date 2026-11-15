//go:build windows

package app

// The remaining exit paths still run on Windows; only the suspend case is
// Unix-specific.
var shellSuspendCase *shellExitCase
