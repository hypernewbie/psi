//go:build !windows

package app

import (
	"testing"
	"time"
)

// With a provisioned bridge the tool advertises only after Phi negotiates
// the capability.
func TestSyncboardAvailableFollowsNegotiation(t *testing.T) {
	r := testRuntime(t)
	if r.syncboardAvailable() {
		t.Fatal("tool reads available without a bridge")
	}
	startBridgeRuntime(t, r)
	deadline := time.Now().Add(5 * time.Second)
	for !r.syncboardAvailable() {
		if time.Now().After(deadline) {
			t.Fatal("tool was not advertised after negotiation")
		}
		time.Sleep(10 * time.Millisecond)
	}
}
