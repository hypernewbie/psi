package theme

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

var okColorReference = regexp.MustCompile(`(?i)^ok(lch|hsl)\(`)

// ColorValue is one Pi theme color representation: a string literal/reference
// or an integer palette index. ParseColorValue deliberately leaves color-space
// parsing to the resolver/conversion layer.
type ColorValue struct {
	Text    string
	Index   int
	IsIndex bool
}

func ParseColorValue(raw json.RawMessage) (ColorValue, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) > 0 && trimmed[0] == '"' {
		var text string
		if err := json.Unmarshal(trimmed, &text); err != nil {
			return ColorValue{}, fmt.Errorf("invalid color string: %w", err)
		}
		return ColorValue{Text: text}, nil
	}
	if len(trimmed) == 0 || !strings.ContainsRune("-0123456789", rune(trimmed[0])) {
		return ColorValue{}, fmt.Errorf("color value must be a string or integer")
	}
	var number json.Number
	decoder := json.NewDecoder(bytes.NewReader(trimmed))
	decoder.UseNumber()
	if err := decoder.Decode(&number); err != nil {
		return ColorValue{}, fmt.Errorf("color value must be a string or integer")
	}
	f, err := strconv.ParseFloat(number.String(), 64)
	if err != nil || math.IsNaN(f) || math.IsInf(f, 0) || math.Trunc(f) != f || f < 0 || f > 255 {
		return ColorValue{}, fmt.Errorf("palette index must be an integer from 0 through 255")
	}
	return ColorValue{Index: int(f), IsIndex: true}, nil
}

func (v ColorValue) IsDefault() bool { return !v.IsIndex && v.Text == "" }

// ResolvedColor retains the token slot required by Pi; foreground and
// background tokens cannot be substituted for each other implicitly.
type ResolvedColor struct {
	Token Token
	Value ColorValue
}

// Document is a strictly validated Pi theme JSON document. Schema URLs are
// retained as inert metadata and are never fetched.
type Document struct {
	Schema     string
	Name       string
	Appearance string
	Vars       map[string]ColorValue
	Colors     map[string]ColorValue
	Export     map[string]ColorValue
}

var allowedTopLevel = map[string]bool{
	"$schema": true, "name": true, "appearance": true, "vars": true, "colors": true, "export": true,
}
var allowedExport = map[string]bool{"pageBg": true, "cardBg": true, "infoBg": true}

// Parse reads a Pi theme document. It accepts a UTF-8 BOM as Pi does, validates
// exact property names and all 56 registered color tokens, and does not fetch
// the optional $schema URL.
func Parse(data []byte) (*Document, error) {
	data = bytes.TrimPrefix(data, []byte{0xef, 0xbb, 0xbf})
	root, err := parseObject(data, "theme")
	if err != nil {
		return nil, err
	}
	if err := rejectUnknown(root, allowedTopLevel, "theme"); err != nil {
		return nil, err
	}
	doc := &Document{Vars: map[string]ColorValue{}, Colors: map[string]ColorValue{}, Export: map[string]ColorValue{}}
	if raw, ok := root["$schema"]; ok {
		value, err := parseJSONString(raw)
		if err != nil {
			return nil, fmt.Errorf("$schema must be a string")
		}
		doc.Schema = value
	}
	nameRaw, ok := root["name"]
	if !ok {
		return nil, fmt.Errorf("missing required property \"name\"")
	}
	doc.Name, err = parseJSONString(nameRaw)
	if err != nil || doc.Name == "" {
		return nil, fmt.Errorf("theme name must be a non-empty string")
	}
	if strings.Contains(doc.Name, "/") {
		return nil, fmt.Errorf("invalid theme name %q: theme names cannot contain '/' because it is reserved for automatic light/dark theme settings", doc.Name)
	}
	if raw, ok := root["appearance"]; ok {
		doc.Appearance, err = parseJSONString(raw)
		if err != nil || (doc.Appearance != "dark" && doc.Appearance != "light") {
			return nil, fmt.Errorf("appearance must be \"dark\" or \"light\"")
		}
	}
	if raw, ok := root["vars"]; ok {
		variables, err := parseObject(raw, "vars")
		if err != nil {
			return nil, err
		}
		for name, value := range variables {
			parsed, err := ParseColorValue(value)
			if err != nil {
				return nil, fmt.Errorf("vars.%s: %w", name, err)
			}
			doc.Vars[name] = parsed
		}
	}
	colorsRaw, ok := root["colors"]
	if !ok {
		return nil, fmt.Errorf("missing required property \"colors\"")
	}
	colors, err := parseObject(colorsRaw, "colors")
	if err != nil {
		return nil, err
	}
	knownTokens := make(map[string]bool, len(tokenRegistry))
	for _, token := range tokenRegistry {
		knownTokens[token.Name] = true
	}
	if err := rejectUnknown(colors, knownTokens, "colors"); err != nil {
		return nil, err
	}
	for _, token := range tokenRegistry {
		raw, present := colors[token.Name]
		if !present {
			if token.Fallback != "" {
				continue
			}
			return nil, fmt.Errorf("missing required color token %q", token.Name)
		}
		parsed, err := ParseColorValue(raw)
		if err != nil {
			return nil, fmt.Errorf("colors.%s: %w", token.Name, err)
		}
		doc.Colors[token.Name] = parsed
	}
	if raw, ok := root["export"]; ok {
		export, err := parseObject(raw, "export")
		if err != nil {
			return nil, err
		}
		if err := rejectUnknown(export, allowedExport, "export"); err != nil {
			return nil, err
		}
		for name, value := range export {
			parsed, err := ParseColorValue(value)
			if err != nil {
				return nil, fmt.Errorf("export.%s: %w", name, err)
			}
			doc.Export[name] = parsed
		}
	}
	return doc, nil
}

func parseJSONString(raw []byte) (string, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || trimmed[0] != '"' {
		return "", fmt.Errorf("expected a string")
	}
	var value string
	if err := json.Unmarshal(trimmed, &value); err != nil {
		return "", err
	}
	return value, nil
}

func parseObject(raw []byte, label string) (map[string]json.RawMessage, error) {
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil {
		return nil, fmt.Errorf("%s must be an object: %w", label, err)
	}
	if object == nil {
		return nil, fmt.Errorf("%s must be an object", label)
	}
	return object, nil
}

func rejectUnknown(object map[string]json.RawMessage, allowed map[string]bool, label string) error {
	var unknown []string
	for key := range object {
		if !allowed[key] {
			unknown = append(unknown, key)
		}
	}
	if len(unknown) == 0 {
		return nil
	}
	sort.Strings(unknown)
	return fmt.Errorf("unknown %s field(s): %s", label, strings.Join(unknown, ", "))
}

// ColorsWithFallbacks applies Pi's five missing-only fallbacks. An explicit
// empty string remains a terminal-default value and is never replaced.
func (d *Document) ColorsWithFallbacks() (map[string]ResolvedColor, error) {
	if d == nil {
		return nil, fmt.Errorf("theme document is nil")
	}
	values := make(map[string]ColorValue, len(tokenRegistry))
	for name, value := range d.Colors {
		values[name] = value
	}
	for _, token := range tokenRegistry {
		if _, ok := values[token.Name]; ok {
			continue
		}
		if token.Fallback == "" {
			return nil, fmt.Errorf("missing required color token %q", token.Name)
		}
		value, ok := values[token.Fallback]
		if !ok {
			return nil, fmt.Errorf("token %q fallback %q is missing", token.Name, token.Fallback)
		}
		values[token.Name] = value
	}
	resolved := make(map[string]ResolvedColor, len(tokenRegistry))
	for _, token := range tokenRegistry {
		value, err := d.resolve(values[token.Name], map[string]bool{})
		if err != nil {
			return nil, fmt.Errorf("colors.%s: %w", token.Name, err)
		}
		resolved[token.Name] = ResolvedColor{Token: token, Value: value}
	}
	return resolved, nil
}

// ResolveVariable resolves one string reference with Pi's chain and cycle
// rules. Literal syntax validation and color conversion are handled separately.
func (d *Document) ResolveVariable(value ColorValue) (ColorValue, error) {
	return d.resolve(value, map[string]bool{})
}

func (d *Document) resolve(value ColorValue, visited map[string]bool) (ColorValue, error) {
	if value.IsIndex || value.Text == "" || strings.HasPrefix(value.Text, "#") || okColorReference.MatchString(value.Text) {
		return value, nil
	}
	if visited[value.Text] {
		return ColorValue{}, fmt.Errorf("circular variable reference detected: %s", value.Text)
	}
	variable, ok := d.Vars[value.Text]
	if !ok {
		return ColorValue{}, fmt.Errorf("variable reference not found: %s", value.Text)
	}
	visited[value.Text] = true
	return d.resolve(variable, visited)
}
