//go:build linux || darwin

package main

import (
	"os"
	"syscall"
)

func secretStoreDatabasePrivate(_ string, info os.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && info.Mode().IsRegular() && info.Mode().Perm()&0o077 == 0 && stat.Uid == uint32(os.Geteuid()) && stat.Nlink == 1
}
