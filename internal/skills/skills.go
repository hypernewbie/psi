// Package skills discovers instruction-only skills (Phase 5).
package skills

// Skill is a discovered SKILL.md (metadata + path; body loaded on demand).
type Skill struct {
	Name        string
	Description string
	FilePath    string
}
