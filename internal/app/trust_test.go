package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Pi asks about a folder only when it holds trust-requiring project
// resources. The user skills directory in the home directory is a user
// resource and never asks.
func TestHasTrustRequiringProjectResources(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.MkdirAll(filepath.Join(home, ".agents", "skills"), 0o755); err != nil {
		t.Fatal(err)
	}
	work := t.TempDir()

	if hasTrustRequiringProjectResources(work) {
		t.Fatal("plain folder asked for trust")
	}
	if hasTrustRequiringProjectResources(home) {
		t.Fatal("home folder with user skills asked for trust")
	}
	for _, name := range trustRequiringProjectResources {
		dir := t.TempDir()
		if err := os.MkdirAll(filepath.Join(dir, ".pi", name), 0o755); err != nil {
			t.Fatal(err)
		}
		if !hasTrustRequiringProjectResources(dir) {
			t.Fatalf("%s did not ask for trust", name)
		}
	}
	packages := t.TempDir()
	if err := os.MkdirAll(filepath.Join(packages, ".pi", "packages"), 0o755); err != nil {
		t.Fatal(err)
	}
	if hasTrustRequiringProjectResources(packages) {
		t.Fatal("project packages are not a trust-requiring resource")
	}
	project := filepath.Join(work, "project")
	if err := os.MkdirAll(filepath.Join(project, ".agents", "skills"), 0o755); err != nil {
		t.Fatal(err)
	}
	if !hasTrustRequiringProjectResources(project) {
		t.Fatal("project skills did not ask for trust")
	}
	nested := filepath.Join(project, "nested")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	if !hasTrustRequiringProjectResources(nested) {
		t.Fatal("ancestor project skills did not ask for trust")
	}
}

// The decision for a folder is stored once and the nearest ancestor entry
// decides, like pi's trust store.
func TestProjectTrustStoreRemembersDecision(t *testing.T) {
	agentDir := filepath.Join(t.TempDir(), "agent")
	store := newProjectTrustStore(agentDir)
	work := t.TempDir()
	nested := filepath.Join(work, "nested")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, ok := store.get(work); ok {
		t.Fatal("empty store returned a decision")
	}
	trusted := true
	if err := store.setMany([]projectTrustUpdate{{path: work, decision: &trusted}}); err != nil {
		t.Fatal(err)
	}
	if decision, ok := store.get(work); !ok || !decision {
		t.Fatalf("stored decision = %v, %v", decision, ok)
	}
	if decision, ok := store.get(nested); !ok || !decision {
		t.Fatalf("ancestor decision = %v, %v", decision, ok)
	}
	untrusted := false
	if err := store.setMany([]projectTrustUpdate{{path: work, decision: &untrusted}}); err != nil {
		t.Fatal(err)
	}
	if decision, ok := store.get(work); !ok || decision {
		t.Fatalf("replaced decision = %v, %v", decision, ok)
	}
	raw, err := os.ReadFile(store.path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(string(raw), "}\n") {
		t.Fatalf("trust store format: %q", raw)
	}
	if !strings.Contains(string(raw), "\n  \"") {
		t.Fatalf("trust store is not indented: %q", raw)
	}
	// A null decision removes the entry for the path.
	if err := store.setMany([]projectTrustUpdate{{path: work, decision: nil}}); err != nil {
		t.Fatal(err)
	}
	if _, ok := store.get(work); ok {
		t.Fatal("removed decision was returned")
	}
}

// Trusting the parent folder stores the parent decision and drops the
// decision for the folder itself.
func TestProjectTrustParentFolderDecision(t *testing.T) {
	agentDir := filepath.Join(t.TempDir(), "agent")
	store := newProjectTrustStore(agentDir)
	work := t.TempDir()
	options := projectTrustOptions(work)
	if len(options) != 5 {
		t.Fatalf("trust options = %+v", options)
	}
	labels := []string{"Trust", "Trust parent folder (" + filepath.Dir(canonicalTrustPath(work)) + ")", "Trust (this session only)", "Do not trust", "Do not trust (this session only)"}
	for i, option := range options {
		if option.label != labels[i] {
			t.Fatalf("option %d = %q, want %q", i, option.label, labels[i])
		}
	}
	if err := store.setMany(options[1].updates); err != nil {
		t.Fatal(err)
	}
	if decision, ok := store.get(work); !ok || !decision {
		t.Fatalf("parent decision = %v, %v", decision, ok)
	}
}

// The startup decision follows pi: an override wins, then the stored
// decision, then the defaultProjectTrust setting, then no trust without a
// prompt.
func TestResolveProjectTrustOrder(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	agentDir := filepath.Join(home, "agent")
	work := t.TempDir()
	if err := os.MkdirAll(filepath.Join(work, ".pi", "skills"), 0o755); err != nil {
		t.Fatal(err)
	}
	if trusted, err := resolveProjectTrust(Options{}, work, agentDir, "ask", nil, nil); err != nil || trusted {
		t.Fatalf("undecided folder = %v, %v", trusted, err)
	}
	if trusted, err := resolveProjectTrust(Options{AllowProj: true}, work, agentDir, "never", nil, nil); err != nil || !trusted {
		t.Fatalf("approved folder = %v, %v", trusted, err)
	}
	if trusted, err := resolveProjectTrust(Options{DenyProj: true}, work, agentDir, "always", nil, nil); err != nil || trusted {
		t.Fatalf("denied folder = %v, %v", trusted, err)
	}
	if trusted, err := resolveProjectTrust(Options{}, work, agentDir, "always", nil, nil); err != nil || !trusted {
		t.Fatalf("always setting = %v, %v", trusted, err)
	}
	if trusted, err := resolveProjectTrust(Options{}, work, agentDir, "never", nil, nil); err != nil || trusted {
		t.Fatalf("never setting = %v, %v", trusted, err)
	}
	store := newProjectTrustStore(agentDir)
	trusted := true
	if err := store.setMany([]projectTrustUpdate{{path: work, decision: &trusted}}); err != nil {
		t.Fatal(err)
	}
	if decision, err := resolveProjectTrust(Options{}, work, agentDir, "never", nil, nil); err != nil || !decision {
		t.Fatalf("stored decision = %v, %v", decision, err)
	}
	plain := t.TempDir()
	if decision, err := resolveProjectTrust(Options{}, plain, agentDir, "never", nil, nil); err != nil || !decision {
		t.Fatalf("folder without project resources = %v, %v", decision, err)
	}
}
