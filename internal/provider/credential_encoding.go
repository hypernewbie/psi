package provider

import (
	"errors"
	"unicode/utf8"
)

var errCredentialTooLarge = errors.New("API key is too large for auth.json")

// encodeCredentialLiteral returns a complete JSON string for a Pi-compatible
// literal credential. It applies Pi's literal escaping before JSON escaping:
// a leading '!' becomes "$!" and every '$' becomes "$$".
//
// The sizing pass checks every expansion against maxAuthJSONSize before the
// output buffer is allocated. This prevents integer wraparound and an
// unbounded allocation from an oversized key.
func encodeCredentialLiteral(raw []byte) ([]byte, error) {
	if !utf8.Valid(raw) {
		return nil, errors.New("API key is invalid UTF-8")
	}
	size := 2 // opening and closing quotes
	for i := 0; i < len(raw); i++ {
		n := jsonCredentialByteSize(raw[i], i == 0)
		if n > maxAuthJSONSize-size {
			return nil, errCredentialTooLarge
		}
		size += n
	}
	out := make([]byte, 0, size)
	out = append(out, '"')
	for i := 0; i < len(raw); i++ {
		b := raw[i]
		switch {
		case b == '$':
			out = append(out, '$', '$')
		case b == '!' && i == 0:
			out = append(out, '$', '!')
		case b == '"' || b == '\\':
			out = append(out, '\\', b)
		case b == '\b':
			out = append(out, '\\', 'b')
		case b == '\f':
			out = append(out, '\\', 'f')
		case b == '\n':
			out = append(out, '\\', 'n')
		case b == '\r':
			out = append(out, '\\', 'r')
		case b == '\t':
			out = append(out, '\\', 't')
		case b < 0x20:
			// JSON requires escaping ASCII controls below SPACE (U+0020).
			// Encode the byte as two hex digits; 0x0f selects its low four bits.
			const hex = "0123456789abcdef"
			out = append(out, '\\', 'u', '0', '0', hex[b>>4], hex[b&0x0f])
		default:
			out = append(out, b)
		}
	}
	out = append(out, '"')
	return out, nil
}

func jsonCredentialByteSize(b byte, leading bool) int {
	switch {
	case b == '$':
		return 2
	case b == '!' && leading:
		return 2
	case b == '"', b == '\\', b == '\b', b == '\f', b == '\n', b == '\r', b == '\t':
		return 2
	case b < 0x20:
		return 6
	default:
		return 1
	}
}
