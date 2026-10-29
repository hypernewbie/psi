package theme

import "math"

// System theme data and contrast rules ported from Pi's system-theme.ts at
// d5b3de22d0e24a3e9c38a9410d3329b099717283.
type family struct {
	hue      float64
	min, max float64
	slot     int
}

var families = map[string]family{
	"neutral":            {231.49, 0.02, 0.08, 8},
	"blue":               {231.49, 0.1, 0.68, 4},
	"green":              {158.68, 0.1, 0.76, 2},
	"red":                {20, 0.1, 0.92, 1},
	"yellow":             {82.36, 0.5, 1, 3},
	"orange":             {52, 0.12, 0.85, 3},
	"violet":             {295, 0.2, 0.6, 5},
	"calamine":           {202.43, 0.1, 0.74, 6},
	"thinkingSlate":      {231.49, 0.08, 0.2, 4},
	"thinkingBlue":       {231.49, 0.2, 0.45, 4},
	"thinkingPeriwinkle": {263.25, 0.3, 0.6, 6},
	"thinkingViolet":     {295, 0.4, 0.75, 5},
	"thinkingMagenta":    {337.5, 0.5, 0.85, 13},
	"thinkingRed":        {20, 0.95, 1, 1},
}

var tokenFamilies = map[string]string{
	"selectedBg": "blue", "searchMatchBg": "orange", "userMessageBg": "blue", "customMessageBg": "violet",
	"toolPendingBg": "neutral", "toolSuccessBg": "green", "toolErrorBg": "red",
	"text": "neutral", "userMessageText": "neutral", "customMessageText": "neutral", "toolTitle": "neutral",
	"syntaxOperator": "neutral", "syntaxPunctuation": "neutral", "muted": "neutral", "dim": "neutral",
	"thinkingText": "neutral", "toolOutput": "neutral", "mdLinkUrl": "neutral", "mdQuote": "neutral",
	"mdQuoteBorder": "neutral", "mdHr": "neutral", "mdCodeBlockBorder": "neutral", "toolDiffContext": "neutral",
	"syntaxComment": "neutral", "scrollbarTrack": "neutral", "scrollbarThumb": "neutral", "searchMatchText": "neutral",
	"borderMuted": "neutral",
	"accent":      "violet", "borderAccent": "violet", "customMessageLabel": "violet", "mdCode": "violet",
	"mdListBullet": "violet", "syntaxType": "violet", "border": "blue", "mdLink": "blue", "syntaxKeyword": "blue",
	"syntaxVariable": "calamine", "success": "green", "mdCodeBlock": "green", "toolDiffAdded": "green",
	"bashMode": "green", "syntaxNumber": "green", "error": "red", "toolDiffRemoved": "red", "warning": "yellow",
	"mdHeading": "yellow", "syntaxFunction": "yellow", "syntaxString": "orange",
	"thinkingOff": "neutral", "thinkingMinimal": "thinkingSlate", "thinkingLow": "thinkingBlue",
	"thinkingMedium": "thinkingPeriwinkle", "thinkingHigh": "thinkingViolet", "thinkingXhigh": "thinkingMagenta",
	"thinkingMax": "thinkingRed",
}

var tokenSlots = map[string]int{"syntaxString": 2, "syntaxNumber": 5, "searchMatchBg": 3}

type contrastCurve struct {
	coefficients [6]float64
	min, max     float64
}
type levelCurves [2]contrastCurve

var levels = map[string]levelCurves{
	"panel": {
		{[6]float64{0.29131, -0.39746, 2.33185, -0.85524, -1.2076, 0.86276}, 0, 0.979},
		{[6]float64{-3.74073, 27.94549, -78.44258, 112.6798, -79.60015, 22.11277}, 0.348, 1},
	},
	"track": {
		{[6]float64{0.39028, -0.23015, 0.83573, 2.43829, -4.38292, 2.01582}, 0, 0.946},
		{[6]float64{-5.24921, 38.37322, -107.28833, 152.10005, -106.17127, 29.18061}, 0.368, 1},
	},
	"thinking0": {
		{[6]float64{0.52988, -0.05809, -0.30924, 4.63567, -6.52933, 2.89108}, 0, 0.873},
		{[6]float64{-28.27749, 182.85284, -469.62416, 603.15916, -384.59976, 97.35147}, 0.51, 1},
	},
	"thinking1": {
		{[6]float64{0.55278, -0.03667, -0.45659, 4.95347, -6.90265, 3.0706}, 0, 0.858},
		{[6]float64{-37.10484, 235.86282, -596.62344, 754.3633, -474.00763, 118.3551}, 0.535, 1},
	},
	"thinking2": {
		{[6]float64{0.57486, -0.01765, -0.58987, 5.25227, -7.27175, 3.25532}, 0, 0.842},
		{[6]float64{-59.89653, 377.05024, -945.07843, 1182.03145, -734.96375, 181.68658}, 0.556, 1},
	},
	"thinking3": {
		{[6]float64{0.59621, -0.00062, -0.71148, 5.53588, -7.6392, 3.44606}, 0, 0.827},
		{[6]float64{-72.07122, 445.84082, -1099.57352, 1353.88793, -829.53392, 202.26164}, 0.58, 1},
	},
	"thinking4": {
		{[6]float64{0.61691, 0.01462, -0.82288, 5.80651, -8.00641, 3.64333}, 0, 0.811},
		{[6]float64{-110.14338, 674.21488, -1645.75941, 2004.32367, -1215.15899, 293.3183}, 0.6, 1},
	},
	"thinking5": {
		{[6]float64{0.63702, 0.02826, -0.92498, 6.06465, -8.37246, 3.84651}, 0, 0.795},
		{[6]float64{-175.47701, 1063.54495, -2570.70594, 3098.80776, -1860.15527, 444.76392}, 0.62, 1},
	},
	"thinking6": {
		{[6]float64{0.65658, 0.04044, -1.01835, 6.30989, -8.73529, 4.05439}, 0, 0.779},
		{[6]float64{-183.81712, 1094.70055, -2602.68539, 3088.71276, -1826.91131, 430.75931}, 0.643, 1},
	},
	"subtle": {
		{[6]float64{0.56762, -0.02475, -0.5383, 5.12628, -7.10931, 3.17324}, 0, 0.848},
		{[6]float64{-232.85459, 1376.54473, -3249.11801, 3827.91186, -2248.29472, 526.55751}, 0.657, 1},
	},
	"thumb": {
		{[6]float64{0.60323, 0.00278, -0.73328, 5.57157, -7.68067, 3.46933}, 0, 0.823},
		{[6]float64{-82.89897, 511.01355, -1255.98095, 1540.76821, -940.68087, 228.58523}, 0.586, 1},
	},
	"readable": {
		{[6]float64{0.66937, 0.04704, -1.06871, 6.43941, -8.9332, 4.17229}, 0, 0.77},
		{[6]float64{-1554.52576, 8733.56817, -19604.93507, 21977.72696, -12300.99599, 2749.81288}, 0.751, 1},
	},
	"emphasis": {
		{[6]float64{0.7303, 0.07695, -1.31626, 7.1681, -10.14436, 4.92846}, 0, 0.712},
		{[6]float64{-4948.31942, 26870.91986, -58334.48399, 63280.17197, -34298.01053, 7430.30146}, 0.811, 1},
	},
	"textOnPanel": {
		{[6]float64{0.86713, 0.05232, -0.89428, 4.79014, -5.5432, 1.75023}, 0, 0.542},
		{[6]float64{-8570.89457, 43954.60805, -90084.00702, 92220.6791, -47152.15802, 9632.27113}, 0.867, 1},
	},
	"text": {
		{[6]float64{0.89242, 0.02311, -0.44862, 2.34417, -0.06084, -2.63844}, 0, 0.5},
		{[6]float64{-2004.67048, 6664.47299, -6060.70202, -1792.61209, 5133.82359, -1939.85583}, 0.894, 1},
	},
}

type surfaceRule struct {
	token string
	on    []string
	level string
}

var (
	toolPanels       = []string{"toolPendingBg", "toolSuccessBg", "toolErrorBg"}
	messagePanels    = []string{"userMessageBg", "customMessageBg"}
	panels           = []string{"userMessageBg", "toolPendingBg", "toolSuccessBg", "toolErrorBg", "selectedBg", "searchMatchBg", "customMessageBg"}
	thinkingTokens   = []string{"thinkingOff", "thinkingMinimal", "thinkingLow", "thinkingMedium", "thinkingHigh", "thinkingXhigh", "thinkingMax"}
	thinkingLevels   = []string{"thinking0", "thinking1", "thinking2", "thinking3", "thinking4", "thinking5", "thinking6"}
	foregroundTokens = []string{"text", "userMessageText", "toolTitle"}
	readableFloor    = map[string]string{"dark": "readable", "light": "subtle"}
	foregroundLevel  = "emphasis"
	minimumWCAG      = 4.5
)

func each(tokens []string, on []string, level string) []surfaceRule {
	rules := make([]surfaceRule, 0, len(tokens))
	for _, token := range tokens {
		rules = append(rules, surfaceRule{token, on, level})
	}
	return rules
}

var rules = func() []surfaceRule {
	toolSurfaces := append([]string(nil), toolPanels...)
	messageSurfaces := append([]string(nil), messagePanels...)
	panelsOnBackground := []string{"background"}
	out := []surfaceRule{}
	out = append(out, each(panels, panelsOnBackground, "panel")...)
	out = append(out, surfaceRule{"text", []string{"background"}, "text"})
	out = append(out, surfaceRule{"text", []string{"selectedBg"}, "textOnPanel"})
	out = append(out, surfaceRule{"userMessageText", []string{"userMessageBg"}, "textOnPanel"})
	out = append(out, surfaceRule{"toolTitle", toolSurfaces, "textOnPanel"})
	out = append(out, each([]string{"accent", "success", "error", "warning"}, append([]string{"background", "selectedBg"}, toolSurfaces...), "readable")...)
	out = append(out, surfaceRule{"muted", append([]string{"background", "selectedBg", "customMessageBg"}, toolSurfaces...), "readable"})
	out = append(out, surfaceRule{"dim", append([]string{"background", "selectedBg", "customMessageBg"}, toolSurfaces...), "subtle"})
	out = append(out, surfaceRule{"thinkingText", []string{"background"}, "readable"})
	out = append(out, surfaceRule{"customMessageText", append([]string{"customMessageBg"}, toolSurfaces...), "readable"})
	out = append(out, surfaceRule{"customMessageLabel", append([]string{"background", "customMessageBg", "selectedBg"}, toolSurfaces...), "readable"})
	out = append(out, surfaceRule{"toolOutput", append([]string{"background"}, toolSurfaces...), "readable"})
	out = append(out, each([]string{"mdHeading", "mdLink", "mdLinkUrl", "mdCode", "mdQuote", "mdCodeBlockBorder", "mdListBullet"}, append([]string{"background"}, messageSurfaces...), "readable")...)
	out = append(out, surfaceRule{"mdCodeBlock", append(append([]string{"background"}, messageSurfaces...), toolSurfaces...), "readable"})
	out = append(out, each([]string{"toolDiffAdded", "toolDiffRemoved", "toolDiffContext"}, append([]string{"background"}, toolSurfaces...), "readable")...)
	syntaxTokens := []string{"syntaxComment", "syntaxKeyword", "syntaxFunction", "syntaxVariable", "syntaxString", "syntaxNumber", "syntaxType", "syntaxOperator", "syntaxPunctuation"}
	out = append(out, each(syntaxTokens, append(append([]string{"background"}, messageSurfaces...), toolSurfaces...), "readable")...)
	out = append(out, surfaceRule{"searchMatchText", []string{"searchMatchBg"}, "readable"})
	out = append(out, each([]string{"bashMode", "border", "borderAccent"}, []string{"background"}, "readable")...)
	out = append(out, surfaceRule{"borderMuted", []string{"background"}, "subtle"})
	out = append(out, each([]string{"mdQuoteBorder", "mdHr"}, append(append([]string{"background"}, messageSurfaces...), toolSurfaces...), "readable")...)
	out = append(out, surfaceRule{"scrollbarTrack", []string{"background"}, "track"})
	out = append(out, surfaceRule{"scrollbarThumb", []string{"scrollbarTrack"}, "thumb"})
	for i, token := range thinkingTokens {
		out = append(out, surfaceRule{token, []string{"background"}, thinkingLevels[i]})
	}
	return out
}()

var solveOrder = func() []string {
	order := []string{}
	seen := map[string]bool{}
	var visit func(string)
	visit = func(token string) {
		if seen[token] {
			return
		}
		for _, rule := range rules {
			if rule.token != token {
				continue
			}
			for _, surface := range rule.on {
				if surface != "background" {
					visit(surface)
				}
			}
		}
		seen[token] = true
		order = append(order, token)
	}
	for _, rule := range rules {
		visit(rule.token)
	}
	return order
}()

func levelTarget(level, appearance string, surfaceLightness float64) (float64, bool) {
	curvePair, ok := levels[level]
	if !ok {
		return 0, false
	}
	curve := curvePair[0]
	if appearance == "light" {
		curve = curvePair[1]
	}
	if surfaceLightness < curve.min || surfaceLightness > curve.max {
		return 0, false
	}
	target := 0.0
	for power, coefficient := range curve.coefficients {
		target += coefficient * math.Pow(surfaceLightness, float64(power))
	}
	return target, true
}
