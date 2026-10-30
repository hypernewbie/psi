package app

import (
	"bytes"
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

func TestRuntimeCommandsDirectly(t *testing.T) {
	r := testRuntime(t)
	var out bytes.Buffer

	// /help
	quit, err := r.command(&terminal.Command{Name: "help"}, &out)
	if err != nil || quit || !strings.Contains(out.String(), "Commands") {
		t.Errorf("/help failed: quit=%v, err=%v, out=%q", quit, err, out.String())
	}
	out.Reset()

	// /hotkeys
	quit, err = r.command(&terminal.Command{Name: "hotkeys"}, &out)
	if err != nil || quit || !strings.Contains(out.String(), "Keys") {
		t.Errorf("/hotkeys failed: quit=%v, err=%v, out=%q", quit, err, out.String())
	}
	out.Reset()

	// /hotkeys with arg should fail
	_, err = r.command(&terminal.Command{Name: "hotkeys", Arg: "invalid"}, &out)
	if err == nil {
		t.Errorf("/hotkeys with arg should return error")
	}
	out.Reset()

	// /session
	quit, err = r.command(&terminal.Command{Name: "session"}, &out)
	if err != nil || quit || !strings.Contains(out.String(), "session ") {
		t.Errorf("/session failed: quit=%v, err=%v, out=%q", quit, err, out.String())
	}
	out.Reset()

	// /name without arg (none set yet)
	quit, err = r.command(&terminal.Command{Name: "name"}, &out)
	if err != nil || quit {
		t.Errorf("/name empty failed: quit=%v, err=%v", quit, err)
	}
	out.Reset()

	// /name with arg
	quit, err = r.command(&terminal.Command{Name: "name", Arg: "my-test-session"}, &out)
	if err != nil || quit || !strings.Contains(out.String(), "Session name set: my-test-session") {
		t.Errorf("/name with arg failed: quit=%v, err=%v, out=%q", quit, err, out.String())
	}
	out.Reset()

	// /name again should print current name
	quit, err = r.command(&terminal.Command{Name: "name"}, &out)
	if err != nil || quit || !strings.Contains(out.String(), "Session name: my-test-session") {
		t.Errorf("/name query failed: quit=%v, err=%v, out=%q", quit, err, out.String())
	}
	out.Reset()

	// /new session
	oldID := r.store.Header().ID
	quit, err = r.command(&terminal.Command{Name: "new"}, &out)
	if err != nil || quit || !strings.Contains(out.String(), "new session ") {
		t.Errorf("/new failed: quit=%v, err=%v, out=%q", quit, err, out.String())
	}
	newID := r.store.Header().ID
	if oldID == newID {
		t.Errorf("/new did not update session ID")
	}
	out.Reset()

	// /quit
	quit, err = r.command(&terminal.Command{Name: "quit"}, &out)
	if err != nil || !quit {
		t.Errorf("/quit failed: quit=%v, err=%v", quit, err)
	}
}

