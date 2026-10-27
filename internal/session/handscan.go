package session

// Zero-copy hand scanner (F05/P3-018/S12). One byte pass extracts index
// metadata with exact property names: alias-case keys can never fold
// because keys are compared as raw bytes. Big values are skipped without
// retention or unescaping; only small protocol values are decoded.
// Strictness mirrors UnmarshalEntry leaf-for-leaf; the differential fuzzer
// guards eager/thin equivalence. Malformed JSON is always an error.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"
)

// hscan walks one JSON record without allocating for skipped values.
type hscan struct {
	b   []byte
	i   int
	err error
}

// pairSep consumes the separator after a pair or element: ',' means more
// follow, the matching close bracket ends the container.
func (s *hscan) pairSep(close byte) (more bool, err error) {
	s.skipWS()
	if s.i < len(s.b) && s.b[s.i] == ',' {
		s.i++
		return true, nil
	}
	if s.i < len(s.b) && s.b[s.i] == close {
		s.i++
		return false, nil
	}
	if s.i >= len(s.b) {
		return false, fmt.Errorf("unterminated value")
	}
	return false, fmt.Errorf("invalid value")
}

func (s *hscan) skipWS() {
	for s.i < len(s.b) {
		switch s.b[s.i] {
		case ' ', '\t', '\n', '\r':
			s.i++
		default:
			return
		}
	}
}

func (s *hscan) peek() byte {
	s.skipWS()
	if s.i >= len(s.b) {
		s.err = fmt.Errorf("unexpected end of record")
		return 0
	}
	return s.b[s.i]
}

func (s *hscan) expect(c byte) bool {
	if s.peek() != c {
		s.err = fmt.Errorf("invalid record")
		return false
	}
	s.i++
	return true
}

// rawString scans a JSON string, returning its raw (still-escaped) bytes.
// Escape sequences are validated (like encoding/json, even for skipped
// strings); full unescaping happens only for kept values.
func (s *hscan) rawString() ([]byte, bool) {
	s.skipWS()
	if s.i >= len(s.b) || s.b[s.i] != '"' {
		return nil, false
	}
	start := s.i
	s.i++
	for s.i < len(s.b) {
		c := s.b[s.i]
		if c == '\\' {
			if s.i+1 >= len(s.b) {
				s.err = fmt.Errorf("unterminated escape")
				return nil, false
			}
			e := s.b[s.i+1]
			switch e {
			case '"', '\\', '/', 'b', 'f', 'n', 'r', 't':
				s.i += 2
			case 'u':
				if s.i+5 >= len(s.b) {
					s.err = fmt.Errorf("unterminated escape")
					return nil, false
				}
				for _, h := range s.b[s.i+2 : s.i+6] {
					if !(h >= '0' && h <= '9' || h >= 'a' && h <= 'f' || h >= 'A' && h <= 'F') {
						s.err = fmt.Errorf("invalid escape")
						return nil, false
					}
				}
				s.i += 6
			default:
				s.err = fmt.Errorf("invalid escape")
				return nil, false
			}
			continue
		}
		if c == '"' {
			s.i++
			return s.b[start:s.i], true
		}
		if c < 0x20 {
			s.err = fmt.Errorf("invalid string")
			return nil, false
		}
		s.i++
	}
	s.err = fmt.Errorf("unterminated string")
	return nil, false
}

// scanKey reads an object key and reports whether it equals want by exact
// bytes. Escaped keys take the slow path (unescaped before compare).
func (s *hscan) scanKeyEq(want string) bool {
	s.skipWS()
	if s.i >= len(s.b) || s.b[s.i] != '"' {
		s.err = fmt.Errorf("invalid object key")
		return false
	}
	raw, ok := s.rawString()
	if !ok {
		return false
	}
	inner := raw[1 : len(raw)-1]
	if indexByte(inner, '\\') < 0 {
		return string(inner) == want
	}
	un, err := unescapeJSONString(inner)
	if err != nil {
		s.err = fmt.Errorf("invalid object key")
		return false
	}
	return un == want
}

func indexByte(b []byte, c byte) int {
	for i, v := range b {
		if v == c {
			return i
		}
	}
	return -1
}

// skipValue discards one complete JSON value without retaining it.
func (s *hscan) skipValue() {
	if s.err != nil {
		return
	}
	s.skipWS()
	if s.i >= len(s.b) {
		s.err = fmt.Errorf("unexpected end of record")
		return
	}
	switch c := s.b[s.i]; c {
	case '"':
		if _, ok := s.rawString(); !ok && s.err == nil {
			s.err = fmt.Errorf("invalid string")
		}
	case '{', '[':
		s.i++
		if c == '{' {
			s.skipObject()
		} else {
			s.skipArray()
		}
	default:
		// literal: true/false/null/number — scan to delimiter.
		start := s.i
		for s.i < len(s.b) {
			d := s.b[s.i]
			if d == ',' || d == '}' || d == ']' || d == ' ' || d == '\t' || d == '\n' || d == '\r' {
				break
			}
			s.i++
		}
		lit := string(s.b[start:s.i])
		switch lit {
		case "true", "false", "null":
		default:
			if !validNumber(s.b[start:s.i]) {
				s.err = fmt.Errorf("invalid literal")
			}
		}
	}
}

// getString decodes a required string value (mistyped fails; null is a
// struct no-op and reads as missing).
func (s *hscan) getString() (string, bool) {
	if s.atNull() {
		return "", true
	}
	raw, ok := s.rawString()
	if !ok {
		return "", false
	}
	u, err := unescapeJSONString(raw[1 : len(raw)-1])
	if err != nil {
		s.err = fmt.Errorf("invalid string")
		return "", false
	}
	return u, true
}

// getOptString decodes an optional string (null/missing tolerated upstream).
func (s *hscan) getOptString() (string, bool) {
	s.skipWS()
	if s.i < len(s.b) && s.b[s.i] == 'n' {
		if hasPrefix(s.b[s.i:], "null") {
			s.i += 4
			return "", false
		}
		s.err = fmt.Errorf("invalid value")
		return "", false
	}
	v, ok := s.getString()
	if !ok {
		return "", true
	}
	return v, true
}

// atNull consumes a JSON null and reports it. Struct decoding treats null
// as a silent no-op for every field type, so all strict leaves accept it.
func (s *hscan) atNull() bool {
	s.skipWS()
	if s.i < len(s.b) && s.b[s.i] == 'n' && hasPrefix(s.b[s.i:], "null") {
		s.i += 4
		return true
	}
	return false
}

func hasPrefix(b []byte, p string) bool {
	if len(b) < len(p) {
		return false
	}
	return string(b[:len(p)]) == p
}

// getBool decodes a required bool (mistyped fails; null is a no-op).
func (s *hscan) getBool() (bool, bool) {
	if s.atNull() {
		return false, true
	}
	s.skipWS()
	if hasPrefix(s.b[s.i:], "true") {
		s.i += 4
		return true, true
	}
	if hasPrefix(s.b[s.i:], "false") {
		s.i += 5
		return false, true
	}
	return false, false
}

// getNumberRaw scans a JSON number, returning raw bytes. Null reads as
// absent (nil raw, accepted); callers treat nil as the zero value.
func (s *hscan) getNumberRaw() ([]byte, bool) {
	if s.atNull() {
		return nil, true
	}
	s.skipWS()
	start := s.i
	if s.i < len(s.b) && s.b[s.i] == '-' {
		s.i++
	}
	for s.i < len(s.b) {
		c := s.b[s.i]
		if (c >= '0' && c <= '9') || c == '.' || c == 'e' || c == 'E' || c == '+' || c == '-' {
			s.i++
			continue
		}
		break
	}
	if s.i == start {
		return nil, false
	}
	raw := s.b[start:s.i]
	if !validNumber(raw) {
		s.err = fmt.Errorf("invalid number")
		return nil, false
	}
	return raw, true
}

func validNumber(b []byte) bool {
	if len(b) == 0 {
		return false
	}
	i := 0
	if b[0] == '-' {
		i++
		if i >= len(b) {
			return false
		}
	}
	if b[i] == '0' {
		i++
	} else if b[i] >= '1' && b[i] <= '9' {
		for i < len(b) && b[i] >= '0' && b[i] <= '9' {
			i++
		}
	} else {
		return false
	}
	if i < len(b) && b[i] == '.' {
		i++
		if i >= len(b) || b[i] < '0' || b[i] > '9' {
			return false
		}
		for i < len(b) && b[i] >= '0' && b[i] <= '9' {
			i++
		}
	}
	if i < len(b) && (b[i] == 'e' || b[i] == 'E') {
		i++
		if i < len(b) && (b[i] == '+' || b[i] == '-') {
			i++
		}
		if i >= len(b) || b[i] < '0' || b[i] > '9' {
			return false
		}
		for i < len(b) && b[i] >= '0' && b[i] <= '9' {
			i++
		}
	}
	return i == len(b)
}

// unescapeJSONString decodes JSON string escapes (no surrounding quotes).
func unescapeJSONString(b []byte) (string, error) {
	if indexByte(b, '\\') < 0 {
		return coerceUTF8(b), nil
	}
	out := make([]byte, 0, len(b))
	for i := 0; i < len(b); {
		c := b[i]
		if c != '\\' {
			out = append(out, c)
			i++
			continue
		}
		i++
		if i >= len(b) {
			return "", fmt.Errorf("invalid escape")
		}
		e := b[i]
		i++
		switch e {
		case '"', '\\', '/':
			out = append(out, e)
		case 'b':
			out = append(out, '\b')
		case 'f':
			out = append(out, '\f')
		case 'n':
			out = append(out, '\n')
		case 'r':
			out = append(out, '\r')
		case 't':
			out = append(out, '\t')
		case 'u':
			if i+4 > len(b) {
				return "", fmt.Errorf("invalid escape")
			}
			r, err := parseHex4(b[i : i+4])
			if err != nil {
				return "", err
			}
			i += 4
			if r >= 0xD800 && r <= 0xDBFF && i+6 <= len(b) && b[i] == '\\' && b[i+1] == 'u' {
				lo, err := parseHex4(b[i+2 : i+6])
				if err == nil && lo >= 0xDC00 && lo <= 0xDFFF {
					r = 0x10000 + (r-0xD800)<<10 + (lo - 0xDC00)
					i += 6
				}
			}
			var buf [4]byte
			n := encodeRune(buf[:], r)
			out = append(out, buf[:n]...)
		default:
			return "", fmt.Errorf("invalid escape")
		}
	}
	return coerceUTF8(out), nil
}

func parseHex4(b []byte) (rune, error) {
	var v rune
	for _, c := range b {
		v <<= 4
		switch {
		case c >= '0' && c <= '9':
			v |= rune(c - '0')
		case c >= 'a' && c <= 'f':
			v |= rune(c-'a') + 10
		case c >= 'A' && c <= 'F':
			v |= rune(c-'A') + 10
		default:
			return 0, fmt.Errorf("invalid escape")
		}
	}
	return v, nil
}

func encodeRune(b []byte, r rune) int {
	if r < 0x80 {
		b[0] = byte(r)
		return 1
	}
	if r < 0x800 {
		b[0] = 0xC0 | byte(r>>6)
		b[1] = 0x80 | byte(r&0x3F)
		return 2
	}
	if r < 0x10000 {
		b[0] = 0xE0 | byte(r>>12)
		b[1] = 0x80 | byte((r>>6)&0x3F)
		b[2] = 0x80 | byte(r&0x3F)
		return 3
	}
	b[0] = 0xF0 | byte(r>>18)
	b[1] = 0x80 | byte((r>>12)&0x3F)
	b[2] = 0x80 | byte((r>>6)&0x3F)
	b[3] = 0x80 | byte(r&0x3F)
	return 4
}

func validUTF8(b []byte) bool {
	for i := 0; i < len(b); {
		c := b[i]
		if c < 0x80 {
			i++
			continue
		}
		var n int
		switch {
		case c&0xE0 == 0xC0:
			n = 1
		case c&0xF0 == 0xE0:
			n = 2
		case c&0xF8 == 0xF0:
			n = 3
		default:
			return false
		}
		if i+n >= len(b) {
			return false
		}
		for j := 1; j <= n; j++ {
			if b[i+j]&0xC0 != 0x80 {
				return false
			}
		}
		i += n + 1
	}
	return true
}

// handEntry holds one decoded thin record.
type handEntry struct {
	kind   string
	id     string
	parent *string
	ts     string
	msg    *thinMsg
	custom string
	target string
	usage  Usage
	hasU   bool
	label  string
	hasLbl bool
	usageE Usage
}

// handScanRecord extracts one record with a single pass and no copies of
// skipped values.
func handScanRecord(data []byte) (*Entry, *SessionHeader, error) {
	s := &hscan{b: data}
	if !s.expect('{') {
		return nil, nil, s.err
	}
	var msg *thinMsg
	var target string
	var usage Usage
	var hasU bool
	vals := map[string][]byte{}
	needPair := false
	for {
		s.skipWS()
		if s.i >= len(s.b) {
			return nil, nil, fmt.Errorf("unterminated record")
		}
		if s.b[s.i] == '}' {
			s.i++
			if needPair {
				return nil, nil, fmt.Errorf("trailing comma")
			}
			break
		}
		key, ok := s.scanKey()
		if !ok {
			return nil, nil, s.err
		}
		if !s.expect(':') {
			return nil, nil, s.err
		}
		switch key {
		case "type", "id", "parentId", "timestamp",
			"version", "cwd", "parentSession", "provider", "modelId", "thinkingLevel", "firstKeptEntryId",
			"customType", "targetId", "display", "name", "kind", "model",
			"note", "summary", "tokensBefore", "fromHook", "fromId", "label":
			// Small canonical leaves buffer as raw slices (zero-copy);
			// kind-aware strict validation happens at build.
			start := s.i
			s.skipValue()
			if s.err != nil {
				return nil, nil, s.err
			}
			vals[key] = s.b[start:s.i]
		case "message":
			msg = s.scanMessage()
			if s.err != nil {
				return nil, nil, s.err
			}
		case "usage":
			u, present, err := s.scanUsage()
			if err != nil {
				return nil, nil, err
			}
			if present {
				usage, hasU = u, true
			}
		case "data", "details":
			target = s.scanTarget()
			if s.err != nil {
				return nil, nil, s.err
			}
		case "systemMessage":
			if err := s.scanSystem(); err != nil {
				return nil, nil, err
			}
		case "replacement", "content":
			// RawMessage payloads: any valid JSON; retained on disk.
			s.skipValue()
			if s.err != nil {
				return nil, nil, s.err
			}
		default:
			s.skipValue()
			if s.err != nil {
				return nil, nil, s.err
			}
		}
		s.skipWS()
		if s.i >= len(s.b) {
			return nil, nil, fmt.Errorf("unterminated record")
		}
		if s.b[s.i] == ',' {
			s.i++
			needPair = true
			continue
		}
		needPair = false
		if s.b[s.i] != '}' {
			return nil, nil, fmt.Errorf("invalid record")
		}
	}
	s.skipWS()
	if s.i != len(s.b) {
		return nil, nil, fmt.Errorf("trailing bytes")
	}
	return buildHandScan(vals, msg, target, usage, hasU, s)
}

// skipObject consumes pairs after '{' with strict separators.
func (s *hscan) skipObject() {
	needPair := false
	for {
		s.skipWS()
		if s.i >= len(s.b) {
			s.err = fmt.Errorf("unterminated value")
			return
		}
		if s.b[s.i] == '}' {
			s.i++
			if needPair {
				s.err = fmt.Errorf("trailing comma")
			}
			return
		}
		if _, ok := s.rawString(); !ok {
			if s.err == nil {
				s.err = fmt.Errorf("invalid object key")
			}
			return
		}
		s.skipWS()
		if s.i >= len(s.b) || s.b[s.i] != ':' {
			s.err = fmt.Errorf("invalid value")
			return
		}
		s.i++
		s.skipValue()
		if s.err != nil {
			return
		}
		s.skipWS()
		if s.i >= len(s.b) {
			s.err = fmt.Errorf("unterminated value")
			return
		}
		needPair = false
		if s.b[s.i] == ',' {
			s.i++
			needPair = true
			continue
		}
		if s.b[s.i] != '}' {
			s.err = fmt.Errorf("invalid value")
			return
		}
	}
}

// skipArray consumes elements after '[' with strict separators.
func (s *hscan) skipArray() {
	needElement := false
	for {
		s.skipWS()
		if s.i >= len(s.b) {
			s.err = fmt.Errorf("unterminated value")
			return
		}
		if s.b[s.i] == ']' {
			s.i++
			if needElement {
				s.err = fmt.Errorf("trailing comma")
			}
			return
		}
		s.skipValue()
		if s.err != nil {
			return
		}
		s.skipWS()
		if s.i >= len(s.b) {
			s.err = fmt.Errorf("unterminated value")
			return
		}
		needElement = false
		if s.b[s.i] == ',' {
			s.i++
			needElement = true
			continue
		}
		if s.b[s.i] != ']' {
			s.err = fmt.Errorf("invalid value")
			return
		}
	}
}

// scanKey reads an object key and returns its unescaped value.
func (s *hscan) scanKey() (string, bool) {
	raw, ok := s.rawString()
	if !ok {
		if s.err == nil {
			s.err = fmt.Errorf("invalid object key")
		}
		return "", false
	}
	inner := raw[1 : len(raw)-1]
	if indexByte(inner, '\\') < 0 {
		return string(inner), true
	}
	u, err := unescapeJSONString(inner)
	if err != nil {
		s.err = err
		return "", false
	}
	return u, true
}

// scanMessage parses the message sub-object with tolerant role probing,
// strict canonical leaves, and tool-identity-only content blocks.
func (s *hscan) scanMessage() *thinMsg {
	m := &thinMsg{}
	s.skipWS()
	if s.i >= len(s.b) || s.b[s.i] != '{' {
		if s.i < len(s.b) && s.b[s.i] == 'n' && hasPrefix(s.b[s.i:], "null") {
			s.i += 4
			s.err = fmt.Errorf("invalid message")
			return nil
		}
		s.err = fmt.Errorf("invalid message")
		return nil
	}
	s.i++
	needPair := false
	for {
		s.skipWS()
		if s.i >= len(s.b) {
			s.err = fmt.Errorf("unterminated message")
			return nil
		}
		if s.b[s.i] == '}' {
			s.i++
			if needPair {
				s.err = fmt.Errorf("trailing comma")
				return nil
			}
			break
		}
		key, ok := s.scanKey()
		if !ok {
			return nil
		}
		if !s.expect(':') {
			return nil
		}
		switch key {
		case "role":
			m.roleSeen = true
			s.skipWS()
			if s.i < len(s.b) && s.b[s.i] == '"' {
				v, ok := s.getString()
				if ok {
					m.role = v
				} else {
					return nil
				}
			} else if s.i < len(s.b) && s.b[s.i] == 'n' && hasPrefix(s.b[s.i:], "null") {
				s.i += 4
			} else {
				s.skipValue()
			}
		case "provider", "model", "api", "toolCallId", "toolName", "customType",
			"command", "output", "summary":
			v, ok := s.getString()
			if !ok {
				s.err = fmt.Errorf("invalid %q", key)
				return nil
			}
			switch key {
			case "provider":
				m.prov = v
			case "model":
				m.model = v
			case "api":
				m.api = v
			case "toolCallId":
				m.callID = v
			case "toolName":
				m.toolName = v
			case "customType":
				m.customType = v
			case "command":
			case "output":
			case "summary":
				m.summary = v
			}
		case "responseModel", "fullOutputPath", "errorMessage", "rawStopReason", "stopReason":
			s.skipWS()
			if s.i < len(s.b) && s.b[s.i] == 'n' && hasPrefix(s.b[s.i:], "null") {
				s.i += 4
				break
			}
			v, ok := s.getString()
			if !ok {
				s.err = fmt.Errorf("invalid %q", key)
				return nil
			}
			if key == "responseModel" {
				m.respModel = &v
			}
		case "responseId", "providerThinkingLevel", "thinkingLevel":
			s.skipWS()
			if s.i < len(s.b) && s.b[s.i] == 'n' && hasPrefix(s.b[s.i:], "null") {
				s.i += 4
				break
			}
			if _, ok := s.getString(); !ok {
				s.err = fmt.Errorf("invalid %q", key)
				return nil
			}
		case "usage":
			u, present, err := s.scanUsage()
			if err != nil {
				s.err = err
				return nil
			}
			if present {
				m.usage = u
			}
		case "content":
			s.scanContent(m)
			if s.err != nil {
				return nil
			}
		case "isError", "cancelled", "display", "truncated", "excludeFromContext":
			b, ok := s.getBool()
			if !ok {
				s.err = fmt.Errorf("invalid %q", key)
				return nil
			}
			switch key {
			case "isError":
				m.isErr = b
			case "cancelled":
				m.cancelled = b
			case "display":
				m.display = b
			}
		case "exitCode", "tokensBefore":
			raw, ok := s.getNumberRaw()
			if !ok {
				s.err = fmt.Errorf("invalid %q", key)
				return nil
			}
			if raw == nil {
				break
			}
			v, err := strconv.ParseFloat(string(raw), 64)
			if err != nil {
				s.err = fmt.Errorf("invalid %q", key)
				return nil
			}
			if key == "exitCode" {
				if float64(int(v)) != v {
					s.err = fmt.Errorf("invalid %q", key)
					return nil
				}
				iv := int(v)
				m.exitCode = &iv
			} else {
				m.tokensBefore = v
			}
		case "timestamp":
			raw, ok := s.getNumberRaw()
			if !ok {
				s.err = fmt.Errorf("invalid %q", key)
				return nil
			}
			if raw == nil {
				break
			}
			if _, err := strconv.ParseInt(string(raw), 10, 64); err != nil {
				s.err = fmt.Errorf("invalid %q", key)
				return nil
			}
		case "durationMs":
			raw, ok := s.getNumberRaw()
			if !ok {
				s.err = fmt.Errorf("invalid %q", key)
				return nil
			}
			if raw == nil {
				break
			}
			if _, err := strconv.ParseFloat(string(raw), 64); err != nil {
				s.err = fmt.Errorf("invalid %q", key)
				return nil
			}
		case "endTurn":
			s.skipWS()
			if s.i < len(s.b) && s.b[s.i] == 'n' && hasPrefix(s.b[s.i:], "null") {
				s.i += 4
				break
			}
			if _, ok := s.getBool(); !ok {
				s.err = fmt.Errorf("invalid %q", key)
				return nil
			}
		case "fromId":
			s.skipWS()
			if s.i < len(s.b) && s.b[s.i] == 'n' && hasPrefix(s.b[s.i:], "null") {
				s.i += 4
				break
			}
			v, ok := s.getString()
			if !ok {
				s.err = fmt.Errorf("invalid %q", key)
				return nil
			}
			m.fromID = &v
		case "sections":
			if err := s.scanSections(); err != nil {
				s.err = err
				return nil
			}
		case "toolsAdded", "toolsRemoved":
			if err := s.scanToolList(key == "toolsAdded"); err != nil {
				s.err = err
				return nil
			}
		case "details":
			var raw json.RawMessage
			start := s.i
			s.skipValue()
			if s.err != nil {
				return nil
			}
			raw = append(raw, s.b[start:s.i]...)
			m.details = raw
		default:
			s.skipValue()
			if s.err != nil {
				return nil
			}
		}
		if s.err != nil {
			return nil
		}
		s.skipWS()
		if s.i >= len(s.b) {
			s.err = fmt.Errorf("unterminated message")
			return nil
		}
		needPair = false
		if s.b[s.i] == ',' {
			s.i++
			needPair = true
			continue
		}
		if s.b[s.i] != '}' {
			s.err = fmt.Errorf("invalid message")
			return nil
		}
	}
	if s.err != nil {
		return nil
	}
	if err := resolveContent(m); err != nil {
		s.err = err
		return nil
	}
	return m
}

// scanContent consumes a content value: arrays parse inline when the role is
// already known, otherwise raw bytes buffer for end-of-message resolution
// (key order independence with at most one transient copy).
func (s *hscan) scanContent(m *thinMsg) {
	s.skipWS()
	if s.i < len(s.b) && s.b[s.i] == 'n' && hasPrefix(s.b[s.i:], "null") {
		s.i += 4
		m.calls, m.text = nil, false
		m.contentBuf, m.contentSeen = nil, false
		return
	}
	if m.roleSeen && (m.role == "assistant" || m.role == "toolResult") {
		m.calls, m.text = nil, false
		m.contentBuf, m.contentSeen = nil, false
		s.scanBlocks(m, m.role == "assistant")
		return
	}
	if m.roleSeen {
		m.calls, m.text = nil, false
		m.contentBuf, m.contentSeen = nil, false
		s.skipValue()
		return
	}
	start := s.i
	s.skipValue()
	if s.err != nil {
		return
	}
	m.calls, m.text = nil, false
	m.contentBuf = append(m.contentBuf[:0], s.b[start:s.i]...)
	m.contentSeen = true
}

func resolveContent(m *thinMsg) error {
	if len(m.contentBuf) == 0 {
		return nil
	}
	raw := bytes.TrimSpace(m.contentBuf)
	m.contentBuf = nil
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	strict := m.role == "assistant" || m.role == "toolResult"
	if len(raw) == 0 || raw[0] != '[' {
		if strict {
			return fmt.Errorf("invalid content")
		}
		return nil
	}
	sub := &hscan{b: raw}
	if !sub.expect('[') {
		return fmt.Errorf("invalid content")
	}
	return sub.scanBlockArray(m, strict)
}

func (s *hscan) scanBlocks(m *thinMsg, strictArgs bool) {
	s.skipWS()
	if s.i >= len(s.b) || s.b[s.i] != '[' {
		s.err = fmt.Errorf("invalid content")
		return
	}
	s.i++
	if err := s.scanBlockArray(m, strictArgs); err != nil {
		s.err = err
	}
}

func (s *hscan) scanBlockArray(m *thinMsg, strictArgs bool) error {
	needElement := false
	for {
		s.skipWS()
		if s.i >= len(s.b) {
			return fmt.Errorf("unterminated content")
		}
		if s.b[s.i] == ']' {
			s.i++
			if needElement {
				return fmt.Errorf("trailing comma")
			}
			return nil
		}
		if s.b[s.i] != '{' {
			return fmt.Errorf("invalid content block")
		}
		s.i++
		var btype, id, name string
		argsSeen := false
		var argsRaw []byte
		var textRaw, textSigRaw, thinkingRaw, thinkingSigRaw, redactedRaw, dataRaw, mimeRaw []byte
		needPair := false
		for {
			s.skipWS()
			if s.i >= len(s.b) {
				return fmt.Errorf("unterminated block")
			}
			if s.b[s.i] == '}' {
				s.i++
				if needPair {
					return fmt.Errorf("trailing comma")
				}
				break
			}
			key, ok := s.scanKey()
			if !ok {
				return s.err
			}
			if !s.expect(':') {
				return s.err
			}
			switch key {
			case "type", "id", "name", "text", "textSignature", "thinking", "thinkingSignature", "redacted", "data", "mimeType":
				s.skipWS()
				if s.i < len(s.b) && s.b[s.i] == '"' {
					v, ok := s.getString()
					if !ok {
						return s.err
					}
					switch key {
					case "type":
						btype = v
					case "id":
						id = v
					case "name":
						name = v
					}
				} else if s.i < len(s.b) && s.b[s.i] == 'n' && hasPrefix(s.b[s.i:], "null") {
					s.i += 4
				} else {
					start := s.i
					s.skipValue()
					if s.err != nil {
						return s.err
					}
					switch key {
					case "text":
						textRaw = s.b[start:s.i]
					case "textSignature":
						textSigRaw = s.b[start:s.i]
					case "thinking":
						thinkingRaw = s.b[start:s.i]
					case "thinkingSignature":
						thinkingSigRaw = s.b[start:s.i]
					case "redacted":
						redactedRaw = s.b[start:s.i]
					case "data":
						dataRaw = s.b[start:s.i]
					case "mimeType":
						mimeRaw = s.b[start:s.i]
					}
				}
			case "arguments":
				start := s.i
				s.skipValue()
				if s.err != nil {
					return s.err
				}
				argsSeen = true
				argsRaw = s.b[start:s.i]
			default:
				s.skipValue()
			}
			if s.err != nil {
				return s.err
			}
			s.skipWS()
			if s.i >= len(s.b) {
				return fmt.Errorf("unterminated block")
			}
			needPair = false
			if s.b[s.i] == ',' {
				s.i++
				needPair = true
				continue
			}
			if s.b[s.i] != '}' {
				return fmt.Errorf("invalid block")
			}
		}
		switch btype {
		case "text":
			if err := checkOptString(textRaw, "text"); err != nil {
				return err
			}
			if err := checkOptString(textSigRaw, "textSignature"); err != nil {
				return err
			}
			m.text = true
		case "thinking":
			if strictArgs {
				if err := checkOptString(thinkingRaw, "thinking"); err != nil {
					return err
				}
				if err := checkOptString(thinkingSigRaw, "thinkingSignature"); err != nil {
					return err
				}
				if err := checkOptBool(redactedRaw); err != nil {
					return err
				}
			}
		case "image":
			if !strictArgs {
				if err := checkReqString(dataRaw, "data"); err != nil {
					return err
				}
				if err := checkReqString(mimeRaw, "mimeType"); err != nil {
					return err
				}
			}
		case "toolCall":
			if strictArgs {
				if argsSeen && !argsObjectOrNull(argsRaw) {
					return fmt.Errorf("invalid tool arguments")
				}
				m.calls = append(m.calls, ContentBlock{Type: "toolCall", Call: &ToolCall{Type: "toolCall", ID: id, Name: name}})
			}
		}
		s.skipWS()
		if s.i >= len(s.b) {
			return fmt.Errorf("unterminated content")
		}
		needElement = false
		if s.b[s.i] == ',' {
			s.i++
			needElement = true
			continue
		}
		if s.b[s.i] != ']' {
			return fmt.Errorf("invalid content")
		}
	}
}

// argsObjectOrNull reports whether captured arguments are null or a
// JSON object (eager map-decode parity); syntax was validated on capture.
func argsObjectOrNull(raw []byte) bool {
	t := bytes.TrimSpace(raw)
	if len(t) == 0 || string(t) == "null" {
		return true
	}
	return len(t) > 0 && t[0] == '{'
}

func (s *hscan) scanUsage() (Usage, bool, error) {
	var u Usage
	s.skipWS()
	if s.i < len(s.b) && s.b[s.i] == 'n' && hasPrefix(s.b[s.i:], "null") {
		s.i += 4
		return u, false, nil
	}
	if s.i >= len(s.b) || s.b[s.i] != '{' {
		return u, true, fmt.Errorf("invalid usage")
	}
	s.i++
	setNum := func(dst *float64) error {
		s.skipWS()
		if s.i < len(s.b) && s.b[s.i] == 'n' && hasPrefix(s.b[s.i:], "null") {
			s.i += 4
			return nil
		}
		raw, ok := s.getNumberRaw()
		if !ok {
			return fmt.Errorf("invalid usage")
		}
		v, err := strconv.ParseFloat(string(raw), 64)
		if err != nil {
			return fmt.Errorf("invalid usage")
		}
		*dst = v
		return nil
	}
	needPair := false
	for {
		s.skipWS()
		if s.i >= len(s.b) {
			return u, true, fmt.Errorf("unterminated usage")
		}
		if s.b[s.i] == '}' {
			s.i++
			if needPair {
				return u, true, fmt.Errorf("trailing comma")
			}
			return u, true, nil
		}
		if s.i >= len(s.b) {
			return u, true, fmt.Errorf("unterminated usage")
		}
		key, ok := s.scanKey()
		if !ok {
			return u, true, s.err
		}
		if !s.expect(':') {
			return u, true, s.err
		}
		switch key {
		case "input":
			if err := setNum(&u.Input); err != nil {
				return u, true, err
			}
		case "output":
			if err := setNum(&u.Output); err != nil {
				return u, true, err
			}
		case "cacheRead":
			if err := setNum(&u.CacheRead); err != nil {
				return u, true, err
			}
		case "cacheWrite":
			if err := setNum(&u.CacheWrite); err != nil {
				return u, true, err
			}
		case "cacheWrite1h", "reasoning":
			s.skipWS()
			if s.i < len(s.b) && s.b[s.i] == 'n' && hasPrefix(s.b[s.i:], "null") {
				s.i += 4
				break
			}
			raw, ok := s.getNumberRaw()
			if !ok {
				return u, true, fmt.Errorf("invalid usage")
			}
			if raw == nil {
				break
			}
			v, err := strconv.ParseFloat(string(raw), 64)
			if err != nil {
				return u, true, fmt.Errorf("invalid usage")
			}
			if key == "cacheWrite1h" {
				u.CacheWrite1h = &v
			} else {
				u.Reasoning = &v
			}
		case "totalTokens":
			if err := setNum(&u.TotalTokens); err != nil {
				return u, true, err
			}
		case "cost":
			if err := s.scanCost(&u.Cost); err != nil {
				return u, true, err
			}
		default:
			s.skipValue()
			if s.err != nil {
				return u, true, s.err
			}
		}
		s.skipWS()
		if s.i >= len(s.b) {
			return u, true, fmt.Errorf("unterminated usage")
		}
		needPair = false
		if s.b[s.i] == ',' {
			s.i++
			needPair = true
			continue
		}
		if s.b[s.i] != '}' {
			return u, true, fmt.Errorf("invalid usage")
		}
	}
}

func (s *hscan) scanCost(c *UsageCost) error {
	s.skipWS()
	if s.i < len(s.b) && s.b[s.i] == 'n' && hasPrefix(s.b[s.i:], "null") {
		s.i += 4
		return nil
	}
	if s.i >= len(s.b) || s.b[s.i] != '{' {
		return fmt.Errorf("invalid cost")
	}
	s.i++
	for {
		s.skipWS()
		if s.i < len(s.b) && s.b[s.i] == '}' {
			s.i++
			return nil
		}
		if s.i >= len(s.b) {
			return fmt.Errorf("unterminated cost")
		}
		key, ok := s.scanKey()
		if !ok {
			return s.err
		}
		if !s.expect(':') {
			return s.err
		}
		switch key {
		case "input", "output", "cacheRead", "cacheWrite":
			s.skipWS()
			if s.i < len(s.b) && s.b[s.i] == 'n' && hasPrefix(s.b[s.i:], "null") {
				s.i += 4
				break
			}
			raw, ok := s.getNumberRaw()
			if !ok {
				return fmt.Errorf("invalid cost")
			}
			if raw == nil {
				break
			}
			v, err := strconv.ParseFloat(string(raw), 64)
			if err != nil {
				return fmt.Errorf("invalid cost")
			}
			switch key {
			case "input":
				c.Input = v
			case "output":
				c.Output = v
			case "cacheRead":
				c.CacheRead = v
			case "cacheWrite":
				c.CacheWrite = v
			}
		case "totalFromProvider":
			s.skipWS()
			if s.i < len(s.b) && s.b[s.i] == 'n' && hasPrefix(s.b[s.i:], "null") {
				s.i += 4
				break
			}
			if hasPrefix(s.b[s.i:], "true") {
				t := true
				c.TotalFromProvider = &t
				s.i += 4
			} else if hasPrefix(s.b[s.i:], "false") {
				f := false
				c.TotalFromProvider = &f
				s.i += 5
			} else {
				return fmt.Errorf("invalid cost")
			}
		default:
			s.skipValue()
			if s.err != nil {
				return s.err
			}
		}
		s.skipWS()
		if s.i < len(s.b) && s.b[s.i] == ',' {
			s.i++
			continue
		}
	}
}

func (s *hscan) scanTarget() string {
	s.skipWS()
	if s.i >= len(s.b) || s.b[s.i] != '{' {
		if s.i < len(s.b) && s.b[s.i] == 'n' && hasPrefix(s.b[s.i:], "null") {
			s.i += 4
			return ""
		}
		s.skipValue()
		return ""
	}
	s.i++
	out := ""
	for {
		s.skipWS()
		if s.i < len(s.b) && s.b[s.i] == '}' {
			s.i++
			return out
		}
		if s.i >= len(s.b) {
			s.err = fmt.Errorf("unterminated details")
			return ""
		}
		key, ok := s.scanKey()
		if !ok {
			return ""
		}
		if !s.expect(':') {
			return ""
		}
		if key == "targetId" || key == "psiOutcome" {
			s.skipWS()
			if s.i < len(s.b) && s.b[s.i] == '"' {
				if v, ok := s.getString(); ok {
					out = v
				}
			} else {
				s.skipValue()
			}
		} else {
			s.skipValue()
		}
		s.skipWS()
		if s.i < len(s.b) && s.b[s.i] == ',' {
			s.i++
			continue
		}
	}
}

func (s *hscan) scanSystem() error {
	s.skipWS()
	if s.i < len(s.b) && s.b[s.i] == 'n' && hasPrefix(s.b[s.i:], "null") {
		s.i += 4
		return nil
	}
	if s.i >= len(s.b) || s.b[s.i] != '{' {
		return fmt.Errorf("invalid systemMessage")
	}
	s.i++
	for {
		s.skipWS()
		if s.i < len(s.b) && s.b[s.i] == '}' {
			s.i++
			return nil
		}
		if s.i >= len(s.b) {
			return fmt.Errorf("unterminated systemMessage")
		}
		key, ok := s.scanKey()
		if !ok {
			return s.err
		}
		if !s.expect(':') {
			return s.err
		}
		switch key {
		case "role":
			if _, ok := s.getString(); !ok {
				return fmt.Errorf("invalid %q", key)
			}
		case "content":
			s.skipValue()
			if s.err != nil {
				return s.err
			}
		case "timestamp":
			if _, ok := s.getNumberRaw(); !ok {
				return fmt.Errorf("invalid %q", key)
			}
		case "sections":
			s.skipWS()
			if s.i < len(s.b) && s.b[s.i] == 'n' && hasPrefix(s.b[s.i:], "null") {
				s.i += 4
				break
			}
			if s.i >= len(s.b) || s.b[s.i] != '{' {
				return fmt.Errorf("invalid sections")
			}
			s.i++
			for {
				s.skipWS()
				if s.i < len(s.b) && s.b[s.i] == '}' {
					s.i++
					break
				}
				if s.i >= len(s.b) {
					return fmt.Errorf("unterminated sections")
				}
				if _, ok := s.scanKey(); !ok {
					return s.err
				}
				if !s.expect(':') {
					return s.err
				}
				s.skipWS()
				if s.i < len(s.b) && s.b[s.i] == 'n' && hasPrefix(s.b[s.i:], "null") {
					s.i += 4
				} else {
					if s.i >= len(s.b) || s.b[s.i] != '"' {
						return fmt.Errorf("invalid section")
					}
					if _, ok := s.getString(); !ok {
						return s.err
					}
				}
			}
		case "toolsAdded", "toolsRemoved":
			if err := s.scanToolList(key == "toolsAdded"); err != nil {
				return err
			}
		default:
			s.skipValue()
			if s.err != nil {
				return s.err
			}
		}
		if s.err != nil {
			return s.err
		}
		s.skipWS()
		if s.i < len(s.b) && s.b[s.i] == ',' {
			s.i++
			continue
		}
	}
}

func (s *hscan) scanToolList(withDesc bool) error {
	s.skipWS()
	if s.i < len(s.b) && s.b[s.i] == 'n' && hasPrefix(s.b[s.i:], "null") {
		s.i += 4
		return nil
	}
	if s.i >= len(s.b) || s.b[s.i] != '[' {
		return fmt.Errorf("invalid tool list")
	}
	s.i++
	for {
		s.skipWS()
		if s.i < len(s.b) && s.b[s.i] == ']' {
			s.i++
			return nil
		}
		if s.i >= len(s.b) || s.b[s.i] != '{' {
			return fmt.Errorf("invalid tool list")
		}
		s.i++
		for {
			s.skipWS()
			if s.i < len(s.b) && s.b[s.i] == '}' {
				s.i++
				break
			}
			if s.i >= len(s.b) {
				return fmt.Errorf("unterminated tool")
			}
			name, ok := s.scanKey()
			if !ok {
				return s.err
			}
			if !s.expect(':') {
				return s.err
			}
			switch name {
			case "name", "namespace":
				s.skipWS()
				if s.i < len(s.b) && s.b[s.i] == 'n' && hasPrefix(s.b[s.i:], "null") {
					s.i += 4
					break
				}
				if _, ok := s.getString(); !ok {
					return fmt.Errorf("invalid tool")
				}
			case "description":
				if !withDesc {
					s.skipValue()
					if s.err != nil {
						return s.err
					}
					break
				}
				s.skipWS()
				if s.i < len(s.b) && s.b[s.i] == 'n' && hasPrefix(s.b[s.i:], "null") {
					s.i += 4
					break
				}
				if _, ok := s.getString(); !ok {
					return fmt.Errorf("invalid tool")
				}
			default:
				s.skipValue()
				if s.err != nil {
					return s.err
				}
			}
			s.skipWS()
			if s.i < len(s.b) && s.b[s.i] == ',' {
				s.i++
				continue
			}
		}
		s.skipWS()
		if s.i < len(s.b) && s.b[s.i] == ',' {
			s.i++
			continue
		}
	}
}

func (s *hscan) scanSections() error {
	s.skipWS()
	if s.i < len(s.b) && s.b[s.i] == 'n' && hasPrefix(s.b[s.i:], "null") {
		s.i += 4
		return nil
	}
	if s.i >= len(s.b) || s.b[s.i] != '{' {
		return fmt.Errorf("invalid sections")
	}
	s.i++
	for {
		s.skipWS()
		if s.i < len(s.b) && s.b[s.i] == '}' {
			s.i++
			return nil
		}
		if s.i >= len(s.b) {
			return fmt.Errorf("unterminated sections")
		}
		if _, ok := s.scanKey(); !ok {
			return s.err
		}
		if !s.expect(':') {
			return s.err
		}
		s.skipWS()
		if s.i < len(s.b) && s.b[s.i] == 'n' && hasPrefix(s.b[s.i:], "null") {
			s.i += 4
		} else {
			if s.i >= len(s.b) || s.b[s.i] != '"' {
				return fmt.Errorf("invalid section")
			}
			if _, ok := s.getString(); !ok {
				return s.err
			}
		}
		s.skipWS()
		if s.i < len(s.b) && s.b[s.i] == ',' {
			s.i++
			continue
		}
	}
}

func buildHandScan(vals map[string][]byte, msg *thinMsg, target string, usage Usage, hasU bool, s *hscan) (*Entry, *SessionHeader, error) {
	if s.err != nil {
		return nil, nil, s.err
	}
	recType := handOptStrVal(vals, "type")
	recID := handOptStrVal(vals, "id")
	if recType == "" {
		return nil, nil, fmt.Errorf("entry missing type")
	}
	if recID == "" {
		return nil, nil, fmt.Errorf("entry missing id")
	}
	if recType == "session" {
		return nil, scanHandHeader(vals, recID), nil
	}
	ts, err := handStr(vals, "timestamp")
	if err != nil {
		if _, ok := vals["timestamp"]; !ok {
			ts = ""
		} else {
			return nil, nil, err
		}
	}
	var parentID *string
	if raw, ok := vals["parentId"]; ok {
		t := bytes.TrimSpace(raw)
		if len(t) > 0 && string(t) != "null" {
			if len(t) == 0 || t[0] != '"' {
				return nil, nil, fmt.Errorf("invalid %q", "parentId")
			}
			u, err := unescapeJSONString(t[1 : len(t)-1])
			if err != nil {
				return nil, nil, fmt.Errorf("invalid %q", "parentId")
			}
			if u != "" {
				parentID = &u
			}
		}
	}
	strictFor := func(kinds ...string) bool {
		for _, k := range kinds {
			if k == recType {
				return true
			}
		}
		return false
	}
	get := func(key string) (string, error) {
		if !strictFor(kindOwners(key)...) {
			return "", nil
		}
		return handStr(vals, key)
	}
	_ = get
	e := &Entry{Kind: recType, Base: entryBase{Type: recType, ID: recID, ParentID: parentID, Timestamp: ts}}
	switch recType {
	case "message":
		if msg == nil {
			e.Message = &SessionMessageEntry{Type: recType, ID: recID, ParentID: parentID, Timestamp: ts, Message: AgentMessage{}}
			return e, nil, nil
		}
		am := AgentMessage{Role: msg.role}
		switch msg.role {
		case "user":
			am.User = &UserMessage{Role: msg.role}
		case "system":
			am.System = &SystemMessage{Role: msg.role}
		case "assistant":
			am.Assistant = &AssistantMessage{Role: msg.role, Provider: msg.prov, Model: msg.model, API: msg.api, ResponseModel: msg.respModel, Usage: msg.usage, Content: msg.calls}
			if msg.text {
				am.Assistant.Content = append(am.Assistant.Content, ContentBlock{Type: "text", Text: &TextContent{Type: "text"}})
			}
		case "toolResult":
			details := json.RawMessage(`{"psiOutcome":""}`)
			if len(msg.details) > 0 {
				details = json.RawMessage(`{"psiOutcome":` + strconvQuote(thinDetailString(msg.details)) + `}`)
			}
			am.ToolResult = &ToolResultMessage{Role: msg.role, ToolCallID: msg.callID, ToolName: msg.toolName, IsError: msg.isErr, Details: details}
		case "bashExecution":
			am.BashExecution = &BashExecutionMessage{Role: msg.role, Cancelled: msg.cancelled, ExitCode: msg.exitCode}
		case "custom":
			am.Custom = &CustomMessage{Role: msg.role, CustomType: msg.customType}
		case "branchSummary":
			am.BranchSummary = &BranchSummaryMessage{Role: msg.role}
		case "compactionSummary":
			am.CompactionSummary = &CompactionSummaryMessage{Role: msg.role}
		}
		e.Message = &SessionMessageEntry{Type: recType, ID: recID, ParentID: parentID, Timestamp: ts, Message: am}
		return e, nil, nil
	}
	// Kind-specific leaves below; validated only for their owning kinds.
	need := func(key string, owners ...string) (string, error) {
		for _, o := range owners {
			if o == recType {
				return handStr(vals, key)
			}
		}
		return "", nil
	}
	needOpt := func(key string, owners ...string) (*string, error) {
		for _, o := range owners {
			if o == recType {
				return handOptStr(vals, key)
			}
		}
		return nil, nil
	}
	needBool := func(key string, owners ...string) (bool, error) {
		for _, o := range owners {
			if o == recType {
				return handBool(vals, key)
			}
		}
		return false, nil
	}
	needOptBool := func(key string, owners ...string) (*bool, error) {
		for _, o := range owners {
			if o == recType {
				return handOptBool(vals, key)
			}
		}
		return nil, nil
	}
	needFloat := func(key string, owners ...string) (float64, error) {
		for _, o := range owners {
			if o == recType {
				if _, ok := vals[key]; !ok {
					return 0, nil
				}
				return handFloat(vals, key)
			}
		}
		return 0, nil
	}
	_ = needOpt
	_ = needBool
	_ = needOptBool
	_ = needFloat
	switch recType {
	case "model_change":
		prov, err := need("provider", "model_change")
		if err != nil {
			return nil, nil, err
		}
		modelID, err := need("modelId", "model_change")
		if err != nil {
			return nil, nil, err
		}
		e.ModelChange = &ModelChangeEntry{Provider: prov, ModelID: modelID}
	case "thinking_level_change":
		lvl, err := need("thinkingLevel", "thinking_level_change")
		if err != nil {
			return nil, nil, err
		}
		e.ThinkingLevelChange = &ThinkingLevelChangeEntry{ThinkingLevel: lvl}
	case "compaction":
		firstKept, err := need("firstKeptEntryId", "compaction")
		if err != nil {
			return nil, nil, err
		}
		var usagePtr *Usage
		if hasU {
			usagePtr = &usage
		}
		e.Compaction = &CompactionEntry{FirstKeptEntryID: firstKept, Usage: usagePtr}
	case "context_edit":
		targetID, err := need("targetId", "context_edit")
		if err != nil {
			return nil, nil, err
		}
		e.ContextEdit = &ContextEditEntry{TargetID: targetID}
	case "custom":
		customType, err := need("customType", "custom")
		if err != nil {
			return nil, nil, err
		}
		e.Custom = &CustomEntry{CustomType: customType}
		if customType == "psi.input" {
			e.provenanceTarget = target
		}
	case "custom_message":
		customType, err := need("customType", "custom_message")
		if err != nil {
			return nil, nil, err
		}
		display, err := needBool("display", "custom_message")
		if err != nil {
			return nil, nil, err
		}
		e.CustomMessage = &CustomMessageEntry{CustomType: customType, Display: display}
		if customType == "psi.input" {
			e.provenanceTarget = target
		}
	case "branch_summary":
		e.BranchSummary = &BranchSummaryEntry{}
	case "usage":
		e.Usage = &UsageEntry{}
		e.Usage.Usage = usage
	case "label":
		e.Label = &LabelEntry{}
	case "session_info":
		name, err := needOpt("name", "session_info")
		if err != nil {
			return nil, nil, err
		}
		e.SessionInfo = &SessionInfoEntry{Name: name}
	default:
		return nil, nil, fmt.Errorf("unknown entry type %q", recType)
	}
	return e, nil, nil
}

func kindOwners(key string) []string {
	switch key {
	case "provider", "model":
		return []string{"model_change", "usage"}
	case "modelId":
		return []string{"model_change"}
	case "thinkingLevel":
		return []string{"thinking_level_change"}
	case "firstKeptEntryId", "summary", "tokensBefore", "fromHook":
		if key == "summary" {
			return []string{"compaction", "branch_summary"}
		}
		if key == "fromHook" {
			return []string{"compaction", "branch_summary"}
		}
		return []string{"compaction"}
	case "targetId":
		return []string{"context_edit", "label"}
	case "customType":
		return []string{"custom", "custom_message"}
	case "display":
		return []string{"custom_message"}
	case "name":
		return []string{"session_info"}
	case "kind", "note":
		return []string{"usage"}
	case "fromId", "label":
		return []string{"branch_summary", "label"}
	}
	return nil
}

func scanHandHeader(vals map[string][]byte, recID string) *SessionHeader {
	h := &SessionHeader{Type: "session", ID: recID}
	if raw, ok := vals["version"]; ok {
		t := bytes.TrimSpace(raw)
		if len(t) > 0 && string(t) != "null" {
			if v, err := strconv.ParseInt(string(t), 10, 64); err == nil {
				iv := int(v)
				h.Version = &iv
			}
		}
	}
	if s, err := handStr(vals, "cwd"); err == nil {
		h.Cwd = s
	}
	if s, err := handStr(vals, "timestamp"); err == nil {
		h.Timestamp = s
	}
	if raw, ok := vals["parentSession"]; ok {
		t := bytes.TrimSpace(raw)
		if len(t) > 2 && string(t) != "null" && t[0] == '"' {
			if u, err := unescapeJSONString(t[1 : len(t)-1]); err == nil {
				h.ParentSession = &u
			}
		}
	}
	return h
}

type thinMsg struct {
	role             string
	roleSeen         bool
	prov, model, api string
	respModel        *string
	usage            Usage
	calls            []ContentBlock
	text             bool
	contentBuf       json.RawMessage
	contentSeen      bool
	callID, toolName string
	isErr, cancelled bool
	exitCode         *int
	customType       string
	display          bool
	summary          string
	fromID           *string
	tokensBefore     float64
	details          json.RawMessage
}

func thinDetailString(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	obj, err := exactObject(raw)
	if err != nil {
		return ""
	}
	s, _ := exactString(obj, "psiOutcome")
	return s
}

func strconvQuote(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"', '\\':
			b.WriteByte('\\')
			b.WriteRune(r)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}

// handStr decodes a required string leaf from buffered raw bytes; null
// counts as missing (struct no-op parity is handled by the caller).
func handStr(vals map[string][]byte, key string) (string, error) {
	raw, ok := vals[key]
	if !ok {
		return "", nil
	}
	t := bytes.TrimSpace(raw)
	if len(t) == 0 || string(t) == "null" {
		return "", nil
	}
	if len(t) < 2 || t[0] != '"' {
		return "", fmt.Errorf("invalid %q", key)
	}
	u, err := unescapeJSONString(t[1 : len(t)-1])
	if err != nil {
		return "", fmt.Errorf("invalid %q", key)
	}
	return u, nil
}

func handOptStr(vals map[string][]byte, key string) (*string, error) {
	raw, ok := vals[key]
	if !ok {
		return nil, nil
	}
	t := bytes.TrimSpace(raw)
	if len(t) == 0 || string(t) == "null" {
		return nil, nil
	}
	if len(t) < 2 || t[0] != '"' {
		return nil, fmt.Errorf("invalid %q", key)
	}
	u, err := unescapeJSONString(t[1 : len(t)-1])
	if err != nil {
		return nil, fmt.Errorf("invalid %q", key)
	}
	return &u, nil
}

func handBool(vals map[string][]byte, key string) (bool, error) {
	raw, ok := vals[key]
	if !ok {
		return false, nil
	}
	t := bytes.TrimSpace(raw)
	switch string(t) {
	case "true":
		return true, nil
	case "false", "null", "":
		return false, nil
	default:
		return false, fmt.Errorf("invalid %q", key)
	}
}

func handOptBool(vals map[string][]byte, key string) (*bool, error) {
	raw, ok := vals[key]
	if !ok {
		return nil, nil
	}
	t := bytes.TrimSpace(raw)
	switch string(t) {
	case "true":
		b := true
		return &b, nil
	case "false":
		b := false
		return &b, nil
	case "null", "":
		return nil, nil
	default:
		return nil, fmt.Errorf("invalid %q", key)
	}
}

func handFloat(vals map[string][]byte, key string) (float64, error) {
	raw, ok := vals[key]
	if !ok {
		return 0, nil
	}
	t := bytes.TrimSpace(raw)
	if len(t) == 0 || string(t) == "null" {
		return 0, nil
	}
	v, err := strconv.ParseFloat(string(t), 64)
	if err != nil {
		return 0, fmt.Errorf("invalid %q", key)
	}
	return v, nil
}

func handOptInt(vals map[string][]byte, key string) (*int, error) {
	raw, ok := vals[key]
	if !ok {
		return nil, nil
	}
	t := bytes.TrimSpace(raw)
	if len(t) == 0 || string(t) == "null" {
		return nil, nil
	}
	v, err := strconv.ParseInt(string(t), 10, 64)
	if err != nil {
		return nil, fmt.Errorf("invalid %q", key)
	}
	out := int(v)
	return &out, nil
}

// handOptStrVal reads an envelope discriminator tolerantly (probe parity:
// absent, null, and mistyped all yield "").
func handOptStrVal(vals map[string][]byte, key string) string {
	raw, ok := vals[key]
	if !ok {
		return ""
	}
	t := bytes.TrimSpace(raw)
	if len(t) < 2 || t[0] != '"' {
		return ""
	}
	u, err := unescapeJSONString(t[1 : len(t)-1])
	if err != nil {
		return ""
	}
	return u
}

// coerceUTF8 mirrors encoding/json string decoding: each invalid byte
// becomes one U+FFFD (not one per run).
func coerceUTF8(b []byte) string {
	out := make([]byte, 0, len(b))
	for i := 0; i < len(b); {
		c := b[i]
		if c < utf8.RuneSelf {
			out = append(out, c)
			i++
			continue
		}
		r, size := utf8.DecodeRune(b[i:])
		if r == utf8.RuneError && size <= 1 {
			out = append(out, 0xEF, 0xBF, 0xBD)
			i++
			continue
		}
		out = append(out, b[i:i+size]...)
		i += size
	}
	return string(out)
}

// checkOptString validates an optional string leaf captured as raw bytes:
// absent and null accept; otherwise valid JSON string required.
func checkOptString(raw []byte, what string) error {
	if len(raw) == 0 {
		return nil
	}
	t := bytes.TrimSpace(raw)
	if len(t) == 0 || string(t) == "null" {
		return nil
	}
	if len(t) < 2 || t[0] != '"' {
		return fmt.Errorf("invalid %q", what)
	}
	if _, err := unescapeJSONString(t[1 : len(t)-1]); err != nil {
		return fmt.Errorf("invalid %q", what)
	}
	return nil
}

// checkReqString validates a required string leaf (null counts as missing).
func checkReqString(raw []byte, what string) error {
	if len(raw) == 0 {
		return fmt.Errorf("invalid %q", what)
	}
	return checkOptString(raw, what)
}

// checkOptBool validates an optional bool leaf (absent/null accept).
func checkOptBool(raw []byte) error {
	if len(raw) == 0 {
		return nil
	}
	t := bytes.TrimSpace(raw)
	switch string(t) {
	case "true", "false", "null", "":
		return nil
	default:
		return fmt.Errorf("invalid bool")
	}
}
