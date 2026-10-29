package terminal

import (
	"strings"
	"testing"

	"psi/internal/event"
)

func TestPiDisplaySettingsAffectMessageAndToolGeometry(t *testing.T) {
	display := DisplaySettings{OutputPad: 0, AutocompleteMaxVisible: 3, CodeBlockIndent: "> "}
	assistant := FormatAssistantMessageWithDisplay("answer", 40, true, display)
	if len(assistant) == 0 || !strings.HasPrefix(assistant[0], " answer") {
		t.Fatalf("outputPad=0 did not change the message base margin: %q", assistant)
	}
	user := FormatUserMessageWithDisplay("request", 40, true, display)
	if len(user) == 0 || !strings.HasPrefix(user[0], " › request") {
		t.Fatalf("user marker did not follow outputPad: %q", user)
	}
	code := RenderMarkdownWithDisplay("```go\nfmt.Println(1)\n```", 40, true, display)
	found := false
	for _, row := range code {
		if row == "> fmt.Println(1)" {
			found = true
		}
	}
	if !found {
		t.Fatalf("markdown.codeBlockIndent was ignored: %q", code)
	}
	tool := Sanitize(FormatToolRowWithDisplay(event.ToolSummary{Kind: "read", Path: "file.go", Available: true}, "read", "done", 40, true, display))
	if !strings.HasPrefix(tool, "  • read") {
		t.Fatalf("tool inset did not follow outputPad: %q", tool)
	}
}

func TestEditorPaddingAndCompletionVisibilityUseDisplaySnapshot(t *testing.T) {
	c := &Console{Editor: NewEditor(), Out: &strings.Builder{}, writer: NewWriter(&strings.Builder{}), SizeFunc: func() (int, int) { return 40, 12 }}
	c.SetPresentation(PresentationSnapshot{NoColor: true, Display: DisplaySettings{OutputPad: 1, EditorPaddingX: 1, AutocompleteMaxVisible: 3, CodeBlockIndent: "  "}})
	c.Editor.Insert("x")
	view := ComposeConsoleView(40, 12, c.Editor.buf, c.Editor.pos, c.presentation, "", "")
	if view.CursorCol != 2 || !strings.HasPrefix(view.Rows[view.CursorRow], " x") {
		t.Fatalf("editor padding mismatch: cursor=%d row=%q", view.CursorCol, view.Rows[view.CursorRow])
	}
	c.Commands = []string{"/help", "/history", "/model", "/new", "/quit", "/resume", "/session"}
	c.Editor.RestoreDraft("/", 1)
	c.refreshCompletion()
	if len(c.completionRows) != 3 {
		t.Fatalf("autocompleteMaxVisible ignored: rows=%d", len(c.completionRows))
	}
}
