package app

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// gitHeadPath walks up from dir and returns the path of the HEAD file of
// the nearest enclosing git work tree. It returns "", false when dir is not
// inside a work tree, when a .git entry cannot be read, or when the HEAD
// file is missing.
func gitHeadPath(dir string) (string, bool) {
	if dir == "" {
		return "", false
	}
	current := filepath.Clean(dir)
	for {
		gitPath := filepath.Join(current, ".git")
		info, err := os.Stat(gitPath)
		if err == nil {
			if info.IsDir() {
				head := filepath.Join(gitPath, "HEAD")
				if _, headErr := os.Stat(head); headErr == nil {
					return head, true
				}
				return "", false
			}
			if info.Mode().IsRegular() {
				content, readErr := os.ReadFile(gitPath)
				if readErr != nil {
					return "", false
				}
				text := strings.TrimSpace(string(content))
				if strings.HasPrefix(text, "gitdir:") {
					target := strings.TrimSpace(strings.TrimPrefix(text, "gitdir:"))
					if target == "" {
						return "", false
					}
					if !filepath.IsAbs(target) {
						target = filepath.Join(current, target)
					}
					head := filepath.Join(target, "HEAD")
					if _, headErr := os.Stat(head); headErr != nil {
						return "", false
					}
					return head, true
				}
				// Not a work tree pointer. Keep walking up.
			}
		} else if !os.IsNotExist(err) {
			return "", false
		}
		parent := filepath.Dir(current)
		if parent == current {
			return "", false
		}
		current = parent
	}
}

// gitBranchFromHead returns the branch name recorded in a HEAD file. A
// detached HEAD has no branch name and yields "detached".
func gitBranchFromHead(content string) string {
	const prefix = "ref: refs/heads/"
	text := strings.TrimSpace(content)
	if !strings.HasPrefix(text, prefix) {
		return "detached"
	}
	name := strings.TrimSpace(strings.TrimPrefix(text, prefix))
	if name == "" || name == ".invalid" {
		return "detached"
	}
	return name
}

// gitBranchFor returns the branch of the work tree that contains dir, or
// "" when dir is not inside a work tree. The result is cached per working
// directory and refreshed when the HEAD file changes.
func (r *runtime) gitBranchFor(dir string) string {
	if r.gitBranchDir != dir {
		r.gitBranchDir = dir
		r.gitHeadFile, r.gitHeadFound = gitHeadPath(dir)
		r.gitBranchKey, r.gitBranchValue = "", ""
	}
	if !r.gitHeadFound {
		return ""
	}
	info, err := os.Stat(r.gitHeadFile)
	if err != nil {
		return ""
	}
	// A real HEAD file is a few bytes. A larger file is not a work tree
	// pointer, so read nothing and show no branch.
	if info.Size() > 4096 {
		return ""
	}
	key := fmt.Sprintf("%s|%d|%d", r.gitHeadFile, info.ModTime().UnixNano(), info.Size())
	if key == r.gitBranchKey {
		return r.gitBranchValue
	}
	file, err := os.Open(r.gitHeadFile)
	if err != nil {
		return ""
	}
	defer file.Close()
	content, err := io.ReadAll(io.LimitReader(file, 4096))
	if err != nil {
		return ""
	}
	branch := gitBranchFromHead(string(content))
	r.gitBranchKey, r.gitBranchValue = key, branch
	return branch
}
