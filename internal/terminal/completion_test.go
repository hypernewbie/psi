package terminal

import (
	"strings"
	"testing"

	"psi/internal/testutil"
)

func completionConsole() (*Console, *testutil.VTModel) {
	vt := testutil.NewVTModel(80, 24)
	c := &Console{Editor: NewEditor(), Out: vtFeed{vt}, writer: NewWriter(vtFeed{vt}), SizeFunc: func() (int, int) { return 80, 24 }}
	c.Commands = CommandNames()
	c.CompletionDescriptions = CommandDescriptions()
	c.SetPresentation(PresentationSnapshot{WorkingDirectory: "/work", Model: "model", NoColor: true})
	return c, vt
}

func TestSlashCompletionLivesInFiveRowEditorTail(t *testing.T) {
	c, vt := completionConsole()
	if _, _, _, _ = c.Handle(Input{Key: []byte("/")}, false); c.Editor.Text() != "/" {
		t.Fatalf("draft=%q", c.Editor.Text())
	}
	if len(c.completionRows) == 0 || len(c.completionRows) > 5 {
		t.Fatalf("completion rows=%d: %q", len(c.completionRows), c.completionRows)
	}
	if len(vt.Scrollback) != 0 || strings.Contains(vt.Text(), "Candidates:") {
		t.Fatalf("completion polluted native scrollback: scroll=%q screen=%q", vt.Scrollback, vt.Text())
	}
	if !strings.Contains(vt.Text(), "/model") || !strings.Contains(vt.Text(), "model") {
		t.Fatalf("completion or footer absent from tail: %q", vt.Text())
	}
	if _, _, _, _ = c.Handle(Input{Key: []byte("\x1b[B")}, false); c.completionSelected != 1 {
		t.Fatalf("Down did not select second completion: %d", c.completionSelected)
	}
	c.Handle(Input{Key: []byte{9}}, false)
	if c.Editor.Text() != "/model" || len(c.completionRows) != 0 {
		t.Fatalf("Tab completion text=%q rows=%q", c.Editor.Text(), c.completionRows)
	}
}

func TestACSubcommandCompletion(t *testing.T) {
	c, _ := completionConsole()
	c.Editor.RestoreDraft("/ac th", len([]rune("/ac th")))
	c.refreshCompletion()
	if !strings.EqualFold(strings.Join(c.completionItems, ","), "threshold") {
		t.Fatalf("/ac subcommands = %q", c.completionItems)
	}
	if !c.applyCompletion() || c.Editor.Text() != "/ac threshold" {
		t.Fatalf("subcommand completion draft = %q", c.Editor.Text())
	}
	c, _ = completionConsole()
	c.Editor.RestoreDraft("/autocompact compactor c", len([]rune("/autocompact compactor c")))
	c.refreshCompletion()
	if got := strings.Join(c.completionItems, ","); got != "current,clear" {
		t.Fatalf("compactor values = %q", got)
	}
	c, _ = completionConsole()
	c.Editor.RestoreDraft("/ac end-turn-delay ", len([]rune("/ac end-turn-delay ")))
	c.refreshCompletion()
	if !strings.Contains(strings.Join(c.completionItems, ","), "0") {
		t.Fatalf("delay values = %q", c.completionItems)
	}
}

func TestSlashCompletionEscapeAndEnterPrecedence(t *testing.T) {
	c, _ := completionConsole()
	c.Handle(Input{Key: []byte("/th")}, false)
	before := c.Editor.Text()
	c.Handle(Input{Key: []byte{27}}, false)
	if c.Editor.Text() != before || len(c.completionItems) != 0 {
		t.Fatalf("Escape changed draft or left completion active: %q %q", c.Editor.Text(), c.completionItems)
	}
	c.Handle(Input{Key: []byte("i")}, false)
	if len(c.completionItems) != 1 {
		t.Fatalf("editing after dismiss did not refresh completion: %q", c.completionItems)
	}
	text, submitted, _, _ := c.Handle(Input{Key: []byte{'\r'}}, false)
	if !submitted || text != "/thinking" || c.Editor.Text() != "" {
		t.Fatalf("Enter precedence: text=%q submitted=%v remaining=%q", text, submitted, c.Editor.Text())
	}
}
