//go:build windows

package shellutil

import (
	"os/exec"
	"syscall"
)

// HideWindow prevents spawned console shells from flashing a console window
// on Windows, matching Pi's windowsHide: true option.
func HideWindow(cmd *exec.Cmd) {
	if cmd == nil {
		return
	}
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.HideWindow = true
}
