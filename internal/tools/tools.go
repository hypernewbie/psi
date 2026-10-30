// Package tools implements the four built-in tools (Phase 7).
//
// The registry dispatches one call at a time per invocation; the agent layer
// runs eligible calls from one assistant response concurrently and settles
// them in assistant order. Same-file write/edit mutations serialize through
// the shared MutationQueue. Shared path/output behavior, truncation, and
// privacy rules live here.
// Reference: temp/pi-mono/packages/coding-agent/src/core/tools/{read,write,edit,bash,truncate,path-utils,file-mutation-queue}.ts
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
	// One mutation queue is shared by write and edit so same-file mutations
	// serialize across both tools, like Pi's file-mutation-queue.
	mutations := NewMutationQueue()
	return &Registry{Cwd: cwd, Read: &ReadTool{Cwd: cwd}, Write: &WriteTool{Cwd: cwd, Mutations: mutations}, Edit: &EditTool{Cwd: cwd, Mutations: mutations}, Bash: &BashTool{Cwd: cwd}}
}

// Dispatch runs one invocation. Callers that execute a batch dispatch each
// call themselves; same-file mutations serialize through the shared queue.
func (r *Registry) Dispatch(inv Invocation) Result {
	return r.DispatchCtx(context.Background(), inv)
}

// DispatchCtx propagates cancellation to shell processes and prevents new
// filesystem operations from starting after cancellation.
func (r *Registry) DispatchCtx(ctx context.Context, inv Invocation) Result {
	if err := ctx.Err(); err != nil {
		return errResult(fmt.Sprintf("cancelled before dispatch: %v", err))
	}
	switch inv.Name {
	case Read:
		return r.Read.ExecuteCtx(ctx, inv)
	case Write:
		return r.Write.ExecuteCtx(ctx, inv)
	case Edit:
		return r.Edit.ExecuteCtx(ctx, inv)
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
