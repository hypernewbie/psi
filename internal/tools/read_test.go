package tools

import (
	"context"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestReadOffsetAndLimitBounds: offset and limit parameter edge cases and error bounds.
func TestReadOffsetAndLimitBounds(t *testing.T) {
	dir := t.TempDir()
	p5 := filepath.Join(dir, "five_lines.txt")
	if err := os.WriteFile(p5, []byte("1\n2\n3\n4\n5\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	p1 := filepath.Join(dir, "one_line.txt")
	if err := os.WriteFile(p1, []byte("single line\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	pEmpty := filepath.Join(dir, "empty.txt")
	if err := os.WriteFile(pEmpty, []byte(""), 0o644); err != nil {
		t.Fatal(err)
	}

	tool := &ReadTool{Cwd: dir}

	invalidParams := []struct {
		name string
		args map[string]any
	}{
		{"offset 0", map[string]any{"path": "five_lines.txt", "offset": 0}},
		{"offset negative", map[string]any{"path": "five_lines.txt", "offset": -1}},
		{"offset float non-integer", map[string]any{"path": "five_lines.txt", "offset": 1.5}},
		{"offset huge float", map[string]any{"path": "five_lines.txt", "offset": 1e20}},
		{"offset NaN", map[string]any{"path": "five_lines.txt", "offset": math.NaN()}},
		{"limit 0", map[string]any{"path": "five_lines.txt", "limit": 0}},
		{"limit negative", map[string]any{"path": "five_lines.txt", "limit": -10}},
		{"limit float non-integer", map[string]any{"path": "five_lines.txt", "limit": 2.5}},
		{"limit NaN", map[string]any{"path": "five_lines.txt", "limit": math.NaN()}},
	}

	for _, tc := range invalidParams {
		t.Run(tc.name, func(t *testing.T) {
			res := tool.ExecuteCtx(context.Background(), Invocation{
				ID:        "inv",
				Name:      Read,
				Arguments: tc.args,
			})
			if !res.IsError || !strings.Contains(res.Content[0].Text, "positive safe integers") {
				t.Fatalf("expected positive safe integer error for %s: %+v", tc.name, res)
			}
		})
	}

	// Offset beyond EOF on 5-line file: offset 6
	resBeyond5 := tool.ExecuteCtx(context.Background(), Invocation{
		ID:        "inv",
		Name:      Read,
		Arguments: map[string]any{"path": "five_lines.txt", "offset": 6},
	})
	if !resBeyond5.IsError || !strings.Contains(resBeyond5.Content[0].Text, "beyond end of file") {
		t.Fatalf("expected beyond EOF for offset 6 on 5-line file: %+v", resBeyond5)
	}

	// Offset beyond EOF on 1-line file: offset 2
	resBeyond1 := tool.ExecuteCtx(context.Background(), Invocation{
		ID:        "inv",
		Name:      Read,
		Arguments: map[string]any{"path": "one_line.txt", "offset": 2},
	})
	if !resBeyond1.IsError || !strings.Contains(resBeyond1.Content[0].Text, "beyond end of file") {
		t.Fatalf("expected beyond EOF for offset 2 on 1-line file: %+v", resBeyond1)
	}

	// Offset beyond EOF on empty file: offset 2
	resBeyondEmpty := tool.ExecuteCtx(context.Background(), Invocation{
		ID:        "inv",
		Name:      Read,
		Arguments: map[string]any{"path": "empty.txt", "offset": 2},
	})
	if !resBeyondEmpty.IsError || !strings.Contains(resBeyondEmpty.Content[0].Text, "beyond end of file") {
		t.Fatalf("expected beyond EOF for offset 2 on empty file: %+v", resBeyondEmpty)
	}

	// Offset 1 on empty file: returns "(empty file)"
	resEmpty1 := tool.ExecuteCtx(context.Background(), Invocation{
		ID:        "inv",
		Name:      Read,
		Arguments: map[string]any{"path": "empty.txt", "offset": 1},
	})
	if resEmpty1.IsError || resEmpty1.Content[0].Text != "(empty file)" {
		t.Fatalf("expected (empty file), got: %+v", resEmpty1)
	}

	// Missing path argument
	resNoPath := tool.ExecuteCtx(context.Background(), Invocation{
		ID:        "inv",
		Name:      Read,
		Arguments: map[string]any{},
	})
	if !resNoPath.IsError || !strings.Contains(resNoPath.Content[0].Text, "path is required") {
		t.Fatalf("expected path is required: %+v", resNoPath)
	}
}

// TestReadCRLFAndTrailingNewline: preserves CRLF and respects absent trailing newline.
func TestReadCRLFAndTrailingNewline(t *testing.T) {
	dir := t.TempDir()
	crlfPath := filepath.Join(dir, "crlf.txt")
	contentCRLF := "alpha\r\nbeta\r\ngamma\r\n"
	if err := os.WriteFile(crlfPath, []byte(contentCRLF), 0o644); err != nil {
		t.Fatal(err)
	}

	noNewlinePath := filepath.Join(dir, "no_newline.txt")
	contentNoNewline := "line1\nline2"
	if err := os.WriteFile(noNewlinePath, []byte(contentNoNewline), 0o644); err != nil {
		t.Fatal(err)
	}

	tool := &ReadTool{Cwd: dir}

	// CRLF read
	resCRLF := tool.ExecuteCtx(context.Background(), Invocation{
		ID:        "inv",
		Name:      Read,
		Arguments: map[string]any{"path": "crlf.txt"},
	})
	if resCRLF.IsError || resCRLF.Content[0].Text != contentCRLF {
		t.Fatalf("CRLF not preserved: got %q, want %q", resCRLF.Content[0].Text, contentCRLF)
	}

	// Partial CRLF read with offset and limit
	resPartial := tool.ExecuteCtx(context.Background(), Invocation{
		ID:        "inv",
		Name:      Read,
		Arguments: map[string]any{"path": "crlf.txt", "offset": 2, "limit": 1},
	})
	if resPartial.IsError || !strings.HasPrefix(resPartial.Content[0].Text, "beta\r\n") {
		t.Fatalf("partial CRLF read failed: got %q", resPartial.Content[0].Text)
	}

	// No trailing newline read
	resNoNL := tool.ExecuteCtx(context.Background(), Invocation{
		ID:        "inv",
		Name:      Read,
		Arguments: map[string]any{"path": "no_newline.txt"},
	})
	if resNoNL.IsError || resNoNL.Content[0].Text != contentNoNewline {
		t.Fatalf("no trailing newline corrupted: got %q", resNoNL.Content[0].Text)
	}
}

// TestReadTruncationAndContinuationNotice: truncation by line count (2000) and byte cap (50 KiB).
func TestReadTruncationAndContinuationNotice(t *testing.T) {
	dir := t.TempDir()
	tool := &ReadTool{Cwd: dir}

	// 2500 lines
	lines2500 := strings.Repeat("test line\n", 2500)
	pLines := filepath.Join(dir, "lines2500.txt")
	if err := os.WriteFile(pLines, []byte(lines2500), 0o644); err != nil {
		t.Fatal(err)
	}

	resLines := tool.ExecuteCtx(context.Background(), Invocation{
		ID:        "inv",
		Name:      Read,
		Arguments: map[string]any{"path": "lines2500.txt"},
	})
	if resLines.IsError {
		t.Fatalf("read failed: %+v", resLines)
	}
	textLines := resLines.Content[0].Text
	if !strings.Contains(textLines, "Output truncated. Use offset=2001 to continue") {
		t.Fatalf("continuation notice missing or wrong in 2500 lines read: %s", textLines[len(textLines)-200:])
	}
	detailsLines, _ := resLines.Details.(map[string]any)
	if detailsLines["truncated"] != true {
		t.Fatalf("expected truncated details true, got %v", detailsLines)
	}

	// Single 60 KiB line
	bigLine := strings.Repeat("a", 60*1024) + "\n"
	pBig := filepath.Join(dir, "bigline.txt")
	if err := os.WriteFile(pBig, []byte(bigLine), 0o644); err != nil {
		t.Fatal(err)
	}

	resBig := tool.ExecuteCtx(context.Background(), Invocation{
		ID:        "inv",
		Name:      Read,
		Arguments: map[string]any{"path": "bigline.txt"},
	})
	if resBig.IsError {
		t.Fatalf("read failed: %+v", resBig)
	}
	textBig := resBig.Content[0].Text
	if !strings.Contains(textBig, "Output truncated. Use offset=1 to continue; use bash for a line larger than 50 KiB") {
		t.Fatalf("continuation notice missing in big line read: %s", textBig[len(textBig)-200:])
	}
}

// TestReadBinaryAndImageFormats: binary detection and supported/unsupported image MIME types.
func TestReadBinaryAndImageFormats(t *testing.T) {
	dir := t.TempDir()
	tool := &ReadTool{Cwd: dir}

	// Binary file with NUL byte
	pBin := filepath.Join(dir, "binary.bin")
	if err := os.WriteFile(pBin, []byte("text before\x00text after"), 0o644); err != nil {
		t.Fatal(err)
	}
	resBin := tool.ExecuteCtx(context.Background(), Invocation{
		ID:        "inv",
		Name:      Read,
		Arguments: map[string]any{"path": "binary.bin"},
	})
	if !resBin.IsError || !strings.Contains(resBin.Content[0].Text, "binary data") {
		t.Fatalf("binary file must be rejected: %+v", resBin)
	}

	// Supported image types
	for _, ext := range []string{".jpg", ".jpeg", ".png", ".gif", ".webp"} {
		pImg := filepath.Join(dir, "sample"+ext)
		if err := os.WriteFile(pImg, []byte("fake-image-bytes"), 0o644); err != nil {
			t.Fatal(err)
		}
		resImg := tool.ExecuteCtx(context.Background(), Invocation{
			ID:        "inv",
			Name:      Read,
			Arguments: map[string]any{"path": "sample" + ext},
		})
		if resImg.IsError || len(resImg.Content) != 2 || resImg.Content[1].Type != "image" {
			t.Fatalf("expected image content for %s: %+v", ext, resImg)
		}
	}

	// BMP image (Anthropic limitation)
	pBMP := filepath.Join(dir, "sample.bmp")
	_ = os.WriteFile(pBMP, []byte("bmp-bytes"), 0o644)
	resBMP := tool.ExecuteCtx(context.Background(), Invocation{
		ID:        "inv",
		Name:      Read,
		Arguments: map[string]any{"path": "sample.bmp"},
	})
	if !resBMP.IsError || !strings.Contains(resBMP.Content[0].Text, "BMP images are unsupported") {
		t.Fatalf("BMP must return unsupported notice: %+v", resBMP)
	}

	// BlockImages tool flag
	blockedTool := &ReadTool{Cwd: dir, BlockImages: true}
	resBlocked := blockedTool.ExecuteCtx(context.Background(), Invocation{
		ID:        "inv",
		Name:      Read,
		Arguments: map[string]any{"path": "sample.png"},
	})
	if !resBlocked.IsError || !strings.Contains(resBlocked.Content[0].Text, "does not support image input") {
		t.Fatalf("BlockImages must reject image: %+v", resBlocked)
	}

	// Oversized image > 3 MiB
	pOversized := filepath.Join(dir, "huge.png")
	_ = os.WriteFile(pOversized, make([]byte, 3*1024*1024+10), 0o644)
	resOversized := tool.ExecuteCtx(context.Background(), Invocation{
		ID:        "inv",
		Name:      Read,
		Arguments: map[string]any{"path": "huge.png"},
	})
	if !resOversized.IsError || !strings.Contains(resOversized.Content[0].Text, "raw-image limit") {
		t.Fatalf("oversized image must be rejected: %+v", resOversized)
	}
}

// TestReadNonRegularFileAndCancellation: reading directories and handling cancellation.
func TestReadNonRegularFileAndCancellation(t *testing.T) {
	dir := t.TempDir()
	subDir := filepath.Join(dir, "subdir")
	if err := os.Mkdir(subDir, 0o755); err != nil {
		t.Fatal(err)
	}

	tool := &ReadTool{Cwd: dir}

	// Directory read
	resDir := tool.ExecuteCtx(context.Background(), Invocation{
		ID:        "inv",
		Name:      Read,
		Arguments: map[string]any{"path": "subdir"},
	})
	if !resDir.IsError || !strings.Contains(resDir.Content[0].Text, "regular file") {
		t.Fatalf("reading directory must fail: %+v", resDir)
	}

	// Cancellation via AfterOpen hook
	pNormal := filepath.Join(dir, "normal.txt")
	_ = os.WriteFile(pNormal, []byte("line1\nline2\n"), 0o644)

	ctx, cancel := context.WithCancel(context.Background())
	cancelTool := &ReadTool{
		Cwd: dir,
		AfterOpen: func(path string) {
			cancel()
		},
	}

	resCancel := cancelTool.ExecuteCtx(ctx, Invocation{
		ID:        "inv",
		Name:      Read,
		Arguments: map[string]any{"path": "normal.txt"},
	})
	if !resCancel.IsError || !strings.Contains(resCancel.Content[0].Text, "context canceled") {
		t.Fatalf("expected context canceled result: %+v", resCancel)
	}
}
