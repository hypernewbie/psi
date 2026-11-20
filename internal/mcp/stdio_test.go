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
	if os.Getenv("PSI_MCP_TAIL_MODE") == "partial" {
		output += stdioPartialTail
	}
	if _, err := io.WriteString(os.Stdout, output); err != nil {
		os.Exit(2)
	}
	os.Exit(0)
}

func TestStdioDrainsFramesAfterProcessExit(t *testing.T) {
	for _, mode := range []string{"complete", "partial"} {
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
			if mode == "complete" {
				if !errors.Is(err, io.EOF) {
					t.Fatalf("after final replies: %v; want EOF", err)
				}
			} else if err == nil || !strings.Contains(err.Error(), "incomplete stdio frame") {
				t.Fatalf("after partial tail: %v; want incomplete frame error", err)
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
		frames:     make(chan []byte),
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
		for range connection.frames {
		}
		close(drained)
	}()
	awaitStdioEvent(t, drained, "reader exit after the peer closed stdout")
}

func TestStdioReadCancellationStopsConnection(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		connection := &stdioConnection{frames: make(chan []byte), stopping: make(chan struct{})}
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
		stdout:     bufio.NewReaderSize(stdout, 64<<10),
		stdoutPipe: stdout,
		frames:     make(chan []byte),
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
	line, err := bufio.NewReader(os.Stdin).ReadBytes('\n')
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
	data = append(data, '\n')
	if _, err := os.Stdout.Write(data); err != nil {
		os.Exit(2)
	}
	os.Exit(0)
}

func TestStdioRoundTrip(t *testing.T) {
	id, err := jsonrpc.MakeID("round-trip-1")
	if err != nil {
		t.Fatal(err)
	}
	params := json.RawMessage(`{"ping":"hello"}`)
	want := &jsonrpc.Request{ID: id, Method: "echo", Params: params}
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
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	if err := connection.Write(ctx, want); err != nil {
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
	if got.Error != nil {
		t.Fatalf("round trip reply error = %v", got.Error)
	}
	if !reflect.DeepEqual(got.ID, want.ID) {
		t.Fatalf("round trip id = %#v; want %#v", got.ID, want.ID)
	}
	if string(got.Result) != string(params) {
		t.Fatalf("round trip result = %s; want %s", got.Result, params)
	}
}
