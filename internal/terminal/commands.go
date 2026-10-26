package terminal

import (
	"fmt"
	"strings"
)

// Commands (Step 84): /help /new /resume /model /thinking /compact /session /quit
// plus /skill:name. Bounded interactions, no fullscreen dialogs.

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
	case "help", "new", "resume", "model", "thinking", "compact", "session", "quit":
		return &Command{Name: name, Arg: arg}, true
	default:
		return &Command{Name: "unknown:" + name, Arg: arg}, true
	}
}

func HelpText() string {
	return strings.Join([]string{
		"Commands:",
		"  /help              show this help",
		"  /new               start a new session",
		"  /resume            resume a session (numbered list)",
		"  /model PROVIDER MODEL  switch model (session-local)",
		"  /thinking LEVEL    switch thinking (off,minimal,low,medium,high,xhigh,max)",
		"  /compact [focus]   compact context now",
		"  /session           show session path and usage",
		"  /skill:name [args] invoke a skill",
		"  /quit              exit (Ctrl+D also exits)",
		"",
		"Keys: Enter submit, Ctrl+J newline, Ctrl+C cancel run (idle: clear draft, again: exit).",
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
