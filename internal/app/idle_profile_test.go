//go:build acceptance && unix

package app

import (
	"os"
	"strings"
	"testing"
	"time"

	"psi/internal/terminal"
)

func TestTenIdleSecondsProduceNoScheduledFrames(t *testing.T) {
	r := testRuntime(t)
	input, inputWrite, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	out := &lockedFrameOutput{}
	console := terminal.NewConsole(input, out)
	console.SizeFunc = func() (int, int) { return 80, 24 }
	finished := make(chan error, 1)
	go func() { finished <- r.repl(console) }()
	finishedSeen := false
	defer func() {
		_ = inputWrite.Close()
		if !finishedSeen {
			select {
			case <-finished:
			case <-time.After(5 * time.Second):
			}
		}
		console.Close()
		_ = input.Close()
	}()

	deadline := time.After(5 * time.Second)
	for !strings.Contains(out.String(), "─") {
		select {
		case <-time.After(10 * time.Millisecond):
		case <-deadline:
			t.Fatalf("idle console did not render its editor: %q", out.String())
		}
	}
	// Allow startup notices and cached footer setup to settle before sampling.
	time.Sleep(100 * time.Millisecond)
	beforeWrites, beforeFrames, beforeBytes, _ := console.FrameStats()
	time.Sleep(10 * time.Second)
	afterWrites, afterFrames, afterBytes, _ := console.FrameStats()
	if beforeWrites != afterWrites || beforeFrames != afterFrames || beforeBytes != afterBytes {
		t.Fatalf("idle UI emitted after settling: writes %d→%d frames %d→%d bytes %d→%d", beforeWrites, afterWrites, beforeFrames, afterFrames, beforeBytes, afterBytes)
	}
	if _, err := inputWrite.Write([]byte("/quit\r")); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-finished:
		finishedSeen = true
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("idle REPL did not exit after /quit")
	}
}
