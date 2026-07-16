package agent

import (
	"fmt"
	"os"
	"strings"

	"golang.org/x/term"
	"terva.sh/terva/packages/i18n"
	"terva.sh/terva/packages/tui"
)

// PrintHelp writes the help text to stderr. When stderr is a TTY it
// uses the same palette as terva's TUI; when redirected it falls back to
// plain text with no ANSI escapes.
func PrintHelp(version string) {
	th := tui.Dark
	fd := int(os.Stderr.Fd())
	useColor := term.IsTerminal(fd)
	style := func(c int, s string) string {
		if !useColor {
			return s
		}
		return th.FG256(c, s)
	}
	assistant := func(s string) string { return style(th.Assistant, s) }
	muted := func(s string) string { return style(th.Muted, s) }
	fg := func(s string) string { return style(th.FG, s) }
	width := 96
	if useColor {
		if w, _, err := term.GetSize(fd); err == nil && w > 20 {
			width = w
		}
	}
	ruleWidth := width
	if ruleWidth < 40 {
		ruleWidth = 40
	}
	rule := strings.Repeat("─", ruleWidth)
	if useColor {
		rule = muted(rule)
	}
	leftW := 34
	if width >= 120 {
		leftW = 40
	}
	if width >= 140 {
		leftW = 46
	}
	type row struct{ left, right string }
	section := func(title string, rows ...row) {
		fmt.Fprintln(os.Stderr)
		fmt.Fprintln(os.Stderr, assistant(title))
		fmt.Fprintln(os.Stderr, rule)
		narrow := width < 100
		for _, r := range rows {
			if narrow {
				fmt.Fprintf(os.Stderr, "  %s\n", fg(r.left))
				fmt.Fprintf(os.Stderr, "    %s\n", muted(r.right))
				fmt.Fprintln(os.Stderr)
				continue
			}
			left := r.left
			if len([]rune(left)) < leftW {
				left += strings.Repeat(" ", leftW-len([]rune(left)))
			}
			fmt.Fprintf(os.Stderr, "  %s    %s\n", fg(left), muted(r.right))
		}
	}

	fmt.Fprintln(os.Stderr)
	// The help / usage screen interrogates the harness, not a session, so it
	// speaks as terva (the vessel) — not as a Persona (the mind), which greets
	// you in the interactive welcome banner instead.
	greeting := "i'm terva. " + th.Greeting()
	var headline string
	if useColor {
		headline = th.AccentBar(th.Assistant) + assistant(tui.Bold(greeting))
	} else {
		headline = greeting
	}
	fmt.Fprintln(os.Stderr, headline)
	fmt.Fprintln(os.Stderr, muted(i18n.T("ask anything, or type /help inside the tui to see commands.")))
	fmt.Fprintf(os.Stderr, "%s %s\n", muted(i18n.T("version:")), fg(version))

	section(i18n.T("modes"),
		row{"terva", i18n.T("interactive tui")},
		row{"terva \"prompt\"", i18n.T("interactive, pre-filled prompt")},
		row{"terva -p \"prompt\"", i18n.T("print final text, exit")},
		row{"terva --json \"prompt\"", i18n.T("newline-delimited json events, exit")},
		row{"terva rpc", i18n.T("json-rpc loop on stdin/stdout (see docs/rpc.md)")},
	)
	// Subcommands are listed one line each; each has its own detailed screen —
	// run `terva <command> --help`. This keeps the top-level help scannable as
	// the surface grew (the per-command detail used to be inlined here).
	section(i18n.T("commands — run `terva <command> --help` for detail"),
		row{"terva ext ...", i18n.T("install and manage extensions (also --ext PATH for one run)")},
		row{"terva models ...", i18n.T("scaffold and edit custom model definitions")},
		row{"terva locale ...", i18n.T("ui languages & translations (or TERVA_LANG=<tag> to run in one)")},
		row{"terva persona ...", i18n.T("manage personas (agent identities)")},
		row{"terva lore ...", i18n.T("manage lore (keyed-context) entries")},
		row{"terva card ...", i18n.T("inspect character cards")},
		row{"terva bot ...", i18n.T("run a chat-bridge bot (telegram and others)")},
		row{"terva raati \"q\"", i18n.T("convene a three-unit deliberation panel; prints verdict + dissent")},
		row{"terva web", i18n.T("browser control panel (self-hosted web ui)")},
		row{"terva attach [URL]", i18n.T("run the TUI as a client of a running terva web daemon (default ws://127.0.0.1:8730/ws; --token / --token-file / TERVA_WEB_TOKEN for auth). Survives daemon restarts; quit and re-attach without disturbing the agent")},
		row{"terva trust / untrust", i18n.T("manage which directories load project extensions/skills/context")},
		row{"terva doctor", i18n.T("report effective privilege & deployment posture (uid, no-new-privs, sudo)")},
		row{"terva project ...", i18n.T("project-scoped agents (data + extensions pinned to a directory)")},
		row{"terva migrate", i18n.T("migrate a legacy install's data to the terva location")},
		row{"terva update", i18n.T("download and install the latest release")},
	)
	section(i18n.T("provider and model flags"),
		row{"--provider", i18n.T("provider to use (anthropic|openai|openai-codex|kimi|deepseek|google|ollama|openai-compatible)")},
		row{"--model ID", i18n.T("model id (see --list-models)")},
		row{"--api-key KEY", i18n.T("api key for this run (env / auth.json fallback)")},
		row{"--base-url URL", i18n.T("override provider api base url")},
		row{"--reasoning off|minimum|low|medium|high|maximum", i18n.T("set thinking level on supported models")},
		row{"--temperature N", i18n.T("sampling temperature, 0 to 2 (omit for provider default)")},
		row{"--insecure", i18n.T("skip TLS verification for the inference --base-url (openai-compatible/ollama only)")},
	)
	section(i18n.T("prompt and session flags"),
		row{"--system-prompt TEXT", i18n.T("replace the default system prompt")},
		row{"--append-system-prompt TEXT", i18n.T("append to the system prompt (repeatable)")},
		row{"--persona NAME|FILE", i18n.T("load a persona (built-in/on-disk name or .md path) as the identity")},
		row{"--card PATH", i18n.T("load a character card (.json/.png) as a chat/play identity (implies --chat)")},
		row{"--greeting N", i18n.T("with --card: pick the opening line (0 = first_mes, 1..N = alternate greetings)")},
		row{"--as NAME", i18n.T("what a card's {{user}} resolves to (defaults to the saved name, else \"User\")")},
		row{"--context-file PATH", i18n.T("inject a file's contents into the system prompt (repeatable)")},
		row{"--task PATH", i18n.T("preload the prompt from a file: interactive opens with it pre-filled (not sent), headless runs it")},
		row{"-c, --continue", i18n.T("continue the most recent session for this cwd")},
		row{"-r, --resume [ID]", i18n.T("open the session picker at startup, or resume session ID directly (also: terva attach -r)")},
		row{"--session PATH", i18n.T("resume a specific session file")},
		row{"--no-session", i18n.T("do not read or write a session file")},
	)
	section(i18n.T("workspace, tools, skills"),
		row{"--cwd PATH", i18n.T("treat PATH (an existing directory) as the working directory")},
		// building blocks — each turns off one capability, independently:
		row{"--no-workspace-tools", i18n.T("turn off all built-in base tools (read/write/edit/bash/grep/glob, status, ask, generate_image); keep extensions + MCP + skills (least-privilege for bots)")},
		row{"--ext PATH", i18n.T("load an extension from PATH for this run only (repeatable; bypasses config)")},
		row{"--no-ext / --no-extensions", i18n.T("turn off extension discovery for this run")},
		row{"--no-mcp", i18n.T("turn off MCP server startup for this run")},
		// narrowing variants — allowlists instead of all-off:
		row{"--extensions csv", i18n.T("only load the listed installed extensions (by name); --ext paths bypass, config disables still subtract")},
		row{"--mcp csv", i18n.T("only start the listed MCP servers (by name)")},
		// composite modes built from the blocks (+ an identity change):
		row{"--no-tools", i18n.T("all three blocks above together (and the skill tool) — no tools at all")},
		row{"--chat", i18n.T("no tools at all + a conversational, non-coding identity (pairs with --persona)")},
		row{"--play", i18n.T("extensions + MCP only (= --no-workspace-tools) + an embodied identity")},
		row{"--cast NAME=REF", i18n.T("declare an actor the director can voice (REF = persona name or card path); repeatable, implies --play. A trusted project's .terva/cast.json declares a cast too")},
		row{"--tools csv", i18n.T("only enable the listed (built-in) tools")},
		row{"--no-skill", i18n.T("skip all skill discovery for this run")},
		row{"--no-lore", i18n.T("skip all lore (keyed-context) discovery + injection for this run")},
		row{"--approval plan|ask|auto-edit|workspace|yolo", i18n.T("approval mode: plan = read-only only, ask = confirm everything, auto-edit = confirm non-edit tools, workspace = run built-ins + reads, confirm foreign side-effects (interactive default), yolo = run freely. See docs/permissions.md")},
		row{"--no-yolo", i18n.T("alias for --approval ask")},
		row{"--jail / --no-jail", i18n.T("force the sandbox on / off at startup (default: on for interactive, off for headless)")},
		row{"--trust", i18n.T("trust the cwd for this run only (load project extensions/skills/context; not persisted)")},
		row{"--project / --no-project", i18n.T("force project-scoped mode on/off (data in .terva/home, only project extensions; login+trust stay global)")},
	)
	section(i18n.T("misc"),
		row{"--allow-restart", i18n.T("enable Tier-1 self-restart: the agent (terva_restart) or the control plane can re-exec terva into the currently-installed binary; the TUI resumes its session, web clients reconnect")},
		row{"--swarm-worktrees", i18n.T("give each swarm sub-agent its own git worktree (needs the terva-git-worktree extension)")},
		row{"--max-steps N", i18n.T("agent loop iteration cap (default: unlimited)")},
		row{"--dump-prompt[=text|json|raw|sizes]", i18n.T("print the assembled prompt for the pending turn and exit (no model call); sizes shows per-section/per-tool byte+token weight")},
		row{"--list-models[=FILTER]", i18n.T("print known models and exit. FILTER: comma list of user|live|catalog|speculative, a tier threshold like live+ (that tier and above), or available (only providers your credentials can use right now)")},
		row{"-h, --help", i18n.T("show this help")},
		row{"-v, --version", i18n.T("show version info")},
	)
	fmt.Fprintln(os.Stderr)
	fmt.Fprintln(os.Stderr, assistant(i18n.T("see also: docs/extensions.md, docs/rpc.md, docs/skills.md")))
	fmt.Fprintln(os.Stderr)
}
