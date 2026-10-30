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
	{Name: "loop", Syntax: "/loop goal|forever|<N> <task>", Description: "repeat work until goal, count, or user stop"},
	{Name: "loop-stop", Syntax: "/loop-stop", Description: "stop the active loop"},
	{Name: "session", Syntax: "/session", Description: "identity, workspace, provider/model, thinking, usage"},
	{Name: "reload", Syntax: "/reload", Description: "reload theme and terminal display data"},
	{Name: "history", Syntax: "/history [N|all]", Description: "last N user spans or display full history"},
	{Name: "name", Syntax: "/name [name]", Description: "set or show the session display name"},
	{Name: "clone", Syntax: "/clone", Description: "duplicate this session into a new one"},
	{Name: "hotkeys", Syntax: "/hotkeys", Description: "show keyboard shortcuts"},
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

func HotkeysText() string {
	return strings.Join([]string{
		"Keys (fixed, no Kitty required):",
		"  Enter submit · Ctrl+J newline · Left/Right grapheme · Up/Down visual rows (edges browse history)",
		"  Ctrl+P/N history · Home/End line · Ctrl+A/E line · Ctrl+Home/End doc · Backspace/Delete grapheme",
		"  Ctrl+W word · Ctrl+U/K line · Tab complete /command /skill:name · Esc cancels picker/completion",
		"  Ctrl+X stops the loop and aborts its turn · Shift+Tab cycle thinking · Ctrl+L select model · Ctrl+C cancels run (idle: clear draft, empty exits)",
		"  Ctrl+D delete/exit · paste literal, no submit",
	}, "\n")
}

func HelpText() string {
	lines := []string{"Commands (inline, no fullscreen):"}
	for _, spec := range commandSpecs {
		lines = append(lines, fmt.Sprintf("  %-22s %s", spec.Syntax, spec.Description))
	}
	lines = append(lines,
		"  /skill:name [args]   resolve skill, persist reproducible input, show invocation",
		"",
		HotkeysText(),
		"",
		"Busy input stays active: Enter queues steering; Esc/Ctrl+C cancels; undelivered steering returns to the draft.",
		"Unknown /commands never become prompts; command errors preserve the draft.",
		"",
		"Examples:",
		"  /model anthropic claude-x · /thinking high · /resume api · /history all · /loop goal improve tests · /name Refactor auth · /skill:review fix login")
	return strings.Join(lines, "\n")
}
