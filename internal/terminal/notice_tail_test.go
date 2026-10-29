package terminal

import (
	"strings"
	"testing"

	"psi/internal/testutil"
)

func TestNoticePrefixSurvivesAppendAndOwnedTailRedraw(t *testing.T) {
	const width = 40
	rows := FormatNotice(NoticeError, "the first failure detail wraps onto another row", width, true)
	if len(rows) < 2 || !strings.HasPrefix(rows[0], " Error:") || !strings.HasPrefix(rows[1], " ") {
		t.Fatalf("notice lost its first character or continuation inset: %q", rows)
	}
	vt := testutil.NewVTModel(width, 14)
	c := &Console{Editor: NewEditor(), Out: vtFeed{vt}, writer: NewWriter(vtFeed{vt}), SizeFunc: func() (int, int) { return width, 14 }}
	c.SetPresentation(PresentationSnapshot{WorkingDirectory: "/work", Model: "model", NoColor: true})
	c.Draw()
	if _, err := c.Write([]byte(strings.Join(rows, "\r\n") + "\r\n")); err != nil {
		t.Fatal(err)
	}
	c.Draw()
	c.Draw()
	visible := strings.Join(vt.Scrollback, "\n") + "\n" + vt.Text()
	if strings.Count(visible, "Error:") != 1 || !strings.Contains(visible, "failure detail") || !strings.Contains(visible, "model") {
		t.Fatalf("notice append or tail redraw lost/duplicated content: %q", visible)
	}
}
