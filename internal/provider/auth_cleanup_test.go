package provider

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReleaseAuthEntriesWipesRawAllocations(t *testing.T) {
	first := []byte(`{"key":"first"}`)
	second := []byte(`{"key":"second"}`)
	entries := map[string]json.RawMessage{"one": first, "two": second}
	ReleaseAuthEntries(entries)
	if len(entries) != 0 {
		t.Fatal("release retained entry keys")
	}
	for i, b := range first {
		if b != 0 {
			t.Fatalf("first raw byte %d survived release", i)
		}
	}
	for i, b := range second {
		if b != 0 {
			t.Fatalf("second raw byte %d survived release", i)
		}
	}
}

func TestWriteAuthDocumentCountsFinalNewlineAtLimit(t *testing.T) {
	base := map[string]json.RawMessage{"unrelated": json.RawMessage(`""`)}
	encoded, err := json.MarshalIndent(base, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	fillerLen := maxAuthJSONSize - 1 - len(encoded)
	if fillerLen < 0 {
		t.Fatal("base fixture exceeds the document limit")
	}
	atLimit := map[string]json.RawMessage{"unrelated": mustJSONString(t, strings.Repeat("x", fillerLen))}
	encoded, err = json.MarshalIndent(atLimit, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if got := len(encoded) + 1; got != maxAuthJSONSize {
		t.Fatalf("exact fixture size = %d, want %d", got, maxAuthJSONSize)
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "auth.json")
	target, err := resolveAuthTarget(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeAuthDocument(target, atLimit, 0o600, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) != maxAuthJSONSize || data[len(data)-1] != '\n' {
		t.Fatalf("written size=%d last=%q", len(data), data[len(data)-1])
	}

	overLimit := map[string]json.RawMessage{"unrelated": mustJSONString(t, strings.Repeat("x", fillerLen+1))}
	target, err = resolveAuthTarget(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeAuthDocument(target, overLimit, 0o600, func() error { return nil }); !errors.Is(err, errAuthDocumentTooLarge) {
		t.Fatalf("one-byte-over document: %v", err)
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(after, data) {
		t.Fatal("rejected over-limit write changed the target")
	}
	if leftovers, _ := filepath.Glob(filepath.Join(dir, ".psi-auth-*")); len(leftovers) != 0 {
		t.Fatalf("temporary files remain: %v", leftovers)
	}
}

func mustJSONString(t *testing.T, value string) json.RawMessage {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}
