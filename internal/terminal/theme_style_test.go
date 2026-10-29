package terminal

import (
	"strings"
	"testing"

	pitheme "psi/internal/theme"
)

func TestResolvedThemeStylesBackgroundAndDefaultResetWithoutChangingWidth(t *testing.T) {
	doc, err := pitheme.BuiltinDocument("dark")
	if err != nil {
		t.Fatal(err)
	}
	palette, err := doc.Compile(pitheme.ColorModeTruecolor)
	if err != nil {
		t.Fatal(err)
	}
	row := StyledRow{Spans: []StyledSpan{
		{Text: "› request", Token: ThemeToken("userMessageText"), Background: ThemeToken("userMessageBg")},
		{Text: " answer", Token: TokenText},
	}}
	styled := row.SerializeWithTheme(palette, false)
	background, err := palette.BackgroundANSI("userMessageBg")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(styled, background) || !strings.Contains(styled, "\x1b[49m") || !strings.Contains(styled, "\x1b[39m") {
		t.Fatalf("theme row lacks trusted surface/default resets: %q", styled)
	}
	if got := CellWidth(row.Plain()); got != row.Width() || got != CellWidth("› request answer") {
		t.Fatalf("ANSI changed row width: %d", got)
	}
	if got := row.SerializeWithTheme(palette, true); got != row.Plain() {
		t.Fatalf("no-color serialization=%q, plain=%q", got, row.Plain())
	}
}

func TestMarkdownUsesSpecificPiThemeRoles(t *testing.T) {
	doc, err := pitheme.BuiltinDocument("dark")
	if err != nil {
		t.Fatal(err)
	}
	palette, err := doc.Compile(pitheme.ColorModeTruecolor)
	if err != nil {
		t.Fatal(err)
	}
	output := strings.Join(RenderMarkdown("# Head\n\n[link](https://example.test)", 80, false, palette), "\n")
	heading, _ := palette.ForegroundANSI("mdHeading")
	link, _ := palette.ForegroundANSI("mdLink")
	url, _ := palette.ForegroundANSI("mdLinkUrl")
	if !strings.Contains(output, heading) || !strings.Contains(output, link) || !strings.Contains(output, url) {
		t.Fatalf("Markdown roles not resolved independently: %q", output)
	}
}
