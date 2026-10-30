package tools

import (
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
)

func encodeBase64(b []byte) string { return base64.StdEncoding.EncodeToString(b) }

// WriteTool creates parent dirs and writes complete UTF-8 content.
type WriteTool struct {
	Cwd string
	// Mutations serializes writes and edits per resolved file. Nil runs the
	// body directly (single-tool tests).
	Mutations *MutationQueue
	// MkdirAll/WriteFile seams admit deterministic cancellation schedules.
	MkdirAll  func(path string, perm os.FileMode) error
	WriteFile func(path string, data []byte, perm os.FileMode) error
	// AfterMkdir runs between directory creation and the file write.
	AfterMkdir func()
}

func (t *WriteTool) doMkdirAll(dir string) error {
	if t.MkdirAll != nil {
		return t.MkdirAll(dir, 0o777)
	}
	return os.MkdirAll(dir, 0o777)
}

func (t *WriteTool) doWriteFile(abs, content string) error {
	if t.WriteFile != nil {
		return t.WriteFile(abs, []byte(content), 0o666)
	}
	// Ordinary new tool files follow the Node default (0666 before umask).
	return os.WriteFile(abs, []byte(content), 0o666)
}

// ExecuteCtx checks cancellation between every filesystem step (F06): before
// mkdir, after mkdir, and after the write. A cancelled write cannot be
// rolled back, so the result reports cancellation truthfully without
// claiming the file stayed unchanged. Same-file ordering is the shared
// mutation queue's; the body runs inside its slot and never releases a
// successor early.
func (t *WriteTool) ExecuteCtx(ctx context.Context, inv Invocation) Result {
	if err := ctx.Err(); err != nil {
		return errResult(fmt.Sprintf("write: cancelled before start: %v", err))
	}
	path, ok := argString(inv.Arguments, "path")
	if !ok || path == "" {
		return errResult("write: path is required")
	}
	content, ok := argString(inv.Arguments, "content")
	if !ok {
		return errResult("write: content is required (string)")
	}
	return t.Mutations.withSlot(t.Cwd, path, func() Result {
		return t.writeChecked(ctx, path, content)
	})
}

func (t *WriteTool) writeChecked(ctx context.Context, path, content string) Result {
	if err := ctx.Err(); err != nil {
		return errResult(fmt.Sprintf("write: cancelled while waiting, before mkdir: %v", err))
	}
	abs := resolvePath(t.Cwd, path)
	if err := t.doMkdirAll(filepath.Dir(abs)); err != nil {
		return errResult(fmt.Sprintf("write: %v", err))
	}
	if err := ctx.Err(); err != nil {
		return errResult(fmt.Sprintf("write: cancelled after mkdir, before write (no file change by this call): %v", err))
	}
	if t.AfterMkdir != nil {
		t.AfterMkdir()
	}
	if err := ctx.Err(); err != nil {
		return errResult(fmt.Sprintf("write: cancelled before write (no file change by this call): %v", err))
	}
	if err := t.doWriteFile(abs, content); err != nil {
		return errResult(fmt.Sprintf("write: %v", err))
	}
	if err := ctx.Err(); err != nil {
		return errResult(fmt.Sprintf("write: cancelled after write completed (file was modified): %v", err))
	}
	return textResult(fmt.Sprintf("Successfully wrote to %s", path))
}
