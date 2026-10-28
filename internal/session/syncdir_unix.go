//go:build unix

package session

import "os"

// syncSessionDir makes a new session file durable in its directory entry.
func syncSessionDir(dirPath string) error {
	dir, err := os.Open(dirPath)
	if err != nil {
		return err
	}
	err = dir.Sync()
	closeErr := dir.Close()
	if err != nil {
		return err
	}
	return closeErr
}
