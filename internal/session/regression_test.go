package session

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
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
