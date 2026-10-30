package theme

import (
	"encoding/json"
	"strconv"
	"testing"
)

func TestPiColorConversionFixtures(t *testing.T) {
	fixtures := []struct{ input, hex string }{
		{"#abc", "#aabbcc"},
		{"#ABCDEF", "#abcdef"},
		{"oklch(60% 0.2 30)", "#de3e2d"},
		{"okhsl(232 54% 67%)", "#69add0"},
		{"okhsl(295 73% 59%)", "#9776e5"},
		{"oklch(100% 0.3 150)", "#ffffff"},
		{"okhsl(52 67% 67%)", "#de8d5a"},
		{"oklch(.6 2e-1 30deg)", "#de3e2d"},
		{"okhsl(232deg .54 67%)", "#69add0"},
		{"okhsl(232 54% 67.%)", "#69add0"},
	}
	for _, fixture := range fixtures {
		t.Run(fixture.input, func(t *testing.T) {
			color, err := ParseColor(fixture.input)
			if err != nil {
				t.Fatal(err)
			}
			hex, err := ColorToHex(color)
			if err != nil {
				t.Fatal(err)
			}
			if hex != fixture.hex {
				t.Fatalf("Pi conversion=%s, want %s", hex, fixture.hex)
			}
		})
	}
}

func TestPiIndexedAndTerminalDefaultColorModes(t *testing.T) {
	indexed, err := ParseColorValue(json.RawMessage(`39`))
	if err != nil {
		t.Fatal(err)
	}
	color, err := ParseValue(indexed)
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := ColorToHex(color); got != "#00afff" {
		t.Fatalf("index 39 hex=%s", got)
	}
	ansi256, _ := ForegroundANSI(color, ColorMode256)
	truecolor, _ := ForegroundANSI(color, ColorModeTruecolor)
	if ansi256 != "\x1b[38;5;39m" || truecolor != "\x1b[38;5;39m" {
		t.Fatalf("indexed color modes: %q %q", ansi256, truecolor)
	}
	rgb, _ := ParseColor("#00afff")
	truecolorRGB, _ := ForegroundANSI(rgb, ColorModeTruecolor)
	if truecolorRGB != "\x1b[38;2;0;175;255m" {
		t.Fatalf("truecolor RGB ANSI=%q", truecolorRGB)
	}
	defaultColor, err := ParseValue(ColorValue{Text: ""})
	if err != nil {
		t.Fatal(err)
	}
	fg, _ := ForegroundANSI(defaultColor, ColorModeTruecolor)
	bg, _ := BackgroundANSI(defaultColor, ColorModeTruecolor)
	if fg != "\x1b[39m" || bg != "\x1b[49m" {
		t.Fatalf("default colors: %q %q", fg, bg)
	}
}

func TestPi256ColorQuantizationFixtures(t *testing.T) {
	fixtures := map[string]int{"#abcdef": 153, "#808080": 244, "#f0f0f0": 255, "#222222": 235, "#ffcc00": 220}
	for input, index := range fixtures {
		color, err := ParseColor(input)
		if err != nil {
			t.Fatal(err)
		}
		got, err := ForegroundANSI(color, ColorMode256)
		if err != nil {
			t.Fatal(err)
		}
		want := "\x1b[38;5;" + strconv.Itoa(index) + "m"
		if got != want {
			t.Errorf("%s ANSI=%q, want %q", input, got, want)
		}
	}
}

func TestPiColorParserRejectsInvalidSyntaxAndRanges(t *testing.T) {
	for _, input := range []string{"#12", "#ggg", "oklch(110% 0.2 30)", "oklch(50% -0.1 30)", "okhsl(0 101% 50%)", "okhsl(0 20% -1%)", "red", " oklch(50% 0.2 30)"} {
		if _, err := ParseColor(input); err == nil {
			t.Errorf("accepted %q", input)
		}
	}
}

func TestMixColorsSRGBAndOKLCH(t *testing.T) {
	c1, err := ParseColor("#000000")
	if err != nil {
		t.Fatal(err)
	}
	c2, err := ParseColor("#ffffff")
	if err != nil {
		t.Fatal(err)
	}

	// Mix 50% srgb
	midSRGB, err := MixColors(c1, c2, 0.5, true)
	if err != nil {
		t.Fatal(err)
	}
	hex, _ := ColorToHex(midSRGB)
	if hex != "#808080" {
		t.Errorf("MixColors(srgb 0.5) = %s, want #808080", hex)
	}

	// Mix 50% oklch
	midOKLCH, err := MixColors(c1, c2, 0.5, false)
	if err != nil {
		t.Fatal(err)
	}
	if hex, _ := ColorToHex(midOKLCH); hex == "" {
		t.Errorf("MixColors(oklch 0.5) produced empty hex")
	}

	// Invalid amounts
	if _, err := MixColors(c1, c2, -0.1, true); err == nil {
		t.Errorf("expected error for amount < 0")
	}
	if _, err := MixColors(c1, c2, 1.1, true); err == nil {
		t.Errorf("expected error for amount > 1")
	}
}

