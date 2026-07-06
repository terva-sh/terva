package discord

// The api seam: every Discord SDK touch goes through this narrow
// interface, so transport logic tests run against a fake with zero
// network and an SDK break (or swap — see the plan's disgo risk notes)
// is contained to this file.

import (
	"context"
	"fmt"
	"os"
	"sync"

	"github.com/disgoorg/disgo"
	"github.com/disgoorg/disgo/bot"
	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/events"
	"github.com/disgoorg/disgo/gateway"
	"github.com/disgoorg/disgo/rest"
	"github.com/disgoorg/snowflake/v2"
)

// inboundAttachment is one attachment on an inbound message, by URL —
// the transport decides what to download.
type inboundAttachment struct {
	URL         string
	Filename    string
	ContentType string
	Size        int64
	DurationMS  int
}

// inboundChatEvent is one stage-D happening from the gateway.
type inboundChatEvent struct {
	Kind      string // "edited" | "deleted" | "reaction_add" | "reaction_remove"
	ChannelID string
	MessageID string
	TS        int64
	Content   string
	Mentions  []string
	UserID    string
	Username  string
	Emoji     string // unicode emoji, or the custom emoji's ID
}

// inboundMessage is the seam's normalized inbound event.
type inboundMessage struct {
	MessageID   string
	TS          int64 // unix ms, from the snowflake
	ChannelID   string
	GuildID     string // "" for DMs
	IsThread    bool   // the channel is a thread (kind "thread" on the wire)
	AuthorID    string
	AuthorName  string
	AuthorIsBot bool // any bot or webhook author (echo hygiene upstream)
	Content     string
	ReplyToID   string   // id of the message this replies to, if any
	Mentions    []string // user ids mentioned; the transport spots itself here
	Attachments []inboundAttachment
}

// inboundMembership is one guild join/leave, mapped onto the guild's
// system channel (the closest thing a Discord SERVER has to "the
// chat the bot landed in"). Discord does not say who added the bot.
type inboundMembership struct {
	GuildID   string // the guild (scope) the change concerns
	ChannelID string // system channel; "" when the guild has none
	Title     string // guild name
	Added     bool
}

// inboundInteraction is one component (button) interaction from the
// gateway — the seam's normalized shape. Identity here is
// server-attested: Discord proves who clicked.
type inboundInteraction struct {
	InteractionID string
	Token         string
	CustomID      string
	UserID        string
	Username      string
	ChannelID     string
	MessageID     string
}

// askButton is one button on an ask message. Style carries the wire's
// rendering hint ("affirm" → green, "deny" → red, else grey).
type askButton struct {
	Key   string
	Label string
	Style string
}

// api is what the transport needs from Discord.
type api interface {
	// me validates credentials and returns the bot's own identity.
	// Works before open (plain REST).
	me(ctx context.Context) (id, username string, err error)
	// appID returns the application id the OAuth2 invite URL wants.
	// It equals the bot's user id for modern bots, but is fetched from
	// the application-info endpoint so ancient bots get the right
	// value. Works before open (plain REST); setup-time only.
	appID(ctx context.Context) (string, error)
	// open connects the gateway and streams inbound messages, button
	// interactions, guild membership changes, and message events
	// (edits, deletes, reactions) until close. Reconnection/backoff
	// inside is the SDK's job; open returning an error is fatal.
	open(ctx context.Context, onMessage func(inboundMessage), onInteraction func(inboundInteraction), onMembership func(inboundMembership), onChatEvent func(inboundChatEvent)) error
	close()
	sendMessage(ctx context.Context, channelID, replyTo, text string) (messageID string, err error)
	sendFile(ctx context.Context, channelID, path, name, caption string) error
	typing(ctx context.Context, channelID string) error
	// sendAsk posts text with button rows; each button's custom_id is
	// "ask:<askID>:<key>" so a click routes with no state but the map
	// the transport keeps.
	sendAsk(ctx context.Context, channelID, replyTo, text, askID string, buttons []askButton) (messageID string, err error)
	// updateAskMessage swaps the ask message's content and strips its
	// buttons — the ask_close rendering.
	updateAskMessage(ctx context.Context, channelID, messageID, content string) error
	// ackComponent acknowledges a click without visible effect
	// (DEFERRED_UPDATE_MESSAGE — inside Discord's 3s deadline).
	ackComponent(ctx context.Context, interactionID, token string) error
	// respondEphemeral answers a click with a note only the clicker
	// sees ("not for you", "no longer active").
	respondEphemeral(ctx context.Context, interactionID, token, text string) error
	// sendAsWebhook posts under an alternate display name through the
	// channel's managed webhook (created on first use, cached) — the
	// PluralKit pattern. Fails on DMs and without MANAGE_WEBHOOKS; the
	// transport falls back to a prefixed plain send.
	sendAsWebhook(ctx context.Context, channelID, name, text string) (messageID string, err error)
	// createThread opens a thread in channelID — anchored to a message
	// when fromMessageID is set, standalone otherwise — and returns
	// the thread's channel id (the new chat).
	createThread(ctx context.Context, channelID, fromMessageID, name string) (threadID string, err error)
	// editMessage rewrites one of the bot's own messages.
	editMessage(ctx context.Context, channelID, messageID, text string) error
	// deleteMessage retracts one of the bot's own messages.
	deleteMessage(ctx context.Context, channelID, messageID string) error
	// react toggles the bot's own reaction (unicode emoji).
	react(ctx context.Context, channelID, messageID, emoji string, remove bool) error
}

// castWebhookName names the managed webhook terva creates per channel
// for speaker messages. Stable so restarts reuse it.
const castWebhookName = "terva cast"

// newAPI builds the disgo-backed seam. Variable so tests swap it.
var newAPI = func(token string) (api, error) {
	return &disgoAPI{token: token}, nil
}

type disgoAPI struct {
	token  string
	client *bot.Client

	// webhooks caches the managed cast webhook per channel (id +
	// token), so speaker messages cost one REST call after the first.
	webhookMu sync.Mutex
	webhooks  map[string]cachedWebhook
}

type cachedWebhook struct {
	id    snowflake.ID
	token string
}

// intents: deliberately NO privileged intents. DMs and @mentions
// deliver content without MESSAGE_CONTENT; other guild messages arrive
// with empty content and the transport skips them — which is exactly
// the mention-gated group posture the connector wants by default (see
// docs/plans/discord-connector.md).
var wantedIntents = []gateway.Intents{
	gateway.IntentGuilds,
	gateway.IntentGuildMessages,
	gateway.IntentDirectMessages,
	// Stage D: reaction streams — also unprivileged.
	gateway.IntentGuildMessageReactions,
	gateway.IntentDirectMessageReactions,
}

func (d *disgoAPI) ensureClient(onMessage func(inboundMessage), onInteraction func(inboundInteraction), onMembership func(inboundMembership), onChatEvent func(inboundChatEvent)) error {
	if d.client != nil {
		return nil
	}
	opts := []bot.ConfigOpt{
		bot.WithGatewayConfigOpts(gateway.WithIntents(wantedIntents...)),
	}
	if onMessage != nil {
		opts = append(opts, bot.WithEventListenerFunc(func(e *events.MessageCreate) {
			im := normalizeMessage(e.Message)
			// The message object carries no channel type; the Guilds
			// intent keeps threads in the cache, so resolve kind here.
			if ch, ok := e.Client().Caches.Channel(e.Message.ChannelID); ok {
				switch ch.Type() {
				case discord.ChannelTypeGuildPublicThread, discord.ChannelTypeGuildPrivateThread, discord.ChannelTypeGuildNewsThread:
					im.IsThread = true
				}
			}
			onMessage(im)
		}))
	}
	if onChatEvent != nil {
		opts = append(opts,
			bot.WithEventListenerFunc(func(e *events.MessageUpdate) {
				ev := inboundChatEvent{Kind: "edited",
					ChannelID: e.ChannelID.String(), MessageID: e.MessageID.String(),
					Content: e.Message.Content}
				if e.Message.EditedTimestamp != nil {
					ev.TS = e.Message.EditedTimestamp.UnixMilli()
				}
				for _, u := range e.Message.Mentions {
					ev.Mentions = append(ev.Mentions, u.ID.String())
				}
				onChatEvent(ev)
			}),
			bot.WithEventListenerFunc(func(e *events.MessageDelete) {
				onChatEvent(inboundChatEvent{Kind: "deleted",
					ChannelID: e.ChannelID.String(), MessageID: e.MessageID.String()})
			}),
			bot.WithEventListenerFunc(func(e *events.MessageReactionAdd) {
				ev := inboundChatEvent{Kind: "reaction_add",
					ChannelID: e.ChannelID.String(), MessageID: e.MessageID.String(),
					UserID: e.UserID.String(), Emoji: emojiKey(e.Emoji)}
				if e.Member != nil {
					ev.Username = e.Member.User.Username
				}
				onChatEvent(ev)
			}),
			bot.WithEventListenerFunc(func(e *events.MessageReactionRemove) {
				onChatEvent(inboundChatEvent{Kind: "reaction_remove",
					ChannelID: e.ChannelID.String(), MessageID: e.MessageID.String(),
					UserID: e.UserID.String(), Emoji: emojiKey(e.Emoji)})
			}))
	}
	if onMembership != nil {
		// GuildJoin fires for genuine joins only (startup GUILD_CREATE
		// bursts surface as GuildReady), so this never spams at boot.
		opts = append(opts,
			bot.WithEventListenerFunc(func(e *events.GuildJoin) {
				onMembership(normalizeGuildMembership(e.Guild.Guild, true))
			}),
			bot.WithEventListenerFunc(func(e *events.GuildLeave) {
				onMembership(normalizeGuildMembership(e.Guild, false))
			}))
	}
	if onInteraction != nil {
		// Component interactions arrive with NO intent — the posture
		// stays unprivileged.
		opts = append(opts, bot.WithEventListenerFunc(func(e *events.ComponentInteractionCreate) {
			onInteraction(normalizeInteraction(e))
		}))
	}
	client, err := disgo.New(d.token, opts...)
	if err != nil {
		return err
	}
	d.client = client
	return nil
}

func normalizeInteraction(e *events.ComponentInteractionCreate) inboundInteraction {
	return inboundInteraction{
		InteractionID: e.ID().String(),
		Token:         e.Token(),
		CustomID:      e.Data.CustomID(),
		UserID:        e.User().ID.String(),
		Username:      e.User().Username,
		ChannelID:     e.Message.ChannelID.String(),
		MessageID:     e.Message.ID.String(),
	}
}

// emojiKey pins the doctrine: custom emoji key on their ID (names
// null out when the emoji is deleted); unicode emoji ride as-is.
func emojiKey(e discord.PartialEmoji) string {
	if e.ID != nil {
		return e.ID.String()
	}
	if e.Name != nil {
		return *e.Name
	}
	return ""
}

func normalizeGuildMembership(g discord.Guild, added bool) inboundMembership {
	mb := inboundMembership{GuildID: g.ID.String(), Title: g.Name, Added: added}
	if g.SystemChannelID != nil {
		mb.ChannelID = g.SystemChannelID.String()
	}
	return mb
}

func normalizeMessage(m discord.Message) inboundMessage {
	im := inboundMessage{
		MessageID:   m.ID.String(),
		TS:          m.ID.Time().UnixMilli(),
		ChannelID:   m.ChannelID.String(),
		AuthorID:    m.Author.ID.String(),
		AuthorName:  m.Author.Username,
		AuthorIsBot: m.Author.Bot || m.Author.System || m.WebhookID != nil,
		Content:     m.Content,
	}
	for _, u := range m.Mentions {
		im.Mentions = append(im.Mentions, u.ID.String())
	}
	if m.GuildID != nil {
		im.GuildID = m.GuildID.String()
	}
	if m.MessageReference != nil && m.MessageReference.MessageID != nil {
		im.ReplyToID = m.MessageReference.MessageID.String()
	}
	for _, a := range m.Attachments {
		ct := ""
		if a.ContentType != nil {
			ct = *a.ContentType
		}
		att := inboundAttachment{
			URL: a.URL, Filename: a.Filename, ContentType: ct, Size: int64(a.Size),
		}
		if a.DurationSecs != nil {
			att.DurationMS = int(*a.DurationSecs * 1000)
		}
		im.Attachments = append(im.Attachments, att)
	}
	return im
}

func (d *disgoAPI) me(ctx context.Context) (string, string, error) {
	if err := d.ensureClient(nil, nil, nil, nil); err != nil {
		return "", "", err
	}
	user, err := d.client.Rest.GetCurrentUser("", rest.WithCtx(ctx))
	if err != nil {
		return "", "", err
	}
	return user.ID.String(), user.Username, nil
}

func (d *disgoAPI) appID(ctx context.Context) (string, error) {
	if err := d.ensureClient(nil, nil, nil, nil); err != nil {
		return "", err
	}
	app, err := d.client.Rest.GetBotApplicationInfo(rest.WithCtx(ctx))
	if err != nil {
		return "", err
	}
	return app.ID.String(), nil
}

func (d *disgoAPI) open(ctx context.Context, onMessage func(inboundMessage), onInteraction func(inboundInteraction), onMembership func(inboundMembership), onChatEvent func(inboundChatEvent)) error {
	// The identity-time client (no listener) is rebuilt with one.
	if d.client != nil {
		d.client.Close(context.Background())
		d.client = nil
	}
	if err := d.ensureClient(onMessage, onInteraction, onMembership, onChatEvent); err != nil {
		return err
	}
	return d.client.OpenGateway(ctx)
}

func (d *disgoAPI) editMessage(ctx context.Context, channelID, messageID, text string) error {
	cid, err := snowflake.Parse(channelID)
	if err != nil {
		return fmt.Errorf("bad channel id %q: %w", channelID, err)
	}
	mid, err := snowflake.Parse(messageID)
	if err != nil {
		return fmt.Errorf("bad message id %q: %w", messageID, err)
	}
	_, err = d.client.Rest.UpdateMessage(cid, mid, discord.MessageUpdate{Content: &text}, rest.WithCtx(ctx))
	return err
}

func (d *disgoAPI) deleteMessage(ctx context.Context, channelID, messageID string) error {
	cid, err := snowflake.Parse(channelID)
	if err != nil {
		return fmt.Errorf("bad channel id %q: %w", channelID, err)
	}
	mid, err := snowflake.Parse(messageID)
	if err != nil {
		return fmt.Errorf("bad message id %q: %w", messageID, err)
	}
	return d.client.Rest.DeleteMessage(cid, mid, rest.WithCtx(ctx))
}

func (d *disgoAPI) react(ctx context.Context, channelID, messageID, emoji string, remove bool) error {
	cid, err := snowflake.Parse(channelID)
	if err != nil {
		return fmt.Errorf("bad channel id %q: %w", channelID, err)
	}
	mid, err := snowflake.Parse(messageID)
	if err != nil {
		return fmt.Errorf("bad message id %q: %w", messageID, err)
	}
	if remove {
		return d.client.Rest.RemoveOwnReaction(cid, mid, emoji, rest.WithCtx(ctx))
	}
	return d.client.Rest.AddReaction(cid, mid, emoji, rest.WithCtx(ctx))
}

func (d *disgoAPI) close() {
	if d.client != nil {
		d.client.Close(context.Background())
	}
}

func (d *disgoAPI) sendMessage(ctx context.Context, channelID, replyTo, text string) (string, error) {
	cid, err := snowflake.Parse(channelID)
	if err != nil {
		return "", fmt.Errorf("bad channel id %q: %w", channelID, err)
	}
	mc := discord.MessageCreate{Content: text}
	if replyTo != "" {
		if mid, err := snowflake.Parse(replyTo); err == nil {
			// FailIfNotExists false: a deleted reply target degrades to
			// a plain send instead of erroring.
			mc.MessageReference = &discord.MessageReference{MessageID: &mid}
		}
	}
	msg, err := d.client.Rest.CreateMessage(cid, mc, rest.WithCtx(ctx))
	if err != nil {
		return "", err
	}
	return msg.ID.String(), nil
}

func (d *disgoAPI) sendFile(ctx context.Context, channelID, path, name, caption string) error {
	cid, err := snowflake.Parse(channelID)
	if err != nil {
		return fmt.Errorf("bad channel id %q: %w", channelID, err)
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = d.client.Rest.CreateMessage(cid, discord.MessageCreate{
		Content: caption,
		Files:   []*discord.File{discord.NewFile(name, "", f)},
	}, rest.WithCtx(ctx))
	return err
}

func (d *disgoAPI) typing(ctx context.Context, channelID string) error {
	cid, err := snowflake.Parse(channelID)
	if err != nil {
		return fmt.Errorf("bad channel id %q: %w", channelID, err)
	}
	return d.client.Rest.SendTyping(cid, rest.WithCtx(ctx))
}

func (d *disgoAPI) sendAsk(ctx context.Context, channelID, replyTo, text, askID string, buttons []askButton) (string, error) {
	cid, err := snowflake.Parse(channelID)
	if err != nil {
		return "", fmt.Errorf("bad channel id %q: %w", channelID, err)
	}
	// An action row holds at most 5 buttons and a message at most 5
	// rows; chunk so any option set the host sends renders.
	var rows []discord.LayoutComponent
	var row []discord.InteractiveComponent
	for _, b := range buttons {
		customID := "ask:" + askID + ":" + b.Key
		switch b.Style {
		case "affirm":
			row = append(row, discord.NewSuccessButton(b.Label, customID))
		case "deny":
			row = append(row, discord.NewDangerButton(b.Label, customID))
		default:
			row = append(row, discord.NewSecondaryButton(b.Label, customID))
		}
		if len(row) == 5 {
			rows = append(rows, discord.NewActionRow(row...))
			row = nil
		}
	}
	if len(row) > 0 {
		rows = append(rows, discord.NewActionRow(row...))
	}
	mc := discord.MessageCreate{Content: text, Components: rows}
	if replyTo != "" {
		if mid, err := snowflake.Parse(replyTo); err == nil {
			mc.MessageReference = &discord.MessageReference{MessageID: &mid}
		}
	}
	msg, err := d.client.Rest.CreateMessage(cid, mc, rest.WithCtx(ctx))
	if err != nil {
		return "", err
	}
	return msg.ID.String(), nil
}

func (d *disgoAPI) updateAskMessage(ctx context.Context, channelID, messageID, content string) error {
	cid, err := snowflake.Parse(channelID)
	if err != nil {
		return fmt.Errorf("bad channel id %q: %w", channelID, err)
	}
	mid, err := snowflake.Parse(messageID)
	if err != nil {
		return fmt.Errorf("bad message id %q: %w", messageID, err)
	}
	empty := []discord.LayoutComponent{}
	_, err = d.client.Rest.UpdateMessage(cid, mid, discord.MessageUpdate{
		Content:    &content,
		Components: &empty,
	}, rest.WithCtx(ctx))
	return err
}

func (d *disgoAPI) ackComponent(ctx context.Context, interactionID, token string) error {
	iid, err := snowflake.Parse(interactionID)
	if err != nil {
		return fmt.Errorf("bad interaction id %q: %w", interactionID, err)
	}
	return d.client.Rest.CreateInteractionResponse(iid, token, discord.InteractionResponse{
		Type: discord.InteractionResponseTypeDeferredUpdateMessage,
	}, rest.WithCtx(ctx))
}

func (d *disgoAPI) sendAsWebhook(ctx context.Context, channelID, name, text string) (string, error) {
	cid, err := snowflake.Parse(channelID)
	if err != nil {
		return "", fmt.Errorf("bad channel id %q: %w", channelID, err)
	}
	wh, err := d.castWebhook(ctx, cid)
	if err != nil {
		return "", err
	}
	msg, err := d.client.Rest.CreateWebhookMessage(wh.id, wh.token, discord.WebhookMessageCreate{
		Content:  text,
		Username: name,
	}, rest.CreateWebhookMessageParams{Wait: true}, rest.WithCtx(ctx))
	if err != nil {
		return "", err
	}
	return msg.ID.String(), nil
}

// castWebhook returns the channel's managed cast webhook, finding an
// existing one by name before creating (restarts must not accumulate
// webhooks — channels cap at 15).
func (d *disgoAPI) castWebhook(ctx context.Context, channelID snowflake.ID) (cachedWebhook, error) {
	d.webhookMu.Lock()
	if d.webhooks == nil {
		d.webhooks = map[string]cachedWebhook{}
	}
	if wh, ok := d.webhooks[channelID.String()]; ok {
		d.webhookMu.Unlock()
		return wh, nil
	}
	d.webhookMu.Unlock()

	var found *cachedWebhook
	existing, err := d.client.Rest.GetWebhooks(channelID, rest.WithCtx(ctx))
	if err != nil {
		return cachedWebhook{}, fmt.Errorf("list webhooks: %w", err)
	}
	for _, w := range existing {
		if in, ok := w.(discord.IncomingWebhook); ok && in.Name() == castWebhookName {
			found = &cachedWebhook{id: in.ID(), token: in.Token}
			break
		}
	}
	if found == nil {
		created, err := d.client.Rest.CreateWebhook(channelID, discord.WebhookCreate{Name: castWebhookName}, rest.WithCtx(ctx))
		if err != nil {
			return cachedWebhook{}, fmt.Errorf("create webhook: %w", err)
		}
		found = &cachedWebhook{id: created.ID(), token: created.Token}
	}
	d.webhookMu.Lock()
	d.webhooks[channelID.String()] = *found
	d.webhookMu.Unlock()
	return *found, nil
}

func (d *disgoAPI) createThread(ctx context.Context, channelID, fromMessageID, name string) (string, error) {
	cid, err := snowflake.Parse(channelID)
	if err != nil {
		return "", fmt.Errorf("bad channel id %q: %w", channelID, err)
	}
	if fromMessageID != "" {
		mid, err := snowflake.Parse(fromMessageID)
		if err != nil {
			return "", fmt.Errorf("bad message id %q: %w", fromMessageID, err)
		}
		th, err := d.client.Rest.CreateThreadFromMessage(cid, mid, discord.ThreadCreateFromMessage{Name: name}, rest.WithCtx(ctx))
		if err != nil {
			return "", err
		}
		return th.ID().String(), nil
	}
	th, err := d.client.Rest.CreateThread(cid, discord.GuildPublicThreadCreate{Name: name}, rest.WithCtx(ctx))
	if err != nil {
		return "", err
	}
	return th.ID().String(), nil
}

func (d *disgoAPI) respondEphemeral(ctx context.Context, interactionID, token, text string) error {
	iid, err := snowflake.Parse(interactionID)
	if err != nil {
		return fmt.Errorf("bad interaction id %q: %w", interactionID, err)
	}
	return d.client.Rest.CreateInteractionResponse(iid, token, discord.InteractionResponse{
		Type: discord.InteractionResponseTypeCreateMessage,
		Data: discord.MessageCreate{Content: text, Flags: discord.MessageFlagEphemeral},
	}, rest.WithCtx(ctx))
}
