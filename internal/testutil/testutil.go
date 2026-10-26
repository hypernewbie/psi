// Package testutil provides deterministic helpers for offline tests.
package testutil

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TempHome sets HOME to a temp dir for the test (isolated agent dir).
func TempHome(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("PSI_CODING_AGENT_DIR", filepath.Join(dir, ".pi", "agent"))
	return dir
}

// TempWork creates an isolated workspace directory.
func TempWork(t *testing.T) string {
	t.Helper()
	return t.TempDir()
}

// FixedClock returns a deterministic timestamp helper.
func FixedClock(start time.Time) func() time.Time {
	cur := start
	return func() time.Time {
		out := cur
		cur = cur.Add(time.Second)
		return out
	}
}

// WriteFile writes a file, creating parents.
func WriteFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// MustRead reads a file or fails the test.
func MustRead(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

var _ = os.Getenv
