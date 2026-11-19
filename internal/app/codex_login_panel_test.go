package app

import (
	"strings"
	"testing"
)

func TestCodexLoginPanelManualBrowserLink(t *testing.T) {
	link := "https://auth.example/authorize?state=" + strings.Repeat("x", 370) + "&end=1"
	panel := codexLoginPanel{link: link, manualOpen: true, temporaryPort: true}

	short := strings.Join(panel.rows(80, 7, true, nil), "\n")
	if !strings.Contains(short, "Temporary port") || !strings.Contains(short, "Enlarge the terminal") || strings.Contains(short, "https://") {
		t.Fatalf("short panel must warn and offer recovery without showing a partial link: %q", short)
	}

	rows := panel.rows(80, 10, true, nil)
	var displayed strings.Builder
	for _, row := range rows {
		row = strings.TrimSpace(row)
		if displayed.Len() > 0 || strings.HasPrefix(row, "https://") {
			displayed.WriteString(row)
			if strings.HasSuffix(row, "&end=1") {
				break
			}
		}
	}
	if displayed.String() != link {
		t.Fatalf("resized panel did not show the complete link: %q", rows)
	}
}

func TestCodexDeviceLoginPanelKeepsCodeAndURLInCompactView(t *testing.T) {
	panel := codexLoginPanel{code: "ABCD-EFGH", link: "https://auth.openai.com/codex/device"}
	rows := strings.Join(panel.rows(40, 2, true, nil), "\n")
	if !strings.Contains(rows, panel.code) || !strings.Contains(rows, panel.link) {
		t.Fatalf("compact panel hid device sign-in instructions: %q", rows)
	}
}
