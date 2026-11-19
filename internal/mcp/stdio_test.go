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
	"reflect"
	"strings"
	"testing"
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
	pipe := func() (*os.File, *os.File) {
		t.Helper()
		reader, writer, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			_ = reader.Close()
			_ = writer.Close()
		})
		return reader, writer
	}
	_, stdin := pipe()
	stdout, stdoutPeer := pipe()
	stderr, _ := pipe()
	done := make(chan struct{})
	close(done)
	connection := &stdioConnection{
		stdin:      stdin,
		stdout:     bufio.NewReader(stdout),
		stdoutPipe: stdout,
		stderrPipe: stderr,
		frames:     make(chan []byte),
		readerStop: make(chan struct{}),
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
	if err := connection.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-result:
		if !errors.Is(err, sdk.ErrConnectionClosed) {
			t.Fatalf("Read after Close = %v; want %v", err, sdk.ErrConnectionClosed)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("transport close left Read blocked on an open peer")
	}
	// Join the reader before cleanup closes the pipe ends: with the peer
	// gone it must report EOF and exit, proving cleanup cannot race a
	// lingering read on Windows.
	_ = stdoutPeer.Close()
	drained := make(chan struct{})
	go func() {
		for range connection.frames {
		}
		close(drained)
	}()
	select {
	case <-drained:
	case <-time.After(10 * time.Second):
		t.Fatal("reader did not exit after the peer closed stdout")
	}
	if _, err := connection.Read(context.Background()); !errors.Is(err, io.EOF) {
		t.Fatalf("Read after peer exit = %v; want EOF", err)
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
