package terminal

import (
	"slices"
	"strings"
	"testing"

	"github.com/hypernewbie/psi/internal/testutil"
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
		t.Fatalf("Down did not select the model completion: %d", c.completionSelected)
	}
	if c.completionItems[c.completionSelected] != "/model" {
		t.Fatalf("selected completion=%q want /model", c.completionItems[c.completionSelected])
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

func TestExactCommandCompletion(t *testing.T) {
	t.Run("backspace keeps the longer highlight", func(t *testing.T) {
		c, _ := completionConsole()
		c.Handle(Input{Key: []byte("/bu")}, false)
		c.Handle(Input{Key: []byte{0x7f}}, false) // Backspace
		if !slices.Equal(c.completionItems, []string{"/b", "/bump"}) {
			t.Fatalf("after backspace: %q", c.completionItems)
		}
		if len(c.completionRows) != 2 || !strings.HasPrefix(c.completionRows[1], "→ /bump ") {
			t.Fatalf("expected /bump highlighted below /b, rows=%q", c.completionRows)
		}
		text, submitted, _, _ := c.Handle(Input{Key: []byte{'\r'}}, false)
		if !submitted || text != "/bump" || c.Editor.Text() != "" {
			t.Fatalf("Enter: text=%q submitted=%v remaining=%q", text, submitted, c.Editor.Text())
		}
	})

	t.Run("cursor before a suffix is not a token end", func(t *testing.T) {
		c, _ := completionConsole()
		c.Editor.RestoreDraft("/loginX", len([]rune("/login")))
		c.refreshCompletion()
		if len(c.completionItems) != 0 {
			t.Fatalf("exact /login offered before suffix: %q", c.completionItems)
		}
		text, submitted, _, _ := c.Handle(Input{Key: []byte{'\r'}}, false)
		if !submitted || text != "/loginX" {
			t.Fatalf("Enter: text=%q submitted=%v", text, submitted)
		}
	})

	t.Run("exact skill keeps its argument", func(t *testing.T) {
		c, _ := completionConsole()
		c.Skills = []string{"review"}
		const skill = "/skill:review"
		// A non-breaking space ends the token for both matching and replacement.
		const draft = skill + "\u00a0next"
		c.Editor.RestoreDraft(draft, len([]rune(skill)))
		c.refreshCompletion()
		if !slices.Equal(c.completionItems, []string{skill}) {
			t.Fatalf("exact skill candidates: %q", c.completionItems)
		}
		text, submitted, _, _ := c.Handle(Input{Key: []byte{'\t'}}, false)
		if submitted || text != "" {
			t.Fatalf("Tab submitted: text=%q submitted=%v", text, submitted)
		}
		if c.Editor.Text() != draft || len(c.completionItems) != 0 || len(c.completionRows) != 0 {
			t.Fatalf("Tab draft=%q items=%q rows=%q", c.Editor.Text(), c.completionItems, c.completionRows)
		}
	})

	t.Run("exact command is visible and Escape dismisses", func(t *testing.T) {
		c, _ := completionConsole()
		c.Editor.RestoreDraft("/bump", len([]rune("/bump")))
		c.refreshCompletion()
		if len(c.completionRows) != 1 || !strings.HasPrefix(c.completionRows[0], "→ /bump ") {
			t.Fatalf("expected a single highlighted /bump row, got %q", c.completionRows)
		}
		text, submitted, _, _ := c.Handle(Input{Key: []byte{27}}, false)
		if submitted || text != "" || c.Editor.Text() != "/bump" {
			t.Fatalf("Escape: text=%q submitted=%v draft=%q", text, submitted, c.Editor.Text())
		}
		if len(c.completionItems) != 0 || len(c.completionRows) != 0 {
			t.Fatalf("Escape left completion: items=%q rows=%q", c.completionItems, c.completionRows)
		}
	})

	t.Run("out of range cursors offer nothing", func(t *testing.T) {
		const draft = "/login界"
		for _, tc := range []struct {
			name string
			pos  int
		}{
			{name: "negative", pos: -1},
			{name: "past rune length", pos: len([]rune(draft)) + 1},
		} {
			t.Run(tc.name, func(t *testing.T) {
				candidates, showList := Complete(draft, tc.pos, CommandNames(), nil)
				if len(candidates) != 0 || showList {
					t.Fatalf("candidates=%q showList=%v", candidates, showList)
				}
			})
		}
	})
}

func TestExactACCompletion(t *testing.T) {
	for _, tc := range []struct {
		name       string
		before     string // draft up to the cursor
		after      string // draft after the cursor
		candidates []string
		submission string
	}{
		{
			name:       "exact subcommand before whitespace",
			before:     "/ac end-turn",
			after:      " off",
			candidates: []string{"end-turn", "end-turn-delay"},
			submission: "/ac end-turn off",
		},
		{
			name:       "exact alias value at draft end",
			before:     "/autocompact end-turn-delay 1",
			candidates: []string{"1", "10"},
			submission: "/autocompact end-turn-delay 1",
		},
		{
			name:       "suffix blocks exact value",
			before:     "/ac threshold off",
			after:      "X",
			submission: "/ac threshold offX",
		},
		{
			name:       "suffix does not block longer prefix match",
			before:     "/ac end-turn",
			after:      "X",
			candidates: []string{"end-turn-delay"},
			submission: "/ac end-turn-delay",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, _ := completionConsole()
			c.Editor.RestoreDraft(tc.before+tc.after, len([]rune(tc.before)))
			c.refreshCompletion()
			if !slices.Equal(c.completionItems, tc.candidates) {
				t.Fatalf("candidates=%q, want %q", c.completionItems, tc.candidates)
			}
			text, submitted, _, _ := c.Handle(Input{Key: []byte{'\r'}}, false)
			if !submitted || text != tc.submission {
				t.Fatalf("Enter: text=%q submitted=%v, want submission %q", text, submitted, tc.submission)
			}
		})
	}
}
