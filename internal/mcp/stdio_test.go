package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"os"
	"os/exec"
	"reflect"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	stdioFinalFirst  = `{"jsonrpc":"2.0","id":1,"result":{"content":[{"type":"text","text":"first reply"}]}}`
	stdioFinalLast   = `{"jsonrpc":"2.0","id":2,"result":{"content":[{"type":"text","text":"final reply"}]}}`
	stdioPartialTail = `{"jsonrpc":"2.0","id":3`
)

const (
	stdioConfiguredValueMissingExit = 2
	stdioAmbientValuePresentExit    = 3
)

func TestMCPStdioEnvironmentHelper(t *testing.T) {
	if os.Getenv("PSI_MCP_ENV_HELPER") != "1" ||
		flag.Lookup("test.run").Value.String() != "^TestMCPStdioEnvironmentHelper$" {
		return
	}
	if os.Getenv("PSI_MCP_CONFIGURED") != "configured-value" {
		os.Exit(stdioConfiguredValueMissingExit)
	}
	if os.Getenv("PSI_MCP_AMBIENT_SENTINEL") != "" {
		os.Exit(stdioAmbientValuePresentExit)
	}
	os.Exit(0)
}

func TestStdioServerUsesOnlyConfiguredEnvironment(t *testing.T) {
	t.Setenv("PSI_MCP_AMBIENT_SENTINEL", "parent-only")
	transport := newStdioTransport(Server{
		Command: os.Args[0],
		Args:    []string{"-test.run=^TestMCPStdioEnvironmentHelper$"},
		Env: map[string]string{
			"PSI_MCP_ENV_HELPER": "1",
			"PSI_MCP_CONFIGURED": "configured-value",
		},
	}, t.TempDir())
	connection, err := transport.Connect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	stdio := connection.(*stdioConnection)
	<-stdio.done
	if err := connection.Close(); err != nil {
		t.Fatalf("configured-only stdio helper failed: %v", err)
	}
}

func TestMCPStdioTailHelper(t *testing.T) {
	if os.Getenv("PSI_MCP_TAIL_HELPER") != "1" || flag.Lookup("test.run").Value.String() != "^TestMCPStdioTailHelper$" {
		return
	}
	output := stdioFinalFirst + "\n" + stdioFinalLast + "\n"
	switch os.Getenv("PSI_MCP_TAIL_MODE") {
	case "partial":
		output += stdioPartialTail
	case "malformed":
		output += "not-json\n"
	}
	if _, err := io.WriteString(os.Stdout, output); err != nil {
		os.Exit(2)
	}
	os.Exit(0)
}

func TestStdioDrainsFramesAfterProcessExit(t *testing.T) {
	for _, mode := range []string{"complete", "partial", "malformed"} {
		t.Run(mode, func(t *testing.T) {
			transport := newStdioTransport(Server{
				Command: os.Args[0],
				Args:    []string{"-test.run=^TestMCPStdioTailHelper$"},
				Env: map[string]string{
					"PSI_MCP_TAIL_HELPER": "1",
					"PSI_MCP_TAIL_MODE":   mode,
				},
			}, t.TempDir())
			connection, err := transport.Connect(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = connection.Close() })
			// Leave both replies in the OS pipe until the leader has been
			// reaped. Waiting must not discard already-written frames.
			select {
			case <-connection.(*stdioConnection).done:
			case <-time.After(10 * time.Second):
				t.Fatal("stdio fixture did not exit")
			}
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			for _, frame := range []string{stdioFinalFirst, stdioFinalLast} {
				got, err := connection.Read(ctx)
				if err != nil {
					t.Fatalf("lost final frame after process exit: %v", err)
				}
				want, err := jsonrpc.DecodeMessage([]byte(frame))
				if err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(got, want) {
					t.Fatalf("final frame = %#v; want %#v", got, want)
				}
			}
			_, err = connection.Read(ctx)
			switch mode {
			case "complete":
				if !errors.Is(err, io.EOF) {
					t.Fatalf("after final replies: %v; want EOF", err)
				}
			case "partial":
				if err == nil || !strings.Contains(err.Error(), "incomplete stdio frame") {
					t.Fatalf("after partial tail: %v; want incomplete frame error", err)
				}
			case "malformed":
				if err == nil || !strings.Contains(err.Error(), "malformed stdio frame") {
					t.Fatalf("after malformed tail: %v; want malformed frame error", err)
				}
			}
		})
	}
}

func TestStdioCloseUnblocksReadWithOpenPeer(t *testing.T) {
	stdout, stdoutPeer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = stdoutPeer.Close() })
	done := make(chan struct{})
	close(done)
	connection := &stdioConnection{
		stdout:     bufio.NewReader(stdout),
		stdoutPipe: stdout,
		messages:   make(chan jsonrpc.Message),
		stopping:   make(chan struct{}),
		done:       done,
	}
	go connection.readLoop()
	// Model a reaped leader whose descendant still holds stdout open.
	// Closing the transport must release Read without that peer cooperating.
	result := make(chan error, 1)
	go func() {
		_, err := connection.Read(context.Background())
		result <- err
	}()
	closed := make(chan struct{})
	go func() {
		_ = connection.Close()
		close(closed)
	}()
	awaitStdioEvent(t, closed, "Close with an open stdout peer")
	select {
	case err := <-result:
		if !errors.Is(err, sdk.ErrConnectionClosed) {
			t.Fatalf("Read after Close = %v; want %v", err, sdk.ErrConnectionClosed)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("transport close left Read blocked on an open peer")
	}
	// Only the reader closes its end. Once the peer goes away, the reader
	// must release that pipe and stop.
	_ = stdoutPeer.Close()
	drained := make(chan struct{})
	go func() {
		for range connection.messages {
		}
		close(drained)
	}()
	awaitStdioEvent(t, drained, "reader exit after the peer closed stdout")
}

type stdioReaderFunc func([]byte) (int, error)

func (f stdioReaderFunc) Read(p []byte) (int, error) { return f(p) }

func TestStdioStopsReadingBetweenFrameChunks(t *testing.T) {
	connection := &stdioConnection{stopping: make(chan struct{})}
	connection.stdout = bufio.NewReader(stdioReaderFunc(func(p []byte) (int, error) {
		select {
		case <-connection.stopping:
			t.Fatal("read another chunk after shutdown started")
		default:
		}
		// Close while this read is in flight. The peer returns a full chunk
		// without a newline, but might not return any further data.
		close(connection.stopping)
		for i := range p {
			p[i] = 'x'
		}
		return len(p), nil
	}))
	if _, err := connection.readFrame(); !errors.Is(err, sdk.ErrConnectionClosed) {
		t.Fatalf("read incomplete frame during shutdown: %v; want closed connection", err)
	}
}

func TestStdioReadCancellationStopsConnection(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		connection := &stdioConnection{messages: make(chan jsonrpc.Message), stopping: make(chan struct{})}
		result := make(chan error, 1)
		go func() {
			_, err := connection.Read(ctx)
			result <- err
		}()
		synctest.Wait() // Read is waiting for a frame.
		cancel()
		if err := <-result; !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled Read returned %v; want context.Canceled", err)
		}
		select {
		case <-connection.stopping:
		default:
			t.Fatal("canceled Read returned without stopping the connection")
		}
	})
}

type stdioTestWriter struct {
	write func([]byte) (int, error)
	close func()
}

func (w stdioTestWriter) Write(p []byte) (int, error) { return w.write(p) }

func (w stdioTestWriter) Close() error {
	w.close()
	return nil
}

func awaitStdioEvent(t *testing.T, event <-chan struct{}, operation string) {
	t.Helper()
	select {
	case <-event:
	case <-time.After(10 * time.Second):
		t.Fatalf("timed out waiting for %s", operation)
	}
}

func TestStdioReapsLeaderWithBlockedWrite(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestMCPStdioTailHelper$")
	cmd.Env = append(os.Environ(), "PSI_MCP_TAIL_HELPER=1")
	prepareCommand(cmd)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	started, release, inputClosed := make(chan struct{}), make(chan struct{}), make(chan struct{})
	unblock := sync.OnceFunc(func() { close(release) })
	defer unblock()
	connection := &stdioConnection{
		cmd: cmd,
		stdin: stdioTestWriter{
			write: func(p []byte) (int, error) {
				close(started)
				<-release
				return len(p), nil
			},
			close: func() { close(inputClosed) },
		},
		stopping: make(chan struct{}),
		writeCh:  make(chan struct{}, 1),
		done:     make(chan struct{}),
	}
	connection.writeCh <- struct{}{}
	result := make(chan error, 1)
	go func() {
		result <- connection.Write(ctx, &jsonrpc.Request{Method: "notification"})
	}()
	// Wait on the process only after a write is in flight. The fake stdin
	// models a surviving descendant that retains the pipe after leader exit.
	select {
	case <-started:
	case <-ctx.Done():
		go connection.wait() // Reap the fixture even if Write did not start.
		t.Fatal("write never reached stdin")
	}
	go connection.wait()
	awaitStdioEvent(t, connection.done, "leader exit with stdin still blocked")
	select {
	case err := <-result:
		if !errors.Is(err, sdk.ErrConnectionClosed) {
			t.Fatalf("blocked Write after leader exit = %v; want %v", err, sdk.ErrConnectionClosed)
		}
	case <-ctx.Done():
		t.Fatal("leader exit left Write blocked")
	}
	select {
	case <-inputClosed:
		t.Fatal("stdin closed while a write still owned it")
	default:
	}
	unblock()
	awaitStdioEvent(t, inputClosed, "stdin close by the writer")
}

func TestStdioReportsOversizedFrameDuringAbort(t *testing.T) {
	stdout, err := os.CreateTemp(t.TempDir(), "stdout-*")
	if err != nil {
		t.Fatal(err)
	}
	if err := stdout.Truncate(maxStdinFrame + 1); err != nil {
		_ = stdout.Close()
		t.Fatal(err)
	}
	closing, release := make(chan struct{}), make(chan struct{})
	defer close(release)
	connection := &stdioConnection{
		stdin: stdioTestWriter{close: func() {
			close(closing)
			<-release
		}},
		stdout:     bufio.NewReaderSize(stdout, stdioReadBufferSize),
		stdoutPipe: stdout,
		messages:   make(chan jsonrpc.Message),
		stopping:   make(chan struct{}),
	}
	go connection.readLoop()
	// Read after abort starts but before it finishes closing stdin. Shutdown
	// must not mask the frame-size error that caused it.
	awaitStdioEvent(t, closing, "abort on oversized stdout frame")
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	var limit *limitError
	if _, err := connection.Read(ctx); !errors.As(err, &limit) || limit.what != "stdio frame" {
		t.Fatalf("oversized frame returned %v; want stdio frame limit error", err)
	}
}

func TestMCPStdioRoundTripHelper(t *testing.T) {
	if os.Getenv("PSI_MCP_ROUNDTRIP_HELPER") != "1" || flag.Lookup("test.run").Value.String() != "^TestMCPStdioRoundTripHelper$" {
		return
	}
	stdin := bufio.NewReader(os.Stdin)
	for {
		line, err := stdin.ReadBytes('\n')
		if err == io.EOF && len(line) == 0 {
			os.Exit(0)
		}
		if err != nil {
			os.Exit(2)
		}
		line = bytes.TrimSuffix(line, []byte("\n"))
		line = bytes.TrimSuffix(line, []byte("\r"))
		msg, err := jsonrpc.DecodeMessage(line)
		if err != nil {
			os.Exit(2)
		}
		req, ok := msg.(*jsonrpc.Request)
		if !ok {
			os.Exit(2)
		}
		data, err := jsonrpc.EncodeMessage(&jsonrpc.Response{ID: req.ID, Result: req.Params})
		if err != nil {
			os.Exit(2)
		}
		if _, err := os.Stdout.Write(append(data, '\n')); err != nil {
			os.Exit(2)
		}
	}
}

func connectStdioEcho(t testing.TB) sdk.Connection {
	t.Helper()
	transport := newStdioTransport(Server{
		Command: os.Args[0],
		Args:    []string{"-test.run=^TestMCPStdioRoundTripHelper$"},
		Env: map[string]string{
			"PSI_MCP_ROUNDTRIP_HELPER": "1",
		},
	}, t.TempDir())
	connection, err := transport.Connect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = connection.Close() })
	return connection
}

func TestStdioRoundTrip(t *testing.T) {
	id, err := jsonrpc.MakeID("round-trip-1")
	if err != nil {
		t.Fatal(err)
	}
	connection := connectStdioEcho(t)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	echo := func(params json.RawMessage) *jsonrpc.Response {
		t.Helper()
		if err := connection.Write(ctx, &jsonrpc.Request{ID: id, Method: "echo", Params: params}); err != nil {
			t.Fatalf("stdio round trip write: %v", err)
		}
		msg, err := connection.Read(ctx)
		if err != nil {
			t.Fatalf("stdio round trip read: %v", err)
		}
		got, ok := msg.(*jsonrpc.Response)
		if !ok {
			t.Fatalf("round trip reply = %T; want *jsonrpc.Response", msg)
		}
		if got.Error != nil || got.ID != id || !bytes.Equal(got.Result, params) {
			t.Fatalf("invalid round trip reply: id=%v, error=%v, result length=%d (want %d)", got.ID, got.Error, len(got.Result), len(params))
		}
		return got
	}
	small := json.RawMessage(`{"ping":"hello"}`)
	first := echo(small)
	// A multi-chunk reply refills the read buffer. The small reply must
	// still own its decoded payload after the buffer has been reused.
	echo(json.RawMessage(`{"ping":"` + strings.Repeat("x", stdioReadBufferSize) + `"}`))
	if !bytes.Equal(first.Result, small) {
		t.Fatalf("first reply changed after the next read: %q", first.Result)
	}
}

func BenchmarkStdioRoundTrip(b *testing.B) {
	connection := connectStdioEcho(b)
	ctx, cancel := context.WithTimeout(b.Context(), 30*time.Second)
	defer cancel()
	id, err := jsonrpc.MakeID("round-trip-1")
	if err != nil {
		b.Fatal(err)
	}
	params := json.RawMessage(`{"ping":"` + strings.Repeat("x", 1024) + `"}`)
	req := &jsonrpc.Request{ID: id, Method: "echo", Params: params}
	b.ReportAllocs()
	for b.Loop() {
		if err := connection.Write(ctx, req); err != nil {
			b.Fatal(err)
		}
		if _, err := connection.Read(ctx); err != nil {
			b.Fatal(err)
		}
	}
}
