package main

import (
	"regexp"
	"strings"
)

// inboundMessage is the transport-independent subset used for admission.
type inboundMessage struct {
	MessageID    string
	GuildID      string
	ChannelID    string
	ParentID     string
	AuthorID     string
	RoleIDs      []string
	IsDM         bool
	IsGroupDM    bool
	IsThread     bool
	FromSelf     bool
	FromBot      bool
	FromWebhook  bool
	MentionsBot  bool
	RepliesToBot bool
	ReferenceID  string
}

// admission is the decision for one inbound event. A denied event produces no
// model call, state write, history read, or reply.
type admission struct {
	Allowed bool
	Reason  string
}

type accessPolicy struct {
	dmPolicy        string
	allowFrom       map[string]bool
	dmGroupEnabled  bool
	dmGroupChannels []string
	groupPolicy     string
	guilds          map[string]*guildSettings
	ownerAllowFrom  map[string]bool
	// ownerBootstrapped records that the owner set came from the direct-message
	// allowlist because no owner is configured, mirroring the incumbent's
	// one-owner pairing bootstrap.
	ownerBootstrapped bool
}

const (
	dmPolicyPairing = "pairing"
	defaultAtHour   = 4
)

func newAccessPolicy(s *fileSettings) *accessPolicy {
	discord := s.Channels.Discord
	p := &accessPolicy{
		dmPolicy:    discord.DMPolicy,
		groupPolicy: discord.GroupPolicy,
		guilds:      discord.Guilds,
	}
	if p.dmPolicy == "" {
		p.dmPolicy = dmPolicyPairing
	}
	if p.groupPolicy == "" {
		p.groupPolicy = "allowlist"
	}
	p.allowFrom = idSet(discord.AllowFrom)
	if discord.DM != nil {
		p.dmGroupEnabled = discord.DM.GroupEnabled != nil && *discord.DM.GroupEnabled
		p.dmGroupChannels = discord.DM.GroupChannels
	}
	if s.Commands != nil {
		p.ownerAllowFrom = idSet(s.Commands.OwnerAllowFrom)
	}
	// Parity with the incumbent's pairing bootstrap: when no owner is
	// configured, exactly one owner exists — the first explicit direct-message
	// allowlist entry. A wildcard or empty allowlist bootstraps nobody, and an
	// explicit owner list always wins.
	if len(p.ownerAllowFrom) == 0 {
		for _, entry := range discord.AllowFrom {
			if id := normalizeUserID(entry); id != "" && id != "*" {
				p.ownerAllowFrom = map[string]bool{id: true}
				p.ownerBootstrapped = true
				break
			}
		}
	}
	return p
}

func (p *accessPolicy) isOwner(userID string) bool {
	return p.ownerAllowFrom[normalizeUserID(userID)]
}

// dmSenderAllowed applies the direct-message policy to one sender. Group DMs
// reuse it: every member of an admitted group channel must individually pass
// the same policy as a private DM.
func (p *accessPolicy) dmSenderAllowed(msg inboundMessage) admission {
	switch p.dmPolicy {
	case "disabled":
		return admission{Reason: "direct messages are disabled"}
	case "open":
		return admission{Allowed: true}
	case "allowlist":
		if p.allowFrom["*"] || p.allowFrom[normalizeUserID(msg.AuthorID)] {
			return admission{Allowed: true}
		}
		return admission{Reason: "sender is not in the direct-message allowlist"}
	case dmPolicyPairing:
		// Static allowFrom entries remain usable. The pairing workflow itself
		// is not implemented, so an unknown sender stays denied.
		if p.allowFrom["*"] || p.allowFrom[normalizeUserID(msg.AuthorID)] {
			return admission{Allowed: true}
		}
		return admission{Reason: "sender is not paired or allowlisted"}
	default:
		return admission{Reason: "unsupported direct-message policy"}
	}
}

// admit applies DM and guild policy in the same order as the existing
// installation: destination policy first, then guild/channel enablement,
// sender lists, and finally mention gating.
func (p *accessPolicy) admit(msg inboundMessage, mentioned bool) admission {
	if msg.FromSelf || msg.FromBot || msg.FromWebhook {
		return admission{Reason: "not a human message"}
	}
	if msg.IsGroupDM {
		// Group DMs are opt-in through channels.discord.dm.groupEnabled, with
		// an optional channel allowlist, and every sender still passes the DM
		// policy. They are shared rooms: no private scoping applies downstream.
		if p.dmPolicy == "disabled" {
			return admission{Reason: "direct messages are disabled"}
		}
		if !p.dmGroupEnabled {
			return admission{Reason: "group direct messages are not enabled"}
		}
		if len(p.dmGroupChannels) > 0 && !containsID(p.dmGroupChannels, msg.ChannelID) {
			return admission{Reason: "group channel is not in the group allowlist"}
		}
		return p.dmSenderAllowed(msg)
	}
	if msg.IsDM {
		return p.dmSenderAllowed(msg)
	}
	switch p.groupPolicy {
	case "disabled":
		return admission{Reason: "guild messages are disabled"}
	case "open", "allowlist":
	default:
		return admission{Reason: "unsupported guild policy"}
	}
	guild, ok := p.guild(msg.GuildID)
	if !ok {
		if p.groupPolicy != "open" {
			return admission{Reason: "guild is not allowed"}
		}
		guild = &guildSettings{}
	}
	if guild.Enabled != nil && !*guild.Enabled {
		return admission{Reason: "guild is disabled"}
	}
	channelKey := msg.ChannelID
	if msg.ParentID != "" {
		channelKey = msg.ParentID
	}
	channel, configured := guild.Channel(channelKey)
	if len(guild.Channels) > 0 && !configured {
		return admission{Reason: "channel is not in the guild allowlist"}
	}
	if channel != nil && channel.Enabled != nil && !*channel.Enabled {
		return admission{Reason: "channel is disabled"}
	}
	if !senderAllowed(msg.AuthorID, msg.RoleIDs, guild.Users, guild.Roles) {
		return admission{Reason: "sender is not allowed in this guild"}
	}
	if channel != nil && !senderAllowed(msg.AuthorID, msg.RoleIDs, channel.Users, channel.Roles) {
		return admission{Reason: "sender is not allowed in this channel"}
	}
	requireMention := true
	if guild.RequireMention != nil {
		requireMention = *guild.RequireMention
	}
	if channel != nil && channel.RequireMention != nil {
		requireMention = *channel.RequireMention
	}
	if requireMention && !mentioned {
		return admission{Reason: "message does not mention the bot"}
	}
	return admission{Allowed: true}
}

// Channel resolves a configured channel, including the wildcard entry.
func (g *guildSettings) Channel(id string) (*guildChannelSettings, bool) {
	if g.Channels == nil {
		return nil, false
	}
	if channel, ok := g.Channels[id]; ok {
		if channel == nil {
			channel = &guildChannelSettings{}
		}
		return channel, true
	}
	channel, ok := g.Channels["*"]
	if channel == nil {
		channel = &guildChannelSettings{}
	}
	return channel, ok
}

func (p *accessPolicy) guild(id string) (*guildSettings, bool) {
	if p.guilds == nil {
		return &guildSettings{}, false
	}
	if guild, ok := p.guilds[id]; ok {
		if guild == nil {
			guild = &guildSettings{}
		}
		return guild, true
	}
	guild, ok := p.guilds["*"]
	if guild == nil {
		guild = &guildSettings{}
	}
	return guild, ok
}

func senderAllowed(userID string, roleIDs, users, roles []string) bool {
	if len(users) == 0 && len(roles) == 0 {
		return true
	}
	return containsID(users, userID) || containsAnyID(roles, roleIDs)
}

func containsAnyID(allowed, actual []string) bool {
	for _, id := range actual {
		if containsID(allowed, id) {
			return true
		}
	}
	return false
}

func containsID(list []string, id string) bool {
	id = normalizeUserID(id)
	for _, entry := range list {
		if strings.TrimSpace(entry) == "*" || normalizeUserID(entry) == id {
			return true
		}
	}
	return false
}

var (
	userIDPattern      = regexp.MustCompile(`^(?:(?:discord:)?user:|discord:)?(\d+)$`)
	userMentionPattern = regexp.MustCompile(`^<@!?(\d+)>$`)
)

// normalizeUserID accepts the documented ID, mention, and prefixed forms.
func normalizeUserID(raw string) string {
	raw = strings.TrimSpace(strings.ToLower(raw))
	if match := userMentionPattern.FindStringSubmatch(raw); match != nil {
		return match[1]
	}
	if match := userIDPattern.FindStringSubmatch(raw); match != nil {
		return match[1]
	}
	return raw
}

func idSet(ids []string) map[string]bool {
	set := make(map[string]bool, len(ids))
	for _, id := range ids {
		set[normalizeUserID(id)] = true
	}
	return set
}
