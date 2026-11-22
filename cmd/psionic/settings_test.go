package main

import (
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func testSettings(t *testing.T) (*fileSettings, string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json5")
	content := `{
  // Existing installation settings, reduced to the supported subset.
  agents: {
    defaults: {
      workspace: "~/workspace",
      model: { primary: "demo/large-model", fallbacks: ["demo/small-model"] },
      identity: { name: "Aster", theme: "quiet" },
    },
    entries: {
      main: { identity: { name: "Aster" }, tools: { deny: ["bash"] } },
    },
  },
  channels: {
    discord: {
      token: { source: "env", id: "PSIONIC_TEST_BOT_TOKEN" },
      dmPolicy: "allowlist",
      allowFrom: ["123456789012345678", "<@!223456789012345678>"],
      groupPolicy: "allowlist",
      guilds: {
        "323456789012345678": {
          requireMention: false,
          users: ["123456789012345678"],
          channels: { "423456789012345678": { enabled: true, requireMention: true } },
        },
      },
    },
  },
  messages: { visibleReplies: "message_tool" },
  session: {
    dmScope: "per-channel-peer",
    reset: { mode: "daily", atHour: 4 },
    resetByType: { thread: { mode: "idle", idleMinutes: 30 } },
  },
  commands: { ownerAllowFrom: ["discord:523456789012345678"] },
}`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	env := func(key string) (string, bool) {
		if key == "PSIONIC_TEST_BOT_TOKEN" {
			return "test-token", true
		}
		return "", false
	}
	settings, err := loadFileSettings(path, env)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	return settings, dir
}

func TestLoadFileSettingsJSON5(t *testing.T) {
	settings, dir := testSettings(t)
	if settings.Channels.Discord.Token == nil {
		t.Fatal("token was not decoded")
	}
	token, err := settings.Channels.Discord.Token.resolve(func(string) (string, bool) { return "token", true })
	if err != nil || token != "token" {
		t.Fatalf("token = %q, %v", token, err)
	}
	if got := settings.Messages.VisibleReplies; got != "message_tool" {
		t.Fatalf("visibleReplies = %q", got)
	}
	agent, err := resolveAgent(settings, "main")
	if err != nil {
		t.Fatalf("agent: %v", err)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	if agent.Workspace != filepath.Join(home, "workspace") {
		t.Fatalf("workspace = %q", agent.Workspace)
	}
	if agent.Model != "" {
		t.Fatalf("external model selection was not ignored: %q", agent.Model)
	}
	if agent.Identity.Name != "Aster" {
		t.Fatalf("identity = %+v", agent.Identity)
	}
	if _, err := os.Stat(filepath.Join(dir, "settings.json5")); err != nil {
		t.Fatal(err)
	}
}

func TestEnvironmentTokenProviderAllowlist(t *testing.T) {
	lookup := func(name string) (string, bool) { return "test-token", true }
	for _, tc := range []struct {
		name, secrets, provider string
		wantError               bool
	}{
		{"built-in", ``, `default`, false},
		{"allowed", `secrets: { providers: { trusted: { source: "env", allowlist: ["BOT_TOKEN"] } } },`, `trusted`, false},
		{"denied", `secrets: { providers: { trusted: { source: "env", allowlist: [] } } },`, `trusted`, true},
		{"unknown alias", ``, `unknown`, true},
		{"wrong source", `secrets: { providers: { custom: { source: "file" } } },`, `custom`, true},
		{"default collision", `secrets: { providers: { default: { source: "file" } } },`, `default`, false},
		{"invalid alias", ``, `BadAlias`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := fmt.Sprintf(`{ %s channels: { discord: { token: { source: "env", provider: %q, id: "BOT_TOKEN" } } } }`, tc.secrets, tc.provider)
			_, err := decodeSettings([]byte(body), lookup)
			if (err != nil) != tc.wantError {
				t.Fatalf("error = %v, wantError = %t", err, tc.wantError)
			}
		})
	}
}

func TestEnvironmentTokenShorthandUsesProviderAllowlist(t *testing.T) {
	lookup := func(name string) (string, bool) {
		return "test-token", name == "BOT_TOKEN"
	}
	for _, shorthand := range []string{"$BOT_TOKEN", "${BOT_TOKEN}"} {
		t.Run(shorthand, func(t *testing.T) {
			for _, tc := range []struct {
				allowlist string
				wantError bool
			}{
				{`["BOT_TOKEN"]`, false},
				{`[]`, true},
			} {
				body := fmt.Sprintf(`{
					secrets: { defaults: { env: "restricted" }, providers: { restricted: { source: "env", allowlist: %s } } },
					channels: { discord: { token: %q } }
				}`, tc.allowlist, shorthand)
				settings, err := decodeSettings([]byte(body), lookup)
				if (err != nil) != tc.wantError {
					t.Fatalf("allowlist %s: error = %v", tc.allowlist, err)
				}
				if err == nil {
					value, err := settings.Channels.Discord.Token.resolve(lookup)
					if err != nil || value != "test-token" {
						t.Fatalf("reference did not resolve: %v", err)
					}
				}
			}
		})
	}
}

func TestDiscordTokenConfigurationSubstitution(t *testing.T) {
	lookup := func(name string) (string, bool) {
		if name == "VALUE" {
			return "resolved", true
		}
		return "", false
	}
	for _, tc := range []struct {
		name, input, want string
		wantError         bool
	}{
		{"embedded shorthand is literal", "prefix-$VALUE", "prefix-$VALUE", false},
		{"embedded template", "prefix-${VALUE}", "prefix-resolved", false},
		{"escaped template", "$${VALUE}", "${VALUE}", false},
		{"fallback", "prefix-${MISSING:-fallback}", "prefix-fallback", false},
		{"invalid template remains literal", "${bad}", "${bad}", false},
		{"missing template", "prefix-${MISSING}", "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ref := &secretRef{value: tc.input}
			got, err := ref.load(lookup)
			if (err != nil) != tc.wantError || got != tc.want {
				t.Fatalf("got %q, error %v", got, err)
			}
		})
	}
}

func TestMissingEnvironmentTokenDoesNotExposeReference(t *testing.T) {
	body := `{ channels: { discord: { token: { source: "env", provider: "default", id: "SYNTHETIC_PRIVATE_NAME" } } } }`
	_, err := decodeSettings([]byte(body), func(string) (string, bool) { return "", false })
	if err == nil {
		t.Fatal("missing token was accepted")
	}
	if strings.Contains(err.Error(), "SYNTHETIC_PRIVATE_NAME") {
		t.Fatalf("error exposed reference: %v", err)
	}
}

func TestEnvironmentTokenRejectsInvalidReferenceID(t *testing.T) {
	lookup := func(string) (string, bool) { return "test-token", true }
	for _, id := range []string{"lowercase", "1TOKEN", strings.Repeat("A", 129)} {
		body := fmt.Sprintf(`{ channels: { discord: { token: { source: "env", provider: "default", id: %q } } } }`, id)
		if _, err := decodeSettings([]byte(body), lookup); err == nil {
			t.Fatalf("accepted invalid environment reference %q", id)
		}
	}
}

func TestLoadFileSettingsRejectsUnsupportedPolicies(t *testing.T) {
	cases := map[string]string{
		"unknown dm":        `{ channels: { discord: { dmPolicy: "request" } } }`,
		"exec secret":       `{ channels: { discord: { token: { source: "exec", id: "cmd" } } } }`,
		"multiple accounts": `{ channels: { discord: { accounts: { a: {}, b: {} } } } }`,
		"missing file":      ``,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "settings.json5")
			if body == "" {
				path = filepath.Join(dir, "absent.json5")
			} else if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := loadFileSettings(path, func(string) (string, bool) { return "", false }); err == nil {
				t.Fatal("expected an error")
			}
		})
	}
}

func TestSettingsErrorsDoNotEchoPrivateValues(t *testing.T) {
	const privateValue = "SYNTHETIC_PRIVATE_VALUE"
	for name, body := range map[string]string{
		"unknown secret source": `{ channels: { discord: { token: { source: "` + privateValue + `", id: "` + privateValue + `" } } } }`,
		"unknown group policy":  `{ channels: { discord: { groupPolicy: "` + privateValue + `" } } }`,
		"unknown reply mode":    `{ messages: { visibleReplies: "` + privateValue + `" } }`,
		"unknown tool profile":  `{ agents: { entries: { "` + privateValue + `": { tools: { profile: "` + privateValue + `" } } } } }`,
		"unknown reset mode":    `{ session: { reset: { mode: "` + privateValue + `" } } }`,
	} {
		t.Run(name, func(t *testing.T) {
			_, err := decodeSettings([]byte(body), func(string) (string, bool) { return "", false })
			if err == nil {
				t.Fatal("unsupported setting was accepted")
			}
			if strings.Contains(err.Error(), privateValue) {
				t.Fatalf("error echoed a configured value: %v", err)
			}
		})
	}
}

func TestResolveAgentRequiresSelectionForMultipleAgents(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json5")
	one := filepath.Join(dir, "one")
	two := filepath.Join(dir, "two")
	body := fmt.Sprintf(`{
  agents: { entries: {
    one: { workspace: %q },
    two: { workspace: %q },
  } },
  channels: { discord: {} },
}`, one, two)
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	settings, err := loadFileSettings(path, func(string) (string, bool) { return "", false })
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if _, err := resolveAgent(settings, ""); err == nil || !strings.Contains(err.Error(), "--agent") {
		t.Fatalf("expected selection error, got %v", err)
	}
	if agent, err := resolveAgent(settings, "two"); err != nil || agent.Workspace != two {
		t.Fatalf("agent = %+v, %v", agent, err)
	}
	const privateID = "SYNTHETIC_PRIVATE_AGENT_ID"
	if _, err := resolveAgent(settings, privateID); err == nil || strings.Contains(err.Error(), privateID) {
		t.Fatalf("invalid agent diagnostic exposed its ID: %v", err)
	}
}

func TestSplitModelRef(t *testing.T) {
	provider, model, err := splitModelRef("openrouter/moonshotai/kimi-k2.7-code")
	if err != nil || provider != "openrouter" || model != "moonshotai/kimi-k2.7-code" {
		t.Fatalf("got %q %q %v", provider, model, err)
	}
	if _, _, err := splitModelRef("no-slash"); err == nil {
		t.Fatal("expected an error")
	}
}

func TestDeepSettingsAreRejectedBeforeIncludesCanHide(t *testing.T) {
	var body strings.Builder
	for i := 0; i < 20; i++ {
		body.WriteString("{ nested: ")
	}
	body.WriteString("{}")
	for i := 0; i < 20; i++ {
		body.WriteString(" }")
	}
	if _, err := decodeSettings([]byte(body.String()), func(string) (string, bool) { return "", false }); err == nil {
		t.Fatal("deep settings were accepted")
	}
}

func TestMainScopeRequiresTheOwnerPeer(t *testing.T) {
	base := `{
  channels: { discord: { dmPolicy: "allowlist", allowFrom: ["123456789012345678"] } },
  session: { dmScope: "main" },
`
	if _, err := decodeSettings([]byte(base+`}`), func(string) (string, bool) { return "token", true }); err == nil {
		t.Fatal("main scope without an owner was accepted")
	}
	if _, err := decodeSettings([]byte(base+`commands: { ownerAllowFrom: ["123456789012345678"] } }`), func(string) (string, bool) { return "token", true }); err != nil {
		t.Fatalf("owner main scope rejected: %v", err)
	}
}

func TestPairingPolicyUsesStaticAllowFrom(t *testing.T) {
	settings, err := decodeSettings([]byte(`{ channels: { discord: { dmPolicy: "pairing", allowFrom: ["123456789012345678"] } } }`), func(string) (string, bool) { return "token", true })
	if err != nil {
		t.Fatalf("pairing policy: %v", err)
	}
	if settings.Channels.Discord.DMPolicy != "pairing" {
		t.Fatalf("dmPolicy = %q", settings.Channels.Discord.DMPolicy)
	}
}

func TestVisibleRepliesAcceptsBooleanForm(t *testing.T) {
	settings := mustSettings(t, `{ channels: { discord: {} }, messages: { visibleReplies: false } }`)
	if string(settings.Messages.VisibleReplies) != "message_tool" {
		t.Fatalf("visibleReplies = %q", settings.Messages.VisibleReplies)
	}
	settings = mustSettings(t, `{ channels: { discord: {} }, messages: { groupChat: { visibleReplies: true } } }`)
	if string(settings.Messages.GroupChat.VisibleReplies) != "automatic" {
		t.Fatalf("group visibleReplies = %q", settings.Messages.GroupChat.VisibleReplies)
	}
}

func TestSettingsRejectIncludesAndBroadMainScope(t *testing.T) {
	cases := map[string]string{
		"include":     `{ "$include": "other.json5", channels: { discord: {} } }`,
		"main scope":  `{ channels: { discord: { dmPolicy: "allowlist", allowFrom: ["1", "2"] } }, session: { dmScope: "main" } }`,
		"bad replies": `{ channels: { discord: {} }, messages: { visibleReplies: "visible" } }`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := decodeSettings([]byte(body), func(string) (string, bool) { return "", false }); err == nil {
				t.Fatal("expected an error")
			}
		})
	}
}

func TestAllowFromPreservesSnowflakeNumbers(t *testing.T) {
	settings := mustSettings(t, `{ channels: { discord: {
    dmPolicy: "allowlist",
    allowFrom: [123456789012345678, "223456789012345678"],
  } } }`)
	want := []string{"123456789012345678", "223456789012345678"}
	if len(settings.Channels.Discord.AllowFrom) != 2 {
		t.Fatalf("allowFrom = %v", settings.Channels.Discord.AllowFrom)
	}
	for i, id := range want {
		if settings.Channels.Discord.AllowFrom[i] != id {
			t.Fatalf("allowFrom[%d] = %q", i, settings.Channels.Discord.AllowFrom[i])
		}
	}
}

func TestNativeCommandSetting(t *testing.T) {
	for body, want := range map[string]bool{
		`{ channels: { discord: {} }, commands: { native: false } }`:  false,
		`{ channels: { discord: {} }, commands: { native: true } }`:   true,
		`{ channels: { discord: {} }, commands: { native: "auto" } }`: true,
	} {
		settings := mustSettings(t, body)
		rt := newRuntime(settings, selectedAgent{Workspace: t.TempDir()}, nil, t.TempDir(), nil, configDefaults(), nil, log.New(io.Discard, "", 0))
		if got := rt.commandsEnabled(); got != want {
			t.Fatalf("native = %v, want %v", got, want)
		}
	}
	if _, err := decodeSettings([]byte(`{ channels: { discord: {} }, commands: { native: "sometimes" } }`), func(string) (string, bool) { return "", false }); err == nil {
		t.Fatal("invalid native setting accepted")
	}
}

func TestUnknownToolProfileIsRejected(t *testing.T) {
	_, err := decodeSettings([]byte(`{
		agents: { defaults: { tools: { profile: "unknown" } } },
		channels: { discord: {} },
	}`), func(string) (string, bool) { return "", false })
	if err == nil {
		t.Fatal("unknown tool profile accepted")
	}
}

func TestTopLevelToolPolicyIsInherited(t *testing.T) {
	settings := mustSettings(t, fmt.Sprintf(`{
  channels: { discord: {} },
  tools: { profile: "coding", deny: ["memory_get"] },
  agents: { defaults: { workspace: %q, tools: { alsoAllow: ["message"] } } },
}`, t.TempDir()))
	agent, err := resolveAgent(settings, "")
	if err != nil {
		t.Fatal(err)
	}
	if agent.Tools.Profile != "coding" || !agent.Tools.allows("message") || agent.Tools.allows("memory_get") {
		t.Fatalf("tools = %+v", agent.Tools)
	}
}

func TestRouteToolsCanOnlyNarrowTheAgentPolicy(t *testing.T) {
	settings := mustSettings(t, `{
  tools: { profile: "coding" },
  channels: { discord: { guilds: { "323456789012345678": {
    tools: { alsoAllow: ["message"], deny: ["memory_get"] },
    channels: { "423456789012345678": { tools: { deny: ["message"] } } },
  } } } },
  agents: { defaults: { workspace: `+fmt.Sprintf("%q", t.TempDir())+` } },
}`)
	rt := newRuntime(settings, selectedAgent{Workspace: t.TempDir(), Tools: toolPolicy{Profile: "coding"}}, nil, t.TempDir(), nil, configDefaults(), nil, log.New(io.Discard, "", 0))
	msg := inboundMessage{GuildID: "323456789012345678", ChannelID: "423456789012345678"}
	policy := rt.routeTools(msg)
	if policy.allows("message") || policy.allows("memory_get") || !policy.allows("memory_search") {
		t.Fatalf("route policy = %+v", policy)
	}
	widened := narrowTools(toolPolicy{Profile: "full"}, toolPolicy{Profile: "minimal"})
	for _, tool := range implementedTools {
		if widened.allows(tool) {
			t.Fatalf("route widened %s", tool)
		}
	}
}

func TestOwnerWildcardIsRejected(t *testing.T) {
	_, err := decodeSettings([]byte(`{ channels: { discord: {} }, commands: { ownerAllowFrom: ["*"] } }`), func(string) (string, bool) { return "", false })
	if err == nil {
		t.Fatal("owner wildcard accepted")
	}
}

func TestUnknownResetModeIsRejected(t *testing.T) {
	_, err := decodeSettings([]byte(`{ channels: { discord: {} }, session: { reset: { mode: "weekly" } } }`), func(string) (string, bool) { return "", false })
	if err == nil {
		t.Fatal("unknown reset mode accepted")
	}
}
