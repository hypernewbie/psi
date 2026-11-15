package app

import (
	"context"
	"fmt"
	"strings"

	"psi/internal/agent"
	"psi/internal/provider"
	"psi/internal/tools"
	"psi/internal/webaccess"
)

// loadWebAccess reads the plugin configuration once per runtime. Callers must
// keep the returned client for the whole process lifetime.
func loadWebAccess() (*webaccess.Client, []string) {
	cfg, diagnostics := webaccess.Load(webaccess.OSEnv())
	return webaccess.New(cfg), diagnostics
}

// webDiagnosticsLines keeps the startup summary bounded. Messages are already
// value-free; this only caps count and line length.
func webDiagnosticsLines(diagnostics []string) []string {
	const maxLines = 5
	const maxWidth = 200
	var out []string
	for i, message := range diagnostics {
		if i == maxLines {
			out = append(out, fmt.Sprintf("note: web access: %d more configuration problem(s)", len(diagnostics)-maxLines))
			break
		}
		message = strings.TrimSpace(strings.ReplaceAll(message, "\n", " "))
		if len(message) > maxWidth {
			message = message[:maxWidth]
		}
		out = append(out, "note: "+message)
	}
	return out
}

// webDefinitions returns web tools that are both selected by psi and enabled by
// the plugin configuration. CLI selection always wins.
func (r *runtime) webDefinitions() []provider.ToolDef {
	if r.web == nil {
		return nil
	}
	selected := make(map[string]bool, len(r.settings.DefaultTools))
	for _, name := range r.settings.DefaultTools {
		selected[name] = true
	}
	var out []provider.ToolDef
	for _, def := range r.web.Definitions() {
		if selected[def.Name] {
			out = append(out, def)
		}
	}
	return out
}

// webHandlers registers handlers for exactly the advertised web tools.
func (r *runtime) webHandlers() map[string]agent.InternalToolHandler {
	if r.web == nil {
		return nil
	}
	client := r.web
	out := map[string]agent.InternalToolHandler{}
	for _, def := range r.webDefinitions() {
		out[def.Name] = func(ctx context.Context, invocation tools.Invocation) tools.Result {
			return client.Dispatch(ctx, invocation)
		}
	}
	return out
}

// filterPromptTools removes disabled web tools from the prompt name list so a
// disabled tool never appears in tool snippets or rules.
func filterPromptTools(names []string, web *webaccess.Client) []string {
	if web == nil {
		return names
	}
	enabled := map[string]bool{}
	for _, def := range web.Definitions() {
		enabled[def.Name] = true
	}
	out := make([]string, 0, len(names))
	for _, name := range names {
		switch name {
		case webaccess.ToolSearch, webaccess.ToolFetch, webaccess.ToolContent:
			if !enabled[name] {
				continue
			}
		}
		out = append(out, name)
	}
	return out
}
