package terminal

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
)

func TestAcceptance256KiBPaste(t *testing.T) {
	c, _, _ := reviewConsole()
	tail := "\nmultiline 🎉\n"
	paste := strings.Repeat("x", pasteBudget-len(tail)) + tail
	c.Handle(Input{Paste: true, PasteStart: true}, false)
	for i := 0; i < len(paste); i += 64 * 1024 {
		c.Handle(Input{Key: []byte(paste[i:min(len(paste), i+64*1024)]), Paste: true}, false)
	}
	_, submitted, quit, _ := c.Handle(Input{Paste: true, PasteEnd: true}, false)
	if submitted || quit || c.Editor.Text() != paste {
		t.Fatal("paste submitted, truncated, or changed")
	}
	writes, _, _, _ := c.writer.Stats()
	if writes != 1 {
		t.Fatalf("paste caused %d redraw writes", writes)
	}
}

func TestAcceptanceTinyDeltas(t *testing.T) {
	out := &countWriter{}
	st := NewStreamer(out, true)
	for i := 0; i < 10000; i++ {
		st.Delta("a")
	}
	st.Close()
	if out.data.String() != strings.Repeat("a", 10000)+"\n" {
		t.Fatal("stream text lost or duplicated")
	}
	if out.writes > 2 {
		t.Fatalf("uncoalesced stream: %d writes", out.writes)
	}
}

func TestAcceptanceToolHeavy(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	var out bytes.Buffer
	s := NewStatusWriter(&out)
	for i := 0; i < 100; i++ {
		id := fmt.Sprintf("call-%d", i)
		s.Update("read", id, ToolRunning)
		s.Update("read", id, ToolDone)
		s.Update("read", id, ToolDone)
	}
	if out.String() != strings.Repeat(MessageIndent+"read: done\n", 100) {
		t.Fatal("tool status duplicated or details leaked")
	}
}
