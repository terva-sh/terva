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

	"terva.sh/terva/packages/tui"
)

// slashSpec declares one built-in slash command.
type slashSpec struct {
	name    string   // canonical name, with leading slash
	aliases []string // alternate spellings that dispatch identically
	desc    string   // autocomplete popup + /help description
	// group is the display section this command belongs to. Consecutive
	// registry entries sharing a group render under one divider row in
	// the popup (and one section label in /help). Empty = ungrouped
	// (used for /help itself, which leads the list without a divider).
	group string
	// hint, when set, describes the argument a command accepts. It is the
	// text a non-TUI frontend (e.g. the ACP slash catalog) shows in the
	// command's input field before anything is typed. Empty for commands
	// that take no argument.
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

// Display groups, in the order they appear in the popup and /help.
// The names double as the divider/section labels the user sees.
const (
	groupSession = "session"
	groupContext = "context & skills"
	groupModel   = "model & account"
	groupSafety  = "permissions & trust"
	groupAgents  = "agents & integrations"
	groupSystem  = "system"
)

// slashRegistry is the table, in popup/help display order. Assigned
// in init() rather than a var initializer: handler bodies may
// (transitively) reference the registry — /help renders the catalog,
// dispatch helpers look commands up — and a var initializer would
// make that an initialization cycle.
var slashRegistry []slashSpec

func init() {
	// Display order: /help leads ungrouped, then commands cluster into
	// labelled groups (rendered as divider rows in the popup and section
	// labels in /help), ordered roughly by how often a session reaches
	// for them: conversation management first, context/capability next,
	// model + account, safety, integrations, and system verbs last.
	slashRegistry = []slashSpec{
		{name: "/help", desc: "show key bindings and commands", run: (*Interactive).slashHelp},

		{name: "/new", group: groupSession, desc: "start a fresh session (the current one stays on disk)", cancelsTurn: true,
			run: func(i *Interactive, _ context.Context, _ []string, _ string) bool {
				i.startNewSession()
				return false
			}},
		{name: "/sessions", group: groupSession, desc: "resume a previous session for this directory",
			run: func(i *Interactive, _ context.Context, _ []string, _ string) bool {
				i.sessionDialog.Open(i.cfg.TervaHome, i.cfg.CWD)
				return false
			}},
		{name: "/session", group: groupSession, desc: "export the current session to a .tervasession file, or import one", hint: "export | import [path]",
			run: func(i *Interactive, _ context.Context, parts []string, _ string) bool {
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
			}},
		{name: "/jump", group: groupSession, desc: "scroll the chat to a previous turn (or /jump <text>)", hint: "text to jump to (optional)",
			run: func(i *Interactive, _ context.Context, parts []string, _ string) bool {
				i.openJumpDialog(parts[1:])
				return false
			}},
		{name: "/compact", group: groupSession, desc: "summarize and replace the transcript to free up context", cancelsTurn: true,
			run: func(i *Interactive, ctx context.Context, _ []string, _ string) bool {
				i.runCompact(ctx, false)
				return false
			}},
		{name: "/clear", group: groupSession, desc: "clear the chat transcript", cancelsTurn: true,
			run: (*Interactive).slashClear},

		{name: "/study", group: groupContext, desc: "read every file in the cwd (or a passed file/dir) so the agent has full context", hint: "file or directory (optional)",
			run: (*Interactive).slashStudy},
		{name: "/btw", group: groupContext, desc: "side-chat that doesn't add to the main thread (saves tokens)",
			run: func(i *Interactive, _ context.Context, parts []string, _ string) bool {
				i.openBtwDialog(parts[1:])
				return false
			}},
		{name: "/skill", group: groupContext, desc: "prime your next request with a specific skill", hint: "name [request]",
			run: (*Interactive).slashSkill},
		{name: "/skills", group: groupContext, desc: "list discovered skills (SKILL.md files)",
			run: func(i *Interactive, _ context.Context, _ []string, _ string) bool {
				i.openSkillsDialog()
				return false
			}},
		{name: "/context", group: groupContext, desc: "context breakdown (token sizes) + what extensions inject",
			run: func(i *Interactive, _ context.Context, _ []string, _ string) bool {
				i.slashContext()
				return false
			}},
		{name: "/lore", group: groupContext, desc: "list this run's active lore (keyed-context) entries",
			run: func(i *Interactive, _ context.Context, _ []string, _ string) bool {
				i.slashLore()
				return false
			}},

		{name: "/model", group: groupModel, desc: "pick a model (or /model <id>)", hint: "model id (optional)", cancelsTurn: true,
			run: func(i *Interactive, _ context.Context, parts []string, _ string) bool {
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
			}},
		{name: "/login", group: groupModel, desc: "log in via api key or subscription", cancelsTurn: true,
			run: func(i *Interactive, _ context.Context, _ []string, _ string) bool {
				i.dialog.Open(i.cfg.TervaHome)
				return false
			}},
		{name: "/logout", group: groupModel, desc: "clear a provider's credentials", hint: "provider (anthropic | openai | all)", cancelsTurn: true,
			run: func(i *Interactive, _ context.Context, parts []string, _ string) bool {
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
			}},
		{name: "/usage", group: groupModel, desc: "subscription usage limits and reset windows",
			run: func(i *Interactive, _ context.Context, _ []string, _ string) bool {
				i.openUsageDialog()
				return false
			}},

		{name: "/permissions", group: groupSafety, aliases: []string{"/perms"},
			desc: "show the approval mode, permission rules, and session grants",
			run: func(i *Interactive, _ context.Context, _ []string, _ string) bool {
				i.openPermissionsDialog()
				return false
			}},
		{name: "/jail", group: groupSafety, desc: "confine tools to the current directory",
			run: func(i *Interactive, _ context.Context, _ []string, _ string) bool {
				if i.cfg.Sandbox == nil {
					i.setStatusErr("sandbox not available in this build")
					return false
				}
				i.cfg.Sandbox.Lock()
				i.setStatusOK("jailed to " + i.cfg.CWD + " (tools cannot touch paths outside this directory)")
				return false
			}},
		{name: "/unjail", group: groupSafety, desc: "allow tools to touch paths outside this directory",
			run: func(i *Interactive, _ context.Context, _ []string, _ string) bool {
				if i.cfg.Sandbox == nil {
					i.setStatusErr("sandbox not available in this build")
					return false
				}
				i.cfg.Sandbox.Unlock()
				i.setStatusOK("unjailed")
				return false
			}},
		{name: "/trust", group: groupSafety, desc: "trust this directory so its project extensions/skills/context load (/trust parent for descendants too)",
			hint: "parent (optional)", cancelsTurn: true,
			run: (*Interactive).slashTrust},
		{name: "/untrust", group: groupSafety, desc: "remove this directory from the trust list (its project content stops loading)",
			cancelsTurn: true,
			run:         (*Interactive).slashUntrust},

		{name: "/swarm", group: groupAgents, desc: "supervise background agents that share this working directory",
			run: func(i *Interactive, ctx context.Context, parts []string, _ string) bool {
				i.runSwarm(ctx, parts[1:])
				return false
			}},
		{name: "/extensions", group: groupAgents, aliases: []string{"/ext"}, desc: "list installed extensions; enable/disable globally or per-project",
			run: func(i *Interactive, _ context.Context, _ []string, _ string) bool {
				i.openExtensionsDialog()
				return false
			}},
		{name: "/reload-ext", group: groupAgents, desc: "hot-reload all extensions (re-read manifests and respawn)", cancelsTurn: true,
			run: func(i *Interactive, ctx context.Context, _ []string, _ string) bool {
				i.runReloadExt(ctx)
				return false
			}},
		{name: "/mcp", group: groupAgents, desc: "list MCP servers; enable/disable globally or per-project",
			run: func(i *Interactive, _ context.Context, _ []string, _ string) bool {
				i.openMCPDialog()
				return false
			}},
		{name: "/connect", group: groupAgents, aliases: []string{"/telegram", "/tg"},
			desc: "connect, disconnect, or show status of a chat bridge (compiled-in or connector extension)", hint: "connect [name] | disconnect | status",
			run: func(i *Interactive, _ context.Context, parts []string, _ string) bool {
				if len(parts) >= 2 {
					i.doConnector(strings.Join(parts[1:], " "))
					return false
				}
				i.openConnectDialog()
				return false
			}},

		{name: "/settings", group: groupSystem, desc: "open settings",
			run: func(i *Interactive, _ context.Context, _ []string, _ string) bool {
				i.openSettingsDialog()
				return false
			}},
		{name: "/paste", group: groupSystem, desc: "paste an image from the system clipboard into the prompt",
			run: func(i *Interactive, _ context.Context, _ []string, _ string) bool {
				i.pasteClipboard()
				return false
			}},
		{name: "/migrate", group: groupSystem, desc: "move your zot data dir to the terva location", cancelsTurn: true, // rename:keep
			run: func(i *Interactive, _ context.Context, _ []string, _ string) bool {
				i.openMigrateDialog()
				return false
			}},
		{name: "/exit", group: groupSystem, desc: "exit terva",
			run: func(*Interactive, context.Context, []string, string) bool { return true }},
		{name: "/cd", hidden: true, cancelsTurn: true, run: (*Interactive).slashCD},
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
			out = append(out, slashCommand{Header: true, Name: s.group})
		}
		prevGroup = s.group
		out = append(out, slashCommand{Name: s.name, Desc: s.desc})
	}
	return out
}

// SlashCommandInfo is the frontend-agnostic view of a built-in slash
// command: its canonical name, its one-line description, and an optional
// argument hint. It carries no handler and no reference to *Interactive,
// so a non-TUI frontend (the ACP run mode advertising a command catalog)
// can consume the registry without dragging in the interactive struct.
type SlashCommandInfo struct {
	Name string // canonical name, with leading "/"
	Desc string // one-line description
	Hint string // argument hint, empty when the command takes no argument
}

// BuiltinSlashCommands returns the advertisable built-in slash commands in
// registry (display) order: every non-hidden command, with its description
// and argument hint. It is the exported, decoupled accessor that lets a
// non-TUI frontend publish a slash-command catalog — the ACP
// available_commands_update is built from a curated subset of this list.
// Hidden internal verbs (e.g. /cd) are excluded, exactly as the
// autocomplete catalog excludes them.
func BuiltinSlashCommands() []SlashCommandInfo {
	out := make([]SlashCommandInfo, 0, len(slashRegistry))
	for _, s := range slashRegistry {
		if s.hidden {
			continue
		}
		out = append(out, SlashCommandInfo{Name: s.name, Desc: s.desc, Hint: s.hint})
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
	if ag := i.turns.Agent(); ag != nil {
		ag.SetMessages(nil)
	}
	i.resetLoreFired()
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
	i.extNotes = nil
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
func (i *Interactive) slashCD(_ context.Context, parts []string, raw string) bool {
	if i.cfg.ChangeCWD == nil {
		i.setStatusErr("/cd unavailable: host did not wire ChangeCWD")
		return false
	}
	path := strings.TrimSpace(strings.TrimPrefix(raw, parts[0]))
	if path == "" {
		i.setStatusErr("/cd: missing path")
		return false
	}
	if err := i.cfg.ChangeCWD(path); err != nil {
		i.mu.Lock()
		i.statusErr = "/cd: " + err.Error()
		i.statusOK = ""
		i.mu.Unlock()
		return false
	}
	// ChangeCWD has already updated i.cfg.CWD and swapped the
	// agent + session. Reset transient TUI state so the new
	// session opens clean.
	i.mu.Lock()
	i.toolCalls = map[string]*tui.ToolCallView{}
	i.toolOrder = nil
	i.turns.ResetGates()
	i.helpBlock = nil
	i.parkedTurn = 0
	i.statusOK = "cwd " + i.cfg.CWD
	i.statusErr = ""
	i.mu.Unlock()
	i.fileSuggest.Reset()
	i.fileSuggest.SetCWD(i.cfg.CWD)
	i.invalidate()
	return false
}

// slashTrust persists Workspace Trust for the current cwd (with `parent`
// it also trusts descendants), then re-applies project content for the
// session by re-cd-ing into the same directory — which rebuilds the
// agent with the now-trusted Resolve (project extensions/skills/context
// load). If the host didn't wire a rebuild path, it persists and tells
// the user a restart will apply it. See docs/plans/workspace-trust.md.
func (i *Interactive) slashTrust(_ context.Context, parts []string, _ string) bool {
	if i.cfg.TrustWorkspace == nil {
		i.setStatusErr("/trust unavailable: host did not wire TrustWorkspace")
		return false
	}
	parent := len(parts) >= 2 && strings.EqualFold(strings.TrimSpace(parts[1]), "parent")
	if err := i.cfg.TrustWorkspace(parent); err != nil {
		i.setStatusErr("/trust: " + err.Error())
		return false
	}
	i.mu.Lock()
	i.cfg.Trusted = true
	cwd := i.cfg.CWD
	i.mu.Unlock()

	// Let the extension manager search the now-trusted project dir on its
	// next (re)discovery, so /reload-ext picks up project extensions.
	if i.cfg.Extensions != nil {
		i.cfg.Extensions.SetProjectTrusted(true)
	}

	// Live re-apply: re-cd into the same dir rebuilds the agent with the
	// now-trusted Resolve (project skills + context load immediately).
	// Project EXTENSIONS need a /reload-ext (or restart) to spawn — the
	// extension subprocesses aren't re-discovered by an agent rebuild.
	// ChangeCWD already swaps agent + session and resets transient state.
	if i.cfg.ChangeCWD != nil {
		if err := i.cfg.ChangeCWD(cwd); err != nil {
			i.setStatusOK("trusted " + cwd + " — restart terva to load its project extensions/skills/context")
			return false
		}
		i.mu.Lock()
		i.toolCalls = map[string]*tui.ToolCallView{}
		i.toolOrder = nil
		i.turns.ResetGates()
		i.helpBlock = nil
		i.parkedTurn = 0
		i.statusOK = "trusted " + i.cfg.CWD + " — project skills/context loaded (/reload-ext for project extensions)"
		i.statusErr = ""
		i.mu.Unlock()
		i.fileSuggest.Reset()
		i.fileSuggest.SetCWD(i.cfg.CWD)
		i.invalidate()
		return false
	}
	i.setStatusOK("trusted " + cwd + " — restart terva to load its project extensions/skills/context")
	return false
}

// slashUntrust removes the current cwd from the trust store. Already-
// loaded project content stays for this session (a restart re-restricts);
// the change takes full effect on the next launch / re-cd.
func (i *Interactive) slashUntrust(_ context.Context, _ []string, _ string) bool {
	if i.cfg.UntrustWorkspace == nil {
		i.setStatusErr("/untrust unavailable: host did not wire UntrustWorkspace")
		return false
	}
	if err := i.cfg.UntrustWorkspace(); err != nil {
		i.setStatusErr("/untrust: " + err.Error())
		return false
	}
	i.mu.Lock()
	i.cfg.Trusted = false
	cwd := i.cfg.CWD
	i.mu.Unlock()
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
