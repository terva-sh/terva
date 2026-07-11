package build

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/i18n"
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
	// ModeWeb is the browser control-panel mode: a local HTTP server that
	// speaks ctrlproto over a WebSocket to a self-hosted terva workspace
	// (chat + sessions + models). Routed via `terva web` / --web. The server
	// is an opt-in build (-tags terva_web); the no-tag binary routes here too
	// but exits with "web mode not built in".
	ModeWeb Mode = "web"
	// ModeReplay plays a recorded session transcript back through the
	// interactive TUI as a deterministic, transport-controlled scene (the
	// session player). Routed via `terva replay <file>` / --replay. It backs
	// the TUI with a read-only replay carrier instead of a live Workspace, so
	// it needs no credential and rejects prompts. See
	// docs/proposals/session-player.md.
	ModeReplay Mode = "replay"
)

// Args holds parsed command-line options.
type Args struct {
	Mode     Mode
	Provider string
	Model    string
	APIKey   string

	BaseURL string // override provider base URL (for tests/self-hosted)

	// ReplayPath is the recorded session transcript to play back (--replay /
	// `terva replay <file>`, Mode == ModeReplay).
	ReplayPath string

	// Web control-panel mode (--web / `terva web`, build tag terva_web).
	WebAddr           string   // listen address (default 127.0.0.1:8730)
	WebAuthHeader     string   // trust this forward-auth header as the authenticated user (e.g. X-Forwarded-User)
	WebTrustedProxies []string // IPs/CIDRs (besides loopback) allowed to assert the forward-auth header
	WebToken          string   // bearer token required on requests when no forward-auth is used
	WebInsecure       bool     // allow binding a non-loopback address with no auth mode (dangerous)
	WebInsecureCIDRs  []string // IPs/CIDRs granted no-auth access (besides loopback) — the scoped, safer form of --web-insecure (e.g. a tailnet range)
	WebAllowRestart   bool     // enable Tier-1 self-restart (control.restart + the terva_restart tool); off by default

	SystemPrompt       string
	AppendSystemPrompt []string
	// Persona selects the active Persona (--Persona): a built-in/on-disk name
	// or a path to a .md file. Empty resolves via Persona.md / default_persona /
	// the embedded Mieli default.
	Persona string

	// Card loads a SillyTavern Character Card V2 (.json or .png) as the
	// immersive identity for a chat/play session (--card <path>). It implies
	// --chat when no Experience mode is set and is not valid in regular coding
	// mode (a card can't be a working charter). Separate from --Persona: a
	// card is foreign roleplay content gated to chat/play.
	Card string

	// Substrate is an opaque, scheme-qualified reference to a shared
	// authoritative state surface a dispatched actor binds to
	// (--substrate <scheme>:<ref>) — e.g. a world instance or a task board.
	// RESERVED: empty means the substrate is projected by the parent (the
	// near-term director model), so nothing resolves it yet. It is threaded
	// through the swarm boot spec so a future dispatch skin can attach a child
	// to a substrate without changing the argv contract. See
	// docs/proposals/agent-dispatch.md.
	Substrate string

	// Cast declares the named actors a --play director can voice via the
	// actor_spawn tool: --cast NAME=REF (repeatable), where REF is a Persona
	// name or a character-card path. The model dispatches a cast member by NAME
	// only (never a path), so a session's cast is a closed, user-declared set —
	// not the global Persona roster. See docs/proposals/agent-dispatch.md.
	Cast map[string]string

	// Greeting selects which opening line a --card seeds: 0 = first_mes
	// (default), 1..N = the Nth alternate_greetings entry. Out of range is an
	// error; ignored without --card.
	Greeting int

	// DumpPrompt, when non-empty ("text"|"json"|"raw"), assembles the prompt
	// for the pending turn, prints the manifest, and exits before any model
	// call (--dump-prompt / --dump-prompt=<format>). A debugging + assertion
	// tool; needs no credential.
	DumpPrompt string

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

	// As sets what a character card's {{user}} macro resolves to for this run
	// (the name the character addresses the user by), overriding the persisted
	// user_name config. Empty falls back to config then the literal "User".
	As string

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

	// NoMCP skips MCP server startup for this run (config "mcp" Key
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

	// WithExtensions is the per-run extension allowlist (--extensions,
	// comma-separated manifest names, repeatable). When non-empty, only
	// the named INSTALLED extensions load; --ext paths bypass it and the
	// config disable list still subtracts (restrict-only — it narrows a
	// run, never resurrects). The least-privilege composition flag for
	// exposed agents: a Discord bot in a group room gets `--extensions
	// calendar`, not your mail extension.
	WithExtensions []string

	// WithMCP is the per-run MCP-server allowlist (--mcp, comma-
	// separated server names, repeatable). Same semantics as
	// WithExtensions, applied to the merged user+project MCP config
	// before any server spawns; per-server config disables still
	// subtract.
	WithMCP []string

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

	// NoLore disables the lore keyed-context primitive for this run: no
	// discovery from any tier and no injection (cached prefix or per-turn
	// tail), including a loaded card's character_book. Sibling of
	// --no-skill / --no-ext / --no-mcp. Lore is on by default (config
	// `lore`, default true); the `terva lore` CLI is unaffected.
	NoLore bool

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
	// ResolveApprovalMode (permissions.go).
	Approval string

	ListModels bool
	// ListModelsFilter narrows --list-models output (the `=FILTER`
	// form): a comma list of source terms (user | live | catalog |
	// speculative — `live` folds in `cache`, which is just the last
	// live listing loaded from disk), tier thresholds like `live+`
	// ("this tier and above": user > live/cache > catalog >
	// speculative), and `available` (only providers whose credentials
	// resolve right now — the set a --provider/--model pin could
	// actually use). Terms AND together. Empty = everything.
	ListModelsFilter string
	Help             bool
	Version          bool

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
			return "", i18n.Errorf("%s requires a value", flag)
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
		case "--web":
			a.Mode = ModeWeb
		case "--replay":
			v, err := want(&i, arg)
			if err != nil {
				return a, err
			}
			a.Mode = ModeReplay
			a.ReplayPath = v
		case "--web-addr":
			v, err := want(&i, arg)
			if err != nil {
				return a, err
			}
			a.WebAddr = v
		case "--web-auth-header":
			v, err := want(&i, arg)
			if err != nil {
				return a, err
			}
			a.WebAuthHeader = v
		case "--web-trusted-proxy":
			v, err := want(&i, arg)
			if err != nil {
				return a, err
			}
			for _, p := range strings.Split(v, ",") {
				if p = strings.TrimSpace(p); p != "" {
					a.WebTrustedProxies = append(a.WebTrustedProxies, p)
				}
			}
		case "--web-token":
			v, err := want(&i, arg)
			if err != nil {
				return a, err
			}
			a.WebToken = v
		case "--web-insecure":
			a.WebInsecure = true
		case "--web-insecure-cidr":
			v, err := want(&i, arg)
			if err != nil {
				return a, err
			}
			for _, p := range strings.Split(v, ",") {
				if p = strings.TrimSpace(p); p != "" {
					a.WebInsecureCIDRs = append(a.WebInsecureCIDRs, p)
				}
			}
		case "--web-allow-restart":
			a.WebAllowRestart = true
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
				return a, i18n.Errorf("--chat and --play are mutually exclusive")
			}
			a.Experience = ExperienceChat
		case "--play":
			if a.Experience == ExperienceChat {
				return a, i18n.Errorf("--chat and --play are mutually exclusive")
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
		case "--card":
			v, err := want(&i, arg)
			if err != nil {
				return a, err
			}
			a.Card = v
		case "--substrate":
			v, err := want(&i, arg)
			if err != nil {
				return a, err
			}
			a.Substrate = v
		case "--cast":
			v, err := want(&i, arg)
			if err != nil {
				return a, err
			}
			name, ref, ok := strings.Cut(v, "=")
			name = strings.TrimSpace(name)
			ref = strings.TrimSpace(ref)
			if !ok || name == "" || ref == "" {
				return a, i18n.Errorf("--cast expects NAME=REF (a persona name or card path), got %q", v)
			}
			if a.Cast == nil {
				a.Cast = map[string]string{}
			}
			a.Cast[name] = ref
		case "--greeting":
			v, err := want(&i, arg)
			if err != nil {
				return a, err
			}
			n, perr := strconv.Atoi(strings.TrimSpace(v))
			if perr != nil || n < 0 {
				return a, i18n.Errorf("--greeting must be a non-negative integer")
			}
			a.Greeting = n
		case "--as":
			v, err := want(&i, arg)
			if err != nil {
				return a, err
			}
			a.As = strings.TrimSpace(v)
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
		case "--tui-legacy", "--tui-ctrlproto":
			// deprecated: the legacy direct *core.Agent TUI driver was
			// removed; the ctrlproto carrier is now the only backend
			// (docs/proposals/tui-on-ctrlproto.md). Both flags are accepted
			// silently for backwards compatibility and do nothing.
		case "--extensions":
			v, err := want(&i, arg)
			if err != nil {
				return a, err
			}
			for _, n := range strings.Split(v, ",") {
				if n = strings.TrimSpace(n); n != "" {
					a.WithExtensions = append(a.WithExtensions, n)
				}
			}
		case "--mcp":
			v, err := want(&i, arg)
			if err != nil {
				return a, err
			}
			for _, n := range strings.Split(v, ",") {
				if n = strings.TrimSpace(n); n != "" {
					a.WithMCP = append(a.WithMCP, n)
				}
			}
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
		case "--no-lore":
			a.NoLore = true
		case "--dump-prompt":
			a.DumpPrompt = "text"
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
				return a, i18n.Errorf("--reasoning must be off|minimum|low|medium|high|maximum")
			}
		case "--temperature":
			v, err := want(&i, arg)
			if err != nil {
				return a, err
			}
			f, err := strconv.ParseFloat(v, 32)
			if err != nil || f < 0 || f > 2 {
				return a, i18n.Errorf("--temperature must be a number between 0 and 2")
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
				return a, i18n.Errorf("--max-steps must be a positive integer")
			}
			a.MaxSteps = n
		default:
			if strings.HasPrefix(arg, "--list-models=") {
				a.ListModels = true
				a.ListModelsFilter = strings.TrimPrefix(arg, "--list-models=")
				continue
			}
			if strings.HasPrefix(arg, "--dump-prompt=") {
				v := strings.TrimPrefix(arg, "--dump-prompt=")
				switch v {
				case "text", "json", "raw":
					a.DumpPrompt = v
				default:
					return a, i18n.Errorf("--dump-prompt must be text|json|raw")
				}
				continue
			}
			if strings.HasPrefix(arg, "-") && arg != "-" {
				return a, i18n.Errorf("unknown flag %q", arg)
			}
			positional = append(positional, arg)
		}
	}

	// A cast is meaningful only under --play (both consumers — the addendum
	// and actor_spawn — gate on it): imply --play when no Experience mode was
	// chosen, and reject --chat --cast loudly rather than silently ignoring
	// the cast. Runs before the --card implication so --card --cast lands in
	// --play (the card then shapes the director's identity there).
	if len(a.Cast) > 0 {
		switch a.Experience {
		case "":
			a.Experience = ExperiencePlay
		case ExperienceChat:
			return a, i18n.Errorf("--cast requires --play (--chat has no director to voice a cast)")
		}
	}

	// A character card runs as an immersive chat identity: imply --chat when no
	// Experience mode was chosen. (A card is not a working charter, so it is
	// not valid in regular coding mode.)
	if a.Card != "" && a.Experience == "" {
		a.Experience = ExperienceChat
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
			return a, fmt.Errorf("%s: %w", i18n.T("--cwd %q", a.CWD), err)
		}
		info, err := os.Stat(abs)
		if err != nil {
			return a, fmt.Errorf("%s: %w", i18n.T("--cwd %q", a.CWD), err)
		}
		if !info.IsDir() {
			return a, i18n.Errorf("--cwd %q is not a directory", a.CWD)
		}
		a.CWD = abs
	}
	return a, nil
}

// PrintWebHelp renders the `terva web --help` screen: the web control panel's
// own flags + the session flags that shape it. `terva web` is a build-tag mode
// routed through an argv shim, so its --help would otherwise fall through to the
// top-level screen (which no longer inlines web detail). Always built (help must
// work even in a binary without -tags terva_web — it documents what web offers).
func PrintWebHelp() {
	fmt.Fprint(os.Stderr, i18n.H("help.web", `terva web — browser control panel (self-hosted web UI)

  terva web                          serve on 127.0.0.1:8730 (open http://127.0.0.1:8730)
  terva web --web-addr 0.0.0.0:8730  bind all IPv4 interfaces (needs an auth mode or --web-insecure-cidr; see below)

Web-specific flags:
  --web-addr ADDR               listen address:port (default 127.0.0.1:8730)
  --web-token TOKEN             require this bearer token (Authorization: Bearer, or ?token= for the browser)
  --web-auth-header NAME        trust a forward-auth proxy header as the authenticated user
  --web-trusted-proxy CIDR      IP/CIDR(s) allowed to assert --web-auth-header (comma-separated; loopback always allowed)
  --web-allow-restart           enable Tier-1 self-restart (control.restart + the terva_restart tool)
  --web-insecure                allow binding a non-loopback address with NO auth (dangerous)
  --web-insecure-cidr CIDR      grant NO-auth access to these source IP/CIDR(s) only (comma-separated; loopback always
                                allowed) — the scoped, safer form of --web-insecure for a trusted overlay (e.g. a tailnet
                                range like 100.64.0.0/10); permits a non-loopback bind and self-restart

Security: the server binds loopback by default and REFUSES a non-loopback bind
unless you set an auth mode or --web-insecure. The forward-auth header is honored
ONLY from loopback (a same-host proxy) or a --web-trusted-proxy CIDR — otherwise
it is forgeable by anyone who reaches the port, so a non-loopback bind under
header auth needs --web-trusted-proxy. Prefer a token from a native client over
?token= in a URL (it can leak into proxy logs / history). Put an OAuth/forward-
auth proxy (e.g. Authentik) in front for real deployments. To expose over a
trusted overlay network (tailscale/WireGuard/VPN) with no per-request auth, scope
it with --web-insecure-cidr <range> instead of the blanket --web-insecure — access
is then limited to source IPs in that range and the network is your boundary.

A web session honors the usual session flags — the directory, model, and posture
it runs with (per-session model is switchable in-app):
  --cwd PATH                    pin the workspace directory
  --model ID / --provider NAME  default model for new sessions
  --approval MODE               approval posture (default: workspace)
  --project                     project-scoped mode (data + extensions pinned here)
  --persona NAME|FILE           default persona/identity

Requires a build with -tags terva_web (the release binaries include it).
See docs/web.md for deployment (systemd, LXC/VM, reverse proxy).
`))
}
