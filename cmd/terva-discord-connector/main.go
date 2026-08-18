// Command terva-discord-connector is the out-of-process flavor of
// terva's built-in discord connector. The compiled-in flavor already
// speaks the connector protocol (chat/connlocal, in-process); this
// binary is the SAME transport behind child-process stdio instead —
// the packaging-portability proof from docs/plans/discord-connector.md,
// and the twin of cmd/terva-telegram-connector.
//
// It registers as "discord-ext" so it never collides with the
// compiled-in "discord" service, and keeps its own token in
// $TERVA_HOME/connectors/discord-ext/config.json — setup here never
// fights the built-in connector's $TERVA_HOME/discord.json.
//
// Install: `go install ./cmd/terva-discord-connector`, write a
// manifest {"name":"discord-ext","exec":"terva-discord-connector"}
// somewhere, then `terva bot link <path>/connector.json` (or pass
// --connector-manifest for a single run). See docs/connectors.md.
package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	"terva.sh/terva/packages/agent/connsdk"
	"terva.sh/terva/packages/agent/modes/discord"
	"terva.sh/terva/packages/envcompat"
	"terva.sh/terva/packages/i18n"
)

const name = "discord-ext"

var version = "0.0.0" // -ldflags "-X main.version=..."

type extConfig struct {
	BotToken string `json:"bot_token,omitempty"`
}

// state seals the bot token in place, to this connector's own key AND terva's,
// so the host can rotate or audit it while this process is not running — and so
// a model reading connectors/discord-ext/config.json finds ciphertext. The
// declared path is what lets the file be called clean rather than unscanned.
var state = connsdk.SealedState{Name: name, Paths: []string{"/bot_token"}}

func configPath() string { return state.Path() }

func loadToken() (string, error) {
	doc, err := state.Load()
	if err != nil {
		return "", err
	}
	var c extConfig
	if err := json.Unmarshal(doc, &c); err != nil {
		return "", err
	}
	return c.BotToken, nil
}

func saveToken(token string) error {
	// Owner-only: this directory exists to hold a bot token. The file is
	// 0600, but a 0755 directory around it leaves the whole state dir
	// listable, and the token's own mode is then the only thing standing
	// between it and every other local user. SealedState keeps that and adds
	// the seal.
	b, err := json.Marshal(extConfig{BotToken: token})
	if err != nil {
		return err
	}
	return state.Save(b)
}

func main() {
	// A connector is its own process, so it configures i18n itself from
	// the environment (TERVA_LANG + the operator's $TERVA_HOME/locales
	// overlay) — the host can't inject it across the process boundary.
	_ = i18n.Configure(envcompat.Get("LANG"), envcompat.Home())
	connsdk.Main(connsdk.Config{
		Name:         name,
		Version:      version,
		Capabilities: discord.Capabilities(),
		NewTransport: func(s connsdk.Session) (connsdk.Transport, error) {
			token, err := loadToken()
			if err != nil {
				return nil, err
			}
			return discord.NewTransport(token, s.DataDir)
		},
		Setup: func() error {
			fmt.Print(i18n.T("discord bot token (from the developer portal's Bot page): "))
			line, err := bufio.NewReader(os.Stdin).ReadString('\n')
			if err != nil {
				return err
			}
			token := strings.TrimSpace(line)
			if token == "" {
				return i18n.Errorf("no token provided")
			}
			if err := saveToken(token); err != nil {
				return err
			}
			fmt.Println(i18n.T("saved to %s", configPath()))
			return nil
		},
		Status: func() (string, error) {
			token, err := loadToken()
			if err != nil {
				return "", err
			}
			if token == "" {
				return i18n.T("%s: not configured (run setup)", name), nil
			}
			return i18n.T("%s: token configured (%s)", name, configPath()), nil
		},
		Reset: func() error {
			err := os.Remove(configPath())
			if errors.Is(err, os.ErrNotExist) {
				return nil
			}
			return err
		},
		Configured: func() bool {
			token, err := loadToken()
			return err == nil && token != ""
		},
		// Declaring the SAME SealedState we load and save with is what makes the
		// seal maintainable. Sealing without declaring leaves terva holding
		// ciphertext it can never re-seal: `terva secret rotate --revoke` skips
		// the file, and its own advice ("start it once and re-run") is
		// unfollowable, because starting a connector that never declares changes
		// nothing. Enforced by TestAConnectorThatSealsAlsoDeclares.
		Secrets: &state,
	})
}
