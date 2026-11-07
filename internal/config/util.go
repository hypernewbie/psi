package config

import "bytes"

func stripBOM(b []byte) []byte { return bytes.TrimPrefix(b, []byte("\xef\xbb\xbf")) }

func bytesReader(b []byte) *bytes.Reader { return bytes.NewReader(b) }

// stripJSONComments removes the // line comments and the trailing commas that
// Pi accepts in models.json. String literals stay untouched, so a key or a
// path that contains a slash or a comma keeps its value.
func stripJSONComments(data []byte) []byte {
	withoutComments := make([]byte, 0, len(data))
	inString, escaped := false, false
	for i := 0; i < len(data); i++ {
		c := data[i]
		if inString {
			withoutComments = append(withoutComments, c)
			switch {
			case escaped:
				escaped = false
			case c == '\\':
				escaped = true
			case c == '"':
				inString = false
			}
			continue
		}
		if c == '"' {
			inString = true
			withoutComments = append(withoutComments, c)
			continue
		}
		if c == '/' && i+1 < len(data) && data[i+1] == '/' {
			for i < len(data) && data[i] != '\n' {
				i++
			}
			if i < len(data) {
				withoutComments = append(withoutComments, '\n')
			}
			continue
		}
		withoutComments = append(withoutComments, c)
	}
	out := make([]byte, 0, len(withoutComments))
	inString, escaped = false, false
	for i := 0; i < len(withoutComments); i++ {
		c := withoutComments[i]
		if inString {
			out = append(out, c)
			switch {
			case escaped:
				escaped = false
			case c == '\\':
				escaped = true
			case c == '"':
				inString = false
			}
			continue
		}
		if c == '"' {
			inString = true
			out = append(out, c)
			continue
		}
		if c == ',' {
			next := i + 1
			for next < len(withoutComments) && isJSONSpace(withoutComments[next]) {
				next++
			}
			if next < len(withoutComments) && (withoutComments[next] == '}' || withoutComments[next] == ']') {
				continue
			}
		}
		out = append(out, c)
	}
	return out
}

func isJSONSpace(c byte) bool {
	return c == ' ' || c == '\t' || c == '\n' || c == '\r'
}
