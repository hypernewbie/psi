package session

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestAppendCompleteUnterminatedRecord(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.jsonl")
	prefix := `{"type":"session","version":3,"id":"test","cwd":"/workspace","timestamp":"2026-01-01T00:00:00Z"}` + "\n" +
		`{"type":"message","id":"u","parentId":null,"timestamp":"2026-01-01T00:00:01Z","message":{"role":"user","content":"` + strings.Repeat("x", 80*1024) + `","timestamp":1}}`
	if err := os.WriteFile(path, []byte(prefix), 0600); err != nil {
		t.Fatal(err)
	}
	store, err := OpenFile(path, "/workspace", true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.AppendMessage(AgentMessage{Role: "user", User: &UserMessage{Role: "user", Content: "next", Timestamp: 2}}); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	if !bytes.HasPrefix(data, []byte(prefix+"\n")) {
		t.Fatal("original record was overwritten or concatenated")
	}
	if _, _, _, err := ReadFileEntries(path); err != nil {
		t.Fatal(err)
	}
}

func TestExternalReplacementWithSameSizeIsRefused(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.jsonl")
	piFixture(t, path)
	s, err := OpenFile(path, "/workspace", true)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	other := path + ".new"
	if err := os.WriteFile(other, data, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(other, path); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AppendModelChange("anthropic", "m"); err == nil {
		t.Fatal("same-size replacement was not detected")
	}
}

func TestSystemReplayPreservesOrderAndToolRedefinition(t *testing.T) {
	var first, patch AgentMessage
	if err := json.Unmarshal([]byte(`{"role":"system","content":"base","sections":{"z":"first","a":"second"},"toolsAdded":[{"name":"read","description":"old"}],"timestamp":1}`), &first); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(`{"role":"system","content":"more","sections":{"z":null},"toolsRemoved":[{"name":"read"}],"toolsAdded":[{"name":"read","description":"new"}],"timestamp":2}`), &patch); err != nil {
		t.Fatal(err)
	}
	eff := ReplaySystem([]AgentMessage{first, patch})
	if eff.Content != "base\n\nmore\n\nsecond" || len(eff.Tools) != 1 || eff.Tools[0].Description != "new" {
		t.Fatalf("replay: %+v", eff)
	}
	encoded, err := json.Marshal(eff.Checkpoint)
	if err != nil {
		t.Fatal(err)
	}
	var checkpoint AgentMessage
	if err := json.Unmarshal(encoded, &checkpoint); err != nil {
		t.Fatal(err)
	}
	if ReplaySystem([]AgentMessage{checkpoint}).Content != eff.Content {
		t.Fatal("checkpoint does not reconstruct complete system state")
	}
}

func TestToolArgumentsPreserveLargeIntegers(t *testing.T) {
	var m AgentMessage
	if err := json.Unmarshal([]byte(`{"role":"assistant","content":[{"type":"toolCall","id":"c","name":"test","arguments":{"n":9007199254740993}}],"stopReason":"toolUse","timestamp":1}`), &m); err != nil {
		t.Fatal(err)
	}
	n := m.Assistant.Content[0].Call.Arguments["n"]
	if n != json.Number("9007199254740993") {
		t.Fatalf("integer rounded: %#v", n)
	}
}

// Optional differential check against a real Pi session-manager module. The
// default suite remains Go-only; no ignored checkout is required.
func TestReferencePiProjection(t *testing.T) {
	module := os.Getenv("PSI_PI_SESSION_MODULE")
	if module == "" {
		t.Skip("set PSI_PI_SESSION_MODULE to a pinned Pi dist/core/session-manager.js")
	}
	path := filepath.Join(t.TempDir(), "pi-session.jsonl")
	piFixture(t, path)
	s, err := OpenFile(path, "/tmp/ws", true)
	if err != nil {
		t.Fatal(err)
	}
	keptID, err := s.AppendMessage(AgentMessage{Role: "user", User: &UserMessage{Role: "user", Content: "psi continues", Timestamp: 20}})
	if err != nil {
		t.Fatal(err)
	}
	assertReferenceProjection(t, module, path, s)
	// Check a new Go-written compaction and complete system checkpoint too.
	instruction := "updated psi instruction"
	if _, err := s.AppendMessage(AgentMessage{Role: "system", System: &SystemMessage{Role: "system", Content: "", Sections: map[string]*string{"psi": &instruction}, Timestamp: 21}}); err != nil {
		t.Fatal(err)
	}
	proj, err := s.Projection()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.AppendCompaction("summary written by psi", keptID, 100, ReplaySystem(proj.Flat).Checkpoint); err != nil {
		t.Fatal(err)
	}
	assertReferenceProjection(t, module, path, s)
}

func assertReferenceProjection(t *testing.T, module, path string, s *Store) {
	t.Helper()
	projection, err := s.Projection()
	if err != nil {
		t.Fatal(err)
	}
	script := `const m = await import(process.argv[1]); const entries = m.loadEntriesFromFile(process.argv[2]); const p = m.buildSessionProjection(entries.filter(e => e.type !== "session")); console.log(JSON.stringify(p.messages));`
	out, err := exec.Command("node", "--input-type=module", "-e", script, module, path).CombinedOutput()
	if err != nil {
		t.Fatalf("real Pi reader: %v: %s", err, out)
	}
	want, err := json.Marshal(projection.Flat)
	if err != nil {
		t.Fatal(err)
	}
	var a, b any
	if err := json.Unmarshal(out, &a); err != nil {
		t.Fatalf("Pi output: %s: %v", out, err)
	}
	if err := json.Unmarshal(want, &b); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(a, b) {
		t.Fatalf("real Pi projection differs:\nPi: %s\nGo: %s", out, want)
	}
}
