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

// shellCollector streams normalized, sanitized output into a bounded tail.
// The runner calls Feed serially and joins its output reader before the
// lifetime worker calls finish. The collector never rereads the capture file.
type shellCollector struct {
	sanitizer terminal.Sanitizer
	pendingCR bool
	tail      string
	newlines  int
	truncated bool
}

func (c *shellCollector) Feed(p []byte) {
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
	c.appendSafe(c.sanitizer.Feed(text))
}

func (c *shellCollector) appendSafe(text string) {
	c.tail += text
	c.newlines += strings.Count(text, "\n")
	cut := max(0, len(c.tail)-shellOutputBytes)
	// The sanitizer emits valid UTF-8. Advance past a partial leading rune.
	for cut < len(c.tail) && !utf8.RuneStart(c.tail[cut]) {
		cut++
	}
	c.newlines -= strings.Count(c.tail[:cut], "\n")
	// Count the empty line after a trailing newline, as the bash tool does.
	for c.newlines >= shellOutputLines {
		cut += strings.IndexByte(c.tail[cut:], '\n') + 1
		c.newlines--
	}
	if cut != 0 {
		c.tail = c.tail[cut:]
		c.truncated = true
	}
}

// finish flushes a held carriage return and drops incomplete UTF-8 or escape
// state. Sanitize the diagnostic separately so an unfinished child escape
// sequence cannot swallow it.
func (c *shellCollector) finish(diagnostic string) (output string, truncated bool) {
	if c.pendingCR {
		c.appendSafe(c.sanitizer.Feed("\n"))
	}
	if safe := terminal.Sanitize(diagnostic); safe != "" {
		if c.tail != "" && !strings.HasSuffix(c.tail, "\n") {
			c.appendSafe("\n")
		}
		c.appendSafe(safe)
	}
	return c.tail, c.truncated
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
		var collector shellCollector
		runResult := run(ctx, tool, command, collector.Feed)
		diagnostic := ""
		if runResult.InfraErr != nil {
			diagnostic = "shell: " + runResult.InfraErr.Error()
		}
		output, truncated := collector.finish(diagnostic)
		fullPath := ""
		if runResult.FullOutputPath != "" {
			if _, err := os.Stat(runResult.FullOutputPath); err == nil {
				fullPath = runResult.FullOutputPath
			}
		}
		lifetime.results <- shellResult{
			output:         output,
			exitCode:       runResult.ExitCode,
			cancelled:      runResult.Cancelled,
			truncated:      runResult.Truncated || truncated,
			fullOutputPath: fullPath,
			infraErr:       runResult.InfraErr,
		}
	}()
	return lifetime
}
