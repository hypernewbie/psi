package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestEditMultipleReplacements: multiple disjoint edits applied in reverse
// start-offset order so earlier modifications do not corrupt subsequent coordinates.
func TestEditMultipleReplacements(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "multi.txt")
	initial := "one alpha two beta three gamma four delta"
	if err := os.WriteFile(p, []byte(initial), 0o644); err != nil {
		t.Fatal(err)
	}

	tool := &EditTool{Cwd: dir}
	res := tool.ExecuteCtx(context.Background(), Invocation{
		ID:   "e-multi",
		Name: Edit,
		Arguments: map[string]any{
			"path": "multi.txt",
			"edits": []any{
				map[string]any{"oldText": "one", "newText": "1"},
				map[string]any{"oldText": "two", "newText": "2"},
				map[string]any{"oldText": "three", "newText": "3"},
				map[string]any{"oldText": "four", "newText": "4"},
			},
		},
	})

	if res.IsError {
		t.Fatalf("multi-edit failed: %+v", res)
	}

	data, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	expected := "1 alpha 2 beta 3 gamma 4 delta"
	if string(data) != expected {
		t.Fatalf("content mismatch:\ngot:  %q\nwant: %q", string(data), expected)
	}
}

// TestEditCRLFAndBOMPreservation: preserving Windows line endings and UTF-8 BOM.
func TestEditCRLFAndBOMPreservation(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "crlf_bom.txt")
	const bom = "\xef\xbb\xbf"
	initial := bom + "line1\r\nline2\r\nline3\r\n"
	if err := os.WriteFile(p, []byte(initial), 0o644); err != nil {
		t.Fatal(err)
	}

	tool := &EditTool{Cwd: dir}
	res := tool.ExecuteCtx(context.Background(), Invocation{
		ID:   "e-bom",
		Name: Edit,
		Arguments: map[string]any{
			"path": "crlf_bom.txt",
			"edits": []any{
				map[string]any{"oldText": "line2", "newText": "EDITED_LINE_2"},
			},
		},
	})

	if res.IsError {
		t.Fatalf("edit failed: %+v", res)
	}

	data, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}

	content := string(data)
	if !strings.HasPrefix(content, bom) {
		t.Fatalf("UTF-8 BOM was lost: %q", content[:10])
	}
	if !strings.Contains(content, "EDITED_LINE_2\r\n") {
		t.Fatalf("CRLF line endings lost: %q", content)
	}
	expected := bom + "line1\r\nEDITED_LINE_2\r\nline3\r\n"
	if content != expected {
		t.Fatalf("mismatch:\ngot:  %q\nwant: %q", content, expected)
	}
}

// TestEditZeroMatches: oldText not found in the file.
func TestEditZeroMatches(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "f.txt")
	_ = os.WriteFile(p, []byte("hello world\n"), 0o644)

	tool := &EditTool{Cwd: dir}
	res := tool.ExecuteCtx(context.Background(), Invocation{
		ID:   "e-zero",
		Name: Edit,
		Arguments: map[string]any{
			"path": "f.txt",
			"edits": []any{
				map[string]any{"oldText": "nonexistent text", "newText": "replacement"},
			},
		},
	})

	if !res.IsError || !strings.Contains(res.Content[0].Text, "oldText not found (edit 0)") {
		t.Fatalf("expected oldText not found error: %+v", res)
	}
}

// TestEditMultipleMatchesAndAmbiguity: oldText matches multiple times.
func TestEditMultipleMatchesAndAmbiguity(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "ambig.txt")
	_ = os.WriteFile(p, []byte("repeat repeat repeat\n"), 0o644)

	tool := &EditTool{Cwd: dir}

	// Multiple discrete matches
	res1 := tool.ExecuteCtx(context.Background(), Invocation{
		ID:   "e-ambig",
		Name: Edit,
		Arguments: map[string]any{
			"path": "ambig.txt",
			"edits": []any{
				map[string]any{"oldText": "repeat", "newText": "once"},
			},
		},
	})
	if !res1.IsError || !strings.Contains(res1.Content[0].Text, "matches multiple locations") {
		t.Fatalf("expected multiple locations error: %+v", res1)
	}

	// Overlapping substring matches (e.g. "aa" in "aaa")
	pOverlap := filepath.Join(dir, "overlap_str.txt")
	_ = os.WriteFile(pOverlap, []byte("aaaa"), 0o644)
	res2 := tool.ExecuteCtx(context.Background(), Invocation{
		ID:   "e-overlap",
		Name: Edit,
		Arguments: map[string]any{
			"path": "overlap_str.txt",
			"edits": []any{
				map[string]any{"oldText": "aa", "newText": "b"},
			},
		},
	})
	if !res2.IsError || !strings.Contains(res2.Content[0].Text, "matches multiple locations") {
		t.Fatalf("expected overlapping occurrence multiple locations error: %+v", res2)
	}
}

// TestEditOverlappingEditsArray: edits in array overlap with each other.
func TestEditOverlappingEditsArray(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "overlap_edits.txt")
	_ = os.WriteFile(p, []byte("abcdefghijk"), 0o644)

	tool := &EditTool{Cwd: dir}

	// Partial overlap
	res := tool.ExecuteCtx(context.Background(), Invocation{
		ID:   "e-over",
		Name: Edit,
		Arguments: map[string]any{
			"path": "overlap_edits.txt",
			"edits": []any{
				map[string]any{"oldText": "cdef", "newText": "X"},
				map[string]any{"oldText": "efgh", "newText": "Y"},
			},
		},
	})
	if !res.IsError || !strings.Contains(res.Content[0].Text, "overlapping edits") {
		t.Fatalf("expected overlapping edits error: %+v", res)
	}

	// Nested containment
	resNest := tool.ExecuteCtx(context.Background(), Invocation{
		ID:   "e-nest",
		Name: Edit,
		Arguments: map[string]any{
			"path": "overlap_edits.txt",
			"edits": []any{
				map[string]any{"oldText": "bcdef", "newText": "OUTER"},
				map[string]any{"oldText": "cde", "newText": "INNER"},
			},
		},
	})
	if !resNest.IsError || !strings.Contains(resNest.Content[0].Text, "overlapping edits") {
		t.Fatalf("expected nested overlapping edits error: %+v", resNest)
	}
}

// TestEditEmptyOldText: oldText must be non-empty.
func TestEditEmptyOldText(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "f.txt")
	_ = os.WriteFile(p, []byte("hello\n"), 0o644)

	tool := &EditTool{Cwd: dir}
	res := tool.ExecuteCtx(context.Background(), Invocation{
		ID:   "e-empty",
		Name: Edit,
		Arguments: map[string]any{
			"path": "f.txt",
			"edits": []any{
				map[string]any{"oldText": "", "newText": "something"},
			},
		},
	})
	if !res.IsError || (!strings.Contains(res.Content[0].Text, "requires oldText and newText strings") && !strings.Contains(res.Content[0].Text, "must be non-empty")) {
		t.Fatalf("expected non-empty oldText error: %+v", res)
	}
}

// TestEditOldTextEqualsNewText: no-op edit preserves file and succeeds.
func TestEditOldTextEqualsNewText(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "noop.txt")
	initial := "exact original text"
	_ = os.WriteFile(p, []byte(initial), 0o644)

	tool := &EditTool{Cwd: dir}
	res := tool.ExecuteCtx(context.Background(), Invocation{
		ID:   "e-noop",
		Name: Edit,
		Arguments: map[string]any{
			"path": "noop.txt",
			"edits": []any{
				map[string]any{"oldText": initial, "newText": initial},
			},
		},
	})
	if res.IsError {
		t.Fatalf("no-op edit should succeed: %+v", res)
	}
	data, _ := os.ReadFile(p)
	if string(data) != initial {
		t.Fatalf("file content changed: %q", string(data))
	}
}

// TestEditMakesFileEmpty: replacement makes the entire file empty.
func TestEditMakesFileEmpty(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "to_empty.txt")
	_ = os.WriteFile(p, []byte("delete everything"), 0o644)

	tool := &EditTool{Cwd: dir}
	res := tool.ExecuteCtx(context.Background(), Invocation{
		ID:   "e-empty",
		Name: Edit,
		Arguments: map[string]any{
			"path": "to_empty.txt",
			"edits": []any{
				map[string]any{"oldText": "delete everything", "newText": ""},
			},
		},
	})
	if res.IsError {
		t.Fatalf("edit to empty failed: %+v", res)
	}
	data, _ := os.ReadFile(p)
	if len(data) != 0 {
		t.Fatalf("expected 0 bytes, got %d: %q", len(data), string(data))
	}
}

// TestEditReadOnlyFileFails: edit on read-only file fails gracefully.
func TestEditReadOnlyFileFails(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores file read-only permissions")
	}
	dir := t.TempDir()
	p := filepath.Join(dir, "readonly.txt")
	_ = os.WriteFile(p, []byte("cannot change me"), 0o444)
	t.Cleanup(func() { _ = os.Chmod(p, 0o644) })

	tool := &EditTool{Cwd: dir}
	res := tool.ExecuteCtx(context.Background(), Invocation{
		ID:   "e-ro",
		Name: Edit,
		Arguments: map[string]any{
			"path": "readonly.txt",
			"edits": []any{
				map[string]any{"oldText": "cannot change me", "newText": "changed"},
			},
		},
	})
	if !res.IsError {
		t.Fatalf("edit on read-only file must fail: %+v", res)
	}
}

// TestEditParameterNormalization: normalization of various argument encodings.
func TestEditParameterNormalization(t *testing.T) {
	dir := t.TempDir()
	tool := &EditTool{Cwd: dir}

	// 1. JSON string form for edits
	pJSON := filepath.Join(dir, "json.txt")
	_ = os.WriteFile(pJSON, []byte("original"), 0o644)
	resJSON := tool.ExecuteCtx(context.Background(), Invocation{
		ID:   "1",
		Name: Edit,
		Arguments: map[string]any{
			"path":  "json.txt",
			"edits": `[{"oldText":"original","newText":"json-edited"}]`,
		},
	})
	if resJSON.IsError {
		t.Fatalf("JSON string edits failed: %+v", resJSON)
	}
	dataJSON, _ := os.ReadFile(pJSON)
	if string(dataJSON) != "json-edited" {
		t.Fatalf("got %q", string(dataJSON))
	}

	// 2. Single object form for edits
	pObj := filepath.Join(dir, "obj.txt")
	_ = os.WriteFile(pObj, []byte("original"), 0o644)
	resObj := tool.ExecuteCtx(context.Background(), Invocation{
		ID:   "2",
		Name: Edit,
		Arguments: map[string]any{
			"path":  "obj.txt",
			"edits": map[string]any{"oldText": "original", "newText": "obj-edited"},
		},
	})
	if resObj.IsError {
		t.Fatalf("single object edits failed: %+v", resObj)
	}
	dataObj, _ := os.ReadFile(pObj)
	if string(dataObj) != "obj-edited" {
		t.Fatalf("got %q", string(dataObj))
	}

	// 3. Array of map[string]any
	pMapArr := filepath.Join(dir, "maparr.txt")
	_ = os.WriteFile(pMapArr, []byte("hello"), 0o644)
	resMapArr := tool.ExecuteCtx(context.Background(), Invocation{
		ID:   "3",
		Name: Edit,
		Arguments: map[string]any{
			"path": "maparr.txt",
			"edits": []map[string]any{
				{"oldText": "hello", "newText": "world"},
			},
		},
	})
	if resMapArr.IsError {
		t.Fatalf("[]map[string]any failed: %+v", resMapArr)
	}

	// 4. Missing path argument
	resNoPath := tool.ExecuteCtx(context.Background(), Invocation{
		ID:        "4",
		Name:      Edit,
		Arguments: map[string]any{"edits": []any{map[string]any{"oldText": "a", "newText": "b"}}},
	})
	if !resNoPath.IsError || !strings.Contains(resNoPath.Content[0].Text, "path is required") {
		t.Fatalf("expected path required: %+v", resNoPath)
	}

	// 5. Missing edits entirely
	resNoEdits := tool.ExecuteCtx(context.Background(), Invocation{
		ID:        "5",
		Name:      Edit,
		Arguments: map[string]any{"path": "f.txt"},
	})
	if !resNoEdits.IsError || !strings.Contains(resNoEdits.Content[0].Text, "edits must contain at least one replacement") {
		t.Fatalf("expected at least one replacement error: %+v", resNoEdits)
	}

	// 6. Malformed edit items
	resBadItem := tool.ExecuteCtx(context.Background(), Invocation{
		ID:        "6",
		Name:      Edit,
		Arguments: map[string]any{"path": "f.txt", "edits": []any{"not-a-map"}},
	})
	if !resBadItem.IsError || !strings.Contains(resBadItem.Content[0].Text, "must be an object") {
		t.Fatalf("expected object error: %+v", resBadItem)
	}

	resMissingFields := tool.ExecuteCtx(context.Background(), Invocation{
		ID:        "7",
		Name:      Edit,
		Arguments: map[string]any{"path": "f.txt", "edits": []any{map[string]any{"oldText": "only"}}},
	})
	if !resMissingFields.IsError || !strings.Contains(resMissingFields.Content[0].Text, "requires oldText and newText strings") {
		t.Fatalf("expected requires oldText and newText: %+v", resMissingFields)
	}
}
