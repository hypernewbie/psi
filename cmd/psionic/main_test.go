package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCheckDoesNotWriteOrConnect(t *testing.T) {
	agentDir := t.TempDir()
	writeTestModels(t, agentDir)
	t.Setenv("PSI_CODING_AGENT_DIR", agentDir)
	workspace := writeWorkspace(t, map[string]string{"AGENTS.md": "rules"})
	configPath := writeTestSettings(t, workspace)
	stateDir := t.TempDir()
	var stdout, stderr bytes.Buffer
	err := run([]string{"--config", configPath, "--state-dir", stateDir, "--check"}, &stdout, &stderr)
	if err != nil {
		t.Fatalf("check: %v\n%s", err, stderr.String())
	}
	output := stdout.String()
	if !strings.Contains(output, "model: demo/large-model") {
		t.Fatalf("output = %q", output)
	}
	if strings.Contains(output, "test-token") {
		t.Fatal("token value was printed")
	}
	if _, err := os.Stat(filepath.Join(agentDir, "sessions")); !os.IsNotExist(err) {
		t.Fatal("check created a session directory")
	}
	if entries, err := os.ReadDir(stateDir); err != nil || len(entries) != 0 {
		t.Fatalf("check changed the state directory: %v, %v", entries, err)
	}
}

func TestPrepareUsesPsiModelInsteadOfExternalModelSettings(t *testing.T) {
	agentDir := t.TempDir()
	writeTestModels(t, agentDir)
	workspace := t.TempDir()
	path := filepath.Join(t.TempDir(), "settings.json5")
	body := fmt.Sprintf(`{
		agents: { defaults: { workspace: %q, model: { primary: "other/missing" }, compaction: { model: "other/missing" } } },
		models: { providers: { demo: { auth: "unsupported", apiKey: { source: "exec", id: "ignored" } } } },
		channels: { discord: { token: "test-token" } }
	}`, workspace)
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, err := prepare(options{configPath: path, agentDir: agentDir, stateDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if loaded.agent.Model != "demo/large-model" || loaded.agent.Compaction != "" {
		t.Fatalf("unexpected model selection: %+v", loaded.agent)
	}
}

func TestCheckStateDirRejectsOverlaps(t *testing.T) {
	agentDir := t.TempDir()
	t.Setenv("PSI_CODING_AGENT_DIR", agentDir)
	workspace := t.TempDir()
	sessions := filepath.Join(agentDir, "sessions")
	if err := os.MkdirAll(sessions, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := checkStateDir(filepath.Join(sessions, "psionic"), workspace, agentDir); err == nil {
		t.Fatal("state inside the Psi session root was accepted")
	}
	if err := checkStateDir(filepath.Join(workspace, "state"), workspace, agentDir); err == nil {
		t.Fatal("state inside the workspace was accepted")
	}
	if err := checkStateDir(t.TempDir(), workspace, agentDir); err != nil {
		t.Fatalf("separate state: %v", err)
	}
}

func TestPrepareUsesOnlyThePsionicSessionDirectory(t *testing.T) {
	agentDir := t.TempDir()
	writeTestModels(t, agentDir)
	t.Setenv("PSI_CODING_AGENT_DIR", agentDir)
	psiSessions := filepath.Join(agentDir, "sessions")
	if err := os.MkdirAll(psiSessions, 0o700); err != nil {
		t.Fatal(err)
	}
	sentinel := filepath.Join(psiSessions, "sentinel.jsonl")
	if err := os.WriteFile(sentinel, []byte("sentinel\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	workspace := writeWorkspace(t, map[string]string{"AGENTS.md": "rules"})
	settings, err := decodeSettings([]byte(`{
  agents: { defaults: { model: "demo/large-model" } },
  channels: { discord: { token: "test-token" } },
}`), func(string) (string, bool) { return "", false })
	if err != nil {
		t.Fatal(err)
	}
	models, err := loadTestModels(t, agentDir)
	if err != nil {
		t.Fatal(err)
	}
	state, err := openState(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer state.close()
	rt := newRuntime(settings, selectedAgent{Workspace: workspace, Model: "demo/large-model"}, models, agentDir, state, configDefaults(), nil, log.New(io.Discard, "", 0))
	chat := &conversation{key: "dm:1", rt: rt}
	chat.ctx, chat.cancel = context.WithCancel(context.Background())
	defer chat.cancel()
	if err := chat.prepare(inboundMessage{IsDM: true, ChannelID: "123456789012345678", AuthorID: "223456789012345678"}); err != nil {
		t.Fatalf("prepare: %v", err)
	}
	store := chat.store
	if store == nil {
		t.Fatal("no store")
	}
	if store.Header().ID == "" {
		t.Fatal("missing session ID")
	}
	entries, err := os.ReadDir(psiSessions)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "sentinel.jsonl" {
		t.Fatalf("Psi session directory changed: %v", entries)
	}
	data, err := os.ReadFile(sentinel)
	if err != nil || string(data) != "sentinel\n" {
		t.Fatalf("sentinel changed: %q, %v", data, err)
	}
	chat.retireLocked()
}

func TestCheckRejectsExplicitReplyModeWithoutMessageTool(t *testing.T) {
	agentDir := t.TempDir()
	writeTestModels(t, agentDir)
	t.Setenv("PSI_CODING_AGENT_DIR", agentDir)
	workspace := writeWorkspace(t, map[string]string{"AGENTS.md": "rules"})
	path := filepath.Join(t.TempDir(), "settings.json5")
	body := `{
  agents: { defaults: { workspace: ` + fmt.Sprintf("%q", workspace) + `, model: "demo/large-model", tools: { deny: ["message"] } } },
  channels: { discord: { token: "test-token" } },
  messages: { visibleReplies: "message_tool" },
}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	if err := run([]string{"--config", path, "--state-dir", t.TempDir(), "--check"}, &stdout, &stderr); err == nil {
		t.Fatal("check accepted a reply mode without its required tool")
	}
}

func TestCheckRejectsProfileWithoutMessageToolForExplicitReplies(t *testing.T) {
	agentDir := t.TempDir()
	writeTestModels(t, agentDir)
	t.Setenv("PSI_CODING_AGENT_DIR", agentDir)
	workspace := writeWorkspace(t, map[string]string{"AGENTS.md": "rules"})
	for profile, wantError := range map[string]bool{"coding": true, "messaging": false} {
		path := filepath.Join(t.TempDir(), "settings.json5")
		body := `{
  agents: { defaults: { workspace: ` + fmt.Sprintf("%q", workspace) + `, model: "demo/large-model", tools: { profile: ` + fmt.Sprintf("%q", profile) + ` } } },
  channels: { discord: { token: "test-token" } },
  messages: { visibleReplies: "message_tool" },
}`
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		var stdout, stderr bytes.Buffer
		err := run([]string{"--config", path, "--state-dir", t.TempDir(), "--check"}, &stdout, &stderr)
		if (err != nil) != wantError {
			t.Fatalf("profile %s: err = %v, wantError %v", profile, err, wantError)
		}
	}
}

func TestCheckStateDirRejectsSymlinkedStateDirectory(t *testing.T) {
	target := t.TempDir()
	alias := filepath.Join(t.TempDir(), "state")
	if err := os.Symlink(target, alias); err != nil {
		t.Skip("symlinks unavailable")
	}
	if err := checkStateDir(alias, t.TempDir(), ""); err == nil {
		t.Fatal("symlinked state directory was accepted")
	}
}

func TestCheckStateDirRejectsUnmarkedContents(t *testing.T) {
	workspace := t.TempDir()
	for name, setup := range map[string]func(string) error{
		"empty": func(dir string) error { return nil },
		"marked": func(dir string) error {
			return os.WriteFile(filepath.Join(dir, stateMarker), []byte(stateMarkerContents), 0o600)
		},
		"unmarked": func(dir string) error {
			return os.WriteFile(filepath.Join(dir, "other"), []byte("x"), 0o600)
		},
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			if err := setup(dir); err != nil {
				t.Fatal(err)
			}
			err := checkStateDir(dir, workspace, "")
			if name == "unmarked" && err == nil {
				t.Fatal("unmarked state directory was accepted")
			}
			if name != "unmarked" && err != nil {
				t.Fatalf("state directory rejected: %v", err)
			}
		})
	}
}

func TestCheckStateDirRejectsSymlinkAlias(t *testing.T) {
	agentDir := t.TempDir()
	t.Setenv("PSI_CODING_AGENT_DIR", agentDir)
	workspace := t.TempDir()
	sessions := filepath.Join(agentDir, "sessions")
	if err := os.MkdirAll(sessions, 0o700); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(sessions, alias); err != nil {
		t.Skip("symlinks unavailable")
	}
	if err := checkStateDir(filepath.Join(alias, "psionic"), workspace, agentDir); err == nil {
		t.Fatal("symlinked state inside the Psi session root was accepted")
	}
}

func TestCheckRejectsAChannelThatDisablesExplicitReplies(t *testing.T) {
	agentDir := t.TempDir()
	writeTestModels(t, agentDir)
	t.Setenv("PSI_CODING_AGENT_DIR", agentDir)
	workspace := writeWorkspace(t, map[string]string{"AGENTS.md": "rules"})
	path := filepath.Join(t.TempDir(), "settings.json5")
	body := `{
  agents: { defaults: { workspace: ` + fmt.Sprintf("%q", workspace) + `, model: "demo/large-model" } },
  channels: { discord: { token: "test-token", groupPolicy: "open", guilds: { "323456789012345678": { channels: { "423456789012345678": { tools: { deny: ["message"] } } } } } } },
  messages: { groupChat: { visibleReplies: "message_tool" } },
}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	if err := run([]string{"--config", path, "--state-dir", t.TempDir(), "--check"}, &stdout, &stderr); err == nil {
		t.Fatal("check accepted a channel without the message tool")
	}
}
