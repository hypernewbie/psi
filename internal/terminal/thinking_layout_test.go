package terminal

import (
	"fmt"
	"strings"
	"testing"
)

func TestThinkingCommitsRowsClippedByLayout(t *testing.T) {
	for _, tc := range []struct {
		name   string
		height int
		batch  bool
		setup  func(*Console)
		after  func(*Console)
	}{
		{name: "short screen", height: 6},
		{name: "batched short screen", height: 6, batch: true},
		{name: "tiny screen", height: 2},
		{name: "status takes remaining row", height: 6, setup: func(c *Console) { c.SetThinking(true) }},
		{name: "picker owns screen", height: 12, setup: func(c *Console) {
			c.SetPickerChoiceRows([]string{"choice 1", "choice 2", "choice 3", "choice 4", "choice 5", "choice 6", "choice 7"})
		}},
		{name: "picker opens and reserves spacer", height: 8, after: func(c *Console) {
			c.SetPickerRows([]string{"choice 1"})
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, vt, _ := newTestConsole(40, tc.height)
			c.SetPresentation(PresentationSnapshot{NoColor: true})
			if tc.setup != nil {
				tc.setup(c)
			}
			if tc.batch {
				c.BeginFrame()
			}
			for i := 1; i <= 20; i++ {
				if _, err := c.ThinkingWrite([]byte(fmt.Sprintf("reason %02d\n", i))); err != nil {
					t.Fatal(err)
				}
			}
			if tc.batch {
				if err := c.EndFrame(); err != nil {
					t.Fatal(err)
				}
			}
			if tc.after != nil {
				tc.after(c)
				c.Draw()
			}
			for _, phase := range []string{"live", "closed"} {
				if phase == "closed" {
					if err := c.CloseThinking(); err != nil {
						t.Fatal(err)
					}
				}
				visible := thinkingVisible(vt)
				for i := 1; i <= 20; i++ {
					needle := fmt.Sprintf("reason %02d", i)
					if got := strings.Count(visible, needle); got != 1 {
						t.Fatalf("%s %q count = %d, want 1:\n%s", phase, needle, got, visible)
					}
				}
			}
		})
	}
}
