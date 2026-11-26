package app

import (
	"context"
	"os"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/hypernewbie/psi/internal/session"
	"github.com/hypernewbie/psi/internal/terminal"
	"github.com/hypernewbie/psi/internal/tools"
)

const (
	shellOutputBytes = 50 * 1024
	shellOutputLines = 2000
	// maxShellSlots bounds running plus settled-but-unappended direct shells.
	// The editor admits one running shell at a time; completed results may
	// wait for the model boundary up to this process-wide bound.
	maxShellSlots = 8
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

	// observe receives each sanitized chunk before it is retained in the
	// bounded tail. RPC uses it for streamed updates; the editor leaves it nil.
	observe func(string)

	// snapshot is a bounded immutable copy for the live preview. Feed runs on
	// the runner reader goroutine; the REPL only takes this small mutex to
	// copy the latest revision, never while doing terminal work.
	snapshotMu sync.Mutex
	snapshot   string
	revision   uint64
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
	safe := c.sanitizer.Feed(text)
	if safe != "" && c.observe != nil {
		c.observe(safe)
	}
	c.appendSafe(safe)
	c.publish()
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

// publish copies the current bounded tail for the frame timer. The string
// header is immutable after publication, so the reader can format it after
// releasing the lock.
func (c *shellCollector) publish() {
	c.snapshotMu.Lock()
	c.snapshot = c.tail
	c.revision++
	c.snapshotMu.Unlock()
}

// Snapshot returns the latest published preview and its revision.
func (c *shellCollector) Snapshot() (string, uint64) {
	c.snapshotMu.Lock()
	defer c.snapshotMu.Unlock()
	return c.snapshot, c.revision
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
	collector  *shellCollector
}

type shellResult struct {
	output         string
	exitCode       *int
	cancelled      bool
	truncated      bool
	fullOutputPath string
	infraErr       error
}

// pendingShell is a completed command whose durable append waits for the
// model boundary. The result is final and must never be re-run.
type pendingShell struct {
	lifetime *shellLifetime
	result   shellResult
}

// shellOutcome is the command outcome used by display and status rendering.
// A storage failure is a separate persistence state and must not turn a
// successful command into an exit-code failure.
func shellOutcome(res shellResult) string {
	switch {
	case res.cancelled:
		return "cancelled"
	case res.infraErr != nil:
		return "failed"
	case res.exitCode == nil:
		return "interrupted"
	case *res.exitCode != 0:
		return "failed"
	default:
		return "done"
	}
}

// bashExecutionMessage builds the one durable shell entry shared by the
// editor and RPC. Callers append it once, in their owning phase.
func bashExecutionMessage(command string, res shellResult, exclude bool) session.AgentMessage {
	var fullPath *string
	if res.fullOutputPath != "" {
		path := res.fullOutputPath
		fullPath = &path
	}
	return session.AgentMessage{Role: "bashExecution", BashExecution: &session.BashExecutionMessage{
		Role:               "bashExecution",
		Command:            command,
		Output:             res.output,
		ExitCode:           res.exitCode,
		Cancelled:          res.cancelled,
		Truncated:          res.truncated,
		FullOutputPath:     fullPath,
		Timestamp:          time.Now().UnixMilli(),
		ExcludeFromContext: &exclude,
	}}
}

func shellDocumentBlock(command string, res shellResult, exclude, expanded bool) terminal.DocumentBlock {
	return terminal.DocumentBlock{Kind: terminal.DocumentShell, Shell: &terminal.ShellDisplay{
		Command: command, Output: res.output, State: shellOutcome(res),
		ExitCode: res.exitCode, Excluded: exclude,
		Truncated: res.truncated, FullOutputPath: res.fullOutputPath,
		Expanded: expanded,
	}}
}

func (l *shellLifetime) cancelRun() {
	if l == nil {
		return
	}
	l.cancelOnce.Do(l.cancel)
}

func startShellLifetime(parent context.Context, run shellRunFunc, tool *tools.BashTool, command string, exclude bool, callID string, generation int64, store *session.Store) *shellLifetime {
	ctx, cancel := context.WithCancel(parent)
	collector := &shellCollector{}
	lifetime := &shellLifetime{
		cancel:     cancel,
		results:    make(chan shellResult, 1),
		command:    command,
		exclude:    exclude,
		callID:     callID,
		generation: generation,
		store:      store,
		collector:  collector,
	}
	go func() {
		defer cancel()
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
