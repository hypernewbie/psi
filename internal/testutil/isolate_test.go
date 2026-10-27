package testutil

import (
	"os"
	"path/filepath"
	"testing"
)

// P2-006: isolated HOME/subprocess env + canaries.
func TestIsolationCanaries(t *testing.T) {
	home, _, _, _ := IsolatedEnv(t)
	if os.Getenv("PSI_TEST_CANARY_PARENT_SECRET") == "" {
		t.Fatal("canary not set in test env")
	}
	child := ChildEnv(home, map[string]string{"FOO": "bar"})
	// Child env must not inherit parent secrets/proxy/live flags.
	for _, kv := range child {
		if len(kv) >= 23 && kv[:23] == "PSI_TEST_CANARY_PARENT_" {
			t.Fatalf("child inherits canary: %q", kv[:30])
		}
	}
	// Artifact check refuses canary leaks.
	p := filepath.Join(home, "artifact.txt")
	os.WriteFile(p, []byte("hello canary-parent-secret-0000"), 0o644)
	AssertNoArtifactLeak(t, []string{filepath.Join(home, "missing.txt")}) // missing ok
	if err := artifactLeak(p); err == nil {
		t.Fatal("artifact scanner accepted an actual canary leak")
	}
	if err := os.WriteFile(p, []byte("safe synthetic artifact"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := artifactLeak(p); err != nil {
		t.Fatal("artifact scanner rejected safe content")
	}
}
