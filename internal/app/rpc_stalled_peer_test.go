//go:build (unix || windows) && acceptance

package app

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// TestRPCBashStalledPeerActualApp leaves stdout unread, so the first large
// bash_execution_update cannot complete. The frame watchdog must fail the
// connection within its 10-second bound instead of dropping the frame and
// serving forever.
func TestRPCBashStalledPeerActualApp(t *testing.T) {
	binary := rpcTestBinary()
	if binary == "" {
		t.Skip("stalled-peer proof requires -psi-test-binary")
	}
	home, work := t.TempDir(), t.TempDir()
	agentDir := filepath.Join(home, "agent")
	sessionDir := filepath.Join(home, "sessions")
	if err := os.MkdirAll(filepath.Join(home, "tmp"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(sessionDir, 0o700); err != nil {
		t.Fatal(err)
	}
	args := []string{"--mode", "rpc", "--deny-project", "--no-tools", "--session-dir", sessionDir, "--session-id", "stalled-peer"}
	cmd := exec.Command(binary, args...)
	cmd.Dir = work
	cmd.Env = appTestEnv(home, agentDir)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = stdin.Close()
		_ = stdout.Close()
	}()
	command := map[string]any{"id": "stalled-1", "type": "bash", "command": "i=0; while [ $i -lt 20000 ]; do printf '%064d\\n' 0; i=$((i+1)); done"}
	line, _ := json.Marshal(command)
	if _, err := stdin.Write(append(line, '\n')); err != nil {
		t.Fatal(err)
	}
	// Do not read stdout. The child must cancel the blocked frame, settle the
	// shell, and exit on its own.
	start := time.Now()
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		elapsed := time.Since(start)
		if elapsed < 9*time.Second {
			t.Fatalf("child exited after %s, before the frame watchdog: err=%v stderr=%s", elapsed, err, stderr.String())
		}
		if elapsed > 20*time.Second {
			t.Fatalf("child exceeded the frame watchdog: %s", elapsed)
		}
	case <-time.After(25 * time.Second):
		_ = cmd.Process.Kill()
		<-done
		t.Fatalf("child stayed blocked with unread stdout; stderr=%s", stderr.String())
	}
}
