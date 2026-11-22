package main

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/hypernewbie/psi/internal/provider"
	"github.com/hypernewbie/psi/internal/skills"
	"github.com/hypernewbie/psi/internal/tools"
)

const maxSkillOutput = 32 << 10

type activeTurn struct {
	channelID string
	staged    string
	stagedOK  bool
}

type conversationIO interface {
	readMessages(ctx context.Context, channelID string, limit int, before, after string) ([]historyMessage, error)
}

// toolSet is the exact model-visible tool surface for one route.
type toolSet struct {
	definitions []provider.ToolDef
	handlers    map[string]agentToolHandler
}

type agentToolHandler = func(context.Context, tools.Invocation) tools.Result

func memoryToolDefinitions() []provider.ToolDef {
	str := func(description string) map[string]any {
		return map[string]any{"type": "string", "description": description}
	}
	integer := func(description string, minimum int) map[string]any {
		return map[string]any{"type": "integer", "minimum": minimum, "description": description}
	}
	return []provider.ToolDef{
		{
			Name:        "memory_search",
			Description: "Search Markdown memory (MEMORY.md, USER.md, and memory/*.md) when an answer depends on prior work, decisions, dates, people, preferences, or todos. Every query term must appear.",
			Schema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"query":      str("Search terms"),
					"maxResults": integer("Maximum results (default 6, maximum 20)", 1),
					"minScore":   map[string]any{"type": "number", "minimum": 0, "description": "Minimum occurrence score"},
				},
				"required": []string{"query"},
			},
		},
		{
			Name:        "memory_get",
			Description: "Read a bounded line range from a Markdown memory file returned by memory_search.",
			Schema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"path":  str("Memory path, relative to the workspace"),
					"from":  integer("First line (1-indexed)", 1),
					"lines": integer("Maximum lines to return", 1),
				},
				"required": []string{"path"},
			},
		},
	}
}

func skillToolDefinition() provider.ToolDef {
	return provider.ToolDef{
		Name:        "skill",
		Description: "Load the instructions for one named workspace skill. Resolve referenced paths relative to the skill location in the result.",
		Schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"name": map[string]any{"type": "string", "description": "Skill name"},
				"args": map[string]any{"type": "string", "description": "Optional task arguments appended to the instructions"},
			},
			"required": []string{"name"},
		},
	}
}

func messageToolDefinition(allowSend bool) provider.ToolDef {
	action := map[string]any{"type": "string", "enum": []string{"read"}, "description": "Message action"}
	if allowSend {
		action["enum"] = []string{"read", "send"}
	}
	return provider.ToolDef{
		Name:        "message",
		Description: "Read recent messages or one checked message in this conversation, or send the visible reply when this route uses explicit delivery.",
		Schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"action":    action,
				"text":      map[string]any{"type": "string", "description": "Text for action=send"},
				"limit":     map[string]any{"type": "integer", "minimum": 1, "maximum": 100, "description": "Messages to read"},
				"before":    map[string]any{"type": "string", "description": "Read messages before this message ID"},
				"after":     map[string]any{"type": "string", "description": "Read messages after this message ID"},
				"messageId": map[string]any{"type": "string", "description": "Read one checked message ID in this conversation"},
			},
			"required": []string{"action"},
		},
	}
}

// implementedTools lists every tool this release can implement. The exec tool
// is listed so explicit allow/deny entries can match it, but no profile
// includes it: it needs tools.exec.enabled and the owner DM route.
var implementedTools = []string{"memory_search", "memory_get", "message", "skill", "exec"}

// profileTools mirrors the tool-profile subsets that apply to the tools Psionic
// implements. The coding profile excludes the messaging tool and the
// messaging profile excludes memory tools; minimal has neither.
var profileTools = map[string]map[string]bool{
	"minimal":   {},
	"coding":    {"memory_search": true, "memory_get": true, "skill": true},
	"messaging": {"message": true, "skill": true},
	"full":      {"memory_search": true, "memory_get": true, "message": true, "skill": true},
}

// allows applies profile, allow, alsoAllow, and deny as a filter over the
// tools this release actually implements.
func (p toolPolicy) allows(name string) bool {
	base := map[string]bool{}
	for _, tool := range implementedTools {
		base[tool] = true
	}
	if p.Profile != "" {
		profile, ok := profileTools[p.Profile]
		if !ok {
			return false
		}
		base = make(map[string]bool, len(profile))
		for tool, allowed := range profile {
			base[tool] = allowed
		}
	}
	if len(p.Allow) > 0 {
		base = map[string]bool{}
		for _, pattern := range p.Allow {
			for _, tool := range implementedTools {
				if toolMatches(pattern, tool) {
					base[tool] = true
				}
			}
		}
	}
	for _, pattern := range p.AlsoAllow {
		for _, tool := range implementedTools {
			if toolMatches(pattern, tool) {
				base[tool] = true
			}
		}
	}
	for _, pattern := range p.Deny {
		for _, tool := range implementedTools {
			if toolMatches(pattern, tool) {
				base[tool] = false
			}
		}
	}
	return base[name]
}

func toolMatches(pattern, name string) bool {
	pattern = strings.ToLower(strings.TrimSpace(pattern))
	if pattern == "*" || pattern == name {
		return true
	}
	if strings.HasPrefix(pattern, "group:") {
		return false
	}
	if strings.HasSuffix(pattern, "*") {
		return strings.HasPrefix(name, strings.TrimSuffix(pattern, "*"))
	}
	return false
}

// buildTools applies the route's memory visibility and the configured tool
// policy before any tool definition or handler is exposed. The exec tool is
// additionally opt-in: it needs tools.exec.enabled and an owner direct-message
// route (includeMemory), regardless of profile or allow entries in a shared
// room.
func buildTools(workspace string, includeMemory, allowSend bool, policy toolPolicy, available []skills.Skill, turn *activeTurn, io conversationIO, execCfg *execSettings) (toolSet, error) {
	set := toolSet{handlers: map[string]agentToolHandler{}}
	var memory *memoryIndex
	if includeMemory && (policy.allows("memory_search") || policy.allows("memory_get")) {
		index, err := loadMemory(workspace)
		if err != nil {
			return toolSet{}, err
		}
		memory = index
	}
	add := func(def provider.ToolDef, handler agentToolHandler) {
		if !policy.allows(def.Name) {
			return
		}
		set.definitions = append(set.definitions, def)
		set.handlers[def.Name] = handler
	}
	if memory != nil {
		add(memoryToolDefinitions()[0], func(_ context.Context, inv tools.Invocation) tools.Result {
			query, _ := inv.Arguments["query"].(string)
			if strings.TrimSpace(query) == "" {
				return toolError("query is required")
			}
			maxResults := intArg(inv.Arguments, "maxResults", 6)
			if maxResults <= 0 {
				maxResults = 6
			}
			if maxResults > 20 {
				maxResults = 20
			}
			minScore, _ := inv.Arguments["minScore"].(float64)
			if minScore < 0 {
				minScore = 0
			}
			hits := memory.search(query, maxResults, minScore)
			return toolText(memorySearchResult(hits))
		})
		add(memoryToolDefinitions()[1], func(_ context.Context, inv tools.Invocation) tools.Result {
			name, _ := inv.Arguments["path"].(string)
			from := intArg(inv.Arguments, "from", 1)
			lines := intArg(inv.Arguments, "lines", 200)
			text, err := memory.get(name, from, lines)
			if err != nil {
				return toolError(err.Error())
			}
			return toolText(text)
		})
	}
	if len(available) > 0 {
		add(skillToolDefinition(), func(_ context.Context, inv tools.Invocation) tools.Result {
			name, _ := inv.Arguments["name"].(string)
			args, _ := inv.Arguments["args"].(string)
			var selected *skills.Skill
			for i := range available {
				if available[i].Name == name {
					selected = &available[i]
					break
				}
			}
			if selected == nil {
				return toolError("skill is unavailable")
			}
			// Recheck links at invocation: the skill tree can change after
			// discovery. The workspace and the skill paths are compared in
			// their resolved form, because a temporary directory has a
			// different name than its real location on macOS and Windows.
			root := resolvePath(workspace)
			file, err := filepath.EvalSymlinks(selected.FilePath)
			if err != nil || !inside(root, file) {
				return toolError("skill is unavailable")
			}
			base, err := filepath.EvalSymlinks(selected.BaseDir)
			if err != nil || !inside(root, base) {
				return toolError("skill is unavailable")
			}
			text, err := skills.Invoke([]skills.Skill{*selected}, name, args)
			if err != nil {
				return toolError("skill is unavailable")
			}
			text = strings.ReplaceAll(text, selected.FilePath, relativePath(workspace, selected.FilePath))
			text = strings.ReplaceAll(text, selected.BaseDir, relativePath(workspace, selected.BaseDir))
			text = truncateWithNote(text, maxSkillOutput)
			return toolText(text)
		})
	}
	if io != nil {
		add(messageToolDefinition(allowSend), func(ctx context.Context, inv tools.Invocation) tools.Result {
			action, _ := inv.Arguments["action"].(string)
			switch action {
			case "read":
				limit := intArg(inv.Arguments, "limit", 20)
				if limit <= 0 {
					limit = 20
				}
				if limit > 100 {
					limit = 100
				}
				before, _ := inv.Arguments["before"].(string)
				after, _ := inv.Arguments["after"].(string)
				messageID, _ := inv.Arguments["messageId"].(string)
				if messageID != "" {
					if before != "" || after != "" {
						return toolError("choose messageId, before, or after")
					}
					reader, ok := io.(referenceReader)
					if !ok {
						return toolError("message lookup is unavailable")
					}
					message, err := reader.message(ctx, turn.channelID, messageID)
					if err != nil {
						return toolError("message is unavailable")
					}
					return toolText(formatHistory([]historyMessage{message}))
				}
				if before != "" && after != "" {
					return toolError("choose before or after")
				}
				messages, err := io.readMessages(ctx, turn.channelID, limit, before, after)
				if err != nil {
					return toolError("history is unavailable")
				}
				return toolText(formatHistory(messages))
			case "send":
				if !allowSend {
					return toolError("action=send is unavailable because replies are automatic")
				}
				text, _ := inv.Arguments["text"].(string)
				if strings.TrimSpace(text) == "" {
					return toolError("text is required for action=send")
				}
				if len(text) > maxReplyText {
					return toolError("reply is too large")
				}
				if turn.stagedOK {
					return toolError("a reply is already staged for this turn")
				}
				turn.staged, turn.stagedOK = text, true
				return toolText("Reply staged for delivery.")
			default:
				return toolError("action must be read or send")
			}
		})
	}
	// The exec tool is owner-DM-only and opt-in. includeMemory is true exactly
	// on the owner direct-message route, so a shared room never registers it,
	// no matter what its profile or allow lists say.
	if execCfg != nil && execCfg.enabled() && includeMemory {
		limits := resolveExecLimits(execCfg)
		workspaceDir := workspace
		add(execToolDefinition(), func(ctx context.Context, inv tools.Invocation) tools.Result {
			command, _ := inv.Arguments["command"].(string)
			result := runExec(ctx, workspaceDir, command, limits)
			return toolText(result.replyText())
		})
	}
	return set, nil
}

func relativePath(workspace, path string) string {
	rel, err := filepath.Rel(workspace, path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return filepath.Base(path)
	}
	return filepath.ToSlash(rel)
}

func toolText(text string) tools.Result {
	return tools.Result{Content: []tools.Block{{Type: "text", Text: text}}}
}

func toolError(text string) tools.Result {
	return tools.Result{Content: []tools.Block{{Type: "text", Text: text}}, IsError: true}
}

func intArg(args map[string]any, name string, fallback int) int {
	switch value := args[name].(type) {
	case float64:
		return int(value)
	case json.Number:
		if n, err := value.Int64(); err == nil {
			return int(n)
		}
	}
	return fallback
}

func formatHistory(messages []historyMessage) string {
	if len(messages) == 0 {
		return "No messages."
	}
	var b strings.Builder
	for _, message := range messages {
		fmt.Fprintf(&b, "[%s] %s: %s\n", message.ID, message.Author, message.Text)
	}
	return strings.TrimSpace(b.String())
}
