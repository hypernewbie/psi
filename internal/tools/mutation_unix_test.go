package tools

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// TestMutationQueueUnresolvablePathFailsClosed: a permission failure during
// key resolution fails the mutation without running the body, instead of
// running it unserialized.
func TestMutationQueueUnresolvablePathFailsClosed(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	dir := t.TempDir()
	blocked := filepath.Join(dir, "blocked")
	if err := os.Mkdir(blocked, 0o755); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(blocked, "x.txt")
	if err := os.WriteFile(target, []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(blocked, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(blocked, 0o755) })
	reg := NewRegistry(dir)
	ran := false
	reg.Write.WriteFile = func(path string, data []byte, mode os.FileMode) error {
		ran = true
		return os.WriteFile(path, data, mode)
	}
	res := reg.DispatchCtx(context.Background(), Invocation{ID: "w1", Name: Write,
		Arguments: map[string]any{"path": target, "content": "must not write"}})
	if !res.IsError || ran {
		t.Fatalf("unresolvable path must fail closed: ran=%v result=%+v", ran, res)
	}
}
