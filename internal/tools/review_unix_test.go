//go:build unix

package tools

// Unix-only shell regressions: escaped-pipe release and post-exit
// cancellation depend on setsid and process-group kills, which Windows
// lacks. Windows stops the leader only (see proc_windows.go).

import (
	"context"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestReviewEscapedQuietPipeCannotHoldReader(t *testing.T) {
	if _, err := exec.LookPath("setsid"); err != nil {
		t.Skip("setsid unavailable for inherited-pipe fixture")
	}
	dir := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan Result, 1)
	go func() {
		done <- NewRegistry(dir).DispatchCtx(ctx, Invocation{Name: Bash, Arguments: map[string]any{
			"command": "setsid bash -c 'echo $$ > escaped.pid; sleep 30' &",
		}})
	}()
	pid := reviewWaitPID(t, filepath.Join(dir, "escaped.pid"))
	t.Cleanup(func() { _ = syscall.Kill(-pid, syscall.SIGKILL) })
	select {
	case res := <-done:
		if res.IsError {
			t.Fatalf("quiet inherited pipe changed the leader status: %+v", res)
		}
	case <-time.After(time.Second):
		_ = syscall.Kill(-pid, syscall.SIGKILL)
		<-done
		t.Fatal("pipe reader waited for an escaped descendant after idle drain")
	}
}

func TestReviewCancellationAfterLeaderExit(t *testing.T) {
	dir := t.TempDir()
	pidPath := filepath.Join(dir, "leader.pid")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan Result, 1)
	go func() {
		done <- NewRegistry(dir).DispatchCtx(ctx, Invocation{Name: Bash, Arguments: map[string]any{
			"command": "echo $$ > leader.pid; (for ((i=0;i<200;i++)); do printf x; sleep 0.02; done) &",
		}})
	}()
	pid := reviewWaitPID(t, pidPath)
	t.Cleanup(func() { _ = syscall.Kill(-pid, syscall.SIGKILL) })
	// The shell has no wait command. Its background child keeps the drain
	// active after the leader exits, unlike the earlier '& wait' regression.
	deadline := time.Now().Add(2 * time.Second)
	for {
		if err := syscall.Kill(pid, 0); err == syscall.ESRCH {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("fixture leader did not exit")
		}
		time.Sleep(time.Millisecond)
	}
	cancel()
	select {
	case res := <-done:
		if !res.IsError || !strings.Contains(strings.ToLower(res.Content[0].Text), "cancel") {
			t.Fatalf("cancellation lost: %+v", res)
		}
	case <-time.After(500 * time.Millisecond):
		_ = syscall.Kill(-pid, syscall.SIGKILL)
		<-done
		t.Fatal("cancellation during post-exit drain did not stop the descendants")
	}
}
