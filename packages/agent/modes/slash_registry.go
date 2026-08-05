package modes

// The slash-command registry (roadmap B8, second half). One ordered
// table declares every built-in command: canonical name, aliases,
// popup/help description, hidden flag, whether dispatch must cancel a
// running turn first, and the handler. Everything else derives from
// it — the autocomplete catalog, /help, known-command routing, the
// cancel-before-run set, and runSlash dispatch — replacing five
// hand-synced structures (slashCatalog, hiddenSlashCommands,
// slashCancelsTurn, the runSlash switch, isKnownSlashCommand's
// lists) whose drift had already made the /perms, /telegram, and
// /tg aliases undispatchable from the editor.
//
// Adding a command is one slashSpec at the right display position.

import (
	"context"
	"strings"

	"terva.sh/terva/packages/agent/config"

	"terva.sh/terva/packages/agent/slash"
	"terva.sh/terva/packages/i18n"
	"terva.sh/terva/packages/tui"
)

// slashSpec is one built-in slash command as this package dispatches
// it: the shared metadata from packages/agent/slash (the neutral,
// handler-free catalog every command surface consumes) joined with the
// interactive handler bound at init.
type slashSpec struct {
	name    string   // canonical name, with leading slash
	aliases []string // alternate spellings that dispatch identically
	desc    string   // autocomplete popup + /help description
	// group is the display section this command belongs to. Consecutive
	// registry entries sharing a group render under one divider row in
	// the popup (and one section label in /help). Empty = ungrouped
	// (used for /help itself, which leads the list without a divider).
	group string
	// hint, when set, describes the argument a command accepts. Empty for
	// commands that take no argument.
	hint string
	// hidden commands dispatch but stay out of the popup and /help
	// (internal verbs like /cd, driven by extensions).
	hidden bool
	// cancelsTurn marks commands that mutate the transcript or
	// replace the agent: dispatch cancels the active turn and waits
	// for idle before running them, so they don't race a streaming
	// response. Safe commands run immediately.
	cancelsTurn bool
	run         func(i *Interactive, ctx context.Context, parts []string, raw string) (done bool)
}

// slashHandlers binds each catalog entry — by canonical name — to its
// interactive handler. Metadata (descriptions, aliases, groups, hints,
// flags, display order) lives in packages/agent/slash; adding a command
// means one Spec there and one entry here. The two tables cannot
// drift: TestSlashHandlersMatchCatalog fails on a spec without a
// handler or a handler without a spec.
var slashHandlers = map[string]func(i *Interactive, ctx context.Context, parts []string, raw string) (done bool){
	"/help": (*Interactive).slashHelp,

	"/new": func(i *Interactive, _ context.Context, _ []string, _ string) bool {
		i.startNewSession()
		return false
	},
	"/sessions": func(i *Interactive, _ context.Context, _ []string, _ string) bool {
		i.openSessionsDialog()
		return false
	},
	"/session": func(i *Interactive, _ context.Context, parts []string, _ string) bool {
		if len(parts) >= 2 {
			action := parts[1]
			arg := ""
			if len(parts) >= 3 {
				arg = strings.Join(parts[2:], " ")
			}
			i.doSessionOp(action, arg)
			return false
		}
		i.openSessionOpsDialog()
		return false
	},
	"/jump": func(i *Interactive, _ context.Context, parts []string, _ string) bool {
		i.openJumpDialog(parts[1:])
		return false
	},
	"/compact": func(i *Interactive, ctx context.Context, _ []string, _ string) bool {
		i.runCompact(ctx)
		return false
	},
	"/clear": (*Interactive).slashClear,

	"/study": (*Interactive).slashStudy,
	"/btw": func(i *Interactive, _ context.Context, parts []string, _ string) bool {
		i.openBtwDialog(parts[1:])
		return false
	},
	"/skill": (*Interactive).slashSkill,
	"/skills": func(i *Interactive, _ context.Context, _ []string, _ string) bool {
		i.openSkillsDialog()
		return false
	},
	"/context": func(i *Interactive, _ context.Context, _ []string, _ string) bool {
		i.slashContext()
		return false
	},
	// Crossing a /clear. Scrolling up walks back through compactions on its own, but
	// it stops at a clear: that was a deliberate act — "done with that, start fresh"
	// — and closer to a session boundary than a compaction, which merely condenses a
	// conversation you are still having. So undoing it takes a deliberate act too.
	// Not an unlock: those turns are in the session file either way.
	"/reveal": func(i *Interactive, _ context.Context, _ []string, _ string) bool {
		i.revealAcrossClear()
		return false
	},
	"/lore": func(i *Interactive, _ context.Context, _ []string, _ string) bool {
		i.slashLore()
		return false
	},
	"/memory": func(i *Interactive, _ context.Context, _ []string, _ string) bool {
		i.slashMemory()
		return false
	},
	"/tasks": func(i *Interactive, _ context.Context, _ []string, _ string) bool {
		i.openTasksDialog()
		return false
	},
	"/worktree": func(i *Interactive, _ context.Context, parts []string, _ string) bool {
		i.openWorktreeDialog(len(parts) > 1 && strings.EqualFold(parts[1], "collect"))
		return false
	},

	"/model": func(i *Interactive, _ context.Context, parts []string, _ string) bool {
		if len(parts) >= 2 {
			i.applyModelSelection("", parts[1])
			return false
		}
		var loggedIn []string
		if i.cfg.LoggedInProviders != nil {
			loggedIn = i.cfg.LoggedInProviders()
		}
		var favs []string
		if i.cfg.FavoriteModels != nil {
			favs = i.cfg.FavoriteModels()
		}
		i.modelDialog.Open(i.cfg.Model, loggedIn, favs)
		return false
	},
	"/reasoning": func(i *Interactive, _ context.Context, parts []string, _ string) bool {
		if len(parts) >= 2 {
			i.applyReasoningSelection(parts[1])
			return false
		}
		i.openReasoningDialog()
		return false
	},
	"/login": func(i *Interactive, _ context.Context, _ []string, _ string) bool {
		i.dialog.Open(i.cfg.AuthStore)
		return false
	},
	"/logout": func(i *Interactive, _ context.Context, parts []string, _ string) bool {
		if len(parts) >= 2 {
			// Explicit target: /logout anthropic | openai | all
			i.doLogout(parts[1])
			return false
		}
		// No arg: open the picker over whichever providers are
		// currently logged in. If nothing's logged in, bail with
		// a status line.
		i.openLogoutDialog()
		return false
	},
	"/usage": func(i *Interactive, _ context.Context, _ []string, _ string) bool {
		i.openUsageDialog()
		return false
	},
	"/resets": func(i *Interactive, _ context.Context, _ []string, _ string) bool {
		i.openResetsDialog()
		return false
	},

	"/permissions": func(i *Interactive, _ context.Context, _ []string, _ string) bool {
		i.openPermissionsDialog()
		return false
	},
	"/jail":    (*Interactive).slashJail,
	"/unjail":  (*Interactive).slashUnjail,
	"/trust":   (*Interactive).slashTrust,
	"/untrust": (*Interactive).slashUntrust,

	"/swarm": func(i *Interactive, ctx context.Context, parts []string, _ string) bool {
		i.runSwarm(ctx, parts[1:])
		return false
	},
	"/workflows": func(i *Interactive, _ context.Context, _ []string, _ string) bool {
		i.openWorkflowsDialog()
		return false
	},
	"/extensions": func(i *Interactive, _ context.Context, _ []string, _ string) bool {
		i.openExtensionsDialog()
		return false
	},
	"/reload-ext": func(i *Interactive, ctx context.Context, _ []string, _ string) bool {
		i.runReloadExt(ctx)
		return false
	},
	"/mcp": func(i *Interactive, _ context.Context, _ []string, _ string) bool {
		i.openMCPDialog()
		return false
	},
	"/connect": func(i *Interactive, _ context.Context, parts []string, _ string) bool {
		if len(parts) >= 2 {
			i.doConnector(strings.Join(parts[1:], " "))
			return false
		}
		i.openConnectDialog()
		return false
	},

	"/status": func(i *Interactive, _ context.Context, _ []string, _ string) bool {
		i.slashStatus()
		return false
	},
	"/restart": (*Interactive).slashRestart,
	"/settings": func(i *Interactive, _ context.Context, _ []string, _ string) bool {
		i.openSettingsDialog()
		return false
	},
	"/paste": func(i *Interactive, _ context.Context, _ []string, _ string) bool {
		i.pasteClipboard()
		return false
	},
	"/migrate": func(i *Interactive, _ context.Context, _ []string, _ string) bool {
		i.openMigrateDialog()
		return false
	},
	"/exit": func(*Interactive, context.Context, []string, string) bool { return true },
	"/cd":   (*Interactive).slashCD,
}

// slashRegistry is the dispatch table: the shared catalog joined with
// slashHandlers, in the catalog's display order. Assigned in init()
// rather than a var initializer: handler bodies may (transitively)
// reference the registry — /help renders the catalog, dispatch helpers
// look commands up — and a var initializer would make that an
// initialization cycle.
var slashRegistry []slashSpec

func init() {
	specs := slash.Registry()
	slashRegistry = make([]slashSpec, 0, len(specs))
	for _, s := range specs {
		slashRegistry = append(slashRegistry, slashSpec{
			name:        s.Name,
			aliases:     s.Aliases,
			desc:        s.Desc,
			group:       s.Group,
			hint:        s.Hint,
			hidden:      s.Hidden,
			cancelsTurn: s.CancelsTurn,
			run:         slashHandlers[s.Name],
		})
	}
}

// lookupSlash resolves a typed command head (canonical name or alias)
// to its spec.
func lookupSlash(head string) (*slashSpec, bool) {
	for idx := range slashRegistry {
		s := &slashRegistry[idx]
		if s.name == head {
			return s, true
		}
		for _, a := range s.aliases {
			if a == head {
				return s, true
			}
		}
	}
	return nil, false
}

// builtinSlashCatalog is the autocomplete/help view of the registry:
// every non-hidden command, in registry order, with a header row
// inserted where the display group changes. A function rather than
// a package var so the registry's handler bodies may reference
// catalog consumers (e.g. /help renders it) without an
// initialization cycle.
func builtinSlashCatalog() []slashCommand {
	out := make([]slashCommand, 0, len(slashRegistry))
	prevGroup := ""
	for _, s := range slashRegistry {
		if s.hidden {
			continue
		}
		if s.group != "" && s.group != prevGroup {
			out = append(out, slashCommand{Header: true, Name: i18n.T(s.group)})
		}
		prevGroup = s.group
		out = append(out, slashCommand{Name: s.name, Desc: i18n.T(s.desc)})
	}
	return out
}

// slashCancelsTurn reports whether dispatching head must cancel the
// active turn first (see slashSpec.cancelsTurn).
func slashCancelsTurn(head string) bool {
	s, ok := lookupSlash(head)
	return ok && s.cancelsTurn
}

// isKnownSlashCommand reports whether text's head matches a built-in
// command name or alias. Extension commands are looked up separately
// by the dispatcher (which consults the extension manager).
func isKnownSlashCommand(text string) bool {
	text = strings.TrimSpace(text)
	if text == "" || text[0] != '/' {
		return false
	}
	head := text
	if i := strings.IndexAny(text, " \t\n"); i >= 0 {
		head = text[:i]
	}
	_, ok := lookupSlash(head)
	return ok
}

// ---- handlers too large to inline in the table ----

// slashRestart drives Tier-1 self-restart through the carrier's control verb —
// the same control.restart the web panel's Restart button calls. The daemon
// side answers with a clear error when the capability is off (no
// --allow-restart), which lands on the status line. On success the pre-exec
// hook (registered in Run) tears the terminal down and hands the session id
// to the next image; there is nothing more to do here but tell the user.
func (i *Interactive) slashRestart(_ context.Context, _ []string, _ string) bool {
	if i.cfg.Carrier == nil {
		i.setStatusErr(i18n.T("no workspace to restart"))
		return false
	}
	if err := i.cfg.Carrier.Restart(context.Background()); err != nil {
		i.setStatusErr(err.Error())
		return false
	}
	i.setStatusOK(i18n.T("restarting…"))
	return false
}

func (i *Interactive) slashHelp(context.Context, []string, string) bool {
	i.mu.Lock()
	i.helpBlock = renderHelpBlock(i.cfg.Theme, i.lastCols())
	i.statusErr = ""
	i.statusOK = ""
	// Pin the viewport to the newest content so the help block,
	// which we just appended to the end of the transcript, is
	// what the user actually sees.
	i.scrollOffset = 0
	i.mu.Unlock()
	return false
}

func (i *Interactive) slashClear(context.Context, []string, string) bool {
	// The service owns the transcript: Clear wipes the live agent AND writes
	// the durable empty checkpoint (so a resume starts fresh too). A still-busy
	// session lands on the status line. With no workspace bound (embedder/test)
	// there is no transcript to wipe — the local view still resets below.
	if c := i.cfg.Carrier; c != nil {
		if err := c.Clear(context.Background(), i.carrierSession()); err != nil {
			i.setStatusErr(err.Error())
			i.invalidate()
			return false
		}
	}
	i.mu.Lock()
	i.toolCalls = map[string]*tui.ToolCallView{}
	i.toolOrder = nil
	i.turns.ResetGates()
	i.statusErr = ""
	i.statusOK = ""
	i.helpBlock = nil
	i.parkedTurn = 0
	i.parkedTotal = 0
	i.scrollOffset = 0
	i.resetNotes()
	i.shellBlock = nil
	i.view.InvalidateRenderCache()
	// Clear the screen + scrollback too, so a cleared conversation actually
	// looks cleared instead of leaving the old transcript in the terminal's
	// scrollback above (same reasoning as /new; VS Code-safe via keepScrollback).
	if i.rend != nil {
		i.rend.Clear()
	}
	i.mu.Unlock()
	i.invalidate()
	return false
}

// slashStudy sends a canned prompt that tells the agent to read every
// file in some target so its later turns have the whole thing in
// context. With no argument, the target is the current directory.
// With an argument, the target is whatever the user passed — typed by
// hand, drag-dropped, or selected via the @ file picker (which is why
// we accept both files and directories; the @-picker chips for both
// have already been expanded to absolute paths before dispatch).
// Dispatched through the normal queue-or-start path so it behaves
// identically to typing the prompt by hand.
func (i *Interactive) slashStudy(ctx context.Context, parts []string, raw string) bool {
	studyPrompt := buildStudyPrompt(strings.TrimSpace(strings.TrimPrefix(raw, parts[0])), i.cfg.CWD)
	// startTurn claims-or-queues atomically inside the turn engine,
	// so a busy agent gets the prompt at its next safe boundary.
	i.startTurn(ctx, studyPrompt)
	return false
}

// slashCD switches the running session's cwd. Hidden: not in the
// popup, not in /help. Used by the workspaces extension's panel-key
// Enter handler so picking a row jumps terva into that directory
// without relaunching.
//
// Recovers the raw argument (path) from the original command string
// rather than parts, so paths with spaces survive. The host's
// ChangeCWD hook handles validation, session close + reopen, agent
// rebuild, sandbox re-rooting, and re-jail-if-jailed semantics.
func (i *Interactive) slashCD(_ context.Context, _ []string, _ string) bool {
	// Each session is pinned to its working directory — sessions, trust,
	// extensions and the permission policy all resolve from it — so mid-session
	// /cd isn't offered. This used to be a nil-check on a ChangeCWD host hook,
	// but the direct driver that supplied it is gone and nothing else ever did,
	// so this message was already the only outcome.
	i.setStatusErr(i18n.T("/cd isn't available here — each session is pinned to its working directory; start terva in the target directory instead"))
	return false
}

// slashTrust persists Workspace Trust for the current cwd (with `parent`
// it also trusts descendants), then re-applies project content for the
// session by re-cd-ing into the same directory — which rebuilds the
// agent with the now-trusted Resolve (project extensions/skills/context
// load). If the host didn't wire a rebuild path, it persists and tells
// the user a restart will apply it. See docs/plans/workspace-trust.md.
// wantsAlways reports whether a slash arg asked for the decision to be
// remembered. Accepts the repo's bare-word idiom (`/unjail always`, matching
// `/trust parent`) and the flag spelling people reach for anyway.
func wantsAlways(parts []string) bool {
	for _, p := range parts[1:] {
		switch strings.ToLower(strings.TrimSpace(p)) {
		case "always", "--always", "-always":
			return true
		}
	}
	return false
}

// slashJail confines tools to the working directory. `/jail always` also drops
// any persisted unjail record for this directory, so the confinement survives a
// restart — otherwise a stored exception would silently undo it on the next
// launch, which is the sort of thing that makes a sandbox worthless.
func (i *Interactive) slashJail(_ context.Context, parts []string, _ string) bool {
	if i.cfg.Sandbox == nil {
		i.setStatusErr(i18n.T("sandbox not available in this build"))
		return false
	}
	i.cfg.Sandbox.Lock()
	msg := "jailed to " + i.cfg.CWD + " (tools cannot touch paths outside this directory)"

	if wantsAlways(parts) {
		if err := config.RejailPath(i.cfg.CWD); err != nil {
			i.setStatusErr(i18n.T("/jail: %s", err))
			return false
		}
		msg += " - and remembered"
	}
	i.setStatusOK(msg)
	return false
}

// slashUnjail lifts the sandbox. `/unjail always` records the directory so it
// starts unjailed from now on; plain `/unjail` is this session only, as before.
func (i *Interactive) slashUnjail(_ context.Context, parts []string, _ string) bool {
	if i.cfg.Sandbox == nil {
		i.setStatusErr(i18n.T("sandbox not available in this build"))
		return false
	}
	i.cfg.Sandbox.Unlock()

	if !wantsAlways(parts) {
		i.setStatusOK("unjailed (this session only - /unjail always to remember)")
		return false
	}
	if err := config.UnjailPath(i.cfg.CWD, false); err != nil {
		i.setStatusErr(i18n.T("/unjail: %s", err))
		return false
	}
	i.setStatusOK("unjailed " + i.cfg.CWD + " and remembered - tools may read and write outside it from now on (`/jail always` to undo)")
	return false
}

func (i *Interactive) slashTrust(_ context.Context, parts []string, _ string) bool {
	if i.cfg.TrustWorkspace == nil {
		i.setStatusErr(i18n.T("/trust unavailable: host did not wire TrustWorkspace"))
		return false
	}
	parent := len(parts) >= 2 && strings.EqualFold(strings.TrimSpace(parts[1]), "parent")
	if err := i.cfg.TrustWorkspace(parent); err != nil {
		i.setStatusErr(i18n.T("/trust: %s", err))
		return false
	}
	i.mu.Lock()
	i.cfg.Trusted = true
	cwd := i.cfg.CWD
	i.mu.Unlock()

	// No extension-gate flip here. It used to set the project-trust flag on the
	// TUI's own extension host, from when the TUI OWNED a manager; the carrier
	// resolves to the daemon session's manager, which TrustWorkspace above has
	// already flipped AND reloaded. Setting it again afterwards changed nothing
	// and read as a second, partial apply of an event that belongs in one place
	// (build.ApplyTrust).

	// The host did the whole job (the ctrlproto carriers reload extensions,
	// rebuild tools and re-render the prompt across every open session). Say so
	// — this used to fall through to the restart note below and send people
	// looking for a problem that had already been fixed.
	if i.cfg.TrustAppliesLive {
		i.setStatusOK(i18n.T("trusted %s — its project extensions, skills, and context are loaded now", cwd))
		return false
	}

	// No live re-apply here: it used to re-cd into the same directory to rebuild
	// against the now-trusted Resolve, through the same ChangeCWD host hook /cd
	// used — which no frontend has supplied since the direct driver's removal,
	// so this restart notice was already the only outcome. The daemon applies
	// trust live through control.trust (cfg.TrustAppliesLive, handled above);
	// that is the path a live re-apply belongs on if one is wanted again.
	i.setStatusOK("trusted " + cwd + " — restart terva to load its project extensions/skills/context")
	return false
}

// slashUntrust removes the current cwd from the trust store. Already-
// loaded project content stays for this session (a restart re-restricts);
// the change takes full effect on the next launch / re-cd.
func (i *Interactive) slashUntrust(_ context.Context, _ []string, _ string) bool {
	if i.cfg.UntrustWorkspace == nil {
		i.setStatusErr(i18n.T("/untrust unavailable: host did not wire UntrustWorkspace"))
		return false
	}
	if err := i.cfg.UntrustWorkspace(); err != nil {
		i.setStatusErr(i18n.T("/untrust: %s", err))
		return false
	}
	i.mu.Lock()
	i.cfg.Trusted = false
	cwd := i.cfg.CWD
	i.mu.Unlock()
	// See slashTrust: the carrier's manager is the daemon's, already flipped and
	// reloaded by UntrustWorkspace above.
	if i.cfg.TrustAppliesLive {
		// The ctrlproto carriers tear the project content back down on the spot,
		// so unlike the note below this is not a next-launch promise.
		i.setStatusOK(i18n.T("untrusted %s — its project extensions, skills, and context are unloaded", cwd))
		return false
	}
	i.setStatusOK("untrusted " + cwd + " — its project content will not load on the next launch")
	return false
}

// setStatusErr / setStatusOK set one status line (and
// clear the other) under the mutex — the small ceremony most handlers
// repeat.
func (i *Interactive) setStatusErr(msg string) {
	i.mu.Lock()
	i.statusErr = msg
	i.mu.Unlock()
}

func (i *Interactive) setStatusOK(msg string) {
	i.mu.Lock()
	i.statusOK = msg
	i.statusErr = ""
	i.mu.Unlock()
}
