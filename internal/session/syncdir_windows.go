//go:build windows

package session

import "os"

// syncSessionDir has no directory-sync primitive on Windows: opening a
// directory handle for Sync fails with access denied. The file payload
// itself is still synced by writeDurable; only the directory entry lags
// a crash. Open and close the directory so a missing path still errors.
func syncSessionDir(dirPath string) error {
	dir, err := os.Open(dirPath)
	if err != nil {
		return err
	}
	return dir.Close()
}
