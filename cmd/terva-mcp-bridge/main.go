// Command terva-mcp-bridge reaches a remote OAuth-gated MCP server on terva's
// behalf. To terva it is an ordinary stdio MCP server — spawned as a plain
// `command` in the MCP config — so core needs no changes and takes no dependency
// on this binary. Upstream it is a Streamable-HTTP MCP client that performs the
// OAuth 2.1 authorization-code+PKCE flow (discovery, dynamic client registration,
// a localhost redirect listener, token refresh). That service-shaped weight — a
// listening socket, a browser launch, a token vault — is exactly what stays OUT of
// the small static core; static-auth remote servers are reached in-core instead
// (see docs/plans/mcp-http-transport.md).
//
// It is NOT a chat connector: its wire to terva is plain MCP stdio, not connsdk.
// It shares only the "ship a companion binary" packaging precedent.
//
// Usage:
//
//	terva-mcp-bridge login <url>     one-time interactive OAuth (opens a browser)
//	terva-mcp-bridge <url>           relay mode: what terva spawns; reuses stored tokens
//
// Flags:
//
//	--bearer-env VAR    static auth: send $VAR as a bearer token, skip OAuth entirely
//	--header 'K: V'     add a fixed header to every upstream request (repeatable)
//	--client-id ID      use a pre-provisioned OAuth client id (skip dynamic registration)
//	--token-dir DIR     token store location (default: $TERVA_HOME/mcp-bridge)
//
// Tokens live under $TERVA_HOME/mcp-bridge/<host>/tokens.json at 0600.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
)

const protocolVersion = "2025-06-18"

var version = "0.0.0" // -ldflags "-X main.version=..."

type options struct {
	mode      string // "relay" or "login"
	remoteURL string
	bearerEnv string
	clientID  string
	tokenDir  string
	headers   map[string]string
}

func main() {
	opts, err := parseArgs(os.Args[1:])
	if err != nil {
		fmt.Fprintln(os.Stderr, "terva-mcp-bridge:", err)
		fmt.Fprintln(os.Stderr, "usage: terva-mcp-bridge [login] <remote-url> [--bearer-env VAR] [--header 'K: V'] [--client-id ID] [--token-dir DIR]")
		os.Exit(2)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	switch opts.mode {
	case "login":
		if err := runLogin(ctx, opts); err != nil {
			fmt.Fprintln(os.Stderr, "terva-mcp-bridge:", err)
			os.Exit(1)
		}
	default:
		if err := runRelay(ctx, opts); err != nil {
			fmt.Fprintln(os.Stderr, "terva-mcp-bridge:", err)
			os.Exit(1)
		}
	}
}

func parseArgs(argv []string) (options, error) {
	opts := options{mode: "relay", headers: map[string]string{}}
	var positionals []string
	for i := 0; i < len(argv); i++ {
		a := argv[i]
		switch {
		case a == "login":
			opts.mode = "login"
		case a == "--version" || a == "-v":
			fmt.Println("terva-mcp-bridge", version)
			os.Exit(0)
		case a == "--bearer-env":
			v, err := next(argv, &i, a)
			if err != nil {
				return opts, err
			}
			opts.bearerEnv = v
		case a == "--client-id":
			v, err := next(argv, &i, a)
			if err != nil {
				return opts, err
			}
			opts.clientID = v
		case a == "--token-dir":
			v, err := next(argv, &i, a)
			if err != nil {
				return opts, err
			}
			opts.tokenDir = v
		case a == "--header":
			v, err := next(argv, &i, a)
			if err != nil {
				return opts, err
			}
			k, val, ok := strings.Cut(v, ":")
			if !ok {
				return opts, fmt.Errorf("--header wants 'Key: Value', got %q", v)
			}
			opts.headers[strings.TrimSpace(k)] = strings.TrimSpace(val)
		case strings.HasPrefix(a, "-"):
			return opts, fmt.Errorf("unknown flag %q", a)
		default:
			positionals = append(positionals, a)
		}
	}
	if len(positionals) != 1 {
		return opts, fmt.Errorf("expected exactly one remote MCP url, got %d", len(positionals))
	}
	opts.remoteURL = positionals[0]
	if !strings.HasPrefix(opts.remoteURL, "http://") && !strings.HasPrefix(opts.remoteURL, "https://") {
		return opts, fmt.Errorf("remote url must be http(s): %q", opts.remoteURL)
	}
	return opts, nil
}

func next(argv []string, i *int, flag string) (string, error) {
	if *i+1 >= len(argv) {
		return "", fmt.Errorf("%s wants a value", flag)
	}
	*i++
	return argv[*i], nil
}

// runRelay is what terva spawns: build the credential (static or stored OAuth),
// connect + initialize upstream, then serve terva over stdio.
func runRelay(ctx context.Context, opts options) error {
	hc := &http.Client{Timeout: 60 * time.Second}

	auth, err := relayAuth(opts, hc)
	if err != nil {
		return err
	}

	up := newUpstream(opts.remoteURL, hc, auth, opts.headers)
	initCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	initRes, err := up.initialize(initCtx)
	cancel()
	if err != nil {
		return fmt.Errorf("connecting to %s: %w", opts.remoteURL, err)
	}
	fmt.Fprintf(os.Stderr, "terva-mcp-bridge: connected to %s\n", opts.remoteURL)

	srv := newStdioServer(up, initRes, os.Stdin, os.Stdout)
	return srv.serve(ctx)
}

// relayAuth builds the headless credential for relay mode: a static bearer from
// --bearer-env, or stored OAuth tokens. It never runs the interactive flow — a
// relay with no usable credential fails with a pointer to `login`.
func relayAuth(opts options, hc *http.Client) (authSource, error) {
	if opts.bearerEnv != "" {
		tok := os.Getenv(opts.bearerEnv)
		if tok == "" {
			return nil, fmt.Errorf("--bearer-env %q is not set", opts.bearerEnv)
		}
		return staticAuth{value: "Bearer " + tok}, nil
	}
	store, err := newTokenStore(opts.tokenDir, opts.remoteURL)
	if err != nil {
		return nil, err
	}
	toks, ok, err := store.load()
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, fmt.Errorf("no stored credentials for %s — run `terva-mcp-bridge login %s` first", opts.remoteURL, opts.remoteURL)
	}
	return newOAuthSource(store, hc, toks), nil
}

// runLogin is the one-time interactive step: probe the server for its auth
// requirement, discover its authorization server, run the browser flow, and
// persist the tokens a later relay will refresh from.
func runLogin(ctx context.Context, opts options) error {
	if opts.bearerEnv != "" {
		fmt.Fprintln(os.Stderr, "terva-mcp-bridge: --bearer-env uses a static token; no login needed.")
		return nil
	}
	hc := &http.Client{Timeout: 60 * time.Second}

	wwwAuth, needsAuth, err := probeAuth(ctx, hc, opts.remoteURL)
	if err != nil {
		return fmt.Errorf("probing %s: %w", opts.remoteURL, err)
	}
	if !needsAuth {
		fmt.Fprintf(os.Stderr, "terva-mcp-bridge: %s does not require OAuth; add it directly (in-core http) or with --bearer-env.\n", opts.remoteURL)
		return nil
	}

	oc, err := discover(ctx, hc, opts.remoteURL, wwwAuth)
	if err != nil {
		return err
	}
	oc.ClientID = opts.clientID // "" means dynamic registration

	store, err := newTokenStore(opts.tokenDir, opts.remoteURL)
	if err != nil {
		return err
	}
	toks, err := authorizeInteractive(ctx, hc, oc, func(s string) { fmt.Fprintln(os.Stderr, s) })
	if err != nil {
		return err
	}
	if err := store.save(toks); err != nil {
		return fmt.Errorf("saving tokens: %w", err)
	}
	fmt.Fprintf(os.Stderr, "terva-mcp-bridge: authorized. Tokens stored at %s\n", store.path)
	return nil
}

// probeAuth sends an unauthenticated initialize to the remote to learn whether it
// requires OAuth. A 401 returns its WWW-Authenticate (the discovery pointer); a
// 2xx means no auth is needed.
func probeAuth(ctx context.Context, hc *http.Client, remoteURL string) (wwwAuth string, needsAuth bool, err error) {
	up := newUpstream(remoteURL, hc, noAuth{}, nil)
	params, _ := json.Marshal(map[string]any{
		"protocolVersion": protocolVersion,
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "terva-mcp-bridge", "version": version},
	})
	_, status, www, derr := up.do(ctx, marshalRequest(json.RawMessage(`1`), "initialize", params))
	if status == http.StatusUnauthorized {
		return www, true, nil
	}
	if derr != nil {
		return "", false, derr
	}
	return "", false, nil
}
