package theme

import (
	"fmt"
	"math"
)

// Theme is a resolved Pi theme. It is immutable after construction, so callers
// can safely retain it across render cycles.
type Theme struct {
	Name       string
	SourcePath string
	Appearance string
	Mode       ColorMode
	colors     map[string]ResolvedColor
	dimTokens  map[string]bool
}

// Compile resolves token fallbacks and variable chains, then validates and
// converts every color value using Pi's color parser.
func (d *Document) Compile(mode ColorMode) (*Theme, error) {
	resolved, err := d.ColorsWithFallbacks()
	if err != nil {
		return nil, err
	}
	values := make(map[string]ColorValue, len(resolved))
	for name, color := range resolved {
		values[name] = color.Value
	}
	theme, err := NewResolvedTheme(d.Name, d.Appearance, values, mode, nil)
	if err != nil {
		return nil, err
	}
	theme.Appearance = d.Appearance
	if theme.Appearance == "" {
		theme.Appearance = detectAppearance(theme.colors)
	}
	return theme, nil
}

// ThemeColor pairs the exact token descriptor with its parsed color.
type ThemeColor struct {
	Token Token
	Value ColorValue
	Color Color
}

// ColorFor returns a token only in the slot declared by Pi's registry.
func (t *Theme) ColorFor(name string, slot Slot) (Color, error) {
	if t == nil {
		return Color{}, fmt.Errorf("theme is nil")
	}
	value, ok := t.colors[name]
	if !ok {
		return Color{}, fmt.Errorf("unknown theme color: %s", name)
	}
	if value.Token.Slot != slot {
		return Color{}, fmt.Errorf("theme color %s belongs to %s slot, not %s", name, value.Token.Slot, slot)
	}
	return value.Color, nil
}

// Colors returns a copy of all resolved color values, with their Pi slots.
func (t *Theme) Colors() map[string]ThemeColor {
	out := make(map[string]ThemeColor, len(t.colors))
	for name, color := range t.colors {
		out[name] = ThemeColor{Token: color.Token, Value: color.Value, Color: color.Color}
	}
	return out
}

// NewResolvedTheme compiles a complete token map such as Pi's generated system theme.
func NewResolvedTheme(name, appearance string, values map[string]ColorValue, mode ColorMode, dimTokens []string) (*Theme, error) {
	colors := make(map[string]ResolvedColor, len(tokenRegistry))
	for _, token := range tokenRegistry {
		value, ok := values[token.Name]
		if !ok {
			return nil, fmt.Errorf("missing required color token %q", token.Name)
		}
		color, err := ParseValue(value)
		if err != nil {
			return nil, fmt.Errorf("colors.%s: %w", token.Name, err)
		}
		colors[token.Name] = ResolvedColor{Token: token, Value: value, Color: color}
	}
	if mode == "" {
		mode = ColorMode256
	}
	dim := make(map[string]bool, len(dimTokens))
	for _, token := range dimTokens {
		if descriptor, ok := tokenByName(token); ok && descriptor.Slot == Foreground {
			dim[token] = true
		}
	}
	return &Theme{Name: name, Appearance: appearance, Mode: mode, colors: colors, dimTokens: dim}, nil
}

func (t *Theme) IsDimForeground(name string) bool { return t != nil && t.dimTokens[name] }

func (t *Theme) ForegroundANSI(name string) (string, error) {
	color, err := t.ColorFor(name, Foreground)
	if err != nil {
		return "", err
	}
	return ForegroundANSI(color, t.Mode)
}
func (t *Theme) BackgroundANSI(name string) (string, error) {
	color, err := t.ColorFor(name, Background)
	if err != nil {
		return "", err
	}
	return BackgroundANSI(color, t.Mode)
}

// TextStyle contains the styling operations supported by Pi's TUI.
type TextStyle struct {
	Bold, Dim, Italic, Underline, Inverse, Strikethrough bool
}

// Style emits one styled segment with reset codes in reverse opening order,
// matching Pi's styleTextWithAnsi behavior.
func (t *Theme) Style(text, fgToken, bgToken string, style TextStyle) (string, error) {
	if t.IsDimForeground(fgToken) {
		style.Dim = true
	}
	var fgANSI, bgANSI string
	var err error
	if fgToken != "" {
		fgANSI, err = t.ForegroundANSI(fgToken)
		if err != nil {
			return "", err
		}
	}
	if bgToken != "" {
		bgANSI, err = t.BackgroundANSI(bgToken)
		if err != nil {
			return "", err
		}
	}
	prefix, suffix := "", ""
	if fgANSI != "" {
		prefix += fgANSI
		suffix = "\x1b[39m"
	}
	if bgANSI != "" {
		prefix += bgANSI
		suffix = "\x1b[49m" + suffix
	}
	if style.Bold {
		prefix += "\x1b[1m"
	}
	if style.Dim {
		prefix += "\x1b[2m"
	}
	if style.Bold || style.Dim {
		suffix = "\x1b[22m" + suffix
	}
	if style.Italic {
		prefix += "\x1b[3m"
		suffix = "\x1b[23m" + suffix
	}
	if style.Underline {
		prefix += "\x1b[4m"
		suffix = "\x1b[24m" + suffix
	}
	if style.Inverse {
		prefix += "\x1b[7m"
		suffix = "\x1b[27m" + suffix
	}
	if style.Strikethrough {
		prefix += "\x1b[9m"
		suffix = "\x1b[29m" + suffix
	}
	return prefix + text + suffix, nil
}

func detectAppearance(colors map[string]ResolvedColor) string {
	var foregrounds, backgrounds []float64
	for _, token := range tokenRegistry {
		tokenColor := colors[token.Name]
		color := tokenColor.Color
		if color.Kind == ColorDefault || color.Kind == ColorIndexed && color.Index < 16 {
			continue
		}
		l, _, _, err := ColorToOKLCH(color)
		if err != nil {
			continue
		}
		if tokenColor.Token.Slot == Background {
			backgrounds = append(backgrounds, l)
		} else {
			foregrounds = append(foregrounds, l)
		}
	}
	average := func(values []float64) (float64, bool) {
		if len(values) == 0 {
			return 0, false
		}
		sum := 0.0
		for _, value := range values {
			sum += value
		}
		return sum / float64(len(values)), true
	}
	fg, hasFG := average(foregrounds)
	bg, hasBG := average(backgrounds)
	if hasFG && hasBG {
		if bg < fg {
			return "dark"
		}
		return "light"
	}
	if hasBG {
		if bg < 0.5 {
			return "dark"
		}
		return "light"
	}
	if hasFG {
		if fg > 0.5 {
			return "dark"
		}
		return "light"
	}
	return ""
}

// AppearanceContrast is the relative contrast between two sRGB colors.
func AppearanceContrast(first, second RGB) float64 {
	luminance := func(rgb RGB) float64 {
		linear := func(channel float64) float64 {
			value := channel / 255
			if value <= 0.04045 {
				return value / 12.92
			}
			return math.Pow((value+0.055)/1.055, 2.4)
		}
		return 0.2126*linear(rgb.R) + 0.7152*linear(rgb.G) + 0.0722*linear(rgb.B)
	}
	a, b := luminance(first), luminance(second)
	return (math.Max(a, b) + 0.05) / (math.Min(a, b) + 0.05)
}
