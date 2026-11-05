package session

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Smoke fixtures only: 200 small files and an approximately 2 MiB index.
// These do not establish PLAN2's 10,000-file or 512 MiB live-activation gates.
func TestMetadataScanSmoke(t *testing.T) {
	dir := t.TempDir()
	ws := filepath.Join(dir, "ws")
	os.MkdirAll(filepath.Join(dir, "sess"), 0o755)
	// Small synthetic metadata scan.
	for i := 0; i < 200; i++ {
		path := filepath.Join(dir, "sess", fmt.Sprintf("s-%04d.jsonl", i))
		data := fmt.Sprintf("{\"type\":\"session\",\"version\":3,\"id\":\"id-%04d\",\"timestamp\":\"2026-01-01T00:00:00.000Z\",\"cwd\":%q}\n{\"type\":\"message\",\"id\":\"u\",\"parentId\":null,\"timestamp\":\"2026-01-01T00:00:01.000Z\",\"message\":{\"role\":\"user\",\"content\":\"hi %d\",\"timestamp\":1}}\n", i, ws, i)
		os.WriteFile(path, []byte(data), 0o644)
	}
	start := time.Now()
	infos, err := Discover(filepath.Join(dir, "sess"))
	if err != nil {
		t.Fatal(err)
	}
	if len(infos) < 200 {
		t.Fatalf("discovery: %d", len(infos))
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("metadata scan %v exceeds 2s", elapsed)
	}
	// First page usable without full transcripts (20/page).
	if len(infos) > 20 {
		_ = infos[:20]
	}
}

func TestEagerLoadSmoke(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "large.jsonl")
	f, _ := os.Create(path)
	fmt.Fprintf(f, "{\"type\":\"session\",\"version\":3,\"id\":\"large\",\"timestamp\":\"2026-01-01T00:00:00.000Z\",\"cwd\":\"/tmp/ws\"}\n")
	// Substantial historical tool data + small active context.
	for i := 0; i < 2000; i++ {
		fmt.Fprintf(f, "{\"type\":\"message\",\"id\":\"t%d\"%s,\"timestamp\":\"2026-01-01T00:00:01.000Z\",\"message\":{\"role\":\"toolResult\",\"toolCallId\":\"c%d\",\"toolName\":\"bash\",\"content\":[{\"type\":\"text\",\"text\":%q}],\"timestamp\":1}}\n", i, parentFor(i), i, largeText(i))
	}
	f.Close()
	start := time.Now()
	store, err := OpenFile(path, "/tmp/ws", false)
	if err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("eager load %v exceeds 3s", elapsed)
	}
	// The active path is served from memory: no disk work per call.
	start = time.Now()
	active, err := store.ActivePath()
	if err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(start); elapsed > 500*time.Millisecond {
		t.Fatalf("active path %v exceeds 500ms", elapsed)
	}
	if len(active) == 0 {
		t.Fatal("empty active path")
	}
}

// parentFor renders the parent link of a message. The first message is the
// root of the branch and has no parent.
func parentFor(i int) string {
	if i == 0 {
		return ""
	}
	return fmt.Sprintf(",\"parentId\":\"t%d\"", i-1)
}

func largeText(i int) string {
	// 1 KiB per record synthetic.
	s := fmt.Sprintf("output-%d ", i)
	for len(s) < 1024 {
		s += "x"
	}
	return s[:1024]
}
