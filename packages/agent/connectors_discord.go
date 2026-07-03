//go:build !terva_no_discord

package agent

// Discord is a default-on connector: it ships unless the build
// explicitly opts out with -tags terva_no_discord (`just build-min`
// passes it together with terva_no_telegram). Unlike telegram's
// built-in, the discord service speaks the connector protocol even
// when compiled in (chat/connlocal) — the connproto-v2 dogfood
// (docs/plans/discord-connector.md).
import (
	_ "terva.sh/terva/packages/agent/modes/discord"
)
