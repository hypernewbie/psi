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
	cmd.Env = mergeEnvironment(os.Environ(), t.server.Env)
	if t.server.Cwd != "" {
		cwd := expandHome(t.server.Cwd)
		if !filepath.IsAbs(cwd) {
			cwd = filepath.Join(t.baseCwd, cwd)
		}
		cmd.Dir = cwd
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, err
	}
	prepareCommand(cmd)
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	c := &stdioConnection{
		cmd:     cmd,
		stdin:   stdin,
		stdout:  bufio.NewReaderSize(stdout, 64<<10),
		writeCh: make(chan struct{}, 1),
		done:    make(chan struct{}),
		stderr:  &ringBuffer{limit: maxStderrRetain},
	}
	c.writeCh <- struct{}{}
	go c.drainStderr(stderr)
	go c.wait()
	return c, nil
}

type stdioConnection struct {
	cmd     *exec.Cmd
	stdin   io.WriteCloser
	stdout  *bufio.Reader
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
	line, err := c.readFrame()
	if err != nil {
		return nil, err
	}
	msg, err := jsonrpc.DecodeMessage(line)
	if err != nil {
		c.abort()
		return nil, fmt.Errorf("malformed stdio frame: %w", err)
	}
	return msg, nil
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
		_, err := c.stdin.Write(frame)
		c.writeMu.Unlock()
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
		_ = c.stdin.Close()
		select {
		case <-c.done:
			return
		case <-time.After(500 * time.Millisecond):
		}
		signalCommand(c.cmd, false)
		select {
		case <-c.done:
			return
		case <-time.After(1500 * time.Millisecond):
		}
		signalCommand(c.cmd, true)
		select {
		case <-c.done:
		case <-time.After(2 * time.Second):
		}
	})
	c.waitMu.Lock()
	defer c.waitMu.Unlock()
	return c.waitErr
}

// abort is the cancellation path: close pipes and force the owned process
// group without waiting for a graceful protocol shutdown.
func (c *stdioConnection) abort() {
	c.closed.Store(true)
	c.closeOnce.Do(func() {
		_ = c.stdin.Close()
		signalCommand(c.cmd, true)
	})
}

func (c *stdioConnection) wait() {
	err := c.cmd.Wait()
	// Close can return as soon as done closes. Signal remaining owned
	// descendants before publishing exit, even if the launcher exits first.
	terminateCommand(c.cmd)
	c.waitMu.Lock()
	c.waitErr = err
	c.waitMu.Unlock()
	close(c.done)
}

func (c *stdioConnection) drainStderr(reader io.Reader) {
	buf := make([]byte, 32<<10)
	for {
		n, err := reader.Read(buf)
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
