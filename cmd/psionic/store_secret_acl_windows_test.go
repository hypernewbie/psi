//go:build windows

package main

import (
	"testing"
)

// applyPrivateStoreACL makes the store database readable by its owner only.
// On Windows the product checks the discretionary access control list, so
// the test has to set one instead of a mode.
func applyPrivateStoreACL(t *testing.T, path string) {
	t.Helper()
	setTestSecretACL(t, path, "")
}
