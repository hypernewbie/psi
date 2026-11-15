package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	// maxConfigBytes bounds one mcp.json file before JSON decoding.
	maxConfigBytes = 1 << 20
	// maxCommandOutput bounds stdout from a whole-value !command value.
	maxCommandOutput = 64 << 10
	// maxCommandStderr bounds the retained stderr tail for diagnostics.
	maxCommandStderr = 64 << 10
	// commandTimeout bounds one interpolation command.
	commandTimeout = 10 * time.Second
)

// Transport identifies the MCP wire transport for one server.
type Transport string

const (
	TransportStdio Transport = "stdio"
	TransportHTTP  Transport = "http"
)

// Exposure is the effective declaration policy for one tool.
type Exposure string

const (
	ExposureDirect   Exposure = "direct"
	ExposureCodemode Exposure = "codemode"
	ExposureDeferred Exposure = "deferred"
	ExposureHidden   Exposure = "hidden"
)

// ExposureRule maps a raw tool name pattern to an exposure policy.
type ExposureRule struct {
	Pattern  string
	Exposure Exposure
}

// Server is one validated MCP server entry.
type Server struct {
	Name        string
	Transport   Transport
	Command     string
	Args        []string
	Cwd         string
	Env         map[string]string
	URL         string
	Headers     map[string]string
	Enabled     bool
	Description string
	Timeout     time.Duration
	Exposure    Exposure
	ToolRules   []ExposureRule
	// Source records where the effective connection definition came from.
	Source string
	// EnvSource and HeaderSource record per-key provenance ("user" or "project").
	EnvSource    map[string]string
	HeaderSource map[string]string
	// ProjectCommand reports that at least one retained value came from a
	// project file and needs the user-level command opt-in.
	ProjectCommand bool
}

// Config is the accepted merged server configuration.
type Config struct {
	Servers  []Server
	Warnings []string
}

// ConfigError is a safe, classified configuration failure.
type ConfigError struct {
	Class   string
	Message string
}

func (e *ConfigError) Error() string { return e.Message }

func configErr(class, format string, args ...any) error {
	return &ConfigError{Class: class, Message: fmt.Sprintf(format, args...)}
}

// ErrorClass returns a bounded diagnostic class for any error.
func ErrorClass(err error) string {
	if err == nil {
		return ""
	}
	var ce *ConfigError
	if errors.As(err, &ce) {
		return ce.Class
	}
	var le *limitError
	if errors.As(err, &le) {
		return "limit"
	}
	if errors.Is(err, context.Canceled) {
		return "cancelled"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "timeout"
	}
	return "error"
}

type limitError struct{ what string }

func (e *limitError) Error() string { return e.what + " exceeds the configured limit" }

func errLimit(what string) error { return &limitError{what: what} }

// LoadConfig reads the user file, then the project file only when trusted.
// A malformed root fails the whole load. Invalid entries are omitted and
// reported as warnings so valid siblings can load.
func LoadConfig(userPath, projectPath string, projectTrusted bool) (*Config, error) {
	userRaw, err := readConfigFile(userPath)
	if err != nil {
		return nil, err
	}
	var userEntries []rawServer
	var warnings []string
	if userRaw != nil {
		userEntries, warnings, err = parseServers(userRaw, "user")
		if err != nil {
			return nil, err
		}
	}
	entries := userEntries
	if projectTrusted && projectPath != "" {
		projectRaw, readErr := readConfigFile(projectPath)
		if readErr != nil {
			return nil, readErr
		}
		if projectRaw != nil {
			projectEntries, projectWarnings, parseErr := parseServers(projectRaw, "project")
			if parseErr != nil {
				return nil, parseErr
			}
			entries, warnings = mergeServers(entries, projectEntries, warnings, projectWarnings)
		}
	}
	servers, collisionWarnings := resolveNamespaces(entries)
	warnings = append(warnings, collisionWarnings...)
	return &Config{Servers: servers, Warnings: warnings}, nil
}

func readConfigFile(path string) ([]byte, error) {
	if path == "" {
		return nil, nil
	}
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, configErr("io", "cannot read %s", filepath.Base(path))
	}
	defer f.Close()
	data, err := readBounded(f, maxConfigBytes)
	if err != nil {
		return nil, err
	}
	if len(data) == 0 {
		return nil, configErr("json", "%s is empty", filepath.Base(path))
	}
	data = bytes.TrimPrefix(data, []byte("\xef\xbb\xbf"))
	return data, nil
}

func readBounded(r io.Reader, limit int) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(r, int64(limit)+1))
	if err != nil {
		return nil, err
	}
	if len(data) > limit {
		return nil, errLimit("MCP config file")
	}
	return data, nil
}

// rawServer preserves field presence and JSON order.
type rawServer struct {
	name   string
	source string
	fields map[string]*jsonValue
	order  []string
	dup    bool
}

type jsonKind byte

const (
	jsonObject jsonKind = '{'
	jsonArray  jsonKind = '['
	jsonString jsonKind = '"'
	jsonNumber jsonKind = 'n'
	jsonBool   jsonKind = 'b'
	jsonNull   jsonKind = 'z'
)

type jsonMember struct {
	key   string
	value *jsonValue
}

type jsonValue struct {
	kind    jsonKind
	members []jsonMember
	items   []*jsonValue
	str     string
	num     json.Number
	boolean bool
	dup     bool
}

// parseServers parses one file. It returns entry-level warnings, and a
// file-level error for malformed roots or duplicate root/server keys.
func parseServers(data []byte, source string) ([]rawServer, []string, error) {
	root, err := parseJSON(data)
	if err != nil {
		return nil, nil, configErr("json", "malformed mcp.json: %v", err)
	}
	if root.kind != jsonObject {
		return nil, nil, configErr("json", "mcp.json root must be an object")
	}
	if root.dup {
		return nil, nil, configErr("json", "mcp.json has duplicate root keys")
	}
	var serversValue *jsonValue
	for _, member := range root.members {
		if member.key == "mcpServers" {
			serversValue = member.value
		}
	}
	if serversValue == nil || serversValue.kind == jsonNull {
		if serversValue != nil {
			return nil, nil, configErr("json", "mcpServers must be an object")
		}
		return nil, nil, nil
	}
	if serversValue.kind != jsonObject {
		return nil, nil, configErr("json", "mcpServers must be an object")
	}
	if serversValue.dup {
		return nil, nil, configErr("json", "mcpServers has duplicate server names")
	}
	var entries []rawServer
	var warnings []string
	for _, member := range serversValue.members {
		entry, entryErr := parseServerEntry(member.key, member.value, source)
		if entryErr != nil {
			warnings = append(warnings, fmt.Sprintf("mcp server %q: %s", member.key, entryErr.Error()))
			continue
		}
		if entry.has("autoEnableCodemode") {
			warnings = append(warnings, fmt.Sprintf("mcp server %q: autoEnableCodemode is ignored in this MVP", member.key))
		}
		entries = append(entries, *entry)
	}
	return entries, warnings, nil
}

func parseJSON(data []byte) (*jsonValue, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	value, err := parseValue(dec)
	if err != nil {
		return nil, err
	}
	if _, err := dec.Token(); err == nil {
		return nil, errors.New("trailing JSON data")
	} else if !errors.Is(err, io.EOF) {
		return nil, err
	}
	return value, nil
}

func parseValue(dec *json.Decoder) (*jsonValue, error) {
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	switch t := tok.(type) {
	case json.Delim:
		switch t {
		case '{':
			value := &jsonValue{kind: jsonObject}
			seen := map[string]bool{}
			for dec.More() {
				keyTok, err := dec.Token()
				if err != nil {
					return nil, err
				}
				key, ok := keyTok.(string)
				if !ok {
					return nil, errors.New("object key is not a string")
				}
				child, err := parseValue(dec)
				if err != nil {
					return nil, err
				}
				if seen[key] {
					value.dup = true
					continue
				}
				seen[key] = true
				value.members = append(value.members, jsonMember{key: key, value: child})
			}
			if _, err := dec.Token(); err != nil {
				return nil, err
			}
			return value, nil
		case '[':
			value := &jsonValue{kind: jsonArray}
			for dec.More() {
				child, err := parseValue(dec)
				if err != nil {
					return nil, err
				}
				value.items = append(value.items, child)
			}
			if _, err := dec.Token(); err != nil {
				return nil, err
			}
			return value, nil
		default:
			return nil, errors.New("unexpected JSON delimiter")
		}
	case string:
		return &jsonValue{kind: jsonString, str: t}, nil
	case json.Number:
		return &jsonValue{kind: jsonNumber, num: t}, nil
	case bool:
		return &jsonValue{kind: jsonBool, boolean: t}, nil
	case nil:
		return &jsonValue{kind: jsonNull}, nil
	default:
		return nil, errors.New("unsupported JSON token")
	}
}

func (r *rawServer) has(name string) bool { return r.fields[name] != nil }

func parseServerEntry(name string, value *jsonValue, source string) (*rawServer, error) {
	if value.kind != jsonObject {
		return nil, configErr("json", "entry must be an object")
	}
	if value.dup {
		return nil, configErr("json", "entry has duplicate fields")
	}
	if !serverNamePattern.MatchString(name) {
		return nil, configErr("config", "server name must match ^[A-Za-z0-9_-]+$")
	}
	entry := &rawServer{name: name, source: source, fields: map[string]*jsonValue{}, dup: value.dup}
	for _, member := range value.members {
		entry.fields[member.key] = member.value
		entry.order = append(entry.order, member.key)
	}
	return entry, nil
}

var (
	serverNamePattern = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)
	variablePattern   = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)\}`)
)

var knownServerFields = map[string]bool{
	"type": true, "command": true, "args": true, "cwd": true, "env": true,
	"url": true, "headers": true, "enabled": true, "description": true,
	"timeout": true, "exposure": true, "toolExposure": true, "oauth": true,
	"auth": true, "autoEnableCodemode": true,
}

func checkNullFields(raw *rawServer) error {
	for name, value := range raw.fields {
		if knownServerFields[name] && value.kind == jsonNull {
			return configErr("config", "%s must not be null", name)
		}
	}
	return nil
}

func parseExposure(value string) (Exposure, error) {
	switch value {
	case "direct":
		return ExposureDirect, nil
	case "codemode", "codemode-deferred":
		return ExposureCodemode, nil
	case "deferred":
		return ExposureDeferred, nil
	case "hidden":
		return ExposureHidden, nil
	default:
		return "", configErr("config", "unsupported exposure %q", value)
	}
}

func parseTransportEntry(raw *rawServer) (Server, error) {
	if err := checkNullFields(raw); err != nil {
		return Server{}, err
	}
	server := Server{
		Name:         raw.name,
		Source:       raw.source,
		Enabled:      true,
		Timeout:      60 * time.Second,
		Exposure:     ExposureCodemode,
		Env:          map[string]string{},
		Headers:      map[string]string{},
		EnvSource:    map[string]string{},
		HeaderSource: map[string]string{},
	}
	if raw.has("oauth") || raw.has("auth") {
		return Server{}, configErr("unsupported", "OAuth and auth are not supported")
	}
	if value := raw.fields["autoEnableCodemode"]; value != nil {
		// Compatibility-only field. Keep parsing; discovery ignores it.
	}
	if value := raw.fields["type"]; value != nil {
		if value.kind != jsonString {
			return Server{}, configErr("config", "type must be a string")
		}
		switch value.str {
		case "stdio":
			server.Transport = TransportStdio
		case "http", "streamable-http":
			server.Transport = TransportHTTP
		case "sse":
			return Server{}, configErr("unsupported", "legacy SSE transport is not supported")
		default:
			return Server{}, configErr("config", "unsupported transport type")
		}
	}
	if value := raw.fields["command"]; value != nil {
		if value.kind != jsonString || strings.TrimSpace(value.str) == "" {
			return Server{}, configErr("config", "command must be a non-empty string")
		}
		server.Command = value.str
	}
	if value := raw.fields["url"]; value != nil {
		if value.kind != jsonString || strings.TrimSpace(value.str) == "" {
			return Server{}, configErr("config", "url must be a non-empty string")
		}
		server.URL = value.str
	}
	if server.Command != "" && server.URL != "" {
		return Server{}, configErr("config", "entry must not define both command and url")
	}
	if server.Command == "" && server.URL == "" {
		return Server{}, configErr("config", "entry needs command or url")
	}
	if server.Transport == "" {
		if server.Command != "" {
			server.Transport = TransportStdio
		} else {
			server.Transport = TransportHTTP
		}
	}
	if server.Transport == TransportStdio && server.Command == "" {
		return Server{}, configErr("config", "stdio entry needs command")
	}
	if server.Transport == TransportHTTP && server.URL == "" {
		return Server{}, configErr("config", "http entry needs url")
	}
	if server.Transport == TransportHTTP {
		if _, err := validateEndpoint(server.URL); err != nil {
			return Server{}, err
		}
	}
	if value := raw.fields["args"]; value != nil {
		if value.kind != jsonArray {
			return Server{}, configErr("config", "args must be an array of strings")
		}
		for _, item := range value.items {
			if item.kind != jsonString {
				return Server{}, configErr("config", "args must be an array of strings")
			}
			server.Args = append(server.Args, item.str)
		}
	}
	if value := raw.fields["cwd"]; value != nil {
		if value.kind != jsonString {
			return Server{}, configErr("config", "cwd must be a string")
		}
		server.Cwd = value.str
	}
	if value := raw.fields["env"]; value != nil {
		if value.kind != jsonObject || value.dup {
			return Server{}, configErr("config", "env must be an object with unique string values")
		}
		for _, member := range value.members {
			if member.value.kind != jsonString {
				return Server{}, configErr("config", "env must be an object with unique string values")
			}
			server.Env[member.key] = member.value.str
			server.EnvSource[member.key] = raw.source
		}
	}
	if value := raw.fields["headers"]; value != nil {
		if value.kind != jsonObject || value.dup {
			return Server{}, configErr("config", "headers must be an object with unique string values")
		}
		for _, member := range value.members {
			if member.value.kind != jsonString {
				return Server{}, configErr("config", "headers must be an object with unique string values")
			}
			if !validHeaderName(member.key) {
				return Server{}, configErr("config", "invalid header name")
			}
			if !validHeaderValue(member.value.str) {
				return Server{}, configErr("config", "header %q contains an invalid value", member.key)
			}
			if reservedHeaders[strings.ToLower(member.key)] {
				return Server{}, configErr("config", "header %q is reserved by the MCP transport", member.key)
			}
			lower := strings.ToLower(member.key)
			for existing := range server.Headers {
				if strings.ToLower(existing) == lower {
					return Server{}, configErr("config", "duplicate header name")
				}
			}
			server.Headers[member.key] = member.value.str
			server.HeaderSource[member.key] = raw.source
		}
	}
	if value := raw.fields["enabled"]; value != nil {
		if value.kind != jsonBool {
			return Server{}, configErr("config", "enabled must be a boolean")
		}
		server.Enabled = value.boolean
	}
	if value := raw.fields["description"]; value != nil {
		if value.kind != jsonString {
			return Server{}, configErr("config", "description must be a string")
		}
		server.Description = value.str
	}
	if value := raw.fields["timeout"]; value != nil {
		if value.kind != jsonNumber {
			return Server{}, configErr("config", "timeout must be a positive number of seconds")
		}
		seconds, err := strconv.ParseFloat(value.num.String(), 64)
		if err != nil || math.IsNaN(seconds) || math.IsInf(seconds, 0) || seconds <= 0 || seconds > float64(math.MaxInt64)/float64(time.Second) {
			return Server{}, configErr("config", "timeout must be a positive finite number of seconds")
		}
		server.Timeout = time.Duration(seconds * float64(time.Second))
		if server.Timeout <= 0 {
			return Server{}, configErr("config", "timeout is too small")
		}
		if server.Timeout > time.Duration(math.MaxInt64/10) {
			return Server{}, configErr("config", "timeout is too large for the hard deadline")
		}
	}
	if value := raw.fields["exposure"]; value != nil {
		if value.kind != jsonString {
			return Server{}, configErr("config", "exposure must be a string")
		}
		exposure, err := parseExposure(value.str)
		if err != nil {
			return Server{}, err
		}
		server.Exposure = exposure
	}
	if value := raw.fields["toolExposure"]; value != nil {
		if value.kind != jsonObject || value.dup {
			return Server{}, configErr("config", "toolExposure must be an object with unique exposure values")
		}
		for _, member := range value.members {
			if member.value.kind != jsonString {
				return Server{}, configErr("config", "toolExposure must be an object with unique exposure values")
			}
			exposure, err := parseExposure(member.value.str)
			if err != nil {
				return Server{}, err
			}
			server.ToolRules = append(server.ToolRules, ExposureRule{Pattern: member.key, Exposure: exposure})
		}
	}
	for _, key := range raw.order {
		if !knownServerFields[key] {
			continue
		}
	}
	for key, source := range server.EnvSource {
		if source == "project" && isCommand(server.Env[key]) {
			server.ProjectCommand = true
		}
	}
	for key, source := range server.HeaderSource {
		if source == "project" && isCommand(server.Headers[key]) {
			server.ProjectCommand = true
		}
	}
	return server, nil
}

func isCommand(value string) bool { return strings.HasPrefix(strings.TrimSpace(value), "!") }

func validHeaderName(name string) bool {
	if name == "" {
		return false
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		if c <= ' ' || c >= 0x7f || strings.ContainsRune("()<>@,;:\\\"/[]?={}", rune(c)) {
			return false
		}
	}
	return true
}

func validHeaderValue(value string) bool {
	for i := 0; i < len(value); i++ {
		if value[i] == '\r' || value[i] == '\n' || value[i] == 0 {
			return false
		}
	}
	return true
}

func isPartialEntry(raw *rawServer) bool {
	if raw.has("command") || raw.has("url") || raw.has("type") {
		return false
	}
	for _, key := range raw.order {
		if key != "enabled" && key != "exposure" && key != "toolExposure" {
			return false
		}
	}
	return true
}

func mergeServers(user, project []rawServer, userWarnings, projectWarnings []string) ([]rawServer, []string) {
	warnings := append(append([]string(nil), userWarnings...), projectWarnings...)
	merged := append([]rawServer(nil), user...)
	index := map[string]int{}
	for i := range merged {
		index[merged[i].name] = i
	}
	for _, raw := range project {
		base, exists := index[raw.name]
		if isPartialEntry(&raw) {
			if !exists {
				warnings = append(warnings, fmt.Sprintf("mcp server %q: project override has no user base", raw.name))
				continue
			}
			if err := applyPartialOverride(&merged[base], raw); err != nil {
				warnings = append(warnings, fmt.Sprintf("mcp server %q: %s", raw.name, err.Error()))
			}
			continue
		}
		// A full project entry replaces the user entry, even when it is
		// invalid. The final validation pass then withdraws the changed entry
		// instead of silently keeping the old connection definition.
		if exists {
			merged[base] = raw
		} else {
			index[raw.name] = len(merged)
			merged = append(merged, raw)
		}
	}
	return merged, warnings
}

func applyPartialOverride(base *rawServer, override rawServer) error {
	for _, key := range override.order {
		switch key {
		case "enabled", "exposure", "toolExposure":
		default:
			return configErr("config", "project override may set only enabled, exposure, and toolExposure")
		}
	}
	for _, key := range override.order {
		base.fields[key] = override.fields[key]
		if !containsString(base.order, key) {
			base.order = append(base.order, key)
		}
	}
	return nil
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func resolveNamespaces(entries []rawServer) ([]Server, []string) {
	var servers []Server
	var warnings []string
	owners := map[string]string{}
	for _, raw := range entries {
		server, err := parseTransportEntry(&raw)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("mcp server %q: %s", raw.name, err.Error()))
			continue
		}
		// A full project replacement already validated above; validate again
		// for user entries and partial results.
		namespace := strings.ReplaceAll(server.Name, "-", "_")
		if owner, ok := owners[namespace]; ok && owner != server.Name {
			warnings = append(warnings, fmt.Sprintf("mcp server %q: namespace conflicts with %q", server.Name, owner))
			continue
		}
		owners[namespace] = server.Name
		servers = append(servers, server)
	}
	return servers, warnings
}

// Resolve interpolates env and header values once for a setup attempt.
// Command values from a project file are refused without the user opt-in.
func Resolve(ctx context.Context, server Server, allowProjectCommands bool) (Server, error) {
	resolved := server
	resolved.Env = make(map[string]string, len(server.Env))
	resolved.Headers = make(map[string]string, len(server.Headers))
	for key, value := range server.Env {
		out, err := resolveValue(ctx, value, server.EnvSource[key] == "project", allowProjectCommands)
		if err != nil {
			return Server{}, fmt.Errorf("env %s: %w", key, err)
		}
		resolved.Env[key] = out
	}
	for key, value := range server.Headers {
		out, err := resolveValue(ctx, value, server.HeaderSource[key] == "project", allowProjectCommands)
		if err != nil {
			return Server{}, fmt.Errorf("header %s: %w", key, err)
		}
		if !validHeaderValue(out) {
			return Server{}, configErr("config", "header %s contains an invalid value", key)
		}
		resolved.Headers[key] = out
	}
	return resolved, nil
}

func resolveValue(ctx context.Context, value string, fromProject, allowProjectCommands bool) (string, error) {
	trimmed := strings.TrimSpace(value)
	if strings.HasPrefix(trimmed, "!") {
		if fromProject && !allowProjectCommands {
			return "", configErr("permission", "project command interpolation is disabled; set mcp.allowProjectCommands in user settings")
		}
		command := strings.TrimSpace(strings.TrimPrefix(trimmed, "!"))
		if command == "" {
			return "", configErr("command", "empty interpolation command")
		}
		out, err := runInterpolationCommand(ctx, command)
		if err != nil {
			return "", err
		}
		return out, nil
	}
	var missing string
	out := variablePattern.ReplaceAllStringFunc(value, func(match string) string {
		name := variablePattern.FindStringSubmatch(match)[1]
		replacement, ok := os.LookupEnv(name)
		if !ok {
			if missing == "" {
				missing = name
			}
			return ""
		}
		return replacement
	})
	if missing != "" {
		return "", configErr("missing-env", "environment variable %s is not set", missing)
	}
	return out, nil
}

func runInterpolationCommand(parent context.Context, command string) (string, error) {
	ctx, cancel := context.WithTimeout(parent, commandTimeout)
	defer cancel()
	shell := os.Getenv("PSI_SHELL")
	if strings.TrimSpace(shell) == "" {
		shell = "bash"
	}
	cmd := exec.Command(shell, "-c", command)
	cmd.Env = os.Environ()
	stdout := &limitedBuffer{limit: maxCommandOutput}
	stderr := &ringBuffer{limit: maxCommandStderr}
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	prepareCommand(cmd)
	if err := cmd.Start(); err != nil {
		return "", configErr("command", "cannot start interpolation command")
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			return "", configErr("command", "interpolation command failed")
		}
	case <-ctx.Done():
		terminateCommand(cmd)
		<-done
		return "", configErr("timeout", "interpolation command timed out")
	}
	out := strings.TrimRight(stdout.String(), "\r\n")
	if stdout.overflow {
		return "", errLimit("interpolation command output")
	}
	return out, nil
}

// limitedBuffer retains at most limit bytes and records overflow.
type limitedBuffer struct {
	buf      bytes.Buffer
	limit    int
	overflow bool
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	if b.buf.Len()+len(p) > b.limit {
		remaining := b.limit - b.buf.Len()
		if remaining > 0 {
			b.buf.Write(p[:remaining])
		}
		b.overflow = true
		return len(p), nil
	}
	return b.buf.Write(p)
}

func (b *limitedBuffer) String() string { return b.buf.String() }

// ringBuffer retains only the most recent limit bytes.
type ringBuffer struct {
	data  []byte
	limit int
}

func (r *ringBuffer) Write(p []byte) (int, error) {
	if r.limit <= 0 {
		return len(p), nil
	}
	if len(p) >= r.limit {
		r.data = append(r.data[:0], p[len(p)-r.limit:]...)
		return len(p), nil
	}
	if len(r.data)+len(p) > r.limit {
		drop := len(r.data) + len(p) - r.limit
		r.data = append(r.data[:0], r.data[drop:]...)
	}
	r.data = append(r.data, p...)
	return len(p), nil
}

func (r *ringBuffer) String() string { return string(r.data) }

// SortedNames returns server names in stable order.
func (c *Config) SortedNames() []string {
	names := make([]string, 0, len(c.Servers))
	for _, server := range c.Servers {
		names = append(names, server.Name)
	}
	sort.Strings(names)
	return names
}
