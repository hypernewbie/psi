package config

import (
	"fmt"
	"os"
)

// lockSettings takes a non-blocking advisory lock for psi-side settings
// writers. The sidecar name avoids Pi's proper-lockfile directory, which
// owns settings.json.lock.
func lockSettings(path string) (func(), error) {
	lockPath := path + ".psi.lock"
	file, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open settings lock: %w", err)
	}
	if err := lockFileExclusive(file); err != nil {
		file.Close()
		return nil, fmt.Errorf("settings file is locked by another psi process")
	}
	return func() {
		_ = unlockFile(file)
		_ = file.Close()
	}, nil
}
