package app

import (
	"strings"
	"testing"

	"psi/internal/terminal"
)

// P2-045: command table drives help + parser. Unknown commands never become prompts.
func TestCommandTable(t *testing.T) {
	cmds := []string{"help", "new", "resume", "model", "thinking", "compact", "loop", "loop-stop", "session", "history", "name", "clone", "hotkeys", "quit"}
	help := terminal.HelpText()
	for _, c := range cmds {
		if !strings.Contains(help, "/"+c) {
			t.Errorf("help missing /%s", c)
		}
		if _, ok := terminal.ParseCommand("/" + c); !ok {
			t.Errorf("parser rejects /%s", c)
		}
	}
	if !strings.Contains(terminal.HotkeysText(), "\n") || strings.Contains(terminal.HotkeysText(), `\n`) {
		t.Errorf("/hotkeys must render separate lines: %q", terminal.HotkeysText())
	}
	// Skill + unknown.
	if cmd, ok := terminal.ParseCommand("/skill:review fix"); !ok || cmd.Name != "skill:review" {
		t.Errorf("skill parse: %v %v", cmd, ok)
	}
	if cmd, ok := terminal.ParseCommand("/nope"); !ok || !strings.HasPrefix(cmd.Name, "unknown:") {
		t.Errorf("unknown should be explicit, got %v %v", cmd, ok)
	}
	// Plain text is not a command.
	if _, ok := terminal.ParseCommand("hello"); ok {
		t.Error("text parsed as command")
	}
	if _, ok := terminal.ParseCommand(""); ok {
		t.Error("empty parsed as command")
	}
}
