package app

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"psi/internal/session"
	"psi/internal/terminal"
)

func replayedSkillText(name, body, args string) string {
	text := "<skill name=\"" + name + "\" location=\"/skills/" + name + "/SKILL.md\">\n" +
		"References are relative to /skills/" + name + ".\n\n" + body + "\n</skill>"
	if args != "" {
		text += "\n\n" + args
	}
	return text
}

func TestSkillBlockReplay(t *testing.T) {
	dir := t.TempDir()
	store, err := session.New(session.OpenOptions{Cwd: dir, SessionDir: filepath.Join(dir, "sessions"), AgentDir: dir, Persist: true})
	if err != nil {
		t.Fatal(err)
	}
	first := replayedSkillText("first", "FIRST_BODY", "first-args")
	second := replayedSkillText("second", "SECOND_BODY", "second-args")
	if _, err := store.AppendMessage(agentMessageUser(first)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AppendMessage(session.AgentMessage{Role: "assistant", Assistant: &session.AssistantMessage{
		Role: "assistant", API: "anthropic-messages", Provider: "anthropic", Model: "synthetic", StopReason: "stop",
		Content: []session.ContentBlock{{Type: "text", Text: &session.TextContent{Type: "text", Text: "AFTER_SKILL"}}},
	}}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AppendMessage(session.AgentMessage{Role: "assistant", Assistant: &session.AssistantMessage{
		Role: "assistant", API: "anthropic-messages", Provider: "anthropic", Model: "synthetic", StopReason: "toolUse",
		Content: []session.ContentBlock{{Type: "toolCall", Call: &session.ToolCall{Type: "toolCall", ID: "call-1", Name: "read"}}},
	}}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AppendMessage(session.AgentMessage{Role: "toolResult", ToolResult: &session.ToolResultMessage{
		Role: "toolResult", ToolCallID: "call-1", ToolName: "read",
		Content: []session.UserContentBlock{{Type: "text", Text: &session.TextContent{Type: "text", Text: "tool output"}}},
	}}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AppendMessage(agentMessageUser(second)); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(store.File())
	if err != nil {
		t.Fatal(err)
	}

	var items []session.TranscriptItem
	if _, err := store.Timeline(false, 0, func(item session.TranscriptItem) bool {
		items = append(items, item)
		return true
	}); err != nil {
		t.Fatal(err)
	}
	activation, err := PrepareActivation(store)
	if err != nil {
		t.Fatal(err)
	}
	var blocks []terminal.DocumentBlock
	if !replayActivationDocument(func(block terminal.DocumentBlock) bool {
		blocks = append(blocks, block)
		return true
	}, activation, true, "") {
		t.Fatal("activation replay stopped early")
	}
	if len(blocks) != len(items)+1 || blocks[0].Kind != terminal.DocumentBoundary {
		t.Fatalf("retained blocks = %d, items = %d", len(blocks), len(items))
	}
	docBlocks := blocks[1:]
	for _, expanded := range []bool{false, true} {
		display := terminal.DefaultDisplaySettings()
		display.ExpandSkills = expanded
		rows := terminal.FormatDocument(docBlocks, 80, terminal.PresentationSnapshot{Display: display, NoColor: true})
		var lines []string
		if !renderTimelineAtWidthWithDisplayAndColor(func(line string) bool {
			lines = append(lines, line)
			return true
		}, items, 80, display, true) {
			t.Fatal("checked width replay stopped early")
		}
		if !reflect.DeepEqual(rows, lines) {
			t.Fatalf("expanded %t: retained document and width replay differ\nretained %#v\nreplay   %#v", expanded, rows, lines)
		}
		joined := strings.Join(rows, "\n")
		if expanded {
			if strings.Count(joined, "FIRST_BODY") != 1 || strings.Count(joined, "SECOND_BODY") != 1 {
				t.Fatalf("expanded replay lost a body: %q", rows)
			}
			if strings.Contains(joined, "ctrl+o to expand") {
				t.Fatalf("expanded replay kept the collapse hint: %q", rows)
			}
		} else {
			for _, body := range []string{"FIRST_BODY", "SECOND_BODY"} {
				if strings.Contains(joined, body) {
					t.Fatalf("collapsed replay exposed %q: %q", body, rows)
				}
			}
		}
		if strings.Count(joined, "› first-args") != 1 || strings.Count(joined, "› second-args") != 1 {
			t.Fatalf("replay lost an argument group: %q", rows)
		}
		if strings.Index(joined, "[skill] first") > strings.Index(joined, "› first-args") ||
			strings.Index(joined, "[skill] second") > strings.Index(joined, "› second-args") {
			t.Fatalf("args precede their skill group: %q", rows)
		}
		// The tool batch keeps its separator before the following skill group.
		labels, checked, toolBefore := 0, false, false
		for i, row := range rows {
			if strings.Contains(row, "read") {
				toolBefore = true
			}
			trimmed := strings.TrimSpace(row)
			if trimmed != "[skill]" && !strings.HasPrefix(trimmed, "[skill] ") {
				continue
			}
			labels++
			if labels != 2 {
				continue
			}
			if i == 0 || rows[i-1] != "" {
				t.Fatalf("tool group did not close before the skill: %q", rows)
			}
			if !toolBefore {
				t.Fatalf("second skill did not follow the tool group: %q", rows)
			}
			if trimmed == "[skill]" && (i+1 >= len(rows) || !strings.Contains(rows[i+1], "second")) {
				t.Fatalf("expanded second skill lost its name: %q", rows)
			}
			checked = true
		}
		if !checked {
			t.Fatalf("second skill missing after the tool group: %q", rows)
		}
	}
	after, err := os.ReadFile(store.File())
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("replay rewrote the session file")
	}

	// A refused retained callback keeps only the blocks emitted before it.
	var partial []terminal.DocumentBlock
	if replayActivationDocument(func(block terminal.DocumentBlock) bool {
		partial = append(partial, block)
		return len(partial) < 2
	}, activation, true, "") {
		t.Fatal("retained replay ignored callback refusal")
	}
	if len(partial) != 2 || partial[1].Kind != terminal.DocumentUser || !strings.Contains(partial[1].Text, "FIRST_BODY") {
		t.Fatalf("retained replay kept later blocks: %+v", partial)
	}
	// The checked width walk stops on the same line boundary.
	var emitted []string
	if renderTimelineAtWidthWithDisplayAndColor(func(line string) bool {
		emitted = append(emitted, line)
		return false
	}, items, 80, terminal.DefaultDisplaySettings(), true) {
		t.Fatal("checked width replay ignored callback refusal")
	}
	if len(emitted) != 1 || strings.Contains(strings.Join(emitted, "\n"), "AFTER_SKILL") {
		t.Fatalf("checked width replay emitted later items: %q", emitted)
	}
}
