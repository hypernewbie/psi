package terminal

import (
	"strings"
	"testing"

	pitheme "psi/internal/theme"
)

func TestUserMessageSurfaceAndMonochromeMarker(t *testing.T) {
	doc, err := pitheme.BuiltinDocument("dark")
	if err != nil {
		t.Fatal(err)
	}
	palette, err := doc.Compile(pitheme.ColorModeTruecolor)
	if err != nil {
		t.Fatal(err)
	}
	rows := FormatUserMessage("requested change", 40, false, palette)
	if len(rows) < 2 || !strings.Contains(rows[0], "› requested change") {
		t.Fatalf("user marker missing: %q", rows)
	}
	background, err := palette.BackgroundANSI("userMessageBg")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(rows[0], background) {
		t.Fatalf("user background missing: %q", rows[0])
	}
	plain := strings.Join(FormatUserMessage("requested change", 40, true, palette), "\n")
	if strings.Contains(plain, "\x1b") || !strings.Contains(plain, "› requested change") {
		t.Fatalf("monochrome hierarchy=%q", plain)
	}
}
