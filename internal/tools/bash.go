package tools

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/hypernewbie/psi/internal/shellutil"
)

// BashTool executes shell commands with bounded memory and process-group cancellation.
type BashTool struct {
	Cwd         string
	ShellPath   string
	ShellPrefix string

	// now is the idle-drain clock. Nil means the wall clock; tests inject a
	// fake clock so the post-exit grace never races host scheduling.
	now func() time.Time
}

// BashRunResult is the raw outcome of one shell execution. It is shared by the
// model tool and the interactive editor; neither path infers outcomes from
// formatted text.
type BashRunResult struct {
	Output         string
	ExitCode       *int
	Cancelled      bool
	TimedOut       bool
	Truncated      bool
	FullOutputPath string
	InfraErr       error
	ExitText       string
	ShellExitCode  int
	TotalBytes     int64
	Timeout        time.Duration
	StartedAt      time.Time
	ExitedAt       time.Time
	Deadline       time.Time
}

func (t *BashTool) customShell() string {
	if t.ShellPath != "" {
		return t.ShellPath
	}
	return strings.TrimSpace(os.Getenv("PSI_SHELL"))
}

func (t *BashTool) resolveShell() (shellutil.Config, error) {
	return shellutil.GetShellConfigForDir(t.customShell(), t.Cwd)
}

// idleDrainGrace is the Pi-compatible post-exit grace: every stdout/stderr
// chunk resets it (child-process.ts waitForChildProcess). A quiet inherited
// pipe releases at the deadline with the leader exit status preserved.
const idleDrainGrace = 100 * time.Millisecond

// Run executes one verbatim command with the shared shell selection and
// returns raw accepted output. The output callback runs synchronously after
// each accepted capture write and never receives an unwritten suffix.
func (t *BashTool) Run(ctx context.Context, command string, timeout time.Duration, onOutput func([]byte)) BashRunResult {
	if t.ShellPrefix != "" {
		command = t.ShellPrefix + "\n" + command
	}
	return runShell(ctx, t.Cwd, "bash", "psi-bash-*", t.resolveShell, nil, command, timeout, onOutput, t.now)
}

func (t *BashTool) ExecuteCtx(ctx context.Context, inv Invocation) Result {
	cmdStr, timeout, errRes := parseShellInvocation("bash", inv)
	if errRes != nil {
		return *errRes
	}
	return formatShellResult("bash", t.Run(ctx, cmdStr, timeout, nil))
}

func parseShellInvocation(toolName string, inv Invocation) (string, time.Duration, *Result) {
	cmdStr, _ := argString(inv.Arguments, "command")
	if cmdStr == "" {
		res := errResult(toolName + ": command is required")
		return "", 0, &res
	}
	var timeout time.Duration
	if _, present := inv.Arguments["timeout"]; present {
		f, ok := argFloat(inv.Arguments, "timeout")
		if !ok || f <= 0 || !isFinite(f) || f > 2147483.647 {
			res := errResult(toolName + ": timeout must be a finite number of seconds in (0, 2147483.647]")
			return "", 0, &res
		}
		timeout = time.Duration(f * float64(time.Second))
	}
	return cmdStr, timeout, nil
}

// executeShellCommand runs one model-tool command and formats it. PowerShell
// uses this path; bash uses Run directly so the editor can collect raw output.
func executeShellCommand(
	ctx context.Context,
	cwd string,
	shellName string,
	tempFilePrefix string,
	resolveShell func() (shellutil.Config, error),
	wrapCommand func(shellutil.Config, string) string,
	cmdStr string,
	timeout time.Duration,
	now func() time.Time,
) Result {
	run := runShell(ctx, cwd, shellName, tempFilePrefix, resolveShell, wrapCommand, cmdStr, timeout, nil, now)
	return formatShellResult(shellName, run)
}

// runShell owns one serial shell lifetime. It resolves the shell after the
// early cancellation check, keeps accepted partial output with infrastructure
// errors, and returns a typed outcome instead of parsing its own formatting.
func runShell(
	ctx context.Context,
	cwd string,
	shellName string,
	tempFilePrefix string,
	resolveShell func() (shellutil.Config, error),
	wrapCommand func(shellutil.Config, string) string,
	cmdStr string,
	timeout time.Duration,
	onOutput func([]byte),
	now func() time.Time,
) BashRunResult {
	result := BashRunResult{Timeout: timeout}
	lowerName := strings.ToLower(shellName)
	runCtx, stop := context.WithCancel(ctx)
	defer stop()
	if timeout > 0 {
		var cancel context.CancelFunc
		runCtx, cancel = context.WithTimeout(runCtx, timeout)
		defer cancel()
	}
	result.Deadline, _ = runCtx.Deadline()
	if err := runCtx.Err(); err != nil {
		result.Cancelled = true
		return result
	}
	shellCfg, err := resolveShell()
	if err != nil {
		result.InfraErr = fmt.Errorf("%s: %w", lowerName, err)
		return result
	}
	if cwd != "" {
		if _, err := os.Stat(cwd); err != nil {
			result.InfraErr = fmt.Errorf("Working directory does not exist: %s\nCannot execute %s commands.", cwd, shellName)
			return result
		}
	}
	f, err := os.CreateTemp("", tempFilePrefix) // 0600 from creation, not after the command
	if err != nil {
		result.InfraErr = fmt.Errorf("%s: cannot capture output: %w", lowerName, err)
		return result
	}
	keepFile := false
	defer func() {
		f.Close()
		if !keepFile {
			os.Remove(f.Name())
		}
	}()
	capture := &tailCapture{file: f, cancel: stop, onWrite: onOutput, now: now}
	capture.touch()
	resolvedCmd := cmdStr
	if wrapCommand != nil {
		resolvedCmd = wrapCommand(shellCfg, cmdStr)
	}
	// Own the stdio pipes so leader exit, pipe completion, and idle drain
	// stay distinct (F07). Go's WaitDelay uses one fixed deadline from exit;
	// Pi re-arms a 100ms idle timer on every chunk instead.
	pr, pw, err := os.Pipe()
	if err != nil {
		result.InfraErr = fmt.Errorf("%s: cannot capture output: %w", lowerName, err)
		return result
	}
	var cmd *exec.Cmd
	if shellCfg.CommandTransport == shellutil.TransportStdin {
		cmd = exec.Command(shellCfg.Shell, shellCfg.Args...)
		cmd.Stdin = strings.NewReader(resolvedCmd)
	} else {
		args := make([]string, len(shellCfg.Args)+1)
		copy(args, shellCfg.Args)
		args[len(shellCfg.Args)] = resolvedCmd
		cmd = exec.Command(shellCfg.Shell, args...)
	}
	cmd.Dir = cwd
	setNewProcessGroup(cmd)
	shellutil.HideWindow(cmd)
	cmd.Stdout, cmd.Stderr = pw, pw
	result.StartedAt = time.Now()
	if err := cmd.Start(); err != nil {
		pr.Close()
		pw.Close()
		result.InfraErr = fmt.Errorf("%s: %w", lowerName, err)
		return result
	}
	pg := attachProcessGroup(cmd)
	defer closeProcessGroup(pg)
	// Parent copies only; the child ends stay open across descendants.
	_ = pw.Close()
	copyDone := make(chan struct{})
	go func() {
		defer close(copyDone)
		defer pr.Close()
		buf := make([]byte, 32*1024)
		for {
			n, rerr := pr.Read(buf)
			if n > 0 {
				if _, werr := capture.Write(buf[:n]); werr != nil {
					return
				}
			}
			if rerr != nil {
				return
			}
		}
	}()
	// Leader exit never implies pipe completion: descendants may still hold
	// the inherited write ends. Wait for the leader, then drain.
	type shellExit struct {
		err error
		at  time.Time
	}
	waitDone := make(chan shellExit, 1)
	go func() {
		err := cmd.Wait()
		waitDone <- shellExit{err: err, at: time.Now()}
	}()
	var exited shellExit
	var timedOut bool
	killGroup := func() { killProcessGroup(cmd, pg) }
	drain := func() {
		tick := time.NewTicker(10 * time.Millisecond)
		defer tick.Stop()
		for {
			select {
			case <-copyDone:
				return
			case <-runCtx.Done():
				killGroup()
				return
			default:
			}
			if capture.nowTime().Sub(capture.lastWrite()) > idleDrainGrace {
				// Quiet pipe: release with the leader status preserved.
				killGroup()
				return
			}
			select {
			case <-copyDone:
				return
			case <-runCtx.Done():
				killGroup()
				return
			case <-tick.C:
			}
		}
	}
	select {
	case exited = <-waitDone:
		// Idle grace starts at exit, not at the last pre-exit chunk.
		capture.touch()
		drain()
	case <-runCtx.Done():
		killGroup()
		exited = <-waitDone
		drain()
	}
	timedOut = runCtx.Err() == context.DeadlineExceeded
	if runCtx.Err() != nil {
		killGroup()
	}
	// A descendant can escape the process group but retain the pipe. Close
	// our reader before joining its goroutine so cancellation/idle never
	// waits for an unrelated inherited writer to exit.
	_ = pr.Close()
	<-copyDone
	closeErr := f.Close()
	result.ExitedAt = exited.at
	result.Output = capture.tailString()
	result.TotalBytes = capture.total
	result.Truncated = int64(len(result.Output)) < capture.total
	if len(strings.Split(result.Output, "\n")) > DefaultMaxLines {
		result.Truncated = true
	}
	if exited.err != nil {
		result.ExitText = exited.err.Error()
		code := exitCode(exited.err)
		result.ExitCode = &code
	} else if cmd.ProcessState != nil {
		code := shellExitCode(cmd.ProcessState)
		result.ExitCode = &code
	}
	if cmd.ProcessState != nil {
		result.ShellExitCode = shellExitCode(cmd.ProcessState)
	} else {
		result.ShellExitCode = 1
	}
	if timedOut {
		result.TimedOut = true
	}
	if capturedErr := capture.err; capturedErr != nil {
		result.InfraErr = fmt.Errorf("%s: output capture failed: %w", lowerName, capturedErr)
	} else if closeErr != nil {
		result.InfraErr = fmt.Errorf("%s: output capture failed: %w", lowerName, closeErr)
	}
	if ctx.Err() != nil {
		result.Cancelled = true
		result.ExitCode = nil
	}
	if result.InfraErr != nil {
		result.ExitCode = nil
	}
	if result.Truncated && result.InfraErr == nil {
		keepFile = true
		result.FullOutputPath = f.Name()
	}
	return result
}

// formatShellResult renders one typed run with the model-tool conventions.
func formatShellResult(shellName string, run BashRunResult) Result {
	output := run.Output
	details := map[string]any{}
	if run.FullOutputPath != "" {
		output += fmt.Sprintf("\n\n[Output truncated. Full output: %s]", run.FullOutputPath)
		details["fullOutputPath"] = run.FullOutputPath
	}
	if lines := strings.Split(output, "\n"); len(lines) > DefaultMaxLines {
		output = strings.Join(lines[len(lines)-DefaultMaxLines:], "\n")
	}
	if run.InfraErr != nil {
		return Result{Content: []Block{{Type: "text", Text: run.InfraErr.Error()}}, IsError: true, Details: details}
	}
	if run.Cancelled {
		return Result{Content: []Block{{Type: "text", Text: "Command cancelled\n" + output}}, IsError: true, Details: details}
	}
	if run.TimedOut {
		status := bashTimeoutStatus(run.Timeout, run.StartedAt, run.Deadline, run.ExitedAt, run.ShellExitCode)
		if output != "" {
			status = output + "\n\n" + status
		}
		return Result{Content: []Block{{Type: "text", Text: status}}, IsError: true, Details: details}
	}
	if run.ExitCode != nil && *run.ExitCode != 0 {
		details["exitCode"] = *run.ExitCode
		exitText := run.ExitText
		if exitText == "" {
			exitText = fmt.Sprintf("exit status %d", *run.ExitCode)
		}
		return Result{Content: []Block{{Type: "text", Text: fmt.Sprintf("%s\nCommand failed (exit %d): %s", output, *run.ExitCode, exitText)}}, IsError: true, Details: details}
	}
	if output == "" {
		output = "(no output)"
	}
	return Result{Content: []Block{{Type: "text", Text: output}}, Details: details}
}

func (c *tailCapture) tailString() string {
	output := string(c.tail)
	for len(output) > 0 && !utf8.RuneStart(output[0]) {
		output = output[1:]
	}
	return output
}

// bashTimeoutStatus follows Pi's ShellTimeoutError reporting when the shell
// exited before its deadline but descendants kept the output pipe open.
func bashTimeoutStatus(timeout time.Duration, started, deadline, exited time.Time, code int) string {
	status := fmt.Sprintf("Command timed out after %g seconds", timeout.Seconds())
	if !exited.IsZero() && exited.Before(deadline) {
		seconds := float64((exited.Sub(started)+50*time.Millisecond)/(100*time.Millisecond)) / 10
		status += fmt.Sprintf(". The shell exited with code %d after %g seconds, but background processes it started kept writing output until the timeout.", code, seconds)
	}
	return status
}

// Keep only the last 50 KiB in memory. Spool at most 256 MiB to disk, then
// cancel the process group instead of exhausting storage.
type tailCapture struct {
	mu      sync.Mutex
	file    *os.File
	write   func([]byte) (int, error) // test seam; nil delegates to file
	tail    []byte
	total   int64
	err     error
	cancel  context.CancelFunc
	onWrite func([]byte)
	last    atomic.Int64 // nanos of the most recent chunk (idle drain clock)
	now     func() time.Time
}

// nowTime reports the drain clock instant. A nil clock means the wall clock.
func (c *tailCapture) nowTime() time.Time {
	if c.now != nil {
		return c.now()
	}
	return time.Now()
}

// lastWrite reports the most recent output instant.
func (c *tailCapture) touch() { c.last.Store(c.nowTime().UnixNano()) }

func (c *tailCapture) lastWrite() time.Time { return time.Unix(0, c.last.Load()) }

func (c *tailCapture) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.touch()
	if c.total+int64(len(p)) > 256*1024*1024 {
		c.err = fmt.Errorf("command output exceeds 256 MiB")
		c.cancel()
		return 0, c.err
	}
	write := c.file.Write
	if c.write != nil {
		write = c.write
	}
	n, err := write(p)
	if n > 0 {
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
		if c.onWrite != nil {
			c.onWrite(p[:n])
		}
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
