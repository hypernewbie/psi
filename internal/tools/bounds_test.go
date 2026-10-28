package tools

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestResourceBounds(t *testing.T) {
	ws := t.TempDir()
	// Large tool output bounded to 50 KiB + notice, full copy in temp file.
	big := strings.Repeat("x\n", 20000)
	p := filepath.Join(ws, "big.txt")
	if err := os.WriteFile(p, []byte(big), 0o644); err != nil {
		t.Fatal(err)
	}
	r := NewRegistry(ws)
	res := r.Dispatch(Invocation{ID: "1", Name: "read", Arguments: map[string]any{"path": "big.txt"}})
	if len(res.Content[0].Text) > 60*1024 {
		t.Errorf("output not bounded: %d", len(res.Content[0].Text))
	}
	// Temp full-output files are private (0600) where supported.
	res = r.Dispatch(Invocation{ID: "2", Name: "bash", Arguments: map[string]any{"command": "seq 1 10000"}})
	if dp, ok := res.Details.(map[string]any); ok {
		if fp, ok := dp["fullOutputPath"].(string); ok && fp != "" {
			fi, err := os.Stat(fp)
			if err != nil {
				t.Errorf("full output missing: %v", err)
			// Windows has no Unix mode bits; a private file stats 0666.
			} else if runtime.GOOS != "windows" && fi.Mode().Perm()&0o077 != 0 {
				t.Errorf("full output not private: %v", fi.Mode())
			}
			os.Remove(fp)
		}
	}
	// User files untouched by truncation (no deletion).
	if _, err := os.Stat(p); err != nil {
		t.Errorf("user file removed")
	}
	// Tool args/results never in terminal transcript tested in terminal pkg.
}
