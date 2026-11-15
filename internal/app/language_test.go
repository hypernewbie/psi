package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"psi/internal/config"
	"psi/internal/session"
)

func languageTestEnv(t *testing.T) (work, agentDir string) {
	t.Helper()
	home, work := t.TempDir(), t.TempDir()
	agentDir = filepath.Join(home, "agent")
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("PSI_CODING_AGENT_DIR", agentDir)
	t.Setenv("PI_CODING_AGENT_DIR", agentDir)
	t.Setenv("PSI_CODING_AGENT_SESSION_DIR", "")
	t.Setenv("PI_CODING_AGENT_SESSION_DIR", "")
	t.Setenv("ANTHROPIC_API_KEY", "synthetic-language-key")
	writeAppFile(t, filepath.Join(agentDir, "models.json"), `{"providers":{"anthropic":{"baseUrl":"http://127.0.0.1:1/v1","models":[{"id":"m","api":"anthropic-messages","maxTokens":4096,"contextWindow":8192}]}}}`)
	return work, agentDir
}

func assertPromptLanguage(t *testing.T, prompt, language string) {
	t.Helper()
	if language == config.LanguageChinese {
		for _, want := range []string{"你是 psi，一个极简的 Go 编程智能体。", "默认使用简体中文回复，除非用户明确要求使用其他语言。代码、命令、路径和标识符保持原样。"} {
			if !strings.Contains(prompt, want) {
				t.Fatalf("prompt is not Chinese: missing %q:\n%s", want, prompt)
			}
		}
		if strings.Contains(prompt, "You are psi, a minimal Go coding agent.") {
			t.Fatalf("Chinese prompt kept the English preamble:\n%s", prompt)
		}
		return
	}
	if !strings.Contains(prompt, "You are psi, a minimal Go coding agent.") {
		t.Fatalf("prompt is not English:\n%s", prompt)
	}
	if strings.Contains(prompt, "默认使用简体中文回复") {
		t.Fatalf("English prompt kept the Chinese reply rule:\n%s", prompt)
	}
}

func TestParseLanguage(t *testing.T) {
	tests := []struct {
		name     string
		args     []string
		language string
		messages []string
		wantErr  bool
	}{
		{name: "omitted", args: []string{"hello"}, messages: []string{"hello"}},
		{name: "separate value", args: []string{"--language", "zh"}, language: "zh"},
		{name: "equals value", args: []string{"--language=zh"}, language: "zh"},
		{name: "interspersed", args: []string{"hello", "--language", "zh", "world"}, language: "zh", messages: []string{"hello", "world"}},
		{name: "message boundary", args: []string{"--language", "zh", "--", "--language=en"}, language: "zh", messages: []string{"--language=en"}},
		{name: "missing value", args: []string{"--language"}, wantErr: true},
		{name: "empty value", args: []string{"--language="}, wantErr: true},
		{name: "unsupported value", args: []string{"--language", "fr"}, wantErr: true},
		{name: "last valid wins", args: []string{"--language", "en", "--language", "zh"}, language: "zh"},
		{name: "invalid earlier still fails", args: []string{"--language", "fr", "--language", "zh"}, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			options, _, err := Parse(tt.args)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("Parse(%v) accepted", tt.args)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if options.Language != tt.language {
				t.Fatalf("language = %q, want %q", options.Language, tt.language)
			}
			if strings.Join(options.Messages, "|") != strings.Join(tt.messages, "|") {
				t.Fatalf("messages = %v, want %v", options.Messages, tt.messages)
			}
		})
	}
}

func TestLanguageStartupPrecedence(t *testing.T) {
	const base = `"defaultProvider":"anthropic","defaultModel":"m","defaultTools":["read"]`
	tests := []struct {
		name    string
		user    string
		project string
		consent bool
		cli     string
		want    string
		wantErr bool
	}{
		{name: "default", user: `{` + base + `}`, want: config.LanguageEnglish},
		{name: "user", user: `{` + base + `,"language":"zh"}`, want: config.LanguageChinese},
		{name: "project overrides user", user: `{` + base + `,"language":"zh"}`, project: `{"language":"en"}`, consent: true, want: config.LanguageEnglish},
		{name: "user wins when project declined", user: `{` + base + `,"language":"zh"}`, project: `{"language":"en"}`, consent: false, want: config.LanguageChinese},
		{name: "cli overrides user and project", user: `{` + base + `,"language":"zh"}`, project: `{"language":"zh"}`, consent: true, cli: "en", want: config.LanguageEnglish},
		{name: "cli selects Chinese", user: `{` + base + `,"language":"en"}`, cli: "zh", want: config.LanguageChinese},
		{name: "invalid user fails despite cli", user: `{` + base + `,"language":"fr"}`, cli: "en", wantErr: true},
		{name: "invalid consented project fails despite cli", user: `{` + base + `,"language":"en"}`, project: `{"language":"fr"}`, consent: true, cli: "zh", wantErr: true},
		{name: "declined invalid project stays unread", user: `{` + base + `,"language":"en"}`, project: `{"language":"fr"}`, consent: false, cli: "zh", want: config.LanguageChinese},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			work, agentDir := languageTestEnv(t)
			userPath := filepath.Join(agentDir, "settings.json")
			projectPath := filepath.Join(work, ".pi", "settings.json")
			writeAppFile(t, userPath, tt.user)
			if tt.project != "" {
				writeAppFile(t, projectPath, tt.project)
			}
			loaded, err := config.LoadSettings(userPath, projectPath, tt.consent)
			if err != nil {
				if tt.wantErr {
					return
				}
				t.Fatal(err)
			}
			if tt.wantErr {
				t.Fatal("invalid settings loaded")
			}
			st := loaded.Settings
			prompt, sections, store, err := activateForLanguageTest(t, Options{Language: tt.cli}, work, agentDir, &st, tt.consent)
			if err != nil {
				t.Fatal(err)
			}
			store.Unlock()
			if len(sections) == 0 {
				t.Fatal("activation returned no prompt sections")
			}
			assertPromptLanguage(t, prompt, tt.want)
		})
	}
}

func activateForLanguageTest(t *testing.T, options Options, work, agentDir string, st *config.Settings, consent bool) (string, []config.PromptSection, *session.Store, error) {
	t.Helper()
	_, _, _, prompt, sections, _, store, err := activateSessionWithStore(options, work, agentDir, st, consent, nil, nil, nil)
	return prompt, sections, store, err
}

func TestLanguageResourceBoundaries(t *testing.T) {
	t.Run("declined project resources stay out", func(t *testing.T) {
		work, agentDir := languageTestEnv(t)
		ancestor := t.TempDir()
		work = filepath.Join(ancestor, "workspace")
		if err := os.MkdirAll(work, 0o700); err != nil {
			t.Fatal(err)
		}
		writeAppFile(t, filepath.Join(ancestor, "AGENTS.md"), "ANCESTOR_CONTEXT")
		writeAppFile(t, filepath.Join(agentDir, "settings.json"), `{"language":"en","defaultTools":["read"]}`)
		writeAppFile(t, filepath.Join(work, "AGENTS.md"), "ORDINARY_CONTEXT")
		writeAppFile(t, filepath.Join(work, ".pi", "settings.json"), `{"language":"zh"}`)
		writeAppFile(t, filepath.Join(work, ".pi", "SYSTEM.md"), "PROTECTED_SYSTEM")
		writeAppFile(t, filepath.Join(work, ".pi", "skills", "protected", "SKILL.md"), "---\nname: protected\ndescription: PROTECTED_SKILL\n---\nbody\n")
		userPath := filepath.Join(agentDir, "settings.json")
		loaded, err := config.LoadSettings(userPath, filepath.Join(work, ".pi", "settings.json"), false)
		if err != nil {
			t.Fatal(err)
		}
		prepared, err := preparePromptResources(Options{}, work, agentDir, loaded.Settings, false)
		if err != nil {
			t.Fatal(err)
		}
		assertPromptLanguage(t, prepared.prompt, config.LanguageEnglish)
		for _, want := range []string{"ORDINARY_CONTEXT", "ANCESTOR_CONTEXT"} {
			if !strings.Contains(prepared.prompt, want) {
				t.Fatalf("ordinary context file %q was suppressed:\n%s", want, prepared.prompt)
			}
		}
		for _, absent := range []string{"PROTECTED_SYSTEM", "PROTECTED_SKILL"} {
			if strings.Contains(prepared.prompt, absent) {
				t.Fatalf("declined protected resource %q leaked:\n%s", absent, prepared.prompt)
			}
		}
		if len(prepared.skills) != 0 {
			t.Fatalf("declined project skills loaded: %v", prepared.skills)
		}
	})

	t.Run("consented project resources load", func(t *testing.T) {
		work, agentDir := languageTestEnv(t)
		writeAppFile(t, filepath.Join(agentDir, "settings.json"), `{"language":"en","defaultTools":["read"]}`)
		writeAppFile(t, filepath.Join(work, "AGENTS.md"), "ORDINARY_CONTEXT")
		writeAppFile(t, filepath.Join(work, ".pi", "settings.json"), `{"language":"zh"}`)
		writeAppFile(t, filepath.Join(work, ".pi", "SYSTEM.md"), "PROTECTED_SYSTEM")
		writeAppFile(t, filepath.Join(work, ".pi", "skills", "protected", "SKILL.md"), "---\nname: protected\ndescription: PROTECTED_SKILL\n---\nbody\n")
		userPath := filepath.Join(agentDir, "settings.json")
		userSettings, err := config.LoadSettings(userPath, "", false)
		if err != nil {
			t.Fatal(err)
		}
		loaded, err := config.LoadProjectSettings(userSettings, filepath.Join(work, ".pi", "settings.json"))
		if err != nil {
			t.Fatal(err)
		}
		prepared, err := preparePromptResources(Options{}, work, agentDir, loaded.Settings, true)
		if err != nil {
			t.Fatal(err)
		}
		for _, want := range []string{"项目专用指令与指南：", "以下技能为特定任务提供专用指令。", "PROTECTED_SYSTEM", "PROTECTED_SKILL", "ORDINARY_CONTEXT"} {
			if !strings.Contains(prepared.prompt, want) {
				t.Fatalf("consented prompt missing %q:\n%s", want, prepared.prompt)
			}
		}
		if strings.Contains(prepared.prompt, "<tools>") || strings.Contains(prepared.prompt, "默认使用简体中文回复") {
			t.Fatalf("project SYSTEM.md did not suppress built-in tools/rules:\n%s", prepared.prompt)
		}
	})

	t.Run("disabled resources", func(t *testing.T) {
		work, agentDir := languageTestEnv(t)
		writeAppFile(t, filepath.Join(agentDir, "settings.json"), `{"language":"zh","defaultTools":["read"]}`)
		writeAppFile(t, filepath.Join(work, "AGENTS.md"), "ORDINARY_CONTEXT")
		writeAppFile(t, filepath.Join(agentDir, "skills", "local", "SKILL.md"), "---\nname: local\ndescription: LOCAL_SKILL\n---\nbody\n")
		userPath := filepath.Join(agentDir, "settings.json")
		loaded, err := config.LoadSettings(userPath, "", false)
		if err != nil {
			t.Fatal(err)
		}
		prepared, err := preparePromptResources(Options{NoSkills: true, NoContextFiles: true}, work, agentDir, loaded.Settings, false)
		if err != nil {
			t.Fatal(err)
		}
		assertPromptLanguage(t, prepared.prompt, config.LanguageChinese)
		for _, absent := range []string{"ORDINARY_CONTEXT", "LOCAL_SKILL", "<skills>"} {
			if strings.Contains(prepared.prompt, absent) {
				t.Fatalf("disabled resource %q leaked:\n%s", absent, prepared.prompt)
			}
		}
	})

	t.Run("custom and append prompts keep their contract", func(t *testing.T) {
		work, agentDir := languageTestEnv(t)
		writeAppFile(t, filepath.Join(agentDir, "settings.json"), `{"language":"zh","defaultTools":["read"]}`)
		writeAppFile(t, filepath.Join(work, "AGENTS.md"), "ORDINARY_CONTEXT")
		userPath := filepath.Join(agentDir, "settings.json")
		loaded, err := config.LoadSettings(userPath, "", false)
		if err != nil {
			t.Fatal(err)
		}
		prepared, err := preparePromptResources(Options{SystemPrompt: "CUSTOM_BASE", AppendSystemPrompts: []string{"APPENDED_TEXT"}}, work, agentDir, loaded.Settings, false)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(prepared.prompt, "CUSTOM_BASE\n\n") {
			t.Fatalf("custom base is not the preamble:\n%s", prepared.prompt)
		}
		for _, absent := range []string{"<tools>", "<rules>", "<docs>", "默认使用简体中文回复"} {
			if strings.Contains(prepared.prompt, absent) {
				t.Fatalf("custom prompt kept %q:\n%s", absent, prepared.prompt)
			}
		}
		for _, want := range []string{"APPENDED_TEXT", "<project_context>\n项目专用指令与指南：", "ORDINARY_CONTEXT"} {
			if !strings.Contains(prepared.prompt, want) {
				t.Fatalf("custom prompt missing %q:\n%s", want, prepared.prompt)
			}
		}
	})

	t.Run("cli tools reach the prompt", func(t *testing.T) {
		work, agentDir := languageTestEnv(t)
		writeAppFile(t, filepath.Join(agentDir, "settings.json"), `{"language":"zh","defaultProvider":"anthropic","defaultModel":"m","defaultTools":["read","bash"]}`)
		userPath := filepath.Join(agentDir, "settings.json")
		loaded, err := config.LoadSettings(userPath, "", false)
		if err != nil {
			t.Fatal(err)
		}
		st := loaded.Settings
		prompt, _, store, err := activateForLanguageTest(t, Options{Tools: "read"}, work, agentDir, &st, false)
		if err != nil {
			t.Fatal(err)
		}
		store.Unlock()
		assertPromptLanguage(t, prompt, config.LanguageChinese)
		if !strings.Contains(prompt, "- read: 读取文件内容") || strings.Contains(prompt, "- bash:") {
			t.Fatalf("cli tool selection not applied to the prompt:\n%s", prompt)
		}
	})
}

func TestRPCLanguageStartup(t *testing.T) {
	work, agentDir := languageTestEnv(t)
	settingsPath := filepath.Join(agentDir, "settings.json")
	writeAppFile(t, settingsPath, `{"defaultProvider":"anthropic","defaultModel":"m","defaultTools":["read"],"language":"zh"}`)
	writeAppFile(t, filepath.Join(agentDir, "auth.json"), `{"anthropic":{"type":"api_key","key":"synthetic-rpc-language-key"}}`)
	loaded, err := config.LoadSettings(settingsPath, "", false)
	if err != nil {
		t.Fatal(err)
	}
	st := loaded.Settings
	_, _, _, prompt, sections, _, store, err := activateSessionWithStore(Options{}, work, agentDir, &st, false, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.TryLock(); err != nil {
		t.Fatal(err)
	}
	models, err := config.LoadModels(filepath.Join(agentDir, "models.json"))
	if err != nil {
		store.Unlock()
		t.Fatal(err)
	}
	r := &runtime{store: store, options: Options{}, settings: st, models: models, prompt: prompt, sections: sections, agentDir: agentDir}
	if err := r.restore(); err != nil {
		store.Unlock()
		t.Fatal(err)
	}
	defer func() {
		if r.prov != nil {
			r.prov.CloseIdle()
		}
		store.Unlock()
	}()
	assertPromptLanguage(t, r.cfg.System, config.LanguageChinese)
	if strings.Join(sectionNames(r.cfg.Sections), " ") != strings.Join(sectionNames(sections), " ") {
		t.Fatalf("runtime sections = %v, want %v", sectionNames(r.cfg.Sections), sectionNames(sections))
	}
	for _, section := range r.cfg.Sections {
		if !strings.Contains(r.cfg.System, section.Body) {
			t.Fatalf("runtime system missing section %q:\n%s", section.Name, r.cfg.System)
		}
	}
}

func sectionNames(sections []config.PromptSection) []string {
	names := make([]string, 0, len(sections))
	for _, section := range sections {
		names = append(names, section.Name)
	}
	return names
}
