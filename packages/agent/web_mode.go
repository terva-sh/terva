//go:build terva_web

package agent

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"terva.sh/terva/packages/agent/build"
	"terva.sh/terva/packages/agent/config"
	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/agent/web"
	"terva.sh/terva/packages/agent/workspace"
	"terva.sh/terva/packages/buildinfo"
	"terva.sh/terva/packages/i18n"
	"terva.sh/terva/packages/provider/auth"
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
func runWebMode(ctx context.Context, args build.Args, version string) error {
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()
	// Settle the bearer token before anything reads it. args is a copy, so
	// writing it back here is what lets every downstream check — the
	// self-restart gate below, web.Options, checkBindSafety — see a token that
	// arrived by file or environment, not just one typed on the command line.
	// Miss this and an env-provisioned daemon silently refuses to self-restart
	// and refuses to bind, both on the grounds that it has no auth.
	tok, err := build.ResolveWebToken(args)
	if err != nil {
		return err
	}
	args.WebToken = tok
	// Workspace prep below (credential resolve, MCP server spawn + tool listing)
	// runs BEFORE the listener binds, so a refreshing browser sees
	// connection-refused until it finishes — announce it, and time it so a slow
	// start is attributable at a glance.
	fmt.Fprintf(os.Stderr, "terva web: starting v%s — the control panel will be available shortly\n", version)
	if prev := relaunch.PrevVersion(); prev != "" {
		fmt.Fprintf(os.Stderr, "terva web: self-restart complete — was v%s, now v%s\n", prev, version)
	}
	begin := time.Now()
	ws, err := workspace.NewWorkspace(args, version)
	if err != nil {
		return err
	}
	defer ws.Close()
	// The web daemon has no login flow, so a credential-less Workspace (fine
	// for the TUI, which opens /login) is a hard startup error here — checked
	// before the ready announcement, which must never precede a failure.
	if err := ws.CredentialErr(); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "terva web: workspace ready (took %s)\n", time.Since(begin).Round(10*time.Millisecond))
	cfg, _ := config.LoadConfig()
	fmt.Fprintf(os.Stderr, "terva web: approval mode %q (tool calls that need approval prompt in the browser)\n", build.ResolveApprovalMode(args, cfg))

	// Self-restart is opt-in and must never ride on an unauthenticated,
	// non-loopback listener open to ANY peer — the one place a stranger could
	// re-exec the daemon. The blanket --web-insecure with no auth is exactly that,
	// so refuse there. A scoped --web-insecure-cidr listener is bounded to a
	// trusted source range (the operator's overlay network), so restart is allowed
	// alongside it.
	allowRestart := args.AllowRestart
	unscopedInsecure := args.WebInsecure && len(args.WebInsecureCIDRs) == 0
	if allowRestart && unscopedInsecure && args.WebToken == "" && args.WebAuthHeader == "" {
		fmt.Fprintln(os.Stderr, "terva web: refusing self-restart on an insecure (no-auth) listener — add --web-token, --web-auth-header, or scope it with --web-insecure-cidr")
		allowRestart = false
	}
	// Don't advertise a restart control the platform can't honor (Windows lacks
	// exec(2)): the browser would show a button that can only ever error. Gate the
	// feature here so unsupported hosts render no restart control at all.
	if allowRestart && !relaunch.Supported() {
		fmt.Fprintln(os.Stderr, "terva web: self-restart is not supported on this platform — the restart control is hidden")
		allowRestart = false
	}
	if allowRestart {
		relaunch.Enable()
		// Tell every connected client just before the image is replaced; the PWA
		// auto-reconnects and restores from the on-disk snapshot.
		relaunch.OnPreExec(func(string) {
			// The toast names the outgoing build so the operator can spot the
			// version change once the client reconnects to the new one; the
			// bare semver (not the commit+date display string) keeps it short.
			msg := i18n.T("terva is restarting — reconnecting shortly…")
			if v := buildinfo.Get().Version; v != "" {
				msg = i18n.T("terva v%s is restarting — reconnecting shortly…", v)
			}
			ws.BroadcastAll(ctrlproto.NoticeEvent("info", "", msg))
		})
		// If the deferred exec fails, the process keeps serving on the old image
		// and the socket never drops — so the client is still connected to hear
		// that the restart it was promised did not happen.
		relaunch.OnFailure(func(err error) {
			ws.BroadcastAll(ctrlproto.NoticeEvent("error", "", i18n.T("restart failed — still running the current build: %s", err.Error())))
		})
		fmt.Fprintln(os.Stderr, "terva web: self-restart enabled (control.restart, the terva_restart tool, and SIGHUP / `systemctl reload`)")
	}

	// SIGHUP drives the SAME Tier-1 self-restart as the control plane and the
	// tool — so a systemd unit's `ExecReload=/bin/kill -HUP $MAINPID` picks up a
	// freshly-installed binary in place. Installed unconditionally (not only under
	// allowRestart): an unhandled SIGHUP would terminate the daemon, so when
	// restart is off the signal is swallowed with a log instead of killing it.
	installReloadHandler(ctx)

	// Provider login is opt-in, and refused on an unauthenticated listener for the
	// same reason self-restart is — one rung higher. Writing the credential terva
	// uses to reach a model provider is categorically more authority than driving
	// a conversation, and a stranger who could reach an open port must never be
	// able to revoke the operator's subscription or point the daemon at their own
	// endpoint. Loopback-only or a scoped CIDR is a bounded audience; blanket
	// --web-insecure with no auth is not.
	//
	// Note what this does NOT do: it does not relax the credential-less boot check
	// above. terva still will not start the web daemon without a credential. The
	// first login on a machine happens at the TUI, or is pre-seeded by whatever
	// provisioned the box; this is for the second provider, and for the
	// subscription that expired.
	// Stage is enabled by the --web-stage flag OR the web_stage config knob, so a
	// deployment can turn it on without a launch flag (config read once at start).
	allowStage := args.WebStage || cfg.WebStage

	allowLogin := args.AllowWebLogin
	if allowLogin && unscopedInsecure && args.WebToken == "" && args.WebAuthHeader == "" {
		fmt.Fprintln(os.Stderr, "terva web: refusing provider login on an insecure (no-auth) listener — add --web-token, --web-auth-header, or scope it with --web-insecure-cidr")
		allowLogin = false
	}
	if allowLogin {
		mgr := auth.NewManager(config.AuthStoreFor())
		defer mgr.Close()
		ws.EnableAuth(mgr, workspace.AuthOptions{
			// A daemon must not open a browser: the user is somewhere else.
			OpenBrowser: false,
			ApplyStart:  ApplyLoginStart,
			ApplyLogin: func(providerID string) {
				ApplyLoginSuccess(mgr.Store(), providerID, func(providerName, model, scope string) error {
					return ws.SetDefaultModel(ctx, providerName, model, ctrlproto.DefaultScope(scope))
				})
			},
			ApplyLogout: ApplyLogout,
		})
		fmt.Fprintln(os.Stderr, "terva web: provider login enabled (the Providers pane can add, repair, and revoke credentials)")
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
		CWD:            ws.CWD(),
		Jailed:         ws.Sandbox().Locked(),
		AllowRestart:   allowRestart,
		AllowLogin:     allowLogin,
		AllowStage:     allowStage,
	})
}
