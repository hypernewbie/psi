package terminal

import (
	"fmt"
	"strings"
)

// Commands (P2-045, Section 7.3): bounded inline flows, no fullscreen dialogs.
// The command table drives both help checks and parser tests.

type Command struct {
	Name string
	Arg  string
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
	switch name {
	case "help", "new", "resume", "model", "thinking", "compact", "session", "history", "quit":
		return &Command{Name: name, Arg: arg}, true
	default:
		return &Command{Name: "unknown:" + name, Arg: arg}, true
	}
}

func HelpText() string {
	return strings.Join([]string{
		"Commands (inline, no fullscreen):",
		"  /help                show commands, keys, limits, examples",
		"  /new                 new session (keeps scrollback, resets recall)",
		"  /resume [filter]      select session (20/page, n/p, /filter, number, empty cancels)",
		"  /model               show provider/model; /model [provider] model (session-local)",
		"  /thinking [level]    show or set thinking (off,minimal,low,medium,high,xhigh,max)",
		"  /compact [focus]     real cancellable summary with start/settled outcome",
		"  /session             identity, workspace, provider/model, thinking, continuation, usage",
		"  /history [N|all]     last N user spans (default 20) or full history (display-only)",
		"  /skill:name [args]   resolve skill, persist reproducible input, show invocation",
		"  /quit                settle work, restore terminal (Ctrl+D empty also exits)",
		"",
		"Keys (fixed, no Kitty required):",
		"  Enter submit · Ctrl+J newline · Left/Right grapheme · Up/Down visual rows (edges browse history)",
		"  Ctrl+P/N history · Home/End line · Ctrl+A/E line · Ctrl+Home/End doc · Backspace/Delete grapheme",
		"  Ctrl+W word · Ctrl+U/K line · Tab complete /command /skill:name · Esc cancels selector",
		"  Ctrl+C cancels run (idle: clear draft, empty exits) · Ctrl+D delete/exit · paste literal, no submit",
		"",
		"Limits: busy input is cancellation-only (one quiet notice); unknown /commands never become prompts;",
		"every command works after resume and after cancellation; draft preserved on command errors.",
		"",
		"Examples:",
		"  /model anthropic claude-x · /thinking high · /resume api · /history all · /skill:review fix login",
	}, "\n")
}

func StartupLine(provider, model, workspace, sessionID string, skills int) string {
	return fmt.Sprintf("psi — %s/%s · %s · %s (%d skills)", provider, model, workspace, shortID(sessionID), skills)
}

func shortID(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}
