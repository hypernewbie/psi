package terminal

import (
	"bytes"
	"strings"
	"testing"
)

func skillBlockSource(name, body, args string) string {
	source := "<skill name=\"" + name + "\" location=\"/skills/" + name + "/SKILL.md\">\n" +
		"References are relative to /skills/" + name + ".\n\n" + body + "\n</skill>"
	if args != "" {
		source += "\n\n" + args
	}
	return source
}

func TestSkillBlockFormatting(t *testing.T) {
	source := skillBlockSource("doer", "SKILL_BODY", "run-it")
	collapsed := FormatUserMessageWithDisplay(source, 80, true, DefaultDisplaySettings())
	joined := strings.Join(collapsed, "\n")
	if !strings.Contains(joined, "[skill] doer (ctrl+o to expand)") {
		t.Fatalf("collapsed row missing: %q", collapsed)
	}
	if !strings.Contains(joined, "› run-it") {
		t.Fatalf("args missing from the user surface: %q", collapsed)
	}
	if strings.Contains(joined, "SKILL_BODY") {
		t.Fatalf("collapsed skill rendered its hidden body: %q", collapsed)
	}
	if strings.Count(joined, "›") != 1 {
		t.Fatalf("args should be one separate user group: %q", collapsed)
	}

	display := DefaultDisplaySettings()
	display.ExpandSkills = true
	expanded := FormatUserMessageWithDisplay(source, 80, true, display)
	joined = strings.Join(expanded, "\n")
	if !strings.Contains(joined, "[skill]") || !strings.Contains(joined, "doer") {
		t.Fatalf("expanded group lost its label or name: %q", expanded)
	}
	if !strings.Contains(joined, "SKILL_BODY") || !strings.Contains(joined, "References are relative to /skills/doer.") {
		t.Fatalf("expanded group lost the body: %q", expanded)
	}
	if strings.Contains(joined, "ctrl+o to expand") {
		t.Fatalf("expanded group kept the collapse hint: %q", expanded)
	}
	if strings.Count(joined, "› run-it") != 1 {
		t.Fatalf("args should appear once after the group: %q", expanded)
	}

	// Expanded skill Markdown honors the configured code-block indent.
	display.CodeBlockIndent = "    "
	code := skillBlockSource("doer", "```sh\necho hi\n```", "")
	codeRows := FormatUserMessageWithDisplay(code, 80, true, display)
	if !strings.Contains(strings.Join(codeRows, "\n"), "\n      echo hi\n") {
		t.Fatalf("skill code block ignored CodeBlockIndent: %q", codeRows)
	}

	// An argument tail that looks like another skill block stays ordinary text.
	nested := skillBlockSource("inner", "INNER_BODY", "")
	outer := skillBlockSource("outer", "OUTER_BODY", nested)
	joined = strings.Join(FormatUserMessageWithDisplay(outer, 80, true, DefaultDisplaySettings()), "\n")
	if strings.Count(joined, "[skill]") != 1 {
		t.Fatalf("argument tail was parsed recursively: %q", joined)
	}
	if strings.Contains(joined, "OUTER_BODY") {
		t.Fatalf("collapsed outer skill rendered its body: %q", joined)
	}

	// Malformed text keeps the ordinary user surface.
	malformed := "<skill name=\"doer\">\nReferences are relative to /skills/doer.\n\nBODY\n</skill>"
	joined = strings.Join(FormatUserMessageWithDisplay(malformed, 80, true, DefaultDisplaySettings()), "\n")
	if strings.Contains(joined, "[skill]") || !strings.Contains(joined, "›") {
		t.Fatalf("malformed block did not fall back to plain user text: %q", joined)
	}

	// Plain user output stays byte-identical for ordinary text.
	plain := FormatUserMessageWithDisplay("hello", 40, true, DefaultDisplaySettings())
	if len(plain) != 2 || plain[0] != "  › hello" || plain[1] != "" {
		t.Fatalf("ordinary user rendering changed: %q", plain)
	}

	// No-color output never carries SGR.
	for _, rows := range [][]string{collapsed, expanded} {
		for _, row := range rows {
			if strings.Contains(row, "\x1b") {
				t.Fatalf("no-color skill row carried an escape: %q", row)
			}
		}
	}

	// Untrusted names and bodies cannot inject terminal controls.
	hostile := skillBlockSource("evil\x1b[31m", "BEL\x07BODY", "")
	for _, rows := range [][]string{
		FormatUserMessageWithDisplay(hostile, 80, true, DefaultDisplaySettings()),
		FormatUserMessageWithDisplay(hostile, 80, true, display),
	} {
		for _, row := range rows {
			if strings.ContainsAny(row, "\x1b\x07") {
				t.Fatalf("control sequence leaked: %q", row)
			}
		}
	}

	// Long CJK names and the hint wrap instead of disappearing.
	long := skillBlockSource(strings.Repeat("长", 20)+"-doer", "BODY", "")
	rows := FormatUserMessageWithDisplay(long, 24, true, DefaultDisplaySettings())
	joined = strings.Join(rows, "")
	if !strings.Contains(joined, "ctrl+o") || !strings.Contains(joined, "expand") {
		t.Fatalf("wrapped hint disappeared: %q", rows)
	}
	if len(rows) < 3 {
		t.Fatalf("long name did not wrap: %q", rows)
	}
	for _, row := range rows {
		if CellWidth(row) > 23 {
			t.Fatalf("row exceeds the width boundary: %q", row)
		}
	}

	// Tiny widths use the existing fallback and never slice bytes.
	for width := 1; width <= 4; width++ {
		for _, expand := range []bool{false, true} {
			display := DefaultDisplaySettings()
			display.ExpandSkills = expand
			for _, row := range FormatUserMessageWithDisplay(source, width, true, display) {
				if CellWidth(row) > max(0, width-1) {
					t.Fatalf("width %d row exceeds boundary: %q", width, row)
				}
			}
		}
	}
}

func TestSkillBlockLedgerRelayout(t *testing.T) {
	raw := skillBlockSource("doer", "SKILL_BODY", "run-it")
	var output bytes.Buffer
	c := &Console{
		Out:          &output,
		Editor:       NewEditor(),
		writer:       NewWriter(&output),
		SizeFunc:     func() (int, int) { return 30, 12 },
		presentation: PresentationSnapshot{NoColor: true},
	}
	if err := c.AppendTranscriptBlock(DocumentBlock{Kind: DocumentUser, Text: raw}); err != nil {
		t.Fatal(err)
	}
	if len(c.documentBlocks) != 1 || c.documentBlocks[0].Text != raw {
		t.Fatalf("ledger did not retain one raw block: %+v", c.documentBlocks)
	}
	for _, width := range []int{24, 80} {
		for _, expanded := range []bool{false, true} {
			display := DefaultDisplaySettings()
			display.ExpandSkills = expanded
			rows := FormatDocument([]DocumentBlock{{Kind: DocumentUser, Text: c.documentBlocks[0].Text}}, width,
				PresentationSnapshot{Display: display, NoColor: true})
			joined := strings.Join(rows, "\n")
			if strings.Count(joined, "[skill]") != 1 {
				t.Fatalf("width %d expanded %t: skill label not shown exactly once: %q", width, expanded, rows)
			}
			if strings.Count(joined, "› run-it") != 1 {
				t.Fatalf("width %d expanded %t: args not shown once: %q", width, expanded, rows)
			}
			if strings.Index(joined, "[skill]") > strings.Index(joined, "› run-it") {
				t.Fatalf("width %d expanded %t: args precede the skill group: %q", width, expanded, rows)
			}
			if expanded {
				if strings.Count(joined, "SKILL_BODY") != 1 {
					t.Fatalf("width %d: expanded body not shown once: %q", width, rows)
				}
			} else if strings.Contains(joined, "SKILL_BODY") {
				t.Fatalf("width %d: collapsed body leaked: %q", width, rows)
			}
		}
	}
	if c.documentBlocks[0].Text != raw {
		t.Fatalf("relayout rewrote the retained source: %q", c.documentBlocks[0].Text)
	}
}
