package tools

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
)

// EditTool implements Pi-compatible edit matching and mutation.
type EditTool struct {
	Cwd string
	mu  sync.Mutex // serialize mutation per file (first release: global)
}

type editItem struct {
	Old string
	New string
}

// Execute supports current {path, edits[]} plus legacy {oldText,newText} and
// JSON-string edits (model normalization, Pi parity).
func (t *EditTool) Execute(inv Invocation) Result {
	t.mu.Lock()
	defer t.mu.Unlock()
	path, _ := argString(inv.Arguments, "path")
	if path == "" {
		return errResult("edit: path is required")
	}
	edits, err := normalizeEdits(inv.Arguments)
	if err != nil {
		return errResult(fmt.Sprintf("edit: %v", err))
	}
	abs := resolvePath(t.Cwd, path)
	data, err := os.ReadFile(abs)
	if err != nil {
		return errResult(fmt.Sprintf("edit: %v", err))
	}
	orig := string(data)
	bom := strings.HasPrefix(orig, "\xef\xbb\xbf")
	body := strings.TrimPrefix(orig, "\xef\xbb\xbf")
	ending := detectEnding(body)
	norm := normalizeLF(body)
	matches := make([]matchPos, 0, len(edits))
	for i, e := range edits {
		if e.Old == "" {
			return errResult("edit: edits[].oldText must be non-empty")
		}
		nOld := normalizeLF(e.Old)
		idx := strings.Index(norm, nOld)
		if idx < 0 {
			return errResult(fmt.Sprintf("edit: oldText not found (edit %d)", i))
		}
		if strings.Contains(norm[idx+1:], nOld) {
			// Overlapping occurrences count as ambiguous matches too (aaa/aa).
			return errResult(fmt.Sprintf("edit: oldText matches multiple locations (edit %d); make it unique", i))
		}
		matches = append(matches, matchPos{start: idx, end: idx + len(nOld), edit: e})
	}
	// Overlap check against original coordinates.
	for i := 0; i < len(matches); i++ {
		for j := i + 1; j < len(matches); j++ {
			a, b := matches[i], matches[j]
			if a.start < b.end && b.start < a.end {
				return errResult("edit: overlapping edits; merge them into one edit")
			}
			// Nested is a subset of overlap (same check covers it).
		}
	}
	// Apply from end to start so offsets stay valid.
	out := norm
	// Sort descending by start.
	for i := 0; i < len(matches); i++ {
		for j := i + 1; j < len(matches); j++ {
			if matches[j].start > matches[i].start {
				matches[i], matches[j] = matches[j], matches[i]
			}
		}
	}
	for _, m := range matches {
		out = out[:m.start] + normalizeLF(m.edit.New) + out[m.end:]
	}
	out = restoreEnding(out, ending)
	if bom {
		out = "\xef\xbb\xbf" + out
	}
	if err := os.WriteFile(abs, []byte(out), 0o644); err != nil {
		return errResult(fmt.Sprintf("edit: failed to write: %v", err))
	}
	return textResult(fmt.Sprintf("Successfully edited %s (%d edit(s))", path, len(edits)))
}

type matchPos struct {
	start, end int
	edit       editItem
}

func normalizeEdits(args map[string]any) ([]editItem, error) {
	var raw any
	if v, ok := args["edits"]; ok {
		raw = v
	}
	// JSON-string form.
	if s, ok := raw.(string); ok {
		var parsed any
		if err := json.Unmarshal([]byte(s), &parsed); err == nil {
			raw = parsed
		}
	}
	// Single-object form.
	if m, ok := raw.(map[string]any); ok {
		if _, hasOld := m["oldText"]; hasOld {
			raw = []any{m}
		}
	}
	var list []any
	switch v := raw.(type) {
	case []any:
		list = v
	case []map[string]any:
		for _, e := range v {
			list = append(list, e)
		}
	case nil:
		// A legacy top-level replacement can form the entire list.
	default:
		return nil, fmt.Errorf("edits must contain an array of replacements")
	}
	// Pi appends a legacy top-level replacement to a current edits[] list.
	if oldT, ok := args["oldText"].(string); ok {
		if newT, ok := args["newText"].(string); ok {
			list = append(list, map[string]any{"oldText": oldT, "newText": newT})
		}
	}
	if len(list) == 0 {
		return nil, fmt.Errorf("edits must contain at least one replacement")
	}
	var out []editItem
	for i, e := range list {
		m, ok := e.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("edit %d must be an object", i)
		}
		o, _ := m["oldText"].(string)
		n, hasNew := m["newText"].(string)
		if o == "" || !hasNew {
			return nil, fmt.Errorf("edit %d requires oldText and newText strings", i)
		}
		out = append(out, editItem{Old: o, New: n})
	}
	return out, nil
}

func detectEnding(s string) string {
	if strings.Contains(s, "\r\n") {
		return "\r\n"
	}
	return "\n"
}

func normalizeLF(s string) string {
	return strings.ReplaceAll(s, "\r\n", "\n")
}

func restoreEnding(s, ending string) string {
	if ending == "\r\n" {
		return strings.ReplaceAll(s, "\n", "\r\n")
	}
	return s
}
