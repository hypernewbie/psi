package mcp

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	maxStdinFrame       = 16 << 20
	maxStderrRetain     = 64 << 10
	stdioReadBufferSize = 64 << 10
)

type stdioTransport struct {
	server  Server
	baseCwd string
}

func newStdioTransport(server Server, baseCwd string) *stdioTransport {
	return &stdioTransport{server: server, baseCwd: baseCwd}
}

func (t *stdioTransport) Connect(ctx context.Context) (sdk.Connection, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	command := expandHome(t.server.Command)
	args := make([]string, len(t.server.Args))
	for i, arg := range t.server.Args {
		args[i] = expandHome(arg)
	}
	cmd := exec.Command(command, args...)
	// Do not pass psi's ambient environment to an MCP server. Go may add
	// SYSTEMROOT on Windows when the configured environment omits it; that
	// variable identifies the Windows installation directory.
	cmd.Env = mergeEnvironment([]string{}, t.server.Env)
	if t.server.Cwd != "" {
		cwd := expandHome(t.server.Cwd)
		if !filepath.IsAbs(cwd) {
			cwd = filepath.Join(t.baseCwd, cwd)
		}
		cmd.Dir = cwd
	}
	// Own the pipes instead of using Cmd's pipe helpers: Wait closes their
	// readers on exit and can discard final frames the SDK has not read yet.
	stdinReader, stdin, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	defer stdinReader.Close()
	stdout, stdoutWriter, err := os.Pipe()
	if err != nil {
		_ = stdin.Close()
		return nil, err
	}
	defer stdoutWriter.Close()
	stderr, stderrWriter, err := os.Pipe()
	if err != nil {
		_ = stdin.Close()
		_ = stdout.Close()
		return nil, err
	}
	defer stderrWriter.Close()
	cmd.Stdin, cmd.Stdout, cmd.Stderr = stdinReader, stdoutWriter, stderrWriter
	c := &stdioConnection{
		cmd:        cmd,
		stdin:      stdin,
		stdout:     bufio.NewReaderSize(stdout, stdioReadBufferSize),
		stdoutPipe: stdout,
		stderrPipe: stderr,
		messages:   make(chan jsonrpc.Message),
		stopping:   make(chan struct{}),
		writeCh:    make(chan struct{}, 1),
		done:       make(chan struct{}),
		stderr:     &ringBuffer{limit: maxStderrRetain},
	}
	prepareCommand(cmd)
	if err := cmd.Start(); err != nil {
		// No goroutine owns these pipes yet; close them directly.
		_ = stdin.Close()
		_ = stdout.Close()
		_ = stderr.Close()
		return nil, err
	}
	c.writeCh <- struct{}{}
	go c.readLoop()
	go c.drainStderr()
	go c.wait()
	return c, nil
}

type stdioConnection struct {
	cmd *exec.Cmd
	// stdin is nil when closed or owned by an in-flight write. stdinMu
	// protects that handoff, not the write syscall.
	stdin io.WriteCloser

	// stdout and stdoutPipe are owned by readLoop: it performs every read
	// and closes the pipe when its final read returns. drainStderr likewise
	// owns stderrPipe. No other goroutine touches the read pipes, because on
	// Windows closing a pipe races an in-flight read: File.Close waits for
	// pending I/O while the cancel it issues can land before the read enters
	// the kernel, deadlocking both sides permanently.
	stdout     *bufio.Reader
	stdoutPipe *os.File
	stderrPipe *os.File

	// readLoop publishes readErr before closing messages.
	messages chan jsonrpc.Message
	readErr  error
	// Close and abort close stopping to release blocked Reads and Writes
	// without waiting for the pipe peer.
	stopping chan struct{}

	writeCh chan struct{}
	done    chan struct{}
	stderr  *ringBuffer

	stdinMu   sync.Mutex
	stderrMu  sync.Mutex
	closeOnce sync.Once
	closed    atomic.Bool
	waitMu    sync.Mutex
	waitErr   error
}

func (c *stdioConnection) SessionID() string { return "" }

func (c *stdioConnection) Read(ctx context.Context) (jsonrpc.Message, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var msg jsonrpc.Message
	var ok bool
	select {
	case msg, ok = <-c.messages:
	case <-ctx.Done():
		c.abort()
		return nil, ctx.Err()
	case <-c.stopping:
		// Preserve a terminal frame error already published by readLoop.
		select {
		case msg, ok = <-c.messages:
		default:
			return nil, sdk.ErrConnectionClosed
		}
	}
	if !ok {
		return nil, c.readErr
	}
	return msg, nil
}

// readLoop is the sole reader and closer of stdoutPipe. It exits when the
// stream ends or when Close or abort releases it. A descendant that inherits
// stdout can delay its exit, but never Close, which does not wait for this
// goroutine.
func (c *stdioConnection) readLoop() {
	abort := false
	defer func() {
		_ = c.stdoutPipe.Close()
		close(c.messages)
		// Publish the frame error before abort wakes Read.
		if abort {
			c.abort()
		}
	}()
	for {
		frame, err := c.readFrame()
		if err != nil {
			c.readErr = err
			var limit *limitError
			abort = errors.As(err, &limit)
			return
		}
		// Decode before the next read reuses the buffer. The decoded message
		// owns its data, so only it crosses the channel.
		msg, err := jsonrpc.DecodeMessage(frame)
		if err != nil {
			c.readErr = fmt.Errorf("malformed stdio frame: %w", err)
			abort = true
			return
		}
		select {
		case c.messages <- msg:
		case <-c.stopping:
			c.readErr = sdk.ErrConnectionClosed
			return
		}
	}
}

// readFrame returns bytes that may borrow stdout's buffer until the next read.
func (c *stdioConnection) readFrame() ([]byte, error) {
	var overflow bytes.Buffer
	for {
		select {
		case <-c.stopping:
			return nil, sdk.ErrConnectionClosed
		default:
		}
		frame, err := c.stdout.ReadSlice('\n')
		size := overflow.Len() + len(frame)
		if size > maxStdinFrame {
			return nil, errLimit("stdio frame")
		}
		switch {
		case errors.Is(err, bufio.ErrBufferFull):
			overflow.Write(frame)
			continue
		case errors.Is(err, io.EOF):
			if size > 0 {
				return nil, errors.New("incomplete stdio frame at end of stream")
			}
			return nil, io.EOF
		case err != nil:
			return nil, err
		}
		if overflow.Len() != 0 {
			overflow.Write(frame)
			frame = overflow.Bytes()
		}
		frame = bytes.TrimSuffix(frame, []byte("\n"))
		frame = bytes.TrimSuffix(frame, []byte("\r"))
		if len(bytes.TrimSpace(frame)) > 0 {
			return frame, nil
		}
		overflow = bytes.Buffer{}
	}
}

func (c *stdioConnection) Write(ctx context.Context, msg jsonrpc.Message) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	select {
	case <-c.writeCh:
	case <-ctx.Done():
		return ctx.Err()
	case <-c.stopping:
		return sdk.ErrConnectionClosed
	case <-c.done:
		return sdk.ErrConnectionClosed
	}
	defer func() { c.writeCh <- struct{}{} }()
	if c.closed.Load() {
		return sdk.ErrConnectionClosed
	}
	data, err := jsonrpc.EncodeMessage(msg)
	if err != nil {
		return err
	}
	frame := append(data, '\n')
	writeDone := make(chan error, 1)
	go func() { writeDone <- c.writeFrame(frame) }()
	select {
	case err := <-writeDone:
		if err != nil && !errors.Is(err, sdk.ErrConnectionClosed) {
			c.abort()
		}
		return err
	case <-ctx.Done():
		c.abort()
		return ctx.Err()
	case <-c.stopping:
		return sdk.ErrConnectionClosed
	case <-c.done:
		return sdk.ErrConnectionClosed
	}
}

func (c *stdioConnection) writeFrame(frame []byte) error {
	c.stdinMu.Lock()
	if c.closed.Load() || c.stdin == nil {
		c.stdinMu.Unlock()
		return sdk.ErrConnectionClosed
	}
	stdin := c.stdin
	c.stdin = nil
	c.stdinMu.Unlock()

	_, err := stdin.Write(frame)

	c.stdinMu.Lock()
	defer c.stdinMu.Unlock()
	if c.closed.Load() {
		_ = stdin.Close()
	} else {
		c.stdin = stdin
	}
	return err
}

func (c *stdioConnection) Close() error {
	c.closeOnce.Do(func() {
		c.closed.Store(true)
		close(c.stopping)
		// Offer stdin EOF if no write is in flight. Otherwise the writer
		// owns the pipe and closes it when its syscall returns.
		c.closeStdin()
		if c.waitExit(500 * time.Millisecond) {
			return
		}
		signalCommand(c.cmd, false)
		if !c.waitExit(1500 * time.Millisecond) {
			signalCommand(c.cmd, true)
			c.waitExit(2 * time.Second)
		}
	})
	c.waitMu.Lock()
	defer c.waitMu.Unlock()
	return c.waitErr
}

// waitExit reports whether the process exited within d.
func (c *stdioConnection) waitExit(d time.Duration) bool {
	select {
	case <-c.done:
		return true
	case <-time.After(d):
		return false
	}
}

// abort is the cancellation path: force-stop the owned process or group
// without waiting for a graceful protocol shutdown or outstanding pipe I/O.
func (c *stdioConnection) abort() {
	c.closed.Store(true)
	c.closeOnce.Do(func() {
		signalCommand(c.cmd, true)
		close(c.stopping)
		c.closeStdin()
	})
}

// closeStdin closes only idle stdin. File.Close must not race a write syscall
// on Windows, even after the launcher exits: a descendant can keep stdin open.
func (c *stdioConnection) closeStdin() {
	c.stdinMu.Lock()
	defer c.stdinMu.Unlock()
	if c.stdin == nil {
		return
	}
	_ = c.stdin.Close()
	c.stdin = nil
}

func (c *stdioConnection) wait() {
	err := c.cmd.Wait()
	c.closed.Store(true)
	c.closeStdin()
	// Close can return as soon as done closes. Signal remaining owned
	// descendants before publishing exit, even if the launcher exits first.
	terminateCommand(c.cmd)
	c.waitMu.Lock()
	c.waitErr = err
	c.waitMu.Unlock()
	close(c.done)
}

func (c *stdioConnection) drainStderr() {
	// This goroutine owns stderrPipe: it closes the pipe when the stream
	// ends, after its own final read, so the close can never race another
	// goroutine's I/O.
	defer func() { _ = c.stderrPipe.Close() }()
	buf := make([]byte, 32<<10)
	for {
		n, err := c.stderrPipe.Read(buf)
		if n > 0 {
			c.stderrMu.Lock()
			_, _ = c.stderr.Write(buf[:n])
			c.stderrMu.Unlock()
		}
		if err != nil {
			return
		}
	}
}

func (c *stdioConnection) stderrTail() string {
	c.stderrMu.Lock()
	defer c.stderrMu.Unlock()
	return c.stderr.String()
}

func mergeEnvironment(base []string, overlay map[string]string) []string {
	if len(overlay) == 0 {
		return base
	}
	index := map[string]int{}
	out := append([]string(nil), base...)
	for i, entry := range out {
		if eq := strings.IndexByte(entry, '='); eq >= 0 {
			index[entry[:eq]] = i
		}
	}
	for name, value := range overlay {
		entry := name + "=" + value
		if i, ok := index[name]; ok {
			out[i] = entry
		} else {
			index[name] = len(out)
			out = append(out, entry)
		}
	}
	return out
}

func expandHome(path string) string {
	if path == "~" {
		if home, err := os.UserHomeDir(); err == nil {
			return home
		}
	}
	if strings.HasPrefix(path, "~/") || strings.HasPrefix(path, `~\`) {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, path[2:])
		}
	}
	return path
}
