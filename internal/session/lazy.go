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
func (s *Store) loadEntriesLocked(entries []*Entry) ([]*Entry, error) {
	var f *os.File
	defer func() {
		if f != nil {
			f.Close()
		}
	}()
	out := make([]*Entry, 0, len(entries))
	for _, e := range entries {
		if meta, ok := s.records[e.ID()]; ok {
			if f == nil {
				var err error
				f, err = s.openSnapshotLocked()
				if err != nil {
					return nil, err
				}
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
	s.mu.Unlock()
	if f != nil {
		defer f.Close()
	}
	load := func(e *Entry) (*Entry, error) {
		if m, ok := records[e.ID()]; ok {
			return loadRecord(f, m)
		}
		return e, nil
	}
	tools := displayTools(entries)
	provenance := provenanceEntries(path)
	inputs := map[string]string{}
	if all && !emit(TranscriptItem{Kind: KindHistoryBoundary, Text: "Historical messages (display only, not model input)"}) {
		return false, nil
	}
	for _, e := range entries {
		full := e
		needs := e.Kind == "compaction" || e.Kind == "branch_summary" || e.Kind == "custom_message"
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
		for _, item := range timelineEntry(full, tools, inputs, all) {
			if !emit(item) {
				return false, nil
			}
		}
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
