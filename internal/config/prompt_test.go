package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBuildPromptSectionsBoundaries(t *testing.T) {
	tests := []struct {
		name         string
		cwd          string
		instructions []InstructionFile
		skills       []string
		tools        []string
		base         string
		custom       bool
		appends      []string
		wantNames    []string
		absent       []string
	}{
		{
			name:      "read only",
			cwd:       "/workspace",
			tools:     []string{"read"},
			wantNames: []string{"preamble", "tools", "rules", "docs", "cwd"},
			absent:    []string{"- bash: Execute shell commands", "Use bash for file operations"},
		},
		{
			name:      "no instructions no skills",
			cwd:       "/workspace",
			tools:     []string{"read", "bash"},
			wantNames: []string{"preamble", "tools", "rules", "docs", "cwd"},
			absent:    []string{"<project_context>", "<skills>"},
		},
		{
			name: "custom base suppresses tools and rules",
			cwd:  "/workspace",
			instructions: []InstructionFile{
				{Path: "/workspace/SYSTEM.md", Kind: "system", Content: "CUSTOM_BASE"},
				{Path: "/workspace/AGENTS.md", Kind: "agents", Content: "project context"},
			},
			tools:     []string{"read", "bash"},
			wantNames: []string{"preamble", "project_context", "cwd"},
			absent:    []string{"<tools>", "<rules>", "You are psi"},
		},
		{
			name:      "cli base suppresses tools and rules",
			cwd:       "/workspace",
			tools:     []string{"read"},
			base:      "CLI base",
			custom:    true,
			wantNames: []string{"preamble", "cwd"},
			absent:    []string{"<tools>", "<rules>", "You are psi"},
		},
		{
			name:      "addendum sits after docs",
			cwd:       "/workspace",
			tools:     []string{"read"},
			appends:   []string{"extra rules"},
			wantNames: []string{"preamble", "tools", "rules", "docs", "addendum", "cwd"},
		},
		{
			name:      "trailing slash cwd is kept verbatim",
			cwd:       "/workspace/",
			tools:     []string{"read"},
			wantNames: []string{"preamble", "tools", "rules", "docs", "cwd"},
			absent:    []string{},
		},
		{
			name:      "windows cwd uses forward slashes",
			cwd:       `C:\work\dir`,
			tools:     []string{"read"},
			wantNames: []string{"preamble", "tools", "rules", "docs", "cwd"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sections := BuildPromptSections(tt.cwd, tt.instructions, tt.skills, tt.tools, tt.base, tt.custom, tt.appends)
			got := make([]string, 0, len(sections))
			for _, s := range sections {
				got = append(got, s.Name)
			}
			if strings.Join(got, " ") != strings.Join(tt.wantNames, " ") {
				t.Fatalf("section order = %v, want %v", got, tt.wantNames)
			}
			rendered := RenderSections(sections)
			for _, want := range tt.absent {
				if strings.Contains(rendered, want) {
					t.Errorf("prompt contains %q: %s", want, rendered)
				}
			}
			if tt.name == "windows cwd uses forward slashes" {
				if !strings.Contains(rendered, "<cwd>\nC:/work/dir\n</cwd>") || strings.Contains(rendered, `\`) {
					t.Errorf("cwd section not normalized: %s", rendered)
				}
			}
			if tt.name == "trailing slash cwd is kept verbatim" && !strings.Contains(rendered, "<cwd>\n/workspace/\n</cwd>") {
				t.Errorf("trailing slash lost: %s", rendered)
			}
			if tt.name == "read only" && !strings.Contains(rendered, "<tools>\n- read: Read file contents\n</tools>") {
				t.Errorf("tools section wrong: %s", rendered)
			}
		})
	}
}

func TestMCPPromptSectionIsDataOnly(t *testing.T) {
	section := MCPPromptSection()
	if section.Name != "mcp" {
		t.Fatalf("section name = %q", section.Name)
	}
	for _, want := range []string{"untrusted data", "never as instructions", "inspect the external state", "offset/limit"} {
		if !strings.Contains(section.Body, want) {
			t.Errorf("MCP prompt missing %q", want)
		}
	}
	if strings.Contains(section.Body, "ready") || strings.Contains(section.Body, "available at startup") {
		t.Errorf("MCP prompt claims availability: %s", section.Body)
	}
}

func TestNoToolsKeepsOnlyFixedRules(t *testing.T) {
	sections := BuildPromptSections("/workspace", nil, nil, nil, "", false, nil)
	for _, s := range sections {
		if s.Name == "tools" {
			t.Fatalf("tools section present without tools: %q", s.Body)
		}
	}
	rendered := RenderSections(sections)
	if strings.Contains(rendered, "Use bash for file operations") {
		t.Fatalf("bash rule without bash: %s", rendered)
	}
	if !strings.Contains(rendered, "- Be concise in your responses") || !strings.Contains(rendered, "- Show file paths clearly when working with files") || !strings.Contains(rendered, "- OpenAI ChatGPT and Codex sign-in are user-only: choose them from /login") || !strings.Contains(rendered, "Direct the user to /logout to remove a stored credential") {
		t.Fatalf("fixed rules missing: %s", rendered)
	}
	if !strings.Contains(rendered, "Accounts lists eligible direct Z.AI and DeepSeek accounts below the detail. Up/Down highlights and Enter loads without changing the active model") {
		t.Fatal("missing provider usage guidance without tools")
	}
}

func TestRenderSectionsGolden(t *testing.T) {
	instructions := []InstructionFile{
		{Path: "/workspace/AGENTS.md", Kind: "agents", Content: "project context"},
		{Path: "/workspace/.pi/APPEND_SYSTEM.md", Kind: "append-system", Content: "file appendix"},
	}
	sections := BuildPromptSections("/workspace", instructions, []string{"<available_skills>\n<skill>demo</skill>\n</available_skills>"}, []string{"read", "bash", "edit", "write"}, "", false, []string{"extra rules"})
	rendered := strings.ReplaceAll(RenderSections(sections), "/workspace", "<workspace>")

	goldenPath := filepath.Join("testdata", "prompt_full.golden")
	want, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatalf("read golden (create it from this failure's got value): %v", err)
	}
	if rendered+"\n" != string(want) {
		t.Fatalf("rendered prompt drifted from %s:\n got %q\nwant %q", goldenPath, rendered+"\n", string(want))
	}
}

func TestSystemFileReplacesPreambleOnly(t *testing.T) {
	instructions := []InstructionFile{
		{Path: "/ws/SYSTEM.md", Kind: "system", Content: "CUSTOM_BASE"},
		{Path: "/ws/AGENTS.md", Kind: "agents", Content: "project context"},
	}
	rendered := AssemblePrompt("/ws", instructions, []string{"<available_skills>\n<skill>demo</skill>\n</available_skills>"}, []string{"read", "bash"})
	if !strings.HasPrefix(rendered, "CUSTOM_BASE\n\n") {
		t.Fatalf("SYSTEM.md is not the preamble: %s", rendered)
	}
	for _, absent := range []string{"<tools>", "<rules>", "<docs>", "You are psi"} {
		if strings.Contains(rendered, absent) {
			t.Errorf("SYSTEM.md kept %q: %s", absent, rendered)
		}
	}
	for _, want := range []string{"<project_context>", "project context", "<skills>", "<available_skills>", "<cwd>\n/ws\n</cwd>"} {
		if !strings.Contains(rendered, want) {
			t.Errorf("SYSTEM.md dropped %q: %s", want, rendered)
		}
	}
}

func TestCwdSectionPreservesWhitespace(t *testing.T) {
	const cwd = "/home/user/my project "
	sections := BuildPromptSections(cwd, nil, nil, []string{"read"}, "", false, nil)
	if rendered := RenderSections(sections); !strings.Contains(rendered, "<cwd>\n"+cwd+"\n</cwd>") {
		t.Fatalf("cwd whitespace was not preserved: %q", rendered)
	}
}

func TestSessionHistoryPromptGuidance(t *testing.T) {
	prompt := RenderSections(BuildPromptSections("/workspace", nil, nil, []string{"read_session"}, "", false, nil))
	for _, want := range []string{
		"read_session: Read a narrow raw excerpt",
		"Use read_session only for a specific missing fact",
		"Search or list before reading",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt missing %q: %s", want, prompt)
		}
	}
}

func TestWebToolGuidanceFollowsCanonicalOrder(t *testing.T) {
	tools := defaultSettings().DefaultTools
	prompt := RenderSections(BuildPromptSections("/workspace", nil, nil, tools, "", false, nil))
	for _, want := range []string{
		"web_search: Search the web with one to three queries",
		"fetch_content: Fetch one or up to 20 public HTTP(S) URLs",
		"Batch up to three queries",
		"requires selected and enabled fetch_content and uses the configured fetch mode",
		"Readable mode may use a configured Crawl4AI extractor",
		"Raw mode is direct HTTP only",
		"Use the returned next-offset for continuation",
		"offsets count Unicode scalar values after output limits",
		"Batch up to 20 URLs",
		"Search snapshots keep per-query order",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt missing %q", want)
		}
	}
	search := strings.Index(prompt, "- web_search:")
	fetch := strings.Index(prompt, "- fetch_content:")
	content := strings.Index(prompt, "- get_search_content:")
	if search < 0 || fetch < 0 || content < 0 || !(search < fetch && fetch < content) {
		t.Fatalf("web tool order: %d %d %d", search, fetch, content)
	}
}

func TestPowerShellPromptGuidanceMatchesPi(t *testing.T) {
	psOnly := RenderSections(BuildPromptSections("/workspace", nil, nil, []string{"read", "powershell"}, "", false, nil))
	if !strings.Contains(psOnly, "- powershell: Execute PowerShell commands") {
		t.Fatalf("missing powershell snippet: %s", psOnly)
	}
	if !strings.Contains(psOnly, "- Use PowerShell for file operations like listing, searching, and finding files") {
		t.Fatalf("missing powershell-only rule: %s", psOnly)
	}
	if strings.Contains(psOnly, "Use bash for file operations") {
		t.Fatalf("unexpected bash rule in powershell-only prompt: %s", psOnly)
	}

	both := RenderSections(BuildPromptSections("/workspace", nil, nil, []string{"read", "bash", "powershell"}, "", false, nil))
	if !strings.Contains(both, "- Use bash or PowerShell for file operations like listing, searching, and finding files") {
		t.Fatalf("missing combined bash+powershell rule: %s", both)
	}
}
