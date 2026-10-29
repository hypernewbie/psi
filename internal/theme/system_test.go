package theme

import (
	"bufio"
	"encoding/json"
	"os"
	"reflect"
	"sort"
	"strings"
	"testing"
)

type systemReference struct {
	Name       string                     `json:"name"`
	Colors     map[string]json.RawMessage `json:"colors"`
	Dim        []string                   `json:"dim"`
	Appearance string                     `json:"appearance"`
}

func TestSystemThemeMatchesPinnedPiTokenFixtures(t *testing.T) {
	palette := []RGB{
		{0, 0, 0}, {180, 50, 50}, {50, 180, 50}, {180, 180, 50},
		{50, 50, 180}, {180, 50, 180}, {50, 180, 180}, {200, 200, 200},
		{80, 80, 80}, {255, 80, 80}, {80, 255, 80}, {255, 255, 80},
		{80, 80, 255}, {255, 80, 255}, {80, 255, 255}, {255, 255, 255},
	}
	zero := 0.0
	cases := map[string]SystemThemeInput{
		"darkNoPalette":      {Background: &RGB{30, 30, 30}, Foreground: &RGB{229, 229, 231}},
		"lightNoPalette":     {Background: &RGB{248, 248, 248}, Foreground: &RGB{20, 20, 20}},
		"grayNoPalette":      {Background: &RGB{128, 128, 128}, Foreground: &RGB{128, 128, 128}},
		"darkPalette":        {Background: &RGB{24, 24, 24}, Foreground: &RGB{235, 235, 235}, Palette: palette},
		"noReplies":          {},
		"noRepliesHintLight": {AppearanceHint: "light"},
		"pending":            {Background: &RGB{30, 30, 30}, Foreground: &RGB{229, 229, 231}, Saturation: &zero},
	}
	expected := map[string]systemReference{}
	file, err := os.Open("testdata/pi-system-theme.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 1024), 1<<20)
	for scanner.Scan() {
		var fixture systemReference
		if err := json.Unmarshal(scanner.Bytes(), &fixture); err != nil {
			t.Fatal(err)
		}
		expected[fixture.Name] = fixture
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	if len(expected) != len(cases) {
		t.Fatalf("reference fixtures=%d, cases=%d", len(expected), len(cases))
	}
	for name, input := range cases {
		t.Run(name, func(t *testing.T) {
			want, ok := expected[name]
			if !ok {
				t.Fatalf("missing pinned Pi fixture %q", name)
			}
			got := GenerateSystemThemeColors(input)
			if got.Appearance != want.Appearance {
				t.Errorf("appearance=%q, Pi=%q", got.Appearance, want.Appearance)
			}
			if len(got.Colors) != 56 || len(want.Colors) != 56 {
				t.Fatalf("token count Go=%d Pi=%d", len(got.Colors), len(want.Colors))
			}
			for token, raw := range want.Colors {
				value, err := ParseColorValue(raw)
				if err != nil {
					t.Fatalf("Pi fixture %s: %v", token, err)
				}
				if actual := got.Colors[token]; actual != value {
					t.Errorf("%s=%+v, Pi=%+v", token, actual, value)
				}
			}
			gotDim, wantDim := append([]string(nil), got.Dim...), append([]string(nil), want.Dim...)
			sort.Strings(gotDim)
			sort.Strings(wantDim)
			if !reflect.DeepEqual(gotDim, wantDim) {
				t.Errorf("dim roles=%v, Pi=%v", gotDim, wantDim)
			}
		})
	}
}

func TestSystemThemeBodyTextKeepsPiContrastOnGeneratedSurfaces(t *testing.T) {
	input := SystemThemeInput{Background: &RGB{30, 30, 30}, Foreground: &RGB{229, 229, 231}}
	generated := GenerateSystemThemeColors(input)
	for _, check := range []struct{ token, surface string }{
		{"text", "background"}, {"text", "selectedBg"}, {"userMessageText", "userMessageBg"},
		{"toolTitle", "toolPendingBg"}, {"toolTitle", "toolSuccessBg"}, {"toolTitle", "toolErrorBg"},
	} {
		textValue := generated.Colors[check.token]
		textColor := *input.Foreground
		if !textValue.IsDefault() {
			parsed, err := ParseValue(textValue)
			if err != nil {
				t.Fatal(err)
			}
			textColor, err = ColorToRGB(parsed)
			if err != nil {
				t.Fatal(err)
			}
		}
		var surface RGB
		if check.surface == "background" {
			surface = *input.Background
		} else {
			parsed, err := ParseValue(generated.Colors[check.surface])
			if err != nil {
				t.Fatal(err)
			}
			surface, err = ColorToRGB(parsed)
			if err != nil {
				t.Fatal(err)
			}
		}
		if contrast := WCAGContrast(textColor, surface); contrast < minimumWCAG {
			t.Errorf("%s on %s contrast %.3f, want >= %.1f", check.token, check.surface, contrast, minimumWCAG)
		}
	}
}

func TestNoReplySystemThemeKeepsPanelsDefaultAndNeutralDim(t *testing.T) {
	generated := GenerateSystemThemeColors(SystemThemeInput{AppearanceHint: "light"})
	for _, token := range panels {
		if !generated.Colors[token].IsDefault() {
			t.Errorf("panel %s has fixed color %+v", token, generated.Colors[token])
		}
	}
	if !containsString(generated.Dim, "muted") || !containsString(generated.Dim, "thinkingOff") {
		t.Fatalf("fallback dim roles=%v", generated.Dim)
	}
	compiled, err := NewResolvedTheme("system", generated.Appearance, generated.Colors, ColorMode256, generated.Dim)
	if err != nil {
		t.Fatal(err)
	}
	if !compiled.IsDimForeground("muted") || compiled.IsDimForeground("text") {
		t.Fatalf("fallback dim style not preserved")
	}
	styled, err := compiled.Style("muted", "muted", "", TextStyle{})
	if err != nil || !strings.Contains(styled, "\x1b[2m") {
		t.Fatalf("dim token style=%q, err=%v", styled, err)
	}
}
