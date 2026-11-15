package webaccess

import (
	"bytes"
	"errors"
	"io"
	"mime"
	"regexp"
	"strings"

	"golang.org/x/text/encoding/htmlindex"
	"golang.org/x/text/encoding/unicode"
	"golang.org/x/text/runes"
	"golang.org/x/text/transform"
)

var charsetParam = regexp.MustCompile(`(?i)charset\s*=\s*["']?([a-z0-9._:-]+)`)
var metaCharset = regexp.MustCompile(`(?i)<meta[^>]+charset\s*=\s*["']?([a-z0-9._:-]+)`)

// declaredCharset returns the charset parameter of a Content-Type header.
func declaredCharset(contentType string) string {
	if contentType == "" {
		return ""
	}
	if _, params, err := mime.ParseMediaType(contentType); err == nil {
		return params["charset"]
	}
	if match := charsetParam.FindStringSubmatch(contentType); len(match) == 2 {
		return match[1]
	}
	return ""
}

// sniffCharset reads a BOM and then an HTML meta declaration from the prefix.
func sniffCharset(body []byte) string {
	switch {
	case bytes.HasPrefix(body, []byte{0xEF, 0xBB, 0xBF}):
		return "utf-8"
	case bytes.HasPrefix(body, []byte{0xFF, 0xFE}):
		return "utf-16le"
	case bytes.HasPrefix(body, []byte{0xFE, 0xFF}):
		return "utf-16be"
	}
	prefix := body
	if len(prefix) > 4096 {
		prefix = prefix[:4096]
	}
	if match := metaCharset.FindSubmatch(prefix); len(match) == 2 {
		return string(match[1])
	}
	return ""
}

// decodeText converts body bytes to UTF-8, replacing invalid input. A BOM or
// HTML meta declaration wins over the Content-Type header; otherwise the
// header wins, defaulting to UTF-8.
func decodeText(body []byte, contentType string) (string, error) {
	name := sniffCharset(body)
	if name == "" {
		name = declaredCharset(contentType)
	}
	if name == "" {
		name = "utf-8"
	}
	lower := strings.ToLower(name)
	switch lower {
	case "utf-8", "utf8":
		body = bytes.TrimPrefix(body, []byte{0xEF, 0xBB, 0xBF})
		return string(bytes.ToValidUTF8(body, []byte("\uFFFD"))), nil
	case "utf-16", "utf-16le":
		return decodeWith(body, unicode.UTF16(unicode.LittleEndian, unicode.UseBOM).NewDecoder())
	case "utf-16be":
		return decodeWith(body, unicode.UTF16(unicode.BigEndian, unicode.UseBOM).NewDecoder())
	}
	enc, err := htmlindex.Get(name)
	if err != nil {
		return "", errors.New("unsupported declared charset")
	}
	return decodeWith(body, enc.NewDecoder())
}

func decodeWith(body []byte, decoder transform.Transformer) (string, error) {
	out, err := io.ReadAll(transform.NewReader(bytes.NewReader(body), transform.Chain(decoder, runes.ReplaceIllFormed())))
	if err != nil {
		return "", errors.New("unsupported declared charset")
	}
	return string(out), nil
}
