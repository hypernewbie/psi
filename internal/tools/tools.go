// Package tools implements the four built-in tools (Phase 7).
//
// Registry dispatches read/write/edit/bash sequentially in the first release.
// Shared path/output behavior, truncation, and privacy rules live here.
// Reference: temp/pi-mono/packages/coding-agent/src/core/tools/{read,write,edit,bash,truncate,path-utils}.ts
package tools

import (
	"context"
	"encoding/json"
	"fmt"
)

// Names in the supported subset.
const (
	Read  = "read"
	Write = "write"
	Edit  = "edit"
	Bash  = "bash"
)

// Result is the model-facing tool result.
type Result struct {
	Content []Block
	Details any
	IsError bool
}

type Block struct {
	Type     string // text|image
	Text     string
	Data     string // base64 for images
	MimeType string
}

// Invocation is one validated tool call.
type Invocation struct {
	ID        string
	Name      string
	Arguments map[string]any
}

// Registry dispatches known tools.
type Registry struct {
	Cwd   string
	Read  *ReadTool
	Write *WriteTool
	Edit  *EditTool
	Bash  *BashTool
}

func NewRegistry(cwd string) *Registry {
	return &Registry{Cwd: cwd, Read: &ReadTool{Cwd: cwd}, Write: &WriteTool{Cwd: cwd}, Edit: &EditTool{Cwd: cwd}, Bash: &BashTool{Cwd: cwd}}
}

// Dispatch runs calls sequentially in assistant order.
func (r *Registry) Dispatch(inv Invocation) Result {
	return r.DispatchCtx(context.Background(), inv)
}

// DispatchCtx propagates cancellation to shell processes and prevents new
// filesystem operations from starting after cancellation.
func (r *Registry) DispatchCtx(ctx context.Context, inv Invocation) Result {
	if err := ctx.Err(); err != nil {
		return errResult(err.Error())
	}
	switch inv.Name {
	case Read:
		return r.Read.ExecuteCtx(ctx, inv)
	case Write:
		return r.Write.Execute(inv)
	case Edit:
		return r.Edit.Execute(inv)
	case Bash:
		return r.Bash.ExecuteCtx(ctx, inv)
	default:
		return Result{Content: []Block{{Type: "text", Text: fmt.Sprintf("unknown tool %q: first release supports read, write, edit, bash only", inv.Name)}}, IsError: true}
	}
}

func textResult(s string) Result { return Result{Content: []Block{{Type: "text", Text: s}}} }
func errResult(s string) Result {
	return Result{Content: []Block{{Type: "text", Text: s}}, IsError: true}
}

func argString(args map[string]any, key string) (string, bool) {
	v, ok := args[key]
	if !ok {
		return "", false
	}
	s, ok := v.(string)
	return s, ok
}

func argFloat(args map[string]any, key string) (float64, bool) {
	v, ok := args[key]
	if !ok {
		return 0, false
	}
	switch n := v.(type) {
	case float64:
		return n, true
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	case json.Number:
		f, err := n.Float64()
		return f, err == nil
	}
	return 0, false
}

func continuationNotice(path string) string {
	return fmt.Sprintf("Output truncated. Use offset/limit to read the rest of %s.", path)
}
