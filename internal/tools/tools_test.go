package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func workdir(t *testing.T) string {
	t.Helper()
	return t.TempDir()
}

func writeF(t *testing.T, path, s string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(s), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestRegistryRejectsUnknown(t *testing.T) {
	r := NewRegistry(t.TempDir())
	res := r.Dispatch(Invocation{ID: "1", Name: "grep", Arguments: map[string]any{}})
	if !res.IsError || !strings.Contains(res.Content[0].Text, "unknown tool") {
		t.Errorf("unknown must fail: %+v", res)
	}
}

func TestReadBoundaries(t *testing.T) {
	ws := workdir(t)
	// Long lines + unicode.
	lines := []string{"hello", "wörld 🌍", strings.Repeat("x", 60000), "end"}
	writeF(t, filepath.Join(ws, "f.txt"), strings.Join(lines, "\n")+"\n")
	r := NewRegistry(ws)
	// Offset/limit.
	res := r.Dispatch(Invocation{ID: "1", Name: "read", Arguments: map[string]any{"path": "f.txt", "offset": float64(2), "limit": float64(1)}})
	if res.IsError || !strings.Contains(res.Content[0].Text, "wörld") {
		t.Errorf("offset/limit: %+v", res)
	}
	// Exact limits: 2000 lines.
	many := strings.Repeat("a\n", 2000)
	writeF(t, filepath.Join(ws, "many.txt"), many)
	res = r.Dispatch(Invocation{ID: "2", Name: "read", Arguments: map[string]any{"path": "many.txt"}})
	if res.IsError || strings.Contains(res.Content[0].Text, "truncated") {
		t.Errorf("exact 2000 lines should not truncate")
	}
	writeF(t, filepath.Join(ws, "many2.txt"), many+"extra\n")
	res = r.Dispatch(Invocation{ID: "3", Name: "read", Arguments: map[string]any{"path": "many2.txt"}})
	if !strings.Contains(res.Content[0].Text, "truncated") {
		t.Errorf("2001 lines must truncate: %q", res.Content[0].Text[:100])
	}
	// Missing / empty / denied.
	res = r.Dispatch(Invocation{ID: "4", Name: "read", Arguments: map[string]any{"path": "nope.txt"}})
	if !res.IsError {
		t.Errorf("missing must fail")
	}
	writeF(t, filepath.Join(ws, "empty.txt"), "")
	res = r.Dispatch(Invocation{ID: "5", Name: "read", Arguments: map[string]any{"path": "empty.txt"}})
	if res.IsError || res.Content[0].Text != "(empty file)" {
		t.Errorf("empty: %+v", res)
	}
}

func TestReadImages(t *testing.T) {
	ws := workdir(t)
	// Minimal PNG header (not validated deeply in unit test).
	writeF(t, filepath.Join(ws, "a.png"), "PNGDATA")
	r := NewRegistry(ws)
	res := r.Dispatch(Invocation{ID: "1", Name: "read", Arguments: map[string]any{"path": "a.png"}})
	if res.IsError || len(res.Content) != 2 || res.Content[1].Type != "image" {
		t.Errorf("image: %+v", res)
	}
}

func TestWrite(t *testing.T) {
	ws := workdir(t)
	r := NewRegistry(ws)
	res := r.Dispatch(Invocation{ID: "1", Name: "write", Arguments: map[string]any{"path": "sub/new.txt", "content": "héllo 🌍"}})
	if res.IsError {
		t.Fatalf("write: %+v", res)
	}
	b, _ := os.ReadFile(filepath.Join(ws, "sub", "new.txt"))
	if string(b) != "héllo 🌍" {
		t.Errorf("content=%q", b)
	}
	// Replacement.
	res = r.Dispatch(Invocation{ID: "2", Name: "write", Arguments: map[string]any{"path": "sub/new.txt", "content": "v2"}})
	if res.IsError {
		t.Errorf("replace: %+v", res)
	}
}

func TestEdit(t *testing.T) {
	ws := workdir(t)
	writeF(t, filepath.Join(ws, "e.txt"), "alpha\nbeta\ngamma\n")
	r := NewRegistry(ws)
	// Current schema.
	res := r.Dispatch(Invocation{ID: "1", Name: "edit", Arguments: map[string]any{"path": "e.txt", "edits": []any{map[string]any{"oldText": "beta", "newText": "BETA"}}}})
	if res.IsError {
		t.Fatalf("edit: %+v", res)
	}
	b, _ := os.ReadFile(filepath.Join(ws, "e.txt"))
	if !strings.Contains(string(b), "BETA") {
		t.Errorf("edit not applied: %q", b)
	}
	// Legacy single form.
	writeF(t, filepath.Join(ws, "l.txt"), "one two")
	res = r.Dispatch(Invocation{ID: "2", Name: "edit", Arguments: map[string]any{"path": "l.txt", "oldText": "one", "newText": "ONE"}})
	// Legacy top-level without edits[] is accepted via normalizeEdits? Our impl requires list; test legacy path.
	if res.IsError {
		t.Logf("legacy without edits[] fails as expected in strict mode: %v", res.Content[0].Text)
		// Accept either behavior but document: legacy with only oldText/newText should work.
		// Patch: our normalizeEdits handles legacy when raw is nil? It checks args["edits"] nil then legacy.
		// It should have succeeded; fail if it did not.
		t.Errorf("legacy should succeed, got %+v", res)
	}
	// Duplicate match.
	writeF(t, filepath.Join(ws, "d.txt"), "x\nx\n")
	res = r.Dispatch(Invocation{ID: "3", Name: "edit", Arguments: map[string]any{"path": "d.txt", "edits": []any{map[string]any{"oldText": "x", "newText": "y"}}}})
	if !res.IsError || !strings.Contains(res.Content[0].Text, "multiple") {
		t.Errorf("duplicate must fail: %+v", res)
	}
	// Overlapping.
	writeF(t, filepath.Join(ws, "o.txt"), "abcdef")
	res = r.Dispatch(Invocation{ID: "4", Name: "edit", Arguments: map[string]any{"path": "o.txt", "edits": []any{
		map[string]any{"oldText": "abc", "newText": "X"},
		map[string]any{"oldText": "bcd", "newText": "Y"},
	}}})
	if !res.IsError || !strings.Contains(res.Content[0].Text, "overlapping") {
		t.Errorf("overlap must fail: %+v", res)
	}
	// CRLF + BOM preserved.
	writeF(t, filepath.Join(ws, "c.txt"), "\xef\xbb\xbfline1\r\nline2\r\n")
	res = r.Dispatch(Invocation{ID: "5", Name: "edit", Arguments: map[string]any{"path": "c.txt", "edits": []any{map[string]any{"oldText": "line1", "newText": "LINE1"}}}})
	if res.IsError {
		t.Fatalf("crlf: %+v", res)
	}
	raw, _ := os.ReadFile(filepath.Join(ws, "c.txt"))
	if !strings.HasPrefix(string(raw), "\xef\xbb\xbf") || !strings.Contains(string(raw), "\r\n") {
		t.Errorf("BOM/CRLF lost: %q", raw)
	}
	// Multiple disjoint edits in one call against original.
	writeF(t, filepath.Join(ws, "m.txt"), "a1 b1 c1")
	res = r.Dispatch(Invocation{ID: "6", Name: "edit", Arguments: map[string]any{"path": "m.txt", "edits": []any{
		map[string]any{"oldText": "a1", "newText": "a2"},
		map[string]any{"oldText": "c1", "newText": "c2"},
	}}})
	if res.IsError {
		t.Fatalf("multi: %+v", res)
	}
	raw, _ = os.ReadFile(filepath.Join(ws, "m.txt"))
	if string(raw) != "a2 b1 c2" {
		t.Errorf("multi wrong: %q", raw)
	}
}

func TestBash(t *testing.T) {
	ws := workdir(t)
	r := NewRegistry(ws)
	// Success.
	res := r.Dispatch(Invocation{ID: "1", Name: "bash", Arguments: map[string]any{"command": "echo hi"}})
	if res.IsError || !strings.Contains(res.Content[0].Text, "hi") {
		t.Errorf("bash: %+v", res)
	}
	// Failure exit code.
	res = r.Dispatch(Invocation{ID: "2", Name: "bash", Arguments: map[string]any{"command": "exit 3"}})
	if !res.IsError {
		t.Errorf("non-zero must be error result")
	}
	// Timeout.
	res = r.Bash.ExecuteCtx(context.Background(), Invocation{ID: "3", Name: "bash", Arguments: map[string]any{"command": "sleep 5", "timeout": float64(1)}})
	if !res.IsError || !strings.Contains(strings.ToLower(res.Content[0].Text), "timed out") {
		t.Errorf("timeout: %+v", res)
	}
	// Cancellation stops descendants.
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan Result, 1)
	go func() {
		done <- r.Bash.ExecuteCtx(ctx, Invocation{ID: "4", Name: "bash", Arguments: map[string]any{"command": "sleep 30"}})
	}()
	time.Sleep(200 * time.Millisecond)
	cancel()
	select {
	case res := <-done:
		if !res.IsError || !strings.Contains(strings.ToLower(res.Content[0].Text), "cancelled") {
			t.Errorf("cancel: %+v", res)
		}
	case <-time.After(5 * time.Second):
		t.Errorf("cancel did not settle")
	}
	// Truncation to temp file.
	big := "for i in $(seq 1 5000); do echo line-$i; done"
	res = r.Dispatch(Invocation{ID: "5", Name: "bash", Arguments: map[string]any{"command": big}})
	if !strings.Contains(res.Content[0].Text, "truncated") {
		t.Errorf("bash truncation expected")
	}
}

func TestTruncationExact(t *testing.T) {
	s := strings.Repeat("a\n", 2000)
	tr := truncateHead(s, 2000, 50*1024)
	if tr.Truncated {
		t.Errorf("exact limit should not truncate")
	}
}
