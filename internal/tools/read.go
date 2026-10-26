package tools

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ReadTool implements text reads with offset/limit plus a bounded image subset.
type ReadTool struct{ Cwd string }

var imageExts = map[string]string{
	".jpg": "image/jpeg", ".jpeg": "image/jpeg", ".png": "image/png",
	".gif": "image/gif", ".webp": "image/webp", ".bmp": "image/bmp",
}

func (t *ReadTool) Execute(inv Invocation) Result {
	path, ok := argString(inv.Arguments, "path")
	if !ok || path == "" {
		return errResult("read: path is required")
	}
	abs := resolvePath(t.Cwd, path)
	// offset/limit are 1-indexed lines.
	var offset, limit int
	if f, ok := argFloat(inv.Arguments, "offset"); ok {
		offset = int(f)
	}
	if f, ok := argFloat(inv.Arguments, "limit"); ok {
		limit = int(f)
	}
	if offset < 0 || limit < 0 {
		return errResult("read: offset and limit must be >= 0")
	}
	fi, err := os.Stat(abs)
	if err != nil {
		if os.IsNotExist(err) {
			return errResult(fmt.Sprintf("read: file does not exist: %s", path))
		}
		return errResult(fmt.Sprintf("read: %v", err))
	}
	if fi.IsDir() {
		return errResult(fmt.Sprintf("read: not a file: %s", path))
	}
	ext := strings.ToLower(filepath.Ext(abs))
	if mime, isImg := imageExts[ext]; isImg {
		return t.readImage(abs, mime)
	}
	return t.readText(abs, path, offset, limit)
}

func (t *ReadTool) readText(abs, displayPath string, offset, limit int) Result {
	data, err := os.ReadFile(abs)
	if err != nil {
		return errResult(fmt.Sprintf("read: %v", err))
	}
	if len(data) == 0 {
		return textResult("(empty file)")
	}
	if !isText(data) {
		return errResult(fmt.Sprintf("read: not a text file: %s", displayPath))
	}
	text := string(data)
	lines := splitLines(text)
	total := len(lines)
	start := 0
	if offset > 0 {
		start = offset - 1
		if start > total {
			start = total
		}
	}
	end := total
	if limit > 0 {
		end = start + limit
		if end > total {
			end = total
		}
	}
	slice := strings.Join(lines[start:end], "\n")
	if strings.HasSuffix(text, "\n") && end == total && len(slice) > 0 {
		slice += "\n"
	}
	// Apply Pi byte/line truncation to the slice (bounded output).
	tr := truncateHead(slice, DefaultMaxLines, DefaultMaxBytes)
	out := tr.Content
	if tr.Truncated {
		out += fmt.Sprintf("\n\n[%s]", continuationNotice(displayPath))
	}
	// If sliced (offset/limit), note continuation when more lines remain.
	if end < total && !tr.Truncated {
		out += fmt.Sprintf("\n\n[%s]", continuationNotice(displayPath))
	}
	return Result{Content: []Block{{Type: "text", Text: out}}, Details: map[string]any{"truncated": tr.Truncated}}
}

func (t *ReadTool) readImage(abs, mime string) Result {
	// Bounded subset: 10 MiB cap, supported formats only. No terminal display;
	// image data goes to the model, terminal sees only a note.
	const maxImageBytes = 10 * 1024 * 1024
	fi, err := os.Stat(abs)
	if err != nil {
		return errResult(fmt.Sprintf("read: %v", err))
	}
	if fi.Size() > maxImageBytes {
		return errResult(fmt.Sprintf("read: image exceeds 10 MiB limit: %s", abs))
	}
	data, err := os.ReadFile(abs)
	if err != nil {
		return errResult(fmt.Sprintf("read: %v", err))
	}
	// Capability: BMP is decoded but many models reject it; keep as compatible
	// block and let the provider layer refuse with an actionable error.
	return Result{Content: []Block{
		{Type: "text", Text: fmt.Sprintf("Read image file [%s]", mime)},
		{Type: "image", Data: encodeBase64(data), MimeType: mime},
	}}
}

func isText(data []byte) bool {
	// NUL byte => binary. Otherwise allow valid UTF-8 (with lenient fallback).
	for _, b := range data {
		if b == 0 {
			return false
		}
	}
	return true
}
