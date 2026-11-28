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
			chineseReplyRule,
			chineseCredentialRule,
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
		if !strings.Contains(rendered, chineseReplyRule) {
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
		for _, absent := range []string{"<tools>", "<rules>", "<docs>", chinesePreamble, chineseReplyRule} {
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

	t.Run("web tools", func(t *testing.T) {
		tools := []string{"web_search", "fetch_content", "get_search_content", "read_session"}
		rendered := RenderSections(BuildPromptSections("/workspace", nil, nil, tools, "", false, nil, LanguageChinese))
		for _, want := range []string{
			"web_search: " + chineseToolSnippets["web_search"],
			"fetch_content: " + chineseToolSnippets["fetch_content"],
			"get_search_content: " + chineseToolSnippets["get_search_content"],
			"read_session: " + chineseToolSnippets["read_session"],
			chineseToolGuidelines["web_search"][2],
			chineseToolGuidelines["fetch_content"][3],
			chineseToolGuidelines["get_search_content"][0],
			chineseToolGuidelines["read_session"][0],
		} {
			if !strings.Contains(rendered, want) {
				t.Errorf("Chinese web prompt missing %q:\n%s", want, rendered)
			}
		}
		for _, absent := range []string{
			toolSnippets["web_search"],
			toolSnippets["fetch_content"],
			toolSnippets["get_search_content"],
			toolSnippets["read_session"],
		} {
			if strings.Contains(rendered, absent) {
				t.Errorf("Chinese web prompt kept English text %q:\n%s", absent, rendered)
			}
		}
	})
}

// TestChinesePromptCoversEveryToolAndRule keeps zh from silently falling back
// to English when a new tool or fixed rule lands in the English prompt.
func TestChinesePromptCoversEveryToolAndRule(t *testing.T) {
	for name := range toolSnippets {
		if chineseToolSnippets[name] == "" {
			t.Errorf("tool %q has no Chinese snippet", name)
		}
	}
	for name, rules := range toolGuidelines {
		translated, ok := chineseToolGuidelines[name]
		if !ok {
			t.Errorf("tool %q has no Chinese guidelines", name)
			continue
		}
		if len(translated) != len(rules) {
			t.Errorf("tool %q has %d Chinese guidelines, want %d", name, len(translated), len(rules))
		}
	}
	// The reply rule is Psi-only, so Chinese has one rule more than English.
	if len(chineseFixedRules) != len(fixedRules)+1 {
		t.Errorf("Chinese fixed rules = %d, want %d", len(chineseFixedRules), len(fixedRules)+1)
	}
	for name, rule := range map[string]string{"credential": chineseCredentialRule, "reply": chineseReplyRule} {
		found := false
		for _, candidate := range chineseFixedRules {
			if candidate == rule {
				found = true
			}
		}
		if !found {
			t.Errorf("Chinese fixed rules do not contain the %s rule", name)
		}
	}
}
