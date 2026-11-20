package app

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hypernewbie/psi/internal/config"
	"github.com/hypernewbie/psi/internal/provider"
	"github.com/hypernewbie/psi/internal/session"
	"github.com/hypernewbie/psi/internal/terminal"
)

// newThinkingRuntime builds a runtime for thinking-default tests. The seed runs
// before restore so a stored session level participates in startup.
func newThinkingRuntime(t *testing.T, options Options, settings config.Settings, seed func(*session.Store), models ...config.ModelMeta) *runtime {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-synthetic-thinking-test")
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "")
	cwd := t.TempDir()
	s, err := session.New(session.OpenOptions{Cwd: cwd, SessionDir: t.TempDir(), Persist: true})
	if err != nil {
		t.Fatal(err)
	}
	r := &runtime{
		store:    s,
		agentDir: t.TempDir(),
		settings: settings,
		options:  options,
		models:   &config.ModelsFile{Providers: map[string]config.ProviderMeta{"anthropic": {Models: models}}},
		prompt:   "workspace instructions",
	}
	if err := s.TryLock(); err != nil {
		t.Fatal(err)
	}
	if seed != nil {
		seed(s)
	}
	if err := r.restore(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		s.Unlock()
		if r.prov != nil {
			r.prov.CloseIdle()
		}
	})
	return r
}

func thinkingModel(id string, maxTokens int) config.ModelMeta {
	return config.ModelMeta{ID: id, Reasoning: true, MaxTokens: maxTokens, ContextWindow: 200000}
}

func thinkingSettings() config.Settings {
	return config.Settings{
		DefaultProvider: "anthropic",
		DefaultModel:    "m",
		DefaultThinking: "off",
		DefaultTools:    []string{"read"},
	}
}

func TestStartupAppliesPerModelThinkingDefault(t *testing.T) {
	settings := thinkingSettings()
	settings.DefaultThinking = "low"
	settings.DefaultThinkingSet = true
	settings.ModelThinkingLevels = map[string]string{"anthropic/m": "high"}
	r := newThinkingRuntime(t, Options{}, settings, nil, thinkingModel("m", 64000))

	if r.ov.Thinking != "high" || r.cfg.Thinking != "high" {
		t.Fatalf("startup thinking = %q/%q, want high", r.ov.Thinking, r.cfg.Thinking)
	}
}

func TestStartupKeepsExplicitThinkingOverPerModelDefault(t *testing.T) {
	settings := thinkingSettings()
	settings.DefaultThinking = "low"
	settings.DefaultThinkingSet = true
	settings.ModelThinkingLevels = map[string]string{"anthropic/m": "high"}
	r := newThinkingRuntime(t, Options{Thinking: "off"}, settings, nil, thinkingModel("m", 64000))

	if r.ov.Thinking != "off" || r.cfg.Thinking != "off" {
		t.Fatalf("explicit startup thinking = %q/%q, want off", r.ov.Thinking, r.cfg.Thinking)
	}
}

func TestStartupKeepsRestoredThinkingOverPerModelDefault(t *testing.T) {
	settings := thinkingSettings()
	settings.DefaultThinking = "low"
	settings.DefaultThinkingSet = true
	settings.ModelThinkingLevels = map[string]string{"anthropic/m": "high"}
	r := newThinkingRuntime(t, Options{}, settings, func(s *session.Store) {
		if _, err := s.AppendThinkingLevelChange("minimal"); err != nil {
			t.Fatal(err)
		}
	}, thinkingModel("m", 64000))

	if r.ov.Thinking != "minimal" || r.cfg.Thinking != "minimal" {
		t.Fatalf("restored thinking = %q/%q, want minimal", r.ov.Thinking, r.cfg.Thinking)
	}
}

func TestModelSwitchAppliesPerModelThinkingDefault(t *testing.T) {
	settings := thinkingSettings()
	settings.ModelThinkingLevels = map[string]string{"anthropic/m2": "high"}
	r := newThinkingRuntime(t, Options{}, settings, nil, thinkingModel("m", 64000), thinkingModel("m2", 64000))

	if _, err := r.command(&terminal.Command{Name: "model", Arg: "anthropic/m2"}, io.Discard); err != nil {
		t.Fatal(err)
	}
	if r.ov.Model != "m2" || r.ov.Thinking != "high" {
		t.Fatalf("switch selected %s at %q, want m2 at high", r.ov.Model, r.ov.Thinking)
	}
}

func TestModelSwitchFallsBackToGlobalThinkingDefault(t *testing.T) {
	settings := thinkingSettings()
	settings.DefaultThinking = "medium"
	settings.DefaultThinkingSet = true
	r := newThinkingRuntime(t, Options{}, settings, nil, thinkingModel("m", 64000), thinkingModel("m2", 64000))

	if _, err := r.command(&terminal.Command{Name: "model", Arg: "anthropic/m2"}, io.Discard); err != nil {
		t.Fatal(err)
	}
	if r.ov.Thinking != "medium" {
		t.Fatalf("switch thinking = %q, want medium", r.ov.Thinking)
	}
}

func TestModelSwitchKeepsCarriedThinkingLevelWithoutDefaults(t *testing.T) {
	settings := thinkingSettings()
	settings.DefaultThinking = "high"
	settings.DefaultThinkingSet = false
	r := newThinkingRuntime(t, Options{Thinking: "low"}, settings, nil, thinkingModel("m", 64000), thinkingModel("m2", 64000))

	if _, err := r.command(&terminal.Command{Name: "model", Arg: "anthropic/m2"}, io.Discard); err != nil {
		t.Fatal(err)
	}
	if r.ov.Thinking != "low" {
		t.Fatalf("switch thinking = %q, want carried low", r.ov.Thinking)
	}
}

func TestThinkingCommandIgnoresPerModelDefault(t *testing.T) {
	settings := thinkingSettings()
	settings.ModelThinkingLevels = map[string]string{"anthropic/m": "high"}
	r := newThinkingRuntime(t, Options{}, settings, nil, thinkingModel("m", 64000))

	if _, err := r.command(&terminal.Command{Name: "thinking", Arg: "low"}, io.Discard); err != nil {
		t.Fatal(err)
	}
	if r.ov.Thinking != "low" || r.cfg.Thinking != "low" {
		t.Fatalf("explicit thinking = %q/%q, want low", r.ov.Thinking, r.cfg.Thinking)
	}
}

func TestModelSwitchAfterExplicitStartupAppliesThinkingDefault(t *testing.T) {
	settings := thinkingSettings()
	settings.ModelThinkingLevels = map[string]string{"anthropic/m2": "high"}
	r := newThinkingRuntime(t, Options{Thinking: "off"}, settings, nil, thinkingModel("m", 64000), thinkingModel("m2", 64000))

	if _, err := r.command(&terminal.Command{Name: "model", Arg: "anthropic/m2"}, io.Discard); err != nil {
		t.Fatal(err)
	}
	if r.ov.Thinking != "high" {
		t.Fatalf("switch thinking = %q, want per-model high", r.ov.Thinking)
	}
}

func TestStartupClampsUnsupportedStoredThinkingLevel(t *testing.T) {
	settings := thinkingSettings()
	r := newThinkingRuntime(t, Options{}, settings, func(s *session.Store) {
		if _, err := s.AppendThinkingLevelChange("max"); err != nil {
			t.Fatal(err)
		}
	}, thinkingModel("m", 16384))

	levels := provider.SupportedThinking(r.cfg.Model)
	for _, level := range levels {
		if level == "max" {
			t.Fatalf("fixture advertises max: %v", levels)
		}
	}
	want := provider.ClampThinkingLevel(r.cfg.Model, "max")
	if r.ov.Thinking != want || r.ov.Thinking == "max" {
		t.Fatalf("stored max resolved to %q, want clamped %q", r.ov.Thinking, want)
	}
}

func TestStartupResolvesCanonicalThinkingKey(t *testing.T) {
	settings := thinkingSettings()
	settings.DefaultModel = ""
	settings.ModelThinkingLevels = map[string]string{"anthropic/claude-sonnet-4-5": "high"}
	r := newThinkingRuntime(t, Options{}, settings, nil, thinkingModel("claude-sonnet-4-5", 64000))

	if r.ov.Model != "claude-sonnet-4-5" {
		t.Fatalf("startup model = %q, want the provider default", r.ov.Model)
	}
	if r.ov.Thinking != "high" {
		t.Fatalf("canonical override resolved to %q, want high", r.ov.Thinking)
	}
}

type thinkingStateResponse struct {
	ID      *string `json:"id"`
	Type    string  `json:"type"`
	Command string  `json:"command"`
	Success bool    `json:"success"`
	Error   string  `json:"error"`
	Data    struct {
		Model struct {
			Provider string `json:"provider"`
			ID       string `json:"id"`
		} `json:"model"`
		ThinkingLevel string `json:"thinkingLevel"`
	} `json:"data"`
}

// runThinkingDefaultsProcess drives the real Run entrypoint in RPC mode with
// disk settings, then returns the states before and after a model switch.
func runThinkingDefaultsProcess(t *testing.T, extraArgs ...string) (thinkingStateResponse, thinkingStateResponse, string) {
	t.Helper()
	home, ws := t.TempDir(), t.TempDir()
	agentDir := filepath.Join(home, ".pi", "agent")
	writeAppFile(t, filepath.Join(agentDir, "settings.json"),
		`{"defaultProvider":"anthropic","defaultModel":"m1","defaultThinkingLevel":"low",`+
			`"modelThinkingLevels":{"anthropic/m1":"high","anthropic/m2":"medium"},`+
			`"compaction":{"enabled":false}}`)
	writeAppFile(t, filepath.Join(agentDir, "models.json"),
		`{"providers":{"anthropic":{"baseUrl":"http://127.0.0.1:9/v1","models":[`+
			`{"id":"m1","reasoning":true,"maxTokens":64000,"contextWindow":200000},`+
			`{"id":"m2","reasoning":true,"maxTokens":64000,"contextWindow":200000}]}}}`)
	writeAppFile(t, filepath.Join(agentDir, "auth.json"), `{"anthropic":{"type":"api_key","key":"synthetic-key"}}`)

	args := append([]string{"--mode", "rpc", "--deny-project", "--no-session"}, extraArgs...)
	encoded, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(t.Context(), exe, "-test.run=^TestAppProcess$")
	cmd.Dir = ws
	cmd.Env = append(appTestEnv(home, agentDir), "PSI_APP_TEST_ARGS="+string(encoded))
	cmd.Stdin = strings.NewReader(`{"id":"before","type":"get_state"}
{"id":"switch","type":"set_model","provider":"anthropic","modelId":"m2"}
{"id":"after","type":"get_state"}
`)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("child failed: %v\nstderr: %s\nstdout: %s", err, stderr.String(), stdout.String())
	}

	states := map[string]thinkingStateResponse{}
	for _, line := range strings.Split(strings.TrimSpace(stdout.String()), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var response thinkingStateResponse
		if err := json.Unmarshal([]byte(line), &response); err != nil {
			t.Fatalf("decode response %q: %v", line, err)
		}
		if response.ID == nil {
			continue
		}
		if !response.Success {
			t.Fatalf("command %q failed: %s", *response.ID, response.Error)
		}
		states[*response.ID] = response
	}
	before, ok := states["before"]
	if !ok {
		t.Fatalf("missing before response; stdout: %s\nstderr: %s", stdout.String(), stderr.String())
	}
	if _, ok := states["switch"]; !ok {
		t.Fatalf("missing switch response; stdout: %s\nstderr: %s", stdout.String(), stderr.String())
	}
	after, ok := states["after"]
	if !ok {
		t.Fatalf("missing after response; stdout: %s\nstderr: %s", stdout.String(), stderr.String())
	}
	return before, after, stderr.String()
}

func TestActualAppThinkingDefaultsRPC(t *testing.T) {
	before, after, _ := runThinkingDefaultsProcess(t)
	if before.Data.Model.Provider != "anthropic" || before.Data.Model.ID != "m1" {
		t.Fatalf("initial model = %+v, want anthropic/m1", before.Data.Model)
	}
	if before.Data.ThinkingLevel != "high" {
		t.Fatalf("initial thinking = %q, want per-model high", before.Data.ThinkingLevel)
	}
	if after.Data.Model.Provider != "anthropic" || after.Data.Model.ID != "m2" {
		t.Fatalf("switched model = %+v, want anthropic/m2", after.Data.Model)
	}
	if after.Data.ThinkingLevel != "medium" {
		t.Fatalf("switched thinking = %q, want per-model medium", after.Data.ThinkingLevel)
	}

	explicitBefore, explicitAfter, _ := runThinkingDefaultsProcess(t, "--thinking", "off")
	if explicitBefore.Data.ThinkingLevel != "off" {
		t.Fatalf("explicit startup thinking = %q, want off", explicitBefore.Data.ThinkingLevel)
	}
	if explicitAfter.Data.Model.ID != "m2" || explicitAfter.Data.ThinkingLevel != "medium" {
		t.Fatalf("explicit startup switch = %+v at %q, want m2 at medium", explicitAfter.Data.Model, explicitAfter.Data.ThinkingLevel)
	}
}
