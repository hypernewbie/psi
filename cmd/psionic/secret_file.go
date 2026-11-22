package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/hypernewbie/psi/internal/secretmem"
)

const (
	defaultSecretFileBytes   = 1 << 20
	maxSecretFileBytes       = 20 << 20
	defaultSecretFileTimeout = 5 * time.Second
	maxSecretFileTimeout     = 120 * time.Second
	maxSecretArrayIndex      = 100000
)

// fileTokenProvider validates the file reference without reading its value.
func (s *fileSettings) fileTokenProvider() (*secretProvider, error) {
	ref := s.Channels.Discord.Token
	alias := ref.provider
	if alias == "" && s.Secrets != nil {
		alias = strings.TrimSpace(s.Secrets.Defaults.File)
	}
	if alias == "" {
		alias = "default"
	}
	if !secretProviderName.MatchString(alias) || !validFileSecretID(ref.file) {
		return nil, fmt.Errorf("invalid file secret reference")
	}
	if s.Secrets == nil {
		return nil, fmt.Errorf("file secret provider is unavailable")
	}
	provider := s.Secrets.Providers[alias]
	if provider == nil || provider.Source != "file" {
		return nil, fmt.Errorf("file secret provider is unavailable")
	}
	return provider, nil
}

// resolveConfiguredToken borrows the settings. Returned strings cross the
// Discord client boundary and cannot be wiped; file buffers are wiped here.
func (s *fileSettings) resolveConfiguredToken(lookupEnv func(string) (string, bool)) (string, error) {
	ref := s.Channels.Discord.Token
	if ref.file == "" {
		return ref.resolve(lookupEnv)
	}
	provider, err := s.fileTokenProvider()
	if err != nil {
		return "", err
	}
	path, err := substituteConfigEnvironment(provider.Path, lookupEnv)
	if err != nil {
		return "", fmt.Errorf("file secret is unavailable")
	}
	path, err = expandUser(strings.TrimSpace(path))
	if err != nil || path == "" {
		return "", fmt.Errorf("file secret is unavailable")
	}
	path, err = filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("file secret is unavailable")
	}
	limit := defaultSecretFileBytes
	if provider.MaxBytes != nil {
		limit = *provider.MaxBytes
	}
	timeout := defaultSecretFileTimeout
	if provider.TimeoutMs != nil {
		if *provider.TimeoutMs < 1 || *provider.TimeoutMs > int(maxSecretFileTimeout/time.Millisecond) {
			return "", fmt.Errorf("invalid file secret limits")
		}
		timeout = time.Duration(*provider.TimeoutMs) * time.Millisecond
	}
	if limit < 1 || limit > maxSecretFileBytes {
		return "", fmt.Errorf("invalid file secret limits")
	}
	data, err := readPrivateSecretFile(path, int64(limit), timeout)
	if err != nil {
		return "", fmt.Errorf("file secret is unavailable")
	}
	defer secretmem.Wipe(data[:cap(data)])
	data = bytes.TrimPrefix(data, []byte("\xef\xbb\xbf")) // UTF-8 byte order mark.
	var token string
	switch provider.Mode {
	case "singleValue":
		if ref.file != "value" {
			return "", fmt.Errorf("invalid file secret reference")
		}
		if bytes.HasSuffix(data, []byte("\r\n")) {
			data = data[:len(data)-2]
		} else if bytes.HasSuffix(data, []byte("\n")) {
			data = data[:len(data)-1]
		}
		token = strings.ToValidUTF8(string(data), "\uFFFD")
	case "", "json":
		token, err = fileSecretJSONValue(data, ref.file)
		if err != nil {
			return "", fmt.Errorf("file secret is unavailable")
		}
	default:
		return "", fmt.Errorf("unsupported file secret mode")
	}
	if strings.TrimSpace(token) == "" {
		return "", fmt.Errorf("empty bot token")
	}
	return token, nil
}

func validFileSecretID(id string) bool {
	if id == "value" {
		return true
	}
	if !strings.HasPrefix(id, "/") {
		return false
	}
	for i := 1; i < len(id); i++ {
		if id[i] == '~' {
			if i+1 == len(id) || (id[i+1] != '0' && id[i+1] != '1') {
				return false
			}
			i++
		}
	}
	return true
}

// fileSecretJSONValue borrows data. JSON decoding creates strings that Go
// cannot wipe; the caller owns and wipes the original byte buffer.
func fileSecretJSONValue(data []byte, pointer string) (string, error) {
	var root map[string]any
	if err := json.Unmarshal(data, &root); err != nil || root == nil || !strings.HasPrefix(pointer, "/") {
		return "", fmt.Errorf("invalid file secret document")
	}
	var current any = root
	for _, raw := range strings.Split(pointer[1:], "/") {
		token := strings.ReplaceAll(strings.ReplaceAll(raw, "~1", "/"), "~0", "~")
		switch node := current.(type) {
		case map[string]any:
			var ok bool
			current, ok = node[token]
			if !ok {
				return "", fmt.Errorf("file secret not found")
			}
		case []any:
			index, err := strconv.Atoi(token)
			if err != nil || index < 0 || index > maxSecretArrayIndex || strconv.Itoa(index) != token || index >= len(node) {
				return "", fmt.Errorf("file secret not found")
			}
			current = node[index]
		default:
			return "", fmt.Errorf("file secret not found")
		}
	}
	value, ok := current.(string)
	if !ok {
		return "", fmt.Errorf("file secret is not a string")
	}
	return value, nil
}
