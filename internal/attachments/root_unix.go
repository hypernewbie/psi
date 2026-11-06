//go:build !windows

package attachments

import (
	"os"
	"syscall"
)

// readFlags keeps the open read-only and non-blocking. O_NOFOLLOW is defense
// in depth: the Lstat and opened-identity checks already refuse a symlink.
const readFlags = os.O_RDONLY | syscall.O_NOFOLLOW | syscall.O_NONBLOCK

func supported() bool { return true }

func identity(info os.FileInfo) (dev, ino uint64, ok bool) {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, 0, false
	}
	return uint64(st.Dev), uint64(st.Ino), true
}
