// Package theme parses Pi-compatible theme data. Theme files are data only;
// this package never executes code or accesses the terminal.
package theme

// Slot identifies the only role a token can fill in a style.
type Slot string

const (
	Foreground Slot = "foreground"
	Background Slot = "background"
)

// Token describes one token in Pi's pinned theme registry. Optional tokens
// inherit the named token only when absent; an explicit empty string is kept.
type Token struct {
	Name     string
	Slot     Slot
	Fallback string
}

// tokenRegistry follows theme-tokens.ts at Pi d5b3de22d0e24a3e9c38a9410d3329b099717283.
var tokenRegistry = []Token{
	{Name: "accent", Slot: Foreground},
	{Name: "border", Slot: Foreground},
	{Name: "borderAccent", Slot: Foreground},
	{Name: "borderMuted", Slot: Foreground},
	{Name: "success", Slot: Foreground},
	{Name: "error", Slot: Foreground},
	{Name: "warning", Slot: Foreground},
	{Name: "muted", Slot: Foreground},
	{Name: "dim", Slot: Foreground},
	{Name: "text", Slot: Foreground},
	{Name: "thinkingText", Slot: Foreground},
	{Name: "scrollbarTrack", Slot: Foreground, Fallback: "muted"},
	{Name: "scrollbarThumb", Slot: Foreground, Fallback: "text"},
	{Name: "selectedBg", Slot: Background},
	{Name: "searchMatchBg", Slot: Background, Fallback: "selectedBg"},
	{Name: "searchMatchText", Slot: Foreground, Fallback: "text"},
	{Name: "userMessageBg", Slot: Background},
	{Name: "userMessageText", Slot: Foreground},
	{Name: "customMessageBg", Slot: Background},
	{Name: "customMessageText", Slot: Foreground},
	{Name: "customMessageLabel", Slot: Foreground},
	{Name: "toolPendingBg", Slot: Background},
	{Name: "toolSuccessBg", Slot: Background},
	{Name: "toolErrorBg", Slot: Background},
	{Name: "toolTitle", Slot: Foreground},
	{Name: "toolOutput", Slot: Foreground},
	{Name: "mdHeading", Slot: Foreground},
	{Name: "mdLink", Slot: Foreground},
	{Name: "mdLinkUrl", Slot: Foreground},
	{Name: "mdCode", Slot: Foreground},
	{Name: "mdCodeBlock", Slot: Foreground},
	{Name: "mdCodeBlockBorder", Slot: Foreground},
	{Name: "mdQuote", Slot: Foreground},
	{Name: "mdQuoteBorder", Slot: Foreground},
	{Name: "mdHr", Slot: Foreground},
	{Name: "mdListBullet", Slot: Foreground},
	{Name: "toolDiffAdded", Slot: Foreground},
	{Name: "toolDiffRemoved", Slot: Foreground},
	{Name: "toolDiffContext", Slot: Foreground},
	{Name: "syntaxComment", Slot: Foreground},
	{Name: "syntaxKeyword", Slot: Foreground},
	{Name: "syntaxFunction", Slot: Foreground},
	{Name: "syntaxVariable", Slot: Foreground},
	{Name: "syntaxString", Slot: Foreground},
	{Name: "syntaxNumber", Slot: Foreground},
	{Name: "syntaxType", Slot: Foreground},
	{Name: "syntaxOperator", Slot: Foreground},
	{Name: "syntaxPunctuation", Slot: Foreground},
	{Name: "thinkingOff", Slot: Foreground},
	{Name: "thinkingMinimal", Slot: Foreground},
	{Name: "thinkingLow", Slot: Foreground},
	{Name: "thinkingMedium", Slot: Foreground},
	{Name: "thinkingHigh", Slot: Foreground},
	{Name: "thinkingXhigh", Slot: Foreground},
	{Name: "thinkingMax", Slot: Foreground, Fallback: "thinkingXhigh"},
	{Name: "bashMode", Slot: Foreground},
}

// Tokens returns the pinned registry in Pi theme-file order.
func Tokens() []Token { return append([]Token(nil), tokenRegistry...) }

func tokenByName(name string) (Token, bool) {
	for _, token := range tokenRegistry {
		if token.Name == name {
			return token, true
		}
	}
	return Token{}, false
}
