package webaccess

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type fakeFS struct {
	files map[string]string
}

func newEnv(t *testing.T, vars, files map[string]string) (Env, *fakeFS) {
	t.Helper()
	fs := &fakeFS{files: map[string]string{}}
	for name, body := range files {
		fs.files[filepath.FromSlash(name)] = body
	}
	home := filepath.FromSlash("/home/tester")
	env := Env{
		HomeDir: home,
		LookupEnv: func(key string) (string, bool) {
			v, ok := vars[key]
			return v, ok
		},
		Stat: func(path string) (os.FileInfo, error) {
			if _, ok := fs.files[path]; !ok {
				return nil, os.ErrNotExist
			}
			return fakeInfo{name: filepath.Base(path)}, nil
		},
		ReadFile: func(path string) ([]byte, error) {
			body, ok := fs.files[path]
			if !ok {
				return nil, os.ErrNotExist
			}
			return []byte(body), nil
		},
	}
	return env, fs
}

type fakeInfo struct{ name string }

func (f fakeInfo) Name() string       { return f.name }
func (f fakeInfo) Size() int64        { return 1 }
func (f fakeInfo) Mode() os.FileMode  { return 0o644 }
func (f fakeInfo) ModTime() time.Time { return time.Time{} }
func (f fakeInfo) IsDir() bool        { return false }
func (f fakeInfo) Sys() any           { return nil }

func TestResolvePathPrecedence(t *testing.T) {
	xdgFile := filepath.Join("/xdg", "pi", "web-search.json")
	legacy := filepath.Join("/home/tester", ".pi", "web-search.json")
	agent := filepath.Join("/home/tester", ".pi", "agent", "web-search.json")

	cases := []struct {
		name  string
		vars  map[string]string
		files map[string]string
		want  string
	}{
		{"agent dir", map[string]string{"PI_CODING_AGENT_DIR": "/explicit"}, nil, filepath.Join("/explicit", "web-search.json")},
		{"xdg existing", map[string]string{"XDG_CONFIG_HOME": "/xdg"}, map[string]string{xdgFile: "{}", legacy: "{}"}, xdgFile},
		{"xdg falls to legacy", map[string]string{"XDG_CONFIG_HOME": "/xdg"}, map[string]string{legacy: "{}"}, legacy},
		{"xdg no files", map[string]string{"XDG_CONFIG_HOME": "/xdg"}, nil, xdgFile},
		{"agent existing", nil, map[string]string{agent: "{}", legacy: "{}"}, agent},
		{"legacy fallback", nil, map[string]string{legacy: "{}"}, legacy},
		{"default agent path", nil, nil, agent},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env, _ := newEnv(t, tc.vars, tc.files)
			if got := ResolvePath(env); got != tc.want {
				t.Fatalf("ResolvePath = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestMissingFileUsesDefaults(t *testing.T) {
	env, _ := newEnv(t, nil, nil)
	cfg, diags := Load(env)
	if len(diags) != 0 {
		t.Fatalf("diagnostics = %v, want none", diags)
	}
	if !cfg.FetchEnabled || cfg.FetchMode != "readable" {
		t.Fatalf("defaults = %+v", cfg)
	}
	if cfg.MaxInlineChars != DefaultInlineChars || cfg.CacheMaxEntries != DefaultCacheEntries {
		t.Fatalf("limits = %+v", cfg)
	}
}

func TestUnsupportedCredentialFieldsAreIgnored(t *testing.T) {
	body := `{"firecrawlApiKey":"CANARY-FIRE","provider":"auto","fetch":{"defaultMode":"raw"}}`
	env, _ := newEnv(t, nil, map[string]string{"/home/tester/.pi/agent/web-search.json": body})
	cfg, diags := Load(env)
	if len(diags) != 0 {
		t.Fatalf("diagnostics = %v", diags)
	}
	// A nondefault mode proves Load read the fixture instead of using missing-file defaults.
	if !cfg.FetchEnabled || cfg.FetchMode != "raw" {
		t.Fatalf("fetch: enabled=%v mode=%q, want enabled raw mode", cfg.FetchEnabled, cfg.FetchMode)
	}
	joined := strings.Join(diags, " ")
	if strings.Contains(joined, "CANARY") {
		t.Fatalf("diagnostics leaked a value: %s", joined)
	}
}

func TestCrawl4AIConfig(t *testing.T) {
	cases := []struct {
		name      string
		body      string
		available bool
		allowed   bool
		wantDiag  string
	}{
		{"literal", `{"crawl4aiBaseUrl":"https://crawl.example/base/","crawl4aiApiToken":"token-value"}`, true, true, ""},
		{"unconfigured", `{}`, false, true, ""},
		{"empty base", `{"crawl4aiBaseUrl":"  ","crawl4aiApiToken":"token-value"}`, false, true, ""},
		{"null base", `{"crawl4aiBaseUrl":null}`, false, true, "crawl4aiBaseUrl"},
		{"non-string base", `{"crawl4aiBaseUrl":7}`, false, true, "crawl4aiBaseUrl"},
		{"query base", `{"crawl4aiBaseUrl":"https://crawl.example/?canary=SECRET"}`, false, true, "crawl4aiBaseUrl"},
		{"credentials base", `{"crawl4aiBaseUrl":"https://user:SECRET@crawl.example"}`, false, true, "crawl4aiBaseUrl"},
		{"http needs permission", `{"crawl4aiBaseUrl":"http://crawl.example"}`, false, true, "crawl4aiBaseUrl"},
		{"http with permission", `{"allowHttpBaseUrls":true,"crawl4aiBaseUrl":"http://crawl.example"}`, true, true, ""},
		{"invalid port", `{"crawl4aiBaseUrl":"https://crawl.example:99999"}`, false, true, "crawl4aiBaseUrl"},
		{"loopback http", `{"crawl4aiBaseUrl":"http://127.0.0.1:11235"}`, true, true, ""},
		{"null token", `{"crawl4aiBaseUrl":"https://crawl.example","crawl4aiApiToken":null}`, false, true, "crawl4aiApiToken"},
		{"non-string token", `{"crawl4aiBaseUrl":"https://crawl.example","crawl4aiApiToken":7}`, false, true, "crawl4aiApiToken"},
		{"command token", `{"crawl4aiBaseUrl":"https://crawl.example","crawl4aiApiToken":"!echo SECRET"}`, false, true, "crawl4aiApiToken"},
		{"invalid header token", `{"crawl4aiBaseUrl":"https://crawl.example","crawl4aiApiToken":"bad\nSECRET"}`, false, true, "crawl4aiApiToken"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env, _ := newEnv(t, nil, nil)
			cfg, diags := ParseConfig([]byte(tc.body), env)
			if cfg.Crawl4AIAvailable != tc.available {
				t.Fatalf("available = %v, want %v", cfg.Crawl4AIAvailable, tc.available)
			}
			if cfg.Crawl4AIAllowed != tc.allowed {
				t.Fatalf("allowed = %v, want %v", cfg.Crawl4AIAllowed, tc.allowed)
			}
			if !cfg.SearchEnabled || !cfg.FetchEnabled || !cfg.ContentEnabled {
				t.Fatalf("fallback config disabled unrelated tools: %v %v %v", cfg.SearchEnabled, cfg.FetchEnabled, cfg.ContentEnabled)
			}
			joined := strings.Join(diags, " ")
			if tc.wantDiag != "" && !strings.Contains(joined, tc.wantDiag) {
				t.Fatalf("diagnostics = %v", diags)
			}
			if tc.wantDiag == "" && len(diags) != 0 {
				t.Fatalf("diagnostics = %v, want none", diags)
			}
			if strings.Contains(joined, "SECRET") {
				t.Fatalf("diagnostics leaked a value: %v", diags)
			}
		})
	}
}

func TestCrawl4AIRouting(t *testing.T) {
	cases := []struct {
		name     string
		body     string
		allowed  bool
		wantDiag bool
	}{
		{"missing routing", `{"crawl4aiBaseUrl":"https://crawl.example"}`, true, false},
		{"missing providers", `{"crawl4aiBaseUrl":"https://crawl.example","fetchRouting":{}}`, true, false},
		{"explicit crawl4ai", `{"crawl4aiBaseUrl":"https://crawl.example","fetchRouting":{"providers":[" HTTP ","Crawl4AI"]}}`, true, false},
		{"excluded crawl4ai", `{"crawl4aiBaseUrl":"https://crawl.example","fetchRouting":{"providers":["http","jina"]}}`, false, false},
		{"unsupported names ignored", `{"crawl4aiBaseUrl":"https://crawl.example","fetchRouting":{"providers":["firecrawl","jina"]}}`, false, false},
		{"null routing", `{"crawl4aiBaseUrl":"https://crawl.example","fetchRouting":null}`, false, true},
		{"array routing", `{"crawl4aiBaseUrl":"https://crawl.example","fetchRouting":[]}`, false, true},
		{"null providers", `{"crawl4aiBaseUrl":"https://crawl.example","fetchRouting":{"providers":null}}`, false, true},
		{"empty providers", `{"crawl4aiBaseUrl":"https://crawl.example","fetchRouting":{"providers":[]}}`, false, true},
		{"wrong providers type", `{"crawl4aiBaseUrl":"https://crawl.example","fetchRouting":{"providers":{}}}`, false, true},
		{"empty provider name", `{"crawl4aiBaseUrl":"https://crawl.example","fetchRouting":{"providers":[" "]}}`, false, true},
		{"duplicate provider names", `{"crawl4aiBaseUrl":"https://crawl.example","fetchRouting":{"providers":["http","HTTP"]}}`, false, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env, _ := newEnv(t, nil, nil)
			cfg, diags := ParseConfig([]byte(tc.body), env)
			if cfg.Crawl4AIAllowed != tc.allowed {
				t.Fatalf("allowed = %v, want %v", cfg.Crawl4AIAllowed, tc.allowed)
			}
			if got := len(diags) > 0; got != tc.wantDiag {
				t.Fatalf("diagnostics = %v, want diagnostic %v", diags, tc.wantDiag)
			}
			if !cfg.SearchEnabled || !cfg.FetchEnabled || !cfg.ContentEnabled {
				t.Fatalf("routing disabled unrelated tools: %v %v %v", cfg.SearchEnabled, cfg.FetchEnabled, cfg.ContentEnabled)
			}
		})
	}
}

func TestProviderAliasAndEnablementPrecedence(t *testing.T) {
	body := `{
		"provider":"searxng","searchProvider":"exa",
		"webSearch":{"enabled":false},
		"tools":{"fetchContent":{"enabled":false},"webSearch":{"enabled":true}},
		"searxngBaseUrl":"https://searx.example/base/",
		"fetch":{"allowedModes":["raw","readable","answer"],"defaultMode":"raw"}
	}`
	env, _ := newEnv(t, nil, nil)
	cfg, diags := ParseConfig([]byte(body), env)
	if len(diags) != 0 {
		t.Fatalf("diagnostics = %v", diags)
	}
	if cfg.Provider != "searxng" {
		t.Fatalf("provider = %q", cfg.Provider)
	}
	if !cfg.SearchEnabled {
		t.Fatal("tools.webSearch.enabled should override webSearch.enabled")
	}
	if cfg.FetchEnabled {
		t.Fatal("tools.fetchContent.enabled=false must win")
	}
	if cfg.SearXNGBaseURL != "https://searx.example/base" {
		t.Fatalf("base = %q", cfg.SearXNGBaseURL)
	}
	if cfg.FetchMode != "raw" || len(cfg.FetchModes) != 2 {
		t.Fatalf("modes = %v default = %q", cfg.FetchModes, cfg.FetchMode)
	}
}

func TestEnvironmentOverrides(t *testing.T) {
	env, _ := newEnv(t, map[string]string{
		"SEARXNG_BASE_URL": "https://env.example/searx",
		"EXA_BASE_URL":     "https://exa.example/v1/",
		"EXA_API_KEY":      "env-key",
	}, map[string]string{"/home/tester/.pi/agent/web-search.json": `{
		"searxngBaseUrl":"https://file.example",
		"exaBaseUrl":"https://file-exa.example",
		"exaApiKey":"literal-key"
	}`})
	cfg, diags := Load(env)
	if len(diags) != 0 {
		t.Fatalf("diagnostics = %v", diags)
	}
	if cfg.SearXNGBaseURL != "https://env.example/searx" {
		t.Fatalf("searxng = %q", cfg.SearXNGBaseURL)
	}
	if cfg.ExaBaseURL != "https://exa.example/v1" {
		t.Fatalf("exa = %q", cfg.ExaBaseURL)
	}
	if cfg.ExaAPIKey != "env-key" {
		t.Fatalf("key = %q", cfg.ExaAPIKey)
	}
}

func TestEmptySearXNGOverrideDisablesProvider(t *testing.T) {
	env, _ := newEnv(t, map[string]string{"SEARXNG_BASE_URL": ""}, map[string]string{"/home/tester/.pi/agent/web-search.json": `{"searxngBaseUrl":"https://file.example","provider":"searxng"}`})
	cfg, _ := Load(env)
	if cfg.SearXNGBaseURL != "" || cfg.SearXNGAvailable {
		t.Fatalf("searxng should be unconfigured: %+v", cfg)
	}
	if err := cfg.ProviderError("searxng"); err == nil {
		t.Fatal("explicit searxng selection must error")
	}
}

func TestPlainHTTPRequiresLoopbackOrFlag(t *testing.T) {
	env, _ := newEnv(t, nil, nil)
	cfg, diags := ParseConfig([]byte(`{"searxngBaseUrl":"http://searx.example"}`), env)
	if len(diags) == 0 || cfg.SearXNGAvailable {
		t.Fatalf("http base should be refused: %+v %v", cfg.SearXNGAvailable, diags)
	}
	cfg, _ = ParseConfig([]byte(`{"allowHttpBaseUrls":true,"searxngBaseUrl":"http://searx.example:8080"}`), env)
	if cfg.SearXNGBaseURL != "http://searx.example:8080" {
		t.Fatalf("base = %q", cfg.SearXNGBaseURL)
	}
	cfg, _ = ParseConfig([]byte(`{"searxngBaseUrl":"http://127.0.0.1:8888"}`), env)
	if cfg.SearXNGBaseURL != "http://127.0.0.1:8888" {
		t.Fatalf("loopback base = %q", cfg.SearXNGBaseURL)
	}
}

func TestHTTPOverrideValidation(t *testing.T) {
	env, _ := newEnv(t, map[string]string{"PI_WEB_ACCESS_ALLOW_HTTP_BASE_URLS": "maybe"}, nil)
	cfg, diags := ParseConfig([]byte(`{}`), env)
	if len(diags) == 0 {
		t.Fatal("invalid override must produce a diagnostic")
	}
	if cfg.FetchEnabled || cfg.SearchEnabled || cfg.ContentEnabled {
		t.Fatal("invalid shared value must disable all web tools")
	}
	env, _ = newEnv(t, map[string]string{"PI_WEB_ACCESS_ALLOW_HTTP_BASE_URLS": " TRUE "}, nil)
	cfg, _ = ParseConfig([]byte(`{"searxngBaseUrl":"http://searx.example"}`), env)
	if cfg.SearXNGBaseURL != "http://searx.example" {
		t.Fatalf("override should allow http, got %q", cfg.SearXNGBaseURL)
	}
}

func TestMalformedSharedValuesDisableAll(t *testing.T) {
	cases := []string{
		`{"allowHttpBaseUrls":"yes"}`,
		`{"allowHttpBaseUrls":null}`,
		`{"maxInlineContentChars":1.5}`,
		`{"ssrf":{"allowRanges":["not-a-cidr"]}}`,
		`{"ssrf":{"allowRanges":null}}`,
		`{"cache":{"maxEntries":0}}`,
		`{"cache":{"maxBytes":1e30}}`,
		`{"tools":{"fetchContent":{"enabled":"yes"}}}`,
		`{"tools":{"fetchContent":{"enabled":null}}}`,
		`{"webSearch":{"enabled":null}}`,
		`[]`,
		`{"webSearch":`, // truncated JSON
	}
	for _, body := range cases {
		env, _ := newEnv(t, nil, nil)
		cfg, diags := ParseConfig([]byte(body), env)
		if len(diags) == 0 {
			t.Errorf("%s: expected a diagnostic", body)
		}
		if cfg.FetchEnabled || cfg.SearchEnabled || cfg.ContentEnabled {
			t.Errorf("%s: web tools must be disabled", body)
		}
	}
}

func TestFetchModeFailuresDisableFetchOnly(t *testing.T) {
	env, _ := newEnv(t, nil, nil)
	cfg, _ := ParseConfig([]byte(`{"fetch":{"allowedModes":["answer"],"defaultMode":"readable"}}`), env)
	if cfg.FetchEnabled {
		t.Fatal("fetch must be disabled")
	}
	cfg, _ = ParseConfig([]byte(`{"fetch":{"allowedModes":["readable"],"defaultMode":"raw"}}`), env)
	if cfg.FetchEnabled {
		t.Fatal("unsupported default must disable fetch")
	}
	if !cfg.SearchEnabled || !cfg.ContentEnabled {
		t.Fatal("other tools must stay enabled")
	}
	cfg, _ = ParseConfig([]byte(`{"fetch":{"allowedModes":["raw"]}}`), env)
	if !cfg.FetchEnabled || cfg.FetchMode != "raw" {
		t.Fatalf("disabled or wrong default mode: %+v", cfg)
	}
	args, err := parseFetchArgs(map[string]any{"url": "https://example.com"}, cfg)
	if err != nil || args.mode != "raw" {
		t.Fatalf("parsed mode = %q err=%v", args.mode, err)
	}
}

func TestRoutingFailureDisablesSearchOnly(t *testing.T) {
	env, _ := newEnv(t, nil, nil)
	cfg, _ := ParseConfig([]byte(`{"provider":["searxng","exa"]}`), env)
	if cfg.SearchEnabled {
		t.Fatal("search must be disabled")
	}
	if !cfg.FetchEnabled || !cfg.ContentEnabled {
		t.Fatal("fetch and content stay enabled")
	}
}

func TestInlineLimitRules(t *testing.T) {
	env, _ := newEnv(t, nil, nil)
	for _, tc := range []struct {
		body string
		want int
	}{
		{`{"maxInlineContentChars":500}`, DefaultInlineChars},
		{`{"maxInlineContentChars":3000}`, 3000},
		{`{"maxInlineContentChars":500000}`, maxInlineChars},
	} {
		cfg, _ := ParseConfig([]byte(tc.body), env)
		if cfg.MaxInlineChars != tc.want {
			t.Errorf("%s: got %d want %d", tc.body, cfg.MaxInlineChars, tc.want)
		}
	}
}

func TestAllowRangesNormalizeMappedIPv4(t *testing.T) {
	env, _ := newEnv(t, nil, nil)
	cfg, diags := ParseConfig([]byte(`{"ssrf":{"allowRanges":["198.18.0.0/15","::ffff:10.0.0.0/104"]}}`), env)
	if len(diags) != 0 {
		t.Fatalf("diagnostics = %v", diags)
	}
	if len(cfg.AllowRanges) != 2 || !cfg.AllowRanges[1].Addr().Is4() || cfg.AllowRanges[1].Bits() != 8 {
		t.Fatalf("ranges = %v", cfg.AllowRanges)
	}
}

func TestCredentialRules(t *testing.T) {
	env, _ := newEnv(t, nil, nil)
	cfg, diags := ParseConfig([]byte(`{"exaApiKey":"!security find-generic-password"}`), env)
	if len(diags) == 0 || cfg.ExaAvailable {
		t.Fatalf("command credential must be refused: %+v %v", cfg.ExaAvailable, diags)
	}
	for _, d := range diags {
		if strings.Contains(d, "security find") {
			t.Fatalf("diagnostic leaked command text: %s", d)
		}
	}

	env, _ = newEnv(t, map[string]string{"EXA_API_KEY": "fallback"}, nil)
	cfg, diags = ParseConfig([]byte(`{"exaApiKey":"$MISSING_KEY"}`), env)
	if len(diags) == 0 || cfg.ExaAvailable {
		t.Fatalf("missing $NAME must error despite EXA_API_KEY: %+v %v", cfg.ExaAvailable, diags)
	}

	env, _ = newEnv(t, map[string]string{"NAMED_KEY": "named"}, nil)
	cfg, _ = ParseConfig([]byte(`{"exaApiKey":"$NAMED_KEY"}`), env)
	if cfg.ExaAPIKey != "named" {
		t.Fatalf("key = %q", cfg.ExaAPIKey)
	}

	env, _ = newEnv(t, map[string]string{"NAMED_KEY": "named", "EXA_API_KEY": "fallback"}, nil)
	cfg, _ = ParseConfig([]byte(`{"exaApiKey":"$NAMED_KEY"}`), env)
	if cfg.ExaAPIKey != "named" {
		t.Fatalf("reference did not win over EXA_API_KEY: %q", cfg.ExaAPIKey)
	}

	env, _ = newEnv(t, map[string]string{"BRACED_KEY": "braced"}, nil)
	cfg, diags = ParseConfig([]byte(`{"exaApiKey":"${BRACED_KEY}"}`), env)
	if len(diags) != 0 || cfg.ExaAPIKey != "braced" {
		t.Fatalf("braced reference = %q diagnostics=%v", cfg.ExaAPIKey, diags)
	}

	// Exa keeps literal and resolved whitespace; Crawl4AI trims separately.
	cfg, diags = ParseConfig([]byte(`{"exaApiKey":"  literal key  "}`), env)
	if len(diags) != 0 || cfg.ExaAPIKey != "  literal key  " {
		t.Fatalf("Exa literal whitespace changed: %q diagnostics=%v", cfg.ExaAPIKey, diags)
	}
	env, _ = newEnv(t, map[string]string{"SPACE_KEY": "   "}, nil)
	cfg, diags = ParseConfig([]byte(`{"exaApiKey":"${SPACE_KEY}"}`), env)
	if len(diags) != 0 || cfg.ExaAPIKey != "   " {
		t.Fatalf("Exa resolved whitespace changed: %q diagnostics=%v", cfg.ExaAPIKey, diags)
	}
}

func TestCrawl4AIEnvironment(t *testing.T) {
	cases := []struct {
		name      string
		vars      map[string]string
		body      string
		available bool
		base      string
		token     string
		wantDiag  bool
	}{
		{
			name:      "env base overrides malformed file",
			vars:      map[string]string{"CRAWL4AI_BASE_URL": " https://env-crawl.example/ "},
			body:      `{"crawl4aiBaseUrl":7,"crawl4aiApiToken":"file-token"}`,
			available: true, base: "https://env-crawl.example", token: "file-token",
		},
		{
			name:      "empty env base uses file",
			vars:      map[string]string{"CRAWL4AI_BASE_URL": "   "},
			body:      `{"crawl4aiBaseUrl":"https://file-crawl.example"}`,
			available: true, base: "https://file-crawl.example",
		},
		{
			name:      "env token overrides literal",
			vars:      map[string]string{"CRAWL4AI_API_TOKEN": " env-token "},
			body:      `{"crawl4aiBaseUrl":"https://crawl.example","crawl4aiApiToken":"file-token"}`,
			available: true, base: "https://crawl.example", token: "env-token",
		},
		{
			name:      "empty env token uses literal",
			vars:      map[string]string{"CRAWL4AI_API_TOKEN": "  "},
			body:      `{"crawl4aiBaseUrl":"https://crawl.example","crawl4aiApiToken":" file-token "}`,
			available: true, base: "https://crawl.example", token: "file-token",
		},
		{
			name:      "reference wins over token env",
			vars:      map[string]string{"CRAWL4AI_API_TOKEN": "env-token", "CRAWL_KEY": " ref-token "},
			body:      `{"crawl4aiBaseUrl":"https://crawl.example","crawl4aiApiToken":"${CRAWL_KEY}"}`,
			available: true, base: "https://crawl.example", token: "ref-token",
		},
		{
			name:     "reference missing is not rescued",
			vars:     map[string]string{"CRAWL4AI_API_TOKEN": "env-token"},
			body:     `{"crawl4aiBaseUrl":"https://crawl.example","crawl4aiApiToken":"$MISSING_CRAWL"}`,
			wantDiag: true,
		},
		{
			name:     "reference whitespace is empty",
			vars:     map[string]string{"CRAWL4AI_API_TOKEN": "env-token", "CRAWL_KEY": "   "},
			body:     `{"crawl4aiBaseUrl":"https://crawl.example","crawl4aiApiToken":"$CRAWL_KEY"}`,
			wantDiag: true,
		},
		{
			name:      "resolved bang is data",
			vars:      map[string]string{"CRAWL_KEY": "!not-a-command"},
			body:      `{"crawl4aiBaseUrl":"https://crawl.example","crawl4aiApiToken":"$CRAWL_KEY"}`,
			available: true, base: "https://crawl.example", token: "!not-a-command",
		},
		{
			name:      "unauthenticated",
			body:      `{"crawl4aiBaseUrl":"https://crawl.example"}`,
			available: true, base: "https://crawl.example",
		},
		{
			name:     "empty reference",
			body:     `{"crawl4aiBaseUrl":"https://crawl.example","crawl4aiApiToken":"$"}`,
			wantDiag: true,
		},
		{
			name:     "malformed braced reference",
			body:     `{"crawl4aiBaseUrl":"https://crawl.example","crawl4aiApiToken":"${CRAWL_KEY"}`,
			wantDiag: true,
		},
		{
			name:     "unsupported dollar escape is not a reference",
			vars:     map[string]string{"$CRAWL_KEY": "SECRET", "CRAWL4AI_API_TOKEN": "env-token"},
			body:     `{"crawl4aiBaseUrl":"https://crawl.example","crawl4aiApiToken":"$$CRAWL_KEY"}`,
			wantDiag: true,
		},
		{
			name:     "unsupported bang escape is not a reference",
			vars:     map[string]string{"!CRAWL_KEY": "SECRET", "CRAWL4AI_API_TOKEN": "env-token"},
			body:     `{"crawl4aiBaseUrl":"https://crawl.example","crawl4aiApiToken":"$!CRAWL_KEY"}`,
			wantDiag: true,
		},
		{
			name:     "invalid env token header",
			vars:     map[string]string{"CRAWL4AI_API_TOKEN": "bad\nSECRET"},
			body:     `{"crawl4aiBaseUrl":"https://crawl.example"}`,
			wantDiag: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env, _ := newEnv(t, tc.vars, nil)
			cfg, diags := ParseConfig([]byte(tc.body), env)
			if cfg.Crawl4AIAvailable != tc.available {
				t.Fatalf("available = %v, want %v", cfg.Crawl4AIAvailable, tc.available)
			}
			if cfg.Crawl4AIBaseURL != tc.base {
				t.Fatalf("base = %q, want %q", cfg.Crawl4AIBaseURL, tc.base)
			}
			if cfg.Crawl4AIToken != tc.token {
				t.Fatal("effective token differs")
			}
			if got := len(diags) > 0; got != tc.wantDiag {
				t.Fatalf("diagnostics = %v, want diagnostic %v", diags, tc.wantDiag)
			}
			for _, diag := range diags {
				if strings.Contains(diag, "SECRET") || strings.Contains(diag, "env-token") || strings.Contains(diag, "file-token") {
					t.Fatal("diagnostic leaked a token value")
				}
			}
		})
	}
}

func TestCrawl4AIEnvironmentOnly(t *testing.T) {
	env, _ := newEnv(t, map[string]string{
		"CRAWL4AI_BASE_URL":  "https://env-only-crawl.example/base/",
		"CRAWL4AI_API_TOKEN": "env-only-token",
	}, nil)
	cfg, diags := Load(env)
	if len(diags) != 0 {
		t.Fatalf("diagnostics = %v", diags)
	}
	if !cfg.Crawl4AIAvailable || cfg.Crawl4AIBaseURL != "https://env-only-crawl.example/base" || cfg.Crawl4AIToken != "env-only-token" {
		t.Fatal("environment-only Crawl4AI config was not applied")
	}
}

func TestSearXNGHeaderValidationIsProviderLocal(t *testing.T) {
	env, _ := newEnv(t, nil, nil)
	cfg, diags := ParseConfig([]byte(`{"searxngBaseUrl":"https://searx.example","searxngHeaders":{"X-Api-Key":"secret","x-api-key":"secret"},"exaApiKey":"keep"}`), env)
	if cfg.SearXNGAvailable || cfg.SearXNGBaseURL != "" {
		t.Fatal("duplicate header names must disable SearXNG")
	}
	if !cfg.ExaAvailable || cfg.ExaAPIKey != "keep" {
		t.Fatal("Exa settings must survive a SearXNG header error")
	}
	if cfg.SearchProvider() != "searxng" {
		t.Fatal("invalid SearXNG configuration must not fall back to Exa")
	}
	if len(diags) == 0 {
		t.Fatal("expected a diagnostic")
	}
	for _, d := range diags {
		if strings.Contains(d, "secret") || strings.Contains(d, "x-api-key") {
			t.Fatalf("diagnostic leaked header detail: %s", d)
		}
	}

	cfg, diags = ParseConfig([]byte(`{"searxngBaseUrl":"https://searx.example","searxngHeaders":{"Host":"evil"}}`), env)
	if cfg.SearXNGAvailable {
		t.Fatal("forbidden header must disable SearXNG")
	}
	for _, d := range diags {
		if strings.Contains(strings.ToLower(d), "host") {
			t.Fatalf("diagnostic leaked a header name: %s", d)
		}
	}
}

func TestUnsupportedKeysIgnored(t *testing.T) {
	env, _ := newEnv(t, nil, nil)
	cfg, diags := ParseConfig([]byte(`{"futureKey":{"nested":[1,2]},"provider":"auto"}`), env)
	if len(diags) != 0 || cfg.Provider != "auto" {
		t.Fatalf("cfg=%+v diags=%v", cfg, diags)
	}
}

func TestInvalidBaseURLRules(t *testing.T) {
	env, _ := newEnv(t, nil, nil)
	for _, base := range []string{
		"ftp://searx.example", "https://user:pw@searx.example",
		"https://searx.example/?q=1", "https://searx.example/#frag",
		"https://searx.example/#", "https://searx.example#",
		"https://", "https://searx.example:bad",
		"https://searx.example:0", "https://searx.example:65536",
		"https://searx.example:",
	} {
		cfg, diags := ParseConfig([]byte(`{"searxngBaseUrl":"`+base+`"}`), env)
		if cfg.SearXNGAvailable {
			t.Errorf("%q should be rejected", base)
		}
		if len(diags) == 0 {
			t.Errorf("%q: missing diagnostic", base)
		}
		if cfg.Provider == "auto" && cfg.SearchProvider() != "searxng" {
			t.Errorf("%q: auto must not fall back to Exa", base)
		}
	}
}

func TestAllowRangesRejectBroadMappedIPv4(t *testing.T) {
	env, _ := newEnv(t, nil, nil)
	for _, prefix := range []string{"::ffff:0.0.0.0/32", "::ffff:0.0.0.0/96"} {
		cfg, diags := ParseConfig([]byte(`{"ssrf":{"allowRanges":["`+prefix+`"]}}`), env)
		if len(diags) == 0 || cfg.SearchEnabled || cfg.FetchEnabled || cfg.ContentEnabled {
			t.Fatalf("%s must disable all tools: %+v %v", prefix, cfg, diags)
		}
		if len(cfg.AllowRanges) != 0 {
			t.Fatalf("%s: invalid range retained: %v", prefix, cfg.AllowRanges)
		}
	}
}

type oversizedInfo struct{ fakeInfo }

func (o oversizedInfo) Size() int64 { return maxConfigBytes + 1 }

func TestLoadRejectsOversizedConfigBeforeRead(t *testing.T) {
	env, _ := newEnv(t, nil, map[string]string{"/home/tester/.pi/agent/web-search.json": "{}"})
	env.Stat = func(string) (os.FileInfo, error) { return oversizedInfo{}, nil }
	read := false
	env.ReadFile = func(string) ([]byte, error) {
		read = true
		return []byte("{}"), nil
	}
	cfg, diags := Load(env)
	if read || cfg.FetchEnabled || cfg.SearchEnabled || len(diags) == 0 {
		t.Fatalf("oversized config read or enabled: read=%v cfg=%+v diags=%v", read, cfg, diags)
	}
}

func TestEnvironmentBaseOverridesMalformedFileValue(t *testing.T) {
	env, _ := newEnv(t, map[string]string{
		"SEARXNG_BASE_URL": "https://env-searx.example",
		"EXA_BASE_URL":     "https://env-exa.example/v1",
	}, nil)
	cfg, diags := ParseConfig([]byte(`{"searxngBaseUrl":12,"exaBaseUrl":false}`), env)
	if len(diags) != 0 {
		t.Fatalf("diagnostics = %v", diags)
	}
	if cfg.SearXNGBaseURL != "https://env-searx.example" || cfg.ExaBaseURL != "https://env-exa.example/v1" {
		t.Fatalf("environment overrides not applied: searx=%q exa=%q", cfg.SearXNGBaseURL, cfg.ExaBaseURL)
	}
}

func TestNullProviderValuesAreRejected(t *testing.T) {
	env, _ := newEnv(t, nil, nil)
	cfg, diags := ParseConfig([]byte(`{"searxngBaseUrl":null}`), env)
	if len(diags) == 0 || cfg.SearXNGAvailable {
		t.Fatalf("null SearXNG base: %+v %v", cfg, diags)
	}
	if cfg.SearchProvider() != "searxng" {
		t.Fatal("invalid SearXNG configuration must not fall back to Exa")
	}
	cfg, diags = ParseConfig([]byte(`{"exaBaseUrl":null}`), env)
	if len(diags) == 0 || cfg.ExaAvailable {
		t.Fatalf("null Exa base: %+v %v", cfg, diags)
	}
	cfg, diags = ParseConfig([]byte(`{"exaApiKey":null}`), env)
	if len(diags) == 0 || cfg.ExaAvailable {
		t.Fatalf("null Exa key: %+v %v", cfg, diags)
	}
}

func TestLoadAppliesEnvironmentWithoutFile(t *testing.T) {
	env, _ := newEnv(t, map[string]string{
		"SEARXNG_BASE_URL":                   "https://searx.example",
		"EXA_API_KEY":                        "key",
		"PI_WEB_ACCESS_ALLOW_HTTP_BASE_URLS": "true",
	}, nil)
	cfg, diags := Load(env)
	if len(diags) != 0 {
		t.Fatalf("diagnostics = %v", diags)
	}
	if !cfg.SearXNGAvailable || cfg.SearXNGBaseURL != "https://searx.example" || cfg.SearchProvider() != "searxng" {
		t.Fatalf("SearXNG environment override not applied: %+v", cfg)
	}
	if cfg.ExaAPIKey != "key" {
		t.Fatalf("Exa key environment override not applied: %q", cfg.ExaAPIKey)
	}
	if !cfg.AllowHTTPBaseURLs {
		t.Fatal("HTTP environment override not applied")
	}
}

func TestEnvironmentHTTPPermissionOverridesMalformedFileValue(t *testing.T) {
	env, _ := newEnv(t, map[string]string{"PI_WEB_ACCESS_ALLOW_HTTP_BASE_URLS": "true"}, nil)
	cfg, diags := ParseConfig([]byte(`{"allowHttpBaseUrls":"yes","searxngBaseUrl":"http://searx.example"}`), env)
	if len(diags) != 0 {
		t.Fatalf("valid environment override should ignore malformed file value: %v", diags)
	}
	if !cfg.AllowHTTPBaseURLs || cfg.SearXNGBaseURL != "http://searx.example" {
		t.Fatalf("override not applied: %+v", cfg)
	}
}

func TestInvalidFetchHeaderSettingsDisableFetchOnly(t *testing.T) {
	env, _ := newEnv(t, nil, nil)
	bad := []struct {
		name, body, wantDiag, forbidden string
	}{
		{"unknown profile", `{"fetch":{"headerProfile":"opera"}}`, "fetch.headerProfile", "opera"},
		{"profile type", `{"fetch":{"headerProfile":123}}`, "fetch.headerProfile must be one string", "123"},
		{"profile null", `{"fetch":{"headerProfile":null}}`, "fetch.headerProfile must be one string", "null"},
		{"profile empty", `{"fetch":{"headerProfile":""}}`, "fetch.headerProfile", ""},
		{"language type", `{"fetch":{"acceptLanguage":123}}`, "fetch.acceptLanguage must be one string", "123"},
		{"language null", `{"fetch":{"acceptLanguage":null}}`, "fetch.acceptLanguage must be one string", "null"},
		{"language control", `{"fetch":{"acceptLanguage":"en-US\r\nX: evil"}}`, "fetch.acceptLanguage", "evil"},
		{"language blank", `{"fetch":{"acceptLanguage":" \t "}}`, "fetch.acceptLanguage", ""},
	}
	for _, tc := range bad {
		t.Run(tc.name, func(t *testing.T) {
			cfg, diags := ParseConfig([]byte(tc.body), env)
			if cfg.FetchEnabled {
				t.Fatalf("%s: fetch must be disabled", tc.body)
			}
			if !cfg.SearchEnabled || !cfg.ContentEnabled {
				t.Fatalf("%s: search and content must stay enabled", tc.body)
			}
			joined := strings.Join(diags, "\n")
			if !strings.Contains(joined, tc.wantDiag) {
				t.Fatalf("%s: diagnostic %q missing %q", tc.body, joined, tc.wantDiag)
			}
			if tc.forbidden != "" && strings.Contains(joined, tc.forbidden) {
				t.Fatalf("%s: diagnostic leaked value: %q", tc.body, joined)
			}
		})
	}
}

func TestFetchHeaderSettingsPreserveModeConfig(t *testing.T) {
	env, _ := newEnv(t, nil, nil)
	cfg, diags := ParseConfig([]byte(`{"fetch":{"allowedModes":["raw"],"headerProfile":"chrome","acceptLanguage":" \ten-US  "}}`), env)
	if len(diags) != 0 || !cfg.FetchEnabled || cfg.FetchMode != "raw" {
		t.Fatalf("fetch enabled=%v, mode=%q, diagnostics=%v", cfg.FetchEnabled, cfg.FetchMode, diags)
	}
	if cfg.FetchHeaderProfile != "chrome" || cfg.FetchAcceptLanguage != "en-US" {
		t.Fatalf("headers = %q %q", cfg.FetchHeaderProfile, cfg.FetchAcceptLanguage)
	}
	cfg, diags = ParseConfig([]byte(`{"fetch":{"defaultMode":"raw"}}`), env)
	if len(diags) != 0 || !cfg.FetchEnabled || cfg.FetchMode != "raw" {
		t.Fatalf("fetch enabled=%v, mode=%q, diagnostics=%v", cfg.FetchEnabled, cfg.FetchMode, diags)
	}
	if cfg.FetchHeaderProfile != "psi" || cfg.FetchAcceptLanguage != "" {
		t.Fatalf("defaults = %q %q", cfg.FetchHeaderProfile, cfg.FetchAcceptLanguage)
	}
}
