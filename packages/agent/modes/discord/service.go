package discord

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"terva.sh/terva/packages/agent/chat"
	"terva.sh/terva/packages/agent/chat/connlocal"
	"terva.sh/terva/packages/agent/connsdk"
)

// init registers discord in the chat-service registry. The
// connectors_discord.go shim (gated !terva_no_discord) is what links
// this package in; the minimum build carries neither this code nor the
// SDK.
func init() {
	chat.Register(chat.Service{
		Name:         "discord",
		Configured:   configured,
		NewConnector: newServiceConnector,
		Setup:        setup,
		StatusText:   statusText,
		Reset:        reset,
	})
}

func configured(tervaHome string) bool {
	cfg, err := LoadConfig(tervaHome)
	return err == nil && cfg.BotToken != ""
}

// newServiceConnector builds the compiled-in flavor: the shared
// Transport behind the in-process connproto carrier, so this run
// speaks the full wire (the dogfood contract from
// docs/plans/discord-connector.md).
func newServiceConnector(tervaHome string, warn func(string)) (chat.Connector, chat.Pairing, error) {
	cfg, err := LoadConfig(tervaHome)
	if err != nil {
		return nil, chat.Pairing{}, err
	}
	if cfg.BotToken == "" {
		return nil, chat.Pairing{}, fmt.Errorf("no bot token configured — run `terva bot setup --connector discord` first")
	}
	conn := connlocal.New("discord", connsdk.Config{
		Name:         "discord",
		Capabilities: Capabilities(),
		NewTransport: func(s connsdk.Session) (connsdk.Transport, error) {
			return NewTransport(cfg.BotToken, s.DataDir)
		},
	}, tervaHome, warn)
	pairing := chat.Pairing{
		AllowedUserID: cfg.AllowedUserID,
		Save: func(userID string) error {
			next, err := LoadConfig(tervaHome)
			if err != nil {
				return err
			}
			next.AllowedUserID = userID
			return SaveConfig(tervaHome, next)
		},
	}
	return conn, pairing, nil
}

// setup interactively reads a bot token, verifies it against the API,
// and saves it (`terva bot setup --connector discord`).
func setup(tervaHome string) error {
	cfg, err := LoadConfig(tervaHome)
	if err != nil {
		return err
	}

	fmt.Print("discord bot token (from the developer portal's Bot page): ")
	reader := bufio.NewReader(os.Stdin)
	line, err := reader.ReadString('\n')
	if err != nil {
		return err
	}
	token := strings.TrimSpace(line)
	if token == "" {
		return fmt.Errorf("no token provided")
	}

	a, err := newAPI(token)
	if err != nil {
		return err
	}
	id, username, err := a.me(context.Background())
	if err != nil {
		return fmt.Errorf("token rejected by discord: %w", err)
	}
	cfg.BotToken = token
	cfg.BotUsername = username
	cfg.BotID = id
	// Any stored pairing might be for a different bot; clear it.
	cfg.AllowedUserID = ""
	if err := SaveConfig(tervaHome, cfg); err != nil {
		return err
	}
	fmt.Printf("\nsaved: @%s (id=%s) to %s\n", username, id, ConfigPath(tervaHome))
	fmt.Println("\ninvite it to your server (ctrl/cmd+click in most terminals):")
	fmt.Printf("\n  %s\n\n", inviteURL(resolveInviteAppID(context.Background(), a, id)))
	fmt.Println("no privileged intents are needed: DMs and @mentions work out of the")
	fmt.Println("box; enable MESSAGE_CONTENT in the portal only if approved chats")
	fmt.Println("should hear un-mentioned group messages. the permission set covers")
	fmt.Println("everything terva can use (speakers, threads, reactions, files);")
	fmt.Println("trimming it is safe — features degrade gracefully.")
	fmt.Println("next: `terva bot run --connector discord` runs the bridge in the")
	fmt.Println("foreground (^C stops it cleanly); `terva bot start` detaches it as")
	fmt.Println("a daemon (`bot stop` / `bot status` / `bot logs` manage it).")
	fmt.Println("then DM or @mention your bot and send /start to pair.")
	return nil
}

// invitePermissions is the OAuth2 permission set covering everything
// the connector can use. Trimming is safe — each capability degrades
// gracefully (speakers fall back to a **Name:** prefix, threads to
// plain replies, reactions are lossy by doctrine). The bits:
//
//	1024          view channels
//	2048          send messages
//	65536         read message history
//	32768         attach files
//	64            add reactions
//	536870912     manage webhooks (speakers — the "terva cast")
//	34359738368   create public threads
//	274877906944  send messages in threads
const invitePermissions = "309774617664"

// inviteURL is the ready-to-click OAuth2 invite link for the app.
func inviteURL(appID string) string {
	return "https://discord.com/oauth2/authorize?client_id=" + appID + "&scope=bot&permissions=" + invitePermissions
}

// resolveInviteAppID picks the client_id for the invite URL: the
// application-info endpoint when reachable (the correct value even for
// ancient bots whose application id differs from their user id), else
// the bot's user id (equal to the application id for every bot created
// since the ids were unified).
func resolveInviteAppID(ctx context.Context, a api, botID string) string {
	if appID, err := a.appID(ctx); err == nil && appID != "" {
		return appID
	}
	return botID
}

// statusText renders the config block for `terva bot status` with the
// token masked.
func statusText(tervaHome string) (string, error) {
	cfg, err := LoadConfig(tervaHome)
	if err != nil {
		return "", err
	}
	if cfg.BotToken == "" {
		return "discord: not configured (run `terva bot setup --connector discord`)", nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "discord bot:  @%s (id=%s)\n", cfg.BotUsername, cfg.BotID)
	fmt.Fprintf(&b, "token:        %s\n", maskToken(cfg.BotToken))
	if cfg.AllowedUserID == "" {
		b.WriteString("paired with:  (unpaired — send /start to the bot to claim)\n")
	} else {
		fmt.Fprintf(&b, "paired with:  discord user id %s\n", cfg.AllowedUserID)
	}
	fmt.Fprintf(&b, "config file:  %s", ConfigPath(tervaHome))
	return b.String(), nil
}

// reset wipes the on-disk discord.json entry.
func reset(tervaHome string) (string, error) {
	p := ConfigPath(tervaHome)
	if _, err := os.Stat(p); errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err := os.Remove(p); err != nil {
		return "", err
	}
	return p, nil
}
