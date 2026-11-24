package session

import (
	"fmt"
	"strings"
	"testing"
)

func shellRecallStore(t *testing.T) *Store {
	t.Helper()
	store, err := New(OpenOptions{Cwd: t.TempDir(), AgentDir: t.TempDir(), SessionDir: t.TempDir(), Persist: true})
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func appendRecallUser(t *testing.T, store *Store, text string) {
	t.Helper()
	if _, err := store.AppendMessage(AgentMessage{Role: "user", User: &UserMessage{Role: "user", Content: text, Timestamp: 1}}); err != nil {
		t.Fatal(err)
	}
}

func TestBangShellRecall(t *testing.T) {
	t.Run("both prefixes and malformed commands", func(t *testing.T) {
		store := shellRecallStore(t)
		appendRecallUser(t, store, "ordinary prompt")
		for _, message := range []AgentMessage{
			bashMessage("echo public", "public\n", false),
			bashMessage("printf private", "private\n", true),
			bashMessage(" leading", "bad\n", false),
			bashMessage("carriage\rreturn", "bad\n", false),
			bashMessage("", "bad\n", false),
		} {
			if _, err := store.AppendMessage(message); err != nil {
				t.Fatal(err)
			}
		}
		want := []string{"ordinary prompt", "! echo public", "!! printf private"}
		path := store.Entries()
		if got := PromptRecall(path); strings.Join(got, "\x00") != strings.Join(want, "\x00") {
			t.Fatalf("PromptRecall = %q, want %q", got, want)
		}
		got, err := store.Recall()
		if err != nil {
			t.Fatal(err)
		}
		if strings.Join(got, "\x00") != strings.Join(want, "\x00") {
			t.Fatalf("Store.Recall = %q, want %q", got, want)
		}
	})

	t.Run("latest two hundred entries", func(t *testing.T) {
		store := shellRecallStore(t)
		for i := 0; i < 210; i++ {
			if _, err := store.AppendMessage(bashMessage(fmt.Sprintf("echo %03d", i), "x\n", false)); err != nil {
				t.Fatal(err)
			}
		}
		got, err := store.Recall()
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 200 {
			t.Fatalf("recall length = %d, want 200", len(got))
		}
		if got[0] != "! echo 010" || got[len(got)-1] != "! echo 209" {
			t.Fatalf("recall bounds = %q ... %q", got[0], got[len(got)-1])
		}
		prompt := PromptRecall(store.Entries())
		if len(prompt) != 200 || prompt[0] != "! echo 010" || prompt[len(prompt)-1] != "! echo 209" {
			t.Fatalf("PromptRecall bounds = %d %q ... %q", len(prompt), prompt[0], prompt[len(prompt)-1])
		}
	})

	t.Run("store budget counts the bang prefix", func(t *testing.T) {
		for _, test := range []struct {
			name    string
			command int
			want    bool
		}{
			{name: "exact", command: 4*1024*1024 - 2, want: true},
			{name: "one over", command: 4*1024*1024 - 1, want: false},
		} {
			t.Run(test.name, func(t *testing.T) {
				store := shellRecallStore(t)
				if _, err := store.AppendMessage(bashMessage(strings.Repeat("a", test.command), "x\n", false)); err != nil {
					t.Fatal(err)
				}
				got, err := store.Recall()
				if err != nil {
					t.Fatal(err)
				}
				if (len(got) == 1) != test.want {
					t.Fatalf("recall entries = %d, want present=%t", len(got), test.want)
				}
			})
		}
	})
}
