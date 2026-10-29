package session

import (
	"fmt"
	"os"
	"strings"
)

func validateGraph(entries []*Entry) error {
	byID := make(map[string]*Entry, len(entries))
	for _, e := range entries {
		if _, ok := byID[e.ID()]; ok {
			return fmt.Errorf("duplicate entry id %q", e.ID())
		}
		byID[e.ID()] = e
	}
	done := map[string]bool{}
	for _, e := range entries {
		var chain []string
		seen := map[string]bool{}
		for e != nil && !done[e.ID()] {
			if seen[e.ID()] {
				return fmt.Errorf("parent cycle at entry %q", e.ID())
			}
			seen[e.ID()] = true
			chain = append(chain, e.ID())
			p := e.ParentID()
			if p == nil || *p == "" {
				break
			}
			parent, ok := byID[*p]
			if !ok {
				return fmt.Errorf("missing parent %q for entry %q", *p, e.ID())
			}
			e = parent
		}
		for _, id := range chain {
			done[id] = true
		}
	}
	return nil
}

// MetadataPath never materializes hidden or compacted payloads.
func (s *Store) MetadataPath() ([]*Entry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return BuildPath(s.entries, s.leafID)
}
func (s *Store) EntryCount() int { s.mu.Lock(); defer s.mu.Unlock(); return len(s.entries) }
func (s *Store) openSnapshotLocked() (*os.File, error) {
	f, err := os.Open(s.file)
	if err != nil {
		return nil, err
	}
	st, err := f.Stat()
	if err == nil && s.knownInfo != nil && (!os.SameFile(st, s.knownInfo) || st.Size() != s.knownSize || !st.ModTime().Equal(s.knownInfo.ModTime())) {
		err = fmt.Errorf("session file changed externally: %s", s.file)
	}
	if err != nil {
		f.Close()
		return nil, err
	}
	return f, nil
}

// snapIdentity records the generation a lazy walk started from.
type snapIdentity struct {
	path      string
	knownInfo os.FileInfo
	knownSize int64
}

func (s *Store) snapLocked() snapIdentity {
	return snapIdentity{path: s.file, knownInfo: s.knownInfo, knownSize: s.knownSize}
}

// unchanged reports whether the session file still matches the generation.
// An open descriptor pins an inode, not immutable bytes: every lazy payload
// read rechecks the live path so a mid-walk rewrite can never mix generations.
func (snap snapIdentity) unchanged() bool {
	if snap.path == "" || snap.knownInfo == nil {
		return true
	}
	st, err := os.Stat(snap.path)
	if err != nil {
		return false
	}
	return os.SameFile(st, snap.knownInfo) && st.Size() == snap.knownSize && st.ModTime().Equal(snap.knownInfo.ModTime())
}

func externalChangeError(path string) error {
	return fmt.Errorf("session file changed externally during read, refusing mixed generation: %s", path)
}

func (s *Store) loadEntriesLocked(entries []*Entry) ([]*Entry, error) {
	var f *os.File
	defer func() {
		if f != nil {
			f.Close()
		}
	}()
	snap := s.snapLocked()
	hook := s.LoadHook
	out := make([]*Entry, 0, len(entries))
	for _, e := range entries {
		if meta, ok := s.records[e.ID()]; ok {
			// Fire the mutation hook before opening the snapshot. On
			// Windows an open handle pins the file and hides removal or
			// replacement from this walk.
			if hook != nil {
				hook(meta)
			}
			if f == nil {
				var err error
				f, err = s.openSnapshotLocked()
				if err != nil {
					return nil, err
				}
			}
			if !snap.unchanged() {
				return nil, externalChangeError(s.file)
			}
			loaded, err := loadRecord(f, meta)
			if err != nil {
				return nil, err
			}
			out = append(out, loaded)
		} else {
			out = append(out, e)
		}
	}
	if f != nil && !snap.unchanged() {
		return nil, externalChangeError(s.file)
	}
	return out, nil
}

// Timeline visits one visible record at a time. Hidden tool bodies and reasoning
// never enter the display working set. n==0 means the entire selected view.
func (s *Store) Timeline(all bool, n int, emit func(TranscriptItem) bool) (bool, error) {
	s.mu.Lock()
	path, err := BuildPath(s.entries, s.leafID)
	if err != nil {
		s.mu.Unlock()
		return false, err
	}
	entries := path
	if !all {
		entries = BuildContextEntries(path)
	}
	if n > 0 {
		count := 0
		for i := len(entries) - 1; i >= 0; i-- {
			e := entries[i]
			if e.Message != nil && e.Message.Message.User != nil {
				count++
				if count == n {
					entries = entries[i:]
					break
				}
			}
		}
	}
	var f *os.File
	if len(s.records) > 0 {
		f, err = s.openSnapshotLocked()
		if err != nil {
			s.mu.Unlock()
			return false, err
		}
	}
	// The caller serializes activation/mutation. Copy only small offset metadata.
	records := make(map[string]RecordMeta, len(s.records))
	for k, v := range s.records {
		records[k] = v
	}
	snap := snapIdentity{path: s.file, knownInfo: s.knownInfo, knownSize: s.knownSize}
	cwd := s.cwd
	hook := s.LoadHook
	filePath := s.file
	s.mu.Unlock()
	defer func() {
		if f != nil {
			f.Close()
		}
	}()
	load := func(e *Entry) (*Entry, error) {
		if m, ok := records[e.ID()]; ok {
			if !snap.unchanged() {
				return nil, externalChangeError(filePath)
			}
			if hook != nil {
				hook(m)
				// The hook may remove or replace the file. Reopen the
				// snapshot: on Windows an open handle pins the file
				// and hides removal from this walk.
				if f != nil {
					f.Close()
					f = nil
				}
			}
			if f == nil {
				var err error
				f, err = s.openSnapshotLocked()
				if err != nil {
					return nil, err
				}
			}
			loaded, err := loadRecord(f, m)
			if err != nil {
				return nil, err
			}
			if !snap.unchanged() {
				return nil, externalChangeError(filePath)
			}
			return loaded, nil
		}
		return e, nil
	}
	home, _ := os.UserHomeDir()
	tools := displayTools(entries)
	provenance := provenanceEntries(path)
	inputs := map[string]string{}
	if all && !emit(TranscriptItem{Kind: KindHistoryBoundary, Text: "Historical messages (display only, not model input)"}) {
		return false, nil
	}
	for _, e := range entries {
		full := e
		needs := e.Kind == "branch_summary" || e.Kind == "custom_message" || e.Kind == "compaction" && all
		if e.Message != nil {
			m := e.Message.Message
			needs = m.User != nil
			if m.Assistant != nil {
				for _, b := range m.Assistant.Content {
					if b.Text != nil {
						needs = true
						break
					}
				}
			}
		}
		// State-only custom data and system bodies are not visible.
		if e.Kind == "custom" || e.Kind == "usage" || e.Kind == "label" || e.Kind == "session_info" || e.CustomMessage != nil && !e.CustomMessage.Display {
			continue
		}
		if e.Message != nil && e.Message.Message.User != nil {
			inputs = map[string]string{}
			if p := provenance[e.ID()]; p != nil {
				loaded, loadErr := load(p)
				if loadErr != nil {
					return false, loadErr
				}
				inputs = inputProvenance([]*Entry{loaded})
				if inputs[e.ID()] != "" {
					needs = false
				}
			}
		}
		if needs {
			full, err = load(e)
			if err != nil {
				return false, err
			}
		}
		addToolSummaries(tools, full, cwd, home)
		for _, item := range timelineEntry(full, tools, inputs, all) {
			if !emit(item) {
				return false, nil
			}
		}
		clearToolSummaries(tools, full)
	}
	if f != nil && !snap.unchanged() {
		return false, externalChangeError(filePath)
	}
	return true, nil
}

func (s *Store) Recall() ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	path, err := BuildPath(s.entries, s.leafID)
	if err != nil {
		return nil, err
	}
	provenance := provenanceEntries(path)
	var out []string
	bytes := 0
	for i := len(path) - 1; i >= 0 && len(out) < 200; i-- {
		e := path[i]
		if e.Message == nil || e.Message.Message.User == nil {
			continue
		}
		text := ""
		if p := provenance[e.ID()]; p != nil {
			loaded, err := s.loadEntriesLocked([]*Entry{p})
			if err != nil {
				return nil, err
			}
			text = inputProvenance(loaded)[e.ID()]
		}
		if text == "" {
			loaded, err := s.loadEntriesLocked([]*Entry{e})
			if err != nil {
				return nil, err
			}
			text = timelineUserText(loaded[0].Message.Message.User.Content, nil)
		}
		if strings.TrimSpace(text) == "" {
			continue
		}
		// Imported prompts can exceed the editor budget. Keep the latest usable
		// prompts without retaining unbounded image or envelope payloads.
		if len(text) > 4*1024*1024-bytes {
			if len(out) > 0 {
				break
			}
			continue
		}
		out = append(out, strings.Clone(text))
		bytes += len(text)
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out, nil
}
