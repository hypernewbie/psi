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
	maxStdinFrame   = 16 << 20
	maxStderrRetain = 64 << 10
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
		stdout:     bufio.NewReaderSize(stdout, 64<<10),
		stdoutPipe: stdout,
		stderrPipe: stderr,
		frames:     make(chan []byte),
		readerStop: make(chan struct{}),
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
	// stdin is nil after close; every access runs under writeMu.
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

	// frames carries complete stdout frames from readLoop to Read. readLoop
	// stores the terminal read error in readErr before closing frames, so a
	// receive on the closed channel observes the stored error.
	frames  chan []byte
	readErr error
	// readerStop is closed by Close and abort. It releases Reads blocked on
	// frames without the reader's cooperation, so Close never waits for a
	// peer that holds stdout open.
	readerStop chan struct{}

	writeCh chan struct{}
	done    chan struct{}
	stderr  *ringBuffer

	writeMu   sync.Mutex
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
	stop := context.AfterFunc(ctx, func() { c.abort() })
	defer stop()
	select {
	case line, ok := <-c.frames:
		if !ok {
			return nil, c.readErr
		}
		return c.decodeFrame(line)
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-c.readerStop:
		// The connection closed while this Read was blocked. Prefer a frame
		// or terminal error the reader already delivered over a generic
		// closed error, so reads after a clean EOF stay deterministic.
		select {
		case line, ok := <-c.frames:
			if !ok {
				return nil, c.readErr
			}
			return c.decodeFrame(line)
		default:
			return nil, sdk.ErrConnectionClosed
		}
	}
}

// decodeFrame parses one delivered frame. A malformed frame fails the
// connection.
func (c *stdioConnection) decodeFrame(line []byte) (jsonrpc.Message, error) {
	msg, err := jsonrpc.DecodeMessage(line)
	if err != nil {
		c.abort()
		return nil, fmt.Errorf("malformed stdio frame: %w", err)
	}
	return msg, nil
}

// readLoop is the sole reader and closer of stdoutPipe. It exits when the
// stream ends or when Close or abort releases it. A descendant that inherits
// stdout can delay its exit, but never Close, which does not wait for this
// goroutine.
func (c *stdioConnection) readLoop() {
	defer func() { _ = c.stdoutPipe.Close() }()
	for {
		line, err := c.readFrame()
		if err != nil {
			c.readErr = err
			close(c.frames)
			return
		}
		select {
		case c.frames <- line:
		case <-c.readerStop:
			return
		}
	}
}

func (c *stdioConnection) readFrame() ([]byte, error) {
	for {
		var buf bytes.Buffer
		for {
			chunk, err := c.stdout.ReadSlice('\n')
			if buf.Len()+len(chunk) > maxStdinFrame {
				c.abort()
				return nil, errLimit("stdio frame")
			}
			buf.Write(chunk)
			if err == nil {
				break
			}
			if errors.Is(err, bufio.ErrBufferFull) {
				continue
			}
			if errors.Is(err, io.EOF) {
				if buf.Len() > 0 {
					return nil, errors.New("incomplete stdio frame at end of stream")
				}
				return nil, io.EOF
			}
			return nil, err
		}
		frame := bytes.TrimSuffix(buf.Bytes(), []byte("\n"))
		frame = bytes.TrimSuffix(frame, []byte("\r"))
		if len(bytes.TrimSpace(frame)) == 0 {
			continue
		}
		return frame, nil
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
	frame := append(append([]byte(nil), data...), '\n')
	writeDone := make(chan error, 1)
	go func() {
		c.writeMu.Lock()
		defer c.writeMu.Unlock()
		if c.stdin == nil {
			writeDone <- sdk.ErrConnectionClosed
			return
		}
		_, err := c.stdin.Write(frame)
		writeDone <- err
	}()
	select {
	case err := <-writeDone:
		if err != nil {
			c.abort()
			return err
		}
		return nil
	case <-ctx.Done():
		c.abort()
		return ctx.Err()
	case <-c.done:
		return sdk.ErrConnectionClosed
	}
}

func (c *stdioConnection) Close() error {
	c.closeOnce.Do(func() {
		c.closed.Store(true)
		close(c.readerStop)
		// Offer stdin EOF for a graceful exit while no write is in flight.
		// A write blocked on a child that never reads would wedge a locked
		// close, and that child would ignore the EOF anyway, so skip the
		// graceful close rather than wait for the mutex.
		c.closeStdinIfIdle()
		exited := c.waitExit(500 * time.Millisecond)
		if !exited {
			signalCommand(c.cmd, false)
			exited = c.waitExit(1500 * time.Millisecond)
		}
		if !exited {
			signalCommand(c.cmd, true)
			exited = c.waitExit(2 * time.Second)
		}
		if exited {
			// The process is dead, so a straggler write drains with a
			// broken-pipe error instead of blocking: reclaim stdin under
			// the write mutex.
			c.closeStdin()
		} else {
			// An unkillable child can hold a write, and this mutex, forever.
			// Skip the reclaim rather than hang Close; one pipe FD leaks
			// with the wedged writer.
			c.closeStdinIfIdle()
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
// without waiting for a graceful protocol shutdown. It performs no I/O waits:
// the kill releases the reader and a stuck write, the stop release fails
// blocked Reads, and the stdin reclaim skips when a write is in flight (wait
// reclaims it once the process exits).
func (c *stdioConnection) abort() {
	c.closed.Store(true)
	c.closeOnce.Do(func() {
		signalCommand(c.cmd, true)
		close(c.readerStop)
		c.closeStdinIfIdle()
	})
}

// closeStdin closes the stdin pipe exactly once. It runs under writeMu so the
// close can never race an in-flight write syscall: on Windows that race
// deadlocks File.Close against the write. Callers must ensure no write can
// stay blocked first; after process exit a straggler write drains with a
// broken-pipe error instead.
func (c *stdioConnection) closeStdin() {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	c.closeStdinLocked()
}

// closeStdinIfIdle closes stdin only when no write holds the mutex. It backs
// the graceful EOF offer and the paths that must never block.
func (c *stdioConnection) closeStdinIfIdle() {
	if !c.writeMu.TryLock() {
		return
	}
	defer c.writeMu.Unlock()
	c.closeStdinLocked()
}

func (c *stdioConnection) closeStdinLocked() {
	if c.stdin == nil {
		return
	}
	_ = c.stdin.Close()
	c.stdin = nil
}

func (c *stdioConnection) wait() {
	err := c.cmd.Wait()
	c.closed.Store(true)
	// The process is dead, so no write can stay blocked: reclaim stdin here
	// to cover every path, including an abort that skipped it mid-write.
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
