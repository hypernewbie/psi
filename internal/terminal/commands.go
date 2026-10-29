package terminal

import (
	"fmt"
	"strings"
)

// Commands (P2-045, Section 7.3): bounded inline flows, no fullscreen dialogs.
// The shared metadata drives parsing, help, and slash completion.

type Command struct {
	Name string
	Arg  string
}

type CommandSpec struct {
	Name        string
	Syntax      string
	Description string
}

var commandSpecs = []CommandSpec{
	{Name: "help", Syntax: "/help", Description: "show commands, keys, limits, examples"},
	{Name: "new", Syntax: "/new", Description: "new session (keeps scrollback, resets recall)"},
	{Name: "resume", Syntax: "/resume [filter]", Description: "select session (search, arrows, Enter, Esc)"},
	{Name: "model", Syntax: "/model [provider/]model", Description: "select or set a session-local model"},
	{Name: "thinking", Syntax: "/thinking [level]", Description: "select or set thinking level"},
	{Name: "compact", Syntax: "/compact [focus]", Description: "real cancellable summary with settled outcome"},
	{Name: "session", Syntax: "/session", Description: "identity, workspace, provider/model, thinking, usage"},
	{Name: "reload", Syntax: "/reload", Description: "reload theme and terminal display data"},
	{Name: "history", Syntax: "/history [N|all]", Description: "last N user spans or display full history"},
	{Name: "quit", Syntax: "/quit", Description: "settle work, restore terminal"},
}

func CommandMetadata() []CommandSpec { return append([]CommandSpec(nil), commandSpecs...) }

func CommandNames() []string {
	names := make([]string, 0, len(commandSpecs))
	for _, spec := range commandSpecs {
		names = append(names, spec.Name)
	}
	return names
}

// ParseCommand splits "/name arg". Unknown /commands are distinct from user
// text per the documented rule: they error instead of sending as a prompt.
func ParseCommand(line string) (*Command, bool) {
	t := strings.TrimSpace(line)
	if !strings.HasPrefix(t, "/") {
		return nil, false
	}
	// /skill:name family.
	if strings.HasPrefix(t, "/skill:") {
		name := strings.TrimPrefix(t, "/skill:")
		parts := strings.SplitN(name, " ", 2)
		arg := ""
		if len(parts) == 2 {
			arg = parts[1]
		}
		return &Command{Name: "skill:" + parts[0], Arg: arg}, true
	}
	parts := strings.SplitN(t[1:], " ", 2)
	name := parts[0]
	arg := ""
	if len(parts) == 2 {
		arg = strings.TrimSpace(parts[1])
	}
	for _, spec := range commandSpecs {
		if name == spec.Name {
			return &Command{Name: name, Arg: arg}, true
		}
	}
	return &Command{Name: "unknown:" + name, Arg: arg}, true
}

func HelpText() string {
	lines := []string{"Commands (inline, no fullscreen):"}
	for _, spec := range commandSpecs {
		lines = append(lines, fmt.Sprintf("  %-22s %s", spec.Syntax, spec.Description))
	}
	lines = append(lines,
		"  /skill:name [args]   resolve skill, persist reproducible input, show invocation",
		"",
		"Keys (fixed, no Kitty required):",
		"  Enter submit · Ctrl+J newline · Left/Right grapheme · Up/Down visual rows (edges browse history)",
		"  Ctrl+P/N history · Home/End line · Ctrl+A/E line · Ctrl+Home/End doc · Backspace/Delete grapheme",
		"  Ctrl+W word · Ctrl+U/K line · Tab complete /command /skill:name · Esc cancels picker/completion",
		"  Shift+Tab cycle thinking · Ctrl+L select model · Ctrl+C cancels run (idle: clear draft, empty exits)",
		"  Ctrl+D delete/exit · paste literal, no submit",
		"",
		"Limits: busy input is cancellation-only (one quiet notice); unknown /commands never become prompts;",
		"every command works after resume and after cancellation; draft preserved on command errors.",
		"",
		"Examples:",
		"  /model anthropic claude-x · /thinking high · /resume api · /history all · /skill:review fix login")
	return strings.Join(lines, "\n")
}
