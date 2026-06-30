package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"golang.org/x/term"
	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/tui"
)

// Mode is the CLI run mode.
type Mode string

const (
	ModeInteractive Mode = "interactive"
	ModePrint       Mode = "print"
	ModeJSON        Mode = "json"
	ModeRPC         Mode = "rpc"
	// ModeACP is the Agent Client Protocol mode (editor↔agent JSON-RPC 2.0
	// on stdio — Zed and other ACP clients). Routed via the `terva acp`
	// subcommand and selected by --acp. The wire implementation is an
	// opt-in build (-tags terva_acp); the no-tag binary routes here too but
	// exits with "acp mode not built in".
	ModeACP Mode = "acp"
	// ModeSwarmAgent is the long-lived, headless daemon mode used by
	// swarm-spawned agents. The binary opens a unix-socket inbox at
	// the path provided by --swarm-agent, reads supervisor messages
	// off it ("user ...", "cancel", "shutdown"), runs each user turn
	// against a persistent session, and streams JSONL events on
	// stdout. See packages/agent/swarm/inbox.go for the wire protocol.
	ModeSwarmAgent Mode = "swarm-agent"
)

// Args holds parsed command-line options.
type Args struct {
	Mode     Mode
	Provider string
	Model    string
	APIKey   string

	BaseURL            string // override provider base URL (for tests/self-hosted)
	SystemPrompt       string
	AppendSystemPrompt []string
	// Persona selects the active persona (--persona): a built-in/on-disk name
	// or a path to a .md file. Empty resolves via persona.md / default_persona /
	// the embedded Mieli default.
	Persona     string
	Reasoning   string
	Temperature *float32

	// ContextFiles are paths passed via --context-file (repeatable). Each
	// file's contents are injected into the system prompt at startup, in
	// order, after any project/user .terva/config.json context_files. See
	// readStartupContextFiles.
	ContextFiles []string

	Continue bool
	Resume   bool
	Session  string
	NoSess   bool

	CWD     string
	NoTools bool
	// NoWorkspaceTools drops only the built-in workspace tools (read/write/
	// edit/bash/grep/glob/status/ask) while KEEPING extension + MCP + skill
	// tools and the normal identity. The least-privilege middle ground between
	// the default full agent and --no-tools: an agent (often a bot) that should
	// use its curated integrations but never reach the host filesystem/shell.
	NoWorkspaceTools bool
	Tools            []string
	// Experience is a meta-mode that reframes the harness away from coding:
	// "" (normal), "chat" (pure conversation — no tools), or "play" (act in a
	// simulated world — built-in coding tools off, extension/MCP tools kept).
	// Both drop the coding identity + conventions, skip AGENTS.md, and suppress
	// coding chrome. Set by --chat / --play.
	Experience string
	MaxSteps   int

	// Project / NoProject force project-scoped mode on / off for this run,
	// overriding the `project_scoped` field in .terva/config.json. In
	// project-scoped mode all data lives in a project-local home and only the
	// project's own extensions load; login + trust stay global. See
	// projectscope.go.
	Project   bool
	NoProject bool

	// Jail / NoJail force the sandbox on / off at startup. When
	// neither is set the default is on for an interactive session and
	// off for headless modes (see resolveJail). The TUI /jail and
	// /unjail commands still toggle it at runtime.
	Jail   bool
	NoJail bool

	// NoMCP skips MCP server startup for this run (config "mcp" key
	// is otherwise honored in every mode).
	NoMCP bool

	// Trust trusts the working directory for THIS invocation only —
	// project-local extensions / skills / context load as if the dir
	// were in the trust store, but nothing is persisted (the one-shot
	// operator analog of answering the TUI trust prompt). The headless
	// way to opt a single run into project content without a permanent
	// `terva trust`. See resolveTrust / docs/plans/workspace-trust.md.
	Trust bool

	// Exts is a list of directory paths the user passed via --ext.
	// Each must contain an extension.json. Loaded for one session
	// only; never persisted. Take precedence over installed exts of
	// the same name.
	Exts []string

	// NoExt disables extension discovery + spawn entirely for this
	// run. --ext PATH still works (explicit beats implicit) so you
	// can run "with only this one extension" via --no-ext --ext PATH.
	NoExt bool

	// Insecure skips TLS certificate verification for the inference
	// client ONLY (a self-signed --base-url endpoint). Gated to the
	// openai-compatible / ollama providers with an explicit --base-url;
	// auth, discovery, and every other provider keep normal verification.
	Insecure bool

	// ConnectorManifests are connector.json paths passed via
	// --connector-manifest (repeatable). Each loads ONE external chat
	// connector as a dev service for this invocation only — nothing
	// is discovered, nothing persists (the --ext precedent; see
	// docs/plans/chat-connectors.md, phase 4).
	ConnectorManifests []string

	// NoSkill disables ALL skill discovery for this run, including
	// the built-in skills compiled into the binary. The system
	// prompt loses its "Available skills" manifest and the `skill`
	// tool isn't registered. Useful for running terva without any
	// extra context biasing the model.
	NoSkill bool

	// WithSkills controls loading user-installed skills from
	// $TERVA_HOME/skills/, .terva/skills/, .claude/skills/, and
	// .agents/skills/. It defaults to true; --no-skill disables all
	// skill discovery, including built-ins.
	WithSkills bool

	// NoYolo turns on per-tool confirmation. Before each tool
	// invocation the TUI prompts the user with the tool name + args
	// and waits for an explicit yes/no. The user can also pick
	// "always for this tool this session" or "always for anything
	// this session" to stop being prompted again. Defaults off
	// (yolo mode): tools run without asking.
	//
	// No effect in -p / --json / rpc modes, which have no
	// interactive prompt. A warning is printed to stderr on startup
	// so scripts know the flag is ignored, but tools still run
	// freely so automated workflows keep working.
	//
	// NoYolo is the compatibility spelling of --approval ask; the
	// approval-mode spectrum supersedes it.
	NoYolo bool

	// Approval selects the approval mode: plan, ask, auto-edit, or
	// yolo. Empty means: ask if --no-yolo was given, else the user
	// config's approval default, else yolo. Resolution lives in
	// resolveApprovalMode (permissions.go).
	Approval string

	ListModels bool
	Help       bool
	Version    bool

	Prompt string // concatenated positional args

	// SwarmAgent is the inbox-socket path when this process is a
	// swarm-spawned agent. Empty in every other mode. Set by
	// --swarm-agent <path>; presence flips Mode to ModeSwarmAgent.
	SwarmAgent string

	// SwarmWorktrees is the tri-state override for per-agent swarm
	// worktree isolation. nil means "not set on the command line" (the
	// user config's swarm_worktrees decides); a non-nil value is an
	// explicit --swarm-worktrees (true) override that wins over config.
	// When effectively on, each swarm sub-agent leases its own git
	// worktree via the terva-git-worktree extension instead of sharing
	// the host's tree. Mirrors the bool-pointer config fields.
	SwarmWorktrees *bool
}

// ParseArgs parses the process arguments (excluding argv[0]).
func ParseArgs(in []string) (Args, error) {
	a := Args{Mode: ModeInteractive, MaxSteps: 0, WithSkills: true}
	positional := []string{}

	want := func(i *int, flag string) (string, error) {
		*i++
		if *i >= len(in) {
			return "", fmt.Errorf("%s requires a value", flag)
		}
		return in[*i], nil
	}

	for i := 0; i < len(in); i++ {
		arg := in[i]
		switch arg {
		case "-h", "--help":
			a.Help = true
		case "-v", "--version":
			a.Version = true
		case "-p", "--print":
			a.Mode = ModePrint
		case "--json":
			a.Mode = ModeJSON
		case "--rpc":
			a.Mode = ModeRPC
		case "--acp":
			a.Mode = ModeACP
		case "-c", "--continue":
			a.Continue = true
		case "-r", "--resume":
			a.Resume = true
		case "--no-session":
			a.NoSess = true
		case "--no-tools":
			a.NoTools = true
		case "--no-workspace-tools":
			a.NoWorkspaceTools = true
		case "--chat":
			if a.Experience == ExperiencePlay {
				return a, fmt.Errorf("--chat and --play are mutually exclusive")
			}
			a.Experience = ExperienceChat
		case "--play":
			if a.Experience == ExperienceChat {
				return a, fmt.Errorf("--chat and --play are mutually exclusive")
			}
			a.Experience = ExperiencePlay
		case "--project":
			a.Project = true
		case "--no-project":
			a.NoProject = true
		case "--jail":
			a.Jail = true
		case "--no-jail":
			a.NoJail = true
		case "--list-models":
			a.ListModels = true
		case "--experimental-oauth":
			// deprecated: subscription login is always available.
			// accepted silently for backwards compatibility.
		case "--provider":
			v, err := want(&i, arg)
			if err != nil {
				return a, err
			}
			a.Provider = v
		case "--model":
			v, err := want(&i, arg)
			if err != nil {
				return a, err
			}
			a.Model = v
		case "--api-key":
			v, err := want(&i, arg)
			if err != nil {
				return a, err
			}
			a.APIKey = v
		case "--base-url":
			v, err := want(&i, arg)
			if err != nil {
				return a, err
			}
			a.BaseURL = v
		case "--system-prompt":
			v, err := want(&i, arg)
			if err != nil {
				return a, err
			}
			a.SystemPrompt = v
		case "--append-system-prompt":
			v, err := want(&i, arg)
			if err != nil {
				return a, err
			}
			a.AppendSystemPrompt = append(a.AppendSystemPrompt, v)
		case "--persona":
			v, err := want(&i, arg)
			if err != nil {
				return a, err
			}
			a.Persona = v
		case "--context-file":
			v, err := want(&i, arg)
			if err != nil {
				return a, err
			}
			// Repeatable; each value is a file whose contents are injected
			// into the system prompt in order. Relative paths resolve
			// against args.CWD later, in readStartupContextFiles.
			a.ContextFiles = append(a.ContextFiles, v)
		case "--ext", "-e":
			v, err := want(&i, arg)
			if err != nil {
				return a, err
			}
			// Repeatable; each value is a directory containing an
			// extension.json. Resolved to absolute later so paths like
			// "." survive a later cwd change.
			a.Exts = append(a.Exts, v)
		case "--no-ext", "--no-extensions":
			a.NoExt = true
		case "--insecure":
			a.Insecure = true
		case "--no-mcp":
			a.NoMCP = true
		case "--trust":
			// One-shot: trust the cwd for this run only, NOT persisted.
			// Use `terva trust` to persist. See resolveTrust.
			a.Trust = true
		case "--connector-manifest":
			v, err := want(&i, arg)
			if err != nil {
				return a, err
			}
			a.ConnectorManifests = append(a.ConnectorManifests, v)
		case "--no-skill", "--no-skills":
			a.NoSkill = true
		case "--with-skills", "--with-skill":
			// Deprecated no-op: user skills are loaded by default.
			a.WithSkills = true
		case "--no-yolo":
			a.NoYolo = true
		case "--approval":
			v, err := want(&i, arg)
			if err != nil {
				return a, err
			}
			if _, perr := core.ParseApprovalMode(strings.ToLower(v)); perr != nil {
				return a, perr
			}
			a.Approval = strings.ToLower(v)
		case "--reasoning":
			v, err := want(&i, arg)
			if err != nil {
				return a, err
			}
			switch strings.ToLower(v) {
			case "", "off", "minimum", "minimal", "low", "medium", "high", "maximum", "max":
				a.Reasoning = strings.ToLower(v)
			default:
				return a, fmt.Errorf("--reasoning must be off|minimum|low|medium|high|maximum")
			}
		case "--temperature":
			v, err := want(&i, arg)
			if err != nil {
				return a, err
			}
			f, err := strconv.ParseFloat(v, 32)
			if err != nil || f < 0 || f > 2 {
				return a, fmt.Errorf("--temperature must be a number between 0 and 2")
			}
			t := float32(f)
			a.Temperature = &t
		case "--session":
			v, err := want(&i, arg)
			if err != nil {
				return a, err
			}
			a.Session = v
		case "--cwd":
			v, err := want(&i, arg)
			if err != nil {
				return a, err
			}
			a.CWD = v
		case "--swarm-agent":
			v, err := want(&i, arg)
			if err != nil {
				return a, err
			}
			a.SwarmAgent = v
			a.Mode = ModeSwarmAgent
		case "--swarm-worktrees":
			// Explicit opt-in for per-agent swarm worktree isolation;
			// overrides the user config's swarm_worktrees for this run.
			on := true
			a.SwarmWorktrees = &on
		case "--tools":
			v, err := want(&i, arg)
			if err != nil {
				return a, err
			}
			for _, t := range strings.Split(v, ",") {
				t = strings.TrimSpace(t)
				if t != "" {
					a.Tools = append(a.Tools, t)
				}
			}
		case "--max-steps":
			v, err := want(&i, arg)
			if err != nil {
				return a, err
			}
			var n int
			if _, err := fmt.Sscanf(v, "%d", &n); err != nil || n <= 0 {
				return a, fmt.Errorf("--max-steps must be a positive integer")
			}
			a.MaxSteps = n
		default:
			if strings.HasPrefix(arg, "-") && arg != "-" {
				return a, fmt.Errorf("unknown flag %q", arg)
			}
			positional = append(positional, arg)
		}
	}

	if len(positional) > 0 {
		a.Prompt = strings.Join(positional, " ")
	}

	if a.CWD == "" {
		a.CWD, _ = os.Getwd()
	} else {
		// A user-supplied --cwd is resolved to an absolute path and
		// verified to be an existing directory up front. Without this,
		// a typo'd or relative workspace fails lazily and confusingly
		// later (bash's cmd.Dir errors per command, writes fail when
		// joining a missing parent, the system prompt shows a relative
		// path, and per-cwd session keys diverge from the absolute form).
		abs, err := filepath.Abs(a.CWD)
		if err != nil {
			return a, fmt.Errorf("--cwd %q: %w", a.CWD, err)
		}
		info, err := os.Stat(abs)
		if err != nil {
			return a, fmt.Errorf("--cwd %q: %w", a.CWD, err)
		}
		if !info.IsDir() {
			return a, fmt.Errorf("--cwd %q is not a directory", a.CWD)
		}
		a.CWD = abs
	}
	return a, nil
}

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
	// speaks as terva (the vessel) — not as a persona (the mind), which greets
	// you in the interactive welcome banner instead.
	greeting := "i'm terva. " + th.Greeting()
	var headline string
	if useColor {
		headline = th.AccentBar(th.Assistant) + assistant(tui.Bold(greeting))
	} else {
		headline = greeting
	}
	fmt.Fprintln(os.Stderr, headline)
	fmt.Fprintln(os.Stderr, muted("ask anything, or type /help inside the tui to see commands."))
	fmt.Fprintf(os.Stderr, "%s %s\n", muted("version:"), fg(version))

	section("modes",
		row{"terva", "interactive tui"},
		row{"terva \"prompt\"", "interactive, pre-filled prompt"},
		row{"terva -p \"prompt\"", "print final text, exit"},
		row{"terva --json \"prompt\"", "newline-delimited json events, exit"},
		row{"terva rpc", "json-rpc loop on stdin/stdout (see docs/rpc.md)"},
	)
	section("extensions",
		row{"terva ext list", "list installed extensions"},
		row{"terva ext install <path|url>", "install into $TERVA_HOME/extensions/"},
		row{"terva --ext ./path/to/ext", "load an extension for this run only"},
		row{"terva ext help", "show all extension subcommands"},
	)
	section("custom models",
		row{"terva models init", "scaffold $TERVA_HOME/models.json to edit"},
		row{"terva models init --force", "overwrite an existing models.json"},
	)
	section("self-update",
		row{"terva update", "download and install the latest release"},
		row{"terva update --check", "show whether a new release is available"},
	)
	section("migrate from zot", // rename:keep
		row{"terva migrate", "copy the legacy zot data dir to the terva location (interactive)"}, // rename:keep
		row{"terva migrate --dry-run", "show what would be migrated; change nothing"},
		row{"terva migrate --yes --keep-old", "migrate without prompts; keep the old dir"},
		row{"terva migrate --yes --remove-old", "migrate without prompts; delete the old dir"},
	)
	section("chat bot",
		row{"terva bot setup", "configure a chat connector (telegram via BotFather)"},
		row{"terva bot run", "foreground bridge (ctrl+c to stop)"},
		row{"terva bot start", "background bridge (detached)"},
		row{"terva bot stop", "stop the background bridge"},
		row{"terva bot logs [-f]", "tail the background bridge log"},
		row{"terva bot status", "config + running state"},
		row{"terva bot reset", "forget saved credentials"},
		row{"terva bot ... --connector NAME", "pick the chat service (default: telegram)"},
		row{"terva bot link <connector.json>", "install an external connector by symlink"},
		row{"--connector-manifest PATH", "load an external connector for this run only (dev)"},
		row{"terva telegram-bot / terva tg ...", "aliases for bot --connector=telegram"},
	)
	section("provider and model flags",
		row{"--provider", "provider to use (anthropic|openai|openai-codex|kimi|deepseek|google|ollama|openai-compatible)"},
		row{"--model ID", "model id (see --list-models)"},
		row{"--api-key KEY", "api key for this run (env / auth.json fallback)"},
		row{"--base-url URL", "override provider api base url"},
		row{"--reasoning off|minimum|low|medium|high|maximum", "set thinking level on supported models"},
		row{"--temperature N", "sampling temperature, 0 to 2 (omit for provider default)"},
		row{"--insecure", "skip TLS verification for the inference --base-url (openai-compatible/ollama only)"},
	)
	section("prompt and session flags",
		row{"--system-prompt TEXT", "replace the default system prompt"},
		row{"--append-system-prompt TEXT", "append to the system prompt (repeatable)"},
		row{"--persona NAME|FILE", "load a persona (built-in/on-disk name or .md path) as the identity"},
		row{"--context-file PATH", "inject a file's contents into the system prompt (repeatable)"},
		row{"-c, --continue", "continue the most recent session for this cwd"},
		row{"-r, --resume", "pick a session to resume"},
		row{"--session PATH", "resume a specific session file"},
		row{"--no-session", "do not read or write a session file"},
	)
	section("workspace, tools, skills",
		row{"--cwd PATH", "treat PATH (an existing directory) as the working directory"},
		// building blocks — each turns off one capability, independently:
		row{"--no-workspace-tools", "turn off the built-in read/write/edit/bash/grep tools; keep extensions + MCP (least-privilege for bots)"},
		row{"--no-ext / --no-extensions", "turn off extension discovery for this run"},
		row{"--no-mcp", "turn off MCP server startup for this run"},
		// composite modes built from the blocks (+ an identity change):
		row{"--no-tools", "all three blocks above together (and the skill tool) — no tools at all"},
		row{"--chat", "no tools at all + a conversational, non-coding identity (pairs with --persona)"},
		row{"--play", "extensions + MCP only (= --no-workspace-tools) + an embodied identity"},
		row{"--tools csv", "only enable the listed (built-in) tools"},
		row{"--no-skill", "skip all skill discovery for this run"},
		row{"--approval plan|ask|auto-edit|workspace|yolo", "approval mode: plan = read-only only, ask = confirm everything, auto-edit = confirm non-edit tools, workspace = run built-ins + reads, confirm foreign side-effects (interactive default), yolo = run freely. See docs/permissions.md"},
		row{"--no-yolo", "alias for --approval ask"},
		row{"--jail / --no-jail", "force the sandbox on / off at startup (default: on for interactive, off for headless)"},
		row{"--trust", "trust the cwd for this run only (load project extensions/skills/context; not persisted)"},
		row{"--project / --no-project", "force project-scoped mode on/off (data in .terva/home, only project extensions; login+trust stay global)"},
	)
	section("workspace trust",
		row{"terva trust [path]", "trust a directory so its project extensions/skills/context load (default: cwd)"},
		row{"terva trust --parent [path]", "trust the directory and everything under it"},
		row{"terva trust --list", "show the trusted directories"},
		row{"terva untrust [path]", "remove a directory from the trust list (default: cwd)"},
	)
	section("project-scoped agents (terva project ...)",
		row{"terva project init [--persona NAME]", "scaffold a self-contained agent here (config + dirs + data home)"},
		row{"terva project status", "show what this directory will run: scope, trust, extensions, model"},
		row{"terva project trust / untrust", "trust this directory (required to run it scoped) / revoke"},
		row{"terva project ext adopt/drop/list/disable/enable", "manage this project's extensions"},
		row{"terva project model|provider [VALUE]", "show or set this project's model / provider"},
	)
	section("misc",
		row{"--swarm-worktrees", "give each swarm sub-agent its own git worktree (needs the terva-git-worktree extension)"},
		row{"--max-steps N", "agent loop iteration cap (default: unlimited)"},
		row{"--list-models", "print known models and exit"},
		row{"-h, --help", "show this help"},
		row{"-v, --version", "show version info"},
	)
	fmt.Fprintln(os.Stderr)
	fmt.Fprintln(os.Stderr, assistant("see also: docs/extensions.md, docs/rpc.md, docs/skills.md"))
	fmt.Fprintln(os.Stderr)
}
