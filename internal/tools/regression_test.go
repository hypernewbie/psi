package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestBashKillsDescendantsAndReturnsTail(t *testing.T) {
	ws := t.TempDir()
	reg := NewRegistry(ws)
	start := time.Now()
	res := reg.Bash.ExecuteCtx(context.Background(), Invocation{Name: Bash, Arguments: map[string]any{"command": "sleep 30 & wait", "timeout": 0.05}})
	if !res.IsError || time.Since(start) > time.Second {
		t.Fatalf("descendant retained pipes after timeout: %v %s", res, time.Since(start))
	}
	res = reg.Dispatch(Invocation{Name: Bash, Arguments: map[string]any{"command": "for ((i=1;i<=5000;i++)); do echo line-$i; done"}})
	if res.IsError || !strings.Contains(res.Content[0].Text, "line-5000") || strings.Contains(res.Content[0].Text, "line-1\n") {
		t.Fatal("shell output must retain its tail, not its head")
	}
	if details, ok := res.Details.(map[string]any); ok {
		if path, ok := details["fullOutputPath"].(string); ok {
			os.Remove(path)
		}
	}
}

func TestCancelledDispatchDoesNotMutate(t *testing.T) {
	ws := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	result := NewRegistry(ws).DispatchCtx(ctx, Invocation{Name: Write, Arguments: map[string]any{"path": "bad.txt", "content": "must not write"}})
	if !result.IsError {
		t.Fatal("cancelled dispatch succeeded")
	}
	if _, err := os.Stat(filepath.Join(ws, "bad.txt")); !os.IsNotExist(err) {
		t.Fatal("cancelled dispatch wrote a file")
	}
}

func TestOverlappingOccurrencesAndLegacyMixedEdits(t *testing.T) {
	ws := t.TempDir()
	path := filepath.Join(ws, "file.txt")
	writeF(t, path, "aaa")
	r := NewRegistry(ws)
	res := r.Dispatch(Invocation{Name: Edit, Arguments: map[string]any{"path": path, "oldText": "aa", "newText": "b"}})
	if !res.IsError {
		t.Fatal("overlapping occurrences were treated as unique")
	}
	writeF(t, path, "one two")
	res = r.Dispatch(Invocation{Name: Edit, Arguments: map[string]any{"path": path, "oldText": "two", "newText": "TWO", "edits": []any{map[string]any{"oldText": "one", "newText": "ONE"}}}})
	if res.IsError {
		t.Fatal(res.Content)
	}
	data, _ := os.ReadFile(path)
	if string(data) != "ONE TWO" {
		t.Fatalf("legacy top-level edit was ignored: %q", data)
	}
}

func TestReadRejectsInvalidNumericInputs(t *testing.T) {
	ws := t.TempDir()
	writeF(t, filepath.Join(ws, "file.txt"), "a\nb\n")
	for _, value := range []any{"not-a-number", -1.0, 0.5, 1e100} {
		result := NewRegistry(ws).Dispatch(Invocation{Name: Read, Arguments: map[string]any{"path": "file.txt", "offset": 2.0, "limit": value}})
		if !result.IsError {
			t.Fatalf("invalid limit accepted: %v", value)
		}
	}
}
