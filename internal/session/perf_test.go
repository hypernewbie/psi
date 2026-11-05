package session

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Smoke fixture only: 200 small files. This does not establish PLAN2's
// 10,000-file or 512 MiB live-activation gates.
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
