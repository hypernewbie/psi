package skills

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestYAMLMultilineDescriptionAndInvocationDirectory(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	agent, ws := t.TempDir(), t.TempDir()
	file := writeSkill(t, filepath.Join(agent, "skills"), "folded", "name: folded\ndescription: >\n  Read the repository:\n  then run its tests.\nmetadata:\n  owner: test", "Read references/example.md")
	found, diagnostics := Discover(ws, agent, nil, false)
	if len(diagnostics) != 0 || len(found) != 1 || !strings.Contains(found[0].Description, "then run its tests.") {
		t.Fatalf("YAML frontmatter lost: %+v %+v", found, diagnostics)
	}
	expanded, err := Invoke(found, "folded", "request")
	if err != nil || !strings.Contains(expanded, filepath.Dir(file)) {
		t.Fatalf("relative instruction paths lack their skill directory: %q %v", expanded, err)
	}
}
