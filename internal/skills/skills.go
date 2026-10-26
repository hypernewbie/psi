// Package skills discovers instruction-only skills (Phase 5).
//
// No package installation, no code execution at discovery. Skills add
// instructions only; scripts are ordinary external files.
package skills

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Skill is a discovered SKILL.md (metadata + path; body loaded on demand).
type Skill struct {
	Name        string
	Description string
	FilePath    string
	BaseDir     string
	Disable     bool
	Body        string
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

	add := func(skills []Skill, d []Diagnostic) {
		diags = append(diags, d...)
		for _, s := range skills {
			canon := canonical(s.FilePath)
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
	// Configured paths from documented base dirs (cwd-relative or absolute).
	for _, p := range configured {
		abs := p
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
		if depth > 8 || visited[cur] {
			return
		}
		visited[cur] = true
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
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, []Diagnostic{{Path: path, Message: "unreadable skill file"}}
	}
	if len(b) > 256*1024 {
		return nil, []Diagnostic{{Path: path, Message: "skill file too large"}}
	}
	fm, body := parseFrontmatter(string(b))
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
	return &Skill{Name: name, Description: desc, FilePath: path, BaseDir: filepath.Dir(path), Disable: disable, Body: body}, nil
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
			b, err := os.ReadFile(s.FilePath)
			if err != nil {
				return "", fmt.Errorf("skill %q unreadable: %w", name, err)
			}
			_, body := parseFrontmatter(string(b))
			if strings.TrimSpace(args) != "" {
				return body + "\n\nArguments: " + args, nil
			}
			return body, nil
		}
	}
	return "", fmt.Errorf("skill %q not found", name)
}

// parseFrontmatter parses --- YAML --- + body (minimal subset: key: value).
func parseFrontmatter(content string) (map[string]any, string) {
	norm := strings.ReplaceAll(strings.ReplaceAll(content, "\r\n", "\n"), "\r", "\n")
	if !strings.HasPrefix(norm, "---") {
		return map[string]any{}, norm
	}
	// Find closing --- on its own line.
	rest := norm[3:]
	if strings.HasPrefix(rest, "\n") {
		rest = rest[1:]
	}
	idx := strings.Index(rest, "\n---")
	if idx < 0 {
		return map[string]any{}, norm
	}
	yamlStr := rest[:idx]
	body := strings.TrimSpace(rest[idx+4:])
	fm := map[string]any{}
	for _, line := range strings.Split(yamlStr, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		kv := strings.SplitN(line, ":", 2)
		if len(kv) != 2 {
			continue
		}
		k := strings.TrimSpace(kv[0])
		v := strings.TrimSpace(kv[1])
		v = strings.Trim(v, `"'`)
		switch strings.ToLower(v) {
		case "true":
			fm[k] = true
		case "false":
			fm[k] = false
		default:
			fm[k] = v
		}
	}
	return fm, body
}
