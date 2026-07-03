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
	"path/filepath"
	"strings"

	"terva.sh/terva/packages/agent/connsdk"
	"terva.sh/terva/packages/agent/modes/discord"
)

const name = "discord-ext"

var version = "0.0.0" // -ldflags "-X main.version=..."

type extConfig struct {
	BotToken string `json:"bot_token,omitempty"`
}

func configPath() string {
	return filepath.Join(connsdk.StateDir(name), "config.json")
}

func loadToken() (string, error) {
	b, err := os.ReadFile(configPath())
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	var c extConfig
	if err := json.Unmarshal(b, &c); err != nil {
		return "", err
	}
	return c.BotToken, nil
}

func saveToken(token string) error {
	if err := os.MkdirAll(connsdk.StateDir(name), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(extConfig{BotToken: token}, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(configPath(), append(b, '\n'), 0o600)
}

func main() {
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
			fmt.Print("discord bot token (from the developer portal's Bot page): ")
			line, err := bufio.NewReader(os.Stdin).ReadString('\n')
			if err != nil {
				return err
			}
			token := strings.TrimSpace(line)
			if token == "" {
				return fmt.Errorf("no token provided")
			}
			if err := saveToken(token); err != nil {
				return err
			}
			fmt.Println("saved to", configPath())
			return nil
		},
		Status: func() (string, error) {
			token, err := loadToken()
			if err != nil {
				return "", err
			}
			if token == "" {
				return name + ": not configured (run setup)", nil
			}
			return name + ": token configured (" + configPath() + ")", nil
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
	})
}
