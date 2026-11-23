//go:build windows

package shellutil

import (
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows"
)

// platformPathExists checks path existence via GetFileAttributes instead of
// os.Stat so runnable Windows Store execution aliases (IO_REPARSE_TAG_APPEXECLINK)
// are accepted rather than rejected on reparse-target open.
func platformPathExists(path string) bool {
	if path == "" {
		return false
	}
	ptr, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return false
	}
	attrs, err := windows.GetFileAttributes(ptr)
	return err == nil && attrs != windows.INVALID_FILE_ATTRIBUTES
}

func platformFileExists(path string) bool {
	if path == "" {
		return false
	}
	ptr, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return false
	}
	attrs, err := windows.GetFileAttributes(ptr)
	return err == nil && attrs != windows.INVALID_FILE_ATTRIBUTES && attrs&windows.FILE_ATTRIBUTE_DIRECTORY == 0
}

// platformLookPath searches PATH for executable using platformFileExists so
// Windows Store aliases match, directories are skipped, and lookup does not
// spawn where.exe.
func platformLookPath(executable string) string {
	if executable == "" {
		return ""
	}
	if strings.ContainsAny(executable, `/\`) {
		if platformFileExists(executable) {
			return executable
		}
		return ""
	}
	exts := []string{""}
	if filepath.Ext(executable) == "" {
		pathExt := os.Getenv("PATHEXT")
		if pathExt == "" {
			pathExt = ".COM;.EXE;.BAT;.CMD"
		}
		for _, ext := range filepath.SplitList(pathExt) {
			if ext == "" {
				continue
			}
			if ext[0] != '.' {
				ext = "." + ext
			}
			exts = append(exts, ext)
		}
	}
	for _, dir := range filepath.SplitList(os.Getenv("PATH")) {
		if dir == "" {
			continue
		}
		for _, ext := range exts {
			candidate := filepath.Join(dir, executable+ext)
			if platformFileExists(candidate) {
				return candidate
			}
		}
	}
	return ""
}
