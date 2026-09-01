package agent

// Chat connectors compiled into this binary live in the per-connector
// connectors_*.go files beside this one, each gated by a build tag:
//
//	connectors_telegram.go  !terva_no_telegram  (default ON)
//	connectors_discord.go   terva_discord       (opt-in, future)
//	connectors_matrix.go    terva_matrix        (opt-in, future)
//
// Each import's init() registers the service in the chat registry
// (chat.Register); the CLI (`terva bot`) and the TUI (/connect)
// discover connectors there and degrade gracefully when none are
// compiled in. A connector's registration file is its ONLY link into
// the binary, so excluding the tag drops the whole transport package
// from the build (see docs/plans/archive/chat-connectors.md, phase 3).
