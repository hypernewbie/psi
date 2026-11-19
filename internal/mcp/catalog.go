package mcp

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	maxToolNameLength = 64
	maxCatalogPages   = 128
	maxCatalogBytes   = 16 << 20
)

// Tool is one immutable tool declaration and its routing identity.
type Tool struct {
	Name        string
	Description string
	Schema      map[string]any
	Server      string
	RawName     string
	Epoch       uint64
	Exposure    Exposure
}

// ToolDefSchema returns the schema the catalog already owns. It is shared, not
// copied, so declaration snapshots do not rebuild every tool schema. Callers
// must treat the map as read-only; the provider request builders either keep
// the reference for JSON encoding or copy it before rewriting.
func (t Tool) ToolDefSchema() map[string]any { return t.Schema }

func sanitizeName(value string) string {
	var b strings.Builder
	b.Grow(len(value))
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	return b.String()
}

func baseToolName(server, tool string) string {
	return "mcp__" + sanitizeName(server) + "__" + sanitizeName(tool)
}

func nameHash(server, tool string) string {
	sum := sha256.Sum256([]byte(server + "\x00" + tool))
	return hex.EncodeToString(sum[:])[:8]
}

func suffixedName(base, server, tool string) string {
	suffix := "_" + nameHash(server, tool)
	if len(base)+len(suffix) > maxToolNameLength {
		base = base[:maxToolNameLength-len(suffix)]
	}
	return base + suffix
}

// buildCatalog converts SDK tools to named psi declarations. It computes
// names for every discovered tool before exposure filtering, so a list change
// cannot renumber a tool's name based on what happens to be exposed.
func buildCatalog(server Server, epoch uint64, tools []*sdk.Tool) ([]Tool, error) {
	type pending struct {
		raw    *sdk.Tool
		base   string
		schema map[string]any
	}
	items := make([]pending, 0, len(tools))
	seenRaw := map[string]bool{}
	bases := map[string]int{}
	for _, tool := range tools {
		if tool == nil {
			continue
		}
		if seenRaw[tool.Name] {
			return nil, configErr("catalog", "server %s returned duplicate tool %q", server.Name, tool.Name)
		}
		seenRaw[tool.Name] = true
		schema, err := normalizeSchema(tool.InputSchema)
		if err != nil {
			return nil, fmt.Errorf("tool %s: %w", tool.Name, err)
		}
		base := baseToolName(server.Name, tool.Name)
		items = append(items, pending{raw: tool, base: base, schema: schema})
		bases[base]++
	}
	seenNames := map[string]string{}
	out := make([]Tool, 0, len(items))
	for _, item := range items {
		name := item.base
		if len(item.base) > maxToolNameLength || bases[item.base] > 1 {
			name = suffixedName(item.base, server.Name, item.raw.Name)
		}
		if owner, ok := seenNames[name]; ok && owner != item.raw.Name {
			return nil, configErr("catalog", "server %s produced a tool name collision", server.Name)
		}
		seenNames[name] = item.raw.Name
		out = append(out, Tool{
			Name:        name,
			Description: toolDescription(server.Name, item.raw),
			Schema:      item.schema,
			Server:      server.Name,
			RawName:     item.raw.Name,
			Epoch:       epoch,
			Exposure:    effectiveExposure(server, item.raw.Name),
		})
	}
	return out, nil
}

func toolDescription(server string, tool *sdk.Tool) string {
	if desc := strings.TrimSpace(tool.Description); desc != "" {
		return desc
	}
	if title := strings.TrimSpace(tool.Title); title != "" {
		return title
	}
	if tool.Annotations != nil {
		if title := strings.TrimSpace(tool.Annotations.Title); title != "" {
			return title
		}
	}
	return fmt.Sprintf("MCP tool %s from server %s", tool.Name, server)
}

func normalizeSchema(raw any) (map[string]any, error) {
	schema, ok := raw.(map[string]any)
	if !ok || schema == nil {
		return nil, configErr("schema", "input schema must be a JSON object")
	}
	out := cloneMap(schema)
	if typ, present := out["type"]; !present || typ == nil {
		out["type"] = "object"
	}
	if _, present := out["properties"]; !present {
		out["properties"] = map[string]any{}
	}
	return out, nil
}

func cloneMap(in map[string]any) map[string]any {
	out := make(map[string]any, len(in))
	for key, value := range in {
		out[key] = cloneValue(value)
	}
	return out
}

func cloneValue(value any) any {
	switch v := value.(type) {
	case map[string]any:
		return cloneMap(v)
	case []any:
		out := make([]any, len(v))
		for i, item := range v {
			out[i] = cloneValue(item)
		}
		return out
	case []string:
		return append([]string(nil), v...)
	default:
		return v
	}
}

func effectiveExposure(server Server, rawTool string) Exposure {
	for _, rule := range server.ToolRules {
		if rule.Pattern == rawTool {
			return rule.Exposure
		}
	}
	for _, rule := range server.ToolRules {
		if strings.Contains(rule.Pattern, "*") && wildcardMatch(rule.Pattern, rawTool) {
			return rule.Exposure
		}
	}
	return server.Exposure
}

// wildcardMatch matches a pattern where only '*' is special.
func wildcardMatch(pattern, value string) bool {
	parts := strings.Split(pattern, "*")
	if len(parts) == 1 {
		return pattern == value
	}
	if !strings.HasPrefix(value, parts[0]) {
		return false
	}
	value = value[len(parts[0]):]
	for i := 1; i < len(parts)-1; i++ {
		idx := strings.Index(value, parts[i])
		if idx < 0 {
			return false
		}
		value = value[idx+len(parts[i]):]
	}
	return strings.HasSuffix(value, parts[len(parts)-1])
}

func declared(tool Tool) bool {
	return tool.Exposure == ExposureDirect || tool.Exposure == ExposureCodemode
}

func filterDeclared(tools []Tool) []Tool {
	out := make([]Tool, 0, len(tools))
	for _, tool := range tools {
		if declared(tool) {
			out = append(out, tool)
		}
	}
	return out
}
