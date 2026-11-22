package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/hypernewbie/psi/internal/provider"
)

// execDefaults bound every exec call regardless of configuration.
const (
	execDefaultTimeout = 60 * time.Second
	execMinTimeout     = time.Second
	execMaxTimeout     = 300 * time.Second
	execDefaultOutput  = 16 << 10
	execMinOutput      = 1 << 10
	execMaxOutput      = 64 << 10
)

// execLimits are the resolved, clamped bounds for one configuration.
type execLimits struct {
	timeout   time.Duration
	maxOutput int
	passEnv   map[string]bool
}

func resolveExecLimits(cfg *execSettings) execLimits {
	limits := execLimits{timeout: execDefaultTimeout, maxOutput: execDefaultOutput, passEnv: map[string]bool{}}
	if cfg == nil {
		return limits
	}
	if cfg.TimeoutMs != nil {
		limits.timeout = time.Duration(*cfg.TimeoutMs) * time.Millisecond
	}
	if cfg.MaxOutputBytes != nil {
		limits.maxOutput = int(*cfg.MaxOutputBytes)
	}
	limits.timeout = min(max(limits.timeout, execMinTimeout), execMaxTimeout)
	limits.maxOutput = min(max(limits.maxOutput, execMinOutput), execMaxOutput)
	for _, name := range cfg.PassEnv {
		limits.passEnv[name] = true
	}
	return limits
}

// execToolDefinition describes the shell tool. The description states the
// security model honestly: the command runs as this process's own user, with
// no sandbox, and its output is bounded.
func execToolDefinition() provider.ToolDef {
	return provider.ToolDef{
		Name:        "exec",
		Description: "Run a shell command in the workspace directory. The command runs with this process's own user permissions; there is no sandbox. Output and run time are bounded.",
		Schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"command": map[string]any{"type": "string", "description": "The shell command line to run"},
			},
			"required": []string{"command"},
		},
	}
}

// boundedOutput keeps the first three quarters and the last quarter of the
// combined output under one cap, so a large log does not crowd out its ending.
type boundedOutput struct {
	cap   int
	head  []byte
	tail  []byte
	total int
}

func (b *boundedOutput) Write(p []byte) (int, error) {
	n := len(p)
	b.total += n
	headRoom := b.cap*3/4 - len(b.head)
	if headRoom > 0 {
		take := min(headRoom, n)
		b.head = append(b.head, p[:take]...)
		p = p[take:]
	}
	if len(p) > 0 {
		b.tail = append(b.tail, p...)
		if keep := b.cap / 4; len(b.tail) > keep {
			b.tail = b.tail[len(b.tail)-keep:]
		}
	}
	return n, nil
}

func (b *boundedOutput) text() string {
	if b.total <= b.cap {
		return string(b.head)
	}
	return string(b.head) + "\n[... output truncated ...]\n" + string(b.tail)
}

// execResult is one finished command. Err is non-nil only when the command
// could not start or hit its deadline; the exit status is reported as text
// either way, never as an exception body.
type execResult struct {
	output  string
	status  string
	err     error
	command string
}

func (r execResult) replyText() string {
	body := strings.TrimSpace(r.output)
	if body == "" {
		body = "(no output)"
	}
	return fmt.Sprintf("%s\n%s", r.status, body)
}

// runExec runs one bounded shell command in the workspace. The process group
// is killed when the context ends, so a canceled or stopped turn ends it.
func runExec(ctx context.Context, workspace, command string, limits execLimits) execResult {
	command = strings.TrimSpace(command)
	if command == "" {
		return execResult{err: fmt.Errorf("command is required"), command: command}
	}
	runCtx, cancel := context.WithTimeout(ctx, limits.timeout)
	defer cancel()

	cmd := startExecCommand(runCtx, workspace, command, limits)
	output := &boundedOutput{cap: limits.maxOutput}
	cmd.Stdout = output
	cmd.Stderr = output
	if err := cmd.Start(); err != nil {
		return execResult{status: "command could not start", err: err, command: command}
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	var waitErr error
	timedOut := false
	select {
	case waitErr = <-done:
	case <-runCtx.Done():
		timedOut = true
		stopExecCommand(cmd)
		<-done
	}
	if timedOut {
		return execResult{output: output.text(), status: fmt.Sprintf("timed out after %s", limits.timeout), err: runCtx.Err(), command: command}
	}
	status := "exit 0"
	if waitErr != nil {
		status = "nonzero exit: " + waitErr.Error()
	}
	return execResult{output: output.text(), status: status, command: command}
}

// execConfigFor returns the exec settings only for routes that may run them:
// the owner direct-message route. Every other route gets nil and no tool.
func execConfigFor(settings *fileSettings, ownerDM bool) *execSettings {
	if !ownerDM || settings == nil || settings.Tools == nil {
		return nil
	}
	return settings.Tools.Exec
}
