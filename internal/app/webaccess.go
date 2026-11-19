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
	definitions := r.webDefinitions()
	fetchEnabled := false
	retrievalAdvertised := false
	for _, def := range definitions {
		switch def.Name {
		case webaccess.ToolFetch:
			fetchEnabled = true
		case webaccess.ToolContent:
			retrievalAdvertised = true
		}
	}
	out := map[string]agent.InternalToolHandler{}
	for _, def := range definitions {
		out[def.Name] = func(ctx context.Context, invocation tools.Invocation) tools.Result {
			// Plugin enablement is checked by the client. CLI selection must
			// also apply to page requests made indirectly through web_search.
			if invocation.Name == webaccess.ToolSearch && invocation.Arguments["includeContent"] == true && !fetchEnabled {
				return tools.Result{IsError: true, Content: []tools.Block{{
					Type: "text", Text: "web_search failed: validation: includeContent requires fetch_content to be enabled",
				}}}
			}
			// The advertised retrieval tool is a display fact for this request;
			// it never changes admission or the model-facing result.
			return client.Dispatch(webaccess.WithRetrievalDisplay(ctx, retrievalAdvertised), invocation)
		}
	}
	return out
}

// promptToolNames includes the always-available history tool along with the
// selected built-ins. Copy the selection so appending cannot alter settings.
func promptToolNames(names []string, web *webaccess.Client) []string {
	out := append([]string(nil), filterPromptTools(names, web)...)
	for _, name := range out {
		if name == readSessionToolName {
			return out
		}
	}
	return append(out, readSessionToolName)
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
