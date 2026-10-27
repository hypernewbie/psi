package session

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestIndexedViewsMatchEagerViewsAcrossBranches(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.jsonl")
	piFixture(t, path)
	s, err := OpenFile(path, "/tmp/ws", true)
	if err != nil {
		t.Fatal(err)
	}
	id, err := s.AppendMessage(AgentMessage{Role: "user", User: &UserMessage{Role: "user", Content: "expanded body"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.AppendProvenance(id, "/skill:synthetic fix", "skill"); err != nil {
		t.Fatal(err)
	}
	for _, leaf := range []string{s.LeafID(), "u1", "si1"} {
		t.Run(leaf, func(t *testing.T) {
			if err := s.Branch(leaf); err != nil {
				t.Fatal(err)
			}
			_, entries, _, err := ReadFileEntries(path)
			if err != nil {
				t.Fatal(err)
			}
			eagerPath, err := BuildPath(entries, leaf)
			if err != nil {
				t.Fatal(err)
			}
			want := BuildProjection(eagerPath)
			got, err := s.Projection()
			if err != nil {
				t.Fatal(err)
			}
			a, _ := json.Marshal(want.Flat)
			b, _ := json.Marshal(got.Flat)
			if !bytes.Equal(a, b) || want.State != got.State {
				t.Fatalf("projection mismatch\n%s\n%s", a, b)
			}
			recall, err := s.Recall()
			if err != nil || !reflect.DeepEqual(recall, PromptRecall(eagerPath)) {
				t.Fatalf("recall %q: %v", recall, err)
			}
			for _, all := range []bool{false, true} {
				var items []TranscriptItem
				complete, err := s.Timeline(all, 0, func(item TranscriptItem) bool { items = append(items, item); return true })
				if err != nil || !complete {
					t.Fatal(err)
				}
				expected := CurrentView(eagerPath)
				if all {
					expected = AllHistoryView(eagerPath)
				}
				if !reflect.DeepEqual(items, expected) {
					t.Fatalf("timeline all=%v differs:\n%+v\n%+v", all, items, expected)
				}
			}
		})
	}
	for _, e := range s.entries {
		if len(e.Raw) > 0 {
			t.Fatal("disk payload retained in metadata")
		}
		if e.Message != nil && e.Message.Message.User != nil && e.Message.Message.User.Content != nil {
			t.Fatal("user body retained in index")
		}
		if e.Custom != nil && len(e.Custom.Data) > 0 {
			t.Fatal("custom body retained in index")
		}
	}
}

func TestDiscoveryTitlesUnavailableAndLockMarkers(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "session.jsonl")
	piFixture(t, path)
	info, err := InspectSession(path)
	if err != nil || info.Name != "demo" || info.FirstMessage != "hi" {
		t.Fatalf("metadata: %+v %v", info, err)
	}
	lock, err := acquireLock(path)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Release()
	info, err = InspectSession(path)
	if err != nil || !strings.Contains(info.Unavailable, "locked") {
		t.Fatalf("lock: %+v %v", info, err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	bad := filepath.Join(root, "future.jsonl")
	if err := os.WriteFile(bad, bytes.Replace(raw, []byte(`"version":3`), []byte(`"version":99`), 1), 0600); err != nil {
		t.Fatal(err)
	}
	info, err = InspectSession(bad)
	if err != nil || info.ID == "" || !strings.Contains(info.Unavailable, "unsupported session version") {
		t.Fatalf("future: %+v %v", info, err)
	}
	broken := filepath.Join(root, "truncated.jsonl")
	if err := os.WriteFile(broken, append(raw, []byte(`{"type":"message"`)...), 0600); err != nil {
		t.Fatal(err)
	}
	infos, err := Discover(root)
	if err != nil || len(infos) != 3 {
		t.Fatalf("unavailable sessions vanished: %+v %v", infos, err)
	}
}

func TestIndexedOpaqueDetailsAndLazyProvenance(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "session.jsonl")
	data := `{"type":"session","version":3,"id":"metadata","cwd":"/synthetic/work"}` + "\n" +
		`{"type":"message","id":"u","parentId":null,"message":{"role":"user","content":"expanded"}}` + "\n" +
		`{"type":"custom","id":"p","parentId":"u","customType":"psi.input","data":{"targetId":"u","invocation":"/skill:synthetic"}}` + "\n" +
		`{"type":"message","id":"r","parentId":"p","message":{"role":"toolResult","toolCallId":"orphan","toolName":"read","details":["opaque"],"content":[{"type":"text","text":"hidden"}]}}` + "\n" +
		`{"type":"custom","id":"x","parentId":"r","customType":"opaque","data":["opaque"]}` + "\n"
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	s, err := OpenFile(path, "/synthetic/work", true)
	if err != nil {
		t.Fatal(err)
	}
	var text strings.Builder
	if _, err := s.Timeline(false, 0, func(it TranscriptItem) bool { text.WriteString(it.Text); return true }); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(text.String(), "/skill:synthetic") || strings.Contains(text.String(), "expanded") {
		t.Fatal(text.String())
	}
	recall, err := s.Recall()
	if err != nil || !reflect.DeepEqual(recall, []string{"/skill:synthetic"}) {
		t.Fatalf("recall: %q %v", recall, err)
	}
	// Disk changes are not silently replaced by stale cached payloads.
	if err := os.WriteFile(path, []byte(data+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Projection(); err == nil {
		t.Fatal("indexed context ignored external growth")
	}
}

type failingDurable struct {
	durableFile
	stage string
}

func (f *failingDurable) Write(p []byte) (int, error) {
	if f.stage == "write" {
		n, _ := f.durableFile.Write(p[:len(p)/2])
		return n, fmt.Errorf("synthetic write failure")
	}
	if f.stage == "short" {
		return f.durableFile.Write(p[:len(p)/2])
	}
	return f.durableFile.Write(p)
}
func (f *failingDurable) Sync() error {
	if f.stage == "sync" {
		return fmt.Errorf("synthetic sync failure")
	}
	return f.durableFile.Sync()
}
func (f *failingDurable) Close() error {
	err := f.durableFile.Close()
	if f.stage == "close" {
		return fmt.Errorf("synthetic close failure")
	}
	return err
}

func TestIndexedDurabilityFailuresDoNotAdvanceLogicalState(t *testing.T) {
	for _, initial := range []bool{false, true} {
		for _, stage := range []string{"open", "write", "short", "sync", "close", "post-stat"} {
			t.Run(fmt.Sprintf("initial=%v/%s", initial, stage), func(t *testing.T) {
				root := t.TempDir()
				s, err := New(OpenOptions{Cwd: root, SessionDir: root, Persist: true})
				if err != nil {
					t.Fatal(err)
				}
				message := AgentMessage{Role: "user", User: &UserMessage{Role: "user", Content: "synthetic input"}}
				if !initial {
					if _, err := s.AppendMessage(message); err != nil {
						t.Fatal(err)
					}
				}
				leaf, count := s.LeafID(), s.EntryCount()
				prefix, _ := os.ReadFile(s.File())
				stats := 0
				s.storage = &storageOps{open: func(path string, flags int, mode os.FileMode) (durableFile, error) {
					if stage == "open" {
						return nil, os.ErrPermission
					}
					f, err := os.OpenFile(path, flags, mode)
					if err != nil {
						return nil, err
					}
					return &failingDurable{durableFile: f, stage: stage}, nil
				}, stat: func(path string) (os.FileInfo, error) {
					stats++
					if stage == "post-stat" && (initial || stats == 2) {
						return nil, fmt.Errorf("synthetic post-write stat failure")
					}
					return os.Stat(path)
				}}
				if _, err := s.AppendMessage(message); err == nil {
					t.Fatal("injected failure succeeded")
				}
				if s.LeafID() != leaf || s.EntryCount() != count || s.WriteError() == nil {
					t.Fatal("uncertain append advanced logical state")
				}
				after, _ := os.ReadFile(s.File())
				if !bytes.HasPrefix(after, prefix) {
					t.Fatal("failure changed original prefix")
				}
				if _, err := s.AppendMessage(message); err == nil {
					t.Fatal("uncertain store accepted new work")
				}
				final, _ := os.ReadFile(s.File())
				if !bytes.Equal(after, final) {
					t.Fatal("blocked retry mutated the file")
				}
			})
		}
	}
}
