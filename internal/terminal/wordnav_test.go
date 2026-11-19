package terminal

import (
	"strings"
	"testing"
	"time"
)

func TestWordBackwardSymbolsKeepPrecedingWord(t *testing.T) {
	for text, prefix := range map[string]string{"a🙂": "a", "a👩‍🚀": "a", "word™": "word", "a🛰️": "a"} {
		e := NewEditor()
		e.Insert(text)
		e.DeleteWordBackward()
		if e.Text() != prefix {
			t.Errorf("Ctrl+W on %q: got %q want %q", text, e.Text(), prefix)
		}
	}
}

func TestF09LongWordScaling(t *testing.T) {
	for _, size := range []int{1024, 4096, 65536, 256 * 1024} {
		text := strings.Repeat("x", size)
		start := time.Now()
		got := wordBackward(text, len(text))
		elapsed := time.Since(start)
		if got != 0 {
			t.Fatalf("size %d: got %d", size, got)
		}
		t.Logf("long word %d bytes: %v", size, elapsed)
		// Deadlock guard only. Latency acceptance needs actual-app samples.
		if elapsed > 2*time.Second {
			t.Fatalf("long-word navigation stalled: %v", elapsed)
		}
	}
	// Documented dictionary limitation, separate from the supported corpus.
	if got := wordBackward("世界你好", len("世界你好")); got != 0 {
		t.Fatalf("CJK run behavior changed: got %d want 0", got)
	}
	e := NewEditor()
	e.Insert(strings.Repeat("q", 4096) + " tail")
	e.DeleteWordBackward()
	if e.Text() != strings.Repeat("q", 4096)+" " {
		t.Fatal("editor Ctrl+W deleted the wrong prefix")
	}
}
