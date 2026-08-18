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
	"terva.sh/terva/packages/agent/permissions"
	"terva.sh/terva/packages/agent/web"
	"terva.sh/terva/packages/agent/workspace"
	"terva.sh/terva/packages/buildinfo"
	"terva.sh/terva/packages/i18n"
	"terva.sh/terva/packages/provider/auth"
	"terva.sh/terva/packages/relaunch"
)

// servePrivilegedGroup decides whether a categorically-higher method group may
// be served on this listener, and says so when it refuses.
//
// One predicate for every such group — the auth group and the secrets group
// today — so a third cannot quietly arrive with a laxer rule. The bar is the
// same one self-restart answers to: loopback-only or a scoped CIDR is a bounded
// audience; blanket --web-insecure with no auth is not, and a stranger who can
// reach an open port must not inherit the operator's authority over credentials.
func servePrivilegedGroup(want, noListenerAuth bool, what string) bool {
	if want && noListenerAuth {
		fmt.Fprintf(os.Stderr, "terva web: refusing %s on an insecure (no-auth) listener — add --web-token, --web-auth-header, or scope it with --web-insecure-cidr\n", what)
		return false
	}
	return want
}

func webCredentialBoot(credErr error, allowLogin bool) error {
	if credErr == nil || allowLogin {
		return nil
	}
	return fmt.Errorf("%w\nterva web: nothing here can sign in — start with --web-allow-login (which needs --web-token or --web-auth-header) to log in from the control panel", credErr)
}

// runWebMode runs the browser control-panel server: one in-process Workspace
// (the ctrlproto.WorkspaceService) bound to a WebSocket carrier via web.Serve.
// It is the sibling of runACPMode — same Resolve→NewAgent seam, but the wire is
// ctrlproto over a WebSocket and the frontend is an embedded PWA.
//
// SIGINT/SIGTERM cancel the context so the server drains and ws.Close() tears
// down every session's agent and extension subprocesses — the graceful stop a
// systemd-managed daemon needs.
// webCredentialBoot decides what starting with no credential means for this
// daemon, and returns the error to die on — or nil to carry on without one.
//
// The question is not "is there a credential" but "can anything reachable from
// here supply one". With provider login enabled the browser is a login flow: the
// Providers pane stores a credential and clears this very error, so sessions
// work from that moment without a restart. Refusing to boot would put the only
// remedy behind the daemon that refused to start — exactly the bind a machine
// with no terminal is in, told to log in at a TUI it does not have.
//
// Without login there is no route to a credential at all, so the failure stands.
// It names the flag rather than only the problem, because "start it differently"
// is the actionable half and the operator is already at a shell.
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

	// Whether this daemon can accept a provider login is settled here, before
	// anything else, because it answers two questions rather than one: it gates
	// the Providers pane, and it decides whether starting with no credential is
	// a recoverable state or a dead end (see the boot check below).
	//
	// Login is refused on an unauthenticated listener for the same reason
	// self-restart is, one rung higher. Writing the credential terva uses to
	// reach a model provider is categorically more authority than driving a
	// conversation, and a stranger who could reach an open port must never be
	// able to revoke the operator's subscription or point the daemon at their
	// own endpoint. Loopback-only or a scoped CIDR is a bounded audience;
	// blanket --web-insecure with no auth is not.
	unscopedInsecure := args.WebInsecure && len(args.WebInsecureCIDRs) == 0
	noListenerAuth := unscopedInsecure && args.WebToken == "" && args.WebAuthHeader == ""
	allowLogin := servePrivilegedGroup(args.AllowWebLogin, noListenerAuth, "provider login")
	// The secrets group answers to the same rule, for a related reason a rung
	// along: it returns no secret VALUE and cannot rotate a key — neither is on
	// the wire — but the report names every scope, component and grant on the
	// host. That is a map of what is worth stealing, and not something to hand to
	// whoever reaches an open port.
	allowSecrets := servePrivilegedGroup(args.AllowWebSecrets, noListenerAuth, "secret management")
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
	// A credential-less start is fatal only when nothing reachable can fix it.
	//
	// With provider login enabled the browser IS a login flow — the Providers
	// pane adds, repairs and revokes credentials, and a login there clears this
	// very error (applyCredential → RefreshDefaults), so sessions work from that
	// moment without a restart. Refusing to boot would put the only remedy
	// behind the daemon that refuses to start, which is precisely the bind a
	// machine with no terminal is in: it was told to log in at the TUI it does
	// not have. So boot, say plainly that nothing is configured, and let the
	// operator sign in.
	//
	// Without login the daemon has no way to acquire a credential at all, so the
	// failure stands — and names the flag that would have made it recoverable,
	// because "start it differently" is the actionable half.
	//
	// Checked before the ready announcement either way: that line must never
	// precede a failure.
	if err := webCredentialBoot(ws.CredentialErr(), allowLogin); err != nil {
		return err
	}
	if err := ws.CredentialErr(); err != nil {
		fmt.Fprintf(os.Stderr, "terva web: %v\n", err)
		fmt.Fprintln(os.Stderr, "terva web: starting anyway — sign in from the control panel's Providers pane; sessions can start once a credential lands")
	}
	fmt.Fprintf(os.Stderr, "terva web: workspace ready (took %s)\n", time.Since(begin).Round(10*time.Millisecond))
	cfg, _ := config.LoadConfig()
	fmt.Fprintf(os.Stderr, "terva web: approval mode %q (tool calls that need approval prompt in the browser)\n", permissions.ResolveApprovalMode(args.PermInputs(), cfg))

	// Self-restart is opt-in and must never ride on an unauthenticated,
	// non-loopback listener open to ANY peer — the one place a stranger could
	// re-exec the daemon. The blanket --web-insecure with no auth is exactly that,
	// so refuse there. A scoped --web-insecure-cidr listener is bounded to a
	// trusted source range (the operator's overlay network), so restart is allowed
	// alongside it.
	allowRestart := args.AllowRestart
	if allowRestart && noListenerAuth {
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
			from := buildinfo.Get().Version
			if from != "" {
				msg = i18n.T("terva v%s is restarting — reconnecting shortly…", from)
			}
			ws.BroadcastAll(ctrlproto.KindedNoticeEvent("info", ctrlproto.NoticeRestart, msg, map[string]string{
				"phase":        "restarting",
				"from_version": from,
			}))
		})
		// If the deferred exec fails, the process keeps serving on the old image
		// and the socket never drops — so the client is still connected to hear
		// that the restart it was promised did not happen.
		relaunch.OnFailure(func(err error) {
			ws.BroadcastAll(ctrlproto.KindedNoticeEvent("error", ctrlproto.NoticeRestart,
				i18n.T("restart failed — still running the current build: %s", err.Error()),
				map[string]string{"phase": "failed"}))
		})
		fmt.Fprintln(os.Stderr, "terva web: self-restart enabled (control.restart, the terva_restart tool, and SIGHUP / `systemctl reload`)")
	}

	// SIGHUP drives the SAME Tier-1 self-restart as the control plane and the
	// tool — so a systemd unit's `ExecReload=/bin/kill -HUP $MAINPID` picks up a
	// freshly-installed binary in place. Installed unconditionally (not only under
	// allowRestart): an unhandled SIGHUP would terminate the daemon, so when
	// restart is off the signal is swallowed with a log instead of killing it.
	installReloadHandler(ctx)

	// Stage is enabled by the --web-stage flag OR the web_stage config knob, so a
	// deployment can turn it on without a launch flag (config read once at start).
	allowStage := args.WebStage || cfg.WebStage

	// allowLogin was settled at the top, because the credential-less boot check
	// depends on it — this is where the machinery it gates gets wired.
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

	if allowSecrets {
		ws.EnableSecrets()
		fmt.Fprintln(os.Stderr, "terva web: secret management enabled (posture and grants; no value is served and rotation stays on the CLI)")
	}

	trustedProxies, err := web.ParseTrustedProxies(args.WebTrustedProxies)
	if err != nil {
		return err
	}
	insecureCIDRs, err := web.ParseTrustedProxies(args.WebInsecureCIDRs)
	if err != nil {
		return fmt.Errorf("--web-insecure-cidr: %w", err)
	}

	// Publish where this daemon can be reached, so a client on this machine can
	// find it without being told — `terva attach` with no argument, and
	// `terva ext config`, which must go THROUGH the running instance because
	// that instance owns config.json. The record is scoped to $TERVA_HOME, so it
	// can only ever name a daemon serving the same home the reader resolved.
	stopRecord := func() {}
	defer func() { stopRecord() }()
	onListen := func(network, addr string) {
		stop, err := config.PublishListenRecord(config.ListenRecord{
			Endpoint: config.EndpointForListener(network, addr),
			Version:  version,
			Auth:     args.WebToken != "" || args.WebAuthHeader != "",
		})
		if err != nil {
			// Discovery is a convenience; failing to publish must not take the
			// daemon down. Say so once — a client that then cannot find it
			// should have a reason to point at.
			fmt.Fprintf(os.Stderr, "terva web: could not publish %s (clients will need an explicit endpoint): %v\n", config.ListenRecordPath(), err)
			return
		}
		stopRecord = stop
	}

	return web.Serve(ctx, ws, web.Options{
		Addr:           args.WebAddr,
		OnListen:       onListen,
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
		AllowSecrets:   allowSecrets,
		AllowStage:     allowStage,
	})
}
