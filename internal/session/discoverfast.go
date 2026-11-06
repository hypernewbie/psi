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

// DiscoverHeaders lists session identities without parsing message bodies.
// Reads start at 1KB, extending only for headers up to maxHeaderFastScan.
// MessageCount is zero and FirstMessage is a placeholder; use Discover for
// transcript metadata and ownership status.
func DiscoverHeaders(sessionDir string) ([]SessionInfo, error) {
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
		if st.Mode()&os.ModeSymlink != 0 {
			st, err = os.Stat(p)
			if err != nil {
				continue
			}
		}
		if !st.Mode().IsRegular() {
			continue
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
		h, headerErr := readHeaderFast(f.path)
		if h == nil {
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
		if headerErr != nil {
			info.Unavailable = headerErr.Error()
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
	info, err := os.Lstat(candidate)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", err
	}
	if !info.Mode().IsRegular() {
		// The canonical name is not a regular file, so it cannot hold the
		// session. Leave it alone and let the caller use discovery.
		return "", nil
	}
	header, err := readHeaderFast(candidate)
	if err != nil {
		return "", fmt.Errorf("cannot use session file %s: %w", candidate, err)
	}
	if header.ID != id {
		return "", fmt.Errorf("session file %s has ID %q, want %q", candidate, header.ID, id)
	}
	if cwd != "" && !SameDir(header.Cwd, cwd) {
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
