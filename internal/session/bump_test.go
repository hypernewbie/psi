package session

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func bumpTestMessage(role string) AgentMessage {
	switch role {
	case "user":
		return AgentMessage{Role: "user", User: &UserMessage{Role: "user", Content: "hi", Timestamp: 1}}
	case "toolResult":
		return AgentMessage{Role: "toolResult", ToolResult: &ToolResultMessage{Role: "toolResult", ToolCallID: "c1", ToolName: "read", Timestamp: 1}}
	case "custom":
		return AgentMessage{Role: "custom", Custom: &CustomMessage{Role: "custom", CustomType: "ext", Content: "note", Timestamp: 1}}
	case "system":
		return AgentMessage{Role: "system", System: &SystemMessage{Role: "system", Content: "", Timestamp: 1}}
	case "bashExecution":
		return AgentMessage{Role: "bashExecution", BashExecution: &BashExecutionMessage{Role: "bashExecution", Command: "ls", Output: "x", Timestamp: 1}}
	default:
		return AgentMessage{Role: role}
	}
}

func bumpAssistant(stop string, blocks ...ContentBlock) AgentMessage {
	return AgentMessage{Role: "assistant", Assistant: &AssistantMessage{Role: "assistant", StopReason: stop, Content: blocks}}
}

func bumpText(text string) ContentBlock {
	return ContentBlock{Type: "text", Text: &TextContent{Type: "text", Text: text}}
}

func bumpCall(id string) ContentBlock {
	return ContentBlock{Type: "toolCall", Call: &ToolCall{Type: "toolCall", ID: id, Name: "read", Arguments: map[string]any{}}}
}

func TestBumpEligibility(t *testing.T) {
	cases := []struct {
		name string
		flat []AgentMessage
		want bool
	}{
		{name: "empty", want: false},
		{name: "system only", flat: []AgentMessage{bumpTestMessage("system")}, want: false},
		{name: "user", flat: []AgentMessage{bumpTestMessage("user")}, want: true},
		{name: "user then system", flat: []AgentMessage{bumpTestMessage("user"), bumpTestMessage("system")}, want: true},
		{name: "nil trailing system", flat: []AgentMessage{bumpTestMessage("user"), {Role: "system"}}, want: false},
		{name: "tool result", flat: []AgentMessage{bumpTestMessage("toolResult")}, want: true},
		{name: "custom", flat: []AgentMessage{bumpTestMessage("custom")}, want: true},
		{name: "error", flat: []AgentMessage{bumpAssistant("error", bumpText(""))}, want: true},
		{name: "aborted", flat: []AgentMessage{bumpAssistant("aborted", bumpText(""))}, want: true},
		{name: "tool use with call", flat: []AgentMessage{bumpAssistant("toolUse", bumpCall("c1"))}, want: true},
		{name: "tool use without call", flat: []AgentMessage{bumpAssistant("toolUse", bumpText("thinking"))}, want: false},
		{name: "stop", flat: []AgentMessage{bumpAssistant("stop", bumpText("done"))}, want: false},
		{name: "length", flat: []AgentMessage{bumpAssistant("length", bumpText("cut"))}, want: false},
		{name: "deferred", flat: []AgentMessage{bumpAssistant("deferred", bumpText("later"))}, want: false},
		{name: "pending", flat: []AgentMessage{bumpAssistant("pending", bumpText("later"))}, want: false},
		{name: "unknown stop", flat: []AgentMessage{bumpAssistant("mystery", bumpText("?"))}, want: false},
		{name: "missing user payload", flat: []AgentMessage{{Role: "user"}}, want: false},
		{name: "missing assistant payload", flat: []AgentMessage{{Role: "assistant"}}, want: false},
		{name: "bash execution", flat: []AgentMessage{bumpTestMessage("bashExecution")}, want: false},
		{name: "compaction summary", flat: []AgentMessage{{Role: "compactionSummary", CompactionSummary: &CompactionSummaryMessage{Role: "compactionSummary", Summary: "s"}}}, want: false},
		{name: "branch summary", flat: []AgentMessage{{Role: "branchSummary", BranchSummary: &BranchSummaryMessage{Role: "branchSummary", Summary: "s"}}}, want: false},
		{name: "unknown role", flat: []AgentMessage{{Role: "mystery"}}, want: false},
		{name: "completed then system", flat: []AgentMessage{bumpAssistant("stop", bumpText("done")), bumpTestMessage("system")}, want: false},
		{name: "failed then system", flat: []AgentMessage{bumpAssistant("error", bumpText("")), bumpTestMessage("system")}, want: true},
		{name: "failed before completed", flat: []AgentMessage{bumpAssistant("error", bumpText("")), bumpAssistant("stop", bumpText("done"))}, want: false},
		{name: "completed before failed", flat: []AgentMessage{bumpAssistant("stop", bumpText("done")), bumpAssistant("error", bumpText(""))}, want: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := CanBump(tc.flat); got != tc.want {
				t.Fatalf("CanBump=%v; want %v", got, tc.want)
			}
		})
	}
}

func TestFailureMarkerStaysInvisible(t *testing.T) {
	dir := t.TempDir()
	store, err := New(OpenOptions{Cwd: dir, SessionDir: dir, Persist: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.AppendMessage(bumpTestMessage("user")); err != nil {
		t.Fatal(err)
	}
	reason := "transport failed"
	assistant := AgentMessage{Role: "assistant", Assistant: &AssistantMessage{
		Role: "assistant", StopReason: "error", ErrorMessage: &reason,
		Content: []ContentBlock{bumpText("")},
	}}
	if _, err := store.AppendMessage(assistant); err != nil {
		t.Fatal(err)
	}
	path, err := store.ActivePath()
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range CurrentView(path) {
		if item.Kind == KindAssistant {
			t.Fatalf("failure marker became visible: %#v", item)
		}
	}
	reopened, err := OpenFile(store.File(), dir, true)
	if err != nil {
		t.Fatal(err)
	}
	reopenedPath, err := reopened.ActivePath()
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range CurrentView(reopenedPath) {
		if item.Kind == KindAssistant {
			t.Fatalf("reopened failure marker became visible: %#v", item)
		}
	}
	proj, err := reopened.Projection()
	if err != nil {
		t.Fatal(err)
	}
	last := LastConversationalMessage(proj.Flat)
	if last == nil || last.Assistant == nil || last.Assistant.ErrorMessage == nil || *last.Assistant.ErrorMessage != reason {
		t.Fatalf("saved failure reason missing after reopen: %#v", last)
	}
	if !CanBump(proj.Flat) {
		t.Fatal("reopened failure marker is not bumpable")
	}
}

func persistentBumpStore(t *testing.T) (*Store, []byte) {
	t.Helper()
	dir := t.TempDir()
	store, err := New(OpenOptions{Cwd: dir, SessionDir: dir, Persist: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.AppendMessage(bumpTestMessage("user")); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Projection(); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(store.File())
	if err != nil {
		t.Fatal(err)
	}
	return store, before
}

func requireUnchangedBumpStorage(t *testing.T, store *Store, before []byte, leaf string, count int) {
	t.Helper()
	after, err := os.ReadFile(store.File())
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(after, before) || store.LeafID() != leaf || store.EntryCount() != count {
		t.Fatal("storage check changed durable bytes or logical state")
	}
}

func TestCheckWritablePreflight(t *testing.T) {
	t.Run("healthy", func(t *testing.T) {
		store, before := persistentBumpStore(t)
		leaf, count := store.LeafID(), store.EntryCount()
		if err := store.CheckWritable(); err != nil {
			t.Fatal(err)
		}
		requireUnchangedBumpStorage(t, store, before, leaf, count)
		if store.WriteError() != nil {
			t.Fatalf("healthy check set sticky error: %v", store.WriteError())
		}
	})

	t.Run("lazy and non-persistent skip I/O", func(t *testing.T) {
		dir := t.TempDir()
		lazy, err := New(OpenOptions{Cwd: dir, SessionDir: dir, Persist: true})
		if err != nil {
			t.Fatal(err)
		}
		lazy.storage = &storageOps{
			stat: func(string) (os.FileInfo, error) { return nil, errors.New("stat must not run") },
			open: func(string, int, os.FileMode) (durableFile, error) { return nil, errors.New("open must not run") },
		}
		if err := lazy.CheckWritable(); err != nil {
			t.Fatalf("lazy store performed I/O: %v", err)
		}
		if _, err := os.Stat(lazy.File()); !os.IsNotExist(err) {
			t.Fatalf("lazy check created a file: %v", err)
		}
		nonPersistent, err := New(OpenOptions{Cwd: dir, SessionDir: dir, Persist: false})
		if err != nil {
			t.Fatal(err)
		}
		nonPersistent.storage = lazy.storage
		if err := nonPersistent.CheckWritable(); err != nil {
			t.Fatalf("non-persistent store performed I/O: %v", err)
		}
	})

	t.Run("external growth", func(t *testing.T) {
		store, _ := persistentBumpStore(t)
		leaf, count := store.LeafID(), store.EntryCount()
		f, err := os.OpenFile(store.File(), os.O_APPEND|os.O_WRONLY, 0o600)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.WriteString("\n"); err != nil {
			t.Fatal(err)
		}
		if err := f.Close(); err != nil {
			t.Fatal(err)
		}
		changed, err := os.ReadFile(store.File())
		if err != nil {
			t.Fatal(err)
		}
		if err := store.CheckWritable(); err == nil {
			t.Fatal("external growth accepted")
		}
		if store.WriteError() == nil {
			t.Fatal("failed storage check did not set a sticky error")
		}
		requireUnchangedBumpStorage(t, store, changed, leaf, count)
	})

	t.Run("same-size rewrite", func(t *testing.T) {
		store, before := persistentBumpStore(t)
		leaf, count := store.LeafID(), store.EntryCount()
		info, err := os.Stat(store.File())
		if err != nil {
			t.Fatal(err)
		}
		rewritten := bytes.Replace(before, []byte("hi"), []byte("ho"), 1)
		if len(rewritten) != len(before) || bytes.Equal(rewritten, before) {
			t.Fatal("test rewrite did not keep the size")
		}
		if err := os.WriteFile(store.File(), rewritten, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(store.File(), info.ModTime(), info.ModTime()); err != nil {
			t.Fatal(err)
		}
		changed, err := os.ReadFile(store.File())
		if err != nil {
			t.Fatal(err)
		}
		if err := store.CheckWritable(); err == nil {
			t.Fatal("same-size rewrite accepted")
		}
		requireUnchangedBumpStorage(t, store, changed, leaf, count)
	})

	t.Run("non-persistent copy of changed source", func(t *testing.T) {
		store, before := persistentBumpStore(t)
		copyStore, err := OpenFile(store.File(), store.Cwd(), false)
		if err != nil {
			t.Fatal(err)
		}
		copyStore.storage = &storageOps{
			stat: func(string) (os.FileInfo, error) { return nil, errors.New("stat must not run") },
			open: func(string, int, os.FileMode) (durableFile, error) { return nil, errors.New("open must not run") },
		}
		if err := os.WriteFile(store.File(), append(before, '\n'), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := copyStore.CheckWritable(); err != nil {
			t.Fatalf("non-persistent copy read the changed source: %v", err)
		}
	})

	t.Run("sticky refusal is reused", func(t *testing.T) {
		store, _ := persistentBumpStore(t)
		injected := errors.New("injected stat failure")
		store.storage = &storageOps{stat: func(string) (os.FileInfo, error) { return nil, injected }}
		if err := store.CheckWritable(); err == nil {
			t.Fatal("stat failure accepted")
		}
		store.storage = nil
		if err := store.CheckWritable(); err == nil || !strings.Contains(err.Error(), "storage blocked") {
			t.Fatalf("sticky error not reused: %v", err)
		}
	})
}

type failingCheckFile struct {
	*os.File
	readErr  error
	closeErr error
}

func (f *failingCheckFile) ReadAt(p []byte, off int64) (int, error) {
	if f.readErr != nil {
		return 0, f.readErr
	}
	return f.File.ReadAt(p, off)
}

func (f *failingCheckFile) Close() error {
	return errors.Join(f.File.Close(), f.closeErr)
}

func TestCheckWritableIOFaults(t *testing.T) {
	cases := []struct {
		name  string
		setup func(*Store) *storageOps
	}{
		{
			name: "stat",
			setup: func(*Store) *storageOps {
				return &storageOps{stat: func(string) (os.FileInfo, error) { return nil, errors.New("stat failed") }}
			},
		},
		{
			name: "open",
			setup: func(*Store) *storageOps {
				return &storageOps{open: func(string, int, os.FileMode) (durableFile, error) { return nil, errors.New("open failed") }}
			},
		},
		{
			name: "read",
			setup: func(*Store) *storageOps {
				return &storageOps{open: func(path string, flags int, mode os.FileMode) (durableFile, error) {
					f, err := os.OpenFile(path, flags, mode)
					if err != nil {
						return nil, err
					}
					return &failingCheckFile{File: f, readErr: errors.New("read failed")}, nil
				}}
			},
		},
		{
			name: "close",
			setup: func(*Store) *storageOps {
				return &storageOps{open: func(path string, flags int, mode os.FileMode) (durableFile, error) {
					f, err := os.OpenFile(path, flags, mode)
					if err != nil {
						return nil, err
					}
					return &failingCheckFile{File: f, closeErr: errors.New("close failed")}, nil
				}}
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store, before := persistentBumpStore(t)
			leaf, count := store.LeafID(), store.EntryCount()
			store.storage = tc.setup(store)
			if err := store.CheckWritable(); err == nil {
				t.Fatal("I/O fault accepted")
			}
			if store.WriteError() == nil {
				t.Fatal("I/O fault did not set a sticky error")
			}
			requireUnchangedBumpStorage(t, store, before, leaf, count)
		})
	}
}

func TestCheckWritableRefusesExternalReplacement(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "session.jsonl")
	piFixture(t, path)
	store, err := OpenFile(path, "/tmp/ws", true)
	if err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	leaf, count := store.LeafID(), store.EntryCount()
	store.storage = &storageOps{open: func(path string, flags int, mode os.FileMode) (durableFile, error) {
		if err := os.WriteFile(path+".new", before, mode); err != nil {
			return nil, err
		}
		if err := os.Rename(path+".new", path); err != nil {
			return nil, err
		}
		return os.OpenFile(path, flags, mode)
	}}
	if err := store.CheckWritable(); err == nil {
		t.Fatal("replacement raced the generation check")
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(after, before) || store.LeafID() != leaf || store.EntryCount() != count {
		t.Fatal("refused replacement changed logical or physical state")
	}
	if store.WriteError() == nil {
		t.Fatal("replacement refusal did not set a sticky error")
	}
}

func TestCheckWritablePreservesModTime(t *testing.T) {
	store, _ := persistentBumpStore(t)
	info, err := os.Stat(store.File())
	if err != nil {
		t.Fatal(err)
	}
	if err := store.CheckWritable(); err != nil {
		t.Fatal(err)
	}
	after, err := os.Stat(store.File())
	if err != nil {
		t.Fatal(err)
	}
	if !after.ModTime().Equal(info.ModTime()) {
		t.Fatalf("storage check changed modification time: %v -> %v", info.ModTime(), after.ModTime())
	}
}
