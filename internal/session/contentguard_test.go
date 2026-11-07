package session

import (
	"bytes"
	"os"
	"strings"
	"testing"
)

func TestAppendRefusesSameSizeRewriteWithRestoredMtime(t *testing.T) {
	path := twoMessageFile(t)
	s := openPersisted(t, path)
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	rewriteSecondLineSameID(path)
	if err := os.Chtimes(path, before.ModTime(), before.ModTime()); err != nil {
		t.Fatal(err)
	}
	rewritten, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.AppendMessage(AgentMessage{Role: "user", User: &UserMessage{Role: "user", Content: "after"}})
	if err == nil || !strings.Contains(err.Error(), "changed externally") {
		t.Fatalf("rewrite accepted: %v", err)
	}
	if s.LeafID() != "u2" {
		t.Fatalf("leaf moved on refused append: %q", s.LeafID())
	}
	disk, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(disk, rewritten) {
		t.Fatalf("refused append modified the file: %v", err)
	}
}

func TestNewSessionRefusesRewriteAfterFirstFlush(t *testing.T) {
	s, err := New(OpenOptions{Cwd: t.TempDir(), SessionDir: t.TempDir(), Persist: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.AppendMessage(AgentMessage{Role: "user", User: &UserMessage{Role: "user", Content: "hello"}}); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(s.file)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(s.file)
	if err != nil {
		t.Fatal(err)
	}
	data = bytes.Replace(data, []byte("hello"), []byte("HELLO"), 1)
	if err := os.WriteFile(s.file, data, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(s.file, st.ModTime(), st.ModTime()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AppendMessage(AgentMessage{Role: "user", User: &UserMessage{Role: "user", Content: "next"}}); err == nil {
		t.Fatal("rewrite after first flush accepted")
	}
}

func TestContentGuardTracksSuccessfulAppends(t *testing.T) {
	path := twoMessageFile(t)
	s := openPersisted(t, path)
	for _, text := range []string{"first", "second"} {
		if _, err := s.AppendMessage(AgentMessage{Role: "user", User: &UserMessage{Role: "user", Content: text}}); err != nil {
			t.Fatalf("append %q: %v", text, err)
		}
	}
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	data = bytes.Replace(data, []byte("first"), []byte("FIRST"), 1)
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, before.ModTime(), before.ModTime()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AppendMessage(AgentMessage{Role: "user", User: &UserMessage{Role: "user", Content: "third"}}); err == nil {
		t.Fatal("rewrite after successful appends accepted")
	}
}
