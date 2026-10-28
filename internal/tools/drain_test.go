package tools

// Phase E regressions for PLAN3 F07: post-exit drain follows the Pi idle
// policy (100ms grace re-armed by every chunk) instead of a fixed deadline.
//
// Pi reference (recorded survey behavior): 16 chunks at 40ms gaps complete
// (~671ms total, exit success); a quiet inherited pipe releases with the
// leader status instead of failing the command.

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestF07ActiveDescendantOutputCompletes(t *testing.T) {
	if testing.Short() {
		t.Skip("shell drain timing")
	}
	dir := t.TempDir()
	reg := NewRegistry(dir)
	start := time.Now()
	res := reg.Dispatch(Invocation{ID: "b1", Name: "bash", Arguments: map[string]any{
		// 10ms gaps leave ample margin under the 100ms idle grace on
		// loaded CI hosts. 40ms gaps proved too tight on shared runners.
		"command": "for i in $(seq 1 16); do echo chunk-$i; sleep 0.01; done &",
	}})
	elapsed := time.Since(start)
	if res.IsError {
		t.Fatalf("active drain failed: %s", res.Content[0].Text)
	}
	text := res.Content[0].Text
	for i := 1; i <= 16; i++ {
		if !strings.Contains(text, "chunk-"+itoaF07(i)) {
			t.Fatalf("missing chunk-%d in %q", i, text)
		}
	}
	if strings.Contains(text, "WaitDelay") {
		t.Fatalf("fixed-deadline error leaked: %q", text)
	}
	t.Logf("16 chunks drained in %v", elapsed)
}

func TestF07QuietPipeReleasesWithLeaderStatus(t *testing.T) {
	if testing.Short() {
		t.Skip("shell drain timing")
	}
	dir := t.TempDir()
	reg := NewRegistry(dir)
	start := time.Now()
	res := reg.Dispatch(Invocation{ID: "b1", Name: "bash", Arguments: map[string]any{
		"command": "sleep 30 &",
	}})
	elapsed := time.Since(start)
	if res.IsError {
		t.Fatalf("quiet pipe must preserve leader success: %s", res.Content[0].Text)
	}
	if elapsed > 10*time.Second {
		t.Fatalf("quiet pipe held too long: %v", elapsed)
	}
	t.Logf("quiet pipe released in %v", elapsed)
}

func TestF07QuietPipePreservesLeaderFailure(t *testing.T) {
	dir := t.TempDir()
	reg := NewRegistry(dir)
	res := reg.Dispatch(Invocation{ID: "b1", Name: "bash", Arguments: map[string]any{
		"command": "echo part-out; echo part-err >&2; sleep 30 & exit 3",
	}})
	if !res.IsError || !strings.Contains(res.Content[0].Text, "exit 3") {
		t.Fatalf("leader failure lost: %+v", res)
	}
	if !strings.Contains(res.Content[0].Text, "part-out") {
		t.Fatalf("stdout lost: %q", res.Content[0].Text)
	}
}

func TestF07StderrOnlyAndPartialLine(t *testing.T) {
	dir := t.TempDir()
	reg := NewRegistry(dir)
	res := reg.Dispatch(Invocation{ID: "b1", Name: "bash", Arguments: map[string]any{
		"command": "printf 'no-newline'; echo err-only >&2",
	}})
	if res.IsError {
		t.Fatalf("stderr-only failed: %s", res.Content[0].Text)
	}
	if !strings.Contains(res.Content[0].Text, "no-newline") || !strings.Contains(res.Content[0].Text, "err-only") {
		t.Fatalf("streams lost: %q", res.Content[0].Text)
	}
}

func TestF07CancelDuringDrainKillsGroup(t *testing.T) {
	if testing.Short() {
		t.Skip("shell drain timing")
	}
	// Windows stops the leader only; descendants outlive cancellation
	// (see proc_windows.go).
	if runtime.GOOS == "windows" {
		t.Skip("process-group cleanup is Unix-only")
	}
	dir := t.TempDir()
	reg := NewRegistry(dir)
	marker := filepath.Join(dir, "chunks.log")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan Result, 1)
	go func() {
		done <- reg.DispatchCtx(ctx, Invocation{ID: "b1", Name: "bash", Arguments: map[string]any{
			"command": "for i in $(seq 1 100); do echo $i >> " + marker + "; sleep 0.05; done & wait",
		}})
	}()
	time.Sleep(300 * time.Millisecond)
	cancel()
	res := <-done
	if !res.IsError || !strings.Contains(strings.ToLower(res.Content[0].Text), "cancel") {
		t.Fatalf("expected cancellation: %+v", res)
	}
	before, _ := os.ReadFile(marker)
	time.Sleep(700 * time.Millisecond)
	after, _ := os.ReadFile(marker)
	if len(after) != len(before) {
		t.Fatalf("descendant survived cancellation: %d -> %d bytes", len(before), len(after))
	}
}

func TestF07TimeoutDuringDrain(t *testing.T) {
	if testing.Short() {
		t.Skip("shell drain timing")
	}
	dir := t.TempDir()
	reg := NewRegistry(dir)
	res := reg.Dispatch(Invocation{ID: "b1", Name: "bash", Arguments: map[string]any{
		"command": "sleep 30 & wait", "timeout": 0.3,
	}})
	if !res.IsError || !strings.Contains(strings.ToLower(res.Content[0].Text), "timed out") {
		t.Fatalf("expected timeout: %+v", res)
	}
}

func itoaF07(i int) string {
	if i == 0 {
		return "0"
	}
	var b []byte
	for i > 0 {
		b = append([]byte{byte('0' + i%10)}, b...)
		i /= 10
	}
	return string(b)
}

// TestF53SpoolFailure injects a capture-descriptor failure: the error is
// recorded, the run is cancelled, and no output is trusted.
func TestF53SpoolFailure(t *testing.T) {
	dir := t.TempDir()
	f, err := os.CreateTemp("", "psi-spool-*")
	if err != nil {
		t.Fatal(err)
	}
	name := f.Name()
	f.Close()
	os.Remove(name)
	// Reopen closed semantics: use a file then close its descriptor.
	f2, err := os.Create(name)
	if err != nil {
		t.Fatal(err)
	}
	cancelled := false
	cap := &tailCapture{file: f2, cancel: func() { cancelled = true }}
	_ = f2.Close() // descriptor failures from here on
	if _, err := cap.Write([]byte("chunk")); err == nil {
		t.Fatal("expected capture error")
	}
	if cap.err == nil {
		t.Fatal("capture error not recorded")
	}
	if !cancelled {
		t.Fatal("capture failure did not cancel the run")
	}
	_ = dir
}
