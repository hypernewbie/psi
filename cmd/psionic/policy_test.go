package main

import "testing"

func TestAdmissionDMDirectPolicy(t *testing.T) {
	base := `{ channels: { discord: { dmPolicy: "allowlist", allowFrom: ["123456789012345678"] } } }`
	settings := mustSettings(t, base)
	policy := newAccessPolicy(settings)
	allowed := policy.admit(inboundMessage{AuthorID: "123456789012345678", IsDM: true}, false)
	if !allowed.Allowed {
		t.Fatalf("allowlisted DM denied: %s", allowed.Reason)
	}
	denied := policy.admit(inboundMessage{AuthorID: "999999999999999999", IsDM: true}, false)
	if denied.Allowed {
		t.Fatal("unknown DM allowed")
	}
}

func TestAdmissionDMPairingUsesStaticAllowFrom(t *testing.T) {
	settings := mustSettings(t, `{ channels: { discord: { dmPolicy: "pairing", allowFrom: ["123456789012345678"] } } }`)
	policy := newAccessPolicy(settings)
	if got := policy.admit(inboundMessage{AuthorID: "123456789012345678", IsDM: true}, false); !got.Allowed {
		t.Fatalf("allowlisted pairing DM denied: %+v", got)
	}
	if got := policy.admit(inboundMessage{AuthorID: "999999999999999999", IsDM: true}, false); got.Allowed {
		t.Fatalf("unknown pairing DM allowed: %+v", got)
	}
}

func TestAdmissionGuildAndChannelAllowlists(t *testing.T) {
	settings := mustSettings(t, `{
  channels: { discord: {
    groupPolicy: "allowlist",
    guilds: {
      "323456789012345678": {
        users: ["223456789012345678"],
        channels: { "423456789012345678": { enabled: true, roles: ["623456789012345678"] } },
      },
    },
  } },
}`)
	policy := newAccessPolicy(settings)
	msg := inboundMessage{
		GuildID:   "323456789012345678",
		ChannelID: "423456789012345678",
		AuthorID:  "223456789012345678",
		RoleIDs:   []string{"623456789012345678"},
	}
	if got := policy.admit(msg, true); !got.Allowed {
		t.Fatalf("permitted guild sender denied: %s", got.Reason)
	}
	msg.ChannelID = "523456789012345678"
	if got := policy.admit(msg, true); got.Allowed {
		t.Fatal("unlisted channel allowed")
	}
	msg.ChannelID = "423456789012345678"
	msg.RoleIDs = nil
	if got := policy.admit(msg, true); got.Allowed {
		t.Fatal("sender without the channel role allowed")
	}
	msg.GuildID = "999999999999999999"
	if got := policy.admit(msg, true); got.Allowed {
		t.Fatal("unlisted guild allowed")
	}
}

func TestAdmissionMentionPrecedence(t *testing.T) {
	settings := mustSettings(t, `{
  channels: { discord: {
    guilds: {
      "323456789012345678": {
        requireMention: false,
        channels: {
          "423456789012345678": { requireMention: true },
          "*": { enabled: true },
        },
      },
    },
  } },
}`)
	policy := newAccessPolicy(settings)
	msg := inboundMessage{GuildID: "323456789012345678", ChannelID: "423456789012345678", AuthorID: "123456789012345678"}
	if got := policy.admit(msg, false); got.Allowed {
		t.Fatal("channel mention requirement was ignored")
	}
	msg.ChannelID = "523456789012345678"
	if got := policy.admit(msg, false); !got.Allowed {
		t.Fatalf("guild default denied: %s", got.Reason)
	}
}

func TestAdmissionIgnoresBotsAndSelf(t *testing.T) {
	settings := mustSettings(t, `{ channels: { discord: {} } }`)
	policy := newAccessPolicy(settings)
	for name, msg := range map[string]inboundMessage{
		"self":    {AuthorID: "123456789012345678", IsDM: true, FromSelf: true},
		"bot":     {AuthorID: "223456789012345678", IsDM: true, FromBot: true},
		"webhook": {AuthorID: "323456789012345678", IsDM: true, FromWebhook: true},
	} {
		if got := policy.admit(msg, true); got.Allowed {
			t.Fatalf("%s message allowed", name)
		}
	}
}

func TestAdmissionOpenGuildWithoutEntry(t *testing.T) {
	settings := mustSettings(t, `{ channels: { discord: { groupPolicy: "open" } } }`)
	policy := newAccessPolicy(settings)
	if got := policy.admit(inboundMessage{GuildID: "323456789012345678", ChannelID: "423456789012345678", AuthorID: "123456789012345678"}, true); !got.Allowed {
		t.Fatalf("open guild denied: %s", got.Reason)
	}
}

func TestNormalizeUserID(t *testing.T) {
	forms := []string{
		"123456789012345678",
		"discord:123456789012345678",
		"user:123456789012345678",
		"discord:user:123456789012345678",
		"<@123456789012345678>",
		"<@!123456789012345678>",
	}
	for _, form := range forms {
		if got := normalizeUserID(form); got != "123456789012345678" {
			t.Fatalf("normalizeUserID(%q) = %q", form, got)
		}
	}
}

func mustSettings(t *testing.T, body string) *fileSettings {
	t.Helper()
	settings, err := decodeSettings([]byte(body), func(string) (string, bool) { return "token", true })
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	return settings
}

func TestChannelCannotWidenGuildTools(t *testing.T) {
	settings := mustSettings(t, `{
  channels: { discord: { guilds: { "323456789012345678": {
    tools: { allow: ["skill"] },
    channels: { "423456789012345678": { tools: { alsoAllow: ["message"] } } },
  } } } },
}`)
	rt := newRuntime(settings, selectedAgent{Workspace: t.TempDir()}, nil, t.TempDir(), nil, configDefaults(), nil, nil)
	route := rt.routeTools(inboundMessage{GuildID: "323456789012345678", ChannelID: "423456789012345678"})
	if route.allows("message") {
		t.Fatal("channel widened the guild tool policy")
	}
	if !route.allows("skill") {
		t.Fatal("guild tool policy was lost")
	}
}

func TestThreadUsesParentChannelPolicy(t *testing.T) {
	settings := mustSettings(t, `{
  channels: { discord: { guilds: { "323456789012345678": {
    channels: { "423456789012345678": { enabled: true } },
  } } } },
}`)
	policy := newAccessPolicy(settings)
	base := inboundMessage{GuildID: "323456789012345678", AuthorID: "123456789012345678"}
	thread := base
	thread.ChannelID, thread.ParentID, thread.IsThread = "523456789012345678", "423456789012345678", true
	if got := policy.admit(thread, true); !got.Allowed {
		t.Fatalf("thread parent denied: %s", got.Reason)
	}
	thread.ParentID = "623456789012345678"
	if got := policy.admit(thread, true); got.Allowed {
		t.Fatal("thread with an unlisted parent allowed")
	}
}

func TestGroupDMsAreDenied(t *testing.T) {
	settings := mustSettings(t, `{ channels: { discord: { dmPolicy: "open", allowFrom: ["*"] } } }`)
	policy := newAccessPolicy(settings)
	if got := policy.admit(inboundMessage{AuthorID: "123456789012345678", IsDM: true, IsGroupDM: true}, false); got.Allowed {
		t.Fatal("group DM was admitted")
	}
}
