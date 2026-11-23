//go:build !windows

package shellutil

import (
	"os/exec"
	"syscall"
)

// platformPathExists uses access(F_OK) to match Pi's shellPathExists check.
func platformPathExists(path string) bool {
	if path == "" {
		return false
	}
	return syscall.Access(path, 0) == nil
}

func platformLookPath(executable string) string {
	found, err := exec.LookPath(executable)
	if err != nil {
		return ""
	}
	return found
}
