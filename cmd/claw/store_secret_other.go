//go:build !linux && !darwin && !windows

package main

import "os"

func secretStoreDatabasePrivate(string, os.FileInfo) bool { return false }
