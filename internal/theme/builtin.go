package theme

import (
	"embed"
	"fmt"
)

// These unmodified palettes are from Pi d5b3de22d0e24a3e9c38a9410d3329b099717283.
// Their upstream SHA-256 values are documented in docs/plan5-acceptance.md.
//
//go:embed themes/dark.json themes/light.json
var builtinFiles embed.FS

// BuiltinJSON returns a copy of one embedded Pi theme document.
func BuiltinJSON(name string) ([]byte, error) {
	if name != "dark" && name != "light" {
		return nil, fmt.Errorf("unknown built-in theme: %s", name)
	}
	data, err := builtinFiles.ReadFile("themes/" + name + ".json")
	if err != nil {
		return nil, err
	}
	return append([]byte(nil), data...), nil
}

// BuiltinDocument parses one embedded Pi theme with the same strict loader as
// custom theme data.
func BuiltinDocument(name string) (*Document, error) {
	data, err := BuiltinJSON(name)
	if err != nil {
		return nil, err
	}
	doc, err := Parse(data)
	if err != nil {
		return nil, fmt.Errorf("built-in theme %s: %w", name, err)
	}
	return doc, nil
}
