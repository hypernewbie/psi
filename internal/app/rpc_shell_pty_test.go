//go:build unix

package app

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hypernewbie/psi/internal/session"
)

func TestRPCBashActualApp(t *testing.T) {
	if *reviewBinary == "" {
		t.Skip("RPC shell proof requires -psi-test-binary")
	}
	f := newQuitFixture(t)
	if err := os.MkdirAll(filepath.Join(f.home, "tmp"), 0o700); err != nil {
		t.Fatal(err)
	}
	sessionDir := filepath.Join(f.home, "rpc-sessions")
	if err := os.MkdirAll(sessionDir, 0o700); err != nil {
		t.Fatal(err)
	}
	id := "rpc-shell"
	gate := filepath.Join(f.home, "release-shell")
	p := startRPCChild(t, f, "--session-dir", sessionDir, "--session-id", id)
	p.send(map[string]any{"id": "shell-1", "type": "bash", "command": fmt.Sprintf("printf RPC_SHELL_LIVE; while [ ! -f %q ]; do sleep 0.05; done", gate), "excludeFromContext": true})
	p.log.waitObjects(t, "live shell update", func(objects []map[string]any) bool {
		for _, obj := range objects {
			if obj["type"] == "bash_execution_update" && obj["id"] == "shell-1" && strings.Contains(fmt.Sprint(obj["delta"]), "RPC_SHELL_LIVE") {
				return true
			}
		}
		return false
	})
	if err := os.WriteFile(gate, []byte("release"), 0o600); err != nil {
		t.Fatal(err)
	}
	p.log.waitObjects(t, "shell response", func(objects []map[string]any) bool {
		for _, obj := range objects {
			if obj["type"] != "response" || obj["id"] != "shell-1" || obj["command"] != "bash" {
				continue
			}
			data, _ := obj["data"].(map[string]any)
			return obj["success"] == true && strings.Contains(fmt.Sprint(data["output"]), "RPC_SHELL_LIVE") && data["exitCode"] == float64(0)
		}
		return false
	})
	p.closeStdin()
	p.waitExit()
	if f.requests.Load() != 0 {
		t.Fatalf("provider requests = %d, want 0", f.requests.Load())
	}
	sessionPath := filepath.Join(sessionDir, id+".jsonl")
	store, err := session.OpenFile(sessionPath, f.work, false)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Unlock()
	messages := shellMessages(store)
	if len(messages) != 1 {
		t.Fatalf("shell entries = %d, want 1", len(messages))
	}
	message := messages[0]
	if !strings.Contains(message.Output, "RPC_SHELL_LIVE") || message.ExitCode == nil || *message.ExitCode != 0 || message.Cancelled || message.ExcludeFromContext == nil || !*message.ExcludeFromContext {
		t.Fatalf("saved RPC shell = %+v", message)
	}
}

func TestRPCBashOverlapActualApp(t *testing.T) {
	if *reviewBinary == "" {
		t.Skip("RPC overlap proof requires -psi-test-binary")
	}
	f := newQuitFixture(t)
	if err := os.MkdirAll(filepath.Join(f.home, "tmp"), 0o700); err != nil {
		t.Fatal(err)
	}
	sessionDir := filepath.Join(f.home, "rpc-sessions")
	if err := os.MkdirAll(sessionDir, 0o700); err != nil {
		t.Fatal(err)
	}
	id := "rpc-overlap"
	p := startRPCChild(t, f, "--session-dir", sessionDir, "--session-id", id)
	p.send(map[string]any{"id": "prompt-1", "type": "prompt", "message": "hold the model"})
	p.log.waitObjects(t, "model partial", rpcSawPartial)
	gate1, gate2 := filepath.Join(f.home, "gate1"), filepath.Join(f.home, "gate2")
	p.send(map[string]any{"id": "shell-1", "type": "bash", "command": fmt.Sprintf("printf ONE; while [ ! -f %q ]; do sleep 0.05; done", gate1)})
	p.send(map[string]any{"id": "shell-2", "type": "bash", "command": fmt.Sprintf("printf TWO; while [ ! -f %q ]; do sleep 0.05; done", gate2), "excludeFromContext": true})
	p.log.waitObjects(t, "first shell output", func(objects []map[string]any) bool {
		for _, obj := range objects {
			if obj["type"] == "bash_execution_update" && obj["id"] == "shell-1" {
				return true
			}
		}
		return false
	})
	p.log.waitObjects(t, "second shell output", func(objects []map[string]any) bool {
		for _, obj := range objects {
			if obj["type"] == "bash_execution_update" && obj["id"] == "shell-2" {
				return true
			}
		}
		return false
	})
	p.send(map[string]any{"id": "abort-shells", "type": "abort_bash"})
	p.log.waitObjects(t, "abort response", rpcSucceeded("abort-shells", "abort_bash"))
	p.log.waitObjects(t, "first cancelled shell", func(objects []map[string]any) bool {
		for _, obj := range objects {
			if obj["type"] != "response" || obj["id"] != "shell-1" || obj["command"] != "bash" {
				continue
			}
			data, _ := obj["data"].(map[string]any)
			return data["cancelled"] == true
		}
		return false
	})
	p.log.waitObjects(t, "second cancelled shell", func(objects []map[string]any) bool {
		for _, obj := range objects {
			if obj["type"] != "response" || obj["id"] != "shell-2" || obj["command"] != "bash" {
				continue
			}
			data, _ := obj["data"].(map[string]any)
			return data["cancelled"] == true
		}
		return false
	})
	p.send(map[string]any{"id": "state-1", "type": "get_state"})
	p.log.waitObjects(t, "model still streaming", func(objects []map[string]any) bool {
		for _, obj := range objects {
			if obj["type"] != "response" || obj["id"] != "state-1" {
				continue
			}
			data, _ := obj["data"].(map[string]any)
			return data["isStreaming"] == true
		}
		return false
	})
	sessionPath := filepath.Join(sessionDir, id+".jsonl")
	if data, err := os.ReadFile(sessionPath); err == nil && strings.Contains(string(data), `"role":"bashExecution"`) {
		t.Fatal("cancelled shells appended before the model boundary")
	}
	p.closeStdin()
	p.waitExit()
	if f.requests.Load() != 1 {
		t.Fatalf("provider requests = %d, want 1", f.requests.Load())
	}
	store, err := session.OpenFile(sessionPath, f.work, false)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Unlock()
	messages := shellMessages(store)
	if len(messages) != 2 || !messages[0].Cancelled || !messages[1].Cancelled {
		t.Fatalf("deferred cancelled shells = %+v", messages)
	}
	excluded := false
	for _, message := range messages {
		if message.ExcludeFromContext != nil && *message.ExcludeFromContext {
			excluded = true
		}
	}
	if !excluded {
		t.Fatal("excluded RPC shell flag was lost")
	}
}
