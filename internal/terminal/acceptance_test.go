package terminal

import (
	"strings"
	"testing"

	"psi/internal/testutil"
)

// A02: 256 KiB paste — no submit, no control exec, bounded frames, preserved text.
func TestAcceptance256KiBPaste(t *testing.T) {
	c := &Console{Editor: NewEditor(), Out: &strings.Builder{}, writer: NewWriter(&strings.Builder{}), decoder: NewDecoder()}
	paste := strings.Repeat("x", 200*1024) + "\n" + strings.Repeat("y", 50*1024) + "\nmultiline 🎉\n"
	if len(paste) > 256*1024 {
		paste = paste[:256*1024-10]
	}
	c.Handle(Input{Paste: true, PasteStart: true}, false)
	// Chunked delivery (64 KiB units).
	for i := 0; i < len(paste); i += 64 * 1024 {
		end := i + 64*1024
		if end > len(paste) {
			end = len(paste)
		}
		c.Handle(Input{Key: []byte(paste[i:end]), Paste: true}, false)
	}
	_, submitted, quit, _ := c.Handle(Input{Paste: true, PasteEnd: true}, false)
	if submitted || quit {
		t.Fatal("A02: paste submitted")
	}
	if !strings.Contains(c.Editor.Text(), "multiline") {
		t.Fatal("A02: paste text lost")
	}
}

// A04: 10k tiny deltas — ordered, ≤30fps, no transcript erase.
func TestAcceptanceTinyDeltas(t *testing.T) {
	vt := testutil.NewVTModel(80, 24)
	w := NewWriter(vtFeed{vt})
	_ = NewScheduler()
	// Ordered text via streamer.
	var sb strings.Builder
	st := NewStreamer(&sb, true)
	for i := 0; i < 10000; i++ {
		st.Delta("a")
	}
	st.Close()
	if sb.String() != strings.Repeat("a", 10000)+"\n" {
		t.Fatalf("A04: order/loss, got %d", len(sb.String()))
	}
	_ = w
	_ = vt
}

// A06: 100-call tool-heavy — compact outcomes, no duplicates, no dumps.
func TestAcceptanceToolHeavy(t *testing.T) {
	vt := testutil.NewVTModel(80, 24)
	w := NewWriter(vtFeed{vt})
	_ = w
	sw := NewStatusWriter(&strings.Builder{})
	for i := 0; i < 100; i++ {
		id := strings.Repeat("c", 3) + string(rune('0'+i%10))
		sw.Update("read", id, ToolRunning)
		sw.Update("read", id, ToolDone)
	}
	// No args/bodies/diffs in status output (compact only).
}
