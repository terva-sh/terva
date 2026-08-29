// Package slash declares the built-in slash-command catalog: pure
// metadata — canonical names, aliases, descriptions, argument hints,
// display groups, and dispatch flags — with no handlers and no UI
// dependencies. It is the neutral source of truth every command
// surface consumes: the TUI binds handlers to it by name
// (modes/slash_registry.go), the ACP run mode advertises a curated
// subset of Builtin(), and a future web/ctrlproto palette can do the
// same without dragging in interactive-mode code.
//
// Extracted from packages/agent/modes to retire the tree's one
// wrong-direction import edge (acp → modes), independently flagged by
// the post-v0.116 audit and the 2026-07-06 terva-driven review.
//
// Adding a command: add a Spec here at the right display position, and
// bind its handler in the modes handler map — the modes init check
// fails loudly on a spec without a handler (or a handler without a
// spec), so the two tables cannot drift.
package slash

import "terva.sh/terva/packages/i18n"

// Spec declares one built-in slash command's metadata.
type Spec struct {
	// Name is the canonical name, with leading slash.
	Name string
	// Aliases are alternate spellings that dispatch identically.
	Aliases []string
	// Desc is the autocomplete popup + /help description (English;
	// consumers translate at render time via i18n.T).
	Desc string
	// Group is the display section this command belongs to. Consecutive
	// registry entries sharing a group render under one divider row in
	// the popup (and one section label in /help). Empty = ungrouped
	// (used for /help itself, which leads the list without a divider).
	Group string
	// Hint, when set, describes the argument a command accepts. It is the
	// text a non-TUI frontend (e.g. the ACP slash catalog) shows in the
	// command's input field before anything is typed. Empty for commands
	// that take no argument.
	Hint string
	// Hidden commands dispatch but stay out of the popup, /help, and
	// Builtin() (internal verbs like /cd, driven by extensions).
	Hidden bool
	// CancelsTurn marks commands that mutate the transcript or
	// replace the agent: dispatch cancels the active turn and waits
	// for idle before running them, so they don't race a streaming
	// response. Safe commands run immediately.
	CancelsTurn bool
}

// Display groups, in the order they appear in the popup and /help. The
// names double as the divider/section labels the user sees, so they are
// marked with i18n.M (extractor-visible, English value) and translated at
// render time — never frozen at init.
var (
	groupSession = i18n.M("session")
	groupContext = i18n.M("context & skills")
	groupModel   = i18n.M("model & account")
	groupSafety  = i18n.M("permissions & trust")
	groupAgents  = i18n.M("agents & integrations")
	groupSystem  = i18n.M("system")
)

// registry is the catalog in popup/help display order: /help leads
// ungrouped, then commands cluster into labelled groups, ordered
// roughly by how often a session reaches for them: conversation
// management first, context/capability next, model + account, safety,
// integrations, and system verbs last.
var registry = []Spec{
	{Name: "/help", Desc: i18n.M("show key bindings and commands")},

	{Name: "/new", Group: groupSession, Desc: i18n.M("start a fresh session (the current one stays on disk)"), CancelsTurn: true},
	{Name: "/sessions", Group: groupSession, Desc: i18n.M("resume a previous session for this directory")},
	{Name: "/session", Group: groupSession, Desc: i18n.M("export the current session to a .tervasession file, or import one"), Hint: "export | import [path]"},
	{Name: "/jump", Group: groupSession, Desc: i18n.M("scroll the chat to a previous turn (or /jump <text>)"), Hint: "text to jump to (optional)"},
	{Name: "/copy", Group: groupSession, Desc: i18n.M("copy the last reply to the clipboard, as written (or /copy code for its last code block)"), Hint: "code (optional)"},
	{Name: "/compact", Group: groupSession, Desc: i18n.M("summarize and replace the transcript to free up context"), CancelsTurn: true},
	{Name: "/clear", Group: groupSession, Desc: i18n.M("clear the chat transcript"), CancelsTurn: true},

	{Name: "/study", Group: groupContext, Desc: "read every file in the cwd (or a passed file/dir) so the agent has full context", Hint: "file or directory (optional)"},
	{Name: "/btw", Group: groupContext, Desc: "side-chat that doesn't add to the main thread (saves tokens)"},
	// Beside /btw because it is the same class of thing: one model call that
	// leaves the transcript alone. Where /btw asks the agent a question off the
	// record, this asks it what YOU might say next, and the answer lands in the
	// composer rather than on screen.
	{Name: "/nextstep", Group: groupContext, Desc: "ask for the smallest next step and offer it in the composer (tab accepts; nothing is sent)"},
	{Name: "/skill", Group: groupContext, Desc: "prime your next request with a specific skill", Hint: "name [request]"},
	{Name: "/skills", Group: groupContext, Desc: "list discovered skills (SKILL.md files)"},
	{Name: "/reload-skills", Group: groupContext,
		Desc:        "re-scan SKILL.md files so one written this session is usable now (rebuilds the prompt only if the list changed)",
		CancelsTurn: true},
	{Name: "/context", Group: groupContext, Desc: "context breakdown (token sizes) + what extensions inject"},
	{Name: "/reveal", Group: groupContext, Desc: "show the conversation from before a /clear (scrolling up already walks back through compactions, but stops there)"},
	{Name: "/lore", Group: groupContext, Desc: "list this run's active lore (keyed-context) entries"},
	{Name: "/memory", Group: groupContext, Desc: "the agent's durable memory — facts it carries into future sessions; prune or clear them"},
	{Name: "/tasks", Group: groupContext, Desc: "show the agent's task list (the built-in task tracker)"},
	{Name: "/worktree", Group: groupContext, Desc: "managed git worktrees: list, cd into one, or review the merge-back overview"},
	{Name: "/shared", Group: groupContext, Desc: "files the agent handed you this session: copy the path, open one, or save a copy here"},

	{Name: "/model", Group: groupModel, Desc: "pick a model (or /model <id>)", Hint: "model id (optional)", CancelsTurn: true},
	// Canonical spelling is "thinking": that is the word every surface a user
	// meets already uses (the status bar, /settings, this command's own
	// description). "/reasoning" was the odd one out and stays as an alias —
	// it is what the muscle memory and every older doc say, and renaming a
	// command out from under someone is not worth a tidier table.
	{Name: "/thinking", Group: groupModel, Aliases: []string{"/reasoning", "/think"},
		Desc: "set this session's thinking depth (or /thinking <level>; \"inherit\" follows the global)",
		Hint: "off | minimum | low | medium | high | maximum | max | inherit (optional)"},
	{Name: "/login", Group: groupModel, Desc: "log in via api key or subscription", CancelsTurn: true},
	{Name: "/logout", Group: groupModel, Desc: "clear a provider's credentials", Hint: "provider (anthropic | openai | all)", CancelsTurn: true},
	{Name: "/usage", Group: groupModel, Desc: "subscription usage limits and reset windows"},
	{Name: "/resets", Group: groupModel, Desc: "list and redeem banked usage-reset credits (codex subscription)"},

	{Name: "/permissions", Group: groupSafety, Aliases: []string{"/perms"},
		Desc: "show the approval mode, permission rules, and session grants"},
	{Name: "/jail", Group: groupSafety, Desc: "confine tools to the current directory (/jail always to also forget a saved unjail)"},
	{Name: "/unjail", Group: groupSafety, Desc: "allow tools to touch paths outside this directory (/unjail always to remember it)"},
	{Name: "/trust", Group: groupSafety, Desc: "trust this directory so its project extensions/skills/context load (/trust parent for descendants too)",
		Hint: "parent (optional)", CancelsTurn: true},
	{Name: "/untrust", Group: groupSafety, Desc: "remove this directory from the trust list (its project content stops loading)",
		CancelsTurn: true},

	{Name: "/swarm", Group: groupAgents, Desc: "supervise background agents that share this working directory"},
	{Name: "/workflows", Group: groupAgents, Aliases: []string{"/workflow"}, Desc: "inspect workflow runs: status, cost, the script as it ran, and every journaled result"},
	{Name: "/extensions", Group: groupAgents, Aliases: []string{"/ext"}, Desc: "list installed extensions; enable/disable globally or per-project"},
	{Name: "/reload-ext", Group: groupAgents, Desc: "hot-reload all extensions (re-read manifests and respawn)", CancelsTurn: true},
	{Name: "/mcp", Group: groupAgents, Desc: "list MCP servers; enable/disable globally or per-project"},
	{Name: "/connect", Group: groupAgents, Aliases: []string{"/telegram", "/tg"},
		Desc: "connect, disconnect, or show status of a chat bridge (compiled-in or connector extension)", Hint: "connect [name] | disconnect | status"},

	{Name: "/status", Group: groupSystem, Desc: i18n.M("harness status: version, uptime, model, session, context usage")},
	{Name: "/restart", Group: groupSystem, Desc: i18n.M("re-exec terva into the currently-installed binary and resume this session (needs --allow-restart)"), CancelsTurn: true},
	{Name: "/settings", Group: groupSystem, Desc: "open settings"},
	{Name: "/paste", Group: groupSystem, Desc: "paste an image from the system clipboard into the prompt"},
	{Name: "/migrate", Group: groupSystem, Desc: "move your zot data dir to the terva location", CancelsTurn: true}, // rename:keep
	{Name: "/exit", Group: groupSystem, Desc: "exit terva"},
	{Name: "/cd", Hidden: true, CancelsTurn: true},
}

// Registry returns the full catalog in display order, hidden entries
// included. The slice is a copy; callers may not mutate the catalog.
func Registry() []Spec {
	out := make([]Spec, len(registry))
	copy(out, registry)
	return out
}

// Info is the frontend-agnostic advertisable view of a built-in slash
// command: its canonical name, its one-line (translated) description,
// and an optional argument hint. It carries no handler, so any command
// surface can publish a palette from it.
type Info struct {
	Name string // canonical name, with leading "/"
	Desc string // one-line description, translated
	Hint string // argument hint, empty when the command takes no argument
}

// Builtin returns the advertisable built-in slash commands in registry
// (display) order: every non-hidden command, with its description
// translated and its argument hint. Hidden internal verbs (e.g. /cd)
// are excluded, exactly as the TUI autocomplete catalog excludes them.
func Builtin() []Info {
	out := make([]Info, 0, len(registry))
	for _, s := range registry {
		if s.Hidden {
			continue
		}
		out = append(out, Info{Name: s.Name, Desc: i18n.T(s.Desc), Hint: s.Hint})
	}
	return out
}
