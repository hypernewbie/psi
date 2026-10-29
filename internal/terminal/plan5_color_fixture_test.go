package terminal

import (
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"

	"psi/internal/event"
	pitheme "psi/internal/theme"
)

type plan5ColorFixture struct {
	Reference string                  `json:"reference"`
	Sizes     []plan5ColorSizeFixture `json:"sizes"`
}

type plan5ColorSizeFixture struct {
	Size   string   `json:"size"`
	Before []string `json:"beforeTheme"`
	After  []string `json:"selectedDarkTheme"`
}

func TestPlan5ColoredHierarchyFixtures(t *testing.T) {
	got := buildPlan5ColorFixture(t)
	encoded, err := json.MarshalIndent(got, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	encoded = append(encoded, '\n')
	if os.Getenv("UPDATE_PLAN5_COLOR_FIXTURE") == "1" {
		if err := os.WriteFile("testdata/plan5-color-matrix.json", encoded, 0600); err != nil {
			t.Fatal(err)
		}
		return
	}
	data, err := os.ReadFile("testdata/plan5-color-matrix.json")
	if err != nil {
		t.Fatal(err)
	}
	var want plan5ColorFixture
	if err := json.Unmarshal(data, &want); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("PLAN5 colored component fixture changed; run UPDATE_PLAN5_COLOR_FIXTURE=1 go test ./internal/terminal -run '^TestPlan5ColoredHierarchyFixtures$' to inspect the update\ngot: %s\nwant: %s", encoded, data)
	}
	for _, size := range got.Sizes {
		if len(size.Before) != len(size.After) || len(size.After) < 10 {
			t.Fatalf("incomplete hierarchy fixture at %s: before=%d after=%d", size.Size, len(size.Before), len(size.After))
		}
		selected := strings.Join(strings.Fields(Sanitize(strings.Join(size.After, "\n"))), " ")
		for _, role := range []string{"Create", "report.", "Progress", "compacted"} {
			if !strings.Contains(selected, role) {
				t.Errorf("%s selected-theme fixture is missing %q: %q", size.Size, role, selected)
			}
		}
		if !strings.Contains(selected, "[compaction]") {
			t.Errorf("%s selected-theme fixture is missing the metadata label: %q", size.Size, selected)
		}
		var tool string
		for i, row := range size.After {
			if row == "-- tool --" && i+1 < len(size.After) {
				tool = Sanitize(size.After[i+1])
				break
			}
		}
		if strings.HasPrefix(size.Size, "120x") || strings.HasPrefix(size.Size, "80x") || strings.HasPrefix(size.Size, "60x") {
			if !strings.Contains(tool, "src/main.go") {
				t.Errorf("%s tool preview lost its path: %q", size.Size, tool)
			}
		} else if strings.HasPrefix(size.Size, "40x") {
			if !strings.Contains(tool, "n.go") || !strings.Contains(tool, "…") {
				t.Errorf("%s tool preview did not show bounded path truncation: %q", size.Size, tool)
			}
		} else if strings.Contains(tool, "src/main.go") || !strings.Contains(tool, "• read") || !strings.Contains(tool, "…") {
			t.Errorf("%s must retain a completed tool marker and clip its path: %q", size.Size, tool)
		}
		if !strings.Contains(strings.Join(size.After, "\n"), "\x1b[38;5;") || strings.Contains(strings.Join(size.Before, "\n"), "\x1b[38;2;") {
			t.Errorf("%s must pair indexed fallback rows with selected-theme color rows", size.Size)
		}
	}
}

func buildPlan5ColorFixture(t *testing.T) plan5ColorFixture {
	t.Helper()
	data, err := pitheme.BuiltinJSON("dark")
	if err != nil {
		t.Fatal(err)
	}
	doc, err := pitheme.Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	theme, err := doc.Compile(pitheme.ColorMode256)
	if err != nil {
		t.Fatal(err)
	}
	fixture := plan5ColorFixture{Reference: "Pinned Pi dark theme; beforeTheme is the indexed fallback, selectedDarkTheme is the resolved Pi palette."}
	for _, size := range [][2]int{{120, 40}, {80, 24}, {60, 16}, {40, 12}, {20, 6}} {
		fixture.Sizes = append(fixture.Sizes, plan5ColorSizeFixture{
			Size:   fmt.Sprintf("%dx%d", size[0], size[1]),
			Before: plan5ColorRows(size[0], nil),
			After:  plan5ColorRows(size[0], theme),
		})
	}
	return fixture
}

func plan5ColorRows(width int, theme *pitheme.Theme) []string {
	display := DefaultDisplaySettings()
	rows := []string{"-- user --"}
	rows = append(rows, FormatUserMessageWithDisplay("Create a concise report.", width, false, display, theme)...)
	rows = append(rows, "-- assistant --")
	rows = append(rows, FormatAssistantMessageWithDisplay("## Progress\n\n- Preserve the current task.", width, false, display, theme)...)
	rows = append(rows, "-- tool --")
	rows = append(rows, FormatToolRowWithDisplay(event.ToolSummary{Kind: "read", Path: "internal/presentation/src/main.go", LineRange: ":4-8", Available: true}, "read", "done", width, false, display, theme))
	rows = append(rows, "-- metadata --")
	rows = append(rows, FormatMetadataMessage("[compaction] ", "Context compacted", width, false, display, theme)...)
	return rows
}
