//go:build linux || darwin

package main

import (
	"os"
	"testing"
)

// applyPrivateStoreACL makes the store database readable by its owner only.
// The mode is what the product checks on this platform.
func applyPrivateStoreACL(t *testing.T, path string) {
	t.Helper()
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
}
