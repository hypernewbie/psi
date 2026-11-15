package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLanguageSettings(t *testing.T) {
	valid := []struct {
		name        string
		user        string
		project     string
		consent     bool
		want        string
		wantSource  bool
		projectWins bool
	}{
		{name: "missing key", user: `{}`, want: LanguageEnglish},
		{name: "user en", user: `{"language":"en"}`, want: LanguageEnglish, wantSource: true},
		{name: "user zh", user: `{"language":"zh"}`, want: LanguageChinese, wantSource: true},
		{name: "project overrides user", user: `{"language":"zh"}`, project: `{"language":"en"}`, consent: true, want: LanguageEnglish, wantSource: true, projectWins: true},
		{name: "declined project is ignored", user: `{"language":"zh"}`, project: `{"language":"en"}`, consent: false, want: LanguageChinese, wantSource: true},
	}
	for _, tt := range valid {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			userPath := filepath.Join(dir, "user", "settings.json")
			projectPath := filepath.Join(dir, "project", ".pi", "settings.json")
			if err := os.MkdirAll(filepath.Dir(userPath), 0o700); err != nil {
				t.Fatal(err)
			}
			writeSettingsFileForTest(t, userPath, tt.user, 0o600)
			if tt.project != "" {
				if err := os.MkdirAll(filepath.Dir(projectPath), 0o700); err != nil {
					t.Fatal(err)
				}
				writeSettingsFileForTest(t, projectPath, tt.project, 0o600)
			}
			loaded, err := LoadSettings(userPath, projectPath, tt.consent)
			if err != nil {
				t.Fatal(err)
			}
			if loaded.Settings.Language != tt.want {
				t.Fatalf("language = %q, want %q", loaded.Settings.Language, tt.want)
			}
			source, ok := loaded.Sources["language"]
			if ok != tt.wantSource {
				t.Fatalf("source present = %v, want %v (%q)", ok, tt.wantSource, source)
			}
			if tt.wantSource {
				want := "user:" + userPath
				if tt.projectWins {
					want = "project:" + projectPath
				}
				if source != want {
					t.Fatalf("source = %q, want %q", source, want)
				}
			}
		})
	}

	invalid := []struct {
		name  string
		value string
	}{
		{name: "empty", value: `""`},
		{name: "whitespace", value: `" "`},
		{name: "uppercase", value: `"ZH"`},
		{name: "regional", value: `"zh-CN"`},
		{name: "unknown", value: `"fr"`},
		{name: "null", value: `null`},
		{name: "number", value: `1`},
		{name: "boolean", value: `true`},
		{name: "array", value: `[]`},
		{name: "object", value: `{}`},
	}
	for _, tt := range invalid {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "settings.json")
			writeSettingsFileForTest(t, path, `{"language":`+tt.value+`}`, 0o600)
			if _, err := LoadSettings(path, "", false); err == nil {
				t.Fatalf("language %s accepted", tt.value)
			}
		})
	}

	t.Run("malformed json", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "settings.json")
		writeSettingsFileForTest(t, path, `{"language":"zh"`, 0o600)
		if _, err := LoadSettings(path, "", false); err == nil {
			t.Fatal("malformed settings accepted")
		}
	})

	t.Run("valid project does not hide malformed user file", func(t *testing.T) {
		dir := t.TempDir()
		userPath := filepath.Join(dir, "user.json")
		projectPath := filepath.Join(dir, "project.json")
		writeSettingsFileForTest(t, userPath, `{"language":`, 0o600)
		writeSettingsFileForTest(t, projectPath, `{"language":"en"}`, 0o600)
		if _, err := LoadSettings(userPath, projectPath, true); err == nil {
			t.Fatal("malformed user settings were hidden by a valid project file")
		}
	})

	t.Run("valid project does not hide unsupported user language", func(t *testing.T) {
		dir := t.TempDir()
		userPath := filepath.Join(dir, "user.json")
		projectPath := filepath.Join(dir, "project.json")
		writeSettingsFileForTest(t, userPath, `{"language":"zh-CN"}`, 0o600)
		writeSettingsFileForTest(t, projectPath, `{"language":"en"}`, 0o600)
		if _, err := LoadSettings(userPath, projectPath, true); err == nil {
			t.Fatal("unsupported user language was hidden by a valid project file")
		}
	})
}

func TestValidLanguage(t *testing.T) {
	for _, language := range []string{LanguageEnglish, LanguageChinese} {
		if !ValidLanguage(language) {
			t.Fatalf("%q rejected", language)
		}
	}
	for _, language := range []string{"", "ZH", "zh-CN", "fr", " en", "zh "} {
		if ValidLanguage(language) {
			t.Fatalf("%q accepted", language)
		}
	}
}

func TestBuildPromptSectionsChinese(t *testing.T) {
	skillsDesc := "<available_skills>\n<skill>demo</skill>\n</available_skills>"

	t.Run("default sections", func(t *testing.T) {
		instructions := []InstructionFile{{Path: "/workspace/AGENTS.md", Kind: "agents", Content: "project context"}}
		sections := BuildPromptSections("/workspace", instructions, []string{skillsDesc}, []string{"read", "bash", "edit", "write"}, "", false, []string{"extra rules"}, LanguageChinese)
		names := make([]string, 0, len(sections))
		for _, section := range sections {
			names = append(names, section.Name)
		}
		if got, want := strings.Join(names, " "), "preamble tools rules docs addendum project_context skills cwd"; got != want {
			t.Fatalf("section order = %q, want %q", got, want)
		}
		rendered := RenderSections(sections)
		for _, want := range []string{
			chinesePreamble,
			"<tools>\n- read: 读取文件内容\n- bash: 执行 shell 命令\n- edit: 使用精确文本替换进行文件编辑，支持在一次调用中修改多个不相交的位置\n- write: 创建或覆盖文件\n</tools>",
			chineseBashRule,
			"使用 read 查看文件，不要使用 cat 或 sed。",
			"默认使用简体中文回复，除非用户明确要求使用其他语言。代码、命令、路径和标识符保持原样。",
			"<docs>\n" + chineseDocsIntro + "\n- 运行 `psi --docs` 列出主题。\n- 运行 `psi --docs <topic>` 输出指定主题。\n- 主题：",
			"<addendum>\nextra rules\n</addendum>",
			"<project_context>\n" + chineseProjectContextIntro + "\n\n" + `<project_instructions path="/workspace/AGENTS.md">` + "\nproject context\n</project_instructions>",
			"<skills>\n" + skillsDesc + "\n</skills>",
			"<cwd>\n/workspace\n</cwd>",
		} {
			if !strings.Contains(rendered, want) {
				t.Errorf("prompt missing %q:\n%s", want, rendered)
			}
		}
		for _, absent := range []string{defaultPreamble, "Read file contents", "Use bash for file operations", "Be concise in your responses", "Project-specific instructions and guidelines:"} {
			if strings.Contains(rendered, absent) {
				t.Errorf("Chinese prompt kept English text %q:\n%s", absent, rendered)
			}
		}
	})

	t.Run("filtered tools", func(t *testing.T) {
		sections := BuildPromptSections("/workspace", nil, nil, []string{"read"}, "", false, nil, LanguageChinese)
		rendered := RenderSections(sections)
		if strings.Contains(rendered, "bash") || strings.Contains(rendered, "edit") || strings.Contains(rendered, "write") {
			t.Fatalf("disabled tool text leaked:\n%s", rendered)
		}
		if !strings.Contains(rendered, chineseFixedRules[2]) {
			t.Fatalf("reply rule missing with filtered tools:\n%s", rendered)
		}
	})

	t.Run("no tools keeps Chinese fixed rules", func(t *testing.T) {
		rendered := RenderSections(BuildPromptSections("/workspace", nil, nil, nil, "", false, nil, LanguageChinese))
		for _, want := range chineseFixedRules {
			if !strings.Contains(rendered, want) {
				t.Fatalf("fixed rule %q missing:\n%s", want, rendered)
			}
		}
		if strings.Contains(rendered, "<tools>") || strings.Contains(rendered, chineseBashRule) {
			t.Fatalf("tool sections rendered without tools:\n%s", rendered)
		}
	})

	t.Run("custom base suppresses built-in sections", func(t *testing.T) {
		instructions := []InstructionFile{{Path: "/workspace/AGENTS.md", Kind: "agents", Content: "project context"}}
		sections := BuildPromptSections("/workspace", instructions, []string{skillsDesc}, []string{"read", "bash"}, "CUSTOM_BASE", true, []string{"extra rules"}, LanguageChinese)
		rendered := RenderSections(sections)
		if !strings.HasPrefix(rendered, "CUSTOM_BASE\n\n") {
			t.Fatalf("custom base is not the preamble:\n%s", rendered)
		}
		for _, absent := range []string{"<tools>", "<rules>", "<docs>", chinesePreamble, chineseFixedRules[2]} {
			if strings.Contains(rendered, absent) {
				t.Fatalf("custom base kept %q:\n%s", absent, rendered)
			}
		}
		for _, want := range []string{"<project_context>\n" + chineseProjectContextIntro, skillsDesc, "<cwd>\n/workspace\n</cwd>"} {
			if !strings.Contains(rendered, want) {
				t.Fatalf("custom base dropped %q:\n%s", want, rendered)
			}
		}
	})
}
