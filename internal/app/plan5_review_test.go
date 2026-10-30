package app

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"psi/internal/provider"
	"psi/internal/session"
	"psi/internal/terminal"
)

type failToolStartOutput struct {
	synchronizedBuffer
	store  *session.Store
	once   sync.Once
	failed chan struct{}
}

func (out *failToolStartOutput) Write(p []byte) (int, error) {
	if strings.Contains(terminal.Sanitize(string(p)), "◌") {
		out.once.Do(func() {
			out.store.FailWritesForTest(errors.New("injected tool-result append failure"))
			close(out.failed)
		})
	}
	return out.synchronizedBuffer.Write(p)
}

func TestReviewStorageFailureDrainsAndClearsActiveTool(t *testing.T) {
	r := testRuntime(t)
	r.authErr = nil
	r.cfg.Tools = nil // No dispatch, shell, or target filesystem access.
	var calls []provider.Block
	for i := 0; i < 100; i++ {
		calls = append(calls, provider.Block{Type: "toolCall", ID: fmt.Sprintf("call-%d", i), Name: "read", Arguments: map[string]any{"path": "target.go"}})
	}
	r.prov = &provider.Fake{Responses: []*provider.Response{{ToolCalls: calls, StopReason: "stop"}}}
	input, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	out := &failToolStartOutput{store: r.store, failed: make(chan struct{})}
	console := terminal.NewConsole(input, out)
	console.SizeFunc = func() (int, int) { return 80, 24 }
	console.Editor.Insert("question")
	defer func() { writer.Close(); console.Close(); input.Close() }()
	finished := make(chan error, 1)
	go func() { finished <- r.repl(console) }()
	if _, err := writer.Write([]byte("\r")); err != nil {
		t.Fatal(err)
	}
	select {
	case <-out.failed:
	case <-time.After(5 * time.Second):
		t.Fatal("tool-start fault did not run")
	}
	deadline := time.After(5 * time.Second)
	for !strings.Contains(out.String(), "storage failure, stopping") {
		select {
		case <-time.After(5 * time.Millisecond):
		case <-deadline:
			t.Fatalf("storage failure was not displayed: %q", out.String())
		}
	}
	for {
		if _, err := writer.Write([]byte("/quit\r")); err != nil {
			t.Fatal(err)
		}
		select {
		case err := <-finished:
			if err != nil {
				t.Fatal(err)
			}
			goto settled
		case <-time.After(20 * time.Millisecond):
		case <-deadline:
			t.Fatal("application did not settle after the storage fault")
		}
	}
settled:
	if len(r.activeTools) != 0 {
		t.Fatalf("drained tool start survived settlement: %+v", r.activeTools)
	}
	// Parallel batches preflight every call before settlement, so a storage
	// fault leaves every started call unsettled: exactly one unknown-outcome
	// row per call, with no duplicates and no missing calls.
	if count := strings.Count(terminal.Sanitize(out.String()), "outcome unknown"); count != 100 {
		t.Fatalf("storage fault needs one unknown-outcome row per started call, got %d", count)
	}
	if r.state != StateStorageBlocked {
		t.Fatalf("state after storage fault=%q", r.state)
	}
}

func TestReviewCLIBareHomeThemePath(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	actualHome, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	paths := resolveCLIThemePaths(filepath.Join(home, "workspace"), []string{"~", "~/themes"})
	if len(paths) != 2 || paths[0] != actualHome || paths[1] != filepath.Join(actualHome, "themes") {
		t.Fatalf("home theme paths=%q", paths)
	}
}
