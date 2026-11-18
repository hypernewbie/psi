// Package webaccess implements the native web search and fetch tools.
//
// The tools read the pi-web-access plugin's web-search.json for compatibility.
// Network access is direct: HTTP(S) text fetch with an address guard, SearXNG
// JSON search, and Exa's direct or keyless MCP endpoint.
package webaccess

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"golang.org/x/net/http/httpguts"
)

const (
	// DefaultInlineChars mirrors the reference default for inline content.
	DefaultInlineChars = 30000
	minInlineChars     = 1000
	maxInlineChars     = 200000

	// DefaultCacheEntries and DefaultCacheBytes bound the in-memory store.
	DefaultCacheEntries = 64
	DefaultCacheBytes   = 64 << 20

	maxConfigBytes = 1 << 20
)

// Config is the effective, validated web-access configuration. Diagnostics
// contain no configuration values, only key names and rule descriptions.
type Config struct {
	Provider          string // auto|searxng|exa
	SearXNGBaseURL    string
	SearXNGHeaders    http.Header
	ExaAPIKey         string
	ExaBaseURL        string
	AllowHTTPBaseURLs bool

	SearchEnabled  bool
	FetchEnabled   bool
	ContentEnabled bool

	SearXNGAvailable bool
	SearXNGRequested bool // endpoint present, even if invalid
	ExaAvailable     bool

	FetchMode           string
	FetchModes          []string
	FetchHeaderProfile  string // psi|chrome|edge|firefox|safari
	FetchAcceptLanguage string // optional Accept-Language, empty omits the header
	MaxInlineChars      int

	AllowRanges []netip.Prefix

	CacheMaxEntries int
	CacheMaxBytes   int64

	Diagnostics []string
}

// Env isolates filesystem and environment access so tests never read the
// developer's home directory.
type Env struct {
	HomeDir   string
	LookupEnv func(string) (string, bool)
	Stat      func(string) (os.FileInfo, error)
	ReadFile  func(string) ([]byte, error)
}

// OSEnv returns the process environment.
func OSEnv() Env {
	home, _ := os.UserHomeDir()
	return Env{
		HomeDir:   home,
		LookupEnv: os.LookupEnv,
		Stat:      os.Stat,
		ReadFile:  os.ReadFile,
	}
}

func (e Env) lookup(key string) (string, bool) {
	if e.LookupEnv == nil {
		return "", false
	}
	return e.LookupEnv(key)
}

func (e Env) exists(path string) bool {
	if e.Stat == nil {
		return false
	}
	_, err := e.Stat(path)
	return err == nil
}

// ResolvePath returns the full plugin configuration path using the reference
// order. PSI_CODING_AGENT_DIR intentionally does not affect this file.
func ResolvePath(env Env) string {
	if dir, ok := env.lookup("PI_CODING_AGENT_DIR"); ok && dir != "" {
		return filepath.Join(dir, "web-search.json")
	}
	if xdg, ok := env.lookup("XDG_CONFIG_HOME"); ok && xdg != "" {
		path := filepath.Join(xdg, "pi", "web-search.json")
		if env.exists(path) {
			return path
		}
		legacy := filepath.Join(env.HomeDir, ".pi", "web-search.json")
		if env.exists(legacy) {
			return legacy
		}
		return path
	}
	agent := filepath.Join(env.HomeDir, ".pi", "agent", "web-search.json")
	if env.exists(agent) {
		return agent
	}
	legacy := filepath.Join(env.HomeDir, ".pi", "web-search.json")
	if env.exists(legacy) {
		return legacy
	}
	return agent
}

// Load resolves, reads, and validates the plugin configuration. A missing file
// yields defaults. Any non-missing read error disables every web tool.
func Load(env Env) (Config, []string) {
	path := ResolvePath(env)
	cfg, _ := defaultConfig()
	if env.ReadFile == nil {
		return disableAll(cfg, "web access: cannot read web-search.json")
	}
	if env.Stat != nil {
		if info, err := env.Stat(path); err == nil && info != nil && info.Size() > maxConfigBytes {
			return disableAll(cfg, "web access: web-search.json exceeds 1 MiB")
		}
	}
	data, err := env.ReadFile(path)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return disableAll(cfg, "web access: cannot read web-search.json")
		}
		parsed, _ := ParseConfig([]byte("{}"), env)
		return parsed, parsed.Diagnostics
	}
	parsed, _ := ParseConfig(data, env)
	return parsed, parsed.Diagnostics
}

func defaultConfig() (Config, []string) {
	cfg := Config{
		Provider:           "auto",
		ExaBaseURL:         "https://api.exa.ai",
		SearchEnabled:      true,
		FetchEnabled:       true,
		ContentEnabled:     true,
		FetchMode:          "readable",
		FetchModes:         []string{"readable", "raw"},
		FetchHeaderProfile: "psi",
		MaxInlineChars:     DefaultInlineChars,
		CacheMaxEntries:    DefaultCacheEntries,
		CacheMaxBytes:      DefaultCacheBytes,
		ExaAvailable:       true,
	}
	return cfg, nil
}

func finalize(cfg *Config) {
	if cfg.SearXNGBaseURL != "" {
		cfg.SearXNGAvailable = true
	}
}

func disableAll(cfg Config, reason string) (Config, []string) {
	cfg.SearchEnabled = false
	cfg.FetchEnabled = false
	cfg.ContentEnabled = false
	cfg.Diagnostics = append(cfg.Diagnostics, reason)
	return cfg, cfg.Diagnostics
}

// ParseConfig validates one configuration document. The returned diagnostics
// repeat the disabling failures.
func ParseConfig(data []byte, env Env) (Config, []string) {
	cfg, diags := defaultConfig()
	if len(data) > maxConfigBytes {
		return disableAll(cfg, "web access: web-search.json exceeds 1 MiB")
	}
	root, err := decodeObject(data)
	if err != nil {
		return disableAll(cfg, "web access: web-search.json must be one JSON object")
	}

	var shared, fetchErrs, routeErrs, providerErrs []string

	// Shared values: enablement, HTTP permission, inline limit, exemptions, cache.
	if override, ok := env.lookup("PI_WEB_ACCESS_ALLOW_HTTP_BASE_URLS"); ok {
		switch strings.ToLower(strings.TrimSpace(override)) {
		case "true", "1":
			cfg.AllowHTTPBaseURLs = true
		case "false", "0":
			cfg.AllowHTTPBaseURLs = false
		default:
			shared = append(shared, "web access: PI_WEB_ACCESS_ALLOW_HTTP_BASE_URLS must be true, false, 1, or 0")
		}
	} else if raw, ok := root["allowHttpBaseUrls"]; ok {
		if jsonNull(raw) {
			shared = append(shared, "web access: allowHttpBaseUrls must be a boolean")
		} else {
			var v bool
			if err := json.Unmarshal(raw, &v); err != nil {
				shared = append(shared, "web access: allowHttpBaseUrls must be a boolean")
			} else {
				cfg.AllowHTTPBaseURLs = v
			}
		}
	}
	if raw, ok := root["maxInlineContentChars"]; ok {
		n, err := parseInt(raw)
		switch {
		case err != nil:
			shared = append(shared, "web access: maxInlineContentChars must be a non-negative integer")
		case n < minInlineChars:
			cfg.MaxInlineChars = DefaultInlineChars
		case n > maxInlineChars:
			cfg.MaxInlineChars = maxInlineChars
		default:
			cfg.MaxInlineChars = int(n)
		}
	}
	if raw, ok := root["ssrf"]; ok {
		if err := applySSRF(&cfg, raw); err != nil {
			shared = append(shared, "web access: ssrf.allowRanges must be an array of valid CIDRs")
		}
	}
	if raw, ok := root["cache"]; ok {
		if err := applyCache(&cfg, raw); err != nil {
			shared = append(shared, err.Error())
		}
	}
	if raw, ok := root["webSearch"]; ok {
		enabled, err := decodeEnabled(raw)
		if err != nil {
			shared = append(shared, "web access: webSearch must be an object with a boolean enabled")
		} else if enabled != nil {
			cfg.SearchEnabled = *enabled
		}
	}
	webSearchOverride, fetchOverride, contentOverride, toolsErr := decodeTools(root["tools"])
	if toolsErr != nil {
		shared = append(shared, "web access: tools must be an object with boolean enabled switches")
	}
	if shared == nil {
		if webSearchOverride != nil {
			cfg.SearchEnabled = *webSearchOverride
		}
		if fetchOverride != nil {
			cfg.FetchEnabled = *fetchOverride
		}
		if contentOverride != nil {
			cfg.ContentEnabled = *contentOverride
		}
	}

	// Fetch modes and page headers.
	if raw, ok := root["fetch"]; ok {
		settings, err := decodeFetch(raw)
		if err != nil {
			fetchErrs = append(fetchErrs, err.Error())
		} else {
			effective := intersectModes(settings.modes)
			if len(effective) == 0 {
				fetchErrs = append(fetchErrs, "web access: fetch.allowedModes has no supported mode")
			} else if settings.defSet && !contains(effective, settings.def) {
				fetchErrs = append(fetchErrs, "web access: fetch.defaultMode is not in the allowed modes")
			} else {
				cfg.FetchModes = effective
				if settings.defSet {
					cfg.FetchMode = settings.def
				} else if !contains(effective, cfg.FetchMode) {
					cfg.FetchMode = effective[0]
				}
				if settings.profile != "" {
					cfg.FetchHeaderProfile = settings.profile
				}
				cfg.FetchAcceptLanguage = settings.language
			}
		}
	}

	// Search routing.
	providerKey := "provider"
	raw, ok := root["provider"]
	if !ok {
		providerKey = "searchProvider"
		raw, ok = root[providerKey]
	}
	if ok {
		var v string
		if err := json.Unmarshal(raw, &v); err != nil {
			routeErrs = append(routeErrs, "web access: provider must be one string")
		} else {
			switch v {
			case "auto", "searxng", "exa":
				cfg.Provider = v
			default:
				routeErrs = append(routeErrs, "web access: provider must be auto, searxng, or exa")
			}
		}
	}

	// Provider settings.
	applySearXNG(&cfg, root, env, &providerErrs)
	applyExa(&cfg, root, env, &providerErrs)

	if len(shared) > 0 {
		cfg.SearchEnabled, cfg.FetchEnabled, cfg.ContentEnabled = false, false, false
	}
	if len(fetchErrs) > 0 {
		cfg.FetchEnabled = false
	}
	if len(routeErrs) > 0 {
		cfg.SearchEnabled = false
	}
	finalize(&cfg)
	// Providers with an error stay unavailable.
	for _, d := range providerErrs {
		cfg.Diagnostics = append(cfg.Diagnostics, d)
	}
	cfg.Diagnostics = append(cfg.Diagnostics, shared...)
	cfg.Diagnostics = append(cfg.Diagnostics, fetchErrs...)
	cfg.Diagnostics = append(cfg.Diagnostics, routeErrs...)
	diags = append(diags, cfg.Diagnostics...)
	return cfg, diags
}

func jsonNull(raw json.RawMessage) bool {
	return strings.TrimSpace(string(raw)) == "null"
}

func decodeObject(data []byte) (map[string]json.RawMessage, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	root := map[string]json.RawMessage{}
	if err := dec.Decode(&root); err != nil {
		return nil, err
	}
	if root == nil {
		return nil, errors.New("not an object")
	}
	var extra any
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		return nil, errors.New("trailing data")
	}
	return root, nil
}

func parseInt(raw json.RawMessage) (int64, error) {
	var n json.Number
	if err := json.Unmarshal(raw, &n); err != nil {
		return 0, err
	}
	v, err := strconv.ParseInt(n.String(), 10, 64)
	if err != nil {
		return 0, err
	}
	return v, nil
}

func decodeEnabled(raw json.RawMessage) (*bool, error) {
	var obj map[string]json.RawMessage
	if len(raw) == 0 || raw[0] != '{' {
		return nil, errors.New("not an object")
	}
	if err := json.Unmarshal(raw, &obj); err != nil {
		return nil, err
	}
	value, ok := obj["enabled"]
	if !ok {
		return nil, nil
	}
	if jsonNull(value) {
		return nil, errors.New("not a boolean")
	}
	var v bool
	if err := json.Unmarshal(value, &v); err != nil {
		return nil, err
	}
	return &v, nil
}

func decodeTools(raw json.RawMessage) (webSearch, fetch, content *bool, err error) {
	if len(raw) == 0 {
		return nil, nil, nil, nil
	}
	var obj map[string]json.RawMessage
	if len(raw) == 0 || raw[0] != '{' {
		return nil, nil, nil, errors.New("not an object")
	}
	if err := json.Unmarshal(raw, &obj); err != nil {
		return nil, nil, nil, err
	}
	group := func(key string) (*bool, error) {
		value, ok := obj[key]
		if !ok {
			return nil, nil
		}
		var entry map[string]json.RawMessage
		if err := json.Unmarshal(value, &entry); err != nil {
			return nil, err
		}
		if entry == nil {
			return nil, errors.New("not an object")
		}
		enabled, ok := entry["enabled"]
		if !ok {
			return nil, nil
		}
		if jsonNull(enabled) {
			return nil, errors.New("not a boolean")
		}
		var v bool
		if err := json.Unmarshal(enabled, &v); err != nil {
			return nil, err
		}
		return &v, nil
	}
	if webSearch, err = group("webSearch"); err != nil {
		return nil, nil, nil, err
	}
	if fetch, err = group("fetchContent"); err != nil {
		return nil, nil, nil, err
	}
	if content, err = group("getSearchContent"); err != nil {
		return nil, nil, nil, err
	}
	return webSearch, fetch, content, nil
}

func applySSRF(cfg *Config, raw json.RawMessage) error {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil || obj == nil {
		return errors.New("not an object")
	}
	ranges, ok := obj["allowRanges"]
	if !ok {
		return nil
	}
	if jsonNull(ranges) {
		return errors.New("not an array")
	}
	var list []string
	if err := json.Unmarshal(ranges, &list); err != nil {
		return err
	}
	var out []netip.Prefix
	for _, item := range list {
		prefix, err := netip.ParsePrefix(strings.TrimSpace(item))
		if err != nil {
			return err
		}
		if prefix.Addr().Is4In6() && prefix.Bits() <= 96 {
			return errors.New("mapped IPv4 range is too broad")
		}
		out = append(out, normalizePrefix(prefix))
	}
	cfg.AllowRanges = out
	return nil
}

func normalizePrefix(prefix netip.Prefix) netip.Prefix {
	addr := prefix.Addr()
	if addr.Is4In6() {
		bits := prefix.Bits() - 96
		if bits < 0 {
			return netip.Prefix{}
		}
		return netip.PrefixFrom(addr.Unmap(), bits)
	}
	return prefix
}

func applyCache(cfg *Config, raw json.RawMessage) error {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil || obj == nil {
		return errors.New("web access: cache must be an object with positive integer limits")
	}
	if value, ok := obj["maxEntries"]; ok {
		n, err := parseInt(value)
		if err != nil || n <= 0 || n > int64(int(^uint(0)>>1)) {
			return errors.New("web access: cache.maxEntries must be a positive integer")
		}
		cfg.CacheMaxEntries = int(n)
	}
	if value, ok := obj["maxBytes"]; ok {
		n, err := parseInt(value)
		if err != nil || n <= 0 {
			return errors.New("web access: cache.maxBytes must be a positive integer")
		}
		cfg.CacheMaxBytes = n
	}
	return nil
}

type fetchSettings struct {
	modes    []string
	def      string
	defSet   bool
	profile  string
	language string
}

func decodeFetch(raw json.RawMessage) (fetchSettings, error) {
	var out fetchSettings
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil || obj == nil {
		return fetchSettings{}, errors.New("web access: fetch must be an object")
	}
	out.modes = []string{"readable", "raw"}
	if value, ok := obj["allowedModes"]; ok {
		if jsonNull(value) {
			return fetchSettings{}, errors.New("web access: fetch.allowedModes must be a string array")
		}
		var list []string
		if err := json.Unmarshal(value, &list); err != nil {
			return fetchSettings{}, errors.New("web access: fetch.allowedModes must be a string array")
		}
		out.modes = list
	}
	if value, ok := obj["defaultMode"]; ok {
		if jsonNull(value) {
			return fetchSettings{}, errors.New("web access: fetch.defaultMode must be one string")
		}
		if err := json.Unmarshal(value, &out.def); err != nil {
			return fetchSettings{}, errors.New("web access: fetch.defaultMode must be one string")
		}
		out.defSet = true
	}
	if value, ok := obj["headerProfile"]; ok {
		if jsonNull(value) || json.Unmarshal(value, &out.profile) != nil {
			return fetchSettings{}, errors.New("web access: fetch.headerProfile must be one string")
		}
		if !validHeaderProfile(out.profile) {
			return fetchSettings{}, errors.New("web access: fetch.headerProfile must be psi, chrome, edge, firefox, or safari")
		}
	}
	if value, ok := obj["acceptLanguage"]; ok {
		var lang string
		if jsonNull(value) || json.Unmarshal(value, &lang) != nil {
			return fetchSettings{}, errors.New("web access: fetch.acceptLanguage must be one string")
		}
		if httpguts.ValidHeaderFieldValue(lang) {
			// Trim HTTP optional whitespace: spaces and horizontal tabs.
			out.language = strings.Trim(lang, " \t")
		}
		if out.language == "" {
			return fetchSettings{}, errors.New("web access: fetch.acceptLanguage must be one valid header value")
		}
	}
	return out, nil
}

func intersectModes(modes []string) []string {
	var out []string
	for _, mode := range modes {
		if (mode == "readable" || mode == "raw") && !contains(out, mode) {
			out = append(out, mode)
		}
	}
	return out
}

func contains(list []string, value string) bool {
	for _, item := range list {
		if item == value {
			return true
		}
	}
	return false
}

func applySearXNG(cfg *Config, root map[string]json.RawMessage, env Env, diags *[]string) {
	base := ""
	value, overrideSet := env.lookup("SEARXNG_BASE_URL")
	if overrideSet {
		base = value
		if base == "" {
			cfg.SearXNGBaseURL = ""
			return
		}
		cfg.SearXNGRequested = true
	} else if raw, ok := root["searxngBaseUrl"]; ok {
		if jsonNull(raw) {
			cfg.SearXNGRequested = true
			*diags = append(*diags, "web access: searxngBaseUrl must be one string")
			cfg.SearXNGBaseURL = ""
			return
		}
		if err := json.Unmarshal(raw, &base); err != nil {
			cfg.SearXNGRequested = true
			*diags = append(*diags, "web access: searxngBaseUrl must be one string")
			cfg.SearXNGBaseURL = ""
			return
		}
		if base != "" {
			cfg.SearXNGRequested = true
		}
	}
	if base == "" {
		return
	}
	normalized, err := normalizeBaseURL(base, cfg.AllowHTTPBaseURLs)
	if err != nil {
		*diags = append(*diags, "web access: searxngBaseUrl must be an absolute HTTP(S) base URL without credentials, query, or fragment")
		cfg.SearXNGBaseURL = ""
		return
	}
	cfg.SearXNGBaseURL = normalized

	if raw, ok := root["searxngHeaders"]; ok {
		headers, err := decodeHeaders(raw)
		if err != nil {
			*diags = append(*diags, err.Error())
			cfg.SearXNGBaseURL = ""
			return
		}
		cfg.SearXNGHeaders = headers
	}
}

func applyExa(cfg *Config, root map[string]json.RawMessage, env Env, diags *[]string) {
	base := ""
	override, overrideSet := env.lookup("EXA_BASE_URL")
	if overrideSet {
		base = override
		if base == "" {
			*diags = append(*diags, "web access: EXA_BASE_URL must not be empty")
			cfg.ExaAvailable = false
			return
		}
	} else if raw, ok := root["exaBaseUrl"]; ok {
		if jsonNull(raw) {
			*diags = append(*diags, "web access: exaBaseUrl must be one string")
			cfg.ExaAvailable = false
			return
		}
		if err := json.Unmarshal(raw, &base); err != nil {
			*diags = append(*diags, "web access: exaBaseUrl must be one string")
			cfg.ExaAvailable = false
			return
		}
	}
	if base == "" {
		base = "https://api.exa.ai"
	}
	normalized, err := normalizeBaseURL(base, cfg.AllowHTTPBaseURLs)
	if err != nil {
		*diags = append(*diags, "web access: exaBaseUrl must be an absolute HTTP(S) base URL without credentials, query, or fragment")
		cfg.ExaAvailable = false
		return
	}
	cfg.ExaBaseURL = normalized

	key := ""
	if raw, ok := root["exaApiKey"]; ok {
		if jsonNull(raw) {
			*diags = append(*diags, "web access: exaApiKey must be one string")
			cfg.ExaAvailable = false
			return
		}
		if err := json.Unmarshal(raw, &key); err != nil {
			*diags = append(*diags, "web access: exaApiKey must be one string")
			cfg.ExaAvailable = false
			return
		}
	}
	resolved, err := resolveCredential(key, env)
	if err != nil {
		*diags = append(*diags, err.Error())
		cfg.ExaAvailable = false
		return
	}
	if value, ok := env.lookup("EXA_API_KEY"); ok && value != "" && !strings.HasPrefix(key, "$") {
		resolved = value
	}
	cfg.ExaAPIKey = resolved
}

// resolveCredential supports literal values and $NAME references. Command
// credentials are refused; psi never executes a command to obtain a key.
func resolveCredential(value string, env Env) (string, error) {
	if value == "" {
		return "", nil
	}
	if strings.HasPrefix(value, "!") {
		return "", errors.New("web access: exaApiKey command credentials are not supported")
	}
	if strings.HasPrefix(value, "$") {
		name := strings.TrimPrefix(value, "$")
		if name == "" {
			return "", errors.New("web access: exaApiKey has an empty variable reference")
		}
		resolved, ok := env.lookup(name)
		if !ok || resolved == "" {
			return "", errors.New("web access: exaApiKey references a missing environment variable")
		}
		return resolved, nil
	}
	return value, nil
}

func decodeHeaders(raw json.RawMessage) (http.Header, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
		return nil, errors.New("web access: searxngHeaders must be an object of string values")
	}
	headers := http.Header{}
	seen := map[string]bool{}
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return nil, errors.New("web access: searxngHeaders must be an object of string values")
		}
		name, ok := tok.(string)
		if !ok {
			return nil, errors.New("web access: searxngHeaders must be an object of string values")
		}
		lower := strings.ToLower(name)
		if seen[lower] {
			return nil, errors.New("web access: searxngHeaders must not repeat a header name")
		}
		seen[lower] = true
		var value string
		if err := dec.Decode(&value); err != nil {
			return nil, errors.New("web access: searxngHeaders values must be strings")
		}
		if !httpguts.ValidHeaderFieldName(name) {
			return nil, errors.New("web access: searxngHeaders has an invalid header name")
		}
		if !httpguts.ValidHeaderFieldValue(value) {
			return nil, errors.New("web access: searxngHeaders has an invalid header value")
		}
		if forbiddenHeader(lower) {
			return nil, errors.New("web access: searxngHeaders contains a forbidden header")
		}
		headers.Set(name, value)
	}
	if tok, err := dec.Token(); err != nil || tok != json.Delim('}') {
		return nil, errors.New("web access: searxngHeaders must be an object of string values")
	}
	return headers, nil
}

func forbiddenHeader(name string) bool {
	switch name {
	case "connection", "proxy-connection", "keep-alive", "proxy-authenticate",
		"proxy-authorization", "te", "trailer", "transfer-encoding", "upgrade",
		"host", "content-length", "accept-encoding":
		return true
	}
	return false
}

func normalizeBaseURL(raw string, allowHTTP bool) (string, error) {
	raw = strings.TrimSpace(raw)
	if strings.Contains(raw, "#") {
		return "", errors.New("components")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", err
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		return "", errors.New("scheme")
	}
	if u.Host == "" || u.Hostname() == "" {
		return "", errors.New("host")
	}
	if u.User != nil || u.ForceQuery || u.RawQuery != "" || u.Fragment != "" {
		return "", errors.New("components")
	}
	if strings.HasSuffix(u.Host, ":") {
		return "", errors.New("port")
	}
	if port := u.Port(); port != "" {
		value, err := strconv.Atoi(port)
		if err != nil || value < 1 || value > 65535 {
			return "", errors.New("port")
		}
	}
	if scheme == "http" && !allowHTTP && !isLoopbackHost(u.Hostname()) {
		return "", errors.New("http")
	}
	path := strings.TrimRight(u.EscapedPath(), "/")
	if path == "/" {
		path = ""
	}
	return scheme + "://" + u.Host + path, nil
}

func isLoopbackHost(host string) bool {
	host = strings.TrimSuffix(strings.ToLower(host), ".")
	if host == "localhost" {
		return true
	}
	addr, err := netip.ParseAddr(host)
	return err == nil && addr.Unmap().IsLoopback()
}

func (c Config) searxngConfigured() bool { return c.SearXNGAvailable && c.SearXNGBaseURL != "" }

// SearchProvider resolves auto to a concrete provider. An explicit provider is
// returned even when unavailable; callers must check availability. A configured
// but invalid SearXNG is returned rather than falling back to Exa.
func (c Config) SearchProvider() string {
	if c.Provider != "auto" {
		return c.Provider
	}
	if c.searxngConfigured() {
		return "searxng"
	}
	// A malformed SearXNG configuration must not make auto silently use Exa.
	if c.SearXNGRequested {
		return "searxng"
	}
	return "exa"
}

// ProviderError reports why a selected provider cannot run.
func (c Config) ProviderError(name string) error {
	switch name {
	case "searxng":
		if !c.searxngConfigured() {
			if c.SearXNGRequested {
				return errors.New("SearXNG configuration is invalid")
			}
			return errors.New("SearXNG is not configured")
		}
	case "exa":
		if !c.ExaAvailable {
			return errors.New("Exa is not available")
		}
	default:
		return errors.New("unknown search provider")
	}
	return nil
}
