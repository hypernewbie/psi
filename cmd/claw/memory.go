package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	maxMemoryFiles      = 200
	maxMemoryFileBytes  = 1 << 20
	maxMemoryTotalBytes = 8 << 20
	maxMemoryChunkLines = 20
	maxMemorySnippet    = 400
	maxMemoryGetBytes   = 50 << 10
)

type memoryIndex struct {
	files []memoryFile
}

type memoryFile struct {
	path  string
	lines []string
}

type memoryHit struct {
	Path      string  `json:"path"`
	StartLine int     `json:"startLine"`
	EndLine   int     `json:"endLine"`
	Score     float64 `json:"score"`
	Snippet   string  `json:"snippet"`
}

// loadMemory indexes only the documented Markdown sources. Paths are relative
// to the pinned workspace and every read stays inside it.
func loadMemory(workspace string) (*memoryIndex, error) {
	root, err := os.OpenRoot(workspace)
	if err != nil {
		return nil, fmt.Errorf("memory: %w", err)
	}
	defer root.Close()
	index := &memoryIndex{}
	total := 0
	add := func(name string) error {
		if len(index.files) >= maxMemoryFiles {
			return nil
		}
		file, err := root.Open(name)
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		info, err := file.Stat()
		if err != nil {
			file.Close()
			return err
		}
		if !info.Mode().IsRegular() {
			file.Close()
			return nil
		}
		data, err := io.ReadAll(io.LimitReader(file, maxMemoryFileBytes+1))
		file.Close()
		if err != nil {
			return err
		}
		if len(data) > maxMemoryFileBytes || total+len(data) > maxMemoryTotalBytes {
			return nil
		}
		total += len(data)
		index.files = append(index.files, memoryFile{path: name, lines: strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n")})
		return nil
	}
	if err := add("MEMORY.md"); err != nil {
		return nil, fmt.Errorf("memory: %w", err)
	}
	if err := add("USER.md"); err != nil {
		return nil, fmt.Errorf("memory: %w", err)
	}
	if err := walkMemory(root, "memory", 0, add); err != nil {
		return nil, fmt.Errorf("memory: %w", err)
	}
	return index, nil
}

func walkMemory(root *os.Root, dir string, depth int, add func(string) error) error {
	if depth > 4 {
		return nil
	}
	handle, err := root.Open(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	entries, err := handle.ReadDir(-1)
	handle.Close()
	if err != nil {
		return err
	}
	for _, entry := range entries {
		name := path.Join(dir, entry.Name())
		if entry.IsDir() {
			if err := walkMemory(root, name, depth+1, add); err != nil {
				return err
			}
			continue
		}
		if !entry.Type().IsRegular() || !strings.HasSuffix(strings.ToLower(entry.Name()), ".md") {
			continue
		}
		if err := add(name); err != nil {
			return err
		}
	}
	return nil
}

// search requires every query term and ranks chunks by total occurrences.
func (m *memoryIndex) search(query string, maxResults int, minScore float64) []memoryHit {
	terms := queryTerms(query)
	if len(terms) == 0 {
		return nil
	}
	var hits []memoryHit
	for _, file := range m.files {
		for start := 0; start < len(file.lines); start += maxMemoryChunkLines {
			end := min(start+maxMemoryChunkLines, len(file.lines))
			body := strings.Join(file.lines[start:end], "\n")
			lower := strings.ToLower(body)
			score := 0
			matches := true
			for _, term := range terms {
				count := strings.Count(lower, term)
				if count == 0 {
					matches = false
					break
				}
				score += count
			}
			if !matches || float64(score) < minScore {
				continue
			}
			hits = append(hits, memoryHit{
				Path:      file.path,
				StartLine: start + 1,
				EndLine:   end,
				Score:     float64(score),
				Snippet:   snippet(body),
			})
		}
	}
	sort.SliceStable(hits, func(i, j int) bool {
		if hits[i].Score != hits[j].Score {
			return hits[i].Score > hits[j].Score
		}
		if hits[i].Path != hits[j].Path {
			return hits[i].Path < hits[j].Path
		}
		return hits[i].StartLine < hits[j].StartLine
	})
	if maxResults > 0 && len(hits) > maxResults {
		hits = hits[:maxResults]
	}
	return hits
}

func (m *memoryIndex) get(name string, from, lines int) (string, error) {
	name = path.Clean(strings.ReplaceAll(name, "\\", "/"))
	for _, file := range m.files {
		if file.path != name {
			continue
		}
		if from < 1 {
			from = 1
		}
		if from > len(file.lines) {
			return "", fmt.Errorf("line %d is past the end of %s", from, name)
		}
		end := len(file.lines)
		if lines > 0 {
			end = min(from-1+lines, end)
		}
		return truncateWithNote(strings.Join(file.lines[from-1:end], "\n"), maxMemoryGetBytes), nil
	}
	return "", fmt.Errorf("%s is not an indexed memory file", name)
}

func queryTerms(query string) []string {
	seen := map[string]bool{}
	var terms []string
	var current strings.Builder
	flush := func() {
		term := strings.ToLower(current.String())
		current.Reset()
		if term == "" || seen[term] {
			return
		}
		seen[term] = true
		terms = append(terms, term)
	}
	for _, r := range query {
		if unicode.IsLetter(r) || unicode.IsNumber(r) || r == '_' {
			current.WriteRune(r)
			continue
		}
		flush()
	}
	flush()
	return terms
}

func snippet(body string) string {
	body = strings.TrimSpace(body)
	if utf8.RuneCountInString(body) <= maxMemorySnippet {
		return body
	}
	runes := []rune(body)
	return string(runes[:maxMemorySnippet]) + "…"
}

func memorySearchResult(hits []memoryHit) string {
	raw, err := json.Marshal(map[string]any{"results": hits})
	if err != nil {
		return `{"results":[]}`
	}
	return string(raw)
}
