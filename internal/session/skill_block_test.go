package session

import (
	"testing"
)

func piSkillFixture(body, args string) string {
	text := "<skill name=\"doer\" location=\"/skills/doer/SKILL.md\">\n" +
		"References are relative to /skills/doer.\n\n" + body + "\n</skill>"
	if args != "" {
		text += "\n\n" + args
	}
	return text
}

func userItems(t *testing.T, items []TranscriptItem) []TranscriptItem {
	t.Helper()
	var out []TranscriptItem
	for _, item := range items {
		if item.Kind == KindUser {
			out = append(out, item)
		}
	}
	return out
}

func TestSkillBlockTimeline(t *testing.T) {
	block := piSkillFixture("SKILL_BODY", "run-it")
	legacy := "Skill directory: /skills/doer\nResolve relative paths from this directory.\n\nSKILL_BODY\n\nArguments: run-it"
	malformed := "<skill name=\"doer\">\nReferences are relative to /skills/doer.\n\nSKILL_BODY\n</skill>"
	tests := []struct {
		name       string
		text       string
		invocation string
		want       string
	}{
		{name: "Pi text without provenance", text: block, want: block},
		{name: "psi block with provenance", text: block, invocation: "/skill:doer run-it", want: block},
		{name: "legacy preamble with provenance", text: legacy, invocation: "/skill:doer run-it", want: "/skill:doer run-it"},
		{name: "malformed text with provenance", text: malformed, invocation: "/skill:doer", want: "/skill:doer"},
		{name: "ordinary text with provenance", text: "ordinary request", invocation: "/skill:doer", want: "/skill:doer"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			s, err := New(OpenOptions{Cwd: dir, SessionDir: dir, AgentDir: dir})
			if err != nil {
				t.Fatal(err)
			}
			id, err := s.AppendMessage(AgentMessage{Role: "user", User: &UserMessage{Role: "user", Content: tt.text}})
			if err != nil {
				t.Fatal(err)
			}
			if tt.invocation != "" {
				if _, err := s.AppendProvenance(id, tt.invocation, "skill"); err != nil {
					t.Fatal(err)
				}
			}
			path, err := s.ActivePath()
			if err != nil {
				t.Fatal(err)
			}
			for _, view := range [][]TranscriptItem{CurrentView(path), AllHistoryView(path)} {
				users := userItems(t, view)
				if len(users) != 1 {
					t.Fatalf("user items = %d, want 1: %+v", len(users), users)
				}
				if users[0].Text != tt.want {
					t.Fatalf("timeline text = %q, want %q", users[0].Text, tt.want)
				}
				if users[0].SourceID != id {
					t.Fatalf("source ID = %q, want %q", users[0].SourceID, id)
				}
			}
			recall, err := s.Recall()
			if err != nil {
				t.Fatal(err)
			}
			wantRecall := tt.text
			if tt.invocation != "" {
				wantRecall = tt.invocation
			}
			if len(recall) != 1 || recall[0] != wantRecall {
				t.Fatalf("recall = %q, want %q", recall, wantRecall)
			}
			proj, err := s.Projection()
			if err != nil {
				t.Fatal(err)
			}
			if len(proj.Flat) != 1 || proj.Flat[0].User == nil || proj.Flat[0].User.Content != tt.text {
				t.Fatalf("provider context lost the original text: %+v", proj.Flat)
			}
		})
	}
}
