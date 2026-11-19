//go:build unix

package app

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// rpcChildProcess runs `psi --mode rpc` with fixture-owned pipes.
type rpcChildProcess struct {
	t      *testing.T
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	log    *rpcLog
	stderr bytes.Buffer
	done   chan error
	joined bool
}

func startRPCChild(t *testing.T, f *quitFixture) *rpcChildProcess {
	t.Helper()
	args := []string{"--mode", "rpc", "--deny-project", "--no-tools"}
	cmd := exec.Command(os.Args[0], "-test.run=^TestReviewAppProcess$")
	env := appTestEnv(f.home, f.agentDir)
	if *reviewBinary != "" {
		executable, err := filepath.Abs(*reviewBinary)
		if err != nil {
			t.Fatal(err)
		}
		cmd = exec.Command(executable, args...)
	} else {
		encoded, err := json.Marshal(args)
		if err != nil {
			t.Fatal(err)
		}
		env = append(env, "PSI_REVIEW_PROCESS=1", "PSI_REVIEW_ARGS="+string(encoded))
	}
	cmd.Dir = f.work
	cmd.Env = env
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	p := &rpcChildProcess{t: t, cmd: cmd, stdin: stdin, log: &rpcLog{notify: make(chan struct{}, 1)}, done: make(chan error, 1)}
	// Wait joins exec's pipe readers and bounds the drain if an inherited
	// output descriptor is still open after process exit.
	cmd.Stdout, cmd.Stderr = p.log, &p.stderr
	cmd.WaitDelay = 2 * time.Second
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	go func() { p.done <- cmd.Wait() }()
	t.Cleanup(p.cleanup)
	return p
}

func (p *rpcChildProcess) cleanup() {
	_ = p.closeStdinNow()
	if p.joined {
		return
	}
	select {
	case <-p.done:
	case <-time.After(5 * time.Second):
		_ = p.cmd.Process.Kill()
		<-p.done
	}
}

func (p *rpcChildProcess) send(command map[string]any) {
	p.t.Helper()
	line, err := json.Marshal(command)
	if err != nil {
		p.t.Fatal(err)
	}
	if _, err := p.stdin.Write(append(line, '\n')); err != nil {
		p.t.Fatalf("send %v: %v", command, err)
	}
}

func (p *rpcChildProcess) closeStdin() {
	p.t.Helper()
	if err := p.closeStdinNow(); err != nil {
		p.t.Fatal(err)
	}
}

func (p *rpcChildProcess) closeStdinNow() error {
	if p.stdin == nil {
		return nil
	}
	stdin := p.stdin
	p.stdin = nil
	return stdin.Close()
}

func (p *rpcChildProcess) waitExit() {
	p.t.Helper()
	select {
	case err := <-p.done:
		p.joined = true
		if err != nil {
			p.t.Fatalf("rpc process failed: %v\nstderr: %s\nstdout: %s", err, p.stderr.String(), p.log.rawString())
		}
		if raw := p.log.rawString(); !strings.HasSuffix(raw, "\n") {
			p.t.Fatalf("RPC output ended with an incomplete JSONL frame: %q", raw)
		}
		p.log.objects(p.t) // Validate frames written after the last live check too.
	case <-time.After(8 * time.Second):
		_ = p.cmd.Process.Kill()
		<-p.done
		p.joined = true
		p.t.Fatalf("rpc process did not exit\nstderr: %s\nstdout: %s", p.stderr.String(), p.log.rawString())
	}
}

func (l *rpcLog) rawString() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.String()
}

func rpcSawPartial(objects []map[string]any) bool {
	for _, obj := range objects {
		if obj["type"] != "message_update" {
			continue
		}
		update, _ := obj["assistantMessageEvent"].(map[string]any)
		if update["type"] == "text_delta" && update["delta"] == quitPartial {
			return true
		}
	}
	return false
}

func rpcSucceeded(id, command string) func([]map[string]any) bool {
	return func(objects []map[string]any) bool {
		for _, obj := range objects {
			if obj["type"] == "response" && obj["id"] == id && obj["command"] == command && obj["success"] == true {
				return true
			}
		}
		return false
	}
}

func rpcAbortedSettlement(objects []map[string]any) bool {
	for _, obj := range objects {
		if obj["type"] == "agent_settled" && obj["aborted"] == true {
			return true
		}
	}
	return false
}

func TestActualRPCAbortAndEOFSettle(t *testing.T) {
	for _, tc := range []struct {
		name  string
		abort bool
	}{
		{name: "abort", abort: true},
		{name: "eof", abort: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newQuitFixture(t)
			p := startRPCChild(t, f)
			p.send(map[string]any{"id": "prompt-1", "type": "prompt", "message": "partial work"})
			p.log.waitObjects(t, "streamed partial", rpcSawPartial)

			if tc.abort {
				p.send(map[string]any{"id": "abort-1", "type": "abort"})
				p.log.waitObjects(t, "abort response", rpcSucceeded("abort-1", "abort"))
				p.log.waitObjects(t, "aborted settlement", rpcAbortedSettlement)
				p.send(map[string]any{"id": "state-1", "type": "get_state"})
				p.log.waitObjects(t, "get_state response after abort", rpcSucceeded("state-1", "get_state"))
			} else {
				// No abort command: closing stdin must cancel and settle
				// the owned run before the process exits.
				p.closeStdin()
				p.log.waitObjects(t, "aborted settlement", rpcAbortedSettlement)
			}

			p.closeStdin()
			p.waitExit()
			if n := f.requests.Load(); n != 1 {
				t.Fatalf("provider requests = %d, want 1", n)
			}
			assertAbortedQuitSession(t, f.work, f.agentDir)
		})
	}
}
