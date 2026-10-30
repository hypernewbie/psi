package main

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestBinarySmoke(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping binary smoke test in short mode")
	}

	binDir := t.TempDir()
	binPath := filepath.Join(binDir, "psi")

	// Build binary
	buildCmd := exec.Command("go", "build", "-o", binPath, ".")
	if out, err := buildCmd.CombinedOutput(); err != nil {
		t.Fatalf("go build failed: %v\nOutput: %s", err, string(out))
	}

	// 1. --version
	cmd := exec.Command(binPath, "--version")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("--version failed: %v\nOutput: %s", err, string(out))
	}
	if !strings.Contains(string(out), "psi "+version) {
		t.Errorf("expected version output containing %q, got: %s", version, string(out))
	}

	// 2. --help
	cmd = exec.Command(binPath, "--help")
	out, err = cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("--help failed: %v\nOutput: %s", err, string(out))
	}
	if !strings.Contains(string(out), "Usage:") {
		t.Errorf("expected help output containing 'Usage:', got: %s", string(out))
	}

	// 3. Bad flag should exit with code 1
	cmd = exec.Command(binPath, "--definitely-invalid-flag-12345")
	out, err = cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("expected error for bad flag, got nil. Output: %s", string(out))
	}
	if exitErr, ok := err.(*exec.ExitError); !ok || exitErr.ExitCode() != 1 {
		t.Errorf("expected exit code 1, got: %v", err)
	}
}

func TestBinaryDoesNotLinkGoleak(t *testing.T) {
	cmd := exec.Command("go", "list", "-deps", ".")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("go list -deps failed: %v\nOutput: %s", err, string(out))
	}
	for _, line := range strings.Split(string(out), "\n") {
		if strings.Contains(line, "goleak") {
			t.Fatalf("real binary must not depend on goleak: %s", line)
		}
	}
}
