package theme

import (
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
)

// ColorKind uses Pi's four runtime color variants. Default colors are kept
// distinct because an empty theme string means the terminal's default color.
type ColorKind uint8

const (
	ColorDefault ColorKind = iota
	ColorIndexed
	ColorRGB
	ColorOKLCH
)

// RGB channels use 0-255 floats, matching Pi's TUI color type.
type RGB struct{ R, G, B float64 }

// Color retains indexed colors until serialization, and retains OKLCH so gamut
// mapping and color-space operations match Pi.
type Color struct {
	Kind    ColorKind
	Index   int
	R, G, B float64
	L, C, H float64
}

type ColorMode string

const (
	ColorMode256       ColorMode = "256color"
	ColorModeTruecolor ColorMode = "truecolor"
)

const numberPattern = `([-+]?[0-9]+\.?[0-9]*|[-+]?\.[0-9]+)([eE][-+]?[0-9]+)?`

var (
	hexColorPattern = regexp.MustCompile(`^#([[:xdigit:]]{3}|[[:xdigit:]]{6})$`)
	oklchPattern    = regexp.MustCompile(`(?i)^oklch\(\s*` + numberPattern + `(%?)\s+` + numberPattern + `\s+` + numberPattern + `(deg)?\s*\)$`)
	okhslPattern    = regexp.MustCompile(`(?i)^okhsl\(\s*` + numberPattern + `(deg)?\s+` + numberPattern + `(%?)\s+` + numberPattern + `(%?)\s*\)$`)
)

// ParseColor converts Pi's accepted hex, OKLCH, OKHSL, and indexed color
// forms. Empty strings are terminal defaults and are handled by ParseValue.
func ParseColor(value string) (Color, error) {
	if match := hexColorPattern.FindStringSubmatch(value); match != nil {
		digits := match[1]
		if len(digits) == 3 {
			digits = strings.Repeat(string(digits[0]), 2) + strings.Repeat(string(digits[1]), 2) + strings.Repeat(string(digits[2]), 2)
		}
		red, _ := strconv.ParseUint(digits[0:2], 16, 8)
		green, _ := strconv.ParseUint(digits[2:4], 16, 8)
		blue, _ := strconv.ParseUint(digits[4:6], 16, 8)
		return RGBColor(float64(red), float64(green), float64(blue)), nil
	}
	if match := oklchPattern.FindStringSubmatch(value); match != nil {
		lightness, err := parseFloatParts(match, 1)
		if err != nil {
			return Color{}, err
		}
		chroma, err := parseFloatParts(match, 4)
		if err != nil {
			return Color{}, err
		}
		hue, err := parseFloatParts(match, 6)
		if err != nil {
			return Color{}, err
		}
		if match[3] == "%" {
			lightness /= 100
		}
		return OKLCHColor(lightness, chroma, hue)
	}
	if match := okhslPattern.FindStringSubmatch(value); match != nil {
		hue, err := parseFloatParts(match, 1)
		if err != nil {
			return Color{}, err
		}
		saturation, err := parseFloatParts(match, 4)
		if err != nil {
			return Color{}, err
		}
		lightness, err := parseFloatParts(match, 7)
		if err != nil {
			return Color{}, err
		}
		if match[6] == "%" {
			saturation /= 100
		}
		if match[9] == "%" {
			lightness /= 100
		}
		return OKHSLColor(hue, saturation, lightness)
	}
	return Color{}, fmt.Errorf("invalid color value: %s", value)
}

func parseFloatParts(match []string, component int) (float64, error) {
	parsed, err := strconv.ParseFloat(match[component]+match[component+1], 64)
	if err != nil || math.IsNaN(parsed) || math.IsInf(parsed, 0) {
		return 0, fmt.Errorf("invalid finite color component %q", match[component]+match[component+1])
	}
	return parsed, nil
}

func IndexedColor(index int) (Color, error) {
	if index < 0 || index > 255 {
		return Color{}, fmt.Errorf("ANSI color index must be an integer from 0 to 255: %d", index)
	}
	return Color{Kind: ColorIndexed, Index: index}, nil
}
func RGBColor(r, g, b float64) Color {
	return Color{Kind: ColorRGB, R: r, G: g, B: b}
}
func OKLCHColor(l, c, h float64) (Color, error) {
	if !finite(l) || !finite(c) || !finite(h) {
		return Color{}, fmt.Errorf("OKLCH components must be finite")
	}
	if l < 0 || l > 1 {
		return Color{}, fmt.Errorf("OKLCH lightness must be between 0 and 1: %v", l)
	}
	if c < 0 {
		return Color{}, fmt.Errorf("OKLCH chroma must not be negative: %v", c)
	}
	return Color{Kind: ColorOKLCH, L: l, C: c, H: positiveModulo(h, 360)}, nil
}
func OKHSLColor(h, s, l float64) (Color, error) {
	if !finite(h) || !finite(s) || !finite(l) {
		return Color{}, fmt.Errorf("OKHSL components must be finite")
	}
	if s < 0 || s > 1 {
		return Color{}, fmt.Errorf("OKHSL saturation must be between 0 and 1: %v", s)
	}
	if l < 0 || l > 1 {
		return Color{}, fmt.Errorf("OKHSL lightness must be between 0 and 1: %v", l)
	}
	rgb := okhslToRGB(h, s, l)
	return RGBColor(rgb.R, rgb.G, rgb.B), nil
}
func finite(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) }

func ParseValue(value ColorValue) (Color, error) {
	if value.IsIndex {
		return IndexedColor(value.Index)
	}
	if value.Text == "" {
		return Color{Kind: ColorDefault}, nil
	}
	return ParseColor(value.Text)
}

var basicColors = [16]RGB{
	{0, 0, 0}, {128, 0, 0}, {0, 128, 0}, {128, 128, 0},
	{0, 0, 128}, {128, 0, 128}, {0, 128, 128}, {192, 192, 192},
	{128, 128, 128}, {255, 0, 0}, {0, 255, 0}, {255, 255, 0},
	{0, 0, 255}, {255, 0, 255}, {0, 255, 255}, {255, 255, 255},
}
var cubeValues = [6]float64{0, 95, 135, 175, 215, 255}

func ColorToRGB(color Color) (RGB, error) {
	switch color.Kind {
	case ColorIndexed:
		if color.Index < 0 || color.Index > 255 {
			return RGB{}, fmt.Errorf("invalid ANSI color index: %d", color.Index)
		}
		if color.Index < 16 {
			return basicColors[color.Index], nil
		}
		if color.Index < 232 {
			i := color.Index - 16
			return RGB{cubeValues[i/36], cubeValues[(i%36)/6], cubeValues[i%6]}, nil
		}
		gray := float64(8 + (color.Index-232)*10)
		return RGB{gray, gray, gray}, nil
	case ColorRGB:
		for _, channel := range []float64{color.R, color.G, color.B} {
			if !finite(channel) || channel < 0 || channel > 255 {
				return RGB{}, fmt.Errorf("RGB channels must be finite and between 0 and 255")
			}
		}
		return RGB{color.R, color.G, color.B}, nil
	case ColorOKLCH:
		return oklchToRGB(color), nil
	case ColorDefault:
		return RGB{}, fmt.Errorf("terminal-default color has no fixed RGB value")
	default:
		return RGB{}, fmt.Errorf("unknown color kind: %d", color.Kind)
	}
}

func oklchToRGB(color Color) RGB {
	radians := color.H * math.Pi / 180
	cosine, sine := math.Cos(radians), math.Sin(radians)
	atChroma := func(chroma float64) vector { return oklabToLinearSRGB(vector{color.L, chroma * cosine, chroma * sine}) }
	direct := atChroma(color.C)
	if inSRGBGamut(direct) {
		return linearSRGBToRGB(direct)
	}
	linear := atChroma(0)
	low, high := 0.0, color.C
	for i := 0; i < 20; i++ {
		chroma := (low + high) / 2
		candidate := atChroma(chroma)
		if inSRGBGamut(candidate) {
			low, linear = chroma, candidate
		} else {
			high = chroma
		}
	}
	return linearSRGBToRGB(linear)
}
func inSRGBGamut(linear vector) bool {
	const epsilon = 1e-7
	return linear[0] >= -epsilon && linear[0] <= 1+epsilon && linear[1] >= -epsilon && linear[1] <= 1+epsilon && linear[2] >= -epsilon && linear[2] <= 1+epsilon
}

func ColorToOKLCH(color Color) (l, c, h float64, err error) {
	if color.Kind == ColorOKLCH {
		return color.L, color.C, color.H, nil
	}
	rgb, err := ColorToRGB(color)
	if err != nil {
		return 0, 0, 0, err
	}
	lab := rgbToOklab(rgb)
	return lab[0], math.Hypot(lab[1], lab[2]), positiveModulo(math.Atan2(lab[2], lab[1])*180/math.Pi, 360), nil
}
func ColorToOKHSL(color Color) (h, s, l float64, err error) {
	rgb, err := ColorToRGB(color)
	if err != nil {
		return 0, 0, 0, err
	}
	h, s, l = rgbToOKHSL(rgb)
	return h, s, l, nil
}

func ColorToHex(color Color) (string, error) {
	rgb, err := ColorToRGB(color)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("#%02x%02x%02x", int(math.Round(rgb.R)), int(math.Round(rgb.G)), int(math.Round(rgb.B))), nil
}

func MixColors(first, second Color, amount float64, srgb bool) (Color, error) {
	if !finite(amount) || amount < 0 || amount > 1 {
		return Color{}, fmt.Errorf("mix amount must be between 0 and 1: %v", amount)
	}
	if srgb {
		a, err := ColorToRGB(first)
		if err != nil {
			return Color{}, err
		}
		b, err := ColorToRGB(second)
		if err != nil {
			return Color{}, err
		}
		return RGBColor(a.R+(b.R-a.R)*amount, a.G+(b.G-a.G)*amount, a.B+(b.B-a.B)*amount), nil
	}
	al, ac, ah, err := ColorToOKLCH(first)
	if err != nil {
		return Color{}, err
	}
	bl, bc, bh, err := ColorToOKLCH(second)
	if err != nil {
		return Color{}, err
	}
	if ac < 1e-7 {
		ah = bh
	}
	if bc < 1e-7 {
		bh = ah
	}
	hueDelta := math.Mod(bh-ah+540, 360) - 180
	return OKLCHColor(al+(bl-al)*amount, ac+(bc-ac)*amount, ah+hueDelta*amount)
}

func ForegroundANSI(color Color, mode ColorMode) (string, error) {
	return colorANSI(color, mode, false)
}
func BackgroundANSI(color Color, mode ColorMode) (string, error) { return colorANSI(color, mode, true) }
func colorANSI(color Color, mode ColorMode, background bool) (string, error) {
	if color.Kind == ColorDefault {
		if background {
			return "\x1b[49m", nil
		}
		return "\x1b[39m", nil
	}
	if color.Kind == ColorIndexed {
		if color.Index < 0 || color.Index > 255 {
			return "", fmt.Errorf("invalid ANSI color index: %d", color.Index)
		}
		base := 38
		if background {
			base = 48
		}
		return fmt.Sprintf("\x1b[%d;5;%dm", base, color.Index), nil
	}
	rgb, err := ColorToRGB(color)
	if err != nil {
		return "", err
	}
	base := 38
	if background {
		base = 48
	}
	if mode == ColorModeTruecolor {
		return fmt.Sprintf("\x1b[%d;2;%d;%d;%dm", base, int(math.Round(rgb.R)), int(math.Round(rgb.G)), int(math.Round(rgb.B))), nil
	}
	return fmt.Sprintf("\x1b[%d;5;%dm", base, rgbToANSI256(rgb)), nil
}

func rgbToANSI256(rgb RGB) int {
	rIndex, gIndex, bIndex := findClosest(cubeValues[:], rgb.R), findClosest(cubeValues[:], rgb.G), findClosest(cubeValues[:], rgb.B)
	cube := RGB{cubeValues[rIndex], cubeValues[gIndex], cubeValues[bIndex]}
	cubeIndex := 16 + 36*rIndex + 6*gIndex + bIndex
	gray := math.Round(0.299*rgb.R + 0.587*rgb.G + 0.114*rgb.B)
	grayValues := make([]float64, 24)
	for i := range grayValues {
		grayValues[i] = float64(8 + i*10)
	}
	grayOffset := findClosest(grayValues, gray)
	grayValue := grayValues[grayOffset]
	spread := math.Max(rgb.R, math.Max(rgb.G, rgb.B)) - math.Min(rgb.R, math.Min(rgb.G, rgb.B))
	if spread < 10 && colorDistance(rgb, RGB{grayValue, grayValue, grayValue}) < colorDistance(rgb, cube) {
		return 232 + grayOffset
	}
	return cubeIndex
}
func findClosest(values []float64, target float64) int {
	closestIndex, closestDistance := 0, math.Inf(1)
	for i, value := range values {
		distance := math.Abs(target - value)
		if distance < closestDistance {
			closestIndex, closestDistance = i, distance
		}
	}
	return closestIndex
}
func colorDistance(first, second RGB) float64 {
	dr, dg, db := first.R-second.R, first.G-second.G, first.B-second.B
	return dr*dr*0.299 + dg*dg*0.587 + db*db*0.114
}
