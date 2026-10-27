package session

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

const (
	CompactionPrefix = "The conversation history before this point was compacted into the following following summary:\n\n<summary>\n"
	CompactionSuffix = "\n</summary>"
	BranchPrefix     = "The following is a summary of a branch that this conversation came back from:\n\n<summary>\n"
	BranchSuffix     = "</summary>"
)

// Correct prefixes per pinned messages.ts (no duplicated word).
const (
	compactionSummaryPrefix = "The conversation history before this point was compacted into the following summary:\n\n<summary>\n"
	compactionSummarySuffix = "\n</summary>"
	branchSummaryPrefix     = "The following is a summary of a branch that this conversation came back from:\n\n<summary>\n"
	branchSummarySuffix     = "</summary>"
)

// BuildPath walks parent links from leafID (or last entry) to the root and
// returns the active path in chronological order. Sibling branches are excluded.
// Errors on duplicate IDs (caller validates), missing parents, and cycles.
func BuildPath(entries []*Entry, leafID string) ([]*Entry, error) {
	byID := make(map[string]*Entry, len(entries))
	for _, e := range entries {
		id := e.ID()
		if _, dup := byID[id]; dup {
			return nil, fmt.Errorf("duplicate entry id %q", id)
		}
		byID[id] = e
	}
	var leaf *Entry
	if leafID != "" {
		var ok bool
		leaf, ok = byID[leafID]
		if !ok {
			return nil, fmt.Errorf("leaf id %q not found", leafID)
		}
	} else if len(entries) > 0 {
		leaf = entries[len(entries)-1]
	} else {
		return nil, nil
	}
	var rev []*Entry
	seen := map[string]bool{}
	cur := leaf
	for cur != nil {
		id := cur.ID()
		if seen[id] {
			return nil, fmt.Errorf("parent cycle at entry %q", id)
		}
		seen[id] = true
		rev = append(rev, cur)
		pid := cur.ParentID()
		if pid == nil || *pid == "" {
			break
		}
		parent, ok := byID[*pid]
		if !ok {
			return nil, fmt.Errorf("missing parent %q for entry %q", *pid, id)
		}
		cur = parent
	}
	// Reverse to chronological.
	for i, j := 0, len(rev)-1; i < j; i, j = i+1, j-1 {
		rev[i], rev[j] = rev[j], rev[i]
	}
	return rev, nil
}

// ModelState is the restored provider/model + thinking level.
type ModelState struct {
	ThinkingLevel string
	Provider      string
	ModelID       string
	HasModel      bool
}

// RestoreModelState scans the active path for thinking-level and model changes.
// Assistant message metadata acts as model signal (latest wins), matching Pi.
func RestoreModelState(path []*Entry) ModelState {
	st := ModelState{ThinkingLevel: "off"}
	for _, e := range path {
		switch e.Kind {
		case "thinking_level_change":
			st.ThinkingLevel = e.ThinkingLevelChange.ThinkingLevel
		case "model_change":
			st.Provider = e.ModelChange.Provider
			st.ModelID = e.ModelChange.ModelID
			st.HasModel = true
		case "message":
			if e.Message.Message.Assistant != nil {
				a := e.Message.Message.Assistant
				if a.Provider != "" && a.Model != "" {
					st.Provider = a.Provider
					st.ModelID = a.Model
					st.HasModel = true
				}
			}
		}
	}
	return st
}

// EntryToContextMessages projects one entry to model-visible messages.
// Returns empty for state-only entries. Mirrors sessionEntryToContextMessages.
func EntryToContextMessages(e *Entry) []AgentMessage {
	switch e.Kind {
	case "message":
		m := e.Message.Message
		// Normalize legacy null content (old sessions / hand-edited files).
		switch {
		case m.System != nil && m.System.Content == nil:
			cp := *m.System
			cp.Content = ""
			return []AgentMessage{{Role: "system", System: &cp}}
		case m.User != nil && m.User.Content == nil:
			cp := *m.User
			cp.Content = []UserContentBlock{}
			// Preserve raw? content normalized; keep raw nil so marshal uses struct.
			out := cp
			out.Raw = nil
			return []AgentMessage{{Role: "user", User: &out}}
		case m.Assistant != nil && m.Assistant.Content == nil:
			cp := *m.Assistant
			cp.Content = []ContentBlock{}
			out := cp
			out.Raw = nil
			return []AgentMessage{{Role: "assistant", Assistant: &out}}
		case m.ToolResult != nil && m.ToolResult.Content == nil:
			cp := *m.ToolResult
			cp.Content = []UserContentBlock{}
			out := cp
			out.Raw = nil
			return []AgentMessage{{Role: "toolResult", ToolResult: &out}}
		}
		return []AgentMessage{m}
	case "custom_message":
		cm := e.CustomMessage
		ts := parseTimeMillis(cm.Timestamp)
		return []AgentMessage{{Role: "custom", Custom: &CustomMessage{
			Role: "custom", CustomType: cm.CustomType, Content: cm.Content,
			Display: cm.Display, Details: cm.Details, Timestamp: ts,
		}}}
	case "branch_summary":
		bs := e.BranchSummary
		ts := parseTimeMillis(bs.Timestamp)
		from := bs.FromID
		return []AgentMessage{{Role: "branchSummary", BranchSummary: &BranchSummaryMessage{
			Role: "branchSummary", Summary: bs.Summary, FromID: &from, Timestamp: ts,
		}}}
	case "compaction":
		c := e.Compaction
		ts := parseTimeMillis(c.Timestamp)
		var out []AgentMessage
		if c.SystemMessage != nil {
			out = append(out, AgentMessage{Role: "system", System: c.SystemMessage})
		}
		out = append(out, AgentMessage{Role: "compactionSummary", CompactionSummary: &CompactionSummaryMessage{
			Role: "compactionSummary", Summary: c.Summary, TokensBefore: c.TokensBefore, Timestamp: ts,
		}})
		return out
	default:
		return nil
	}
}

// BuildContextEntries returns the compaction-aware active entry list.
// The latest compaction on the path contributes its checkpoint + summary,
// followed by kept entries starting at firstKeptEntryId and entries after it.
// Older compactions inside the retained range contribute no duplicate summary.
func BuildContextEntries(path []*Entry) []*Entry {
	latest := -1
	for i, e := range path {
		if e.Kind == "compaction" {
			latest = i
		}
	}
	if latest < 0 {
		return path
	}
	comp := path[latest].Compaction
	var out []*Entry
	out = append(out, path[latest])
	found := false
	for i := 0; i < latest; i++ {
		e := path[i]
		if e.ID() == comp.FirstKeptEntryID {
			found = true
		}
		if found {
			// System declarations before the compaction boundary are superseded
			// by the compaction system checkpoint; skip them (Pi semantics).
			if e.Kind == "message" && e.Message.Message.System != nil {
				continue
			}
			out = append(out, e)
		}
	}
	out = append(out, path[latest+1:]...)
	return out
}

// Projection is the provenance-preserving model context.
type Projection struct {
	// Entries parallels BuildContextEntries output; Messages[i] is that entry's
	// contribution after context edits (empty for state-only/omitted).
	Entries  []*Entry
	Messages [][]AgentMessage
	// Flattened model-visible messages.
	Flat  []AgentMessage
	State ModelState
}

// BuildProjection applies context edits (latest wins) over compaction-aware entries.
func BuildProjection(path []*Entry) *Projection {
	state := RestoreModelState(path)
	ctxEntries := BuildContextEntries(path)
	edits := map[string]*ContextEditEntry{}
	for _, e := range ctxEntries {
		if e.Kind == "context_edit" {
			edits[e.ContextEdit.TargetID] = e.ContextEdit
		}
	}
	p := &Projection{State: state}
	for idx, e := range ctxEntries {
		msgs := EntryToContextMessages(e)
		// Only the newest compaction contributes checkpoint+summary.
		if e.Kind == "compaction" && idx > 0 {
			// Older retained compaction: find whether this is the latest.
			// Since ctxEntries[0] is the latest, any later compaction is older.
			msgs = nil
		}
		if edit, ok := edits[e.ID()]; ok {
			msgs = applyEdit(msgs, edit)
		}
		p.Entries = append(p.Entries, e)
		p.Messages = append(p.Messages, msgs)
		p.Flat = append(p.Flat, msgs...)
	}
	return p
}

func applyEdit(msgs []AgentMessage, edit *ContextEditEntry) []AgentMessage {
	if len(edit.Replacement) == 0 || string(edit.Replacement) == "null" {
		return nil
	}
	var rep struct {
		Content json.RawMessage `json:"content"`
	}
	if err := json.Unmarshal(edit.Replacement, &rep); err != nil {
		return msgs
	}
	// Null content already handled; determine string vs array.
	var asString string
	isString := json.Unmarshal(rep.Content, &asString) == nil
	out := make([]AgentMessage, 0, len(msgs))
	for _, m := range msgs {
		switch m.Role {
		case "user", "custom":
			out = append(out, replaceContent(m, rep.Content, false)...)
		case "assistant", "toolResult":
			if isString {
				// Normalize string replacements for roles requiring content arrays.
				arr, _ := json.Marshal([]map[string]string{{"type": "text", "text": asString}})
				out = append(out, replaceContent(m, arr, true)...)
			} else {
				out = append(out, replaceContent(m, rep.Content, true)...)
			}
		default:
			out = append(out, m)
		}
	}
	return out
}

func replaceContent(m AgentMessage, contentRaw json.RawMessage, _ bool) []AgentMessage {
	// Rebuild the message with replaced content, preserving metadata.
	raw := append(json.RawMessage(nil), m.Raw...)
	switch m.Role {
	case "user":
		if m.User == nil {
			return []AgentMessage{m}
		}
		cp := *m.User
		cp.Raw = nil
		if tryUserBlocks(contentRaw, &cp) {
			return []AgentMessage{{Role: "user", User: &cp}}
		}
		// String content.
		var s string
		if err := json.Unmarshal(contentRaw, &s); err == nil {
			cp.Content = s
			return []AgentMessage{{Role: "user", User: &cp}}
		}
		_ = raw
		return []AgentMessage{m}
	case "custom":
		if m.Custom == nil {
			return []AgentMessage{m}
		}
		cp := *m.Custom
		cp.Raw = nil
		var s string
		if err := json.Unmarshal(contentRaw, &s); err == nil {
			cp.Content = s
			return []AgentMessage{{Role: "custom", Custom: &cp}}
		}
		var v any
		if err := json.Unmarshal(contentRaw, &v); err == nil {
			cp.Content = v
			return []AgentMessage{{Role: "custom", Custom: &cp}}
		}
		return []AgentMessage{m}
	case "assistant":
		if m.Assistant == nil {
			return []AgentMessage{m}
		}
		cp := *m.Assistant
		cp.Raw = nil
		var blocks []ContentBlock
		if err := json.Unmarshal(contentRaw, &blocks); err == nil {
			cp.Content = blocks
			return []AgentMessage{{Role: "assistant", Assistant: &cp}}
		}
		return []AgentMessage{m}
	case "toolResult":
		if m.ToolResult == nil {
			return []AgentMessage{m}
		}
		cp := *m.ToolResult
		cp.Raw = nil
		var blocks []UserContentBlock
		if err := json.Unmarshal(contentRaw, &blocks); err == nil {
			cp.Content = blocks
			return []AgentMessage{{Role: "toolResult", ToolResult: &cp}}
		}
		return []AgentMessage{m}
	}
	return []AgentMessage{m}
}

func tryUserBlocks(raw json.RawMessage, u *UserMessage) bool {
	var blocks []UserContentBlock
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return false
	}
	u.Content = blocks
	return true
}

// ConvertToLLM maps agent messages (including historical custom types) to
// LLM-compatible messages. Mirrors convertToLlm in messages.ts.
func ConvertToLLM(msgs []AgentMessage) []AgentMessage {
	var out []AgentMessage
	for _, m := range msgs {
		switch m.Role {
		case "bashExecution":
			b := m.BashExecution
			if b == nil {
				continue
			}
			if b.ExcludeFromContext != nil && *b.ExcludeFromContext {
				continue
			}
			out = append(out, AgentMessage{Role: "user", User: &UserMessage{
				Role: "user", Content: bashExecutionToText(b), Timestamp: b.Timestamp,
			}})
		case "custom":
			c := m.Custom
			if c == nil {
				continue
			}
			var content any
			if s, ok := c.Content.(string); ok {
				content = []UserContentBlock{{Type: "text", Text: &TextContent{Type: "text", Text: s}}}
			} else {
				content = c.Content
			}
			out = append(out, AgentMessage{Role: "user", User: &UserMessage{
				Role: "user", Content: content, Timestamp: c.Timestamp,
			}})
		case "branchSummary":
			b := m.BranchSummary
			if b == nil {
				continue
			}
			out = append(out, AgentMessage{Role: "user", User: &UserMessage{
				Role:      "user",
				Content:   branchSummaryPrefix + b.Summary + branchSummarySuffix,
				Timestamp: b.Timestamp,
			}})
		case "compactionSummary":
			c := m.CompactionSummary
			if c == nil {
				continue
			}
			out = append(out, AgentMessage{Role: "user", User: &UserMessage{
				Role:      "user",
				Content:   compactionSummaryPrefix + c.Summary + compactionSummarySuffix,
				Timestamp: c.Timestamp,
			}})
		case "system", "user", "assistant", "toolResult":
			out = append(out, m)
		default:
			// Unknown context-producing role: refuse is handled by CheckContinuable;
			// ConvertToLLM drops nothing silently for known-safe metadata, but
			// unknown roles are filtered here only after an explicit safety check.
			// Keep behavior strict: drop with no silent instruction injection.
			continue
		}
	}
	return out
}

func bashExecutionToText(b *BashExecutionMessage) string {
	var sb strings.Builder
	sb.WriteString("Ran `" + b.Command + "`\n")
	if b.Output != "" {
		sb.WriteString("```\n" + b.Output + "\n```")
	} else {
		sb.WriteString("(no output)")
	}
	if b.Cancelled {
		sb.WriteString("\n\n(command cancelled)")
	} else if b.ExitCode != nil && *b.ExitCode != 0 {
		sb.WriteString(fmt.Sprintf("\n\nCommand exited with code %d", *b.ExitCode))
	}
	if b.Truncated {
		if b.FullOutputPath != nil {
			sb.WriteString("\n\n[Output truncated. Full output: " + *b.FullOutputPath + "]")
		} else {
			sb.WriteString("\n\n[Output truncated]")
		}
	}
	return sb.String()
}

// CheckContinuable refuses continuation when the projected context contains
// unsafe unknown roles or unresolved deferred/background state.
func CheckContinuable(flat []AgentMessage, entries []*Entry) error {
	for _, m := range flat {
		if err := checkContent(m); err != nil {
			return err
		}
		switch m.Role {
		case "system", "user", "assistant", "toolResult",
			"bashExecution", "custom", "branchSummary", "compactionSummary":
			// known
		default:
			return fmt.Errorf("unsupported context role %q: refuse lossy continuation", m.Role)
		}
	}
	// Deferred/background runtime state is preserved but refuses continuation.
	for _, e := range entries {
		if e.Kind == "message" && e.Message.Message.Assistant != nil {
			// Assistant Raw may carry deferred handles; detect via raw JSON.
			var probe struct {
				Message struct {
					Deferred *json.RawMessage `json:"deferred"`
				} `json:"message"`
			}
			if err := json.Unmarshal(e.Raw, &probe); err == nil && probe.Message.Deferred != nil {
				return fmt.Errorf("session contains deferred/background runtime state: refuse continuation")
			}
		}
	}
	return nil
}

// UnresolvedToolCalls returns assistant tool calls without matching results.
// Callers must not automatically rerun uncertain side effects.
func UnresolvedToolCalls(flat []AgentMessage) []ToolCall {
	results := map[string]bool{}
	for _, m := range flat {
		if m.Role == "toolResult" && m.ToolResult != nil {
			results[m.ToolResult.ToolCallID] = true
		}
	}
	var missing []ToolCall
	for _, m := range flat {
		if m.Role == "assistant" && m.Assistant != nil {
			for _, b := range m.Assistant.Content {
				if b.Type == "toolCall" && b.Call != nil {
					if !results[b.Call.ID] {
						missing = append(missing, *b.Call)
					}
				}
			}
		}
	}
	sort.Slice(missing, func(i, j int) bool { return missing[i].ID < missing[j].ID })
	return missing
}

// EffectiveSystem describes replayed prompt + tool state (Step 25).
type EffectiveSystem struct {
	Content    string
	Tools      []Tool
	Checkpoint *SystemMessage
}

// ReplaySystem folds system messages in order: content appends, sections
// replace/remove by name, toolsAdded/toolsRemoved change the tool set.
func ReplaySystem(msgs []AgentMessage) EffectiveSystem {
	var content []string
	sections := map[string]*string{}
	var order []string
	tools := map[string]Tool{}
	var timestamp int64
	for _, m := range msgs {
		if m.Role != "system" || m.System == nil {
			continue
		}
		s := m.System
		if timestamp == 0 {
			timestamp = s.Timestamp
		}
		if blocks, err := UserBlocks(s.Content); err == nil {
			var text []string
			for _, b := range blocks {
				if b.Text != nil {
					text = append(text, b.Text.Text)
				}
			}
			if t := strings.Join(text, "\n"); t != "" {
				content = append(content, t)
			}
		}
		for _, k := range sectionOrder(s) {
			v := s.Sections[k]
			if v == nil {
				delete(sections, k)
				for i, old := range order {
					if old == k {
						order = append(order[:i], order[i+1:]...)
						break
					}
				}
			} else {
				if _, ok := sections[k]; !ok {
					order = append(order, k)
				}
				sections[k] = v
			}
		}
		// Pi removes first, then adds, so a changed declaration can use both.
		for _, r := range s.ToolsRemoved {
			if r.Name != nil {
				delete(tools, *r.Name)
			}
		}
		for _, t := range s.ToolsAdded {
			tools[t.Name] = t
		}
	}
	base := strings.Join(content, "\n\n")
	parts := []string{}
	if base != "" {
		parts = append(parts, base)
	}
	for _, k := range order {
		if *sections[k] != "" {
			parts = append(parts, *sections[k])
		}
	}
	var names []string
	for n := range tools {
		names = append(names, n)
	}
	sort.Strings(names)
	var out []Tool
	for _, n := range names {
		out = append(out, tools[n])
	}
	checkpoint := &SystemMessage{Role: "system", Content: base, Sections: sections, ToolsAdded: out, Timestamp: timestamp}
	// Preserve section insertion order in the checkpoint's raw representation.
	checkpoint.Raw = systemRaw(checkpoint, order)
	return EffectiveSystem{Content: strings.Join(parts, "\n\n"), Tools: out, Checkpoint: checkpoint}
}
