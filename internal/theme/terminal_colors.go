package theme

import (
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
)

// TerminalColors contains only reported values. Palette is non-nil only after
// the complete ANSI palette has arrived.
type TerminalColors struct {
	Foreground *RGB
	Background *RGB
	Palette    []RGB
}

// ColorReply identifies one supported OSC color response.
type ColorReply struct {
	Target string // "foreground", "background", or "palette"
	Index  int    // used only for palette targets
	Color  *RGB   // nil means the response was syntactically valid but unusable
}

var oscColorResponse = regexp.MustCompile(`(?i)^\x1b\](?:(1[01])|4;(\d{1,3}));([^\x07\x1b]*)(?:\x07|\x1b\\)$`)
var terminalColorSchemeReport = regexp.MustCompile(`^\x1b\[\?997;(1|2)n$`)
var deviceAttributesReply = regexp.MustCompile(`^\x1b\[\?[\d;]*c$`)

// ParseOSCColorResponse parses a bounded OSC 10/11/4 response terminated by
// BEL or ST. It does not accept arbitrary OSC payloads as color data.
func ParseOSCColorResponse(data string) (ColorReply, bool) {
	if len(data) > 256 {
		return ColorReply{}, false
	}
	match := oscColorResponse.FindStringSubmatch(data)
	if match == nil {
		return ColorReply{}, false
	}
	var reply ColorReply
	switch {
	case match[1] == "10":
		reply.Target = "foreground"
	case match[1] == "11":
		reply.Target = "background"
	default:
		index, err := strconv.Atoi(match[2])
		if err != nil || index < 0 || index >= 16 {
			return ColorReply{}, false
		}
		reply.Target, reply.Index = "palette", index
	}
	if color, ok := parseOSCColor(match[3]); ok {
		reply.Color = &color
	}
	return reply, true
}

func parseOSCColor(value string) (RGB, bool) {
	value = strings.TrimSpace(value)
	if strings.HasPrefix(value, "#") {
		hex := value[1:]
		switch len(hex) {
		case 6:
			if !isHex(hex) {
				return RGB{}, false
			}
			r, _ := strconv.ParseUint(hex[0:2], 16, 8)
			g, _ := strconv.ParseUint(hex[2:4], 16, 8)
			b, _ := strconv.ParseUint(hex[4:6], 16, 8)
			return RGB{float64(r), float64(g), float64(b)}, true
		case 12:
			r, rok := parseOSCChannel(hex[0:4])
			g, gok := parseOSCChannel(hex[4:8])
			b, bok := parseOSCChannel(hex[8:12])
			return RGB{r, g, b}, rok && gok && bok
		default:
			return RGB{}, false
		}
	}
	value = strings.TrimPrefix(strings.ToLower(value), "rgb:")
	value = strings.TrimPrefix(value, "rgba:")
	channels := strings.Split(value, "/")
	if len(channels) != 3 {
		return RGB{}, false
	}
	r, rok := parseOSCChannel(channels[0])
	g, gok := parseOSCChannel(channels[1])
	b, bok := parseOSCChannel(channels[2])
	return RGB{r, g, b}, rok && gok && bok
}

func isHex(value string) bool {
	for _, r := range value {
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f' || r >= 'A' && r <= 'F') {
			return false
		}
	}
	return true
}

func parseOSCChannel(value string) (float64, bool) {
	if len(value) == 0 || len(value) > 16 || !isHex(value) {
		return 0, false
	}
	integer, err := strconv.ParseUint(value, 16, 64)
	if err != nil {
		return 0, false
	}
	max := math.Pow(16, float64(len(value))) - 1
	if !finite(max) || max <= 0 {
		return 0, false
	}
	return math.Round(float64(integer) / max * 255), true
}

// IsDeviceAttributesReply identifies the bounded DA1 sentinel used to finish a color query.
func IsDeviceAttributesReply(data string) bool {
	return len(data) <= 64 && deviceAttributesReply.MatchString(data)
}

// ParseTerminalColorSchemeReport parses the retained OSC 2031 appearance reply.
func ParseTerminalColorSchemeReport(data string) (string, bool) {
	if len(data) > 32 {
		return "", false
	}
	match := terminalColorSchemeReport.FindStringSubmatch(data)
	if match == nil {
		return "", false
	}
	if match[1] == "2" {
		return "light", true
	}
	return "dark", true
}

func (colors TerminalColors) SystemInput(appearanceHint string, saturation float64) SystemThemeInput {
	value := saturation
	return SystemThemeInput{Foreground: colors.Foreground, Background: colors.Background, Palette: append([]RGB(nil), colors.Palette...), Saturation: &value, AppearanceHint: appearanceHint}
}

// ColorQuery is Pi's one bounded OSC 10/11/4 query followed by a DA1 sentinel.
func ColorQuery() []byte {
	var builder strings.Builder
	builder.WriteString("\x1b]10;?\x07\x1b]11;?\x07")
	for i := 0; i < 16; i++ {
		builder.WriteString(fmt.Sprintf("\x1b]4;%d;?\x07", i))
	}
	builder.WriteString("\x1b[c")
	return []byte(builder.String())
}

// DetectColorFGBG follows Pi's terminal palette-index heuristic.
func DetectColorFGBG(value string) string {
	fields := strings.Split(value, ";")
	if len(fields) == 0 {
		return ""
	}
	background := strings.TrimSpace(fields[len(fields)-1])
	if len(background) == 0 || len(background) > 2 {
		return ""
	}
	for _, r := range background {
		if r < '0' || r > '9' {
			return ""
		}
	}
	index, err := strconv.Atoi(background)
	if err != nil || index > 15 {
		return ""
	}
	if index <= 6 || index == 8 {
		return "dark"
	}
	return "light"
}

// DetectTerminalAppearance uses a reported background first, then the terminal
// scheme report, COLORFGBG, and Pi's dark fallback.
func DetectTerminalAppearance(colors TerminalColors, reportedScheme, colorFGBG string) string {
	if colors.Background != nil {
		return TerminalAppearance(*colors.Background, colors.Foreground)
	}
	if reportedScheme == "light" || reportedScheme == "dark" {
		return reportedScheme
	}
	if detected := DetectColorFGBG(colorFGBG); detected != "" {
		return detected
	}
	return "dark"
}

// DetectColorMode honors terminal.trueColor before PI_TRUE_COLOR and then the
// retained Pi terminal-family hints. It does not run terminal probes.
func DetectColorMode(setting string, env map[string]string, goos string) ColorMode {
	if setting == "true" {
		return ColorModeTruecolor
	}
	if setting == "false" {
		return ColorMode256
	}
	if value, ok := env["PI_TRUE_COLOR"]; ok {
		if value == "1" {
			return ColorModeTruecolor
		}
		if value == "0" {
			return ColorMode256
		}
	}
	term := strings.ToLower(env["TERM"])
	program := strings.ToLower(env["TERM_PROGRAM"])
	colorTerm := strings.ToLower(env["COLORTERM"])
	if colorTerm == "truecolor" || colorTerm == "24bit" || strings.HasSuffix(term, "-direct") || goos == "windows" {
		return ColorModeTruecolor
	}
	if env["KITTY_WINDOW_ID"] != "" || program == "kitty" || program == "rex" || program == "ghostty" || env["GHOSTTY_RESOURCES_DIR"] != "" || env["WEZTERM_PANE"] != "" || program == "wezterm" || program == "warpterminal" || env["WARP_SESSION_ID"] != "" || env["WARP_TERMINAL_SESSION_UUID"] != "" || env["ITERM_SESSION_ID"] != "" || program == "iterm.app" || env["WT_SESSION"] != "" || program == "alacritty" || program == "vscode" || program == "zed" {
		return ColorModeTruecolor
	}
	return ColorMode256
}
