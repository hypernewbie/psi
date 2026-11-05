package config

import (
	"strings"

	"psi/docs"
)

// PromptSection is one ordered system-prompt section. Body is stored already
// wrapped (<name>...</name>) except for preamble, which is bare.
type PromptSection struct {
	Name string
	Body string
}

// defaultPreamble replaces the old flat base line. The trailing "Show file
// paths clearly" clause is a rule now, so it is not repeated here.
const defaultPreamble = "You are psi, a minimal Go coding agent. Use the declared tools to inspect and change the workspace."

// Tool snippets and guidelines live here, not in internal/tools: config must
// not import tools, because tools imports provider and provider imports config.
var toolSnippets = map[string]string{
	"read":  "Read file contents",
	"bash":  "Execute shell commands",
	"edit":  "Make precise file edits with exact text replacement, including multiple disjoint edits in one call",
	"write": "Create or overwrite files",
}

var toolGuidelines = map[string][]string{
	"read": {"Use read to examine files instead of cat or sed."},
	"edit": {
		"Use edit for precise changes (edits[].oldText must match exactly)",
		"When changing multiple separate locations in one file, use one edit call with multiple entries in edits[] instead of multiple edit calls",
		"Each edits[].oldText is matched against the original file, not after earlier edits are applied. Do not emit overlapping or nested edits. Merge nearby changes into one edit.",
		"Keep edits[].oldText as small as possible while still being unique in the file. Do not pad with large unchanged regions.",
	},
	"write": {"Use write only for new files or complete rewrites."},
}

var fixedRules = []string{
	"Be concise in your responses",
	"Show file paths clearly when working with files",
}

// BuildPromptSections returns the ordered, non-empty system-prompt sections.
// A custom prompt (SYSTEM.md or --system-prompt) replaces the preamble and
// suppresses tools, rules, and docs; project context, skills, and cwd still render.
func BuildPromptSections(cwd string, instructions []InstructionFile, skillDescs []string, tools []string, systemPrompt string, useSystemPrompt bool, appendSystemPrompts []string) []PromptSection {
	base, custom := defaultPreamble, false
	var discoveredAppends []string
	for _, ins := range instructions {
		switch ins.Kind {
		case "system":
			base, custom = ins.Content, true
		case "append-system":
			discoveredAppends = append(discoveredAppends, ins.Content)
		}
	}
	if useSystemPrompt {
		base, custom = systemPrompt, true
	}

	var sections []PromptSection
	if base = strings.TrimSpace(base); base != "" {
		sections = append(sections, PromptSection{Name: "preamble", Body: base})
	}
	if !custom {
		if body := toolsSection(tools); body != "" {
			sections = append(sections, PromptSection{Name: "tools", Body: body})
		}
		if body := rulesSection(tools); body != "" {
			sections = append(sections, PromptSection{Name: "rules", Body: body})
		}
		if body := docsSection(); body != "" {
			sections = append(sections, PromptSection{Name: "docs", Body: body})
		}
	}
	if body := addendumSection(append(discoveredAppends, appendSystemPrompts...)); body != "" {
		sections = append(sections, PromptSection{Name: "addendum", Body: body})
	}
	if body := projectContextSection(instructions); body != "" {
		sections = append(sections, PromptSection{Name: "project_context", Body: body})
	}
	if body := skillsSection(skillDescs); body != "" {
		sections = append(sections, PromptSection{Name: "skills", Body: body})
	}
	if cwd = normalizePromptPath(cwd); cwd != "" {
		sections = append(sections, PromptSection{Name: "cwd", Body: "<cwd>\n" + cwd + "\n</cwd>"})
	}
	return sections
}

// RenderSections joins non-empty section bodies in order.
func RenderSections(sections []PromptSection) string {
	parts := make([]string, 0, len(sections))
	for _, s := range sections {
		if body := strings.TrimSpace(s.Body); body != "" {
			parts = append(parts, body)
		}
	}
	return strings.Join(parts, "\n\n")
}

func toolsSection(tools []string) string {
	var lines []string
	for _, name := range tools {
		if snippet, ok := toolSnippets[name]; ok && snippet != "" {
			lines = append(lines, "- "+name+": "+snippet)
		}
	}
	if len(lines) == 0 {
		return ""
	}
	return "<tools>\n" + strings.Join(lines, "\n") + "\n</tools>"
}

// rulesSection orders bullets the way pi does: the bash file-operations rule,
// per-tool guidelines in tool order, then the fixed prompt rules. Duplicates
// are dropped, first occurrence wins.
func rulesSection(tools []string) string {
	var rules []string
	seen := map[string]bool{}
	add := func(rule string) {
		rule = strings.TrimSpace(rule)
		if rule == "" || seen[rule] {
			return
		}
		seen[rule] = true
		rules = append(rules, rule)
	}
	for _, name := range tools {
		if name == "bash" {
			add("Use bash for file operations like ls, rg, find")
		}
	}
	for _, name := range tools {
		for _, rule := range toolGuidelines[name] {
			add(rule)
		}
	}
	for _, rule := range fixedRules {
		add(rule)
	}
	lines := make([]string, 0, len(rules))
	for _, rule := range rules {
		lines = append(lines, "- "+rule)
	}
	return "<rules>\n" + strings.Join(lines, "\n") + "\n</rules>"
}

// docsSection lists the topics embedded in the binary so the model can read
// them with `psi --docs` instead of guessing.
func docsSection() string {
	names := make([]string, 0, len(docs.Topics))
	for _, topic := range docs.Topics {
		names = append(names, topic.Name)
	}
	if len(names) == 0 {
		return ""
	}
	return "<docs>\nPsi documentation, read only when the user asks about psi itself, its CLI, settings, skills, sessions, or compaction:\n" +
		"- Run `psi --docs` to list topics.\n" +
		"- Run `psi --docs <topic>` to print one topic.\n" +
		"- Topics: " + strings.Join(names, ", ") + "\n" +
		"- Read the whole topic before answering.\n</docs>"
}

func addendumSection(appendSystemPrompts []string) string {
	var parts []string
	for _, text := range appendSystemPrompts {
		if text = strings.TrimSpace(text); text != "" {
			parts = append(parts, text)
		}
	}
	if len(parts) == 0 {
		return ""
	}
	return "<addendum>\n" + strings.Join(parts, "\n\n") + "\n</addendum>"
}

func projectContextSection(instructions []InstructionFile) string {
	var parts []string
	for _, ins := range instructions {
		if ins.Kind == "system" || ins.Kind == "append-system" {
			continue
		}
		content := strings.TrimSpace(ins.Content)
		if content == "" {
			continue
		}
		parts = append(parts, "<project_instructions path=\""+ins.Path+"\">\n"+content+"\n</project_instructions>")
	}
	if len(parts) == 0 {
		return ""
	}
	return "<project_context>\nProject-specific instructions and guidelines:\n\n" + strings.Join(parts, "\n\n") + "\n</project_context>"
}

func skillsSection(skillDescs []string) string {
	var parts []string
	for _, desc := range skillDescs {
		if desc = strings.TrimSpace(desc); desc != "" {
			parts = append(parts, desc)
		}
	}
	if len(parts) == 0 {
		return ""
	}
	return "<skills>\n" + strings.Join(parts, "\n\n") + "\n</skills>"
}

// normalizePromptPath keeps the cwd section stable across platforms.
func normalizePromptPath(path string) string {
	return strings.ReplaceAll(path, `\`, "/")
}
