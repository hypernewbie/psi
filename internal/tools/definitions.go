package tools

import "psi/internal/provider"

// Definitions declares the actual inputs accepted by the four built-in tools.
// The same declarations are persisted in system messages and sent to the model.
func Definitions(names []string) []provider.ToolDef {
	str := func(description string) map[string]any {
		return map[string]any{"type": "string", "description": description}
	}
	obj := func(properties map[string]any, required ...string) map[string]any {
		return map[string]any{"type": "object", "properties": properties, "required": required}
	}
	defs := map[string]provider.ToolDef{
		Read: {Name: Read, Description: "Read a file. Text is limited to 2000 lines or 50 KiB. Use offset and limit to continue large reads.", Schema: obj(map[string]any{
			"path":   str("File path, relative to the workspace or absolute"),
			"offset": map[string]any{"type": "integer", "minimum": 1, "description": "First line to read (1-indexed)"},
			"limit":  map[string]any{"type": "integer", "minimum": 1, "description": "Maximum number of lines to read"},
		}, "path")},
		Write: {Name: Write, Description: "Create or replace a file with the complete supplied content. Creates parent directories.", Schema: obj(map[string]any{
			"path": str("File path"), "content": str("Complete file content"),
		}, "path", "content")},
		Edit: {Name: Edit, Description: "Apply unique, non-overlapping exact-text replacements. All edits match the original file. Preserve line endings.", Schema: obj(map[string]any{
			"path": str("File path"),
			"edits": map[string]any{"type": "array", "minItems": 1, "items": obj(map[string]any{
				"oldText": str("Unique exact text from the original file"), "newText": str("Replacement text"),
			}, "oldText", "newText")},
		}, "path", "edits")},
		Bash: {Name: Bash, Description: "Run a shell command in the workspace. Returns the last 2000 lines or 50 KiB, with a full-output file when truncated. No default timeout.", Schema: obj(map[string]any{
			"command": str("Shell command"), "timeout": map[string]any{"type": "number", "exclusiveMinimum": 0, "description": "Optional timeout in seconds"},
		}, "command")},
	}
	var out []provider.ToolDef
	for _, name := range names {
		if d, ok := defs[name]; ok {
			out = append(out, d)
		}
	}
	return out
}
