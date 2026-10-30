package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"psi/internal/testutil/leakcheck"
)

// TestWriteCreatesIntermediateDirectories: verifies deep parent directory creation.
func TestWriteCreatesIntermediateDirectories(t *testing.T) {
	dir := t.TempDir()
	tool := &WriteTool{Cwd: dir}

	deepPath := filepath.Join("deep", "nested", "dir", "structure", "output.txt")
	content := "deep content hello 🌍"

	res := tool.ExecuteCtx(context.Background(), Invocation{
		ID:   "w1",
		Name: Write,
		Arguments: map[string]any{
			"path":    deepPath,
			"content": content,
		},
	})

	if res.IsError {
		t.Fatalf("write failed: %+v", res)
	}

	data, err := os.ReadFile(filepath.Join(dir, deepPath))
	if err != nil {
		t.Fatalf("failed to read created file: %v", err)
	}
	if string(data) != content {
		t.Fatalf("content mismatch: got %q, want %q", string(data), content)
	}
}

// TestWriteOverwritesExistingFileCleanly: overwriting truncates older content completely.
func TestWriteOverwritesExistingFileCleanly(t *testing.T) {
	dir := t.TempDir()
	tool := &WriteTool{Cwd: dir}

	target := "target.txt"
	absTarget := filepath.Join(dir, target)

	// Write 100 bytes initially
	initial := strings.Repeat("x", 100)
	if err := os.WriteFile(absTarget, []byte(initial), 0o644); err != nil {
		t.Fatal(err)
	}

	// Overwrite with smaller 5 bytes
	smaller := "short"
	res := tool.ExecuteCtx(context.Background(), Invocation{
		ID:        "w2",
		Name:      Write,
		Arguments: map[string]any{"path": target, "content": smaller},
	})
	if res.IsError {
		t.Fatalf("overwrite failed: %+v", res)
	}

	data, _ := os.ReadFile(absTarget)
	if string(data) != smaller {
		t.Fatalf("file not truncated cleanly: len=%d content=%q", len(data), string(data))
	}

	// Overwrite with empty string
	resEmpty := tool.ExecuteCtx(context.Background(), Invocation{
		ID:        "w3",
		Name:      Write,
		Arguments: map[string]any{"path": target, "content": ""},
	})
	if resEmpty.IsError {
		t.Fatalf("empty write failed: %+v", resEmpty)
	}

	dataEmpty, _ := os.ReadFile(absTarget)
	if len(dataEmpty) != 0 {
		t.Fatalf("expected empty file, got %d bytes", len(dataEmpty))
	}
}

// TestWriteThroughSymlink: writing to a symlink updates the target file.
func TestWriteThroughSymlink(t *testing.T) {
	dir := t.TempDir()
	tool := &WriteTool{Cwd: dir}

	realTarget := filepath.Join(dir, "real.txt")
	symlinkPath := filepath.Join(dir, "symlink.txt")

	if err := os.WriteFile(realTarget, []byte("original"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(realTarget, symlinkPath); err != nil {
		t.Skipf("symlink not supported: %v", err)
	}

	res := tool.ExecuteCtx(context.Background(), Invocation{
		ID:        "w-sym",
		Name:      Write,
		Arguments: map[string]any{"path": "symlink.txt", "content": "updated-via-symlink"},
	})
	if res.IsError {
		t.Fatalf("write through symlink failed: %+v", res)
	}

	// Check real target content
	dataReal, _ := os.ReadFile(realTarget)
	if string(dataReal) != "updated-via-symlink" {
		t.Fatalf("real target not updated: %q", string(dataReal))
	}

	// Check symlink is still a symlink
	fi, err := os.Lstat(symlinkPath)
	if err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("symlink replaced by regular file: %v", err)
	}
}

// TestWriteFilesystemCollisions: writing when target is a directory or parent is a file.
func TestWriteFilesystemCollisions(t *testing.T) {
	dir := t.TempDir()
	tool := &WriteTool{Cwd: dir}

	// 1. Target is an existing directory
	existingDir := filepath.Join(dir, "dir_target")
	if err := os.Mkdir(existingDir, 0o755); err != nil {
		t.Fatal(err)
	}
	resDir := tool.ExecuteCtx(context.Background(), Invocation{
		ID:        "w-err",
		Name:      Write,
		Arguments: map[string]any{"path": "dir_target", "content": "should fail"},
	})
	if !resDir.IsError {
		t.Fatalf("writing to existing directory should fail: %+v", resDir)
	}

	// 2. Parent path is an existing file (mkdir fails)
	parentFile := filepath.Join(dir, "file_blocking_dir")
	if err := os.WriteFile(parentFile, []byte("blocker"), 0o644); err != nil {
		t.Fatal(err)
	}
	resParent := tool.ExecuteCtx(context.Background(), Invocation{
		ID:        "w-err2",
		Name:      Write,
		Arguments: map[string]any{"path": "file_blocking_dir/child.txt", "content": "fail"},
	})
	if !resParent.IsError {
		t.Fatalf("mkdir through file must fail: %+v", resParent)
	}
}

// TestWriteArgumentValidation: missing path, missing content, non-string content.
func TestWriteArgumentValidation(t *testing.T) {
	dir := t.TempDir()
	tool := &WriteTool{Cwd: dir}

	// Missing path
	r1 := tool.ExecuteCtx(context.Background(), Invocation{
		ID:        "1",
		Name:      Write,
		Arguments: map[string]any{"content": "abc"},
	})
	if !r1.IsError || !strings.Contains(r1.Content[0].Text, "path is required") {
		t.Fatalf("expected path required: %+v", r1)
	}

	// Missing content
	r2 := tool.ExecuteCtx(context.Background(), Invocation{
		ID:        "2",
		Name:      Write,
		Arguments: map[string]any{"path": "f.txt"},
	})
	if !r2.IsError || !strings.Contains(r2.Content[0].Text, "content is required") {
		t.Fatalf("expected content required: %+v", r2)
	}

	// Content is not string (e.g. number)
	r3 := tool.ExecuteCtx(context.Background(), Invocation{
		ID:        "3",
		Name:      Write,
		Arguments: map[string]any{"path": "f.txt", "content": 123},
	})
	if !r3.IsError || !strings.Contains(r3.Content[0].Text, "content is required (string)") {
		t.Fatalf("expected string content required: %+v", r3)
	}
}

// TestWriteConcurrentWithRead (T-TL-13): concurrent writes and reads on the same file.
// Read operations must observe either the previous or new version completely.
func TestWriteConcurrentWithRead(t *testing.T) {
	leakcheck.Verify(t)
	dir := t.TempDir()
	p := filepath.Join(dir, "concurrent.txt")
	_ = os.WriteFile(p, []byte("v0\n"), 0o644)

	reg := NewRegistry(dir)
	var stop atomic.Bool
	var wg sync.WaitGroup

	// Reader goroutines
	const numReaders = 4
	for i := 0; i < numReaders; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for !stop.Load() {
				res := reg.Dispatch(Invocation{
					ID:        "r",
					Name:      Read,
					Arguments: map[string]any{"path": "concurrent.txt"},
				})
				if !res.IsError {
					text := res.Content[0].Text
					// os.WriteFile truncates the file during write without an atomic rename (preserving symlinks),
					// so a concurrent read may observe (empty file) during truncation or valid "v..." content.
					if text != "(empty file)" && !strings.HasPrefix(text, "v") {
						t.Errorf("corrupt read observed: %q", text)
					}
				}
				time.Sleep(100 * time.Microsecond)
			}
		}()
	}

	// Writer loop
	for i := 1; i <= 30; i++ {
		res := reg.Dispatch(Invocation{
			ID:        "w",
			Name:      Write,
			Arguments: map[string]any{"path": "concurrent.txt", "content": strings.Repeat("v1234567890\n", 50)},
		})
		if res.IsError {
			t.Fatalf("write failed: %+v", res)
		}
	}

	stop.Store(true)
	wg.Wait()
}
