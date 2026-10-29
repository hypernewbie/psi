package theme

import (
	"fmt"
	"math"
)

// SystemThemeInput contains only terminal color replies needed by Pi's system
// theme. Palette is used only when all 16 ANSI colors were reported.
type SystemThemeInput struct {
	Foreground     *RGB     `json:"foreground,omitempty"`
	Background     *RGB     `json:"background,omitempty"`
	Palette        []RGB    `json:"palette,omitempty"`
	Saturation     *float64 `json:"saturation,omitempty"` // nil selects Pi's default of 1
	AppearanceHint string   `json:"appearanceHint,omitempty"`
}

// SystemThemeColors contains Pi-role values and the no-palette dim treatment.
type SystemThemeColors struct {
	Colors     map[string]ColorValue
	Dim        []string
	Appearance string
}

func oklabLightness(rgb RGB) float64 {
	color := RGBColor(rgb.R, rgb.G, rgb.B)
	l, _, _, _ := ColorToOKLCH(color)
	return l
}

// RelativeLuminance returns WCAG 2 relative luminance for sRGB channels.
func RelativeLuminance(rgb RGB) float64 {
	linear := func(channel float64) float64 {
		value := channel / 255
		if value <= 0.04045 {
			return value / 12.92
		}
		return math.Pow((value+0.055)/1.055, 2.4)
	}
	return 0.2126*linear(rgb.R) + 0.7152*linear(rgb.G) + 0.0722*linear(rgb.B)
}

// WCAGContrast returns the WCAG 2 contrast ratio, from 1 through 21.
func WCAGContrast(first, second RGB) float64 {
	a, b := RelativeLuminance(first), RelativeLuminance(second)
	return (math.Max(a, b) + 0.05) / (math.Min(a, b) + 0.05)
}

// TerminalAppearance follows Pi's foreground/background contrast and WCAG
// fallback rules. The caller supplies a background already reported by OSC 11.
func TerminalAppearance(background RGB, foreground *RGB) string {
	white, black := RGB{255, 255, 255}, RGB{0, 0, 0}
	whiteContrast, blackContrast := WCAGContrast(white, background), WCAGContrast(black, background)
	if foreground != nil {
		foregroundL, backgroundL := oklabLightness(*foreground), oklabLightness(background)
		if math.Abs(foregroundL-backgroundL) > 0.05 {
			appearance := "dark"
			best := whiteContrast
			if foregroundL <= backgroundL {
				appearance, best = "light", blackContrast
			}
			if best >= minimumWCAG {
				return appearance
			}
		}
	}
	if whiteContrast >= blackContrast {
		return "dark"
	}
	return "light"
}

func clamp(value, low, high float64) float64 { return math.Min(high, math.Max(low, value)) }
func hexOf(rgb RGB) string {
	return fmt.Sprintf("#%02x%02x%02x", int(math.Round(rgb.R)), int(math.Round(rgb.G)), int(math.Round(rgb.B)))
}
func bellWeight(lightness float64) float64 {
	gaussian := func(x float64) float64 { return math.Exp(-math.Pow(x-0.5, 2) / (2 * math.Pow(0.25, 2))) }
	return (gaussian(lightness) - gaussian(0)) / (1 - gaussian(0))
}
func saturationCurve(f family, lightness float64) float64 {
	floor := 1.0
	if f.max > 0 {
		floor = f.min / f.max
	}
	return floor + (1-floor)*bellWeight(lightness)
}
func target(level, appearance string, surfaceL, relaxation, extreme float64) (float64, bool) {
	reached, ok := levelTarget(level, appearance, surfaceL)
	if !ok && relaxation == 0 {
		return 0, false
	}
	if !ok {
		reached = extreme
	}
	floor, floorOK := levelTarget(readableFloor[appearance], appearance, surfaceL)
	if !floorOK {
		floor = extreme
	}
	distance := reached - surfaceL
	floorDistance := floor - surfaceL
	compressed := distance
	if math.Abs(distance) > math.Abs(floorDistance) {
		compressed = distance - (distance-floorDistance)*math.Min(relaxation, 1)
	}
	return surfaceL + compressed*(1-math.Max(0, relaxation-1)), true
}

type sourceColor struct {
	hue, saturation, lightness float64
	chroma                     float64
}

func sourceOf(rgb RGB) sourceColor {
	color := RGBColor(rgb.R, rgb.G, rgb.B)
	hue, saturation, lightness, _ := ColorToOKHSL(color)
	_, chroma, _, _ := ColorToOKLCH(color)
	return sourceColor{hue, saturation, lightness, chroma}
}

func anchored(source sourceColor, f family, lightness, saturation float64) RGB {
	anchor := saturationCurve(f, source.lightness)
	falloff := 1.0
	if anchor > 0 {
		falloff = math.Min(1, saturationCurve(f, lightness)/anchor)
	}
	color, _ := OKHSLColor(source.hue, source.saturation*falloff*saturation, lightness)
	rgb, _ := ColorToRGB(color)
	_, chroma, _, _ := ColorToOKLCH(color)
	cap := source.chroma * falloff * saturation
	if chroma <= cap {
		return rgb
	}
	l, _, _, _ := ColorToOKLCH(color)
	mapped, _ := OKLCHColor(l, cap, source.hue)
	result, _ := ColorToRGB(mapped)
	return result
}

func paint(token string, lightness float64, palette []sourceColor, saturation float64) RGB {
	f := families[tokenFamilies[token]]
	if len(palette) == 0 {
		okhslL := oklabToOkhslLightness(lightness)
		return rgbFromOKHSL(f.hue, (f.min+(f.max-f.min)*bellWeight(okhslL))*saturation, okhslL)
	}
	slot, ok := tokenSlots[token]
	if !ok {
		slot = f.slot
	}
	return anchored(palette[slot], f, oklabToOkhslLightness(lightness), saturation)
}

func rgbFromOKHSL(hue, saturation, lightness float64) RGB {
	color, _ := OKHSLColor(hue, saturation, lightness)
	rgb, _ := ColorToRGB(color)
	return rgb
}

func readableText(_ RGB, surfaces []RGB, lighter bool) bool {
	extreme := RGB{0, 0, 0}
	if lighter {
		extreme = RGB{255, 255, 255}
	}
	for _, surface := range surfaces {
		if WCAGContrast(extreme, surface) < minimumWCAG {
			return false
		}
	}
	return true
}

func readablePanel(color RGB, lighter bool) bool {
	extreme := RGB{0, 0, 0}
	if lighter {
		extreme = RGB{255, 255, 255}
	}
	return WCAGContrast(extreme, color) >= minimumWCAG
}

func limitPanel(token string, l, backgroundL, saturation float64, palette []sourceColor, lighter bool) RGB {
	color := paint(token, l, palette, saturation)
	if readablePanel(color, lighter) {
		return color
	}
	low, high := backgroundL, l
	for i := 0; i < 20; i++ {
		middle := (low + high) / 2
		candidate := paint(token, middle, palette, saturation)
		if readablePanel(candidate, lighter) {
			low = middle
		} else {
			high = middle
		}
	}
	return paint(token, low, palette, saturation)
}

// GenerateSystemThemeColors implements Pi's palette-derived, background-only,
// and indexed/default tiers. It returns role data; it performs no terminal I/O.
func GenerateSystemThemeColors(input SystemThemeInput) SystemThemeColors {
	saturation := 1.0
	if input.Saturation != nil {
		saturation = clamp(*input.Saturation, 0, 1)
	}
	if input.Background == nil {
		return indexedSystemColors(saturation, input.AppearanceHint)
	}
	var palette []sourceColor
	if len(input.Palette) == 16 {
		palette = make([]sourceColor, 16)
		for i, color := range input.Palette {
			palette[i] = sourceOf(color)
		}
	}
	background := *input.Background
	appearance := TerminalAppearance(background, input.Foreground)
	lighter := appearance == "dark"
	extreme := 0.0
	if lighter {
		extreme = 1
	}
	backgroundL := oklabLightness(background)

	solve := func(relaxation float64) (map[string]RGB, bool) {
		colors := map[string]RGB{"background": background}
		for _, token := range solveOrder {
			targets := []float64{}
			for _, rule := range rules {
				if rule.token != token {
					continue
				}
				for _, surface := range rule.on {
					surfaceRGB, ok := colors[surface]
					if !ok {
						surfaceRGB = background
					}
					value, ok := target(rule.level, appearance, oklabLightness(surfaceRGB), relaxation, extreme)
					if !ok || value < 0 || value > 1 {
						return nil, false
					}
					targets = append(targets, value)
				}
			}
			l := targets[0]
			for _, candidate := range targets[1:] {
				if lighter && candidate > l || !lighter && candidate < l {
					l = candidate
				}
			}
			if containsString(panels, token) {
				colors[token] = limitPanel(token, l, backgroundL, saturation, palette, lighter)
			} else {
				colors[token] = paint(token, l, palette, saturation)
			}
		}
		return colors, true
	}

	relaxation := 0.0
	colors, ok := solve(0)
	if !ok {
		low, high := 0.0, 2.0
		colors, _ = solve(high)
		for i := 0; i < 20; i++ {
			middle := (low + high) / 2
			attempt, valid := solve(middle)
			if valid {
				high, colors = middle, attempt
			} else {
				low = middle
			}
		}
		relaxation = high
	}
	result := SystemThemeColors{Colors: make(map[string]ColorValue, len(tokenFamilies)), Appearance: appearance}
	for token := range tokenFamilies {
		if color, ok := colors[token]; ok {
			result.Colors[token] = ColorValue{Text: hexOf(color)}
		} else {
			result.Colors[token] = ColorValue{Text: ""}
		}
	}
	for _, token := range foregroundTokens {
		surfaces := tokenSurfaces(token, colors, background)
		text, hasText := colors[token]
		if input.Foreground != nil {
			targets := make([]float64, 0, len(surfaces))
			available := true
			for _, surface := range surfaces {
				value, ok := target(foregroundLevel, appearance, oklabLightness(surface), relaxation, extreme)
				if !ok || value < 0 || value > 1 {
					available = false
					break
				}
				targets = append(targets, value)
			}
			if available && len(targets) > 0 {
				needed := targets[0]
				for _, candidate := range targets[1:] {
					if lighter && candidate > needed || !lighter && candidate < needed {
						needed = candidate
					}
				}
				foregroundL := oklabLightness(*input.Foreground)
				if lighter && foregroundL >= needed || !lighter && foregroundL <= needed {
					result.Colors[token] = ColorValue{Text: ""}
					continue
				}
				text = anchored(sourceOf(*input.Foreground), families["neutral"], oklabToOkhslLightness(needed), saturation)
				hasText = true
			}
		}
		if hasText {
			result.Colors[token] = ColorValue{Text: hexOf(withTextContrast(text, surfaces, lighter))}
		}
	}
	return result
}

func tokenSurfaces(token string, solved map[string]RGB, background RGB) []RGB {
	out := []RGB{}
	for _, rule := range rules {
		if rule.token != token {
			continue
		}
		for _, surface := range rule.on {
			value, ok := solved[surface]
			if !ok {
				value = background
			}
			out = append(out, value)
		}
	}
	return out
}

func withTextContrast(color RGB, surfaces []RGB, lighter bool) RGB {
	if readableText(color, surfaces, lighter) {
		return color
	}
	hue, saturation, lightness, _ := ColorToOKHSL(RGBColor(color.R, color.G, color.B))
	at := func(l float64) RGB { return rgbFromOKHSL(hue, saturation, l) }
	extreme := 0.0
	if lighter {
		extreme = 1
	}
	if !readableText(at(extreme), surfaces, lighter) {
		return at(extreme)
	}
	low, high := lightness, extreme
	for i := 0; i < 20; i++ {
		middle := (low + high) / 2
		if readableText(at(middle), surfaces, lighter) {
			high = middle
		} else {
			low = middle
		}
	}
	return at(high)
}

// NewSystemTheme compiles the generated Pi system roles for the selected
// terminal color mode.
func NewSystemTheme(input SystemThemeInput, mode ColorMode) (*Theme, error) {
	generated := GenerateSystemThemeColors(input)
	return NewResolvedTheme("system", generated.Appearance, generated.Colors, mode, generated.Dim)
}

func indexedSystemColors(saturation float64, appearance string) SystemThemeColors {
	result := SystemThemeColors{Colors: make(map[string]ColorValue, len(tokenFamilies)), Appearance: appearance}
	for token, familyName := range tokenFamilies {
		if containsString(panels, token) {
			result.Colors[token] = ColorValue{Text: ""}
			continue
		}
		f := families[familyName]
		if familyName != "neutral" && saturation > 0 {
			slot, ok := tokenSlots[token]
			if !ok {
				slot = f.slot
			}
			result.Colors[token] = ColorValue{Index: slot, IsIndex: true}
		} else {
			result.Colors[token] = ColorValue{Text: ""}
		}
		if familyName == "neutral" && !containsString(foregroundTokens, token) {
			result.Dim = append(result.Dim, token)
		}
	}
	return result
}

func containsString(values []string, value string) bool {
	for _, candidate := range values {
		if candidate == value {
			return true
		}
	}
	return false
}
