package session

import (
	"os"
	"strings"
	"testing"
)

func bashMessage(command, output string, exclude bool) AgentMessage {
	zero := 0
	return AgentMessage{Role: "bashExecution", BashExecution: &BashExecutionMessage{
		Role:               "bashExecution",
		Command:            command,
		Output:             output,
		ExitCode:           &zero,
		Timestamp:          1234,
		ExcludeFromContext: &exclude,
	}}
}

func TestBangShellFirstMessageDurable(t *testing.T) {
	for _, exclude := range []bool{false, true} {
		name := "included"
		if exclude {
			name = "excluded"
		}
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			cwd := t.TempDir()
			store, err := New(OpenOptions{Cwd: cwd, AgentDir: dir, SessionDir: dir, Persist: true})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := store.AppendModelChange("provider", "model"); err != nil {
				t.Fatal(err)
			}
			if _, err := store.AppendThinkingLevelChange("off"); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(store.File()); !os.IsNotExist(err) {
				t.Fatal("setup-only entries created a file")
			}
			if _, err := store.AppendMessage(bashMessage("printf '%s\\n' shell", "shell\n", exclude)); err != nil {
				t.Fatal(err)
			}
			file := store.File()
			if file == "" {
				t.Fatal("first shell message has no session path")
			}
			if _, err := os.Stat(file); err != nil {
				t.Fatalf("first shell message did not create a session file: %v", err)
			}
			data, err := os.ReadFile(file)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(data), `"role":"bashExecution"`) {
				t.Fatalf("session JSONL lacks bashExecution role: %s", data)
			}
			reopened, err := OpenFile(file, cwd, false)
			if err != nil {
				t.Fatal(err)
			}
			defer reopened.Unlock()
			entries := reopened.Entries()
			found := 0
			for _, entry := range entries {
				if entry.Message == nil || entry.Message.Message.BashExecution == nil {
					continue
				}
				found++
				message := entry.Message.Message.BashExecution
				if message.Command != "printf '%s\\n' shell" || message.Output != "shell\n" || message.ExcludeFromContext == nil || *message.ExcludeFromContext != exclude {
					t.Fatalf("reopened message = %+v", message)
				}
			}
			if found != 1 {
				t.Fatalf("reopened shell entries = %d", found)
			}
			for _, entry := range entries {
				if entry.Message != nil && entry.Message.Message.Role == "user" {
					t.Fatal("shell first message seeded a user prompt")
				}
			}
		})
	}

	t.Run("in-memory writes no file", func(t *testing.T) {
		store, err := New(OpenOptions{Cwd: t.TempDir(), AgentDir: t.TempDir(), SessionDir: t.TempDir(), Persist: false})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := store.AppendMessage(bashMessage("pwd", "/tmp\n", false)); err != nil {
			t.Fatal(err)
		}
		if store.File() != "" {
			t.Fatalf("in-memory shell wrote %q", store.File())
		}
		if len(store.Entries()) != 1 {
			t.Fatalf("in-memory entries = %d", len(store.Entries()))
		}
	})
}
