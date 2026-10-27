package provider

// P3-012: mixed Pi/psi contention on the shared auth lease.
//
// The Pi role runs pinned proper-lockfile 4.1.2 (the installed public
// package) against its own lock acquisition, heartbeat, and in-place write.
// The psi role reads through acquireAuthLock/loadShared. Neither side may
// inspect a torn document. Skipped when node or the pinned dependency is
// unavailable (recorded as a blocked cell, not a pass).

import (
	"bufio"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const properLockfilePath = "/home/hypernewbie/.nvm/versions/node/v24.11.1/lib/node_modules/@earendil-works/pi-coding-agent/node_modules/proper-lockfile"

func TestMixedPiPsiAuthContention(t *testing.T) {
	if _, err := os.Stat(properLockfilePath + "/package.json"); err != nil {
		t.Skip("pinned proper-lockfile unavailable: blocked cell")
	}
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node unavailable: blocked cell")
	}
	dir := t.TempDir()
	authPath := filepath.Join(dir, "auth.json")
	final := codexDoc("pi-final-access", "pi-final-refresh", time.Now().Add(time.Hour).UnixMilli())
	writeAuthDoc(t, authPath, codexDoc("old", "old", time.Now().Add(time.Hour).UnixMilli()))

	script := `const lockfile = require(` + "`" + properLockfilePath + "`" + `);
const fs = require('fs');
const path = process.argv[2];
const final = fs.readFileSync(process.argv[3], 'utf8');
(async () => {
  const release = await lockfile.lock(path, { realpath: false, stale: 30000 });
  fs.writeFileSync(path, '{"openai-code');
  console.log('TRUNCATED');
  await new Promise(r => setTimeout(r, 800));
  fs.writeFileSync(path, final);
  console.log('COMPLETE');
  await release();
  console.log('RELEASED');
})().catch(e => { console.error('NODE-FAIL ' + e.message); process.exit(1); });`
	finalPath := filepath.Join(dir, "final.json")
	if err := os.WriteFile(finalPath, []byte(final), 0o600); err != nil {
		t.Fatal(err)
	}
	scriptPath := filepath.Join(dir, "pi-writer.js")
	if err := os.WriteFile(scriptPath, []byte(script), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(node, scriptPath, authPath, finalPath)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	sc := bufio.NewScanner(stdout)
	waitMarker := func(want string) {
		deadline := time.After(15 * time.Second)
		for {
			select {
			case <-deadline:
				t.Fatalf("missing %q from Pi writer (stderr: %s)", want, stderr.String())
			default:
			}
			if !sc.Scan() {
				t.Fatalf("Pi writer exited early (stderr: %s)", stderr.String())
			}
			if strings.TrimSpace(sc.Text()) == want {
				return
			}
		}
	}
	waitMarker("TRUNCATED")
	// The psi reader must block on the Pi-held lease, then see the document.
	done := make(chan error, 1)
	go func() { done <- (&CodexAuth{Path: authPath}).Check() }()
	select {
	case err := <-done:
		t.Fatalf("psi Check returned during Pi-held torn write: %v", err)
	case <-time.After(300 * time.Millisecond):
	}
	waitMarker("COMPLETE")
	waitMarker("RELEASED")
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("psi Check after Pi release: %v", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("psi Check blocked past Pi release")
	}
	if err := cmd.Wait(); err != nil {
		t.Fatalf("Pi writer failed: %v (stderr: %s)", err, stderr.String())
	}
}
