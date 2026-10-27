package provider

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"psi/internal/testutil"
)

// Explicit test input, never an inherited opt-in or a private installation path.
var testProperLockfile = flag.String("psi-test-proper-lockfile", "", "path to public proper-lockfile 4.1.2 for optional Pi auth interoperability tests")

func properLockfileReference(t *testing.T) (string, string) {
	t.Helper()
	if *testProperLockfile == "" {
		t.Skip("Pi auth reference cell blocked: supply -psi-test-proper-lockfile")
	}
	path, err := filepath.Abs(*testProperLockfile)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(path, "package.json"))
	if err != nil {
		t.Fatal("cannot read public proper-lockfile manifest")
	}
	var manifest struct{ Name, Version string }
	if json.Unmarshal(data, &manifest) != nil || manifest.Name != "proper-lockfile" || manifest.Version != "4.1.2" {
		t.Fatal("reference must be public proper-lockfile 4.1.2")
	}
	node, err := exec.LookPath("node")
	if err != nil {
		t.Fatal("Node is required for the explicitly requested reference test")
	}
	return path, node
}

func runPiAuthReference(t *testing.T, authPath, mode string) (*bufio.Scanner, func()) {
	t.Helper()
	module, node := properLockfileReference(t)
	dir := t.TempDir()
	script := `const lockfile = require(process.argv[2]);
const fs = require('fs');
const path = process.argv[3];
(async () => {
  const release = await lockfile.lock(path, { realpath: false, stale: 30000 });
  console.log('ACQUIRED');
  if (process.argv[4] === 'writer') {
    const final = fs.readFileSync(path, 'utf8');
    fs.writeFileSync(path, '{"openai-code');
    console.log('TRUNCATED');
    await new Promise(r => setTimeout(r, 500));
    fs.writeFileSync(path, final);
  }
  await release();
  console.log('RELEASED');
})().catch(() => { process.exitCode = 1; });`
	file := filepath.Join(dir, "pi-auth.cjs")
	if err := os.WriteFile(file, []byte(script), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	cmd := exec.CommandContext(ctx, node, file, module, authPath, mode)
	cmd.Env = testutil.ChildEnv(dir, map[string]string{"LANG": "C.UTF-8"})
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		cancel()
		t.Fatal(err)
	}
	var once sync.Once
	var waitErr error
	wait := func() { once.Do(func() { waitErr = cmd.Wait() }) }
	t.Cleanup(func() { cancel(); wait() })
	return bufio.NewScanner(stdout), func() {
		wait()
		if waitErr != nil {
			t.Fatalf("synthetic Pi auth reference failed: %v", waitErr)
		}
	}
}

func piAuthMarker(t *testing.T, scanner *bufio.Scanner, marker string) {
	t.Helper()
	for scanner.Scan() {
		if strings.TrimSpace(scanner.Text()) == marker {
			return
		}
	}
	t.Fatalf("Pi reference did not emit %q", marker)
}

func TestMixedPiPsiAuthContention(t *testing.T) {
	properLockfileReference(t)
	authPath := filepath.Join(t.TempDir(), "auth.json")
	writeAuthDoc(t, authPath, codexDoc("pi-final", "pi-refresh", time.Now().Add(time.Hour).UnixMilli()))
	scanner, wait := runPiAuthReference(t, authPath, "writer")
	piAuthMarker(t, scanner, "TRUNCATED")
	done := make(chan error, 1)
	go func() { done <- (&CodexAuth{Path: authPath}).Check() }()
	select {
	case err := <-done:
		t.Fatalf("psi read during Pi-held torn write: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	piAuthMarker(t, scanner, "RELEASED")
	wait()
	if err := <-done; err != nil {
		t.Fatalf("psi read after Pi release: %v", err)
	}
}

func TestMixedPiReclaimsAbandonedPsiLease(t *testing.T) {
	properLockfileReference(t)
	authPath := filepath.Join(t.TempDir(), "auth.json")
	writeAuthDoc(t, authPath, codexDoc("test-access", "test-refresh", time.Now().Add(time.Hour).UnixMilli()))
	unlock, valid, err := acquireAuthLock(context.Background(), authPath+".lock", 0, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	// A stopped/killed holder has no heartbeat. Age its real lease without
	// waiting 30 seconds. Keep its inode pinned to exercise former ownership.
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(authPath+".lock", old, old); err != nil {
		t.Fatal(err)
	}
	scanner, wait := runPiAuthReference(t, authPath, "takeover")
	piAuthMarker(t, scanner, "ACQUIRED")
	piAuthMarker(t, scanner, "RELEASED")
	wait()
	if valid() {
		t.Fatal("former psi owner accepted the Pi replacement lease")
	}
}
