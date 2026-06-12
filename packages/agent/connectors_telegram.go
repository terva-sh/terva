//go:build !terva_no_telegram

package agent

// Telegram is the default-on connector: it ships unless the build
// explicitly opts out with -tags terva_no_telegram (`just build-min`).
import (
	_ "terva.sh/terva/packages/agent/modes/telegram"
)
