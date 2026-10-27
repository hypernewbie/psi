package tools

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode/utf8"
)

// BashTool executes shell commands with bounded memory and process-group cancellation.
type BashTool struct {
	Cwd         string
	ShellPath   string
	ShellPrefix string
}

func (t *BashTool) shell() string {
	if t.ShellPath != "" {
		return t.ShellPath
	}
	if v := os.Getenv("PSI_SHELL"); v != "" {
		return v
	}
	return "bash"
}

func (t *BashTool) Execute(inv Invocation) Result {
	return t.ExecuteCtx(context.Background(), inv)
}

func (t *BashTool) ExecuteCtx(ctx context.Context, inv Invocation) Result {
	cmdStr, _ := argString(inv.Arguments, "command")
	if cmdStr == "" {
		return errResult("bash: command is required")
	}
	var timeout time.Duration
	if _, present := inv.Arguments["timeout"]; present {
		f, ok := argFloat(inv.Arguments, "timeout")
		if !ok || f <= 0 || !isFinite(f) || f > 2147483.647 {
			return errResult("bash: timeout must be a finite number of seconds in (0, 2147483.647]")
		}
		timeout = time.Duration(f * float64(time.Second))
	}
	runCtx, stop := context.WithCancel(ctx)
	defer stop()
	if timeout > 0 {
		var cancel context.CancelFunc
		runCtx, cancel = context.WithTimeout(runCtx, timeout)
		defer cancel()
	}
	if err := runCtx.Err(); err != nil {
		return errResult("Command cancelled")
	}
	f, err := os.CreateTemp("", "psi-bash-*") // 0600 from creation, not after the command
	if err != nil {
		return errResult(fmt.Sprintf("bash: cannot capture output: %v", err))
	}
	keepFile := false
	defer func() {
		f.Close()
		if !keepFile {
			os.Remove(f.Name())
		}
	}()
	capture := &tailCapture{file: f, cancel: stop}
	if t.ShellPrefix != "" {
		cmdStr = t.ShellPrefix + "\n" + cmdStr
	}
	cmd := exec.CommandContext(runCtx, t.shell(), "-c", cmdStr)
	cmd.Dir = t.Cwd
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return os.ErrProcessDone
		}
		// The group ID is the original PID even if the group leader has exited.
		if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); err != nil && err != syscall.ESRCH {
			return err
		}
		return nil
	}
	// Descendants must not keep Wait blocked indefinitely through inherited pipes.
	cmd.WaitDelay = 250 * time.Millisecond
	cmd.Stdout, cmd.Stderr = capture, capture
	runErr := cmd.Run()
	if runErr != nil && cmd.Process != nil {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	closeErr := f.Close()
	if capture.err != nil {
		return errResult(fmt.Sprintf("bash: output capture failed: %v", capture.err))
	}
	if closeErr != nil {
		return errResult(fmt.Sprintf("bash: output capture failed: %v", closeErr))
	}
	output := string(capture.tail)
	for len(output) > 0 && !utf8.RuneStart(output[0]) {
		output = output[1:]
	}
	lines := strings.Split(output, "\n")
	if len(lines) > DefaultMaxLines {
		output = strings.Join(lines[len(lines)-DefaultMaxLines:], "\n")
	}
	truncated := int64(len(output)) < capture.total
	details := map[string]any{}
	if truncated {
		keepFile = true
		details["fullOutputPath"] = f.Name()
		output += fmt.Sprintf("\n\n[Output truncated. Full output: %s]", f.Name())
	}
	if ctx.Err() != nil {
		return Result{Content: []Block{{Type: "text", Text: "Command cancelled\n" + output}}, IsError: true, Details: details}
	}
	if runCtx.Err() == context.DeadlineExceeded {
		return Result{Content: []Block{{Type: "text", Text: fmt.Sprintf("Command timed out after %v\n%s", timeout, output)}}, IsError: true, Details: details}
	}
	if runErr != nil {
		code := exitCode(runErr)
		details["exitCode"] = code
		return Result{Content: []Block{{Type: "text", Text: fmt.Sprintf("%s\nCommand failed (exit %d): %v", output, code, runErr)}}, IsError: true, Details: details}
	}
	if output == "" {
		output = "(no output)"
	}
	return Result{Content: []Block{{Type: "text", Text: output}}, Details: details}
}

// Keep only the last 50 KiB in memory. Spool at most 256 MiB to disk, then
// cancel the process group instead of exhausting storage.
type tailCapture struct {
	mu     sync.Mutex
	file   *os.File
	tail   []byte
	total  int64
	err    error
	cancel context.CancelFunc
}

func (c *tailCapture) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.total+int64(len(p)) > 256*1024*1024 {
		c.err = fmt.Errorf("command output exceeds 256 MiB")
		c.cancel()
		return 0, c.err
	}
	n, err := c.file.Write(p)
	c.total += int64(n)
	if n >= DefaultMaxBytes {
		c.tail = append(c.tail[:0], p[n-DefaultMaxBytes:n]...)
	} else {
		if excess := len(c.tail) + n - DefaultMaxBytes; excess > 0 {
			copy(c.tail, c.tail[excess:])
			c.tail = c.tail[:len(c.tail)-excess]
		}
		c.tail = append(c.tail, p[:n]...)
	}
	if err != nil {
		c.err = err
		c.cancel()
	}
	return n, err
}

func isFinite(f float64) bool { return f < 1e308 && f > -1e308 && f == f }

func exitCode(err error) int {
	if ee, ok := err.(*exec.ExitError); ok {
		return ee.ExitCode()
	}
	return 1
}
