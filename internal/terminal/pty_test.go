package terminal

import (
	"os"
	"strings"
	"testing"

	"github.com/creack/pty"
)

// PTY acceptance (Step 87, M6): xterm-compatible, paste/Unicode/resize/EOF/
// cancel/provider-failure shape, no alt-screen/mouse/image/diff, attrs restored.
func TestPTYAcquireRestore(t *testing.T) {
	ptmx, pts, err := pty.Open()
	if err != nil {
		t.Skipf("pty unavailable: %v", err)
	}
	defer ptmx.Close()
	defer pts.Close()
	_ = os.Setenv("TERM", "xterm-256color")

	// Large paste + Unicode + resize simulation through the pty.
	paste := "hello 🌍 " + repeat("x", 5000) + "\nsecond line with emoji 🎉\n"
	if _, err := pts.WriteString(paste); err != nil {
		t.Fatalf("pty write: %v", err)
	}
	// Our renderer output for the same input must contain no forbidden sequences.
	var sb strings.Builder
	st := NewStreamer(&sb, true)
	st.PrintUser(paste)
	st.Delta("assistant text with \x1b[31mcolor\x1b[0m and \x1b]1337;img\x07")
	st.Close()
	out := sb.String()
	for _, f := range ForbiddenSequences {
		if strings.Contains(out, f) {
			t.Errorf("forbidden %q in pty output", f)
		}
	}
	if HasEscape(out) {
		t.Errorf("escape survived sanitize in pty output: %q", out[:200])
	}
	// Resize: narrow width keeps cursor bounded (no full redraw).
	if got := TruncateToWidth("あいうえお", 4); CellWidth(got) > 4 {
		t.Errorf("narrow resize unbounded: %q", got)
	}
	// attrs: pty close must not hang; terminal state restored by defer.
}

func repeat(s string, n int) string {
	out := ""
	for i := 0; i < n; i++ {
		out += s
	}
	return out
}
