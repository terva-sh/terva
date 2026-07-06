package agent

// The default interactive entry point: the TUI driving an in-process
// Workspace through the ctrlproto WorkspaceService instead of a directly-owned
// core.Agent (docs/proposals/tui-on-ctrlproto.md; the legacy direct driver
// remains available under --tui-legacy). This is the protocol's completeness
// test — the same TUI should later drive a remote daemon over a serialized
// carrier, which is why the hot path consumes the wire vocabulary.

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/agent/modes"
	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/provider/auth"
	"terva.sh/terva/packages/tui"
)

// runInteractiveCtrlproto builds a Workspace, creates a session, and runs the
// interactive TUI against it through the carrier seam. Everything on the hot
// path — prompt dispatch, the event stream, approvals/asks, queue, cancel,
// daemon-side turn policy, session ops, management dialogs, transcript
// rendering — rides the service; the session agent is still handed to the
// TUI as the transitional crutch for the residual readers (/usage,
// fork/tree/export file helpers, startup scroll, queue chips).
func runInteractiveCtrlproto(ctx context.Context, args Args, version string) error {
	// Pre-TUI terminal prompts, same order as the legacy entry: a character
	// card's {{user}} needs a name (it feeds Resolve inside NewWorkspace),
	// and the first interactive run offers the core extension pack before the
	// Workspace's discovery scan so an accepted install is picked up by this
	// same session. Both are TTY-gated, ask-once no-ops otherwise.
	args = maybePromptUserName(args)
	maybeOfferCorePack(args)

	// A credential-less Workspace boots fine (only sessions hard-require a
	// credential), so a first run lands in the TUI with the login dialog
	// auto-opened; CarrierLogin below finishes the flow.
	w, err := NewWorkspace(args, version)
	if err != nil {
		return err
	}
	defer w.Close()

	// In-TUI login support: the auth manager drives /login (browser OAuth or
	// the api-key form); handleAuthEvent's success path hands the stored
	// credential to CarrierLogin below.
	authMgr := auth.NewManager(AuthStoreFor())
	defer authMgr.Close()

	// Session-build diagnostics (permission-policy warnings, extension-load
	// errors) fire before the TUI exists: buffer them, then emit to stderr
	// below — before raw mode, exactly where the legacy path prints them.
	var diagMu sync.Mutex
	var bootDiags []string
	w.SetDiag(func(msg string) {
		diagMu.Lock()
		bootDiags = append(bootDiags, msg)
		diagMu.Unlock()
	})

	// Session selection mirrors the legacy startup: --continue reopens the
	// latest session for this cwd (the Workspace's empty-id resolution is
	// exactly that, falling back to a fresh session when none exists);
	// --resume runs the pre-TUI terminal picker; default is a fresh session.
	//
	// On a credential-less boot the first session is DEFERRED until /login
	// succeeds (sessions hard-require a credential). --resume's picker still
	// runs now — it is a disk scan needing no credential and cannot run once
	// the TUI owns the terminal — and the pick materializes post-login.
	needLogin := w.CredentialErr() != nil
	var info ctrlproto.SessionInfo
	var ag *core.Agent
	var sessID, pendingResume string
	if needLogin {
		if args.Resume {
			picked, perr := pickSession(args.CWD)
			if perr != nil {
				return perr
			}
			pendingResume = sessionIDFromPath(picked)
		}
	} else {
		switch {
		case args.Continue:
			info, err = w.ResumeSession(ctx, "")
		case args.Resume:
			picked, perr := pickSession(args.CWD)
			if perr != nil {
				return perr
			}
			if picked == "" {
				info, err = w.CreateSession(ctx, ctrlproto.CreateOpts{})
			} else {
				info, err = w.ResumeSession(ctx, sessionIDFromPath(picked))
			}
		default:
			info, err = w.CreateSession(ctx, ctrlproto.CreateOpts{})
		}
		if err != nil {
			return err
		}
		ag, sessID, err = w.AgentFor(info.ID)
		if err != nil {
			return err
		}
	}

	// Status-line / banner labels before the first session exists: the
	// workspace's defaults resolve fine without a credential.
	bootProvider, bootModel := info.Provider, info.Model
	bootPersona, bootTrusted := info.Persona, info.Trusted
	if needLogin {
		bootProvider, bootModel = w.Defaults()
		bootPersona, bootTrusted = w.personaName, w.Trusted()
	}

	diagMu.Lock()
	for _, d := range bootDiags {
		fmt.Fprintln(os.Stderr, d)
	}
	diagMu.Unlock()

	initialCfg, _ := LoadConfig()
	theme, _, themeErr := tui.DetectThemeWithCustom(TervaHome(), initialCfg.Theme, 80*time.Millisecond)
	if themeErr != nil {
		fmt.Fprintln(os.Stderr, "theme load:", themeErr)
	}

	term := tui.NewProcTerm()
	// Forward-declared so the session-group closures below can re-point the
	// running TUI (the legacy entry point uses the same pattern).
	var iv *modes.Interactive
	iv = modes.NewInteractive(modes.InteractiveConfig{
		Terminal:            term,
		Theme:               theme,
		ThemeName:           initialCfg.Theme,
		InlineImagesEnabled: initialCfg.InlineImagesEnabled,
		StatusLineRows:      initialCfg.StatusLineRows(),
		StatusScripts:       statusScriptsForTUI(initialCfg),
		SettingsStore:       configSettingsStore{},
		Model:               bootModel,
		Provider:            bootProvider,
		PersonaName:         bootPersona,
		AutoSwarmEnabled:    initialCfg.AutoSwarmEnabled,
		CWD:                 w.cwd,
		TervaHome:           TervaHome(),
		Version:             version,
		Agent:               ag, // transitional crutch: residual readers only (usage, fork/tree/export, startup scroll); nil until login on a credential-less boot
		Carrier:             w,
		CarrierSession:      sessID,
		// /swarm drives the workspace's tasks surface; same gate as the
		// legacy path's cfg.Swarm (withheld from immersive/no-tools sessions
		// so the dashboard can't re-inject the coding skin there).
		CarrierTasks:        hasBaseWorkspaceTools(args),
		InitialInput:        args.Prompt,
		Trusted:             bootTrusted,
		GatedContentPresent: hasGatedProjectContent(w.cwd),

		// --- in-TUI login (the carrier flavor) ---
		// The auth manager runs the OAuth/api-key flows in this process; on
		// success the TUI calls CarrierLogin, which refreshes the workspace's
		// credential/defaults and ensures a session to bind to. Its presence
		// also marks this carrier as login-capable (a remote daemon's client
		// would leave it nil — credentials live server-side there).
		AuthManager:         authMgr,
		RefreshModels:       RefreshModelsForceAsync,
		RefreshCompatModels: RefreshCompatModelsAsync,
		CarrierLogin: func(current string) (ctrlproto.SessionInfo, error) {
			if err := w.RefreshDefaults(); err != nil {
				return ctrlproto.SessionInfo{}, err
			}
			if current != "" {
				// Re-login: hot-swap the live session's provider client so
				// the fresh credential applies now, not at the next
				// cross-provider /model swap.
				if err := w.RefreshSessionCredential(ctx, current); err != nil {
					return ctrlproto.SessionInfo{}, err
				}
				return w.ResumeSession(ctx, current)
			}
			// First login on a credential-less boot: honor the launch-time
			// session selection that was deferred until a credential existed.
			switch {
			case pendingResume != "":
				return w.ResumeSession(ctx, pendingResume)
			case args.Continue:
				return w.ResumeSession(ctx, "")
			default:
				return w.CreateSession(ctx, ctrlproto.CreateOpts{})
			}
		},

		// --- session group (tui-on-ctrlproto.md Stage 2) ---
		// Every legacy session flow (/sessions pick, /new, fork, import,
		// tree) funnels through these seams; here they route through the
		// WorkspaceService instead of rebuilding an agent in place. The
		// helpers below (export/fork/tree) still operate on session FILES —
		// local-disk core helpers with no wire verbs (v1 scope) — but every
		// resulting SWITCH goes through the service.
		LoadSession: func(path string) error {
			return iv.SwitchCarrierSession(sessionIDFromPath(path))
		},
		NewSession: func(providerName, model string) error {
			// Provider qualifies the model id: some ids exist under several
			// providers and the unqualified lookup can land on one with no
			// credential (caught by the Stage-2 live smoke).
			created, err := w.CreateSession(ctx, ctrlproto.CreateOpts{Provider: providerName, Model: model})
			if err != nil {
				return err
			}
			return iv.SwitchCarrierSession(created.ID)
		},
		RenameSessionFile: func(path, title string) error {
			return w.RenameSession(ctx, sessionIDFromPath(path), title)
		},
		// The /sessions picker lists via the session group instead of its own
		// disk scan — same store, but the service overlays live state (current
		// model, settled title, usage) the file's meta line can lag behind.
		ListSessions: func() []core.SessionSummary {
			infos, err := w.Sessions(ctx)
			if err != nil {
				return nil
			}
			return sessionSummariesFromInfos(infos)
		},
		CurrentSessionPath: func() string {
			cur, err := w.ResumeSession(ctx, iv.CarrierSessionID())
			if err != nil {
				return ""
			}
			return cur.Path
		},
		// FlushSession is a no-op: the Workspace persists every message
		// durably as it lands (wireHeadlessSessionPersist), so the session
		// file is already current when export/fork/tree read it.
		FlushSession: func() {},

		// --- control group (tui-on-ctrlproto.md Stage 3) ---
		// The /model picker's catalog reads stay local (same process, same
		// config store the Workspace reads); the ACTIONS ride the service:
		// swapModelCarrier → SwitchModel, favorites → SetFavoriteModel,
		// /trust → Trust/Untrust (which also reloads project content across
		// every open session — more complete than the legacy closure).
		LoggedInProviders: loggedInProviderList,
		FavoriteModels: func() []string {
			cfg, _ := LoadConfig()
			return cfg.FavoriteModels
		},
		SetFavoriteModel: func(key string, on bool) error {
			prov, model, ok := strings.Cut(key, "/")
			if !ok {
				return fmt.Errorf("malformed favorite key %q", key)
			}
			return w.SetFavoriteModel(ctx, prov, model, on)
		},
		PromoteModelDefault: func(providerName, model, scope string) error {
			switch scope {
			case "project":
				return setProjectModel(w.cwd, providerName, model)
			case "global":
				cfg, _ := LoadConfig()
				cfg.Provider = providerName
				cfg.Model = model
				return SaveConfig(cfg)
			default:
				return fmt.Errorf("unknown model-default scope %q", scope)
			}
		},
		TrustWorkspace: func(parent bool) error {
			return w.Trust(ctx, parent)
		},
		UntrustWorkspace: func() error {
			return w.Untrust(ctx)
		},

		// --- extension log viewer + config form ---
		// In-process affordances the wire only flags (ExtensionInfo.HasLog/
		// HasConfig): the log tail and the form schema/values are local disk
		// reads, and the persist half writes the project config — same
		// helpers the legacy entry wires. Only APPLYING a saved config to the
		// running extension is daemon work, so that rides the extensions
		// surface's "config" action (which the web client shares).
		ReadLogTail: readLogTail,
		ExtensionConfigFields: func(name string) []modes.ConfigField {
			return extensionConfigFields(w.cwd, name)
		},
		SetExtensionConfig: func(name string, values map[string]string) error {
			return setExtensionConfigFromForm(w.cwd, name, values)
		},
		ApplyExtensionConfig: func(name string) {
			_ = w.SurfaceAction(ctx, iv.CarrierSessionID(), "extensions", "config",
				map[string]string{"name": name})
		},
	})

	// From here on the TUI owns the screen: route daemon diagnostics into the
	// ext-notes block instead of stderr, which would corrupt the frame.
	w.SetDiag(func(msg string) { iv.Notify("workspace", "info", msg) })

	return iv.Run(ctx)
}

// sessionSummariesFromInfos maps the session group's wire view back onto the
// picker's native row type. FirstUserText stays empty deliberately: the
// service already folds it into Title (titleFromFirstText), which is the only
// thing the picker uses it for.
func sessionSummariesFromInfos(infos []ctrlproto.SessionInfo) []core.SessionSummary {
	out := make([]core.SessionSummary, 0, len(infos))
	for _, in := range infos {
		started, _ := time.Parse(time.RFC3339, in.Created)
		out = append(out, core.SessionSummary{
			Path:         in.Path,
			Started:      started,
			Model:        in.Model,
			Provider:     in.Provider,
			MessageCount: in.Messages,
			TotalCost:    in.Usage.CostUSD,
			Title:        in.Title,
		})
	}
	return out
}
