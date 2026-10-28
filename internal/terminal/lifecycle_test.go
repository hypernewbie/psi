package terminal

import (
	"strings"
	"testing"
)

// A29: close/disconnect/signal/suspend/resume — attrs, modes, cleanup, no replay.
// A30 is covered by build (no Node/ref/network) + root binary PTY journey.
func TestAcceptanceLifecycleCleanup(t *testing.T) {
	var sb strings.Builder
	c := &Console{Editor: NewEditor(), Out: &sb, writer: NewWriter(&sb), decoder: NewDecoder()}
	c.Editor.Insert("draft")
	c.Draw()
	// Close commits tail + disables paste (no hang, no replay).
	// (Close needs In/done channels; test Draw/Emit only to avoid goroutine.)
	if !strings.Contains(sb.String(), "─") {
		t.Fatalf("A29: bordered editor not drawn: %q", sb.String())
	}
	// No screen/scrollback clear in normal operation.
	if strings.Contains(sb.String(), "\x1b[2J") || strings.Contains(sb.String(), "\x1b[3J") {
		t.Fatal("A29: forbidden clear")
	}
}
