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
	return s.pathLocked()
}

// pathLocked walks the active branch through the already-maintained byID map.
func (s *Store) pathLocked() ([]*Entry, error) {
	return buildPathByID(s.byID, s.entries, s.leafID)
}
func (s *Store) EntryCount() int { s.mu.Lock(); defer s.mu.Unlock(); return len(s.entries) }

// Timeline visits one visible record at a time. Hidden tool bodies and reasoning
// never enter the display working set. n==0 means the entire selected view.
func (s *Store) Timeline(all bool, n int, emit func(TranscriptItem) bool) (bool, error) {
	s.mu.Lock()
	path, err := s.pathLocked()
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
	cwd := s.cwd
	s.mu.Unlock()
	home, _ := os.UserHomeDir()
	tools := displayTools(entries)
	provenance := provenanceEntries(path)
	inputs := map[string]string{}
	if all && !emit(TranscriptItem{Kind: KindHistoryBoundary, Text: "Historical messages (display only, not model input)"}) {
		return false, nil
	}
	for _, e := range entries {
		full := e
		// State-only custom data and system bodies are not visible.
		if e.Kind == "custom" || e.Kind == "usage" || e.Kind == "label" || e.Kind == "session_info" || e.CustomMessage != nil && !e.CustomMessage.Display {
			continue
		}
		if e.Message != nil && e.Message.Message.User != nil {
			inputs = map[string]string{}
			if p := provenance[e.ID()]; p != nil {
				inputs = inputProvenance([]*Entry{p})
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
	return true, nil
}

func (s *Store) Recall() ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	path, err := s.pathLocked()
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
			text = inputProvenance([]*Entry{p})[e.ID()]
		}
		if text == "" {
			text = timelineUserText(e.Message.Message.User.Content, nil)
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
