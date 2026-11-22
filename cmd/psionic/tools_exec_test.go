package main

import (
	"context"
	"os"
	stdruntime "runtime"
	"strings"
	"testing"
	"time"

	"github.com/hypernewbie/psi/internal/tools"
)

// execTestTrue echoes; execTestSleep blocks long enough to hit a small
// timeout. Both use each platform's shell syntax.
func execTestTrue() string {
	if stdruntime.GOOS == "windows" {
		return "echo psionic-ok"
	}
	return "printf psionic-ok"
}

func execTestSleep() string {
	if stdruntime.GOOS == "windows" {
		return "ping -n 30 127.0.0.1 > NUL"
	}
	return "sleep 30"
}

func TestRunExecReturnsBoundedOutput(t *testing.T) {
	workspace := t.TempDir()
	result := runExec(context.Background(), workspace, execTestTrue(), resolveExecLimits(nil))
	if result.err != nil {
		t.Fatalf("exec failed: %v", result.err)
	}
	if !strings.Contains(result.output, "psionic-ok") {
		t.Fatalf("output = %q", result.output)
	}
	if !strings.Contains(result.replyText(), "exit 0") {
		t.Fatalf("reply = %q", result.replyText())
	}
}

func TestRunExecTimesOutAndKillsTheCommand(t *testing.T) {
	workspace := t.TempDir()
	limits := resolveExecLimits(&execSettings{TimeoutMs: ptr(int64(1500))})
	start := time.Now()
	result := runExec(context.Background(), workspace, execTestSleep(), limits)
	elapsed := time.Since(start)
	if result.err == nil || !strings.Contains(result.status, "timed out") {
		t.Fatalf("expected timeout, got %+v", result)
	}
	// The bounded timeout plus the kill must return well before the command's
	// own runtime; generous margin keeps this deterministic under host load.
	if elapsed > 15*time.Second {
		t.Fatalf("timeout kill took %s", elapsed)
	}
}

func TestRunExecBoundsOversizedOutput(t *testing.T) {
	workspace := t.TempDir()
	limits := resolveExecLimits(&execSettings{TimeoutMs: ptr(int64(1000))})
	command := "yes psionic-output-head psionic-output-tail"
	if stdruntime.GOOS == "windows" {
		command = "for /L %i in (1,1,100000) do @echo psionic-output-head psionic-output-tail"
	}
	result := runExec(context.Background(), workspace, command, limits)
	if result.err == nil {
		// `yes` never exits on its own; a zero exit means the command did not
		// run as expected on this platform, so stop before asserting bounds.
		t.Skipf("command exited on its own: %s", result.status)
	}
	if !strings.Contains(result.output, "output truncated") {
		t.Fatalf("truncation marker missing: %q", result.output[:min(200, len(result.output))])
	}
	if len(result.output) > limits.maxOutput+64 {
		t.Fatalf("output not bounded: %d bytes", len(result.output))
	}
}

func TestRunExecReportsNonzeroExit(t *testing.T) {
	workspace := t.TempDir()
	command := "exit 3"
	if stdruntime.GOOS == "windows" {
		command = "cmd /c exit 3"
	}
	result := runExec(context.Background(), workspace, command, resolveExecLimits(nil))
	if result.err != nil {
		t.Fatalf("nonzero exit surfaced as tool error: %v", result.err)
	}
	if !strings.Contains(result.status, "nonzero exit") {
		t.Fatalf("status = %q", result.status)
	}
}

func TestExecToolRegistrationGatesOnEnabledAndRoute(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(dir+"/MEMORY.md", []byte("# memory\nfact one\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	enabled := &execSettings{Enabled: ptr(true)}
	turn := &activeTurn{channelID: "423456789012345678"}

	owner, err := buildTools(dir, true, false, toolPolicy{AlsoAllow: []string{"exec"}}, nil, turn, nil, enabled)
	if err != nil {
		t.Fatal(err)
	}
	if owner.handlers["exec"] == nil {
		t.Fatal("owner DM did not register exec")
	}

	// A shared room never gets it, even with an explicit allow entry.
	shared, err := buildTools(dir, false, false, toolPolicy{AlsoAllow: []string{"exec"}}, nil, turn, nil, enabled)
	if err != nil {
		t.Fatal(err)
	}
	if shared.handlers["exec"] != nil {
		t.Fatal("shared room registered exec")
	}

	// Disabled configuration registers nothing on any route.
	off, err := buildTools(dir, true, false, toolPolicy{AlsoAllow: []string{"exec"}}, nil, turn, nil, &execSettings{})
	if err != nil {
		t.Fatal(err)
	}
	if off.handlers["exec"] != nil {
		t.Fatal("disabled exec was registered")
	}

	// Deny entries remove it even on the owner DM.
	denied, err := buildTools(dir, true, false, toolPolicy{AlsoAllow: []string{"exec"}, Deny: []string{"exec"}}, nil, turn, nil, enabled)
	if err != nil {
		t.Fatal(err)
	}
	if denied.handlers["exec"] != nil {
		t.Fatal("denied exec was registered")
	}
}

func TestExecToolHandlerRunsACommand(t *testing.T) {
	dir := t.TempDir()
	set, err := buildTools(dir, true, false, toolPolicy{AlsoAllow: []string{"exec"}}, nil, &activeTurn{}, nil, &execSettings{Enabled: ptr(true)})
	if err != nil {
		t.Fatal(err)
	}
	handler := set.handlers["exec"]
	if handler == nil {
		t.Fatal("exec handler missing")
	}
	result := handler(context.Background(), tools.Invocation{Arguments: map[string]any{"command": execTestTrue()}})
	var text string
	for _, block := range result.Content {
		text += block.Text
	}
	if !strings.Contains(text, "psionic-ok") {
		t.Fatalf("result text = %q", text)
	}
}

func TestExecConfigForIsOwnerDMOnly(t *testing.T) {
	settings := mustSettings(t, `{ channels: { discord: {} }, tools: { exec: { enabled: true } } }`)
	if execConfigFor(settings, false) != nil {
		t.Fatal("shared route received exec configuration")
	}
	if execConfigFor(settings, true) == nil {
		t.Fatal("owner DM lost exec configuration")
	}
}

func TestValidateExecSettingsBounds(t *testing.T) {
	if err := validateExecSettings(&execSettings{TimeoutMs: ptr(int64(50))}); err == nil {
		t.Fatal("tiny timeout accepted")
	}
	if err := validateExecSettings(&execSettings{PassEnv: []string{"lowercase"}}); err == nil {
		t.Fatal("lowercase passEnv accepted")
	}
	if err := validateExecSettings(&execSettings{MaxOutputBytes: ptr(int64(1 << 20))}); err == nil {
		t.Fatal("oversized output cap accepted")
	}
	if err := validateExecSettings(&execSettings{TimeoutMs: ptr(int64(2000)), PassEnv: []string{"HOME"}}); err != nil {
		t.Fatalf("valid settings rejected: %v", err)
	}
}

func ptr[T any](v T) *T { return &v }
