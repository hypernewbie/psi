package tools

import (
	"bytes"
	"encoding/json"
	"math"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"psi/internal/event"
	"psi/internal/terminal"
)

const displaySummaryBytes = 512

var (
	secretAssignment = regexp.MustCompile(`(?i)\b([a-z0-9_]*(api[-_]?key|access[-_]?token|refresh[-_]?token|token|password|secret|authorization)[a-z0-9_]*)\s*=\s*("[^"]*"?|'[^']*'?|[^\s;,]+)`)
	secretFlag       = regexp.MustCompile(`(?i)(--(api[-_]?key|token|password|secret|authorization)\s*(=|\s+))("[^"]*"?|'[^']*'?|[^\s;,]+)`)
	urlCredentials   = regexp.MustCompile(`(?i)(https?://)[^/@\s]+@`)
	authorization    = regexp.MustCompile(`(?i)(authorization\s*:\s*)((?:bearer|basic)\s+)?[^\s,;]+`)
)

// SummarizeArguments extracts only the display fields for psi's four tools.
// It never copies hidden write/edit bodies into an event or summary cache.
func SummarizeArguments(name string, args map[string]any, cwd, home string) event.ToolSummary {
	out := event.ToolSummary{Kind: name, Available: true}
	switch name {
	case "read", "write", "edit", "bash":
	default:
		out.Available = false
		return out
	}
	if args == nil {
		out.Available = false
		return out
	}
	if name == "bash" {
		if command, ok := args["command"].(string); ok {
			out.Command, out.Truncated = summarizeCommand(command, false)
		}
		return out
	}
	path, _ := args["path"].(string)
	if path == "" {
		// Legacy Pi calls can use file_path. This is presentation-only: tool
		// execution still validates its existing path argument unchanged.
		path, _ = args["file_path"].(string)
	}
	if path != "" {
		displayed := displayPath(path, cwd, home)
		out.Path, out.Truncated = boundDisplay(displayed)
	}
	if name == "read" {
		offset, offsetOK := numberValue(args["offset"])
		limit, limitOK := numberValue(args["limit"])
		offsetPresent := args["offset"] != nil
		limitPresent := args["limit"] != nil
		if offsetPresent && !offsetOK || limitPresent && !limitOK {
			return out
		}
		if offsetOK || limitOK {
			start := 1.0
			if offsetOK {
				start = offset
			}
			if math.IsNaN(start) || math.IsInf(start, 0) {
				return out
			}
			if limitOK {
				if math.IsNaN(limit) || math.IsInf(limit, 0) {
					return out
				}
				end := start + limit - 1
				out.LineRange = ":" + numberString(start)
				if end != 0 {
					out.LineRange += "-" + numberString(end)
				}
			} else {
				out.LineRange = ":" + numberString(start)
			}
		}
		if len(out.LineRange) > 128 {
			out.LineRange, _ = boundSuffix(out.LineRange, 128)
			out.Truncated = true
		}
		pathBudget := max(0, displaySummaryBytes-len(out.LineRange))
		if len(out.Path) > pathBudget {
			out.Path, _ = boundSuffix(out.Path, pathBudget)
			out.Truncated = true
		}
	}
	return out
}

// SummarizeArgumentsJSON scans an already-serialized invocation without
// decoding or retaining write/edit bodies. Only allowlisted JSON members are
// materialized, and oversized strings are clipped while they are decoded.
func SummarizeArgumentsJSON(name string, data []byte, cwd, home string) event.ToolSummary {
	out := event.ToolSummary{Kind: name, Available: true}
	switch name {
	case "read", "write", "edit", "bash":
	default:
		out.Available = false
		return out
	}
	fields := []string{"path", "file_path"}
	if name == "bash" {
		fields = []string{"command"}
	} else if name == "read" {
		fields = append(fields, "offset", "limit")
	}
	selected, ok := selectedJSONFields(data, fields)
	if !ok {
		out.Available = false
		return out
	}
	if name == "bash" {
		command, truncated, valid := decodeJSONStringPrefix(selected["command"], 4096)
		if valid {
			out.Command, out.Truncated = summarizeCommand(command, truncated)
		}
		return out
	}
	args := make(map[string]any, 3)
	pathTruncated := false
	pathRaw := selected["path"]
	if len(pathRaw) < 2 || pathRaw[0] != '"' || bytes.Equal(pathRaw, []byte(`""`)) {
		pathRaw = selected["file_path"]
	}
	if raw := pathRaw; len(raw) > 0 && !bytes.Equal(raw, []byte("null")) {
		var path string
		var truncated, valid bool
		if len(raw) > 4098 {
			path, truncated, valid = decodeJSONStringSuffix(raw, displaySummaryBytes-3)
			if truncated {
				path = "…" + path
			}
		} else {
			path, truncated, valid = decodeJSONStringPrefix(raw, 4096)
		}
		if valid {
			args["path"] = path
			pathTruncated = truncated
		}
	}
	if name == "read" {
		for _, key := range []string{"offset", "limit"} {
			raw := selected[key]
			if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
				continue
			}
			if value, valid := jsonNumber(raw); valid {
				args[key] = value
			} else {
				args[key] = struct{}{}
			}
		}
	}
	out = SummarizeArguments(name, args, cwd, home)
	out.Truncated = out.Truncated || pathTruncated
	return out
}

// RebaseSummaryPath applies invocation-specific workspace/home shortening after
// a disk index has extracted a bounded path without knowing the session cwd.
func RebaseSummaryPath(summary event.ToolSummary, cwd, home string) event.ToolSummary {
	if summary.Path == "" || summary.Truncated {
		return summary
	}
	displayed := displayPath(summary.Path, cwd, home)
	summary.Path, summary.Truncated = boundDisplay(displayed)
	budget := max(0, displaySummaryBytes-len(summary.LineRange))
	if len(summary.Path) > budget {
		summary.Path, _ = boundSuffix(summary.Path, budget)
		summary.Truncated = true
	}
	return summary
}

func selectedJSONFields(data []byte, wanted []string) (map[string][]byte, bool) {
	if !json.Valid(data) {
		return nil, false
	}
	want := make(map[string]bool, len(wanted))
	for _, key := range wanted {
		want[key] = true
	}
	i := skipJSONSpace(data, 0)
	if i >= len(data) || data[i] != '{' {
		return nil, false
	}
	i++
	out := make(map[string][]byte, len(wanted))
	for {
		i = skipJSONSpace(data, i)
		if i >= len(data) {
			return nil, false
		}
		if data[i] == '}' {
			return out, true
		}
		keyStart := i
		keyEnd, ok := scanJSONString(data, i)
		if !ok {
			return nil, false
		}
		var key string
		if keyEnd-keyStart <= 128 {
			if json.Unmarshal(data[keyStart:keyEnd], &key) != nil {
				return nil, false
			}
		}
		i = skipJSONSpace(data, keyEnd)
		if i >= len(data) || data[i] != ':' {
			return nil, false
		}
		i = skipJSONSpace(data, i+1)
		end, ok := scanJSONValue(data, i, 0)
		if !ok {
			return nil, false
		}
		if want[key] {
			out[key] = data[i:end]
		}
		i = skipJSONSpace(data, end)
		if i >= len(data) {
			return nil, false
		}
		if data[i] == ',' {
			i++
			continue
		}
		if data[i] == '}' {
			return out, true
		}
		return nil, false
	}
}

func scanJSONValue(data []byte, i, depth int) (int, bool) {
	if depth > 64 || i >= len(data) {
		return 0, false
	}
	switch data[i] {
	case '"':
		return scanJSONString(data, i)
	case '{':
		i++
		for {
			i = skipJSONSpace(data, i)
			if i >= len(data) {
				return 0, false
			}
			if data[i] == '}' {
				return i + 1, true
			}
			end, ok := scanJSONString(data, i)
			if !ok {
				return 0, false
			}
			i = skipJSONSpace(data, end)
			if i >= len(data) || data[i] != ':' {
				return 0, false
			}
			i = skipJSONSpace(data, i+1)
			end, ok = scanJSONValue(data, i, depth+1)
			if !ok {
				return 0, false
			}
			i = skipJSONSpace(data, end)
			if i < len(data) && data[i] == ',' {
				i++
				continue
			}
			if i < len(data) && data[i] == '}' {
				return i + 1, true
			}
			return 0, false
		}
	case '[':
		i++
		for {
			i = skipJSONSpace(data, i)
			if i >= len(data) {
				return 0, false
			}
			if data[i] == ']' {
				return i + 1, true
			}
			end, ok := scanJSONValue(data, i, depth+1)
			if !ok {
				return 0, false
			}
			i = skipJSONSpace(data, end)
			if i < len(data) && data[i] == ',' {
				i++
				continue
			}
			if i < len(data) && data[i] == ']' {
				return i + 1, true
			}
			return 0, false
		}
	default:
		for i < len(data) && data[i] != ',' && data[i] != '}' && data[i] != ']' && data[i] != ' ' && data[i] != '\n' && data[i] != '\r' && data[i] != '\t' {
			i++
		}
		return i, i > 0
	}
}

func scanJSONString(data []byte, i int) (int, bool) {
	if i >= len(data) || data[i] != '"' {
		return 0, false
	}
	for i++; i < len(data); i++ {
		switch data[i] {
		case '\\':
			i++
		case '"':
			return i + 1, true
		}
	}
	return 0, false
}

func skipJSONSpace(data []byte, i int) int {
	for i < len(data) {
		switch data[i] {
		case ' ', '\n', '\r', '\t':
			i++
		default:
			return i
		}
	}
	return i
}

func decodeJSONStringSuffix(raw []byte, limit int) (string, bool, bool) {
	if len(raw) < 2 || raw[0] != '"' || limit <= 0 {
		return "", false, false
	}
	out := make([]byte, 0, limit+utf8.UTFMax)
	truncated := false
	for i := 1; i < len(raw)-1; {
		var piece []byte
		if raw[i] != '\\' {
			_, n := utf8.DecodeRune(raw[i:])
			if n == 0 || n == 1 && raw[i] >= utf8.RuneSelf {
				return "", false, false
			}
			piece = raw[i : i+n]
			i += n
		} else {
			i++
			if i >= len(raw)-1 {
				return "", false, false
			}
			switch raw[i] {
			case '"', '\\', '/':
				piece = []byte{raw[i]}
				i++
			case 'b':
				piece, i = []byte{'\b'}, i+1
			case 'f':
				piece, i = []byte{'\f'}, i+1
			case 'n':
				piece, i = []byte{'\n'}, i+1
			case 'r':
				piece, i = []byte{'\r'}, i+1
			case 't':
				piece, i = []byte{'\t'}, i+1
			case 'u':
				if i+5 > len(raw) {
					return "", false, false
				}
				r1, ok := parseHexRune(raw[i+1 : i+5])
				if !ok {
					return "", false, false
				}
				i += 5
				if utf16IsHigh(r1) && i+6 <= len(raw) && raw[i] == '\\' && raw[i+1] == 'u' {
					r2, ok := parseHexRune(raw[i+2 : i+6])
					if ok && utf16IsLow(r2) {
						r1 = utf16Decode(r1, r2)
						i += 6
					}
				}
				var encoded [utf8.UTFMax]byte
				n := utf8.EncodeRune(encoded[:], r1)
				piece = encoded[:n]
			default:
				return "", false, false
			}
		}
		out = append(out, piece...)
		if len(out) > limit+utf8.UTFMax {
			trim := len(out) - limit
			for trim < len(out) && !utf8.RuneStart(out[trim]) {
				trim++
			}
			out = append(out[:0], out[trim:]...)
			truncated = true
		}
	}
	if len(out) > limit {
		trim := len(out) - limit
		for trim < len(out) && !utf8.RuneStart(out[trim]) {
			trim++
		}
		out = out[trim:]
		truncated = true
	}
	return string(out), truncated, true
}

func decodeJSONString(raw []byte) (string, bool) {
	if len(raw) == 0 || raw[0] != '"' {
		return "", false
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", false
	}
	return value, true
}

// decodeJSONStringPrefix decodes only a bounded prefix of a JSON string. Large
// bash commands therefore do not create a second copy of the whole argument.
func decodeJSONStringPrefix(raw []byte, limit int) (string, bool, bool) {
	if len(raw) < 2 || raw[0] != '"' {
		return "", false, false
	}
	out := make([]byte, 0, limit)
	for i := 1; i < len(raw)-1; {
		var piece []byte
		if raw[i] != '\\' {
			_, n := utf8.DecodeRune(raw[i:])
			if n == 0 || n == 1 && raw[i] >= utf8.RuneSelf {
				return "", false, false
			}
			piece = raw[i : i+n]
			i += n
		} else {
			i++
			if i >= len(raw)-1 {
				return "", false, false
			}
			switch raw[i] {
			case '"', '\\', '/':
				piece = []byte{raw[i]}
				i++
			case 'b':
				piece, i = []byte{'\b'}, i+1
			case 'f':
				piece, i = []byte{'\f'}, i+1
			case 'n':
				piece, i = []byte{'\n'}, i+1
			case 'r':
				piece, i = []byte{'\r'}, i+1
			case 't':
				piece, i = []byte{'\t'}, i+1
			case 'u':
				if i+5 > len(raw) {
					return "", false, false
				}
				r1, ok := parseHexRune(raw[i+1 : i+5])
				if !ok {
					return "", false, false
				}
				i += 5
				if utf16IsHigh(r1) && i+6 <= len(raw) && raw[i] == '\\' && raw[i+1] == 'u' {
					r2, ok := parseHexRune(raw[i+2 : i+6])
					if ok && utf16IsLow(r2) {
						r1 = utf16Decode(r1, r2)
						i += 6
					}
				}
				var encoded [utf8.UTFMax]byte
				n := utf8.EncodeRune(encoded[:], r1)
				piece = encoded[:n]
			default:
				return "", false, false
			}
		}
		if len(out)+len(piece) > limit {
			return string(out), true, true
		}
		out = append(out, piece...)
	}
	return string(out), false, true
}

func parseHexRune(raw []byte) (rune, bool) {
	if len(raw) != 4 {
		return 0, false
	}
	value, err := strconv.ParseUint(string(raw), 16, 16)
	return rune(value), err == nil
}

func utf16IsHigh(r rune) bool { return r >= 0xd800 && r <= 0xdbff }
func utf16IsLow(r rune) bool  { return r >= 0xdc00 && r <= 0xdfff }
func utf16Decode(high, low rune) rune {
	return 0x10000 + (high-0xd800)<<10 + (low - 0xdc00)
}

func jsonNumber(raw []byte) (float64, bool) {
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return 0, false
	}
	if len(raw) > 128 {
		return 0, false
	}
	if raw[0] == '"' {
		if len(raw) > 128 {
			return 0, false
		}
		value, ok := decodeJSONString(raw)
		if !ok {
			return 0, false
		}
		f, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
		return f, err == nil
	}
	f, err := strconv.ParseFloat(string(raw), 64)
	return f, err == nil
}

func numberString(value float64) string { return strconv.FormatFloat(value, 'f', -1, 64) }

func numberValue(value any) (float64, bool) {
	switch n := value.(type) {
	case json.Number:
		f, err := n.Float64()
		return f, err == nil
	case float64:
		return n, true
	case float32:
		return float64(n), true
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	case string:
		f, err := strconv.ParseFloat(strings.TrimSpace(n), 64)
		return f, err == nil
	default:
		return 0, false
	}
}

func displayPath(path, cwd, home string) string {
	path = terminal.SanitizeStatusRow(path)
	if path == "" {
		return ""
	}
	clean := filepath.Clean(path)
	if filepath.IsAbs(clean) {
		if cwd != "" {
			base, err := filepath.Abs(cwd)
			if err == nil {
				if rel, err := filepath.Rel(filepath.Clean(base), clean); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
					return rel
				}
			}
		}
		if home != "" {
			base, err := filepath.Abs(home)
			if err == nil {
				if rel, err := filepath.Rel(filepath.Clean(base), clean); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
					return filepath.Join("~", rel)
				}
			}
		}
	}
	return clean
}

func boundDisplay(text string) (string, bool) {
	return boundSuffix(terminal.SanitizeStatusRow(text), displaySummaryBytes)
}

func boundSuffix(text string, maxBytes int) (string, bool) {
	if maxBytes <= 0 {
		return "", text != ""
	}
	if len(text) <= maxBytes {
		return strings.Clone(text), false
	}
	const ellipsis = "…"
	if maxBytes <= len(ellipsis) {
		return ellipsis[:maxBytes], true
	}
	tail := text[len(text)-maxBytes+len(ellipsis):]
	for !utf8.ValidString(tail) && len(tail) > 0 {
		tail = tail[1:]
	}
	return ellipsis + strings.Clone(tail), true
}

func summarizeCommand(command string, sourceTruncated bool) (string, bool) {
	const scanLimit = 4096
	if len(command) > scanLimit {
		command = command[:scanLimit]
		for !utf8.ValidString(command) {
			command = command[:len(command)-1]
		}
		sourceTruncated = true
	}
	command = strings.ReplaceAll(command, "\r", "\n")
	lines := strings.Split(command, "\n")
	firstIndex, first := -1, ""
	for i, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		firstIndex = i
		first = strings.TrimSpace(strings.ReplaceAll(line, "\t", "   "))
		break
	}
	if firstIndex < 0 {
		return "", false
	}
	first = redactCommand(first)
	more := sourceTruncated
	for _, line := range lines[firstIndex+1:] {
		if strings.TrimSpace(line) != "" {
			more = true
			break
		}
	}
	if more {
		first += " …"
	}
	first = terminal.SanitizeStatusRow(first)
	bounded, clipped := boundCommandPreview(first)
	return bounded, clipped || more
}

func boundCommandPreview(text string) (string, bool) {
	if len(text) <= displaySummaryBytes {
		return strings.Clone(text), false
	}
	const marker = "…"
	prefix := text[:displaySummaryBytes-len(marker)]
	for !utf8.ValidString(prefix) {
		prefix = prefix[:len(prefix)-1]
	}
	return strings.Clone(prefix) + marker, true
}

func redactCommand(command string) string {
	command = secretAssignment.ReplaceAllString(command, "$1=[REDACTED]")
	command = secretFlag.ReplaceAllString(command, "$1[REDACTED]")
	command = urlCredentials.ReplaceAllString(command, "$1[REDACTED]@")
	command = authorization.ReplaceAllString(command, "$1[REDACTED]")
	return command
}

// SetExitCode adds only a small numeric result detail to the already bounded
// display summary.
func SetExitCode(summary *event.ToolSummary, details any) {
	m, ok := details.(map[string]any)
	if !ok {
		return
	}
	value, ok := m["exitCode"]
	if !ok {
		return
	}
	var code int
	switch n := value.(type) {
	case int:
		code = n
	case int32:
		code = int(n)
	case int64:
		code = int(n)
	case float64:
		if math.IsNaN(n) || math.IsInf(n, 0) || math.Trunc(n) != n || n < math.MinInt32 || n > math.MaxInt32 {
			return
		}
		code = int(n)
	default:
		return
	}
	summary.ExitCode = &code
}
