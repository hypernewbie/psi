package session

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type fileStat struct {
	path    string
	modUnix int64
	mtime   time.Time
}

func discoverFast(sessionDir string) ([]SessionInfo, error) {
	dirents, err := os.ReadDir(sessionDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var files []fileStat
	for _, d := range dirents {
		if d.IsDir() || !strings.HasSuffix(d.Name(), ".jsonl") {
			continue
		}
		p := filepath.Join(sessionDir, d.Name())
		st, err := d.Info()
		if err != nil {
			st2, err2 := os.Stat(p)
			if err2 != nil {
				continue
			}
			st = st2
		}
		files = append(files, fileStat{path: p, modUnix: st.ModTime().UnixNano(), mtime: st.ModTime()})
	}
	sort.Slice(files, func(i, j int) bool {
		if files[i].modUnix == files[j].modUnix {
			return files[i].path < files[j].path
		}
		return files[i].modUnix > files[j].modUnix
	})
	var out []SessionInfo
	for _, f := range files {
		h, err := readHeaderFast(f.path)
		if err != nil {
			continue
		}
		info := SessionInfo{
			Path:         f.path,
			ID:           h.ID,
			Cwd:          h.Cwd,
			Modified:     f.mtime,
			MessageCount: 0,
			FirstMessage: "(no messages)",
		}
		if t, err := time.Parse(time.RFC3339Nano, h.Timestamp); err == nil {
			info.Created = t
		}
		if held, err := probeSharedLock(f.path); err == nil && held {
			info.Unavailable = "locked by another psi process"
		} else if err != nil && !os.IsNotExist(err) {
			info.Unavailable = "cannot inspect ownership lock"
		}
		out = append(out, info)
	}
	return out, nil
}

// FindExact returns the canonical path for an exact session ID without
// scanning the session directory. It returns an empty path and nil error
// when the canonical file does not exist, matching FindMostRecent.
func FindExact(sessionDir, cwd, id string) (string, error) {
	if err := AssertValidSessionID(id); err != nil {
		return "", err
	}
	candidate := filepath.Join(sessionDir, id+".jsonl")
	if _, err := os.Stat(candidate); err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", err
	}
	header, err := readHeaderFast(candidate)
	if err != nil {
		return "", fmt.Errorf("cannot use session file %s: %w", candidate, err)
	}
	if header.ID != id {
		return "", fmt.Errorf("session file %s has ID %q, want %q", candidate, header.ID, id)
	}
	if !SameDir(header.Cwd, cwd) {
		return "", fmt.Errorf("session %q belongs to a different workspace", id)
	}
	return candidate, nil
}

func findRecentFast(sessionDir, cwd string) (string, error) {
	dirents, err := os.ReadDir(sessionDir)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", err
	}
	var files []fileStat
	for _, d := range dirents {
		if d.IsDir() || !strings.HasSuffix(d.Name(), ".jsonl") {
			continue
		}
		p := filepath.Join(sessionDir, d.Name())
		st, err := d.Info()
		if err != nil {
			st2, err2 := os.Stat(p)
			if err2 != nil {
				continue
			}
			st = st2
		}
		files = append(files, fileStat{path: p, modUnix: st.ModTime().UnixNano(), mtime: st.ModTime()})
	}
	sort.Slice(files, func(i, j int) bool {
		if files[i].modUnix == files[j].modUnix {
			return files[i].path < files[j].path
		}
		return files[i].modUnix > files[j].modUnix
	})
	for _, f := range files {
		h, err := readHeaderFast(f.path)
		if err != nil {
			continue
		}
		if cwd == "" || SameDir(h.Cwd, cwd) {
			return f.path, nil
		}
	}
	return "", nil
}
