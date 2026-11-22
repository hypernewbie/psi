//go:build !linux && !darwin && !windows

package main

import (
	"fmt"
	"time"
)

// Fail closed until native descriptor ACL verification is available.
func readPrivateSecretFile(path string, maxBytes int64, timeout time.Duration) ([]byte, error) {
	return nil, fmt.Errorf("secret file is unavailable")
}
