package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/bwmarrin/discordgo"
)

const (
	defaultHistoryLimit = 20
	defaultReplyMode    = "off"
	maxHistoryChars     = 500
	maxHistoryLimit     = 250
	discordMaxChars     = 2000
	defaultMaxLines     = 17
	maxHistoryText      = 40 << 10
	// Sixteen chunks at Discord's 2,000-character ceiling bound one reply.
	// The limit is a safety bound, not a second delivery policy.
	maxDiscordChunks = 16
	maxReplyText     = discordMaxChars * maxDiscordChunks
)

type discordTransport struct {
	session        *discordgo.Session
	mu             sync.RWMutex
	botID          string
	historyLimit   int
	textLimit      int
	maxLines       int
	replyToMode    string
	suppressEmbeds bool
	logger         *log.Logger
	client         *http.Client
}

// httpClient lazily builds the bounded HTTP client used for attachments.
func (d *discordTransport) httpClient() *http.Client {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.client == nil {
		d.client = &http.Client{Timeout: attachmentFetchWait}
	}
	return d.client
}

func newDiscordTransport(token string, settings *fileSettings, logger *log.Logger) (*discordTransport, error) {
	// discordgo requires an immutable authorization string and retains it on
	// the session. The caller drops its references after construction, but
	// runtime, SDK, and HTTP copies cannot be reliably wiped.
	if !strings.HasPrefix(token, "Bot ") && !strings.HasPrefix(token, "Bearer ") {
		token = "Bot " + token
	}
	session, err := discordgo.New(token)
	if err != nil {
		return nil, err
	}
	session.Identify.Intents = discordgo.IntentsGuilds | discordgo.IntentsGuildMessages |
		discordgo.IntentsDirectMessages | discordgo.IntentsMessageContent
	discord := settings.Channels.Discord
	tr := &discordTransport{
		session: session, historyLimit: defaultHistoryLimit,
		textLimit: discordMaxChars, maxLines: defaultMaxLines, replyToMode: defaultReplyMode,
		suppressEmbeds: true, logger: logger,
	}
	if discord.HistoryLimit != nil {
		tr.historyLimit = clamp(*discord.HistoryLimit, 0, maxHistoryLimit)
	} else if settings.Messages != nil && settings.Messages.GroupChat != nil && settings.Messages.GroupChat.HistoryLimit != nil {
		tr.historyLimit = clamp(*settings.Messages.GroupChat.HistoryLimit, 0, maxHistoryLimit)
	}
	if discord.TextChunkLimit != nil {
		tr.textLimit = clamp(*discord.TextChunkLimit, 1, discordMaxChars)
	}
	if discord.MaxLines != nil {
		tr.maxLines = max(*discord.MaxLines, 1)
	}
	if discord.ReplyToMode != "" {
		tr.replyToMode = discord.ReplyToMode
	}
	if discord.SuppressEmbeds != nil {
		tr.suppressEmbeds = *discord.SuppressEmbeds
	}
	return tr, nil
}

// run connects and blocks until the context is canceled.
func (d *discordTransport) run(ctx context.Context, rt *runtime) error {
	rt.setDiscordHistory(d.historyLimit)
	d.session.AddHandler(func(s *discordgo.Session, m *discordgo.MessageCreate) {
		rt.handleDiscord(d, m)
	})
	d.session.AddHandler(func(s *discordgo.Session, i *discordgo.InteractionCreate) {
		rt.handleInteraction(d, i)
	})
	if err := d.session.Open(); err != nil {
		return fmt.Errorf("discord: %w", err)
	}
	if d.session.State.User == nil {
		return fmt.Errorf("discord: bot identity is unavailable after connect")
	}
	botID := d.session.State.User.ID
	rt.setBotID(botID)
	d.setBotID(botID)
	if rt.commandsEnabled() {
		if err := d.registerCommands(); err != nil {
			d.logger.Printf("command registration failed (%T)", err)
		}
	}
	<-ctx.Done()
	return nil
}

var nativeCommands = []*discordgo.ApplicationCommand{
	{Name: "new", Description: "Start a fresh conversation"},
	{Name: "reset", Description: "Reset this conversation"},
	{Name: "stop", Description: "Stop the active response"},
	{Name: "status", Description: "Show the current model and session state"},
	{Name: "model", Description: "Show or switch the model (provider/model)", Options: []*discordgo.ApplicationCommandOption{{
		Name: "model", Description: "provider/model reference", Type: discordgo.ApplicationCommandOptionString, Required: false,
	}}},
	{Name: "compact", Description: "Compact this conversation now"},
}

// registerCommands creates only missing global commands. It never bulk
// overwrites the application's command set.
func (d *discordTransport) registerCommands() error {
	application := d.session.State.Application
	applicationID := ""
	if application != nil {
		applicationID = application.ID
	}
	if applicationID == "" {
		applicationID = d.botUserID()
	}
	if applicationID == "" {
		return fmt.Errorf("application ID is unavailable")
	}
	existing, err := d.session.ApplicationCommands(applicationID, "")
	if err != nil {
		return err
	}
	have := map[string]bool{}
	for _, command := range existing {
		have[command.Name] = true
	}
	for _, command := range nativeCommands {
		if have[command.Name] {
			continue
		}
		if _, err := d.session.ApplicationCommandCreate(applicationID, "", command); err != nil {
			return err
		}
	}
	return nil
}

func (r *runtime) commandsEnabled() bool {
	return r.settings.Commands == nil || r.settings.Commands.Native == nil || r.settings.Commands.Native.enabled
}

func (r *runtime) handleInteraction(d *discordTransport, event *discordgo.InteractionCreate) {
	botID := d.botUserID()
	if event.Type != discordgo.InteractionApplicationCommand || botID == "" {
		return
	}
	if !r.commandsEnabled() {
		d.respond(event, "Native commands are disabled.", true)
		return
	}
	data := event.ApplicationCommandData()
	msg := inboundMessage{MessageID: event.ID, GuildID: event.GuildID, ChannelID: event.ChannelID, MentionsBot: true}
	if event.Member != nil && event.Member.User != nil {
		msg.AuthorID = event.Member.User.ID
		msg.RoleIDs = event.Member.Roles
	} else if event.User != nil {
		msg.AuthorID = event.User.ID
	}
	msg.IsDM = event.GuildID == ""
	channel, err := d.session.State.Channel(event.ChannelID)
	if err != nil {
		channel, err = d.session.Channel(event.ChannelID)
	}
	if err == nil {
		msg.IsThread = channel.IsThread()
		msg.ParentID = channel.ParentID
		msg.IsGroupDM = channel.Type == discordgo.ChannelTypeGroupDM
	} else if msg.IsDM {
		// An unknown direct-message channel could be a group conversation.
		msg.IsGroupDM = true
	}
	// A group DM is a shared room, not a private peer DM.
	msg.IsDM = event.GuildID == "" && !msg.IsGroupDM
	if decision := r.policy.admit(msg, true); !decision.Allowed {
		d.respond(event, "Not available here.", true)
		return
	}
	if !r.policy.isOwner(msg.AuthorID) {
		d.respond(event, "Not authorized.", true)
		return
	}
	admitted, err := r.state.admitEvent(botID+"\x00interaction:"+event.ID, time.Now())
	if err != nil {
		d.respond(event, "Unavailable.", true)
		return
	}
	if !admitted {
		d.respond(event, "Already handled.", true)
		return
	}
	chat, err := r.conversation(r.routeKey(msg))
	if err != nil {
		d.respond(event, "Unavailable.", true)
		return
	}
	switch data.Name {
	case "stop":
		if chat.stop() {
			d.respond(event, "Stopped.", true)
		} else {
			d.respond(event, "Nothing to stop.", true)
		}
	case "status":
		d.respond(event, fmt.Sprintf("Model: %s\nStatus: %s", r.agent.Model, chat.status()), true)
	case "new", "reset":
		if chat.busy() {
			d.respond(event, "Busy; stop the active run first.", true)
			return
		}
		if !chat.enqueue(sourceTurn{message: msg, text: "/" + data.Name, noAck: true}) {
			d.respond(event, "Busy; try again shortly.", true)
			return
		}
		d.respond(event, "Queued.", true)
	case "model":
		ref := ""
		if len(data.Options) > 0 && data.Options[0].Name == "model" {
			ref = strings.TrimSpace(data.Options[0].StringValue())
		}
		text := "/model"
		if ref != "" {
			text = "/model " + ref
		}
		if !chat.enqueue(sourceTurn{message: msg, text: text}) {
			d.respond(event, "Busy; try again shortly.", true)
			return
		}
		d.respond(event, "Queued.", true)
	case "compact":
		if !chat.enqueue(sourceTurn{message: msg, text: "/compact"}) {
			d.respond(event, "Busy; try again shortly.", true)
			return
		}
		d.respond(event, "Queued.", true)
	default:
		d.respond(event, "Unknown command.", true)
	}
}

func (d *discordTransport) close() {
	if d.session != nil {
		d.session.Close()
	}
}

func (d *discordTransport) setBotID(id string) {
	d.mu.Lock()
	d.botID = id
	d.mu.Unlock()
}

func (d *discordTransport) botUserID() string {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.botID
}

func (d *discordTransport) respond(event *discordgo.InteractionCreate, content string, ephemeral bool) {
	flags := discordgo.MessageFlags(0)
	if ephemeral {
		flags = discordgo.MessageFlagsEphemeral
	}
	if err := d.session.InteractionRespond(event.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{Content: content, Flags: flags},
	}); err != nil {
		d.logger.Printf("interaction response failed (%T)", err)
	}
}

func (r *runtime) handleDiscord(d *discordTransport, event *discordgo.MessageCreate) {
	botID := d.botUserID()
	if botID == "" || event.Author == nil {
		return
	}
	msg := normalizeDiscord(d.session, event, botID)
	decision := r.policy.admit(msg, msg.MentionsBot || msg.RepliesToBot)
	if !decision.Allowed {
		return
	}
	text := stripBotMention(event.Content, botID)
	if text == "" && len(event.Attachments) == 0 {
		return
	}
	command, _, isCommand := commandWithArg(text)
	if isCommand && !r.policy.isOwner(msg.AuthorID) {
		return
	}
	admitted, err := r.state.admitEvent(botID+"\x00"+msg.MessageID, time.Now())
	if err != nil {
		r.logger.Printf("admission failed (%T)", err)
		return
	}
	if !admitted {
		return
	}
	if text == "" {
		// Attachment-only message: the turn itself decides whether anything is
		// usable (image for a vision model, bounded text); otherwise the
		// conversation worker sends the unsupported notice.
		if len(event.Attachments) == 0 {
			return
		}
		if isCommand {
			return
		}
	}
	if isCommand && command == "/stop" {
		chat, err := r.conversation(r.routeKey(msg))
		if err != nil {
			r.logger.Printf("stop failed (%T)", err)
			return
		}
		if chat.stop() {
			chat.replyCommand(sourceTurn{message: msg}, "✅ Stopped.")
		} else {
			chat.replyCommand(sourceTurn{message: msg}, "Nothing to stop.")
		}
		return
	}
	chat, err := r.conversation(r.routeKey(msg))
	if err != nil {
		r.logger.Printf("conversation unavailable (%T)", err)
		return
	}
	turn := sourceTurn{message: msg, text: text, replyTo: event.ID}
	for i, attachment := range event.Attachments {
		if i >= maxAttachments {
			break
		}
		turn.attachments = append(turn.attachments, inboundAttachment{
			URL: attachment.URL, ContentType: attachment.ContentType, Size: int64(attachment.Size), Name: attachment.Filename,
		})
	}
	if !chat.enqueue(turn) {
		if err := r.deliver(turn, "Busy; try again shortly."); err != nil {
			r.logger.Printf("busy reply failed (%T)", err)
		}
	}
}

// normalizeDiscord converts one gateway event into admission input.
func normalizeDiscord(s *discordgo.Session, m *discordgo.MessageCreate, botID string) inboundMessage {
	msg := inboundMessage{
		MessageID: m.ID, GuildID: m.GuildID, ChannelID: m.ChannelID, AuthorID: m.Author.ID,
		IsDM: m.GuildID == "", FromSelf: m.Author.ID == botID,
		FromBot: m.Author.Bot, FromWebhook: m.WebhookID != "",
	}
	if m.Member != nil {
		msg.RoleIDs = m.Member.Roles
	}
	channel, err := s.State.Channel(m.ChannelID)
	if err != nil {
		// A thread created after login can be absent from the state cache.
		channel, err = s.Channel(m.ChannelID)
	}
	if err == nil {
		msg.IsThread = channel.IsThread()
		msg.ParentID = channel.ParentID
		msg.IsGroupDM = channel.Type == discordgo.ChannelTypeGroupDM
	} else if msg.IsDM {
		// An unknown direct-message channel could be a group conversation.
		msg.IsGroupDM = true
	}
	// A group DM is a shared room, not a private peer DM: it never receives
	// per-peer DM scoping or private prompt material.
	msg.IsDM = m.GuildID == "" && !msg.IsGroupDM
	for _, user := range m.Mentions {
		if user.ID == botID {
			msg.MentionsBot = true
			break
		}
	}
	if m.MessageReference != nil {
		msg.ReferenceID = m.MessageReference.MessageID
	}
	if m.ReferencedMessage != nil && m.ReferencedMessage.Author != nil && m.ReferencedMessage.Author.ID == botID {
		msg.RepliesToBot = true
	}
	return msg
}

// fetchAttachment downloads one attachment into memory with a bounded size.
// It never writes to disk and accepts only Discord's own CDN hosts, so a
// message can never make the bot fetch an arbitrary address.
func (d *discordTransport) fetchAttachment(ctx context.Context, url string, maxBytes int64) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := checkAttachmentHost(url); err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := d.httpClient().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("attachment download status %d", resp.StatusCode)
	}
	if resp.ContentLength > 0 && resp.ContentLength > maxBytes {
		return nil, fmt.Errorf("attachment is too large")
	}
	return io.ReadAll(io.LimitReader(resp.Body, maxBytes+1))
}

// discordAttachmentHosts are the hosts Discord serves attachments from.
var discordAttachmentHosts = map[string]bool{
	"cdn.discordapp.com":   true,
	"media.discordapp.net": true,
}

func checkAttachmentHost(raw string) error {
	parsed, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("attachment URL is invalid")
	}
	if parsed.Scheme != "https" {
		return fmt.Errorf("attachment URL must use https")
	}
	if !discordAttachmentHosts[parsed.Hostname()] {
		return fmt.Errorf("attachment host is not allowed")
	}
	return nil
}

func (d *discordTransport) message(ctx context.Context, channelID, messageID string) (historyMessage, error) {
	if err := ctx.Err(); err != nil {
		return historyMessage{}, err
	}
	message, err := d.session.ChannelMessage(channelID, messageID)
	if err != nil {
		return historyMessage{}, err
	}
	if message.Author == nil {
		return historyMessage{}, fmt.Errorf("referenced message has no author")
	}
	return historyMessage{ID: message.ID, Author: message.Author.Username, Text: truncateUTF8(strings.TrimSpace(message.Content), maxHistoryChars)}, nil
}

func stripBotMention(text, botID string) string {
	text = strings.ReplaceAll(text, "<@"+botID+">", "")
	text = strings.ReplaceAll(text, "<@!"+botID+">", "")
	return strings.TrimSpace(text)
}

// readMessages returns oldest-first history for one conversation.
func (d *discordTransport) readMessages(ctx context.Context, channelID string, limit int, before, after string) ([]historyMessage, error) {
	if limit <= 0 {
		return nil, nil
	}
	if limit > maxHistoryLimit {
		limit = maxHistoryLimit
	}
	var pages [][]*discordgo.Message
	remaining := limit
	for remaining > 0 {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		pageSize := min(remaining, 100)
		page, err := d.session.ChannelMessages(channelID, pageSize, before, after, "")
		if err != nil {
			return nil, err
		}
		if len(page) == 0 {
			break
		}
		pages = append(pages, page)
		remaining -= len(page)
		if after != "" || len(page) < pageSize {
			break
		}
		before = page[len(page)-1].ID
	}
	out := make([]historyMessage, 0, limit)
	for i := len(pages) - 1; i >= 0; i-- {
		page := pages[i]
		for j := len(page) - 1; j >= 0; j-- {
			message := page[j]
			if message.Author == nil {
				continue
			}
			text := strings.TrimSpace(message.Content)
			if text == "" {
				continue
			}
			out = append(out, historyMessage{ID: message.ID, Author: message.Author.Username, AuthorID: message.Author.ID, Text: truncateUTF8(text, maxHistoryChars)})
		}
	}
	total := 0
	start := 0
	for i := len(out) - 1; i >= 0; i-- {
		total += len(out[i].Author) + len(out[i].Text) + len(out[i].ID) + 4
		if total > maxHistoryText {
			start = i + 1
			break
		}
	}
	return out[start:], nil
}

// turnHistory reads bounded context before the current message.
func (r *runtime) turnHistory(ctx context.Context, turn sourceTurn) []historyMessage {
	limit := r.historyLimit()
	if turn.message.IsDM && r.settings.Channels.Discord.DMHistoryLimit != nil {
		limit = clamp(*r.settings.Channels.Discord.DMHistoryLimit, 0, maxHistoryLimit)
	}
	if limit == 0 {
		return nil
	}
	messages, err := r.transport.readMessages(ctx, turn.message.ChannelID, limit, turn.message.MessageID, "")
	if err != nil {
		r.logger.Printf("history read failed")
		return nil
	}
	botID := r.botUserID()
	filtered := messages[:0]
	for _, message := range messages {
		if botID != "" && message.AuthorID == botID {
			continue
		}
		filtered = append(filtered, message)
	}
	return filtered
}

func (d *discordTransport) sendMessage(ctx context.Context, channelID, replyTo, text string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	chunks := splitDiscordText(text, d.textLimit, d.maxLines)
	var last string
	for i, chunk := range chunks {
		if err := ctx.Err(); err != nil {
			return last, err
		}
		message := &discordgo.MessageSend{
			Content:         chunk,
			AllowedMentions: &discordgo.MessageAllowedMentions{},
		}
		if d.suppressEmbeds {
			message.Flags = discordgo.MessageFlagsSuppressEmbeds
		}
		if replyTo != "" && d.replyToMode != "off" && (i == 0 || d.replyToMode == "all") {
			message.Reference = &discordgo.MessageReference{MessageID: replyTo, ChannelID: channelID}
		}
		sent, err := d.session.ChannelMessageSendComplex(channelID, message)
		if err != nil {
			return last, err
		}
		last = sent.ID
	}
	return last, nil
}

// splitDiscordText applies the configured character and line limits, marking
// replies cut short by the message count or text budget.
func splitDiscordText(text string, limit, maxLines int) []string {
	truncated := false
	if limit > 0 && len(text) > limit*maxDiscordChunks {
		text = truncateUTF8(text, limit*maxDiscordChunks)
		truncated = true
	}

	contentLimit, contentLineLimit := limit, maxLines
	fenceOverhead := discordFenceOverhead(text)
	balance := fenceOverhead > 0
	if balance && limit > 0 {
		if fenceOverhead >= limit {
			balance = false
		} else {
			contentLimit -= fenceOverhead
		}
	}
	if balance && maxLines > 0 {
		if maxLines <= 2 {
			// There is no room for content plus both fence markers.
			balance = false
		} else {
			// Reopening and closing a fence each add one line to a chunk.
			contentLineLimit -= 2
		}
	}
	if !balance {
		contentLimit, contentLineLimit = limit, maxLines
	}

	var out []string
	for _, chunk := range chunkText(text, contentLimit) {
		out = append(out, chunkLines(chunk, contentLineLimit)...)
	}
	if len(out) > maxDiscordChunks {
		out = out[:maxDiscordChunks]
		truncated = true
	}
	if truncated {
		if len(out) == 0 {
			out = append(out, "")
		}
		out[len(out)-1] = markDiscordTruncated(out[len(out)-1], contentLimit)
	}
	if balance {
		balanced := balanceFences(append([]string(nil), out...))
		if discordChunksWithinLimits(balanced, limit, maxLines) {
			return balanced
		}
	}
	return out
}

// discordFenceOverhead returns the largest combined prefix and suffix that
// fence balancing can add to one message, including their line breaks.
func discordFenceOverhead(text string) int {
	var current *fence
	overhead := 0
	for _, line := range strings.Split(text, "\n") {
		next := scanFence(line, current)
		if current == nil && next != nil {
			overhead = max(overhead, len(next.open)+len(next.close)+2)
		}
		current = next
	}
	return overhead
}

func discordChunksWithinLimits(chunks []string, maxChars, maxLines int) bool {
	for _, chunk := range chunks {
		if maxChars > 0 && len(chunk) > maxChars && chunk != "[truncated]" {
			return false
		}
		if maxLines > 0 && strings.Count(chunk, "\n")+1 > maxLines {
			return false
		}
	}
	return true
}

// markDiscordTruncated leaves the notice on the same line so it does not
// consume another line of the configured message limit. The notice itself
// takes precedence when the configured character limit is too small for it.
func markDiscordTruncated(text string, limit int) string {
	note := "[truncated]"
	if limit > 0 && limit <= len(note) {
		return note
	}
	if limit <= 0 {
		return text + " " + note
	}
	room := limit - len(note)
	if room <= 0 {
		return note
	}
	text = strings.TrimRight(truncateUTF8(text, room-1), " ")
	if text == "" {
		return note
	}
	return text + " " + note
}

// fence is the open code fence carried across chunks. close is the minimal
// marker that closes it; open restores the original fence and language hint.
type fence struct {
	open  string
	close string
	char  byte
	size  int
}

// balanceFences closes an open code fence at a chunk boundary and reopens it
// in the next chunk. Callers must reserve room for the markers and verify the
// resulting character and line limits.
func balanceFences(chunks []string) []string {
	var current *fence
	for i, chunk := range chunks {
		body := chunk
		if current != nil {
			chunk = current.open + "\n" + chunk
		}
		open := scanFence(body, current)
		if open != nil {
			chunk += "\n" + open.close
		}
		chunks[i] = chunk
		current = open
	}
	return chunks
}

// scanFence returns the fence still open at the end of text.
func scanFence(text string, current *fence) *fence {
	for _, line := range strings.Split(text, "\n") {
		trimmed := strings.TrimLeft(line, " ")
		if len(line)-len(trimmed) > 3 || len(trimmed) < 3 {
			continue
		}
		char := trimmed[0]
		if char != '`' && char != '~' {
			continue
		}
		size := 0
		for size < len(trimmed) && trimmed[size] == char {
			size++
		}
		if size < 3 {
			continue
		}
		if current == nil {
			indent := line[:len(line)-len(trimmed)]
			current = &fence{open: line, close: indent + strings.Repeat(string(char), size), char: char, size: size}
			continue
		}
		if char == current.char && size >= current.size {
			current = nil
		}
	}
	return current
}

func chunkLines(text string, maxLines int) []string {
	if maxLines <= 0 {
		return []string{text}
	}
	lines := strings.Split(text, "\n")
	if len(lines) <= maxLines {
		return []string{text}
	}
	var out []string
	for start := 0; start < len(lines); start += maxLines {
		out = append(out, strings.Join(lines[start:min(start+maxLines, len(lines))], "\n"))
	}
	return out
}

func clamp(value, low, high int) int {
	if value < low {
		return low
	}
	if value > high {
		return high
	}
	return value
}
