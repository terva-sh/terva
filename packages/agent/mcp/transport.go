package mcp

import (
	"context"
	"errors"
	"fmt"
	"io"
)

// transport is a JSON-RPC frame pipe between one Client and one MCP server.
//
// Implementations own connection mechanics — a stdio subprocess today, a
// Streamable-HTTP session next — and nothing else. The Client owns id
// correlation, initialize, tools/list, and tools/call, all transport-agnostic:
// every JSON-RPC message carries its own id, so the transport only has to funnel
// EVERY inbound frame into one channel and the Client matches it to the waiting
// caller. That is the whole reason a single Client body serves both wires.
type transport interface {
	// Send delivers one JSON-RPC message to the server. frame is the marshalled
	// message with NO wire framing; the transport adds whatever its wire needs —
	// a trailing newline for stdio, an HTTP POST body for http. ctx bounds the
	// send (ignored by stdio, honoured by http).
	Send(ctx context.Context, frame []byte) error

	// Incoming delivers every inbound JSON-RPC message as one raw frame. It is
	// CLOSED when the transport disconnects (the subprocess exited, the HTTP
	// session ended), which the Client reads as the server going away — it fails
	// all pending calls and signals Done.
	Incoming() <-chan []byte

	// Close shuts the transport down (stdin close + reap, or HTTP session
	// teardown). Idempotent.
	Close()
}

// transportFactory builds a non-stdio transport from a config. Kinds register
// one from their own build-tagged file's init(), so a lean build that drops the
// tag simply has no factory for that kind.
type transportFactory func(cfg ServerConfig, cwd string, stderr io.Writer) (transport, error)

var transportFactories = map[string]transportFactory{}

// registerTransport records a factory for a transport kind. Called from a
// build-tagged file's init() (e.g. mcp_http.go registers "http").
func registerTransport(kind string, f transportFactory) { transportFactories[kind] = f }

// transportKind is the configured wire, defaulting to stdio.
func (c ServerConfig) transportKind() string {
	if c.Transport == "" {
		return "stdio"
	}
	return c.Transport
}

// validate rejects a malformed server config before anything is spawned or
// dialed: a stdio server needs a command and no url; an http server needs a url
// and no command. A config that mixes the two is ambiguous and refused.
func (c ServerConfig) validate() error {
	switch c.transportKind() {
	case "stdio":
		if c.Command == "" {
			return errors.New("mcp: a stdio server needs a command")
		}
		if c.URL != "" {
			return errors.New("mcp: a stdio server must not set url")
		}
	case "http":
		if c.URL == "" {
			return errors.New("mcp: an http server needs a url")
		}
		if c.Command != "" {
			return errors.New("mcp: an http server must not set command")
		}
	}
	return nil
}

// newTransport builds the transport for one server config. stdio is the default
// and always compiled in; other kinds dispatch to a registered factory, so a
// lean build that drops the tag makes an unknown-kind config fail cleanly ("not
// compiled in") rather than silently falling back to a wrong wire.
func newTransport(cfg ServerConfig, cwd string, stderr io.Writer) (transport, error) {
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	kind := cfg.transportKind()
	if kind == "stdio" {
		return newStdioTransport(cfg, cwd, stderr)
	}
	f, ok := transportFactories[kind]
	if !ok {
		return nil, fmt.Errorf("mcp: transport %q not compiled in", kind)
	}
	return f(cfg, cwd, stderr)
}
