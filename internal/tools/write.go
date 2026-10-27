package tools

import (
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
)

func encodeBase64(b []byte) string { return base64.StdEncoding.EncodeToString(b) }

// WriteTool creates parent dirs and writes complete UTF-8 content.
type WriteTool struct{ Cwd string }

func (t *WriteTool) Execute(inv Invocation) Result {
	path, ok := argString(inv.Arguments, "path")
	if !ok || path == "" {
		return errResult("write: path is required")
	}
	content, ok := argString(inv.Arguments, "content")
	if !ok {
		return errResult("write: content is required (string)")
	}
	abs := resolvePath(t.Cwd, path)
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		return errResult(fmt.Sprintf("write: %v", err))
	}
	// Dispatch checks cancellation before starting. The synchronous write must
	// settle before the agent records its result; cancellation cannot undo it.
	if err := os.WriteFile(abs, []byte(content), 0o644); err != nil {
		return errResult(fmt.Sprintf("write: %v", err))
	}
	return textResult(fmt.Sprintf("Successfully wrote to %s", path))
}
