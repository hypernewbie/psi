package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hypernewbie/psi/internal/tools"
)

type fakeConversationIO struct {
	messages []historyMessage
}

func (f fakeConversationIO) readMessages(context.Context, string, int, string, string) ([]historyMessage, error) {
	return f.messages, nil
}

func (f fakeConversationIO) message(context.Context, string, string) (historyMessage, error) {
	if len(f.messages) == 0 {
		return historyMessage{}, os.ErrNotExist
	}
	return f.messages[0], nil
}

func TestMessageToolReadAndSend(t *testing.T) {
	dir := writeWorkspace(t, map[string]string{"MEMORY.md": "fact"})
	turn := &activeTurn{channelID: "423456789012345678"}
	set, err := buildTools(dir, true, true, toolPolicy{}, nil, turn, fakeConversationIO{messages: []historyMessage{{ID: "1", Author: "Aster", Text: "hello"}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	read := set.handlers["message"](context.Background(), tools.Invocation{Arguments: map[string]any{"action": "read", "limit": float64(5)}})
	if read.IsError || !strings.Contains(read.Content[0].Text, "hello") {
		t.Fatalf("read = %+v", read)
	}
	one := set.handlers["message"](context.Background(), tools.Invocation{Arguments: map[string]any{"action": "read", "messageId": "1"}})
	if one.IsError || !strings.Contains(one.Content[0].Text, "hello") {
		t.Fatalf("single read = %+v", one)
	}
	both := set.handlers["message"](context.Background(), tools.Invocation{Arguments: map[string]any{"action": "read", "messageId": "1", "before": "2"}})
	if !both.IsError {
		t.Fatal("messageId with a range was accepted")
	}
	send := set.handlers["message"](context.Background(), tools.Invocation{Arguments: map[string]any{"action": "send", "text": "answer"}})
	if send.IsError || !turn.stagedOK || turn.staged != "answer" {
		t.Fatalf("send = %+v, staged = %q", send, turn.staged)
	}
	second := set.handlers["message"](context.Background(), tools.Invocation{Arguments: map[string]any{"action": "send", "text": "again"}})
	if !second.IsError {
		t.Fatal("second send was accepted")
	}
	tooLarge := set.handlers["message"](context.Background(), tools.Invocation{Arguments: map[string]any{"action": "send", "text": strings.Repeat("x", maxReplyText+1)}})
	if !tooLarge.IsError {
		t.Fatal("oversized send was staged")
	}
}

func TestMessageToolSendDisabledInAutomaticMode(t *testing.T) {
	dir := writeWorkspace(t, nil)
	turn := &activeTurn{channelID: "423456789012345678"}
	set, err := buildTools(dir, false, false, toolPolicy{}, nil, turn, fakeConversationIO{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	result := set.handlers["message"](context.Background(), tools.Invocation{Arguments: map[string]any{"action": "send", "text": "answer"}})
	if !result.IsError {
		t.Fatal("send was accepted in automatic mode")
	}
}

func TestToolPolicyDenyAndMemoryScope(t *testing.T) {
	dir := writeWorkspace(t, map[string]string{"MEMORY.md": "fact"})
	set, err := buildTools(dir, true, false, toolPolicy{Deny: []string{"memory_search"}}, nil, &activeTurn{channelID: "423456789012345678"}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := set.handlers["memory_search"]; ok {
		t.Fatal("denied tool kept a handler")
	}
	shared, err := buildTools(dir, false, false, toolPolicy{}, nil, &activeTurn{channelID: "423456789012345678"}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := shared.handlers["memory_get"]; ok {
		t.Fatal("shared route loaded memory tools")
	}
}

func TestSkillToolLoadsOnlyDiscoveredWorkspaceSkill(t *testing.T) {
	dir := writeWorkspace(t, map[string]string{
		"skills/review/SKILL.md": "---\nname: review\ndescription: Review a change\n---\nCheck the diff.",
	})
	found, diagnostics := discoverWorkspaceSkills(dir)
	if len(diagnostics) != 0 || len(found) != 1 {
		t.Fatalf("skills = %+v, diagnostics = %+v", found, diagnostics)
	}
	set, err := buildTools(dir, false, false, toolPolicy{}, found, &activeTurn{channelID: "423456789012345678"}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	result := set.handlers["skill"](context.Background(), tools.Invocation{Arguments: map[string]any{"name": "review"}})
	if result.IsError || !strings.Contains(result.Content[0].Text, "Check the diff.") {
		t.Fatalf("skill = %+v", result)
	}
	if strings.Contains(result.Content[0].Text, dir) {
		t.Fatalf("skill exposed the workspace path: %q", result.Content[0].Text)
	}
	if missing := set.handlers["skill"](context.Background(), tools.Invocation{Arguments: map[string]any{"name": "absent"}}); !missing.IsError {
		t.Fatal("unknown skill succeeded")
	}
}

func TestToolProfilesFilterImplementedTools(t *testing.T) {
	dir := writeWorkspace(t, map[string]string{"MEMORY.md": "fact"})
	io := fakeConversationIO{}
	for profile, want := range map[string][]string{
		"minimal":   nil,
		"coding":    {"memory_search", "memory_get"},
		"messaging": {"message"},
		"full":      {"memory_search", "memory_get", "message"},
	} {
		set, err := buildTools(dir, true, false, toolPolicy{Profile: profile}, nil, &activeTurn{channelID: "423456789012345678"}, io, nil)
		if err != nil {
			t.Fatal(err)
		}
		for _, name := range want {
			if _, ok := set.handlers[name]; !ok {
				t.Fatalf("profile %s lacks %s: %v", profile, name, set.handlers)
			}
		}
		if profile != "full" && len(set.handlers) != len(want) {
			t.Fatalf("profile %s handlers = %v, want %v", profile, set.handlers, want)
		}
	}
}

func TestToolPolicyAllowAlsoAllowAndDeny(t *testing.T) {
	dir := writeWorkspace(t, map[string]string{"MEMORY.md": "fact"})
	set, err := buildTools(dir, true, false, toolPolicy{Profile: "minimal", AlsoAllow: []string{"memory_*"}, Deny: []string{"memory_get"}}, nil, &activeTurn{channelID: "423456789012345678"}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := set.handlers["memory_search"]; !ok {
		t.Fatal("alsoAllow did not add memory_search")
	}
	if _, ok := set.handlers["memory_get"]; ok {
		t.Fatal("deny did not remove memory_get")
	}
	only, err := buildTools(dir, true, false, toolPolicy{Allow: []string{"memory_get"}}, nil, &activeTurn{channelID: "423456789012345678"}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(only.handlers) != 1 {
		t.Fatalf("allow list = %v", only.handlers)
	}
}

func TestSkillToolBoundsLargeInstructions(t *testing.T) {
	body := strings.Repeat("x", maxSkillOutput+1)
	dir := writeWorkspace(t, map[string]string{
		"skills/large/SKILL.md": "---\nname: large\ndescription: Large skill\n---\n" + body,
	})
	found, diagnostics := discoverWorkspaceSkills(dir)
	if len(diagnostics) != 0 || len(found) != 1 {
		t.Fatalf("skills = %+v, diagnostics = %+v", found, diagnostics)
	}
	set, err := buildTools(dir, false, false, toolPolicy{}, found, &activeTurn{}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	result := set.handlers["skill"](context.Background(), tools.Invocation{Arguments: map[string]any{"name": "large"}})
	if result.IsError {
		t.Fatalf("skill = %+v", result)
	}
	text := result.Content[0].Text
	if len(text) > maxSkillOutput || !strings.HasSuffix(text, "[truncated]") {
		t.Fatalf("bounded skill length = %d, suffix = %q", len(text), text[max(0, len(text)-12):])
	}
}

func TestSkillToolRejectsALinkedReplacement(t *testing.T) {
	dir := writeWorkspace(t, map[string]string{
		"skills/review/SKILL.md": "---\nname: review\ndescription: Review a change\n---\nCheck the diff.",
	})
	found, diagnostics := discoverWorkspaceSkills(dir)
	if len(diagnostics) != 0 || len(found) != 1 {
		t.Fatalf("skills = %+v, diagnostics = %+v", found, diagnostics)
	}
	outside := filepath.Join(t.TempDir(), "secret.md")
	if err := os.WriteFile(outside, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	skillPath := filepath.Join(dir, "skills", "review", "SKILL.md")
	if err := os.Remove(skillPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, skillPath); err != nil {
		t.Skip("symlinks unavailable")
	}
	set, err := buildTools(dir, false, false, toolPolicy{}, found, &activeTurn{}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	result := set.handlers["skill"](context.Background(), tools.Invocation{Arguments: map[string]any{"name": "review"}})
	if !result.IsError {
		t.Fatal("linked skill replacement was read")
	}
}

// TestSkillDiscoverySurvivesALinkedWorkspace proves the skill discovery works
// when the workspace is reached through a link. macOS hands out temporary
// directories whose real path differs from the name it returns, and Windows
// resolves a short name to its long form.
func TestSkillDiscoverySurvivesALinkedWorkspace(t *testing.T) {
	real := writeWorkspace(t, map[string]string{
		"skills/review/SKILL.md": "---\nname: review\ndescription: Review a change\n---\nCheck the diff.",
	})
	link := filepath.Join(t.TempDir(), "linked-workspace")
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("links are not available here: %v", err)
	}
	found, diagnostics := discoverWorkspaceSkills(link)
	if len(diagnostics) != 0 || len(found) != 1 {
		t.Fatalf("skills = %+v, diagnostics = %+v", found, diagnostics)
	}
}
