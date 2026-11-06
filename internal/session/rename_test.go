package session

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestRenameNoReplacePreservesTarget(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "source.jsonl")
	dst := filepath.Join(dir, "target.jsonl")
	if err := os.WriteFile(src, []byte("source"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dst, []byte("target"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := renameNoReplace(src, dst); err == nil {
		t.Fatal("expected an error when the target exists")
	}
	srcAfter, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	dstAfter, err := os.ReadFile(dst)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(srcAfter, []byte("source")) || !bytes.Equal(dstAfter, []byte("target")) {
		t.Fatal("collision modified a file")
	}
}
