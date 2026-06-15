//go:build !terva_acp

// Package acp is the Agent Client Protocol run mode. The real
// implementation is gated behind -tags terva_acp (opt-in, same discipline
// as the telegram connector). Without the tag this file keeps the package
// non-empty and compilable so a no-tag `go build ./...` succeeds; nothing
// imports it in that build (the parent package's ACP dispatch is itself
// behind the tag, with a no-tag stub that returns "acp mode not built in").
package acp

// BuiltIn reports whether the ACP mode is compiled into this binary. False
// in the no-tag build; the tagged build has no such symbol because callers
// use the real Serve entrypoint instead.
const BuiltIn = false
