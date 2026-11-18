package provider

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
)

// authTarget is the resolved filesystem object for one auth write transaction.
// logical is the user-visible path used for the shared lease; path is the
// regular file that a rename may replace. An absent target keeps its resolved
// parent path and parent identity so a later directory replacement is refused.
type authTarget struct {
	logical    string
	path       string
	info       os.FileInfo
	parentInfo os.FileInfo
	exists     bool
}

// statAuthIdentity captures identity now rather than at a later comparison.
// Windows os.Stat defers file-ID lookup until os.SameFile, so use an opened
// handle there. Other platforms retain os.Stat and its permission requirements.
func statAuthIdentity(path string) (os.FileInfo, error) {
	if runtime.GOOS != "windows" {
		return os.Stat(path)
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	return file.Stat()
}

// resolveAuthTarget follows the final path and its parent directories. A
// missing final component is a valid login target only when its parent
// resolves to a real directory. Dangling or cyclic links and nonregular
// targets fail closed instead of becoming a new file.
func resolveAuthTarget(path string) (authTarget, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return authTarget{}, fmt.Errorf("cannot resolve auth.json path")
	}
	parent, base := filepath.Split(absolute)
	resolvedParent, err := filepath.EvalSymlinks(parent)
	if err != nil {
		return authTarget{}, fmt.Errorf("cannot resolve auth.json directory")
	}
	target := filepath.Join(resolvedParent, base)

	if _, statErr := os.Lstat(absolute); errors.Is(statErr, os.ErrNotExist) {
		parentInfo, err := statAuthIdentity(resolvedParent)
		if err != nil || !parentInfo.IsDir() {
			return authTarget{}, fmt.Errorf("cannot resolve auth.json directory")
		}
		return authTarget{logical: absolute, path: target, parentInfo: parentInfo}, nil
	} else if statErr != nil {
		return authTarget{}, fmt.Errorf("cannot inspect auth.json")
	}
	resolved, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return authTarget{}, fmt.Errorf("cannot resolve auth.json target")
	}
	targetInfo, err := statAuthIdentity(resolved)
	if err != nil {
		return authTarget{}, fmt.Errorf("cannot inspect auth.json target")
	}
	if !targetInfo.Mode().IsRegular() {
		return authTarget{}, fmt.Errorf("auth.json must be a regular file")
	}
	return authTarget{logical: absolute, path: resolved, info: targetInfo, exists: true}, nil
}

// recheck refuses a changed logical target, file identity, or absent-target
// parent. It intentionally does not compare size or mtime: Codex refresh must
// still merge unrelated in-place field updates.
func (t authTarget) recheck() error {
	current, err := resolveAuthTarget(t.logical)
	if err != nil {
		return err
	}
	if current.path != t.path || current.exists != t.exists {
		return fmt.Errorf("auth.json target changed; refusing to write")
	}
	if t.exists {
		if current.info == nil || !os.SameFile(t.info, current.info) {
			return fmt.Errorf("auth.json target changed; refusing to write")
		}
		return nil
	}
	if current.parentInfo == nil || t.parentInfo == nil || !os.SameFile(t.parentInfo, current.parentInfo) {
		return fmt.Errorf("auth.json target changed; refusing to write")
	}
	return nil
}
