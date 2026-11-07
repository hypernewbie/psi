package terminal

import (
	"strings"
)

// AppName is the product name psi shows in place of Pi's APP_NAME.
const AppName = "psi"

// Command is one parsed slash command.
type Command struct {
	Name string
	Arg  string
}

// CommandSpec mirrors one of Pi's builtin slash commands. The name, the
// description, and the argument hint come from Pi's BUILTIN_SLASH_COMMANDS.
type CommandSpec struct {
	Name        string
	Description string
	// ArgumentHint is Pi's argumentHint. It stays empty for the commands Pi
	// lists without one.
	ArgumentHint string
}

// commandSpecs mirrors Pi's BUILTIN_SLASH_COMMANDS for the commands psi
// implements, in Pi's order, plus the requested embedded extensions
// (adaptive compaction and the loop). Pi's commands that psi does not
// implement yet stay absent.
var commandSpecs = []CommandSpec{
	{Name: "model", Description: "Select model (opens selector UI)", ArgumentHint: "<provider/model>"},
	{Name: "thinking", Description: "Set thinking level", ArgumentHint: "<level>"},
	{Name: "name", Description: "Set session display name"},
	{Name: "session", Description: "Show session info and stats"},
	{Name: "hotkeys", Description: "Show all keyboard shortcuts"},
	{Name: "clone", Description: "Duplicate the current session at the current position"},
	{Name: "new", Description: "Start a new session"},
	{Name: "compact", Description: "Manually compact the session context"},
	{Name: "resume", Description: "Resume another session"},
	{Name: "reload", Description: "Reload keybindings, extensions, skills, prompts, themes, and context files"},
	{Name: "quit", Description: "Quit " + AppName},
	// Embedded extensions requested for psi: adaptive compaction and the loop.
	{Name: "ac", Description: "adaptive automatic compaction controls"},
	{Name: "autocompact", Description: "alias for /ac"},
	{Name: "loop", Description: "repeat work until goal, count, or user stop"},
	{Name: "loop-stop", Description: "stop the active loop"},
}

// CommandMetadata returns a copy of the command table.
func CommandMetadata() []CommandSpec { return append([]CommandSpec(nil), commandSpecs...) }

// CommandNames returns the command names in Pi's order.
func CommandNames() []string {
	names := make([]string, 0, len(commandSpecs))
	for _, spec := range commandSpecs {
		names = append(names, spec.Name)
	}
	return names
}

// CommandDescription renders the description Pi's autocomplete shows for a
// command: the argument hint and the description joined by an em dash.
func CommandDescription(spec CommandSpec) string {
	if spec.ArgumentHint == "" {
		return spec.Description
	}
	if spec.Description == "" {
		return spec.ArgumentHint
	}
	return spec.ArgumentHint + " — " + spec.Description
}

// CommandDescriptions maps every command name to the completion description
// Pi's autocomplete shows.
func CommandDescriptions() map[string]string {
	descriptions := make(map[string]string, len(commandSpecs))
	for _, spec := range commandSpecs {
		descriptions["/"+spec.Name] = CommandDescription(spec)
	}
	return descriptions
}

// ParseCommand splits "/name arg". An unknown /command is ordinary user
// text, like Pi: it is sent to the model as a prompt.
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
			if arg != "" && !CommandTakesArgument(name) {
				return nil, false
			}
			return &Command{Name: name, Arg: arg}, true
		}
	}
	return nil, false
}

// CommandTakesArgument reports whether Pi accepts an argument for the
// command. Pi matches "/name" and "/name <arg>" only for the commands whose
// handler reads an argument; every other spelling is ordinary user text.
func CommandTakesArgument(name string) bool {
	switch name {
	case "model", "thinking", "name", "compact", "ac", "autocompact", "loop":
		return true
	}
	return false
}

// HotkeysText renders the keyboard shortcuts, like Pi's /hotkeys command.
func HotkeysText() string {
	return strings.Join([]string{
		"Keys (fixed, no Kitty required):",
		"  Enter submit · Ctrl+J newline · Left/Right grapheme · Up/Down visual rows (edges browse history)",
		"  Ctrl+P/N history · Home/End line · Ctrl+A/E line · Ctrl+Home/End doc · Backspace/Delete grapheme",
		"  Ctrl+W word · Ctrl+U/K line · Tab complete /command /skill:name · Esc cancels picker/completion",
		"  Ctrl+X stops the loop and aborts its turn · Shift+Tab cycle thinking · Ctrl+L select model · Ctrl+T hide/show reasoning · Ctrl+C cancels run (idle: clear draft, empty exits)",
		"  Ctrl+D delete/exit · paste literal, no submit",
	}, "\n")
}
