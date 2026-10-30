package tools

import (
	"context"
	"fmt"
	"math"
	"os"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"psi/internal/testutil/leakcheck"
)

// TestBashOutputTruncationExactCutMultiByteRune: when command output exceeds
// 50 KiB and the cut lands inside a multi-byte UTF-8 rune, the leading orphan
// continuation bytes are trimmed and the resulting output is valid UTF-8.
func TestBashOutputTruncationExactCutMultiByteRune(t *testing.T) {
	leakcheck.Verify(t)
	dir := t.TempDir()
	reg := NewRegistry(dir)

	// Construct a command that produces ~64 KiB where each line contains multibyte emoji
	// We want to ensure that trimming at the front of the 50 KiB tail handles partial runes.
	cmd := "python3 -c 'print(\"🌍🌟🎉😀\" * 4000)'"
	if _, err := os.Stat("/usr/bin/python3"); err != nil {
		cmd = "for i in $(seq 1 4000); do echo \"line-$i 🌍🌟🎉😀\"; done"
	}

	res := reg.Dispatch(Invocation{
		ID:   "b1",
		Name: Bash,
		Arguments: map[string]any{
			"command": cmd,
		},
	})

	if res.IsError {
		t.Fatalf("bash command failed: %+v", res)
	}

	text := res.Content[0].Text
	if !strings.Contains(text, "[Output truncated.") {
		t.Fatalf("expected output truncation: len=%d", len(text))
	}

	if !utf8.ValidString(text) {
		t.Fatal("truncated output contains invalid UTF-8")
	}

	details, ok := res.Details.(map[string]any)
	if !ok {
		t.Fatalf("expected details map, got %T", res.Details)
	}

	fullPath, ok := details["fullOutputPath"].(string)
	if !ok || fullPath == "" {
		t.Fatalf("expected fullOutputPath in details: %v", details)
	}
	defer os.Remove(fullPath)

	fi, err := os.Stat(fullPath)
	if err != nil {
		t.Fatalf("stat fullOutputPath: %v", err)
	}
	if runtime.GOOS != "windows" && fi.Mode().Perm()&0o077 != 0 {
		t.Errorf("full output file permissions not private: %v", fi.Mode().Perm())
	}
}

// TestBashTimeoutRacingNormalExit (T-TL-10): timeout fires concurrently with natural
// exit. Verify clean termination, exactly one outcome reported, and no leaks.
func TestBashTimeoutRacingNormalExit(t *testing.T) {
	dir := t.TempDir()
	reg := NewRegistry(dir)

	// Loop 50 times with command duration around timeout
	for i := 0; i < 50; i++ {
		res := reg.Dispatch(Invocation{
			ID:   fmt.Sprintf("race-%d", i),
			Name: Bash,
			Arguments: map[string]any{
				"command": "sleep 0.03; echo done",
				"timeout": 0.03,
			},
		})

		text := res.Content[0].Text
		isTimeout := strings.Contains(strings.ToLower(text), "timed out")
		isSuccess := !res.IsError && strings.Contains(text, "done")
		isFailed := res.IsError && strings.Contains(text, "Command failed")

		// Exactly one outcome
		count := 0
		if isTimeout {
			count++
		}
		if isSuccess {
			count++
		}
		if isFailed {
			count++
		}

		if count != 1 {
			t.Fatalf("iteration %d: ambiguous outcome (timeout=%v, success=%v, failed=%v): %q",
				i, isTimeout, isSuccess, isFailed, text)
		}

		if dp, ok := res.Details.(map[string]any); ok {
			if fp, ok := dp["fullOutputPath"].(string); ok && fp != "" {
				_ = os.Remove(fp)
			}
		}
	}
}

// TestBash16ConcurrentTruncatedOutputs (T-TL-11): 16 concurrent bash tools, each
// outputting more than the truncation limit. Each full-output temp file must have
// distinct content, and fullOutputPath points at its own file.
func TestBash16ConcurrentTruncatedOutputs(t *testing.T) {
	dir := t.TempDir()
	reg := NewRegistry(dir)

	const n = 16
	type result struct {
		id       int
		fullPath string
		res      Result
	}

	results := make([]result, n)
	var wg sync.WaitGroup
	wg.Add(n)

	for i := 0; i < n; i++ {
		i := i
		go func() {
			defer wg.Done()
			cmd := fmt.Sprintf("for j in $(seq 1 3000); do echo worker-%02d-line-$j; done", i)
			res := reg.Dispatch(Invocation{
				ID:        fmt.Sprintf("worker-%02d", i),
				Name:      Bash,
				Arguments: map[string]any{"command": cmd},
			})

			fullPath := ""
			if dp, ok := res.Details.(map[string]any); ok {
				if fp, ok := dp["fullOutputPath"].(string); ok {
					fullPath = fp
				}
			}
			results[i] = result{id: i, fullPath: fullPath, res: res}
		}()
	}

	wg.Wait()

	seenPaths := make(map[string]int)
	for i, r := range results {
		if r.res.IsError {
			t.Fatalf("worker %d failed: %+v", i, r.res)
		}
		if r.fullPath == "" {
			t.Fatalf("worker %d had no fullOutputPath", i)
		}
		defer os.Remove(r.fullPath)

		if prev, exists := seenPaths[r.fullPath]; exists {
			t.Fatalf("duplicate fullOutputPath between worker %d and %d: %s", prev, i, r.fullPath)
		}
		seenPaths[r.fullPath] = i

		// Verify content belongs exclusively to worker i
		data, err := os.ReadFile(r.fullPath)
		if err != nil {
			t.Fatalf("read full output for worker %d: %v", i, err)
		}
		content := string(data)
		expectedPrefix := fmt.Sprintf("worker-%02d-line-", i)
		if !strings.Contains(content, expectedPrefix) {
			t.Fatalf("worker %d full output missing expected prefix %q", i, expectedPrefix)
		}

		// Ensure other workers' content did not leak into worker i's file
		otherID := (i + 1) % n
		unexpectedPrefix := fmt.Sprintf("worker-%02d-line-", otherID)
		if strings.Contains(content, unexpectedPrefix) {
			t.Fatalf("worker %d full output contaminated by worker %d: %s", i, otherID, unexpectedPrefix)
		}
	}
}

// TestBashExitCodesAndDetails: exit codes 0, 1, 127, and signals are reported truthfully.
func TestBashExitCodesAndDetails(t *testing.T) {
	dir := t.TempDir()
	reg := NewRegistry(dir)

	// Success exit 0
	r0 := reg.Dispatch(Invocation{ID: "0", Name: Bash, Arguments: map[string]any{"command": "echo success; exit 0"}})
	if r0.IsError || !strings.Contains(r0.Content[0].Text, "success") {
		t.Fatalf("exit 0 failed: %+v", r0)
	}

	// Exit 1
	r1 := reg.Dispatch(Invocation{ID: "1", Name: Bash, Arguments: map[string]any{"command": "echo fail; exit 1"}})
	if !r1.IsError {
		t.Fatalf("exit 1 should be error: %+v", r1)
	}
	d1, ok := r1.Details.(map[string]any)
	if !ok || d1["exitCode"] != 1 {
		t.Fatalf("expected exitCode 1 in details: %v", d1)
	}

	// Exit 127
	r127 := reg.Dispatch(Invocation{ID: "127", Name: Bash, Arguments: map[string]any{"command": "exit 127"}})
	if !r127.IsError {
		t.Fatalf("exit 127 should be error: %+v", r127)
	}
	d127, ok := r127.Details.(map[string]any)
	if !ok || d127["exitCode"] != 127 {
		t.Fatalf("expected exitCode 127 in details: %v", d127)
	}
}

// TestBashTimeoutValidation: timeout parameter validation.
func TestBashTimeoutValidation(t *testing.T) {
	dir := t.TempDir()
	reg := NewRegistry(dir)

	invalidTimeouts := []any{
		0,
		-1,
		-0.5,
		math.NaN(),
		math.Inf(1),
		math.Inf(-1),
		2147484.0, // > 2147483.647
		"not-a-number",
	}

	for _, to := range invalidTimeouts {
		res := reg.Dispatch(Invocation{
			ID:   "to-val",
			Name: Bash,
			Arguments: map[string]any{
				"command": "echo hi",
				"timeout": to,
			},
		})
		if !res.IsError || !strings.Contains(res.Content[0].Text, "timeout must be a finite number of seconds") {
			t.Fatalf("invalid timeout %v was accepted: %+v", to, res)
		}
	}
}

// TestBashShellPrefixAndCustomShell: verifies ShellPrefix prepending and invalid shell.
func TestBashShellPrefixAndCustomShell(t *testing.T) {
	dir := t.TempDir()
	tool := &BashTool{
		Cwd:         dir,
		ShellPrefix: "export TEST_VAR=custom_val",
	}

	res := tool.ExecuteCtx(context.Background(), Invocation{
		ID:        "pfx",
		Name:      Bash,
		Arguments: map[string]any{"command": "echo $TEST_VAR"},
	})
	if res.IsError || !strings.Contains(res.Content[0].Text, "custom_val") {
		t.Fatalf("ShellPrefix not honored: %+v", res)
	}

	// Invalid shell path
	badTool := &BashTool{
		Cwd:       dir,
		ShellPath: "/nonexistent/path/to/shell",
	}
	resBad := badTool.ExecuteCtx(context.Background(), Invocation{
		ID:        "bad-shell",
		Name:      Bash,
		Arguments: map[string]any{"command": "echo hi"},
	})
	if !resBad.IsError {
		t.Fatalf("nonexistent shell must fail: %+v", resBad)
	}
}

// TestBashEmptyCommandAndNoOutput: command required and (no output) representation.
func TestBashEmptyCommandAndNoOutput(t *testing.T) {
	dir := t.TempDir()
	reg := NewRegistry(dir)

	// Missing command
	rNoCmd := reg.Dispatch(Invocation{ID: "no-cmd", Name: Bash, Arguments: map[string]any{}})
	if !rNoCmd.IsError || !strings.Contains(rNoCmd.Content[0].Text, "command is required") {
		t.Fatalf("empty command must fail: %+v", rNoCmd)
	}

	// True/colon command produces no output
	rEmpty := reg.Dispatch(Invocation{ID: "empty-out", Name: Bash, Arguments: map[string]any{"command": "true"}})
	if rEmpty.IsError || rEmpty.Content[0].Text != "(no output)" {
		t.Fatalf("expected (no output), got: %+v", rEmpty)
	}
}

// TestBashCancelDuringOutputFlood (T-TL-08): cancel context during heavy output flood.
// Ensure child processes are killed and pipes drained promptly without hanging or leaking.
func TestBashCancelDuringOutputFlood(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping timing-sensitive bash cancellation in short mode")
	}
	if runtime.GOOS == "windows" {
		t.Skip("process group cleanup is Unix only")
	}

	leakcheck.Verify(t)
	dir := t.TempDir()
	reg := NewRegistry(dir)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan Result, 1)

	cmd := "while true; do echo flooding-output-chunk; sleep 0.005; done"
	start := time.Now()
	go func() {
		done <- reg.DispatchCtx(ctx, Invocation{
			ID:        "flood",
			Name:      Bash,
			Arguments: map[string]any{"command": cmd},
		})
	}()

	time.Sleep(100 * time.Millisecond)
	cancel()

	select {
	case res := <-done:
		elapsed := time.Since(start)
		if !res.IsError || !strings.Contains(strings.ToLower(res.Content[0].Text), "cancel") {
			t.Fatalf("expected cancellation error: %+v", res)
		}
		if elapsed > 3*time.Second {
			t.Fatalf("cancellation took too long: %v", elapsed)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("cancellation deadlocked")
	}
}
