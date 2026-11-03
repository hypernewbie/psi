//go:build windows

package pibridge

import (
	"os"
	"testing"
)

func TestFromEnvRefusesNamedPipeTargets(t *testing.T) {
	t.Setenv(envEndpoint, `unix:\\\\.\\pipe\\phi-pi`)
	t.Setenv(envPaneID, "pane")
	t.Setenv(envProcessID, "proc")
	t.Setenv(envToken, "token")
	if c := FromEnv(Snapshot{Generation: 1, SessionID: "s"}); c != nil {
		t.Fatal("windows produced a client without a pipe dialer")
	}
	for _, key := range []string{envEndpoint, envPaneID, envProcessID, envToken, envAttachmentRoot} {
		if got := os.Getenv(key); got != "" {
			t.Errorf("%s not cleared: %q", key, got)
		}
	}
}
