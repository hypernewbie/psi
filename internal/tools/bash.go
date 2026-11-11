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

func (t *BashTool) shell() string {
	if t.ShellPath != "" {
		return t.ShellPath
	}
	if v := os.Getenv("PSI_SHELL"); v != "" {
		return v
	}
	return "bash"
}

// idleDrainGrace is the Pi-compatible post-exit grace: every stdout/stderr
// chunk resets it (child-process.ts waitForChildProcess). A quiet inherited
// pipe releases at the deadline with the leader exit status preserved.
const idleDrainGrace = 100 * time.Millisecond

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
	capture := &tailCapture{file: f, cancel: stop, now: t.now}
	capture.touch()
	if t.ShellPrefix != "" {
		cmdStr = t.ShellPrefix + "\n" + cmdStr
	}
	// Own the stdio pipes so leader exit, pipe completion, and idle drain
	// stay distinct (F07). Go's WaitDelay uses one fixed deadline from exit;
	// Pi re-arms a 100ms idle timer on every chunk instead.
	pr, pw, err := os.Pipe()
	if err != nil {
		return errResult(fmt.Sprintf("bash: cannot capture output: %v", err))
	}
	cmd := exec.Command(t.shell(), "-c", cmdStr)
	cmd.Dir = t.Cwd
	setNewProcessGroup(cmd)
	cmd.Stdout, cmd.Stderr = pw, pw
	startedAt := time.Now()
	if err := cmd.Start(); err != nil {
		pr.Close()
		pw.Close()
		return errResult(fmt.Sprintf("bash: %v", err))
	}
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
	killGroup := func() { killProcessGroup(cmd) }
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
	if timedOut {
		deadline, _ := runCtx.Deadline()
		status := bashTimeoutStatus(timeout, startedAt, deadline, exited.at, shellExitCode(cmd.ProcessState))
		if output != "" {
			status = output + "\n\n" + status
		}
		return Result{Content: []Block{{Type: "text", Text: status}}, IsError: true, Details: details}
	}
	if exited.err != nil {
		code := exitCode(exited.err)
		details["exitCode"] = code
		return Result{Content: []Block{{Type: "text", Text: fmt.Sprintf("%s\nCommand failed (exit %d): %v", output, code, exited.err)}}, IsError: true, Details: details}
	}
	if output == "" {
		output = "(no output)"
	}
	return Result{Content: []Block{{Type: "text", Text: output}}, Details: details}
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
	mu     sync.Mutex
	file   *os.File
	tail   []byte
	total  int64
	err    error
	cancel context.CancelFunc
	last   atomic.Int64 // nanos of the most recent chunk (idle drain clock)
	now    func() time.Time
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
