package app

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"psi/internal/config"
	"psi/internal/session"
	"psi/internal/terminal"
	"psi/internal/testutil/leakcheck"
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
	t.Cleanup(func() {
		leakcheck.Verify(t)
	})
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

	// /name with newlines gets normalized
	quit, err = r.command(&terminal.Command{Name: "name", Arg: "multi\nline\nname"}, &out)
	if err != nil || quit || !strings.Contains(out.String(), "Session name set: multi line name") {
		t.Errorf("/name newline normalization failed: quit=%v, err=%v, out=%q", quit, err, out.String())
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

	// /clone: invalid arg
	_, err = r.command(&terminal.Command{Name: "clone", Arg: "bad-arg"}, &out)
	if err == nil || !strings.Contains(err.Error(), "use /clone") {
		t.Errorf("/clone with arg should fail: %v", err)
	}
	out.Reset()

	// /clone: empty session (no entries yet)
	quit, err = r.command(&terminal.Command{Name: "clone"}, &out)
	if err != nil || quit || !strings.Contains(out.String(), "Nothing to clone yet") {
		t.Errorf("/clone on empty session failed: quit=%v, err=%v, out=%q", quit, err, out.String())
	}
	out.Reset()

	// Append a message to make session non-empty
	if _, err := r.store.AppendMessage(session.AgentMessage{
		Role: "user",
		User: &session.UserMessage{Role: "user", Content: "clonable content 1"},
	}); err != nil {
		t.Fatal(err)
	}

	// /clone: file stat error
	sessionDir := filepath.Dir(r.store.File())
	if err := os.Chmod(sessionDir, 0000); err == nil {
		out.Reset()
		_, statErr := r.command(&terminal.Command{Name: "clone"}, &out)
		_ = os.Chmod(sessionDir, 0700)
		if statErr == nil {
			t.Errorf("/clone on inaccessible directory should return stat error")
		}
	}
	out.Reset()

	// /clone: non-empty session succeeds
	preCloneID := r.store.Header().ID
	quit, err = r.command(&terminal.Command{Name: "clone"}, &out)
	if err != nil || quit {
		t.Fatalf("/clone failed on non-empty session: %v", err)
	}
	postCloneID := r.store.Header().ID
	if preCloneID == postCloneID {
		t.Errorf("/clone did not create a fresh session ID: %s vs %s", preCloneID, postCloneID)
	}
	if len(r.store.Entries()) == 0 {
		t.Errorf("/clone created session with 0 entries")
	}
	out.Reset()

	// /history: populate transcript with more user/assistant turns
	if _, err := r.store.AppendMessage(session.AgentMessage{
		Role:      "assistant",
		Assistant: &session.AssistantMessage{Role: "assistant", Content: []session.ContentBlock{{Type: "text", Text: &session.TextContent{Type: "text", Text: "assistant reply 1"}}}},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.store.AppendMessage(session.AgentMessage{
		Role: "user",
		User: &session.UserMessage{Role: "user", Content: "clonable content 2"},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.store.AppendMessage(session.AgentMessage{
		Role:      "assistant",
		Assistant: &session.AssistantMessage{Role: "assistant", Content: []session.ContentBlock{{Type: "text", Text: &session.TextContent{Type: "text", Text: "assistant reply 2"}}}},
	}); err != nil {
		t.Fatal(err)
	}

	// /history: valid N
	quit, err = r.command(&terminal.Command{Name: "history", Arg: "1"}, &out)
	if err != nil || quit || !strings.Contains(out.String(), "clonable content 2") {
		t.Errorf("/history 1 failed: quit=%v, err=%v, out=%q", quit, err, out.String())
	}
	out.Reset()

	// /history: all
	quit, err = r.command(&terminal.Command{Name: "history", Arg: "all"}, &out)
	if err != nil || quit || !strings.Contains(out.String(), "clonable content 1") || !strings.Contains(out.String(), "clonable content 2") {
		t.Errorf("/history all failed: quit=%v, err=%v, out=%q", quit, err, out.String())
	}
	out.Reset()

	// /history: empty (defaults to 20)
	quit, err = r.command(&terminal.Command{Name: "history"}, &out)
	if err != nil || quit || !strings.Contains(out.String(), "clonable content") {
		t.Errorf("/history empty failed: quit=%v, err=%v, out=%q", quit, err, out.String())
	}
	out.Reset()

	// /history: negative should return error
	_, err = r.command(&terminal.Command{Name: "history", Arg: "-2"}, &out)
	if err == nil || !strings.Contains(err.Error(), "use /history") {
		t.Errorf("/history -2 should fail: err=%v", err)
	}
	out.Reset()

	// /history: out of bounds (larger than count) should render all gracefully
	quit, err = r.command(&terminal.Command{Name: "history", Arg: "999"}, &out)
	if err != nil || quit || !strings.Contains(out.String(), "clonable content 1") {
		t.Errorf("/history 999 failed: quit=%v, err=%v, out=%q", quit, err, out.String())
	}
	out.Reset()

	// /history: invalid non-numeric string
	_, err = r.command(&terminal.Command{Name: "history", Arg: "invalid"}, &out)
	if err == nil || !strings.Contains(err.Error(), "use /history") {
		t.Errorf("/history invalid should fail: err=%v", err)
	}
	out.Reset()

	// Add openai-codex provider to models file for provider switching test
	if r.models.Providers == nil {
		r.models.Providers = make(map[string]config.ProviderMeta)
	}
	r.models.Providers["openai-codex"] = config.ProviderMeta{
		Models: []config.ModelMeta{{ID: "gpt-5.6-luna", API: "openai-codex-responses", Reasoning: true, MaxTokens: 16384}},
	}

	// /model: empty argument shows current model
	quit, err = r.command(&terminal.Command{Name: "model"}, &out)
	if err != nil || quit || !strings.Contains(out.String(), "current ") {
		t.Errorf("/model empty failed: quit=%v, err=%v, out=%q", quit, err, out.String())
	}
	out.Reset()

	// /model: valid provider/model
	quit, err = r.command(&terminal.Command{Name: "model", Arg: "anthropic/m"}, &out)
	if err != nil || quit {
		t.Errorf("/model anthropic/m failed: quit=%v, err=%v", quit, err)
	}
	if r.ov.Provider != "anthropic" || r.ov.Model != "m" {
		t.Errorf("/model mismatch: provider=%q model=%q", r.ov.Provider, r.ov.Model)
	}

	// Set thinking level to low to verify /model leaves thinking level unchanged
	quit, err = r.command(&terminal.Command{Name: "thinking", Arg: "low"}, &out)
	if err != nil || quit {
		t.Fatalf("/thinking low failed: %v", err)
	}
	if r.ov.Thinking != "low" {
		t.Fatalf("expected thinking low, got %q", r.ov.Thinking)
	}
	entriesBeforeModel := len(r.store.Entries())

	// /model: exact model match switching provider to openai-codex leaves thinking level unchanged
	quit, err = r.command(&terminal.Command{Name: "model", Arg: "openai-codex/gpt-5.6-luna"}, &out)
	if err != nil || quit {
		t.Errorf("/model openai-codex/gpt-5.6-luna failed: quit=%v, err=%v", quit, err)
	}
	if r.ov.Provider != "openai-codex" || r.ov.Model != "gpt-5.6-luna" {
		t.Errorf("provider switch mismatch: provider=%q model=%q", r.ov.Provider, r.ov.Model)
	}
	if r.ov.Thinking != "low" {
		t.Errorf("/model exact match must leave thinking level unchanged: got %q, want %q", r.ov.Thinking, "low")
	}
	if len(r.store.Entries()) != entriesBeforeModel+1 {
		t.Errorf("/model appended unexpected number of entries: got %d, want %d", len(r.store.Entries()), entriesBeforeModel+1)
	}
	if lastEntry := r.store.Entries()[len(r.store.Entries())-1]; lastEntry.Kind != "model_change" {
		t.Errorf("/model should only append model_change, got %q", lastEntry.Kind)
	}

	// /model: model reference with suffix like claude-3-7-sonnet:high is not stripped
	_, err = r.command(&terminal.Command{Name: "model", Arg: "anthropic/claude-3-7-sonnet:high"}, &out)
	if err == nil || !strings.Contains(err.Error(), "claude-3-7-sonnet:high") {
		t.Errorf("/model with thinking suffix should not be stripped: got err %v", err)
	}
	if r.ov.Provider != "openai-codex" || r.ov.Model != "gpt-5.6-luna" || r.ov.Thinking != "low" {
		t.Errorf("failed model reference lookup mutated state: provider=%q model=%q thinking=%q", r.ov.Provider, r.ov.Model, r.ov.Thinking)
	}

	// /model: bare model reference with suffix like claude-3-7-sonnet:high is not stripped
	_, err = r.command(&terminal.Command{Name: "model", Arg: "claude-3-7-sonnet:high"}, &out)
	if err == nil || !strings.Contains(err.Error(), "claude-3-7-sonnet:high") {
		t.Errorf("/model bare model with thinking suffix should not be stripped: got err %v", err)
	}

	// /model: unknown provider error
	_, err = r.command(&terminal.Command{Name: "model", Arg: "nonexistent-provider/foo"}, &out)
	if err == nil || !strings.Contains(err.Error(), "unsupported provider") {
		t.Errorf("/model unknown provider should fail: %v", err)
	}

	// /model: invalid formats (3 parts)
	_, err = r.command(&terminal.Command{Name: "model", Arg: "part1 part2 part3"}, &out)
	if err == nil || !strings.Contains(err.Error(), "use /model") {
		t.Errorf("/model with 3 parts should fail: %v", err)
	}

	// /model: empty model after slash
	_, err = r.command(&terminal.Command{Name: "model", Arg: "anthropic/"}, &out)
	if err == nil || !strings.Contains(err.Error(), "use /model") {
		t.Errorf("/model with trailing slash should fail: %v", err)
	}

	// Switch back to anthropic/m with high MaxTokens for thinking level tests
	r.models.Providers["anthropic"] = config.ProviderMeta{
		Models: []config.ModelMeta{{ID: "m", Reasoning: true, MaxTokens: 64000}},
	}
	if _, err := r.command(&terminal.Command{Name: "model", Arg: "anthropic/m"}, &out); err != nil {
		t.Fatal(err)
	}

	// /thinking: empty arg shows description
	quit, err = r.command(&terminal.Command{Name: "thinking"}, &out)
	if err != nil || quit || !strings.Contains(out.String(), "thinking ") {
		t.Errorf("/thinking empty failed: quit=%v, err=%v, out=%q", quit, err, out.String())
	}
	out.Reset()

	// /thinking: all valid levels
	for _, lvl := range []string{"off", "minimal", "low", "medium", "high", "xhigh", "max"} {
		quit, err = r.command(&terminal.Command{Name: "thinking", Arg: lvl}, &out)
		if err != nil || quit {
			t.Errorf("/thinking %q failed: quit=%v, err=%v", lvl, quit, err)
		}
		if r.ov.Thinking != lvl {
			t.Errorf("/thinking level mismatch: got %q, want %q", r.ov.Thinking, lvl)
		}
	}

	// /thinking: invalid level error
	_, err = r.command(&terminal.Command{Name: "thinking", Arg: "invalid-level"}, &out)
	if err == nil || !strings.Contains(err.Error(), "unsupported thinking level") {
		t.Errorf("/thinking invalid-level should fail: %v", err)
	}

	// /thinking: no-op when same as current level
	entriesBefore := len(r.store.Entries())
	quit, err = r.command(&terminal.Command{Name: "thinking", Arg: r.ov.Thinking}, &out)
	if err != nil || quit {
		t.Errorf("/thinking no-op failed: quit=%v, err=%v", quit, err)
	}
	if len(r.store.Entries()) != entriesBefore {
		t.Errorf("/thinking no-op added entries to store: before=%d, after=%d", entriesBefore, len(r.store.Entries()))
	}

	// /reload: arg error
	_, err = r.command(&terminal.Command{Name: "reload", Arg: "unexpected"}, &out)
	if err == nil || !strings.Contains(err.Error(), "use /reload") {
		t.Errorf("/reload with arg should fail: %v", err)
	}

	// /reload: valid settings reload
	settingsPath := filepath.Join(r.agentDir, "settings.json")
	if err := os.WriteFile(settingsPath, []byte(`{"theme":"dark"}`), 0600); err != nil {
		t.Fatal(err)
	}
	quit, err = r.command(&terminal.Command{Name: "reload"}, &out)
	if err != nil || quit || !strings.Contains(out.String(), "theme reloaded:") {
		t.Errorf("/reload valid settings failed: quit=%v, err=%v, out=%q", quit, err, out.String())
	}
	out.Reset()

	// /reload: error propagation on corrupted settings
	if err := os.WriteFile(settingsPath, []byte(`{not valid json`), 0600); err != nil {
		t.Fatal(err)
	}
	_, err = r.command(&terminal.Command{Name: "reload"}, &out)
	if err == nil {
		t.Errorf("/reload with corrupt settings should return error")
	}

	// /quit
	quit, err = r.command(&terminal.Command{Name: "quit"}, &out)
	if err != nil || !quit {
		t.Errorf("/quit failed: quit=%v, err=%v", quit, err)
	}

	// Unknown command
	_, err = r.command(&terminal.Command{Name: "nonexistent-cmd"}, &out)
	if err == nil || !strings.Contains(err.Error(), "unknown command") {
		t.Errorf("unknown command should fail: %v", err)
	}
}

func TestCLISplitArgs(t *testing.T) {
	tests := []struct {
		name         string
		args         []string
		wantFlags    []string
		wantMessages []string
		wantErr      bool
	}{
		{
			name:         "empty",
			args:         []string{},
			wantFlags:    nil,
			wantMessages: nil,
			wantErr:      false,
		},
		{
			name:         "messages only",
			args:         []string{"hello", "world"},
			wantFlags:    nil,
			wantMessages: []string{"hello", "world"},
		},
		{
			name:         "flags only",
			args:         []string{"--provider", "anthropic", "--model", "m"},
			wantFlags:    []string{"--provider", "anthropic", "--model", "m"},
			wantMessages: nil,
		},
		{
			name:         "flags with equals",
			args:         []string{"--provider=anthropic", "--model=m"},
			wantFlags:    []string{"--provider=anthropic", "--model=m"},
			wantMessages: nil,
		},
		{
			name:         "interspersed messages and flags",
			args:         []string{"msg1", "--offline", "msg2", "--provider", "anthropic", "msg3"},
			wantFlags:    []string{"--offline", "--provider", "anthropic"},
			wantMessages: []string{"msg1", "msg2", "msg3"},
		},
		{
			name:         "--list-models without argument",
			args:         []string{"--list-models"},
			wantFlags:    []string{"--list-models="},
			wantMessages: nil,
		},
		{
			name:         "--list-models with argument",
			args:         []string{"--list-models", "sonnet"},
			wantFlags:    []string{"--list-models=sonnet"},
			wantMessages: nil,
		},
		{
			name:         "--list-models with equals",
			args:         []string{"--list-models=haiku"},
			wantFlags:    []string{"--list-models=haiku"},
			wantMessages: nil,
		},
		{
			name:         "--list-models followed by another flag",
			args:         []string{"--list-models", "--offline"},
			wantFlags:    []string{"--list-models=", "--offline"},
			wantMessages: nil,
		},
		{
			name:         "--list-models followed by @file (not consumed as list filter)",
			args:         []string{"--list-models", "@fixture.txt"},
			wantFlags:    []string{"--list-models="},
			wantMessages: []string{"@fixture.txt"},
		},
		{
			name:         "stdin single dash message",
			args:         []string{"-", "--provider", "anthropic"},
			wantFlags:    []string{"--provider", "anthropic"},
			wantMessages: []string{"-"},
		},
		{
			name:         "double dash separator breaks option parsing",
			args:         []string{"--provider", "anthropic", "--", "--not-a-flag", "final-message"},
			wantFlags:    []string{"--provider", "anthropic"},
			wantMessages: []string{"--not-a-flag", "final-message"},
		},
		{
			name:    "print mode is rejected",
			args:    []string{"--print"},
			wantErr: true,
		},
		{
			name:    "print shorthand is rejected",
			args:    []string{"-p"},
			wantErr: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			flags, msgs, err := splitCLIArgs(tc.args)
			if (err != nil) != tc.wantErr {
				t.Fatalf("splitCLIArgs(%v) err = %v, wantErr = %v", tc.args, err, tc.wantErr)
			}
			if tc.wantErr {
				return
			}
			if len(flags) != len(tc.wantFlags) || (len(flags) > 0 && !reflect.DeepEqual(flags, tc.wantFlags)) {
				t.Errorf("flags = %v, want %v", flags, tc.wantFlags)
			}
			if len(msgs) != len(tc.wantMessages) || (len(msgs) > 0 && !reflect.DeepEqual(msgs, tc.wantMessages)) {
				t.Errorf("messages = %v, want %v", msgs, tc.wantMessages)
			}
		})
	}
}

func TestCLINormalizeModelOption(t *testing.T) {
	tests := []struct {
		name         string
		in           Options
		wantProvider string
		wantModel    string
		wantThinking string
		wantErr      bool
	}{
		{
			name:         "empty options",
			in:           Options{},
			wantProvider: "",
			wantModel:    "",
			wantThinking: "",
		},
		{
			name:         "provider only case normalization",
			in:           Options{Provider: "ANTHROPIC"},
			wantProvider: "anthropic",
			wantModel:    "",
		},
		{
			name:         "model alone without suffix",
			in:           Options{Model: "claude-3-5-sonnet"},
			wantProvider: "",
			wantModel:    "claude-3-5-sonnet",
			wantThinking: "",
		},
		{
			name:         "model with thinking level suffix",
			in:           Options{Model: "claude-3-5-sonnet:high"},
			wantProvider: "",
			wantModel:    "claude-3-5-sonnet",
			wantThinking: "high",
		},
		{
			name:         "model with provider prefix",
			in:           Options{Model: "anthropic/claude-3-5-sonnet"},
			wantProvider: "anthropic",
			wantModel:    "claude-3-5-sonnet",
		},
		{
			name:         "model with provider prefix and thinking suffix",
			in:           Options{Model: "anthropic/claude-3-5-sonnet:low"},
			wantProvider: "anthropic",
			wantModel:    "claude-3-5-sonnet",
			wantThinking: "low",
		},
		{
			name:         "matching provider option and model prefix",
			in:           Options{Provider: "anthropic", Model: "anthropic/claude-3-5-sonnet"},
			wantProvider: "anthropic",
			wantModel:    "claude-3-5-sonnet",
		},
		{
			name:    "conflicting provider option and model prefix",
			in:      Options{Provider: "openai", Model: "anthropic/claude-3-5-sonnet"},
			wantErr: true,
		},
		{
			name:    "empty model string after trim",
			in:      Options{Model: "   "},
			wantErr: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			opts := tc.in
			err := normalizeModelOption(&opts)
			if (err != nil) != tc.wantErr {
				t.Fatalf("normalizeModelOption(%+v) err = %v, wantErr = %v", tc.in, err, tc.wantErr)
			}
			if tc.wantErr {
				return
			}
			if opts.Provider != tc.wantProvider {
				t.Errorf("Provider = %q, want %q", opts.Provider, tc.wantProvider)
			}
			if opts.Model != tc.wantModel {
				t.Errorf("Model = %q, want %q", opts.Model, tc.wantModel)
			}
			if opts.Thinking != tc.wantThinking {
				t.Errorf("Thinking = %q, want %q", opts.Thinking, tc.wantThinking)
			}
		})
	}
}

func TestCLIParsingFlags(t *testing.T) {
	t.Run("list-models flag variations", func(t *testing.T) {
		opts, _, err := Parse([]string{"--list-models"})
		if err != nil || !opts.ListModels || opts.ListModelsFilter != "" {
			t.Fatalf("--list-models bare failed: %+v err=%v", opts, err)
		}

		opts, _, err = Parse([]string{"--list-models", "sonnet"})
		if err != nil || !opts.ListModels || opts.ListModelsFilter != "sonnet" {
			t.Fatalf("--list-models with value failed: %+v err=%v", opts, err)
		}

		opts, _, err = Parse([]string{"--list-models=haiku"})
		if err != nil || !opts.ListModels || opts.ListModelsFilter != "haiku" {
			t.Fatalf("--list-models with equals failed: %+v err=%v", opts, err)
		}
	})

	t.Run("append-system-prompt repeatable", func(t *testing.T) {
		opts, _, err := Parse([]string{
			"--append-system-prompt", "rule 1",
			"--append-system-prompt", "rule 2",
			"--append-system-prompt", "rule 3",
		})
		if err != nil {
			t.Fatalf("Parse error: %v", err)
		}
		want := []string{"rule 1", "rule 2", "rule 3"}
		if !reflect.DeepEqual(opts.AppendSystemPrompts, want) {
			t.Errorf("AppendSystemPrompts = %v, want %v", opts.AppendSystemPrompts, want)
		}
	})

	t.Run("offline flag", func(t *testing.T) {
		opts, _, err := Parse([]string{"--offline"})
		if err != nil || !opts.Offline {
			t.Fatalf("--offline failed: %+v err=%v", opts, err)
		}
	})

	t.Run("allow-project aliases", func(t *testing.T) {
		for _, flag := range []string{"--allow-project", "--approve", "-a"} {
			opts, _, err := Parse([]string{flag})
			if err != nil || !opts.AllowProj {
				t.Fatalf("%s failed: %+v err=%v", flag, opts, err)
			}
		}
	})

	t.Run("deny-project aliases", func(t *testing.T) {
		for _, flag := range []string{"--deny-project", "--no-approve", "-na"} {
			opts, _, err := Parse([]string{flag})
			if err != nil || !opts.DenyProj {
				t.Fatalf("%s failed: %+v err=%v", flag, opts, err)
			}
		}
	})

	t.Run("allow and deny conflict", func(t *testing.T) {
		conflicts := [][]string{
			{"--allow-project", "--deny-project"},
			{"--approve", "--no-approve"},
			{"-a", "-na"},
			{"--allow-project", "-na"},
		}
		for _, args := range conflicts {
			_, _, err := Parse(args)
			if err == nil || !strings.Contains(err.Error(), "conflict") {
				t.Errorf("Parse(%v) should return conflict error, got %v", args, err)
			}
		}
	})

	t.Run("unsupported @file message produces error", func(t *testing.T) {
		_, _, err := Parse([]string{"@my-file.txt"})
		if err == nil || !strings.Contains(err.Error(), "Pi @file arguments are not supported") {
			t.Errorf("Parse(@file) expected error, got %v", err)
		}
	})
}
