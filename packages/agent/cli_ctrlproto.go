package agent

// The --tui-ctrlproto entry point: the interactive TUI driving an in-process
// Workspace through the ctrlproto WorkspaceService instead of a directly-owned
// core.Agent (docs/proposals/tui-on-ctrlproto.md). This is the protocol's
// completeness test — the same TUI should later drive a remote daemon over a
// serialized carrier, which is why the hot path consumes the wire vocabulary.

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
	"terva.sh/terva/packages/tui"
)

// runInteractiveCtrlproto builds a Workspace, creates a session, and runs the
// interactive TUI against it through the carrier seam. Stage 1 wires the hot
// path — prompt dispatch, the event stream, approvals/asks, queue, cancel,
// daemon-side turn policy — through the service; session ops and management
// dialogs remain legacy features until Stages 2–3, so this lean entry omits
// their closures rather than double-building the legacy agent stack next to
// the workspace's own (which would spawn every extension subprocess twice).
// The session agent is still handed to the TUI as the transitional crutch:
// it renders the finalized transcript and feeds the not-yet-migrated dialogs.
func runInteractiveCtrlproto(ctx context.Context, args Args, version string) error {
	// The Workspace requires a credential up front (a daemon cannot run turns
	// without one) — there is no in-TUI login flow on this path yet.
	w, err := NewWorkspace(args, version)
	if err != nil {
		return err
	}
	defer w.Close()

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
	var info ctrlproto.SessionInfo
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
	ag, sessID, err := w.AgentFor(info.ID)
	if err != nil {
		return err
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
		Model:               info.Model,
		Provider:            info.Provider,
		PersonaName:         info.Persona,
		CWD:                 w.cwd,
		TervaHome:           TervaHome(),
		Version:             version,
		Agent:               ag, // transitional crutch: transcript render + unmigrated dialogs
		Carrier:             w,
		CarrierSession:      sessID,
		// /swarm drives the workspace's tasks surface; same gate as the
		// legacy path's cfg.Swarm (withheld from immersive/no-tools sessions
		// so the dashboard can't re-inject the coding skin there).
		CarrierTasks:        hasBaseWorkspaceTools(args),
		InitialInput:        args.Prompt,
		Trusted:             info.Trusted,
		GatedContentPresent: hasGatedProjectContent(w.cwd),

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
