package theme

import (
	"strings"
	"testing"
)

func TestParseOSCColorResponseFormatsAndTargets(t *testing.T) {
	cases := []struct {
		input string
		want  ColorReply
	}{
		{"\x1b]10;#102030\a", ColorReply{Target: "foreground", Color: &RGB{16, 32, 48}}},
		{"\x1b]11;#ffff00008000\x1b\\", ColorReply{Target: "background", Color: &RGB{255, 0, 128}}},
		{"\x1b]4;15;rgb:ff/00/80\a", ColorReply{Target: "palette", Index: 15, Color: &RGB{255, 0, 128}}},
		{"\x1b]4;0;rgba:a/80/ff\x1b\\", ColorReply{Target: "palette", Index: 0, Color: &RGB{170, 128, 255}}},
	}
	for _, test := range cases {
		got, ok := ParseOSCColorResponse(test.input)
		if !ok || got.Target != test.want.Target || got.Index != test.want.Index || got.Color == nil || *got.Color != *test.want.Color {
			t.Errorf("%q => %+v, %v; want %+v", test.input, got, ok, test.want)
		}
	}
	for _, malformed := range []string{"\x1b]4;16;#123456\a", "\x1b]10;#ffffff"} {
		if _, ok := ParseOSCColorResponse(malformed); ok {
			t.Errorf("accepted malformed response %q", malformed)
		}
	}
	for _, unusableValue := range []string{"\x1b]11;#gggggg\a", "\x1b]10;#1234\a", "\x1b]4;1;rgb:1/2\a", "\x1b]11;invalid\a"} {
		unusable, ok := ParseOSCColorResponse(unusableValue)
		if !ok || unusable.Color != nil {
			t.Errorf("syntactically valid unparseable value=%+v, %v", unusable, ok)
		}
	}
}

func TestAppearanceAndColorModeFollowPiPrecedence(t *testing.T) {
	if got, ok := ParseTerminalColorSchemeReport("\x1b[?997;2n"); !ok || got != "light" {
		t.Fatalf("light scheme=%q %v", got, ok)
	}
	if got := DetectColorFGBG("7;0"); got != "dark" {
		t.Fatalf("COLORFGBG dark=%q", got)
	}
	if got := DetectColorFGBG("default;15"); got != "light" {
		t.Fatalf("COLORFGBG light=%q", got)
	}
	if got := DetectTerminalAppearance(TerminalColors{}, "", "default;15"); got != "light" {
		t.Fatalf("fallback appearance=%q", got)
	}
	if got := DetectTerminalAppearance(TerminalColors{Background: &RGB{255, 255, 255}}, "dark", "0"); got != "light" {
		t.Fatalf("reported background precedence=%q", got)
	}
	if got := DetectColorMode("false", map[string]string{"PI_TRUE_COLOR": "1", "COLORTERM": "truecolor"}, "linux"); got != ColorMode256 {
		t.Fatalf("explicit false=%s", got)
	}
	if got := DetectColorMode("auto", map[string]string{"PI_TRUE_COLOR": "1"}, "linux"); got != ColorModeTruecolor {
		t.Fatalf("env override=%s", got)
	}
	if got := DetectColorMode("auto", map[string]string{"PI_TRUE_COLOR": "0", "COLORTERM": "truecolor"}, "linux"); got != ColorMode256 {
		t.Fatalf("environment override=%s", got)
	}
	if got := DetectColorMode("auto", map[string]string{"COLORTERM": "24bit"}, "linux"); got != ColorModeTruecolor {
		t.Fatalf("COLORTERM detection=%s", got)
	}
	if got := DetectColorMode("auto", nil, "windows"); got != ColorModeTruecolor {
		t.Fatalf("Windows detection=%s", got)
	}
}

func TestDeviceAttributesAndColorQuery(t *testing.T) {
	if !IsDeviceAttributesReply("\x1b[?1;2c") {
		t.Errorf("expected valid DA1 reply")
	}
	if IsDeviceAttributesReply("invalid") {
		t.Errorf("expected false for invalid DA1 reply")
	}
	longStr := "\x1b[?" + string(make([]byte, 70)) + "c"
	if IsDeviceAttributesReply(longStr) {
		t.Errorf("expected false for over-length reply")
	}

	query := ColorQuery()
	qStr := string(query)
	if !strings.HasPrefix(qStr, "\x1b]10;?\x07\x1b]11;?\x07") || !strings.HasSuffix(qStr, "\x1b[c") {
		t.Errorf("ColorQuery unexpected format: %q", qStr)
	}

	tc := TerminalColors{
		Foreground: &RGB{R: 200, G: 200, B: 200},
		Background: &RGB{R: 20, G: 20, B: 20},
		Palette:    []RGB{{R: 0, G: 0, B: 0}, {R: 255, G: 0, B: 0}},
	}
	sysIn := tc.SystemInput("dark", 0.8)
	if sysIn.AppearanceHint != "dark" || sysIn.Saturation == nil || *sysIn.Saturation != 0.8 {
		t.Errorf("SystemInput mismatch: %+v", sysIn)
	}
	if len(sysIn.Palette) != 2 {
		t.Errorf("SystemInput palette length = %d, want 2", len(sysIn.Palette))
	}
}
