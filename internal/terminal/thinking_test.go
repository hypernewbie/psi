package terminal

import (
	"strings"
	"testing"

	"psi/internal/testutil"
	pitheme "psi/internal/theme"
)

func TestThinkingRowIsFaintAndTransient(t *testing.T) {
	doc, err := pitheme.BuiltinDocument("dark")
	if err != nil {
		t.Fatal(err)
	}
	palette, err := doc.Compile(pitheme.ColorModeTruecolor)
	if err != nil {
		t.Fatal(err)
	}
	row := FormatThinkingRow(80, false, DefaultDisplaySettings(), palette)
	if !strings.Contains(row, "Thinking…") || !strings.Contains(row, "\x1b[2m") {
		t.Fatalf("thinking row is not faint: %q", row)
	}
	plain := FormatThinkingRow(80, true, DefaultDisplaySettings(), palette)
	if strings.Contains(plain, "\x1b") || !strings.Contains(plain, "Thinking…") {
		t.Fatalf("no-color row: %q", plain)
	}
	vt := testutil.NewVTModel(80, 24)
	c := &Console{Editor: NewEditor(), Out: vtFeed{vt}, writer: NewWriter(vtFeed{vt}), SizeFunc: func() (int, int) { return 80, 24 }}
	c.SetPresentation(PresentationSnapshot{NoColor: true})
	c.SetThinking(true)
	c.Draw()
	if !strings.Contains(vt.Text(), "Thinking…") || len(vt.Scrollback) != 0 {
		t.Fatalf("thinking is not in the transient tail: %q", vt.Text())
	}
	c.SetThinking(false)
	c.Draw()
	if strings.Contains(vt.Text(), "Thinking…") || strings.Contains(strings.Join(vt.Scrollback, "\n"), "Thinking…") {
		t.Fatal("thinking row survived settlement")
	}
}
