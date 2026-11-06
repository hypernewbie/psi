//go:build !linux && !darwin && !windows

package session

import "fmt"

// renameNoReplace has no no-overwrite primitive on this platform.
func renameNoReplace(oldPath, newPath string) error {
	return fmt.Errorf("no-overwrite rename is not supported on this platform")
}
