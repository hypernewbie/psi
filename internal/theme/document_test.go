package theme

import (
	"encoding/json"
	"strings"
	"testing"
)

func themeFixture(t *testing.T, colors map[string]any, extra map[string]any) []byte {
	t.Helper()
	root := map[string]any{"name": "fixture", "colors": colors}
	for key, value := range extra {
		root[key] = value
	}
	data, err := json.Marshal(root)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func completeColors() map[string]any {
	colors := map[string]any{}
	for _, token := range Tokens() {
		if token.Fallback == "" {
			colors[token.Name] = ""
		}
	}
	return colors
}

func TestPinnedRegistryAndPiMissingOnlyFallbacks(t *testing.T) {
	tokens := Tokens()
	if len(tokens) != 56 {
		t.Fatalf("pinned token count=%d, want 56", len(tokens))
	}
	colors := completeColors()
	colors["selectedBg"] = 39
	colors["muted"] = "#123456"
	colors["text"] = ""
	colors["thinkingXhigh"] = "#abcdef"
	doc, err := Parse(themeFixture(t, colors, nil))
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := doc.ColorsWithFallbacks()
	if err != nil {
		t.Fatal(err)
	}
	checks := map[string]ColorValue{
		"scrollbarTrack":  {Text: "#123456"},
		"scrollbarThumb":  {Text: ""},
		"searchMatchBg":   {Index: 39, IsIndex: true},
		"searchMatchText": {Text: ""},
		"thinkingMax":     {Text: "#abcdef"},
	}
	for name, want := range checks {
		if got := resolved[name].Value; got != want {
			t.Errorf("%s fallback=%+v, want %+v", name, got, want)
		}
	}
	if !resolved["userMessageBg"].Value.IsDefault() || resolved["userMessageBg"].Token.Slot != Background {
		t.Fatalf("explicit terminal default or token slot changed: %+v", resolved["userMessageBg"])
	}
}

func TestPiThemeStrictFieldsAndRequiredRoles(t *testing.T) {
	cases := []struct {
		name string
		data []byte
		want string
	}{
		{"unknown top-level", themeFixture(t, completeColors(), map[string]any{"Theme": "dark"}), "unknown theme field(s): Theme"},
		{"unknown exact color", themeFixture(t, func() map[string]any { c := completeColors(); c["Accent"] = "red"; return c }(), nil), "unknown colors field(s): Accent"},
		{"slash name", themeFixture(t, completeColors(), map[string]any{"name": "light/dark"}), "cannot contain '/'"},
		{"unknown export", themeFixture(t, completeColors(), map[string]any{"export": map[string]any{"pageBG": "#fff"}}), "unknown export field(s): pageBG"},
		{"null schema", themeFixture(t, completeColors(), map[string]any{"$schema": nil}), "$schema must be a string"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Parse(tc.data)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Parse error=%v, want substring %q", err, tc.want)
			}
		})
	}
	missing := completeColors()
	delete(missing, "mdHeading")
	if _, err := Parse(themeFixture(t, missing, nil)); err == nil || !strings.Contains(err.Error(), "mdHeading") {
		t.Fatalf("missing required role error=%v", err)
	}
}

func TestPiThemeBOMVariablesAndInertSchema(t *testing.T) {
	colors := completeColors()
	colors["accent"] = "primary"
	doc, err := Parse(append([]byte{0xef, 0xbb, 0xbf}, themeFixture(t, colors, map[string]any{
		"$schema":    "https://invalid.example/theme.schema.json",
		"appearance": "dark",
		"vars":       map[string]any{"primary": "secondary", "secondary": "#0af"},
		"export":     map[string]any{"pageBg": 39, "cardBg": "", "infoBg": "primary"},
	})...))
	if err != nil {
		t.Fatal(err)
	}
	if doc.Schema == "" || doc.Appearance != "dark" {
		t.Fatalf("schema metadata or appearance lost: %+v", doc)
	}
	resolved, err := doc.ColorsWithFallbacks()
	if err != nil {
		t.Fatal(err)
	}
	if got := resolved["accent"].Value; got != (ColorValue{Text: "#0af"}) {
		t.Fatalf("chained variable resolved to %+v", got)
	}
}

func TestPiThemeVariablesRejectMissingAndCyclicReferences(t *testing.T) {
	for _, tc := range []struct {
		vars        map[string]any
		value, want string
	}{
		{map[string]any{}, "missing", "variable reference not found: missing"},
		{map[string]any{"a": "b", "b": "a"}, "a", "circular variable reference detected: a"},
	} {
		colors := completeColors()
		colors["accent"] = tc.value
		doc, err := Parse(themeFixture(t, colors, map[string]any{"vars": tc.vars}))
		if err != nil {
			t.Fatal(err)
		}
		_, err = doc.ColorsWithFallbacks()
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("resolve error=%v, want %q", err, tc.want)
		}
	}
}

func TestPiThemeColorValueAcceptsOnlyStringOrPaletteInteger(t *testing.T) {
	for _, raw := range []string{`null`, `true`, `256`, `-1`, `1.5`, `{}`, `[]`} {
		if _, err := ParseColorValue(json.RawMessage(raw)); err == nil {
			t.Errorf("accepted invalid color value %s", raw)
		}
	}
	for _, raw := range []string{`""`, `"okhsl(250 60% 55%)"`, `0`, `39`, `255`, `39.0`} {
		if _, err := ParseColorValue(json.RawMessage(raw)); err != nil {
			t.Errorf("rejected valid schema color value %s: %v", raw, err)
		}
	}
}
