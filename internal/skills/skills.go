// Package skills discovers instruction-only skills (Phase 5).
//
// No package installation, no code execution at discovery. Skills add
// instructions only; scripts are ordinary external files.
package skills

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// Skill is a discovered SKILL.md: metadata and paths only (F15). The body
// is never retained after frontmatter extraction; Invoke rereads the file.
type Skill struct {
	Name        string
	Description string
	FilePath    string
	BaseDir     string
	Disable     bool
}

// Diagnostic for malformed files and duplicate names.
type Diagnostic struct {
	Path    string
	Message string
}

// Discover returns a deterministic list without network requests.
// Order: user dir, project dir, .agents fallbacks, configured paths.
// Applies project consent before loading project skill metadata.
// Bounds traversal and breaks symlink cycles.
func Discover(cwd, agentDir string, configured []string, projectConsent bool) ([]Skill, []Diagnostic) {
	var all []Skill
	var diags []Diagnostic
	seenFiles := map[string]bool{} // canonical path -> seen
	seenNames := map[string]string{}

	var excluded []string
	for _, p := range configured {
		if strings.HasPrefix(p, "-") || strings.HasPrefix(p, "!") {
			excluded = append(excluded, p[1:])
		}
	}
	add := func(skills []Skill, d []Diagnostic) {
		diags = append(diags, d...)
		for _, s := range skills {
			canon := canonical(s.FilePath)
			skip := false
			for _, pattern := range excluded {
				match, _ := filepath.Match(pattern, s.FilePath)
				if match || canon == canonical(pattern) || strings.HasPrefix(canon, canonical(pattern)+string(filepath.Separator)) {
					skip = true
					break
				}
			}
			if skip {
				continue
			}
			if len(all) >= 256 {
				diags = append(diags, Diagnostic{Path: s.FilePath, Message: "skill limit exceeded (256)"})
				continue
			}
			if seenFiles[canon] {
				continue
			}
			if winner, dup := seenNames[s.Name]; dup {
				diags = append(diags, Diagnostic{Path: s.FilePath, Message: fmt.Sprintf("name %q collision: winner %s", s.Name, winner)})
				continue
			}
			seenFiles[canon] = true
			seenNames[s.Name] = s.FilePath
			all = append(all, s)
		}
	}
	// User: <agentDir>/skills
	s, d := loadDir(filepath.Join(agentDir, "skills"))
	add(s, d)
	// Project: <cwd>/.pi/skills (consent-gated)
	if projectConsent {
		s, d := loadDir(filepath.Join(cwd, ".pi", "skills"))
		add(s, d)
		// Selected .agents fallbacks from pinned behavior.
		s, d = loadDir(filepath.Join(cwd, ".agents", "skills"))
		add(s, d)
	}
	home, _ := os.UserHomeDir()
	s, d = loadDir(filepath.Join(home, ".agents", "skills"))
	add(s, d)
	// Configured paths have already been resolved against their source files.
	for _, p := range configured {
		if strings.HasPrefix(p, "-") || strings.HasPrefix(p, "!") {
			continue
		}
		abs := strings.TrimPrefix(p, "+")
		if !filepath.IsAbs(abs) {
			abs = filepath.Join(cwd, abs)
		}
		fi, err := os.Stat(abs)
		if err != nil {
			diags = append(diags, Diagnostic{Path: abs, Message: "skill path does not exist"})
			continue
		}
		if fi.IsDir() {
			s, d := loadDir(abs)
			add(s, d)
		} else if strings.HasSuffix(abs, ".md") {
			sk, dg := loadFile(abs)
			if sk != nil {
				add([]Skill{*sk}, dg)
			} else {
				diags = append(diags, dg...)
			}
		}
	}
	sort.Slice(all, func(i, j int) bool {
		if all[i].Name == all[j].Name {
			return all[i].FilePath < all[j].FilePath
		}
		return all[i].Name < all[j].Name
	})
	return all, diags
}

func canonical(p string) string {
	if e, err := filepath.EvalSymlinks(p); err == nil {
		return e
	}
	return p
}

func loadDir(dir string) ([]Skill, []Diagnostic) {
	var skills []Skill
	var diags []Diagnostic
	fi, err := os.Stat(dir)
	if err != nil || !fi.IsDir() {
		return nil, nil
	}
	// If dir itself contains SKILL.md, treat as one skill root.
	if _, err := os.Stat(filepath.Join(dir, "SKILL.md")); err == nil {
		sk, dg := loadFile(filepath.Join(dir, "SKILL.md"))
		if sk != nil {
			return []Skill{*sk}, dg
		}
		return nil, dg
	}
	visited := map[string]bool{}
	var walk func(cur string, depth int)
	walk = func(cur string, depth int) {
		canon := canonical(cur)
		if depth > 8 || visited[canon] {
			return
		}
		visited[canon] = true
		ents, err := os.ReadDir(cur)
		if err != nil {
			return
		}
		// Check for SKILL.md first (skill root: do not recurse further).
		for _, e := range ents {
			if e.Name() == "SKILL.md" {
				fp := filepath.Join(cur, "SKILL.md")
				sk, dg := loadFile(fp)
				diags = append(diags, dg...)
				if sk != nil {
					skills = append(skills, *sk)
				}
				return
			}
		}
		for _, e := range ents {
			if strings.HasPrefix(e.Name(), ".") || e.Name() == "node_modules" {
				continue
			}
			fp := filepath.Join(cur, e.Name())
			// Follow symlinks with cycle guard.
			fi, err := os.Stat(fp) // follows symlinks
			if err != nil {
				continue
			}
			if fi.IsDir() {
				walk(fp, depth+1)
			} else if depth == 0 && strings.HasSuffix(e.Name(), ".md") {
				sk, dg := loadFile(fp)
				diags = append(diags, dg...)
				if sk != nil {
					skills = append(skills, *sk)
				}
			}
		}
	}
	// Root .md files + recursion.
	walk(dir, 0)
	// Bound advertised metadata: cap total skills and per-file size.
	const maxSkills = 256
	const maxFileBytes = 256 * 1024
	var bounded []Skill
	for _, s := range skills {
		if len(bounded) >= maxSkills {
			diags = append(diags, Diagnostic{Path: s.FilePath, Message: "skill limit exceeded (256); skipped"})
			continue
		}
		if st, err := os.Stat(s.FilePath); err == nil && st.Size() > maxFileBytes {
			diags = append(diags, Diagnostic{Path: s.FilePath, Message: "skill file too large; skipped"})
			continue
		}
		bounded = append(bounded, s)
	}
	return bounded, diags
}

func loadFile(path string) (*Skill, []Diagnostic) {
	b, err := readSkill(path)
	if err != nil {
		return nil, []Diagnostic{{Path: path, Message: "unreadable or oversized skill file"}}
	}
	if len(b) > 256*1024 {
		return nil, []Diagnostic{{Path: path, Message: "skill file too large"}}
	}
	fm, _ := parseFrontmatter(string(b))
	name, _ := fm["name"].(string)
	desc, _ := fm["description"].(string)
	disable, _ := fm["disable-model-invocation"].(bool)
	var errs []string
	if strings.TrimSpace(name) == "" {
		errs = append(errs, "name is required")
	} else {
		errs = append(errs, validateName(name)...)
	}
	if strings.TrimSpace(desc) == "" {
		errs = append(errs, "description is required")
	} else if len(desc) > 1024 {
		errs = append(errs, fmt.Sprintf("description exceeds 1024 characters (%d)", len(desc)))
	}
	if len(errs) > 0 {
		var dg []Diagnostic
		for _, e := range errs {
			dg = append(dg, Diagnostic{Path: path, Message: e})
		}
		return nil, dg
	}
	// Clone small metadata so no slice of the (up to 256 KiB) source buffer
	// survives discovery (F15).
	return &Skill{Name: strings.Clone(name), Description: strings.Clone(desc), FilePath: path, BaseDir: filepath.Dir(path), Disable: disable}, nil
}

func validateName(name string) []string {
	var errs []string
	if len(name) > 64 {
		errs = append(errs, "name exceeds 64 characters")
	}
	for _, c := range name {
		if !((c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '-') {
			errs = append(errs, "name contains invalid characters (must be lowercase a-z, 0-9, hyphens only)")
			break
		}
	}
	if strings.HasPrefix(name, "-") || strings.HasSuffix(name, "-") {
		errs = append(errs, "name must not start or end with a hyphen")
	}
	if strings.Contains(name, "--") {
		errs = append(errs, "name must not contain consecutive hyphens")
	}
	return errs
}

// FormatForPrompt advertises name/description/path without the body.
// Excludes disable-model-invocation skills from automatic advertisement.
func FormatForPrompt(skills []Skill) string {
	var vis []Skill
	for _, s := range skills {
		if !s.Disable {
			vis = append(vis, s)
		}
	}
	if len(vis) == 0 {
		return ""
	}
	var sb strings.Builder
	sb.WriteString("\n\nThe following skills provide specialized instructions for specific tasks.\n")
	sb.WriteString("Use the read tool to load a skill's file when the task matches its description.\n")
	sb.WriteString("When a skill file references a relative path, resolve it against the skill directory (parent of SKILL.md / dirname of the path) and use that absolute path in tool commands.\n")
	sb.WriteString("\n<available_skills>\n")
	for _, s := range vis {
		sb.WriteString("  <skill>\n")
		sb.WriteString("    <name>" + escapeXML(s.Name) + "</name>\n")
		sb.WriteString("    <description>" + escapeXML(s.Description) + "</description>\n")
		sb.WriteString("    <location>" + escapeXML(s.FilePath) + "</location>\n")
		sb.WriteString("  </skill>\n")
	}
	sb.WriteString("</available_skills>")
	return sb.String()
}

func escapeXML(s string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;", `'`, "&apos;")
	return r.Replace(s)
}

// Invoke expands /skill:name into Pi-style user input (skill body + args).
// Reports missing/unreadable without a provider request.
func Invoke(skills []Skill, name, args string) (string, error) {
	for _, s := range skills {
		if s.Name == name {
			b, err := readSkill(s.FilePath)
			if err != nil {
				return "", fmt.Errorf("skill %q unreadable: %w", name, err)
			}
			_, body := parseFrontmatter(string(b))
			body = "Skill directory: " + s.BaseDir + "\nResolve relative paths from this directory.\n\n" + body
			if strings.TrimSpace(args) != "" {
				return body + "\n\nArguments: " + args, nil
			}
			return body, nil
		}
	}
	return "", fmt.Errorf("skill %q not found", name)
}

func readSkill(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, 256*1024+1))
	if len(b) > 256*1024 {
		return nil, fmt.Errorf("skill file exceeds 256 KiB")
	}
	return b, err
}

// Use YAML rather than a line splitter: real skills commonly use folded and
// literal multiline descriptions, quoted colons, comments, and nested metadata.
func parseFrontmatter(content string) (map[string]any, string) {
	norm := strings.TrimPrefix(strings.ReplaceAll(content, "\r\n", "\n"), "\ufeff")
	if !strings.HasPrefix(norm, "---\n") {
		return map[string]any{}, norm
	}
	lines := strings.Split(norm, "\n")
	for i := 1; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) != "---" {
			continue
		}
		var fm map[string]any
		if err := yaml.Unmarshal([]byte(strings.Join(lines[1:i], "\n")), &fm); err != nil {
			return map[string]any{}, norm
		}
		return fm, strings.TrimSpace(strings.Join(lines[i+1:], "\n"))
	}
	return map[string]any{}, norm
}
