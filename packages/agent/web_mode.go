//go:build terva_web

package agent

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/agent/web"
	"terva.sh/terva/packages/i18n"
	"terva.sh/terva/packages/relaunch"
)

// runWebMode runs the browser control-panel server: one in-process Workspace
// (the ctrlproto.WorkspaceService) bound to a WebSocket carrier via web.Serve.
// It is the sibling of runACPMode — same Resolve→NewAgent seam, but the wire is
// ctrlproto over a WebSocket and the frontend is an embedded PWA.
//
// SIGINT/SIGTERM cancel the context so the server drains and ws.Close() tears
// down every session's agent and extension subprocesses — the graceful stop a
// systemd-managed daemon needs.
func runWebMode(ctx context.Context, args Args, version string) error {
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()
	ws, err := NewWorkspace(args, version)
	if err != nil {
		return err
	}
	defer ws.Close()
	// The web daemon has no login flow, so a credential-less Workspace (fine
	// for the TUI, which opens /login) is a hard startup error here.
	if err := ws.CredentialErr(); err != nil {
		return err
	}
	cfg, _ := LoadConfig()
	fmt.Fprintf(os.Stderr, "terva web: approval mode %q (tool calls that need approval prompt in the browser)\n", resolveApprovalMode(args, cfg))

	// Self-restart is opt-in and must never ride on an unauthenticated,
	// non-loopback listener open to ANY peer — the one place a stranger could
	// re-exec the daemon. The blanket --web-insecure with no auth is exactly that,
	// so refuse there. A scoped --web-insecure-cidr listener is bounded to a
	// trusted source range (the operator's overlay network), so restart is allowed
	// alongside it.
	allowRestart := args.WebAllowRestart
	unscopedInsecure := args.WebInsecure && len(args.WebInsecureCIDRs) == 0
	if allowRestart && unscopedInsecure && args.WebToken == "" && args.WebAuthHeader == "" {
		fmt.Fprintln(os.Stderr, "terva web: refusing self-restart on an insecure (no-auth) listener — add --web-token, --web-auth-header, or scope it with --web-insecure-cidr")
		allowRestart = false
	}
	if allowRestart {
		relaunch.Enable()
		// Tell every connected client just before the image is replaced; the PWA
		// auto-reconnects and restores from the on-disk snapshot.
		relaunch.OnPreExec(func(string) {
			ws.broadcastAll(ctrlproto.NoticeEvent("info", "", i18n.T("terva is restarting — reconnecting shortly…")))
		})
		fmt.Fprintln(os.Stderr, "terva web: self-restart enabled (control.restart + the terva_restart tool)")
	}

	trustedProxies, err := web.ParseTrustedProxies(args.WebTrustedProxies)
	if err != nil {
		return err
	}
	insecureCIDRs, err := web.ParseTrustedProxies(args.WebInsecureCIDRs)
	if err != nil {
		return fmt.Errorf("--web-insecure-cidr: %w", err)
	}

	return web.Serve(ctx, ws, web.Options{
		Addr:           args.WebAddr,
		AuthHeader:     args.WebAuthHeader,
		TrustedProxies: trustedProxies,
		Token:          args.WebToken,
		AllowInsecure:  args.WebInsecure,
		InsecureCIDRs:  insecureCIDRs,
		Version:        version,
		Locale:         i18n.ActiveLang(),
		AllowRestart:   allowRestart,
	})
}
