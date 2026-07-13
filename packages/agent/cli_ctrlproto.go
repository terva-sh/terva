package agent

// The interactive entry point: the TUI driving an in-process Workspace
// through the ctrlproto WorkspaceService instead of a directly-owned
// core.Agent (docs/proposals/tui-on-ctrlproto.md; the legacy direct driver
// has been removed — --tui-legacy is accepted as a deprecated no-op). This is
// the protocol's completeness test — the same TUI should later drive a remote
// daemon over a serialized carrier, which is why the hot path consumes the
// wire vocabulary.

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"terva.sh/terva/packages/agent/build"
	"terva.sh/terva/packages/agent/config"
	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/agent/modes"
	"terva.sh/terva/packages/agent/skills"
	"terva.sh/terva/packages/agent/workspace"
	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/i18n"
	"terva.sh/terva/packages/provider/auth"
	"terva.sh/terva/packages/relaunch"
	"terva.sh/terva/packages/tui"
)

// runInteractiveCtrlproto builds a Workspace, creates a session, and runs the
// interactive TUI against it through the carrier seam. Everything on the hot
// path — prompt dispatch, the event stream, approvals/asks, queue, cancel,
// daemon-side turn policy, session ops, management dialogs, transcript
// rendering — rides the service; the session agent is still handed to the
// TUI as the transitional crutch for the residual readers (/usage,
// fork/tree/export file helpers, startup scroll, queue chips).
func runInteractiveCtrlproto(ctx context.Context, args build.Args, version string) error {
	// Pre-TUI terminal prompts, same order as the legacy entry: a character
	// card's {{user}} needs a name (it feeds Resolve inside NewWorkspace),
	// and the first interactive run offers the core extension pack before the
	// Workspace's discovery scan so an accepted install is picked up by this
	// same session. Both are TTY-gated, ask-once no-ops otherwise.
	args = maybePromptUserName(args)
	maybeOfferCorePack(args)

	// Self-restart (--allow-restart): enabled before the Workspace exists so
	// session builds register the terva_restart tool; the /restart command
	// and control.restart gate on it too. Unlike web mode there is no listener
	// here, so there is no insecure-listener refusal to mirror — but the
	// platform gate DOES apply, exactly as in runWebMode: on a host without
	// exec(2) the capability can only ever fail, so don't advertise it. Leaving
	// it on registered terva_restart, offered /restart, and let control.restart
	// through, all so they could error at the end. The TUI's pre-exec hook
	// (terminal restore + session handoff) registers inside Interactive.Run,
	// next to the state it saves.
	allowRestart := args.AllowRestart
	if allowRestart && !relaunch.Supported() {
		fmt.Fprintln(os.Stderr, "terva: self-restart is not supported on this platform — --allow-restart has no effect")
		allowRestart = false
	}
	if allowRestart {
		relaunch.Enable()
	}

	// A credential-less Workspace boots fine (only sessions hard-require a
	// credential), so a first run lands in the TUI with the login dialog
	// auto-opened; CarrierLogin below finishes the flow.
	w, err := workspace.NewWorkspace(args, version)
	if err != nil {
		return err
	}
	defer w.Close()

	// Resolve display metadata WITHOUT requiring a credential — mirrors the
	// pre-TUI resolve the legacy entry did. It supplies the persona banner
	// (name/phonetic/emoji/accent), the meta-mode chrome (experience), and the
	// startup auth-method / reasoning labels; a credential-less boot still gets
	// the right banner and chrome. The Workspace builds the session agents
	// separately, so this is display-only and never touches a session.
	r, rerr := build.Resolve(args, false)
	if rerr != nil {
		return rerr
	}

	// In-TUI login support: the auth manager drives /login (browser OAuth or
	// the api-key form); handleAuthEvent's success path hands the stored
	// credential to CarrierLogin below.
	authMgr := auth.NewManager(config.AuthStoreFor())
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

	// Session selection: --continue reopens the latest session for this cwd
	// (the Workspace's empty-id resolution is exactly that, falling back to a
	// fresh session when none exists); --resume boots the default fresh
	// session and opens the in-TUI session picker over it
	// (OpenSessionsOnBoot below) — Esc keeps the fresh session, a pick
	// switches onto the chosen one. The pre-TUI stderr picker survives only
	// in modes that never own the terminal (openOrCreateSession).
	//
	// On a credential-less boot the first session is DEFERRED until /login
	// succeeds (sessions hard-require a credential); --resume's picker opens
	// after that first login lands (finishCarrierLogin).
	needLogin := w.CredentialErr() != nil
	var info ctrlproto.SessionInfo
	var sessID string
	if !needLogin {
		switch {
		case relaunch.Handoff("SESSION") != "":
			// Restarted in place (--allow-restart): reattach to the session
			// the previous image was serving — whatever the original argv
			// said — falling back to a fresh one if it went missing.
			info, err = w.ResumeSession(ctx, relaunch.Handoff("SESSION"))
			if err != nil {
				info, err = w.CreateSession(ctx, ctrlproto.CreateOpts{})
			}
		case args.ResumeID != "":
			// --resume <id>: direct resume, no picker. Fail loudly — a
			// scripted resume that silently landed on a fresh session would
			// be worse than an error.
			info, err = w.ResumeSession(ctx, build.SessionIDFromPath(args.ResumeID))
			if err != nil {
				return fmt.Errorf("--resume %s: %w", args.ResumeID, err)
			}
		case args.Continue:
			info, err = w.ResumeSession(ctx, "")
		default:
			info, err = w.CreateSession(ctx, ctrlproto.CreateOpts{})
		}
		if err != nil {
			return err
		}
		sessID = info.ID
	}

	// Status-line / banner labels before the first session exists: the
	// workspace's defaults resolve fine without a credential.
	bootProvider, bootModel := info.Provider, info.Model
	bootPersona, bootTrusted := info.Persona, info.Trusted
	if needLogin {
		bootProvider, bootModel = w.Defaults()
		bootPersona, bootTrusted = w.PersonaName(), w.Trusted()
	}

	diagMu.Lock()
	for _, d := range bootDiags {
		fmt.Fprintln(os.Stderr, d)
	}
	diagMu.Unlock()

	initialCfg, _ := config.LoadConfig()
	theme, _, themeErr := tui.DetectThemeWithCustom(config.TervaHome(), initialCfg.Theme, 80*time.Millisecond)
	if themeErr != nil {
		fmt.Fprintln(os.Stderr, "theme load:", themeErr)
	}

	// Update-available banner: check GitHub releases off the UI goroutine and
	// deliver at most one result. agent.UpdateInfo → modes.UpdateInfo here to
	// avoid a cyclic import. Mirrors the legacy entry.
	updateCh := make(chan modes.UpdateInfo, 1)
	go func() {
		defer close(updateCh)
		src := <-CheckForUpdateAsync(config.TervaHome(), version)
		updateCh <- modes.UpdateInfo{
			Current:   src.Current,
			Latest:    src.Latest,
			Available: src.Available,
			URL:       src.URL,
		}
	}()

	// Changelog: when the running version differs from the last version whose
	// release notes the user dismissed, fetch the release body and have the TUI
	// show it once. First-ever launch seeds the stored version silently — don't
	// dump release notes at someone who just installed. Mirrors the legacy entry.
	changelogCh := make(chan modes.ChangelogPayload, 1)
	go func() {
		defer close(changelogCh)
		cfg, _ := config.LoadConfig()
		if cfg.LastChangelogShown == "" {
			SeedChangelogVersion(version)
			return
		}
		if !ShouldShowChangelog(version, cfg) {
			return
		}
		info := <-FetchChangelogAsync(version)
		if info.Body == "" {
			return
		}
		// For dev builds (0.0.0), skip if the latest release was already shown.
		if version == "0.0.0" && info.Version == cfg.LastChangelogShown {
			return
		}
		changelogCh <- modes.ChangelogPayload{
			Version: info.Version,
			Body:    info.Body,
			URL:     info.URL,
		}
	}()

	// After a self-restart resume, tell the operator what build hop happened —
	// the pre-TUI stderr lines are wiped by the TUI's screen clear, so this
	// rides the status line instead.
	bootNotice := ""
	if prev := relaunch.PrevVersion(); prev != "" {
		bootNotice = i18n.T("restarted — was v%s, now v%s", prev, version)
	}

	term := tui.NewProcTerm()
	// Forward-declared so the session-group closures below can re-point the
	// running TUI (the legacy entry point uses the same pattern).
	var iv *modes.Interactive
	iv = modes.NewInteractive(modes.InteractiveConfig{
		Terminal:            term,
		Theme:               experienceTheme(theme, r.Experience),
		ThemeName:           initialCfg.Theme,
		InlineImagesEnabled: initialCfg.InlineImagesEnabled,
		StatusLineRows:      initialCfg.StatusLineRows(),
		StatusScripts:       statusScriptsForTUI(initialCfg),
		SettingsStore:       configSettingsStore{},
		Model:               bootModel,
		Provider:            bootProvider,
		Reasoning:           r.Reasoning,
		AuthMethod:          r.AuthMethod,
		// $TERVA_HOME/models.json path for the Ctrl+E model editor — a local
		// file the editor reads/writes directly, no wire round-trip.
		UserModelsPath: UserModelsPath(),
		// Persona banner + meta-mode chrome (display-only; resolved above
		// without a credential). Experience drives the --chat/--play chrome
		// suppression + spinner flavor; the emoji/accent/phonetic lead and tint
		// the welcome banner.
		PersonaName:     bootPersona,
		PersonaPhonetic: r.Persona.Phonetic(),
		PersonaEmoji:    r.Persona.Emoji,
		PersonaAccent:   r.Persona.AccentColor,
		Experience:      r.Experience,
		// @-mention picker startup prefs (runtime /settings toggles already
		// route through the settings dialog; these honor the persisted values).
		RecursiveFileSuggest: initialCfg.RecursiveFileSuggest,
		RespectGitignore:     initialCfg.RespectGitignore,
		// The workspace-shared sandbox: /jail + /unjail toggle it, the status
		// bar reads its lock, and the local `!`-shell escape enforces it. Every
		// session's file tools already point at this same object.
		Sandbox: w.Sandbox(),
		// Extension-bundled themes in the /settings theme picker (reads the
		// current session's extension manager; nil pre-login).
		ExtensionThemes: func() []tui.ThemeOption { return w.ExtensionThemes(iv.CarrierSessionID()) },
		// Extension slash-commands, status segments, /context detail, panel
		// input, and /reload-ext — resolved against whichever session the TUI
		// is currently bound to (see carrierExtHost). Extension TOOLS already
		// run daemon-side; this restores the interactive/command surface.
		Extensions: workspace.NewCarrierExtHost(w, func() string { return iv.CarrierSessionID() }),
		// /skills picker + /skill <name> priming + completions, resolved
		// against the current session's skill tool. Skill EXECUTION is the
		// model's daemon-side `skill` tool; these are the discovery/refresh
		// reads the picker + autocomplete need.
		SkillSnapshot: func() []*skills.Skill { return w.SkillSnapshot(iv.CarrierSessionID()) },
		ReloadSkills:  func() []*skills.Skill { return w.ReloadSkills(iv.CarrierSessionID()) },
		SkillCompletions: func() []modes.SkillCompletion {
			vis := w.SessionSkills(iv.CarrierSessionID())
			out := make([]modes.SkillCompletion, 0, len(vis))
			for _, sk := range vis {
				out = append(out, modes.SkillCompletion{Name: sk.Name, Desc: sk.Description})
			}
			return out
		},
		// Update-available banner + changelog-on-upgrade overlay.
		UpdateInfoChan: updateCh,
		ChangelogChan:  changelogCh,
		OnChangelogDismiss: func() {
			// Dev builds (0.0.0) store the actual release version so the same
			// changelog doesn't reappear; real builds store the binary version.
			v := version
			if v == "0.0.0" && iv != nil && iv.ChangelogVersion() != "" {
				v = iv.ChangelogVersion()
			}
			_ = MarkChangelogShown(v)
		},
		AutoSwarmEnabled: initialCfg.AutoSwarmEnabled,
		CWD:              w.CWD(),
		TervaHome:        config.TervaHome(),
		Version:          version,
		BootNotice:       bootNotice,
		// A credential-less boot defers the first session until /login, so the
		// prompt gate opens iff a credential resolved. This was `ag != nil`
		// before the agent crutch went away; it means the same thing.
		Ready:          !needLogin,
		Carrier:        w,
		CarrierSession: sessID,
		// /swarm drives the workspace's tasks surface; same gate as the
		// legacy path's cfg.Swarm (withheld from immersive/no-tools sessions
		// so the dashboard can't re-inject the coding skin there).
		CarrierTasks:        build.HasBaseWorkspaceTools(args),
		InitialInput:        args.Prompt,
		OpenSessionsOnBoot:  args.Resume && args.ResumeID == "",
		Trusted:             bootTrusted,
		GatedContentPresent: config.HasGatedProjectContent(w.CWD()),
		JailNotice:          build.JailNoticeFor(args).Message(),

		// --- in-TUI login (the carrier flavor) ---
		// The auth manager runs the OAuth/api-key flows in this process; on
		// success the TUI calls CarrierLogin, which refreshes the workspace's
		// credential/defaults and ensures a session to bind to. Its presence
		// also marks this carrier as login-capable (a remote daemon's client
		// would leave it nil — credentials live server-side there).
		AuthManager:         authMgr,
		RefreshModels:       RefreshModelsForceAsync,
		RefreshCompatModels: RefreshCompatModelsAsync,
		// Logging in/out of Kimi toggles whether terva may fall back to the
		// official Kimi Code CLI token — so a logout actually stops using it.
		SetKimiCLIFallbackDisabled: config.SetKimiCLIFallbackDisabled,
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
			// (Bare --resume needs no case here: it boots the default fresh
			// session and finishCarrierLogin opens the picker over it.)
			switch {
			case args.ResumeID != "":
				return w.ResumeSession(ctx, build.SessionIDFromPath(args.ResumeID))
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
			return iv.SwitchCarrierSession(build.SessionIDFromPath(path))
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
			return w.RenameSession(ctx, build.SessionIDFromPath(path), title)
		},
		GenerateSessionTitle: func(path string) (string, error) {
			return w.GenerateSessionTitle(ctx, build.SessionIDFromPath(path))
		},
		// The /sessions picker lists via the session group instead of its own
		// disk scan — same store, but the service overlays live state (current
		// model, settled title, usage) the file's meta line can lag behind.
		ListSessions: func() []core.SessionSummary {
			infos, err := w.Sessions(ctx)
			if err != nil {
				return nil
			}
			return workspace.SessionSummariesFromInfos(infos)
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
			cfg, _ := config.LoadConfig()
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
				return config.SetProjectModel(w.CWD(), providerName, model)
			case "global":
				cfg, _ := config.LoadConfig()
				cfg.Provider = providerName
				cfg.Model = model
				return config.SaveConfig(cfg)
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
		ReadLogTail: build.ReadLogTail,
		ExtensionConfigFields: func(name string) []modes.ConfigField {
			return extensionConfigFields(w.CWD(), name)
		},
		SetExtensionConfig: func(name string, values map[string]string) error {
			return setExtensionConfigFromForm(w.CWD(), name, values)
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

// carrierExtHost satisfies modes.ExtensionHost. Asserted here, not in the
// workspace package: the assertion is what would force workspace to import
// modes, and cli_ctrlproto.go is the composition root that bridges the two —
// the same reason cli.go holds the modes.Carrier assertion.
var _ modes.ExtensionHost = workspace.CarrierExtHost{}
