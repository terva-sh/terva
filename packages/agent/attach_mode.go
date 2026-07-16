package agent

// runAttachMode: the interactive TUI as a CLIENT of a running daemon (stage 2
// of docs/proposals/archive/tui-attach.md). The same Interactive loop as the default
// entry, but the carrier is a reconnecting ctrlproto WebSocket client
// (ctrlclient) instead of an in-process Workspace — sessions, credentials,
// extensions, and tools all live daemon-side; this process renders and
// controls. Local-only affordances (session file fork/tree/export, extension
// pickers, /login, the sandbox) are left unwired and degrade with their
// existing "unavailable" messages; the wire-backed set (prompting, streaming,
// approvals, /context, /settings, /model, /sessions, /new, /trust, /restart)
// works as-is. v1 assumes attaching from the daemon's host — the @-file
// picker, git probe, and favorites read local disk, pointed at the daemon's
// advertised cwd (Hello.CWD), so they are truthful from any directory on
// that host and degrade to absent cross-host.

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"terva.sh/terva/packages/agent/build"
	"terva.sh/terva/packages/agent/config"
	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/agent/ctrlproto/ctrlclient"
	"terva.sh/terva/packages/agent/modes"
	"terva.sh/terva/packages/agent/modes/widgets"
	"terva.sh/terva/packages/agent/workspace"
	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/fswalk"
	"terva.sh/terva/packages/i18n"
	"terva.sh/terva/packages/provider/auth"
	"terva.sh/terva/packages/relaunch"
	"terva.sh/terva/packages/tui"
)

// attachCarrier is the networked modes.Carrier: the ctrlclient service view
// plus the reconnect-aware SubscribeReliable/Reconnecting pair the pump keys
// on (see carrier.go's networked-carrier contract).
type attachCarrier struct {
	*ctrlclient.Service
	client *ctrlclient.Client
}

var (
	_ modes.Carrier             = attachCarrier{}
	_ modes.ReconnectingCarrier = attachCarrier{}
)

// SubscribeReliable is lossless while the connection holds; the channel
// closes on a transport drop (or consumer overflow), which is the pump's
// signal to re-subscribe and resync from the snapshot.
func (a attachCarrier) SubscribeReliable(ctx context.Context, sess string) (<-chan ctrlproto.Event, error) {
	return a.Subscribe(ctx, sess)
}

// Reconnecting reports the transport being between connections — the pump
// backs off and retries instead of treating a subscribe failure as fatal.
func (a attachCarrier) Reconnecting() bool { return !a.client.Connected() }

// attachEndpoint is a normalized attach target: a WebSocket URL, or — when
// UnixPath is set — the same protocol over a unix domain socket (URL then
// holds the placeholder the HTTP upgrade uses, and names the target in
// human-facing messages).
type attachEndpoint struct {
	URL      string
	UnixPath string
}

// String names the endpoint for errors and the boot notice.
func (e attachEndpoint) String() string {
	if e.UnixPath != "" {
		return "unix:" + e.UnixPath
	}
	return e.URL
}

// normalizeAttachURL resolves the CLI's endpoint argument to a dialable
// target: empty means the local `terva web` default; a bare host:port gets
// the ws scheme and /ws path; http(s) schemes map to ws(s); `unix:/path`
// (or unix:///path) targets a daemon serving a filesystem socket.
func normalizeAttachURL(raw string) (attachEndpoint, error) {
	if rest, ok := strings.CutPrefix(raw, "unix:"); ok {
		path := strings.TrimPrefix(rest, "//")
		if path == "" {
			return attachEndpoint{}, fmt.Errorf("attach: %q names no socket path (want unix:/path/to.sock)", raw)
		}
		return attachEndpoint{URL: "ws://unix/ws", UnixPath: path}, nil
	}
	if raw == "" {
		// terva web's default listener (web.DefaultAddr; spelled out here
		// because the web package is an opt-in build).
		raw = "ws://127.0.0.1:8730/ws"
	}
	if !strings.Contains(raw, "://") {
		raw = "ws://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil {
		return attachEndpoint{}, fmt.Errorf("attach: bad URL %q: %w", raw, err)
	}
	switch u.Scheme {
	case "ws", "wss":
	case "http":
		u.Scheme = "ws"
	case "https":
		u.Scheme = "wss"
	default:
		return attachEndpoint{}, fmt.Errorf("attach: unsupported scheme %q (want ws://, wss://, or unix:)", u.Scheme)
	}
	if u.Path == "" || u.Path == "/" {
		u.Path = "/ws"
	}
	return attachEndpoint{URL: u.String()}, nil
}

// attachConnectTimeout bounds the pre-TUI wait for the first handshake, so a
// wrong URL fails with a clear error instead of a silent retry loop.
const attachConnectTimeout = 8 * time.Second

func runAttachMode(ctx context.Context, args build.Args, version string) error {
	endpoint, err := normalizeAttachURL(args.AttachURL)
	if err != nil {
		return err
	}

	// Self-restart (--allow-restart): the attach CLIENT re-execs itself into the
	// freshly-installed binary and reconnects — the client-side twin of the
	// daemon's own reload (which /restart still drives over the wire; the two are
	// independent). Platform-gated like every mode; there is no listener here, so
	// no insecure-listener refusal. When enabled, modes.Interactive.Run registers
	// the terminal-restore + session-handoff pre-exec hook, so the exec is clean;
	// the SIGHUP trigger is installed just before Run below.
	allowRestart := args.AllowRestart
	if allowRestart && !relaunch.Supported() {
		fmt.Fprintln(os.Stderr, "terva: self-restart is not supported on this platform — --allow-restart has no effect")
		allowRestart = false
	}
	if allowRestart {
		relaunch.Enable()
	}

	// The TUI is constructed only after the first handshake; these bridge the
	// client's callbacks (which start firing immediately) to it. lastVer
	// tracks the daemon build across reconnects so a hop is announced — the
	// attached twin of the web panel's restart toast.
	var (
		bridgeMu sync.Mutex
		iv       *modes.Interactive
		lastVer  string
		dialErr  error
	)
	notify := func(level, msg string) {
		bridgeMu.Lock()
		target := iv
		bridgeMu.Unlock()
		if target != nil {
			target.Notify("attach", level, msg)
		}
	}

	// The transport matches the endpoint form: same WebSocket protocol, over
	// TCP or a unix domain socket (no token needed there — the socket file's
	// permissions gate access; one is still sent if given).
	//
	// TERVA_WEB_TOKEN stands in for --token: the flag is as readable from `ps`
	// as the daemon's is, so attaching to an authenticated daemon should not
	// force the secret onto the command line of every client. --web-token-file
	// is the leak-free route a persistent attach wants (systemd LoadCredential=);
	// a bad token file is fatal here rather than a token-less dial the daemon
	// would reject with a confusing handshake error.
	token, err := build.ResolveAttachToken(args)
	if err != nil {
		return err
	}
	dial := ctrlclient.DialWebSocket(endpoint.URL, token)
	if endpoint.UnixPath != "" {
		dial = ctrlclient.DialWebSocketUnix(endpoint.UnixPath, token)
	}
	client, err := ctrlclient.New(ctrlclient.Options{
		Dial: func(ctx context.Context) (ctrlproto.FrameConn, error) {
			conn, derr := dial(ctx)
			if derr != nil {
				bridgeMu.Lock()
				dialErr = derr
				bridgeMu.Unlock()
			}
			return conn, derr
		},
		Hello: ctrlproto.Hello{
			Agent:   "terva-tui",
			Version: version,
			// The full feature set the in-process TUI enjoys: attachments,
			// inline image payloads, dialog-dismiss resolves, context tree,
			// daemon-side @-file listing, on-demand session titling, and the
			// workspace address (so workspace-scoped events arrive once, on
			// #workspace, rather than stamped onto whatever sessions happen to
			// be subscribed).
			Features: []string{
				ctrlproto.FeatureImages, ctrlproto.FeatureImageData,
				ctrlproto.FeatureResolveEvents, ctrlproto.FeatureContextTree,
				ctrlproto.FeatureFilesList, ctrlproto.FeatureGenerateTitle,
				ctrlproto.FeatureWorkspaceEvents,
			},
		},
		OnConnect: func(h ctrlproto.Hello) {
			bridgeMu.Lock()
			prev := lastVer
			lastVer = h.Version
			target := iv
			bridgeMu.Unlock()
			if prev != "" && h.Version != "" && prev != h.Version {
				notify("info", i18n.T("daemon restarted: v%s → v%s", prev, h.Version))
			}
			// Every hello re-announces the sandbox lock, so a daemon
			// restarted with a different jail flag re-seeds the badge.
			if target != nil {
				target.SetCarrierJailed(h.Jailed)
			}
		},
	})
	if err != nil {
		return err
	}
	runCtx, cancelRun := context.WithCancel(ctx)
	defer cancelRun()
	defer client.Close()
	go func() { _ = client.Run(runCtx) }()

	// First handshake, bounded: a wrong endpoint should say so, not spin.
	deadline := time.Now().Add(attachConnectTimeout)
	for !client.Connected() {
		if time.Now().After(deadline) {
			bridgeMu.Lock()
			derr := dialErr
			bridgeMu.Unlock()
			if derr != nil {
				return fmt.Errorf("attach: cannot reach a terva daemon at %s: %v (is `terva web` running there? does it need --token?)", endpoint, derr)
			}
			return fmt.Errorf("attach: no ctrlproto handshake from %s within %s", endpoint, attachConnectTimeout)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(50 * time.Millisecond):
		}
	}
	hello, _ := client.ServerHello()
	svc := client.Service()

	// Session binding, PWA-style: an explicit --resume <id> wins, else the
	// daemon's current session, else the newest, else a fresh one.
	infos, err := svc.Sessions(ctx)
	if err != nil {
		return fmt.Errorf("attach: sessions.list: %w", err)
	}
	var cur ctrlproto.SessionInfo
	switch {
	case args.ResumeID != "":
		cur, err = svc.ResumeSession(ctx, build.SessionIDFromPath(args.ResumeID))
		if err != nil {
			return fmt.Errorf("attach: --resume %s: %w", args.ResumeID, err)
		}
	case len(infos) > 0:
		cur = infos[0]
		for _, s := range infos {
			if s.Current {
				cur = s
				break
			}
		}
		// Materialize the pick daemon-side so subscribe/prompt land on it.
		if resumed, rerr := svc.ResumeSession(ctx, cur.ID); rerr == nil {
			cur = resumed
		}
	default:
		cur, err = svc.CreateSession(ctx, ctrlproto.CreateOpts{})
		if err != nil {
			return fmt.Errorf("attach: create session: %w", err)
		}
	}

	initialCfg, _ := config.LoadConfig()
	theme, _, themeErr := tui.DetectThemeWithCustom(config.TervaHome(), initialCfg.Theme, 80*time.Millisecond)
	if themeErr != nil {
		fmt.Fprintln(os.Stderr, "theme load:", themeErr)
	}
	authMgr := auth.NewManager(config.AuthStoreFor())
	defer authMgr.Close()
	// The daemon names the tree this client controls (Hello.CWD); empty means
	// a pre-CWD daemon — fall back to the local process cwd, the old lie.
	// Same-host attach (the v1 stance) then points the git prober, the @-file
	// picker, status scripts, and OSC-7 at the daemon's tree no matter where
	// the TUI was launched; cross-host, a dir that doesn't exist locally
	// degrades to an absent git segment and an empty picker — honest, where
	// the local read showed some other repo's state.
	cwd := hello.CWD
	if cwd == "" {
		cwd, _ = os.Getwd()
	}

	// @-file completion rides the wire when the daemon serves files.list —
	// correct from any host. An older daemon leaves this nil and the picker
	// keeps its local-disk read at the daemon's advertised cwd (same-host
	// correct, the previous behavior).
	var remoteFiles widgets.RemoteLister
	if slices.Contains(hello.Features, ctrlproto.FeatureFilesList) {
		remoteFiles = func(dir string, recursive, respectGitignore bool) ([]fswalk.Entry, bool, error) {
			res, lerr := svc.ListFiles(ctx, ctrlproto.FilesListParams{
				Dir: filepath.ToSlash(dir), Recursive: recursive, RespectGitignore: respectGitignore,
			})
			if lerr != nil {
				return nil, false, lerr
			}
			entries := make([]fswalk.Entry, 0, len(res.Files))
			for _, fe := range res.Files {
				entries = append(entries, fswalk.Entry{Rel: filepath.FromSlash(fe.Path), IsDir: fe.Dir})
			}
			return entries, res.Truncated, nil
		}
	}

	// On-demand session titling rides the wire only when the daemon
	// advertises it; nil keeps the picker's `g` an honest "unavailable".
	var generateTitleFn func(path string) (string, error)
	if slices.Contains(hello.Features, ctrlproto.FeatureGenerateTitle) {
		generateTitleFn = func(path string) (string, error) {
			return svc.GenerateSessionTitle(ctx, build.SessionIDFromPath(path))
		}
	}

	carrier := attachCarrier{Service: svc, client: client}
	var ivLocal *modes.Interactive
	ivLocal = modes.NewInteractive(modes.InteractiveConfig{
		Terminal:            tui.NewProcTerm(),
		Theme:               theme,
		ThemeName:           initialCfg.Theme,
		InlineImagesEnabled: initialCfg.InlineImagesEnabled,
		StatusLineRows:      initialCfg.StatusLineRows(),
		StatusScripts:       statusScriptsForTUI(initialCfg),
		SettingsStore:       configSettingsStore{},
		Model:               cur.Model,
		Provider:            cur.Provider,
		PersonaName:         cur.Persona,
		Trusted:             cur.Trusted,
		CWD:                 cwd,
		TervaHome:           config.TervaHome(),
		Version:             version,
		BootNotice:          i18n.T("attached to %s — terva v%s", endpoint.String(), hello.Version),
		Ready:               true,
		Carrier:             carrier,
		CarrierSession:      cur.ID,
		CarrierRemote:       true,
		// The daemon serves the tasks surface over the wire; /swarm and the
		// status bar's swarm glance work attached (the snapshot cache fills
		// asynchronously, so no render frame blocks on the network).
		CarrierTasks: true,
		InitialInput: args.Prompt,
		// attach --resume: open the session picker over the PWA-style
		// binding above (bare flag only — an explicit id already bound).
		// Esc keeps that binding — what attach does without the flag; the
		// picker's list/rename verbs already ride the wire.
		OpenSessionsOnBoot: args.Resume && args.ResumeID == "",
		// CarrierLocal is FALSE: the daemon is somewhere else, so a loopback OAuth
		// callback bound on ITS host is unreachable from this browser. The daemon
		// hands back the paste-back form instead.
		//
		// And note what is NOT here: a local auth.Manager. Attach used to be given
		// one, which meant a /login driven from an attached TUI ran the flow on
		// THIS machine and stored the credential HERE — on the client, not on the
		// daemon whose agent actually needs it. The dialog then fail-softed with
		// "credentials are owned by the daemon", after the credential had already
		// been written to the wrong disk. /login now goes over the wire to the
		// daemon's AuthController, which is the only place a credential is any use.
		CarrierLocal: false,

		RecursiveFileSuggest: initialCfg.RecursiveFileSuggest,
		RemoteFiles:          remoteFiles,
		RespectGitignore:     initialCfg.RespectGitignore,

		// Session flows that are pure wire verbs work attached; the session
		// FILE helpers (fork/tree/export) stay unwired — they read local disk
		// and the transcripts live with the daemon.
		LoadSession: func(path string) error {
			return ivLocal.SwitchCarrierSession(build.SessionIDFromPath(path))
		},
		NewSession: func(providerName, model string) error {
			created, cerr := svc.CreateSession(ctx, ctrlproto.CreateOpts{Provider: providerName, Model: model})
			if cerr != nil {
				return cerr
			}
			return ivLocal.SwitchCarrierSession(created.ID)
		},
		RenameSessionFile: func(path, title string) error {
			return svc.RenameSession(ctx, build.SessionIDFromPath(path), title)
		},
		// Feature-gated (generateTitleFn): nil against a pre-generate-title
		// daemon, so the picker's `g` reports unavailability instead of
		// firing a method the daemon can't serve.
		GenerateSessionTitle: generateTitleFn,
		ListSessions: func() []core.SessionSummary {
			list, lerr := svc.Sessions(ctx)
			if lerr != nil {
				return nil
			}
			return workspace.SessionSummariesFromInfos(list)
		},
		CurrentSessionPath: func() string {
			// The status bar's sess segment reads this every repaint: serve it
			// from the wire-fed cache (noteSessionMeta), never a per-frame
			// resume RPC. Before the binding's first snapshot lands, the
			// resume that picked the session already knows the path.
			if p := ivLocal.CarrierSessionPath(); p != "" {
				return p
			}
			return cur.Path
		},
		FlushSession: func() {},

		// Model catalog reads stay local (same host, same config store —
		// the v1 attach rule); the actions ride the wire.
		LoggedInProviders: loggedInProviderList,
		FavoriteModels: func() []string {
			cfg, _ := config.LoadConfig()
			return cfg.FavoriteModels
		},
		SetFavoriteModel: func(key string, on bool) error {
			prov, model, ok := strings.Cut(key, "/")
			if !ok {
				return fmt.Errorf("malformed favorite key %q", key)
			}
			return svc.SetFavoriteModel(ctx, prov, model, on)
		},
		// The picker's ctrl+d was dead in attach mode until models.set_default
		// existed: the promote logic lived in the local carrier's config closure,
		// which a ctrlproto client never builds. It now lands on the daemon,
		// which is the process whose config actually governs new sessions.
		PromoteModelDefault: func(providerName, model, scope string) error {
			return svc.SetDefaultModel(ctx, providerName, model, ctrlproto.DefaultScope(scope))
		},
		TrustWorkspace:   func(parent bool) error { return svc.Trust(ctx, parent) },
		UntrustWorkspace: func() error { return svc.Untrust(ctx) },
	})

	bridgeMu.Lock()
	iv = ivLocal
	bridgeMu.Unlock()
	// The first hello arrived before the TUI existed — seed the sandbox badge
	// now; reconnects re-seed through OnConnect.
	ivLocal.SetCarrierJailed(hello.Jailed)

	// SIGHUP drives the client self-restart, so `systemctl reload` re-execs a
	// persistent attach into the new binary. Installed ONLY when --allow-restart
	// is set — unlike the web daemon (no terminal), an attach client owns a
	// terminal where SIGHUP is also the hangup signal, so without self-restart we
	// leave SIGHUP's default (terminate on hangup) intact rather than swallowing
	// it. runCtx cancels when this returns, tearing the handler down.
	if relaunch.Enabled() {
		installReloadHandler(runCtx)
	}

	return ivLocal.Run(ctx)
}
