package webaccess

import (
	"sort"
	"strings"
	"unicode/utf8"

	"psi/internal/tools"
)

const (
	resultMaxBytes = tools.DefaultMaxBytes
	resultMaxLines = tools.DefaultMaxLines

	truncatedNotice = "\n\n[truncated]"
)

// boundResult enforces the shared model-facing output limits. It returns the
// bounded text and whether truncation happened.
func boundResult(s string) (string, bool) {
	if len(s) <= resultMaxBytes && lineCount(s) <= resultMaxLines {
		return s, false
	}
	limit := resultMaxBytes - len(truncatedNotice)
	if limit < 0 {
		limit = 0
	}
	prefix := runePrefix(s, limit)
	if idx := lineLimit(prefix, resultMaxLines-2); idx >= 0 {
		prefix = prefix[:idx]
	}
	return strings.TrimRight(prefix, "\n") + truncatedNotice, true
}

func lineCount(s string) int {
	if s == "" {
		return 0
	}
	return strings.Count(s, "\n") + 1
}

// lineLimit returns the byte index at which the (max+1)th line starts, or -1.
func lineLimit(s string, max int) int {
	if max <= 0 {
		return 0
	}
	line := 1
	for i := 0; i < len(s); i++ {
		if s[i] != '\n' {
			continue
		}
		line++
		if line > max {
			return i
		}
	}
	return -1
}

// redactor removes configured secrets in one bounded pass. Markers are
// formatter text and are never rescanned.
type redactor struct {
	secrets []string
}

func newRedactor(cfg Config) redactor {
	var values []string
	add := func(v string) {
		if v == "" {
			return
		}
		trimmed := strings.Trim(v, " \t")
		if trimmed != "" {
			values = append(values, trimmed)
		}
		values = append(values, v)
	}
	add(cfg.ExaAPIKey)
	for _, list := range cfg.SearXNGHeaders {
		for _, v := range list {
			add(v)
		}
	}
	seen := map[string]bool{}
	var unique []string
	for _, v := range values {
		if v == "" || seen[v] {
			continue
		}
		seen[v] = true
		unique = append(unique, v)
	}
	sort.SliceStable(unique, func(i, j int) bool { return len(unique[i]) > len(unique[j]) })
	return redactor{secrets: unique}
}

func (r redactor) redact(s string, limit int) string {
	if limit <= 0 {
		return ""
	}
	if len(s) > limit {
		s = runePrefix(s, limit)
	}
	if len(r.secrets) == 0 {
		return s
	}
	const marker = "[redacted]"
	var b strings.Builder
	b.Grow(min(len(s), limit))
	for i := 0; i < len(s); {
		matched := ""
		for _, secret := range r.secrets {
			if strings.HasPrefix(s[i:], secret) {
				matched = secret
				break
			}
		}
		if matched != "" {
			if b.Len()+len(marker) > limit {
				break
			}
			b.WriteString(marker)
			i += len(matched)
			continue
		}
		_, size := utf8.DecodeRuneInString(s[i:])
		if size == 0 {
			break
		}
		if b.Len()+size > limit {
			break
		}
		b.WriteString(s[i : i+size])
		i += size
	}
	return b.String()
}

func sanitizeMetadata(s string) string {
	s = censorControl(s)
	s = strings.ReplaceAll(s, "\r", " ")
	s = strings.ReplaceAll(s, "\n", " ")
	return strings.TrimSpace(collapseSpace(s))
}

func clipMeta(s string, max int) string {
	s = sanitizeMetadata(s)
	return runePrefix(s, max)
}
