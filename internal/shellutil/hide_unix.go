//go:build !windows

package shellutil

import "os/exec"

// HideWindow is a no-op outside Windows.
func HideWindow(*exec.Cmd) {}
