//go:build unix

package session

import (
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
)

func TestDiscoverHeadersSkipsPipes(t *testing.T) {
	const header = `{"type":"session","version":3,"id":"pipe-id","cwd":"/ws"}` + "\n"
	for _, name := range []string{"fifo", "symlink to fifo"} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			pipe := filepath.Join(dir, "pipe.jsonl")
			if name == "symlink to fifo" {
				pipe = filepath.Join(dir, "pipe")
				if err := os.Symlink("pipe", filepath.Join(dir, "link.jsonl")); err != nil {
					t.Fatal(err)
				}
			}
			if err := unix.Mkfifo(pipe, 0600); err != nil {
				t.Fatal(err)
			}
			// Keep a writer open with one complete record so a regressed
			// header probe reads a false session instead of blocking on open.
			fd, err := unix.Open(pipe, unix.O_RDWR|unix.O_NONBLOCK, 0600)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = unix.Close(fd) })
			if n, err := unix.Write(fd, []byte(header)); err != nil || n != len(header) {
				t.Fatalf("write pipe: %d bytes, error = %v", n, err)
			}
			infos, err := DiscoverHeaders(dir)
			if err != nil || len(infos) != 0 {
				t.Fatalf("pipe must not be opened as a session: %+v, error = %v", infos, err)
			}
		})
	}
}

func TestDiscoverHeadersFollowsRegularFileSymlink(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "sessions")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "real.jsonl"), []byte(`{"type":"session","version":3,"id":"linked-id","cwd":"/ws"}`+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("../real.jsonl", filepath.Join(dir, "link.jsonl")); err != nil {
		t.Fatal(err)
	}
	infos, err := DiscoverHeaders(dir)
	if err != nil || len(infos) != 1 || infos[0].ID != "linked-id" {
		t.Fatalf("regular file symlink lost: %+v, error = %v", infos, err)
	}
}
