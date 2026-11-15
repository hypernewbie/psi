package app

import (
	"context"
	"os"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/hypernewbie/psi/internal/session"
	"github.com/hypernewbie/psi/internal/terminal"
	"github.com/hypernewbie/psi/internal/tools"
)

const (
	shellOutputBytes = 50 * 1024
	shellOutputLines = 2000
)

// shellCollector is the app's sole saved-output source for one shell run. It
// normalizes line endings before feeding one stateful terminal sanitizer, and
// retains only a bounded sanitized tail. It never rereads the capture file.
type shellCollector struct {
	mu        sync.Mutex
	sanitizer terminal.Sanitizer
	pendingCR bool
	segments  []string
	finalized bool
	output    string
	truncated bool
}

func newShellCollector() *shellCollector { return &shellCollector{} }

func (c *shellCollector) Feed(p []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.finalized {
		return
	}
	text := string(p)
	if c.pendingCR {
		text = "\r" + text
		c.pendingCR = false
	}
	if strings.HasSuffix(text, "\r") {
		c.pendingCR = true
		text = text[:len(text)-1]
	}
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	c.appendLocked(c.sanitizer.Feed(text))
}

func (c *shellCollector) appendLocked(safe string) {
	if safe == "" {
		return
	}
	parts := strings.Split(safe, "\n")
	if len(c.segments) == 0 {
		c.segments = parts
	} else {
		c.segments[len(c.segments)-1] += parts[0]
		c.segments = append(c.segments, parts[1:]...)
	}
	c.enforceLocked()
}

// Finalize flushes one held trailing CR as a newline and discards incomplete
// UTF-8 or escape state by simply never flushing those bytes. Repeated calls
// return the same settled text.
func (c *shellCollector) Finalize() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.finalized {
		return c.output
	}
	if c.pendingCR {
		c.pendingCR = false
		c.appendLocked(c.sanitizer.Feed("\n"))
	}
	c.enforceLocked()
	c.finalized = true
	c.output = strings.Join(c.segments, "\n")
	return c.output
}

// AppendDiagnostic adds independently sanitized infrastructure text to the
// bounded saved output. It must run after child-stream finalization so an
// unfinished OSC/DCS in the child stream cannot swallow the diagnostic.
func (c *shellCollector) AppendDiagnostic(text string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.enforceLocked()
	safe := terminal.Sanitize(text)
	if safe == "" {
		return
	}
	if len(c.segments) == 0 {
		c.segments = strings.Split(safe, "\n")
	} else if c.segments[len(c.segments)-1] == "" {
		c.appendLocked(safe)
	} else {
		c.appendLocked("\n" + safe)
	}
	c.enforceLocked()
	if c.finalized {
		c.output = strings.Join(c.segments, "\n")
	}
}

func (c *shellCollector) Text() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return strings.Join(c.segments, "\n")
}

func (c *shellCollector) Truncated() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.truncated
}

func (c *shellCollector) enforceLocked() {
	for len(c.segments) > shellOutputLines {
		c.segments = c.segments[1:]
		c.truncated = true
	}
	for c.sizeLocked() > shellOutputBytes {
		c.truncated = true
		if len(c.segments) > 1 {
			c.segments = c.segments[1:]
			continue
		}
		if len(c.segments) == 1 {
			cut := c.sizeLocked() - shellOutputBytes
			if cut >= len(c.segments[0]) {
				c.segments[0] = ""
				continue
			}
			c.segments[0] = trimLeadingContinuation(c.segments[0][cut:])
			continue
		}
		return
	}
}

func (c *shellCollector) sizeLocked() int {
	size := 0
	for i, segment := range c.segments {
		if i > 0 {
			size++
		}
		size += len(segment)
	}
	return size
}

func trimLeadingContinuation(text string) string {
	for len(text) > 0 && !utf8.RuneStart(text[0]) {
		text = text[1:]
	}
	return text
}

// shellRunFunc is the per-runtime runner seam. Tests replace it before the
// REPL starts; production calls the shared BashTool runner with no timeout.
type shellRunFunc func(context.Context, *tools.BashTool, string, func([]byte)) tools.BashRunResult

func defaultShellRun(ctx context.Context, tool *tools.BashTool, command string, onOutput func([]byte)) tools.BashRunResult {
	return tool.Run(ctx, command, 0, onOutput)
}

// shellLifetime owns one serial shell operation. The worker never reads or
// mutates runtime state; it sends exactly one result on the buffered channel.
type shellLifetime struct {
	cancel     context.CancelFunc
	cancelOnce sync.Once
	results    chan shellResult
	command    string
	exclude    bool
	callID     string
	generation int64
	store      *session.Store
}

type shellResult struct {
	output         string
	exitCode       *int
	cancelled      bool
	truncated      bool
	fullOutputPath string
	infraErr       error
}

func (l *shellLifetime) cancelRun() {
	if l == nil {
		return
	}
	l.cancelOnce.Do(l.cancel)
}

func startShellLifetime(parent context.Context, run shellRunFunc, tool *tools.BashTool, command string, exclude bool, callID string, generation int64, store *session.Store) *shellLifetime {
	ctx, cancel := context.WithCancel(parent)
	lifetime := &shellLifetime{
		cancel:     cancel,
		results:    make(chan shellResult, 1),
		command:    command,
		exclude:    exclude,
		callID:     callID,
		generation: generation,
		store:      store,
	}
	go func() {
		defer cancel()
		collector := newShellCollector()
		runResult := run(ctx, tool, command, collector.Feed)
		collector.Finalize()
		if runResult.InfraErr != nil {
			collector.AppendDiagnostic("shell: " + runResult.InfraErr.Error())
		}
		fullPath := ""
		if runResult.FullOutputPath != "" {
			if _, err := os.Stat(runResult.FullOutputPath); err == nil {
				fullPath = runResult.FullOutputPath
			}
		}
		lifetime.results <- shellResult{
			output:         collector.Text(),
			exitCode:       runResult.ExitCode,
			cancelled:      runResult.Cancelled,
			truncated:      runResult.Truncated || collector.Truncated(),
			fullOutputPath: fullPath,
			infraErr:       runResult.InfraErr,
		}
	}()
	return lifetime
}
