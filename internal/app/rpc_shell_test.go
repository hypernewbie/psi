package app

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hypernewbie/psi/internal/provider"
	"github.com/hypernewbie/psi/internal/session"
	"github.com/hypernewbie/psi/internal/tools"
)

func rpcBashCommand(id, command string, exclude *bool) rpcCommand {
	raw, _ := json.Marshal(command)
	cmd := rpcCommand{Type: "bash", Command: raw}
	if id != "" {
		cmd.ID = &id
	}
	if exclude != nil {
		encoded, _ := json.Marshal(*exclude)
		cmd.ExcludeFromContext = encoded
	}
	return cmd
}

func TestRPCBashLifecycle(t *testing.T) {
	t.Run("streams updates and appends once", func(t *testing.T) {
		s, store, log := newRPCTestRunner(t, nil, nil)
		s.shellRun = func(_ context.Context, _ *tools.BashTool, _ string, onOutput func([]byte)) tools.BashRunResult {
			onOutput([]byte("hello\n"))
			code := 0
			return tools.BashRunResult{Output: "hello\n", ExitCode: &code}
		}
		exclude := false
		if resp := s.handle(rpcBashCommand("shell-1", "printf hello", &exclude)); resp != nil {
			t.Fatalf("bash admission returned %+v", resp)
		}
		objects := log.waitFor(t, "bash response", func(obj map[string]any) bool {
			return obj["type"] == "response" && obj["command"] == "bash"
		})
		var update, response map[string]any
		for _, obj := range objects {
			if obj["type"] == "bash_execution_update" {
				update = obj
			}
			if obj["type"] == "response" && obj["command"] == "bash" {
				response = obj
			}
		}
		if update == nil || update["delta"] != "hello\n" || update["id"] != "shell-1" {
			t.Fatalf("bash update = %+v", update)
		}
		data, _ := response["data"].(map[string]any)
		if response["success"] != true || data["output"] != "hello\n" || data["exitCode"] != float64(0) || data["cancelled"] != false || data["truncated"] != false {
			t.Fatalf("bash response = %+v", response)
		}
		messages := shellMessages(store)
		if len(messages) != 1 || messages[0].Command != "printf hello" || messages[0].Output != "hello\n" || messages[0].ExcludeFromContext == nil || *messages[0].ExcludeFromContext {
			t.Fatalf("saved shell messages = %+v", messages)
		}
	})

	t.Run("malformed fields never launch", func(t *testing.T) {
		s, store, _ := newRPCTestRunner(t, nil, nil)
		var starts int
		s.shellRun = func(context.Context, *tools.BashTool, string, func([]byte)) tools.BashRunResult {
			starts++
			return tools.BashRunResult{}
		}
		cases := []rpcCommand{
			{Type: "bash"},
			{Type: "bash", Command: json.RawMessage("null")},
			{Type: "bash", Command: json.RawMessage("42")},
			{Type: "bash", Command: json.RawMessage(`"ok"`), ExcludeFromContext: json.RawMessage("null")},
			{Type: "bash", Command: json.RawMessage(`"ok"`), ExcludeFromContext: json.RawMessage(`"yes"`)},
		}
		for _, cmd := range cases {
			if resp := s.handle(cmd); resp == nil || resp.Success {
				t.Fatalf("malformed bash admission = %+v", resp)
			}
		}
		if starts != 0 || len(shellMessages(store)) != 0 {
			t.Fatalf("malformed fields launched=%d entries=%d", starts, len(shellMessages(store)))
		}
	})

	t.Run("empty command and empty ID are valid", func(t *testing.T) {
		s, _, log := newRPCTestRunner(t, nil, nil)
		s.shellRun = func(_ context.Context, _ *tools.BashTool, _ string, _ func([]byte)) tools.BashRunResult {
			code := 0
			return tools.BashRunResult{ExitCode: &code}
		}
		id := ""
		cmd := rpcBashCommand("", "", nil)
		cmd.ID = &id
		if resp := s.handle(cmd); resp != nil {
			t.Fatalf("admission returned %+v", resp)
		}
		objects := log.waitFor(t, "bash response", func(obj map[string]any) bool {
			return obj["type"] == "response" && obj["command"] == "bash"
		})
		last := objects[len(objects)-1]
		if last["id"] != "" {
			t.Fatalf("empty id response = %+v", last)
		}
		data, _ := last["data"].(map[string]any)
		if data["output"] != "" || data["cancelled"] != false {
			t.Fatalf("empty command result = %+v", last)
		}
	})

	t.Run("abort cancels before the final response and appends once", func(t *testing.T) {
		s, store, log := newRPCTestRunner(t, nil, nil)
		started := make(chan struct{}, 1)
		s.shellRun = func(ctx context.Context, _ *tools.BashTool, _ string, onOutput func([]byte)) tools.BashRunResult {
			onOutput([]byte("PARTIAL"))
			started <- struct{}{}
			<-ctx.Done()
			return tools.BashRunResult{Output: "PARTIAL", Cancelled: true}
		}
		id := "abort-1"
		if resp := s.handle(rpcBashCommand(id, "sleep 30", nil)); resp != nil {
			t.Fatalf("admission returned %+v", resp)
		}
		select {
		case <-started:
		case <-time.After(8 * time.Second):
			t.Fatal("shell did not start")
		}
		if resp := s.handle(rpcCommand{Type: "abort_bash", ID: &id}); resp == nil || !resp.Success {
			t.Fatalf("abort_bash response = %+v", resp)
		}
		objects := log.waitFor(t, "cancelled bash response", func(obj map[string]any) bool {
			if obj["type"] != "response" || obj["command"] != "bash" {
				return false
			}
			data, _ := obj["data"].(map[string]any)
			return data["cancelled"] == true
		})
		if len(objects) == 0 {
			t.Fatal("no cancelled response")
		}
		messages := shellMessages(store)
		if len(messages) != 1 || !messages[0].Cancelled || messages[0].ExitCode != nil || messages[0].Output != "PARTIAL" {
			t.Fatalf("cancelled shell messages = %+v", messages)
		}
	})

	t.Run("storage failure returns an error and no success frame", func(t *testing.T) {
		s, store, log := newRPCTestRunner(t, nil, nil)
		gate := make(chan struct{})
		s.shellRun = func(_ context.Context, _ *tools.BashTool, _ string, onOutput func([]byte)) tools.BashRunResult {
			<-gate
			onOutput([]byte("blocked\n"))
			code := 0
			return tools.BashRunResult{Output: "blocked\n", ExitCode: &code}
		}
		if resp := s.handle(rpcBashCommand("storage-1", "printf blocked", nil)); resp != nil {
			t.Fatalf("admission returned %+v", resp)
		}
		store.FailWritesForTest(errors.New("blocked shell append"))
		close(gate)
		objects := log.waitFor(t, "bash error", func(obj map[string]any) bool {
			return obj["type"] == "response" && obj["command"] == "bash" && obj["success"] == false
		})
		for _, obj := range objects {
			if obj["type"] == "response" && obj["command"] == "bash" && obj["success"] == true {
				t.Fatalf("storage failure also sent success: %+v", obj)
			}
		}
		if len(shellMessages(store)) != 0 || store.WriteError() == nil {
			t.Fatal("failed shell append left a durable entry")
		}
	})

	t.Run("infrastructure failure saves one diagnostic entry", func(t *testing.T) {
		s, store, log := newRPCTestRunner(t, nil, nil)
		s.shellRun = func(_ context.Context, _ *tools.BashTool, _ string, _ func([]byte)) tools.BashRunResult {
			return tools.BashRunResult{InfraErr: errors.New("spawn failed")}
		}
		if resp := s.handle(rpcBashCommand("infra-1", "missing", nil)); resp != nil {
			t.Fatalf("admission returned %+v", resp)
		}
		log.waitFor(t, "bash error", func(obj map[string]any) bool {
			return obj["type"] == "response" && obj["command"] == "bash" && obj["success"] == false
		})
		messages := shellMessages(store)
		if len(messages) != 1 || !strings.Contains(messages[0].Output, "spawn failed") || messages[0].ExitCode != nil {
			t.Fatalf("infrastructure shell messages = %+v", messages)
		}
	})

	t.Run("truncated result advertises only a retained capture", func(t *testing.T) {
		s, _, log := newRPCTestRunner(t, nil, nil)
		full := filepath.Join(t.TempDir(), "full.log")
		if err := os.WriteFile(full, []byte("full"), 0o600); err != nil {
			t.Fatal(err)
		}
		s.shellRun = func(_ context.Context, _ *tools.BashTool, _ string, onOutput func([]byte)) tools.BashRunResult {
			onOutput([]byte("tail\n"))
			code := 0
			return tools.BashRunResult{Output: "tail\n", ExitCode: &code, Truncated: true, FullOutputPath: full}
		}
		if resp := s.handle(rpcBashCommand("trunc-1", "yes", nil)); resp != nil {
			t.Fatalf("admission returned %+v", resp)
		}
		objects := log.waitFor(t, "truncated response", func(obj map[string]any) bool {
			return obj["type"] == "response" && obj["command"] == "bash"
		})
		last := objects[len(objects)-1]
		data, _ := last["data"].(map[string]any)
		if data["truncated"] != true || data["fullOutputPath"] != full {
			t.Fatalf("truncated data = %+v", data)
		}
	})

	t.Run("mutations refuse while shell owns the runner", func(t *testing.T) {
		s, _, _ := newRPCTestRunner(t, nil, nil)
		gate := make(chan struct{})
		s.shellRun = func(ctx context.Context, _ *tools.BashTool, _ string, _ func([]byte)) tools.BashRunResult {
			<-gate
			<-ctx.Done()
			return tools.BashRunResult{Cancelled: true}
		}
		if resp := s.handle(rpcBashCommand("serial-1", "sleep 30", nil)); resp != nil {
			t.Fatalf("admission returned %+v", resp)
		}
		for _, cmd := range []rpcCommand{
			{Type: "compact"},
			{Type: "set_model", Provider: "anthropic", ModelID: "rpc-test-model"},
			{Type: "set_thinking_level", Level: "low"},
		} {
			resp := s.handle(cmd)
			if resp == nil || resp.Success {
				t.Fatalf("%s was admitted during shell work: %+v", cmd.Type, resp)
			}
		}
		if resp := s.handle(rpcCommand{Type: "get_state"}); resp == nil || !resp.Success {
			t.Fatalf("get_state was refused: %+v", resp)
		}
		s.cancelShells()
		close(gate)
		waitRPCShellDone(t, s)
	})
}

func waitRPCShellDone(t *testing.T, s *rpcRunner) {
	t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		if !s.shellActive() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("shell worker did not settle")
}

func TestRPCBashOverlap(t *testing.T) {
	t.Run("two shells finish during a model run and append at the boundary", func(t *testing.T) {
		gateProvider := &rpcGateProvider{started: make(chan struct{}), release: make(chan struct{})}
		s, store, log := newRPCTestRunner(t, gateProvider, nil)
		releaseFirst, releaseSecond := make(chan struct{}), make(chan struct{})
		started := make(chan string, 2)
		s.shellRun = func(_ context.Context, _ *tools.BashTool, command string, onOutput func([]byte)) tools.BashRunResult {
			started <- command
			if command == "first" {
				<-releaseFirst
				onOutput([]byte("FIRST_DONE\n"))
				code := 0
				return tools.BashRunResult{Output: "FIRST_DONE\n", ExitCode: &code}
			}
			<-releaseSecond
			onOutput([]byte("SECOND_DONE\n"))
			code := 0
			return tools.BashRunResult{Output: "SECOND_DONE\n", ExitCode: &code}
		}
		if resp := s.handle(rpcCommand{Type: "prompt", Message: "hold the model"}); resp == nil || !resp.Success {
			t.Fatalf("prompt admission = %+v", resp)
		}
		waitShellSignal(t, gateProvider.started, "provider start")
		if resp := s.handle(rpcBashCommand("first-shell", "first", nil)); resp != nil {
			t.Fatalf("first bash admission = %+v", resp)
		}
		if resp := s.handle(rpcBashCommand("second-shell", "second", nil)); resp != nil {
			t.Fatalf("second bash admission = %+v", resp)
		}
		waitShellStart(t, started, "first shell start")
		waitShellStart(t, started, "second shell start")
		close(releaseFirst)
		log.waitFor(t, "first bash response", func(obj map[string]any) bool {
			return obj["type"] == "response" && obj["command"] == "bash" && obj["id"] == "first-shell" && obj["success"] == true
		})
		if len(shellMessages(store)) != 0 {
			t.Fatal("first shell appended inside the model run")
		}
		close(releaseSecond)
		log.waitFor(t, "second bash response", func(obj map[string]any) bool {
			return obj["type"] == "response" && obj["command"] == "bash" && obj["id"] == "second-shell" && obj["success"] == true
		})
		if len(shellMessages(store)) != 0 {
			t.Fatal("second shell appended inside the model run")
		}
		close(gateProvider.release)
		waitRPCMessages(t, store, 2)
		if len(shellMessages(store)) != 2 {
			t.Fatal("shell entries did not append at the model boundary")
		}
		messages := shellMessages(store)
		if messages[0].Command != "first" || messages[1].Command != "second" {
			t.Fatalf("shell append order = %q, %q", messages[0].Command, messages[1].Command)
		}
	})

	t.Run("a deferred append failure reports an extension error after success", func(t *testing.T) {
		gateProvider := &rpcGateProvider{started: make(chan struct{}), release: make(chan struct{})}
		s, store, log := newRPCTestRunner(t, gateProvider, nil)
		gate := make(chan struct{})
		s.shellRun = func(_ context.Context, _ *tools.BashTool, _ string, onOutput func([]byte)) tools.BashRunResult {
			<-gate
			onOutput([]byte("LOST\n"))
			code := 0
			return tools.BashRunResult{Output: "LOST\n", ExitCode: &code}
		}
		s.handle(rpcCommand{Type: "prompt", Message: "hold"})
		waitShellSignal(t, gateProvider.started, "provider start")
		s.handle(rpcBashCommand("lost-shell", "lost", nil))
		close(gate)
		log.waitFor(t, "execution response", func(obj map[string]any) bool {
			return obj["type"] == "response" && obj["command"] == "bash" && obj["id"] == "lost-shell" && obj["success"] == true
		})
		store.FailWritesForTest(errors.New("deferred append blocked"))
		close(gateProvider.release)
		log.waitFor(t, "deferred extension error", func(obj map[string]any) bool {
			return obj["type"] == "extension_error" && obj["id"] == "lost-shell"
		})
		if len(shellMessages(store)) != 0 {
			t.Fatal("failed deferred append left a shell entry")
		}
	})
}

func waitShellStart(t *testing.T, started <-chan string, label string) {
	t.Helper()
	select {
	case <-started:
	case <-time.After(8 * time.Second):
		t.Fatalf("timeout waiting for %s", label)
	}
}

type rpcGateProvider struct {
	started chan struct{}
	release chan struct{}
}

func (p *rpcGateProvider) Send(ctx context.Context, _ provider.Request, _ func(provider.Delta)) (*provider.Response, error) {
	select {
	case <-p.started:
	default:
		close(p.started)
	}
	select {
	case <-p.release:
		return &provider.Response{Text: "MODEL_DONE", StopReason: "end_turn", Usage: provider.Usage{Input: 1, Output: 1}}, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (p *rpcGateProvider) CloseIdle() {}

func waitRPCMessages(t *testing.T, store *session.Store, want int) {
	t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		if len(shellMessages(store)) >= want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %d shell messages", want)
}
