package app

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"psi/internal/event"
	"psi/internal/session"
	"psi/internal/terminal"
	"psi/internal/testutil"
)

// P2-003: resume must display conversation through every entry point.
// Three-turn session with text, tool result, second assistant message.
func TestResumeDisplaysHistory(t *testing.T) {
	_, work, _, sessions := testutil.IsolatedEnv(t)
	sessFile := filepath.Join(sessions, "ws", "s.jsonl")
	os.MkdirAll(filepath.Dir(sessFile), 0o755)
	// Marshal the workspace path: Windows temp dirs contain backslashes,
	// which are invalid raw in a JSON string.
	cwdJSON, err := json.Marshal(work)
	if err != nil {
		t.Fatal(err)
	}
	lines := []string{
		`{"type":"session","version":3,"id":"r1","timestamp":"2026-01-01T00:00:00.000Z","cwd":` + string(cwdJSON) + `}`,
		`{"type":"message","id":"u1","parentId":null,"timestamp":"2026-01-01T00:00:01.000Z","message":{"role":"user","content":"first","timestamp":1}}`,
		`{"type":"message","id":"a1","parentId":"u1","timestamp":"2026-01-01T00:00:02.000Z","message":{"role":"assistant","content":[{"type":"text","text":"checking"}],"api":"anthropic-messages","provider":"anthropic","model":"m","usage":{"input":1,"output":1,"cacheRead":0,"cacheWrite":0,"totalTokens":2,"cost":{"input":0,"output":0,"cacheRead":0,"cacheWrite":0,"total":0}},"stopReason":"stop","timestamp":2}}`,
		`{"type":"message","id":"a2","parentId":"a1","timestamp":"2026-01-01T00:00:03.000Z","message":{"role":"assistant","content":[{"type":"toolCall","id":"c1","name":"read","arguments":{}}],"api":"anthropic-messages","provider":"anthropic","model":"m","usage":{"input":1,"output":1,"cacheRead":0,"cacheWrite":0,"totalTokens":2,"cost":{"input":0,"output":0,"cacheRead":0,"cacheWrite":0,"total":0}},"stopReason":"tool_use","timestamp":3}}`,
		`{"type":"message","id":"t1","parentId":"a2","timestamp":"2026-01-01T00:00:04.000Z","message":{"role":"toolResult","toolCallId":"c1","toolName":"read","content":[{"type":"text","text":"file body"}],"timestamp":4}}`,
		`{"type":"message","id":"a3","parentId":"t1","timestamp":"2026-01-01T00:00:05.000Z","message":{"role":"assistant","content":[{"type":"text","text":"done"}],"api":"anthropic-messages","provider":"anthropic","model":"m","usage":{"input":1,"output":1,"cacheRead":0,"cacheWrite":0,"totalTokens":2,"cost":{"input":0,"output":0,"cacheRead":0,"cacheWrite":0,"total":0}},"stopReason":"stop","timestamp":5}}`,
	}
	var data string
	for _, l := range lines {
		data += l + "\n"
	}
	os.WriteFile(sessFile, []byte(data), 0o644)

	open := func() *session.Store {
		st, err := session.OpenFile(sessFile, work, false)
		if err != nil {
			t.Fatalf("open: %v", err)
		}
		return st
	}
	// Every entry point must produce the same current-view timeline with all visible turns.
	for _, st := range []*session.Store{open(), open(), open()} {
		act, err := PrepareActivation(st)
		if err != nil {
			t.Fatalf("prepare: %v", err)
		}
		timeline := activationTimeline(t, act)
		if len(timeline) < 4 {
			t.Fatalf("resume hid history: %d items", len(timeline))
		}
		foundUser, foundTool, foundSecond := false, false, false
		for _, it := range timeline {
			if it.Kind == session.KindUser && it.Text == "first" {
				foundUser = true
			}
			if it.Kind == session.KindTool {
				foundTool = true
			}
			if it.Kind == session.KindAssistant && it.Text == "done" {
				foundSecond = true
			}
		}
		if !foundUser || !foundTool || !foundSecond {
			t.Fatalf("missing turns: user=%v tool=%v second=%v timeline=%v", foundUser, foundTool, foundSecond, timeline)
		}
		// Recall must include the user submission.
		if len(act.Recall) == 0 || act.Recall[0] != "first" {
			t.Fatalf("recall missing: %v", act.Recall)
		}
	}
}

// Replayed tool calls use the same compact, inset row as live settlements.
func TestReplayedToolRowKeepsTheLiveMargin(t *testing.T) {
	var lines []string
	items := []session.TranscriptItem{{Kind: session.KindTool, Text: "read: done"}}
	renderTimelineAtWidthWithDisplayAndColor(func(s string) bool {
		lines = append(lines, s)
		return true
	}, items, 80, terminal.DefaultDisplaySettings(), true)
	want := terminal.FormatToolRow(event.ToolSummary{Kind: "read", Note: "summary unavailable"}, "read", "done", 80, true)
	if len(lines) != 2 || lines[0] != want || lines[1] != "" {
		t.Fatalf("replayed tool row or trailing space differs from the live format: got %q want %q followed by a blank line", lines, want)
	}
}
