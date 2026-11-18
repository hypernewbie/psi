//go:build unix

package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
	"testing"
	"time"
)

func TestMCPProcessGroupHelper(t *testing.T) {
	if os.Getenv("PSI_MCP_GROUP_HELPER") != "1" || flag.Lookup("test.run").Value.String() != "^TestMCPProcessGroupHelper$" {
		return
	}
	control, err := net.Dial("tcp", os.Getenv("PSI_MCP_GROUP_CONTROL"))
	if err != nil {
		os.Exit(2)
	}
	role := os.Getenv("PSI_MCP_GROUP_ROLE")
	encoder := json.NewEncoder(control)
	if role == "descendant" {
		signal.Ignore(syscall.SIGTERM)
	}
	if encoder.Encode(processGroupEvent{Role: role, PID: os.Getpid(), Group: syscall.Getpgrp()}) != nil {
		os.Exit(2)
	}
	if role == "wrapper" {
		child := exec.Command(os.Args[0], "-test.run=^TestMCPProcessGroupHelper$")
		child.Env = mergeEnvironment(os.Environ(), map[string]string{"PSI_MCP_GROUP_ROLE": "descendant"})
		child.Stdin, child.Stdout, child.Stderr = os.Stdin, os.Stdout, os.Stderr
		// No Setpgid: this child deliberately stays in the wrapper's group.
		if child.Start() != nil {
			os.Exit(2)
		}
	}
	reader := bufio.NewReader(control)
	for {
		command, err := reader.ReadString('\n')
		if err != nil || command == "exit\n" {
			os.Exit(0)
		}
		if command == "wait-stdin\n" && role == "wrapper" {
			if encoder.Encode(processGroupEvent{Role: "stdin-armed"}) != nil {
				os.Exit(2)
			}
			_, _ = io.Copy(io.Discard, os.Stdin)
			os.Exit(0)
		}
		if command == "ping\n" {
			if encoder.Encode(processGroupEvent{Role: "alive"}) != nil {
				os.Exit(2)
			}
		}
	}
}

type processGroupEvent struct {
	Role  string
	PID   int
	Group int
}

func TestStdioCloseReachesDescendantAfterLeaderExit(t *testing.T) {
	for _, mode := range []string{"already-exited", "stdin-close"} {
		t.Run(mode, func(t *testing.T) {
			listener, err := net.ListenTCP("tcp", &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1)})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = listener.Close() })
			accept := func(role string) (net.Conn, *json.Decoder, processGroupEvent) {
				t.Helper()
				// Watchdogs detect deadlock; there is no elapsed-time assertion.
				if err := listener.SetDeadline(time.Now().Add(10 * time.Second)); err != nil {
					t.Fatal(err)
				}
				control, err := listener.Accept()
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = control.Close() })
				if err := control.SetDeadline(time.Now().Add(10 * time.Second)); err != nil {
					t.Fatal(err)
				}
				decoder := json.NewDecoder(control)
				var event processGroupEvent
				if err := decoder.Decode(&event); err != nil || event.Role != role {
					t.Fatalf("child handshake = %+v, %v; want %s", event, err, role)
				}
				return control, decoder, event
			}
			transport := newStdioTransport(Server{Command: os.Args[0], Args: []string{"-test.run=^TestMCPProcessGroupHelper$"}, Env: map[string]string{
				"PSI_MCP_GROUP_HELPER": "1", "PSI_MCP_GROUP_CONTROL": listener.Addr().String(), "PSI_MCP_GROUP_ROLE": "wrapper",
			}}, t.TempDir())
			connection, err := transport.Connect(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			stdio := connection.(*stdioConnection)
			t.Cleanup(func() { _ = stdio.Close() })
			wrapper, wrapperDecoder, leader := accept("wrapper")
			child, decoder, descendant := accept("descendant")
			if leader.PID != stdio.cmd.Process.Pid || leader.Group != leader.PID || descendant.Group != leader.Group {
				t.Fatalf("fixture is not in the owned group: leader=%+v descendant=%+v", leader, descendant)
			}
			command := "exit\n"
			if mode == "stdin-close" {
				command = "wait-stdin\n"
			}
			if _, err := wrapper.Write([]byte(command)); err != nil {
				t.Fatal(err)
			}
			if mode == "already-exited" {
				select {
				case <-stdio.done:
				case <-time.After(10 * time.Second):
					t.Fatal("wrapper never exited")
				}
			} else {
				var event processGroupEvent
				if err := wrapperDecoder.Decode(&event); err != nil || event.Role != "stdin-armed" {
					t.Fatalf("wrapper did not arm stdin close: %+v %v", event, err)
				}
			}
			// A slow wrapper may be terminated by the graceful-close policy;
			// the invariant is that its descendants cannot survive either exit.
			_ = stdio.Close()
			// A surviving child answers this ping. A killed child closes the
			// private socket; unread ping data can make the peer reset it.
			// Fallback fixture cleanup has not run yet.
			_, _ = child.Write([]byte("ping\n"))
			var event processGroupEvent
			if err := decoder.Decode(&event); !errors.Is(err, io.EOF) && !errors.Is(err, syscall.ECONNRESET) {
				t.Fatalf("same-group descendant survived cleanup: %+v %v", event, err)
			}
		})
	}
}
