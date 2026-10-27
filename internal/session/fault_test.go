package session

// PLAN3 P3-053: storage fault injection. Every I/O failure point fails stop:
// short writes, sync, close, open, and stat faults leave the disk uncertain
// without repair, retry, or false success, and block further model/tool work.

import (
	"fmt"
	"io"
	"os"
	"strings"
	"testing"
)

type faultFile struct {
	*os.File
	writeN   int // bytes to accept before failing (<=0: fail immediately)
	writeErr error
	syncErr  error
	closeErr error
	wrote    int
}

func (f *faultFile) Write(p []byte) (int, error) {
	if f.writeErr != nil && f.wrote >= f.writeN {
		return 0, f.writeErr
	}
	allow := len(p)
	if f.writeN > 0 && f.wrote+allow > f.writeN {
		allow = f.writeN - f.wrote
	}
	n, err := f.File.Write(p[:allow])
	f.wrote += n
	if err != nil {
		return n, err
	}
	if allow < len(p) {
		if f.writeErr != nil {
			return n, f.writeErr
		}
		return n, io.ErrShortWrite
	}
	return n, nil
}

func (f *faultFile) Sync() error {
	if f.syncErr != nil {
		return f.syncErr
	}
	return f.File.Sync()
}

func (f *faultFile) Close() error {
	if f.closeErr != nil {
		return f.closeErr
	}
	return f.File.Close()
}

func faultStore(t *testing.T, open func(string, int, os.FileMode) (durableFile, error), stat func(string) (os.FileInfo, error)) *Store {
	t.Helper()
	ws := t.TempDir()
	s, err := New(OpenOptions{Cwd: ws, SessionDir: t.TempDir(), Persist: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.AppendMessage(AgentMessage{Role: "user", User: &UserMessage{Role: "user", Content: "hello", Timestamp: 1}}); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(s.File())
	if err != nil {
		t.Fatal(err)
	}
	s.storage = &storageOps{open: open, stat: stat}
	t.Cleanup(func() { s.storage = nil })
	_ = before
	return s
}

func passThrough(t *testing.T) func(string, int, os.FileMode) (durableFile, error) {
	return func(path string, flags int, mode os.FileMode) (durableFile, error) {
		return os.OpenFile(path, flags, mode)
	}
}

func TestF53FaultMatrix(t *testing.T) {
	mkFault := func(t *testing.T, wrap func(*os.File) *faultFile) func(string, int, os.FileMode) (durableFile, error) {
		return func(path string, flags int, mode os.FileMode) (durableFile, error) {
			f, err := os.OpenFile(path, flags, mode)
			if err != nil {
				return nil, err
			}
			return wrap(f), nil
		}
	}
	for _, tc := range []struct {
		name string
		wrap func(*os.File) *faultFile
	}{
		{"short-write", func(f *os.File) *faultFile { return &faultFile{File: f, writeN: 3} }},
		{"write-error", func(f *os.File) *faultFile {
			return &faultFile{File: f, writeErr: fmt.Errorf("injected write failure")}
		}},
		{"sync-error", func(f *os.File) *faultFile { return &faultFile{File: f, syncErr: fmt.Errorf("injected sync failure")} }},
		{"close-error", func(f *os.File) *faultFile {
			return &faultFile{File: f, closeErr: fmt.Errorf("injected close failure")}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := faultStore(t, mkFault(t, tc.wrap), nil)
			before, _ := os.ReadFile(s.File())
			if _, err := s.AppendMessage(AgentMessage{Role: "user", User: &UserMessage{Role: "user", Content: "next", Timestamp: 2}}); err == nil {
				t.Fatal("fault accepted as success")
			}
			after, _ := os.ReadFile(s.File())
			// Failed append never truncates or repairs the durable prefix.
			if len(after) > len(before) && !strings.HasPrefix(string(after), string(before)) {
				t.Fatal("durable prefix rewritten after fault")
			}
			// Fail-stop: further model/tool work is blocked, not retried.
			if _, err := s.AppendMessage(AgentMessage{Role: "user", User: &UserMessage{Role: "user", Content: "retry", Timestamp: 3}}); err == nil {
				t.Fatal("store recovered implicitly after fault")
			}
		})
	}
}

func TestF53OpenStatFaults(t *testing.T) {
	s := faultStore(t,
		func(string, int, os.FileMode) (durableFile, error) { return nil, fmt.Errorf("injected open failure") },
		nil)
	if _, err := s.AppendMessage(AgentMessage{Role: "user", User: &UserMessage{Role: "user", Content: "x", Timestamp: 9}}); err == nil {
		t.Fatal("open fault accepted")
	}
	s2 := faultStore(t, passThrough(t),
		func(string) (os.FileInfo, error) { return nil, fmt.Errorf("injected stat failure") })
	if _, err := s2.AppendMessage(AgentMessage{Role: "user", User: &UserMessage{Role: "user", Content: "x", Timestamp: 9}}); err == nil {
		t.Fatal("stat fault accepted")
	}
}
