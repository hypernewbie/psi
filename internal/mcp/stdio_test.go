package mcp

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"io"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
)

const (
	stdioFinalFirst  = `{"jsonrpc":"2.0","id":1,"result":{"content":[{"type":"text","text":"first reply"}]}}`
	stdioFinalLast   = `{"jsonrpc":"2.0","id":2,"result":{"content":[{"type":"text","text":"final reply"}]}}`
	stdioPartialTail = `{"jsonrpc":"2.0","id":3`
)

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
	stdout, _ := pipe()
	stderr, _ := pipe()
	done := make(chan struct{})
	close(done)
	connection := &stdioConnection{
		stdin:      stdin,
		stdout:     bufio.NewReader(stdout),
		stdoutPipe: stdout,
		stderrPipe: stderr,
		done:       done,
	}
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
		if !errors.Is(err, os.ErrClosed) {
			t.Fatalf("Read after Close = %v; want a closed pipe error", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("transport close left Read blocked on an open peer")
	}
}
