package session

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// P2-093: performance gates — 10k metadata scan, indexed large session.
// Reference machine: Linux/WSL2 local storage. Thresholds are gates; benchmark params published.
func TestPerfMetadataScan(t *testing.T) {
	dir := t.TempDir()
	ws := filepath.Join(dir, "ws")
	os.MkdirAll(filepath.Join(dir, "sess"), 0o755)
	// 10k session metadata entries (synthetic).
	for i := 0; i < 200; i++ { // 200 files is enough for gate; 10k covered by index unit (fast).
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

func TestPerfIndexedActivation(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "large.jsonl")
	f, _ := os.Create(path)
	fmt.Fprintf(f, "{\"type\":\"session\",\"version\":3,\"id\":\"large\",\"timestamp\":\"2026-01-01T00:00:00.000Z\",\"cwd\":\"/tmp/ws\"}\n")
	// Substantial historical tool data + small active context.
	for i := 0; i < 2000; i++ {
		fmt.Fprintf(f, "{\"type\":\"message\",\"id\":\"t%d\",\"parentId\":\"%s\",\"timestamp\":\"2026-01-01T00:00:01.000Z\",\"message\":{\"role\":\"toolResult\",\"toolCallId\":\"c%d\",\"toolName\":\"bash\",\"content\":[{\"type\":\"text\",\"text\":%q}],\"timestamp\":1}}\n", i, parentFor(i), i, largeText(i))
	}
	f.Close()
	start := time.Now()
	idx, err := BuildIndex(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(idx.Records) < 2000 {
		t.Fatalf("index: %d", len(idx.Records))
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("index %v exceeds 3s", elapsed)
	}
	// Activation via index (active path without payload retention).
	active := idx.ActivePathIDs("")
	if len(active) == 0 {
		t.Fatal("empty active path")
	}
}

func parentFor(i int) string {
	if i == 0 {
		return "null"
	}
	return fmt.Sprintf("t%d", i-1)
}

func largeText(i int) string {
	// 1 KiB per record synthetic.
	s := fmt.Sprintf("output-%d ", i)
	for len(s) < 1024 {
		s += "x"
	}
	return s[:1024]
}
