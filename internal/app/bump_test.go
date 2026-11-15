package app

import (
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"psi/internal/agent"
	"psi/internal/provider"
	"psi/internal/session"
	"psi/internal/terminal"
)

func appendBumpUser(t *testing.T, store *session.Store, text string) {
	t.Helper()
	if _, err := store.AppendMessage(session.AgentMessage{Role: "user", User: &session.UserMessage{Role: "user", Content: text, Timestamp: 1}}); err != nil {
		t.Fatal(err)
	}
}

func appendBumpAssistant(t *testing.T, store *session.Store, stop, text string) {
	t.Helper()
	message := session.AgentMessage{Role: "assistant", Assistant: &session.AssistantMessage{
		Role: "assistant", StopReason: stop,
		Content: []session.ContentBlock{{Type: "text", Text: &session.TextContent{Type: "text", Text: text}}},
	}}
	if _, err := store.AppendMessage(message); err != nil {
		t.Fatal(err)
	}
}

func TestBumpAdmission(t *testing.T) {
	cases := []struct {
		name      string
		prepare   func(*testing.T, *runtime)
		unavail   bool
		want      error
		wantText  string
		wantState State
	}{
		{
			name: "eligible user tip",
			prepare: func(t *testing.T, r *runtime) {
				appendBumpUser(t, r.store, "hello")
			},
		},
		{
			name: "completed tip",
			prepare: func(t *testing.T, r *runtime) {
				appendBumpUser(t, r.store, "hello")
				appendBumpAssistant(t, r.store, "stop", "done")
			},
			want: agent.ErrNotBumpable,
		},
		{
			name: "completed tip with unsafe history",
			prepare: func(t *testing.T, r *runtime) {
				appendBumpUser(t, r.store, "old")
				appendBumpAssistant(t, r.store, "deferred", "later")
				appendBumpUser(t, r.store, "now")
				appendBumpAssistant(t, r.store, "stop", "done")
			},
			want: agent.ErrNotBumpable,
		},
		{
			name: "busy run",
			prepare: func(t *testing.T, r *runtime) {
				appendBumpUser(t, r.store, "hello")
			},
			unavail: true,
			want:    agent.ErrNotBumpable,
		},
		{
			name: "compaction state",
			prepare: func(t *testing.T, r *runtime) {
				appendBumpUser(t, r.store, "hello")
				r.state = StateCompacting
			},
			want: agent.ErrNotBumpable,
		},
		{
			name: "selector owns input",
			prepare: func(t *testing.T, r *runtime) {
				appendBumpUser(t, r.store, "hello")
				r.selector = &Selector{}
			},
			want: agent.ErrNotBumpable,
		},
		{
			name: "settings picker owns input",
			prepare: func(t *testing.T, r *runtime) {
				appendBumpUser(t, r.store, "hello")
				r.settingsPicker = &terminal.SettingsPicker{}
			},
			want: agent.ErrNotBumpable,
		},
		{
			name: "auth unavailable",
			prepare: func(t *testing.T, r *runtime) {
				appendBumpUser(t, r.store, "hello")
				r.authErr = errors.New("no synthetic credential")
			},
			wantText: "no synthetic credential",
		},
		{
			name: "offline state without auth error",
			prepare: func(t *testing.T, r *runtime) {
				appendBumpUser(t, r.store, "hello")
				r.state = StateOffline
			},
			want: agent.ErrNotBumpable,
		},
		{
			name: "storage blocked without sticky error",
			prepare: func(t *testing.T, r *runtime) {
				appendBumpUser(t, r.store, "hello")
				r.state = StateStorageBlocked
			},
			wantText:  "storage",
			wantState: StateStorageBlocked,
		},
		{
			name: "changed durable file",
			prepare: func(t *testing.T, r *runtime) {
				appendBumpUser(t, r.store, "hello")
				if _, err := r.store.Projection(); err != nil {
					t.Fatal(err)
				}
				f, err := os.OpenFile(r.store.File(), os.O_APPEND|os.O_WRONLY, 0o600)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := f.WriteString("\n"); err != nil {
					t.Fatal(err)
				}
				if err := f.Close(); err != nil {
					t.Fatal(err)
				}
			},
			wantText:  "storage",
			wantState: StateStorageBlocked,
		},
		{
			name: "unsafe continuation",
			prepare: func(t *testing.T, r *runtime) {
				appendBumpUser(t, r.store, "old")
				appendBumpAssistant(t, r.store, "deferred", "later")
				appendBumpUser(t, r.store, "now")
			},
			wantText: "deferred",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := testRuntime(t)
			r.state = StateIdle
			r.authErr = nil
			fake := &provider.Fake{}
			r.prov = fake
			tc.prepare(t, r)
			before := r.store.EntryCount()
			err := r.checkBump(tc.unavail)
			if tc.want != nil {
				if !errors.Is(err, tc.want) {
					t.Fatalf("checkBump error=%v; want %v", err, tc.want)
				}
			} else if tc.wantText != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantText) {
					t.Fatalf("checkBump error=%v; want %q", err, tc.wantText)
				}
				if errors.Is(err, agent.ErrNotBumpable) {
					t.Fatal("storage/auth refusal reported as informational no-op")
				}
			} else if err != nil {
				t.Fatalf("checkBump error=%v; want eligible", err)
			}
			if fake.CallCount() != 0 {
				t.Fatal("refused check called the provider")
			}
			if r.store.EntryCount() != before {
				t.Fatal("refused check mutated the session")
			}
			if tc.wantState != "" && r.state != tc.wantState {
				t.Fatalf("state=%q; want %q", r.state, tc.wantState)
			}
		})
	}
}

func waitBumpOutput(t *testing.T, out *notifyingBuffer, want string) {
	t.Helper()
	deadline := time.After(8 * time.Second)
	for {
		if strings.Contains(out.String(), want) {
			return
		}
		select {
		case <-out.notify:
		case <-deadline:
			t.Fatalf("timeout waiting for %q: %q", want, out.String())
		}
	}
}

func TestBumpEmptyEnterNotices(t *testing.T) {
	cases := []struct {
		name         string
		prepare      func(*testing.T, *runtime)
		afterStart   func(*testing.T, *runtime)
		refusal      string
		explicitWant string
	}{
		{
			name: "completed tip",
			prepare: func(t *testing.T, r *runtime) {
				appendBumpUser(t, r.store, "hello")
				appendBumpAssistant(t, r.store, "stop", "done")
			},
			refusal:      "Nothing to retry.",
			explicitWant: "Nothing to retry.",
		},
		{
			name: "auth unavailable",
			prepare: func(t *testing.T, r *runtime) {
				appendBumpUser(t, r.store, "hello")
				r.authErr = errors.New("no synthetic credential")
			},
			refusal:      "no synthetic credential",
			explicitWant: "no synthetic credential",
		},
		{
			name: "failed storage",
			prepare: func(t *testing.T, r *runtime) {
				appendBumpUser(t, r.store, "hello")
			},
			afterStart: func(t *testing.T, r *runtime) {
				r.store.FailWritesForTest(errors.New("injected storage failure"))
			},
			refusal:      "storage blocked",
			explicitWant: "storage blocked",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := testRuntime(t)
			r.state = StateIdle
			r.authErr = nil
			fake := &provider.Fake{}
			r.prov = fake
			tc.prepare(t, r)

			input, inputWrite, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			out := &notifyingBuffer{notify: make(chan struct{}, 1)}
			console := terminal.NewConsole(input, out)
			console.SizeFunc = func() (int, int) { return 80, 24 }
			defer func() {
				_ = inputWrite.Close()
				console.Close()
				_ = input.Close()
			}()
			result := make(chan error, 1)
			go func() { result <- r.repl(console) }()
			waitBumpOutput(t, out, "(anthropic) m")
			if tc.afterStart != nil {
				tc.afterStart(t, r)
			}
			beforeEntries := r.store.EntryCount()
			beforeOutput := out.String()
			if _, err := inputWrite.Write([]byte("\r")); err != nil {
				t.Fatal(err)
			}
			if _, err := inputWrite.Write([]byte("/hotkeys\r")); err != nil {
				t.Fatal(err)
			}
			waitBumpOutput(t, out, "Keys (fixed, no Kitty required):")
			segment := strings.TrimPrefix(out.String(), beforeOutput)
			if strings.Contains(segment, tc.refusal) {
				t.Fatalf("empty Enter reported %q; want silence", tc.refusal)
			}
			if fake.CallCount() != 0 {
				t.Fatalf("empty Enter called provider %d times", fake.CallCount())
			}
			if r.store.EntryCount() != beforeEntries {
				t.Fatal("empty Enter mutated the session")
			}
			if _, err := inputWrite.Write([]byte("/b\r")); err != nil {
				t.Fatal(err)
			}
			waitBumpOutput(t, out, tc.explicitWant)
			if fake.CallCount() != 0 {
				t.Fatalf("explicit refusal called provider %d times", fake.CallCount())
			}
			if r.store.EntryCount() != beforeEntries {
				t.Fatal("explicit refusal mutated the session")
			}
			if _, err := inputWrite.Write([]byte("/quit\r")); err != nil {
				t.Fatal(err)
			}
			select {
			case err := <-result:
				if err != nil {
					t.Fatalf("REPL exit error=%v", err)
				}
			case <-time.After(8 * time.Second):
				t.Fatal("REPL did not exit")
			}
		})
	}
}
