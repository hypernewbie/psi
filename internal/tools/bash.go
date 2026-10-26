package tools

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// BashTool executes shell commands in the workspace with bounded output,
// timeout, and process-group cancellation.
type BashTool struct {
	Cwd       string
	ShellPath string
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

// Execute runs command with optional timeout (seconds). No hidden default.
func (t *BashTool) Execute(inv Invocation) Result {
	return t.ExecuteCtx(context.Background(), inv)
}

func (t *BashTool) ExecuteCtx(ctx context.Context, inv Invocation) Result {
	cmdStr, _ := argString(inv.Arguments, "command")
	if cmdStr == "" {
		return errResult("bash: command is required")
	}
	var timeout time.Duration
	if f, ok := argFloat(inv.Arguments, "timeout"); ok {
		if f <= 0 || !isFinite(f) {
			return errResult("bash: invalid timeout: must be a finite number of seconds")
		}
		ms := f * 1000
		if ms > 2147483647 {
			return errResult("bash: invalid timeout: maximum is 2147483.647 seconds")
		}
		timeout = time.Duration(ms * float64(time.Millisecond))
	}
	if _, err := os.Stat(t.Cwd); err != nil {
		return errResult(fmt.Sprintf("bash: working directory does not exist: %s", t.Cwd))
	}
	ctx2 := ctx
	var cancel context.CancelFunc
	if timeout > 0 {
		ctx2, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	shell := t.shell()
	cmd := exec.CommandContext(ctx2, shell, "-c", cmdStr)
	cmd.Dir = t.Cwd
	// New process group so cancellation kills descendants, not just the shell.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	// Full output goes to a private temp file when truncated.
	err := cmd.Run()
	output := buf.String()
	tr := truncateHead(output, DefaultMaxLines, DefaultMaxBytes)
	modelOut := tr.Content
	var fullPath string
	if tr.Truncated {
		f, terr := os.CreateTemp("", "psi-bash-*")
		if terr == nil {
			f.WriteString(output)
			f.Close()
			os.Chmod(f.Name(), 0o600)
			fullPath = f.Name()
			_ = filepath.Base(fullPath)
			modelOut += fmt.Sprintf("\n\n[Output truncated. Full output: %s]", fullPath)
		} else {
			modelOut += "\n\n[Output truncated]"
		}
	}
	if ctx2.Err() == context.DeadlineExceeded {
		return Result{Content: []Block{{Type: "text", Text: fmt.Sprintf("Command timed out after %v\n%s", timeout, modelOut)}}, IsError: true, Details: map[string]any{"fullOutputPath": fullPath}}
	}
	if ctx.Err() == context.Canceled || (err != nil && ctx2.Err() != nil) {
		killGroup(cmd)
		return Result{Content: []Block{{Type: "text", Text: fmt.Sprintf("Command cancelled\n%s", modelOut)}}, IsError: true}
	}
	if err != nil {
		code := exitCode(err)
		text := fmt.Sprintf("Ran `%s`\n```\n%s\n```\n\nCommand exited with code %d", cmdStr, modelOut, code)
		if tr.Truncated {
			text += fmt.Sprintf("\n\n[Output truncated. Full output: %s]", fullPath)
		}
		// Non-zero exit is an error result for the model but still a value.
		return Result{Content: []Block{{Type: "text", Text: text}}, IsError: true, Details: map[string]any{"exitCode": code, "fullOutputPath": fullPath}}
	}
	if strings.TrimSpace(modelOut) == "" {
		return textResult("(no output)")
	}
	return Result{Content: []Block{{Type: "text", Text: modelOut}}, Details: map[string]any{"fullOutputPath": fullPath}}
}

func isFinite(f float64) bool {
	return f < 1e308 && f > -1e308 && f == f
}

func exitCode(err error) int {
	if ee, ok := err.(*exec.ExitError); ok {
		if ws, ok := ee.Sys().(syscall.WaitStatus); ok {
			if ws.Signaled() {
				return 128 + int(ws.Signal())
			}
			return ws.ExitStatus()
		}
		return 1
	}
	return 1
}

func killGroup(cmd *exec.Cmd) {
	if cmd.Process == nil {
		return
	}
	pgid, err := syscall.Getpgid(cmd.Process.Pid)
	if err == nil {
		_ = syscall.Kill(-pgid, syscall.SIGKILL)
		return
	}
	_ = cmd.Process.Kill()
}
