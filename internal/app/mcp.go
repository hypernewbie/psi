package app

import (
	"context"
	"fmt"
	"io"
	"path/filepath"
	"sync"
	"time"

	"github.com/hypernewbie/psi/internal/agent"
	"github.com/hypernewbie/psi/internal/config"
	"github.com/hypernewbie/psi/internal/mcp"
	"github.com/hypernewbie/psi/internal/provider"
	"github.com/hypernewbie/psi/internal/terminal"
	"github.com/hypernewbie/psi/internal/tools"
)

// mcpAccount owns completion accounting across session replacements. It is a
// pointer field on runtime, so shallow candidate copies share the same
// account without copying a mutex.
type mcpAccount struct {
	mu      sync.Mutex
	current *mcp.Manager
	stopped bool
	wg      sync.WaitGroup
}

// replace stops the old manager and starts a fresh one without waiting for
// old cleanup. It returns immediately.
func (a *mcpAccount) replace(next *mcp.Manager) {
	a.mu.Lock()
	old := a.current
	if a.stopped {
		a.current = nil
		a.mu.Unlock()
		if next != nil {
			next.Stop()
		}
		return
	}
	a.current = next
	if next != nil {
		a.wg.Add(1)
	}
	a.mu.Unlock()
	if old != nil {
		old.Stop()
	}
	if next != nil {
		next.Start()
		go func() {
			next.Wait()
			a.wg.Done()
		}()
	}
}

// stop unpublishes the current manager immediately.
func (a *mcpAccount) stop() {
	a.mu.Lock()
	manager := a.current
	a.current = nil
	a.stopped = true
	a.mu.Unlock()
	if manager != nil {
		manager.Stop()
	}
}

func (a *mcpAccount) manager() *mcp.Manager {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.current
}

func (r *runtime) mcpManager() *mcp.Manager {
	if r == nil || r.mcpAccount == nil {
		return nil
	}
	return r.mcpAccount.manager()
}

// mcpInput captures immutable policy for one manager generation.
func (r *runtime) mcpInput() mcp.Input {
	readAvailable := false
	for _, name := range r.settings.DefaultTools {
		if name == tools.Read {
			readAvailable = true
			break
		}
	}
	return mcp.Input{
		UserPath:             filepath.Join(r.agentDir, "mcp.json"),
		ProjectPath:          filepath.Join(r.store.Cwd(), ".pi", "mcp.json"),
		ProjectTrusted:       r.projectTrust == projectTrusted,
		AllowProjectCommands: r.settings.MCPAllowProjectCommands,
		Cwd:                  r.store.Cwd(),
		ReadAvailable:        readAvailable,
	}
}

func (r *runtime) startMCP() {
	r.mcpEnabled = true
	if r.options.NoMCP {
		return
	}
	if r.mcpAccount == nil {
		r.mcpAccount = &mcpAccount{}
	}
	manager := mcp.NewManager(mcp.Options{Input: r.mcpInput(), Version: r.version})
	r.mcpAccount.replace(manager)
}

// restartMCP drops the old manager and starts an empty one for the committed
// session. The old manager cleans up outside the terminal loop.
func (r *runtime) restartMCP() {
	if !r.mcpEnabled || r.mcpAccount == nil {
		return
	}
	if r.options.NoMCP {
		r.mcpAccount.replace(nil)
		return
	}
	r.mcpAccount.replace(mcp.NewManager(mcp.Options{Input: r.mcpInput(), Version: r.version}))
}

func (r *runtime) stopMCP() {
	if r.mcpAccount != nil {
		r.mcpAccount.stop()
	}
}

func (r *runtime) waitMCP(budget time.Duration) {
	if r.mcpAccount == nil {
		return
	}
	done := make(chan struct{})
	go func() {
		r.mcpAccount.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(budget):
	}
}

// reloadMCP enqueues a fresh config read with the current immutable policy.
func (r *runtime) reloadMCP() {
	if !r.mcpEnabled || r.options.NoMCP {
		return
	}
	manager := r.mcpManager()
	if manager == nil {
		r.startMCP()
		return
	}
	manager.Reload(r.mcpInput())
}

// requestMCPSnapshot returns the current declarations. Callers must only use
// it from the agent coordinator while preparing one request.
func requestMCPSnapshot(manager *mcp.Manager) ([]mcp.Tool, []provider.ToolDef) {
	if manager == nil {
		return nil, nil
	}
	snapshot := manager.Snapshot()
	if snapshot == nil {
		return nil, nil
	}
	return snapshot.Tools, snapshot.Definitions()
}

// concurrentMCPTool captures one route for the duration of a request.
func concurrentMCPTool(manager *mcp.Manager, toolsAtRequest []mcp.Tool, name string) (agent.InternalToolHandler, bool) {
	if manager == nil {
		return nil, false
	}
	for _, tool := range toolsAtRequest {
		if tool.Name != name {
			continue
		}
		captured := tool
		return func(ctx context.Context, invocation tools.Invocation) tools.Result {
			return manager.Call(ctx, captured, invocation)
		}, true
	}
	return nil, false
}

// mcpSections appends availability guidance without mutating shared slices.
// A custom system prompt suppresses automatic guidance, so the MCP section is
// omitted there even when MCP declarations remain in the request.
func (r *runtime) mcpSections(sections []config.PromptSection, toolsAtRequest []mcp.Tool) []config.PromptSection {
	if len(toolsAtRequest) == 0 {
		return sections
	}
	automatic := false
	for _, section := range r.sections {
		if section.Name == "docs" {
			automatic = true
			break
		}
	}
	if !automatic {
		return sections
	}
	out := append([]config.PromptSection(nil), sections...)
	out = append(out, config.MCPPromptSection())
	return out
}

func (r *runtime) handleMCPCommand(arg string, out io.Writer) (bool, error) {
	print := func(s string) { fmt.Fprintln(out, terminal.Sanitize(s)) }
	if arg != "" {
		print("usage: /mcp")
		return false, nil
	}
	if r.options.NoMCP {
		print("mcp: disabled by --no-mcp")
		return false, nil
	}
	manager := r.mcpManager()
	if manager == nil {
		print("mcp: not started")
		return false, nil
	}
	snapshot := manager.Snapshot()
	if snapshot == nil || len(snapshot.Servers) == 0 {
		print("mcp: no servers configured")
		if snapshot != nil {
			printMCPDiagnostics(print, snapshot)
		}
		return false, nil
	}
	print("mcp:")
	for _, server := range snapshot.Servers {
		line := fmt.Sprintf("  %-20s %-6s %-12s %d discovered, %d exposed", server.Name, server.Transport, server.State, server.Discovered, server.Exposed)
		if server.ErrorClass != "" {
			line += " (" + server.ErrorClass + ")"
		}
		if server.RefreshWarning != "" {
			line += " [refresh: " + server.RefreshWarning + "]"
		}
		if server.NotificationsUnavailable {
			line += " [notifications unavailable]"
		}
		print(line)
	}
	printMCPDiagnostics(print, snapshot)
	return false, nil
}

func printMCPDiagnostics(print func(string), snapshot *mcp.Snapshot) {
	for _, warning := range snapshot.Warnings {
		print("mcp: " + warning)
	}
	if snapshot.ReloadError != "" {
		print("mcp: reload error (" + snapshot.ReloadError + ")")
	}
}
