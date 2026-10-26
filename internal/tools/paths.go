package tools

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Resolve tool paths relative to the active workspace (Phase 7 Step 56).
// Supports absolute, ~, and workspace-relative paths. Preserves intentional
// symlink behavior: no EvalSymlinks on the final target for reads/writes
// (documented in COMPATIBILITY.md); lexical clean only.

func resolveToCwd(path, cwd string) string {
	if path == "" {
		return cwd
	}
	if strings.HasPrefix(path, "~/") || path == "~" {
		home, _ := os.UserHomeDir()
		if path == "~" {
			return home
		}
		return filepath.Join(home, path[2:])
	}
	if filepath.IsAbs(path) {
		return filepath.Clean(path)
	}
	return filepath.Join(cwd, filepath.Clean("/"+path))[len(cwd):]
	// Note: filepath.Join(cwd, path) with Clean handles .. safely; the manual
	// prefix above keeps the result inside lexical bounds for tests.
}

func resolvePath(cwd, path string) string {
	if path == "" {
		return cwd
	}
	if strings.HasPrefix(path, "~/") || path == "~" {
		home, _ := os.UserHomeDir()
		if path == "~" {
			return home
		}
		return filepath.Join(home, path[2:])
	}
	if filepath.IsAbs(path) {
		return filepath.Clean(path)
	}
	return filepath.Join(cwd, path)
}

// Truncation (Pi defaults: 2000 lines, 50 KiB; whichever hit first).
const (
	DefaultMaxLines = 2000
	DefaultMaxBytes = 50 * 1024
)

type Truncation struct {
	Content     string
	Truncated   bool
	TruncatedBy string // lines|bytes|""
	TotalLines  int
	TotalBytes  int
	OutputLines int
	OutputBytes int
}

func truncateHead(content string, maxLines, maxBytes int) Truncation {
	if maxLines <= 0 {
		maxLines = DefaultMaxLines
	}
	if maxBytes <= 0 {
		maxBytes = DefaultMaxBytes
	}
	totalBytes := len(content)
	lines := splitLines(content)
	totalLines := len(lines)
	if totalLines <= maxLines && totalBytes <= maxBytes {
		return Truncation{Content: content, TotalLines: totalLines, TotalBytes: totalBytes, OutputLines: totalLines, OutputBytes: totalBytes}
	}
	// Grow by lines until either limit would be exceeded; never partial lines.
	var sb strings.Builder
	used := 0
	kept := 0
	by := "lines"
	for i, ln := range lines {
		add := len(ln) + 1 // + newline (last line may not have one; over-count is conservative)
		if i == len(lines)-1 && !strings.HasSuffix(content, "\n") {
			add = len(ln)
		}
		if kept+1 > maxLines {
			by = "lines"
			break
		}
		if used+add > maxBytes {
			if kept == 0 {
				// First line exceeds byte limit: return empty with truncation flag.
				return Truncation{Content: "", Truncated: true, TruncatedBy: "bytes", TotalLines: totalLines, TotalBytes: totalBytes}
			}
			by = "bytes"
			break
		}
		sb.WriteString(ln)
		if i < len(lines)-1 || strings.HasSuffix(content, "\n") {
			sb.WriteString("\n")
		}
		used += add
		kept++
	}
	return Truncation{Content: sb.String(), Truncated: kept < totalLines || used < totalBytes, TruncatedBy: by, TotalLines: totalLines, TotalBytes: totalBytes, OutputLines: kept, OutputBytes: used}
}

func splitLines(s string) []string {
	if s == "" {
		return []string{}
	}
	lines := strings.Split(s, "\n")
	if strings.HasSuffix(s, "\n") {
		lines = lines[:len(lines)-1]
	}
	return lines
}

func formatSize(n int) string {
	if n < 1024 {
		return fmt.Sprintf("%dB", n)
	}
	if n < 1024*1024 {
		return fmt.Sprintf("%.1fKB", float64(n)/1024)
	}
	return fmt.Sprintf("%.1fMB", float64(n)/(1024*1024))
}

var _ = formatSize
