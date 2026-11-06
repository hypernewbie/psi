package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGitHeadPathFindsRepositoryFromSubdirectory(t *testing.T) {
	root := t.TempDir()
	head := filepath.Join(root, ".git", "HEAD")
	if err := os.MkdirAll(filepath.Dir(head), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(head, []byte("ref: refs/heads/main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	sub := filepath.Join(root, "a", "b")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	got, ok := gitHeadPath(sub)
	if !ok || got != head {
		t.Fatalf("gitHeadPath(%q)=(%q, %v); want (%q, true)", sub, got, ok, head)
	}
}

func TestGitHeadPathReadsWorktreeGitFile(t *testing.T) {
	t.Run("relative pointer", func(t *testing.T) {
		root := t.TempDir()
		wt := filepath.Join(root, "shared", "wt")
		if err := os.MkdirAll(wt, 0o755); err != nil {
			t.Fatal(err)
		}
		head := filepath.Join(wt, "HEAD")
		if err := os.WriteFile(head, []byte("ref: refs/heads/context-bar\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, ".git"), []byte("gitdir: shared/wt\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		got, ok := gitHeadPath(root)
		if !ok || got != head {
			t.Fatalf("gitHeadPath(%q)=(%q, %v); want (%q, true)", root, got, ok, head)
		}
	})
	t.Run("absolute pointer", func(t *testing.T) {
		root := t.TempDir()
		real := t.TempDir()
		head := filepath.Join(real, "HEAD")
		if err := os.WriteFile(head, []byte("ref: refs/heads/main\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, ".git"), []byte("gitdir: "+real+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		got, ok := gitHeadPath(root)
		if !ok || got != head {
			t.Fatalf("gitHeadPath(%q)=(%q, %v); want (%q, true)", root, got, ok, head)
		}
	})
}

func TestGitHeadPathReportsNoRepositoryWhenHeadIsMissing(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "empty"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".git"), []byte("gitdir: empty\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, ok := gitHeadPath(root)
	if ok || got != "" {
		t.Fatalf("gitHeadPath(%q)=(%q, %v); want (\"\", false)", root, got, ok)
	}
}

func TestGitHeadPathKeepsWalkingPastAnUnrelatedGitFile(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, ".git"), []byte("not a gitdir\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	sub := filepath.Join(root, "a")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	got, ok := gitHeadPath(sub)
	if ok && strings.HasPrefix(got, root+string(filepath.Separator)) {
		t.Fatalf("gitHeadPath(%q)=(%q, true); the unrelated .git file stopped the walk", sub, got)
	}
}

func TestGitBranchFromHeadParsesBranchNames(t *testing.T) {
	for _, tc := range []struct {
		content string
		want    string
	}{
		{"ref: refs/heads/main\n", "main"},
		{"ref: refs/heads/feature/x", "feature/x"},
		{"ref: refs/heads/.invalid", "detached"},
		{"ref: refs/remotes/origin/main", "detached"},
		{"0123456789abcdef", "detached"},
		{"", "detached"},
	} {
		if got := gitBranchFromHead(tc.content); got != tc.want {
			t.Errorf("gitBranchFromHead(%q)=%q; want %q", tc.content, got, tc.want)
		}
	}
}
