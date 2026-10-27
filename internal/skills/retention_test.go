package skills

// Phase H regressions for PLAN3 F15: discovery retains metadata, never
// bodies. Invoke rereads the file, so post-discovery edits take effect.

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func mkSkillDir(t *testing.T, agentDir string, n int, bodySize int) {
	t.Helper()
	root := filepath.Join(agentDir, "skills")
	for i := 0; i < n; i++ {
		dir := filepath.Join(root, fmt.Sprintf("skill-%03d", i))
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		body := strings.Repeat(fmt.Sprintf("body-%d-", i), bodySize/8)
		content := "---\nname: test-skill-" + fmt.Sprintf("%03d", i) + "\ndescription: synthetic skill for retention test\n---\n" + body
		if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func heapBytes() uint64 {
	runtime.GC()
	runtime.GC()
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	return m.HeapAlloc
}

func TestF15DiscoveryRetainsMetadataOnly(t *testing.T) {
	for _, n := range []int{16, 256} {
		t.Run(fmt.Sprintf("skills-%d", n), func(t *testing.T) {
			agent := t.TempDir()
			mkSkillDir(t, agent, n, 128*1024)
			before := heapBytes()
			got, diags := Discover(t.TempDir(), agent, nil, true)
			_ = diags
			if len(got) != n {
				t.Fatalf("discovered %d, want %d", len(got), n)
			}
			after := heapBytes()
			// 128 KiB bodies each: retained heap must track metadata (~KBs),
			// not the tens of MiB of source bodies. Signed: GC timing noise
			// can only shrink the delta, never hide MiB-scale retention.
			if delta := int64(after) - int64(before); delta > 4*1024*1024 {
				t.Fatalf("retained heap grew %d bytes for %d skills", delta, n)
			}
			// No body text reachable through diagnostics either.
			for _, d := range diags {
				if strings.Contains(d.Message, "body-") {
					t.Fatalf("body leaked into diagnostic: %v", d)
				}
			}
		})
	}
}

func TestF15InvocationSeesCurrentFile(t *testing.T) {
	agent := t.TempDir()
	mkSkillDir(t, agent, 2, 1024)
	got, _ := Discover(t.TempDir(), agent, nil, true)
	if len(got) != 2 {
		t.Fatalf("discovered %d", len(got))
	}
	first, err := Invoke(got, "test-skill-000", "")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(first, "body-0-") {
		t.Fatalf("unexpected invocation: %.60q", first)
	}
	// Change the file after discovery: invocation must use current content.
	dir := filepath.Join(agent, "skills", "skill-000", "SKILL.md")
	updated := "---\nname: test-skill-000\ndescription: synthetic skill for retention test\n---\nCHANGED-BODY-MARKER\n"
	if err := os.WriteFile(dir, []byte(updated), 0o644); err != nil {
		t.Fatal(err)
	}
	second, err := Invoke(got, "test-skill-000", "")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(second, "CHANGED-BODY-MARKER") {
		t.Fatalf("stale invocation: %.60q", second)
	}
}

func TestF15SymlinkCycleAndDuplicates(t *testing.T) {
	agent := t.TempDir()
	skillsRoot := filepath.Join(agent, "skills")
	mkSkillDir(t, agent, 2, 64)
	// Symlink cycle back to the skills root.
	if err := os.Symlink(skillsRoot, filepath.Join(skillsRoot, "skill-000", "loop")); err != nil {
		t.Fatal(err)
	}
	// Duplicate name in a second tree.
	dup := filepath.Join(agent, "extra")
	if err := os.MkdirAll(filepath.Join(dup, "dup"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dup, "dup", "SKILL.md"),
		[]byte("---\nname: test-skill-000\ndescription: duplicate name\n---\nbody"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Malformed YAML skill.
	bad := filepath.Join(skillsRoot, "bad")
	if err := os.MkdirAll(bad, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bad, "SKILL.md"), []byte("---\nname: [unclosed\ndescription: x\n---\nbody"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, diags := Discover(t.TempDir(), agent, []string{dup}, true)
	if len(got) == 0 {
		t.Fatal("no skills discovered")
	}
	t.Logf("discovered=%d diagnostics=%d", len(got), len(diags))
}
