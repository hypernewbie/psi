package tools

import (
	"context"
	"encoding/json"
	"math"
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

// TestDefinitionsGoldenSnapshot: verifies Definitions returns complete schema declarations.
func TestDefinitionsGoldenSnapshot(t *testing.T) {
	allNames := []string{Read, Write, Edit, Bash, "unknown"}
	defs := Definitions(allNames)

	if len(defs) != 4 {
		t.Fatalf("expected 4 definitions, got %d", len(defs))
	}

	names := make(map[string]bool)
	for _, d := range defs {
		names[d.Name] = true
		if d.Description == "" {
			t.Errorf("definition %s missing description", d.Name)
		}
		if d.Schema == nil {
			t.Errorf("definition %s missing schema", d.Name)
		}
	}

	for _, expected := range []string{Read, Write, Edit, Bash} {
		if !names[expected] {
			t.Errorf("missing definition for %s", expected)
		}
	}
}

// TestRegistryPrepareAndPreRegistration: verifies Registry.Prepare and pre-registered execution.
func TestRegistryPrepareAndPreRegistration(t *testing.T) {
	dir := t.TempDir()
	reg := NewRegistry(dir)

	// 1. Prepare Write
	writeInv := Invocation{
		ID:        "w-prep",
		Name:      Write,
		Arguments: map[string]any{"path": "prep.txt", "content": "prepared write"},
	}
	execWrite := reg.Prepare(writeInv)
	resWrite := execWrite(context.Background())
	if resWrite.IsError {
		t.Fatalf("prepared write failed: %+v", resWrite)
	}
	data, _ := os.ReadFile(filepath.Join(dir, "prep.txt"))
	if string(data) != "prepared write" {
		t.Fatalf("content mismatch: %q", string(data))
	}

	// 2. Prepare Edit
	editInv := Invocation{
		ID:   "e-prep",
		Name: Edit,
		Arguments: map[string]any{
			"path": "prep.txt",
			"edits": []any{
				map[string]any{"oldText": "prepared write", "newText": "prepared edit"},
			},
		},
	}
	execEdit := reg.Prepare(editInv)
	resEdit := execEdit(context.Background())
	if resEdit.IsError {
		t.Fatalf("prepared edit failed: %+v", resEdit)
	}
	dataEdit, _ := os.ReadFile(filepath.Join(dir, "prep.txt"))
	if string(dataEdit) != "prepared edit" {
		t.Fatalf("content mismatch: %q", string(dataEdit))
	}

	// 3. Prepare Read and Bash (fallback to DispatchCtx)
	readInv := Invocation{ID: "r-prep", Name: Read, Arguments: map[string]any{"path": "prep.txt"}}
	execRead := reg.Prepare(readInv)
	resRead := execRead(context.Background())
	if resRead.IsError || !strings.Contains(resRead.Content[0].Text, "prepared edit") {
		t.Fatalf("prepared read failed: %+v", resRead)
	}

	bashInv := Invocation{ID: "b-prep", Name: Bash, Arguments: map[string]any{"command": "echo prepared-bash"}}
	execBash := reg.Prepare(bashInv)
	resBash := execBash(context.Background())
	if resBash.IsError || !strings.Contains(resBash.Content[0].Text, "prepared-bash") {
		t.Fatalf("prepared bash failed: %+v", resBash)
	}

	// 4. Prepare cancellation before start
	ctxCancel, cancel := context.WithCancel(context.Background())
	cancel()
	execCancelWrite := reg.Prepare(Invocation{
		ID:        "w-can",
		Name:      Write,
		Arguments: map[string]any{"path": "cancel.txt", "content": "bad"},
	})
	resCancel := execCancelWrite(ctxCancel)
	if !resCancel.IsError || !strings.Contains(resCancel.Content[0].Text, "cancelled") {
		t.Fatalf("expected cancelled result: %+v", resCancel)
	}

	// 5. Prepare invalid arguments
	execBadWrite := reg.Prepare(Invocation{ID: "bad-w", Name: Write, Arguments: map[string]any{}})
	resBadWrite := execBadWrite(context.Background())
	if !resBadWrite.IsError {
		t.Fatal("expected error on bad write prepare")
	}

	execBadEdit := reg.Prepare(Invocation{ID: "bad-e", Name: Edit, Arguments: map[string]any{}})
	resBadEdit := execBadEdit(context.Background())
	if !resBadEdit.IsError {
		t.Fatal("expected error on bad edit prepare")
	}
}

// TestPathsResolveAndTruncate: coverage for resolveToCwd, resolvePath, truncateHead.
func TestPathsResolveAndTruncate(t *testing.T) {
	home, _ := os.UserHomeDir()
	cwd := "/my/project/workspace"

	// resolveToCwd
	if res := resolveToCwd("", cwd); res != cwd {
		t.Errorf("empty path: got %q", res)
	}
	if res := resolveToCwd("~", cwd); res != home {
		t.Errorf("~: got %q", res)
	}
	if res := resolveToCwd("~/foo/bar", cwd); res != filepath.Join(home, "foo/bar") {
		t.Errorf("~/foo/bar: got %q", res)
	}
	if res := resolveToCwd("/abs/path", cwd); res != "/abs/path" {
		t.Errorf("abs: got %q", res)
	}

	// resolvePath
	if res := resolvePath(cwd, ""); res != cwd {
		t.Errorf("resolvePath empty: got %q", res)
	}
	if res := resolvePath(cwd, "~"); res != home {
		t.Errorf("resolvePath ~: got %q", res)
	}
	if res := resolvePath(cwd, "~/sub"); res != filepath.Join(home, "sub") {
		t.Errorf("resolvePath ~/sub: got %q", res)
	}
	if res := resolvePath(cwd, "/abs"); res != "/abs" {
		t.Errorf("resolvePath /abs: got %q", res)
	}
	if res := resolvePath(cwd, "rel/path"); res != filepath.Join(cwd, "rel/path") {
		t.Errorf("resolvePath rel: got %q", res)
	}

	// truncateHead
	// Default zero params
	trDefault := truncateHead("line1\nline2\n", 0, 0)
	if trDefault.Truncated || trDefault.TotalLines != 2 {
		t.Errorf("default truncateHead failed: %+v", trDefault)
	}

	// Truncate by bytes where first line alone exceeds byte limit
	trFirstBig := truncateHead("first line is way too long\nsecond", 10, 5)
	if !trFirstBig.Truncated || trFirstBig.TruncatedBy != "bytes" || trFirstBig.Content != "" {
		t.Errorf("first line exceeds limit failed: %+v", trFirstBig)
	}

	// Truncate by lines
	trLines := truncateHead("1\n2\n3\n4\n5\n", 3, 1000)
	if !trLines.Truncated || trLines.TruncatedBy != "lines" || trLines.OutputLines != 3 {
		t.Errorf("truncate lines failed: %+v", trLines)
	}

	// Truncate by bytes with multiple kept lines
	trBytes := truncateHead("1111\n2222\n3333\n4444\n", 10, 12)
	if !trBytes.Truncated || trBytes.TruncatedBy != "bytes" {
		t.Errorf("truncate bytes failed: %+v", trBytes)
	}

	// splitLines on empty
	if lines := splitLines(""); len(lines) != 0 {
		t.Errorf("expected empty slice, got %v", lines)
	}
}

// TestFormatSize: coverage for formatSize helper.
func TestFormatSize(t *testing.T) {
	if s := formatSize(500); s != "500B" {
		t.Errorf("got %q, want 500B", s)
	}
	if s := formatSize(2048); s != "2.0KB" {
		t.Errorf("got %q, want 2.0KB", s)
	}
	if s := formatSize(5 * 1024 * 1024); s != "5.0MB" {
		t.Errorf("got %q, want 5.0MB", s)
	}
}

// TestContinuationNotice: coverage for continuationNotice.
func TestContinuationNotice(t *testing.T) {
	notice := continuationNotice("my/file.go")
	if !strings.Contains(notice, "my/file.go") {
		t.Errorf("continuationNotice failed: %q", notice)
	}
}

// TestArgFloatAndArgString: test argument parsing edge cases.
func TestArgFloatAndArgString(t *testing.T) {
	args := map[string]any{
		"float":  3.14,
		"int":    42,
		"int64":  int64(100),
		"json":   json.Number("55"),
		"string": "hello",
	}

	if f, ok := argFloat(args, "float"); !ok || f != 3.14 {
		t.Errorf("argFloat float failed: %v %v", f, ok)
	}
	if f, ok := argFloat(args, "int"); !ok || f != 42 {
		t.Errorf("argFloat int failed: %v %v", f, ok)
	}
	if f, ok := argFloat(args, "int64"); !ok || f != 100 {
		t.Errorf("argFloat int64 failed: %v %v", f, ok)
	}
	if f, ok := argFloat(args, "json"); !ok || f != 55 {
		t.Errorf("argFloat json failed: %v %v", f, ok)
	}
	if _, ok := argFloat(args, "missing"); ok {
		t.Error("argFloat missing should be false")
	}
	if _, ok := argFloat(args, "string"); ok {
		t.Error("argFloat string should be false")
	}

	if s, ok := argString(args, "string"); !ok || s != "hello" {
		t.Errorf("argString string failed: %v %v", s, ok)
	}
	if _, ok := argString(args, "missing"); ok {
		t.Error("argString missing should be false")
	}
}

// TestSummaryJSONNumberAndNumberValue: coverage for jsonNumber and numberValue.
func TestSummaryJSONNumberAndNumberValue(t *testing.T) {
	// jsonNumber with string-encoded number: "\"42.5\""
	if val, ok := jsonNumber([]byte(`"42.5"`)); !ok || val != 42.5 {
		t.Errorf("jsonNumber string failed: %v %v", val, ok)
	}
	if _, ok := jsonNumber([]byte(`"not-a-number"`)); ok {
		t.Error("jsonNumber invalid string should fail")
	}
	if _, ok := jsonNumber([]byte(`null`)); ok {
		t.Error("jsonNumber null should fail")
	}
	if _, ok := jsonNumber([]byte(strings.Repeat("9", 200))); ok {
		t.Error("jsonNumber oversized should fail")
	}

	// numberValue
	if val, ok := numberValue(float32(12.5)); !ok || val != 12.5 {
		t.Errorf("numberValue float32: %v", val)
	}
	if val, ok := numberValue(" 99.5 "); !ok || val != 99.5 {
		t.Errorf("numberValue string: %v", val)
	}
	if _, ok := numberValue(struct{}{}); ok {
		t.Error("numberValue struct should fail")
	}

	// SummarizeArguments with unknown tool name or nil args
	if s := SummarizeArguments("unknown", nil, "", ""); s.Available {
		t.Error("unknown tool should have Available=false")
	}
	if s := SummarizeArguments("read", nil, "", ""); s.Available {
		t.Error("nil args should have Available=false")
	}

	// SummarizeArguments with NaN/Inf numbers
	sNaN := SummarizeArguments("read", map[string]any{"path": "f.go", "offset": math.NaN()}, "", "")
	_ = sNaN
}

// TestBashShellEnv: coverage for PSI_SHELL env var.
func TestBashShellEnv(t *testing.T) {
	t.Setenv("PSI_SHELL", "/bin/sh")
	tool := &BashTool{}
	if s := tool.shell(); s != "/bin/sh" {
		t.Errorf("got %q, want /bin/sh", s)
	}
}
