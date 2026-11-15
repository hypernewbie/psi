//go:build unix

package tools

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBashExecutionRawOutcome(t *testing.T) {
	if _, err := os.Stat("/bin/sh"); err != nil {
		t.Skip("test needs a POSIX sh")
	}
	dir := t.TempDir()
	tool := &BashTool{Cwd: dir, ShellPath: "sh"}

	t.Run("explicit zero", func(t *testing.T) {
		run := tool.Run(context.Background(), "printf 'ok'", 0, nil)
		if run.InfraErr != nil || run.Cancelled || run.TimedOut || run.ExitCode == nil || *run.ExitCode != 0 {
			t.Fatalf("zero = %+v", run)
		}
		if run.Output != "ok" {
			t.Fatalf("output = %q", run.Output)
		}
	})

	t.Run("exit seven", func(t *testing.T) {
		run := tool.Run(context.Background(), "exit 7", 0, nil)
		if run.InfraErr != nil || run.ExitCode == nil || *run.ExitCode != 7 {
			t.Fatalf("exit seven = %+v", run)
		}
		result := tool.ExecuteCtx(context.Background(), Invocation{Name: Bash, Arguments: map[string]any{"command": "exit 7"}})
		if !result.IsError || !strings.Contains(result.Content[0].Text, "Command failed (exit 7): exit status 7") {
			t.Fatalf("wrapper formatting = %+v", result)
		}
	})

	t.Run("empty and literal error output", func(t *testing.T) {
		run := tool.Run(context.Background(), "true", 0, nil)
		if run.InfraErr != nil || run.Output != "" || run.ExitCode == nil || *run.ExitCode != 0 {
			t.Fatalf("empty = %+v", run)
		}
		run = tool.Run(context.Background(), "printf 'Command failed (exit 1)\\n'", 0, nil)
		if run.InfraErr != nil || run.ExitCode == nil || *run.ExitCode != 0 || !strings.Contains(run.Output, "Command failed") {
			t.Fatalf("error-like text changed outcome: %+v", run)
		}
	})

	t.Run("cancellation and infrastructure", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		run := tool.Run(ctx, "echo must-not-run", 0, nil)
		if !run.Cancelled || run.InfraErr != nil || run.ExitCode != nil {
			t.Fatalf("pre-cancel = %+v", run)
		}
		bad := &BashTool{Cwd: dir, ShellPath: filepath.Join(dir, "missing-shell")}
		run = bad.Run(context.Background(), "echo ran > marker", 0, nil)
		if run.InfraErr == nil || run.ExitCode != nil || run.Cancelled {
			t.Fatalf("resolution failure = %+v", run)
		}
		if _, err := os.Stat(filepath.Join(dir, "marker")); !os.IsNotExist(err) {
			t.Fatal("resolution failure started a child")
		}
	})
}

func TestBashCaptureAcceptedPrefix(t *testing.T) {
	file, err := os.CreateTemp(t.TempDir(), "capture-*")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()

	t.Run("short write callback", func(t *testing.T) {
		var got []byte
		capture := &tailCapture{file: file, cancel: func() {}, onWrite: func(p []byte) { got = append(got, p...) }}
		capture.write = func(p []byte) (int, error) { return 3, io.ErrShortWrite }
		n, err := capture.Write([]byte("abcdef"))
		if n != 3 || !errors.Is(err, io.ErrShortWrite) {
			t.Fatalf("write = %d, %v", n, err)
		}
		if string(got) != "abc" {
			t.Fatalf("callback = %q, want accepted prefix", got)
		}
		if capture.tailString() != "abc" {
			t.Fatalf("tail = %q", capture.tailString())
		}
	})

	t.Run("zero accepted has no callback", func(t *testing.T) {
		called := false
		capture := &tailCapture{file: file, cancel: func() {}, onWrite: func([]byte) { called = true }}
		capture.write = func([]byte) (int, error) { return 0, io.ErrShortWrite }
		if _, err := capture.Write([]byte("abcdef")); err == nil {
			t.Fatal("write error lost")
		}
		if called {
			t.Fatal("callback received no accepted bytes")
		}
	})

	t.Run("cap rejection has no callback", func(t *testing.T) {
		called := false
		capture := &tailCapture{file: file, cancel: func() {}, onWrite: func([]byte) { called = true }}
		capture.total = 256 * 1024 * 1024
		if n, err := capture.Write([]byte("x")); n != 0 || err == nil {
			t.Fatalf("cap write = %d, %v", n, err)
		}
		if called {
			t.Fatal("rejected cap chunk reached callback")
		}
	})

	t.Run("earlier output survives a later write error", func(t *testing.T) {
		var got []byte
		capture := &tailCapture{file: file, cancel: func() {}, onWrite: func(p []byte) { got = append(got, p...) }}
		capture.write = func(p []byte) (int, error) { return len(p), nil }
		if _, err := capture.Write([]byte("kept")); err != nil {
			t.Fatal(err)
		}
		capture.write = func([]byte) (int, error) { return 0, io.ErrClosedPipe }
		if _, err := capture.Write([]byte("lost")); err == nil {
			t.Fatal("later write error lost")
		}
		if string(got) != "kept" || capture.tailString() != "kept" {
			t.Fatalf("accepted output lost: callback=%q tail=%q", got, capture.tailString())
		}
	})
}
