package tools

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"strings"
)

type ReadTool struct {
	Cwd         string
	BlockImages bool
}

var imageExts = map[string]string{
	".jpg": "image/jpeg", ".jpeg": "image/jpeg", ".png": "image/png",
	".gif": "image/gif", ".webp": "image/webp", ".bmp": "image/bmp",
}

func (t *ReadTool) Execute(inv Invocation) Result { return t.ExecuteCtx(context.Background(), inv) }

func (t *ReadTool) ExecuteCtx(ctx context.Context, inv Invocation) Result {
	path, ok := argString(inv.Arguments, "path")
	if !ok || path == "" {
		return errResult("read: path is required")
	}
	offset, limit := 1, DefaultMaxLines
	for name, target := range map[string]*int{"offset": &offset, "limit": &limit} {
		if _, present := inv.Arguments[name]; !present {
			continue
		}
		f, ok := argFloat(inv.Arguments, name)
		if !ok || !isFinite(f) || f < 1 || f > 1<<53-1 || math.Trunc(f) != f {
			return errResult("read: offset and limit must be positive safe integers")
		}
		*target = int(f)
	}
	abs := resolvePath(t.Cwd, path)
	fi, err := os.Stat(abs)
	if err != nil {
		return errResult(fmt.Sprintf("read: %v", err))
	}
	if !fi.Mode().IsRegular() {
		return errResult("read: path must be a regular file")
	}
	if mime, ok := imageExts[strings.ToLower(filepath.Ext(abs))]; ok {
		if t.BlockImages {
			return errResult("read: the selected model does not support image input")
		}
		if mime == "image/bmp" {
			return errResult("read: BMP images are unsupported by the Anthropic provider; convert to PNG first")
		}
		return t.readImage(abs, mime)
	}
	f, err := os.Open(abs)
	if err != nil {
		return errResult(fmt.Sprintf("read: %v", err))
	}
	defer f.Close()
	br := bufio.NewReader(f)
	var out strings.Builder
	lineNumber, kept := 1, 0
	truncated := false
	for {
		if err := ctx.Err(); err != nil {
			return errResult(err.Error())
		}
		part, err := br.ReadSlice('\n')
		if err != nil && err != io.EOF && err != bufio.ErrBufferFull {
			return errResult(fmt.Sprintf("read: %v", err))
		}
		if lineNumber >= offset {
			if !isText(part) {
				return errResult("read: file contains binary data")
			}
			remaining := DefaultMaxBytes - out.Len()
			if len(part) > remaining {
				truncated = true
				break
			}
			out.Write(part)
		}
		if err != bufio.ErrBufferFull && len(part) > 0 {
			if lineNumber >= offset {
				kept++
			}
			lineNumber++
		}
		if err == io.EOF {
			break
		}
		if kept >= min(limit, DefaultMaxLines) {
			_, peekErr := br.Peek(1)
			truncated = peekErr != io.EOF
			break
		}
	}
	if out.Len() == 0 && !truncated {
		if offset > lineNumber {
			return errResult(fmt.Sprintf("read: offset %d is beyond end of file", offset))
		}
		return textResult("(empty file)")
	}
	text := out.String()
	if truncated {
		text += fmt.Sprintf("\n\n[Output truncated. Use offset=%d to continue; use bash for a line larger than 50 KiB.]", offset+kept)
	}
	return Result{Content: []Block{{Type: "text", Text: text}}, Details: map[string]any{"truncated": truncated}}
}

func (t *ReadTool) readImage(abs, mime string) Result {
	// Base64 expands by 4/3. Keep new image payloads below Anthropic's 5 MiB bound.
	const maxImageBytes = 3 * 1024 * 1024
	f, err := os.Open(abs)
	if err != nil {
		return errResult(fmt.Sprintf("read: %v", err))
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxImageBytes+1))
	if err != nil {
		return errResult(fmt.Sprintf("read: %v", err))
	}
	if len(data) > maxImageBytes {
		return errResult("read: image exceeds the 3 MiB raw-image limit; resize it first")
	}
	return Result{Content: []Block{{Type: "text", Text: fmt.Sprintf("Read image file [%s]", mime)}, {Type: "image", Data: encodeBase64(data), MimeType: mime}}}
}

func isText(data []byte) bool {
	for _, b := range data {
		if b == 0 {
			return false
		}
	}
	return true
}
