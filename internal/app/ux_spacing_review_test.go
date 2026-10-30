package app

import (
	"bytes"
	"strings"
	"testing"

	"psi/internal/session"
	"psi/internal/terminal"
)

func TestConfigurationChangesDoNotAppendChatLogs(t *testing.T) {
	r := testRuntime(t)
	var out bytes.Buffer
	for _, cmd := range []terminal.Command{{Name: "thinking", Arg: "high"}, {Name: "thinking", Arg: "minimal"}, {Name: "model", Arg: "anthropic/m"}} {
		if _, err := r.command(&cmd, &out); err != nil {
			t.Fatal(err)
		}
	}
	if out.Len() != 0 {
		t.Fatalf("configuration updates flooded transcript: %q", out.String())
	}
	before := r.store.EntryCount()
	if _, err := r.command(&terminal.Command{Name: "thinking", Arg: r.ov.Thinking}, &out); err != nil {
		t.Fatal(err)
	}
	if r.store.EntryCount() != before {
		t.Fatal("unchanged thinking level added a durable event")
	}
	proj, err := r.store.Projection()
	if err != nil {
		t.Fatal(err)
	}
	if proj.State.ThinkingLevel != "minimal" || proj.State.ModelID != "m" {
		t.Fatalf("quiet changes were not durable: %+v", proj.State)
	}
}

func TestReplayConfigurationChangesAreStateNotChatMessages(t *testing.T) {
	r := testRuntime(t)
	r.store.AppendMessage(agentMessageUser("VISIBLE_QUERY"))
	r.store.AppendModelChange("anthropic", "m")
	r.store.AppendThinkingLevelChange("high")
	r.store.AppendThinkingLevelChange("low")
	for _, all := range []bool{false, true} {
		var rows []string
		complete, err := r.store.Timeline(all, 0, func(it session.TranscriptItem) bool {
			return renderTimelineChecked(func(line string) bool { rows = append(rows, line); return true }, []session.TranscriptItem{it})
		})
		if err != nil || !complete {
			t.Fatalf("timeline failed: %v", err)
		}
		view := strings.Join(rows, "\n")
		if !strings.Contains(view, "VISIBLE_QUERY") || strings.Contains(view, "thinking:") || strings.Contains(view, "model:") {
			t.Fatalf("configuration records became chat logs (all=%v): %q", all, view)
		}
	}
}
