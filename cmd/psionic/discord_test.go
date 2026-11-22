package main

import (
	"context"
	"io"
	"log"
	"strings"
	"testing"
	"time"

	"github.com/bwmarrin/discordgo"
)

func TestNormalizeDiscord(t *testing.T) {
	session, err := discordgo.New("Bot test")
	if err != nil {
		t.Fatal(err)
	}
	botID := "999999999999999999"
	event := &discordgo.MessageCreate{Message: &discordgo.Message{
		ID:        "123456789012345678",
		GuildID:   "323456789012345678",
		ChannelID: "423456789012345678",
		Content:   "hello <@" + botID + ">",
		Author:    &discordgo.User{ID: "223456789012345678", Username: "Aster"},
		Member:    &discordgo.Member{Roles: []string{"623456789012345678"}},
		Mentions:  []*discordgo.User{{ID: botID}},
	}}
	msg := normalizeDiscord(session, event, botID)
	if !msg.MentionsBot || msg.FromSelf || msg.FromBot || msg.IsDM {
		t.Fatalf("normalized = %+v", msg)
	}
	if len(msg.RoleIDs) != 1 || msg.MessageID != event.ID {
		t.Fatalf("identity = %+v", msg)
	}
	event.MessageReference = &discordgo.MessageReference{MessageID: "723456789012345678"}
	event.ReferencedMessage = &discordgo.Message{Author: &discordgo.User{ID: botID}}
	if got := normalizeDiscord(session, event, botID); !got.RepliesToBot || got.ReferenceID != event.MessageReference.MessageID {
		t.Fatal("reply to the bot was not recognized")
	}
	event.Author.Bot = true
	if got := normalizeDiscord(session, event, botID); !got.FromBot {
		t.Fatal("bot author was not recognized")
	}
	event.WebhookID = "823456789012345678"
	if got := normalizeDiscord(session, event, botID); !got.FromWebhook {
		t.Fatal("webhook was not recognized")
	}
}

func TestStripBotMention(t *testing.T) {
	botID := "999999999999999999"
	for _, text := range []string{"<@" + botID + "> /new", "<@!" + botID + "> hello"} {
		if got := stripBotMention(text, botID); strings.Contains(got, botID) {
			t.Fatalf("mention survived: %q", got)
		}
	}
}

func TestSplitDiscordTextRespectsLimits(t *testing.T) {
	text := strings.Repeat("line\n", 40)
	chunks := splitDiscordText(text, 40, 5)
	if len(chunks) < 2 {
		t.Fatalf("chunks = %d", len(chunks))
	}
	for _, chunk := range chunks {
		if len(chunk) > 40 {
			t.Fatalf("character limit exceeded: %q", chunk)
		}
		if strings.Count(chunk, "\n") > 4 {
			t.Fatalf("line limit exceeded: %q", chunk)
		}
	}
}

func TestSplitDiscordTextBalancesCodeFences(t *testing.T) {
	var b strings.Builder
	b.WriteString("before\n```go\n")
	for i := 0; i < 40; i++ {
		b.WriteString("fmt.Println(\"line\")\n")
	}
	b.WriteString("```\nafter")
	chunks := splitDiscordText(b.String(), 120, 17)
	if len(chunks) < 2 {
		t.Fatalf("chunks = %d", len(chunks))
	}
	for i, chunk := range chunks {
		open := strings.Count(chunk, "```")
		if open%2 != 0 {
			t.Fatalf("chunk %d has an unbalanced fence: %q", i, chunk)
		}
	}
}

func TestSplitDiscordTextBoundsChunkCount(t *testing.T) {
	text := strings.Repeat("x", discordMaxChars*maxDiscordChunks+100)
	chunks := splitDiscordText(text, discordMaxChars, maxDiscordChunks)
	if len(chunks) > maxDiscordChunks {
		t.Fatalf("chunks = %d, limit = %d", len(chunks), maxDiscordChunks)
	}
}

func TestSplitDiscordTextMarksDroppedChunks(t *testing.T) {
	for _, tc := range []struct {
		name  string
		text  string
		limit int
		lines int
	}{
		{"line limit", strings.Repeat("short line\n", 400), 2000, 17},
		{"byte budget", strings.Repeat("x", 40000), 2000, 17},
		{"small limit", strings.Repeat("x", 100), 2, 17},
	} {
		t.Run(tc.name, func(t *testing.T) {
			chunks := splitDiscordText(tc.text, tc.limit, tc.lines)
			if len(chunks) == 0 || len(chunks) > maxDiscordChunks {
				t.Fatalf("chunk count = %d", len(chunks))
			}
			if !strings.HasSuffix(chunks[len(chunks)-1], "[truncated]") {
				t.Fatalf("missing truncation notice in %q", chunks[len(chunks)-1])
			}
			for i, chunk := range chunks {
				if len(chunk) > tc.limit && !(i == len(chunks)-1 && chunk == "[truncated]") {
					t.Fatalf("chunk exceeds character limit: %q", chunk)
				}
				if strings.Count(chunk, "\n") >= tc.lines {
					t.Fatalf("chunk exceeds line limit: %q", chunk)
				}
			}
		})
	}
}

func TestSplitDiscordTextMarksTruncationInsideFence(t *testing.T) {
	text := "```go\n" + strings.Repeat("line\n", 400) + "```"
	chunks := splitDiscordText(text, 2000, 5)
	if len(chunks) != maxDiscordChunks {
		t.Fatalf("chunk count = %d", len(chunks))
	}
	last := chunks[len(chunks)-1]
	if !strings.Contains(last, "[truncated]") || strings.Count(last, "```") != 2 {
		t.Fatalf("truncated fence = %q", last)
	}
}

func TestBalanceFencesKeepsAClosingFenceAtTheChunkStart(t *testing.T) {
	chunks := balanceFences([]string{"```go\ncode", "```\nafter"})
	if len(chunks) != 2 {
		t.Fatalf("chunks = %d", len(chunks))
	}
	if got := strings.Count(chunks[1], "```"); got != 2 {
		t.Fatalf("reopened chunk has %d fences: %q", got, chunks[1])
	}
}

func TestNewDiscordTransportClampsLimits(t *testing.T) {
	settings := mustSettings(t, `{
  channels: { discord: {
    token: "test-token",
    historyLimit: 1000,
    textChunkLimit: 5000,
    maxLinesPerMessage: 1,
    replyToMode: "all",
    suppressEmbeds: false,
  } },
}`)
	transport, err := newDiscordTransport("test-token", settings, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer transport.session.Close()
	if transport.historyLimit != maxHistoryLimit || transport.textLimit != discordMaxChars {
		t.Fatalf("limits = %d, %d", transport.historyLimit, transport.textLimit)
	}
	if transport.maxLines != 1 || transport.replyToMode != "all" || transport.suppressEmbeds {
		t.Fatalf("transport = %+v", transport)
	}
}

func TestUnauthorizedCommandDoesNotAdmitAnEvent(t *testing.T) {
	state, err := openState(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer state.close()
	settings := mustSettings(t, `{
  channels: { discord: { dmPolicy: "allowlist", allowFrom: ["123456789012345678"], groupPolicy: "open", historyLimit: 0 } },
  commands: { ownerAllowFrom: ["123456789012345678"] },
  session: { reset: { mode: "none" } },
}`)
	rt := &runtime{
		settings: settings, policy: newAccessPolicy(settings), state: state,
		logger: log.New(io.Discard, "", 0),
	}
	session, err := discordgo.New("Bot test")
	if err != nil {
		t.Fatal(err)
	}
	botID := "999999999999999999"
	if err := session.State.GuildAdd(&discordgo.Guild{ID: "323456789012345678"}); err != nil {
		t.Fatal(err)
	}
	if err := session.State.ChannelAdd(&discordgo.Channel{
		ID: "423456789012345678", GuildID: "323456789012345678", Type: discordgo.ChannelTypeGuildText,
	}); err != nil {
		t.Fatal(err)
	}
	d := &discordTransport{session: session, logger: log.New(io.Discard, "", 0)}
	d.setBotID(botID)
	event := &discordgo.MessageCreate{Message: &discordgo.Message{
		ID: "523456789012345678", GuildID: "323456789012345678", ChannelID: "423456789012345678",
		Content:  "<@" + botID + "> /status",
		Author:   &discordgo.User{ID: "223456789012345678"},
		Mentions: []*discordgo.User{{ID: botID}},
	}}
	rt.handleDiscord(d, event)
	if admitted, err := state.admitEvent(botID+"\x00"+event.ID, time.Now()); err != nil || !admitted {
		t.Fatalf("unauthorized command was admitted: admitted=%v err=%v", admitted, err)
	}
}

func TestConversationBusyTracksAnActiveRun(t *testing.T) {
	chat := &conversation{}
	if chat.busy() {
		t.Fatal("idle conversation reported busy")
	}
	chat.running = true
	if !chat.busy() {
		t.Fatal("active conversation reported idle")
	}
	chat.running = false
	chat.runStop = func() {}
	if !chat.busy() {
		t.Fatal("cancellable run reported idle")
	}
}

func TestRouteKeySeparatesDMScopes(t *testing.T) {
	settings := mustSettings(t, `{ channels: { discord: {} }, session: { dmScope: "per-peer" } }`)
	rt := newRuntime(settings, selectedAgent{Workspace: t.TempDir()}, nil, t.TempDir(), nil, configDefaults(), nil, log.New(io.Discard, "", 0))
	if got := rt.routeKey(inboundMessage{IsDM: true, ChannelID: "1", AuthorID: "2"}); got != "dm:2" {
		t.Fatalf("per-peer key = %q", got)
	}
	rt.settings.Session.DMScope = "main"
	if got := rt.routeKey(inboundMessage{IsDM: true, ChannelID: "1", AuthorID: "2"}); got != "dm:main" {
		t.Fatalf("main key = %q", got)
	}
	rt.settings.Session.DMScope = "per-channel-peer"
	if got := rt.routeKey(inboundMessage{IsDM: true, ChannelID: "1", AuthorID: "2"}); got != "dm:1:2" {
		t.Fatalf("per-channel-peer key = %q", got)
	}
	if got := rt.routeKey(inboundMessage{GuildID: "3", ChannelID: "4"}); got != "guild:3:4" {
		t.Fatalf("guild key = %q", got)
	}
}

func TestRouteKeyIncludesBotAndAgent(t *testing.T) {
	rt := &runtime{agent: selectedAgent{ID: "main"}, botID: "123456789012345678"}
	if got := rt.routeKey(inboundMessage{GuildID: "3", ChannelID: "4"}); got != "123456789012345678:main:guild:3:4" {
		t.Fatalf("route key = %q", got)
	}
}

func TestTurnHistoryOmitsTheBotEcho(t *testing.T) {
	rt := &runtime{
		botID: "999999999999999999", discordHistory: 10,
		settings: &fileSettings{Channels: &channelsSettings{Discord: &discordSettings{}}},
		transport: &recordingTransport{history: []historyMessage{
			{ID: "1", Author: "Aster", AuthorID: "223456789012345678", Text: "hello"},
			{ID: "2", Author: "Psionic", AuthorID: "999999999999999999", Text: "answer"},
		}},
	}
	got := rt.turnHistory(context.Background(), sourceTurn{message: inboundMessage{MessageID: "3"}})
	if len(got) != 1 || got[0].Text != "hello" {
		t.Fatalf("history = %+v", got)
	}
}
