package agent

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"terva.sh/terva/packages/agent/build"
	"terva.sh/terva/packages/agent/chat/connlocal"
	"terva.sh/terva/packages/agent/chat/extconn"
	"terva.sh/terva/packages/agent/chat/external"
	"terva.sh/terva/packages/agent/config"
	"terva.sh/terva/packages/agent/extensions"
	"terva.sh/terva/packages/agent/hooks"
	"terva.sh/terva/packages/agent/mode"
	"terva.sh/terva/packages/agent/modes"
	"terva.sh/terva/packages/agent/modes/dialogs"
	"terva.sh/terva/packages/agent/permissions"
	"terva.sh/terva/packages/agent/run"
	"terva.sh/terva/packages/agent/tools"
	"terva.sh/terva/packages/agent/tools/tasks/tasktool"
	"terva.sh/terva/packages/agent/workspace"
	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/envcompat"
	"terva.sh/terva/packages/i18n"
	"terva.sh/terva/packages/privfs"
	"terva.sh/terva/packages/provider"
)

// *Workspace is the in-process ctrlproto carrier the interactive TUI drives —
// the only TUI backend. Asserted here (not in workspace.go) so the lower-level
// workspace need not import modes; cli.go is the composition root that bridges
// the two. See docs/proposals/tui-on-ctrlproto.md.
var _ modes.Carrier = (*workspace.Workspace)(nil)

// workspaceRootFn resolves the agent's live workspace root for a
// WorkspaceDiffer: the sandbox's writable root (which a /cd updates) when
// jailed, else the resolved cwd. Reading it live lets the differ follow a
// directory change without re-wiring.
func workspaceRootFn(sandbox *tools.Sandbox, cwd string) func() string {
	return func() string {
		if sandbox != nil && sandbox.Root != "" {
			return sandbox.Root
		}
		return cwd
	}
}

// Run is the top-level entrypoint for the terva binary.
func Run(rawArgs []string, version string) error {
	// Resolve the operator's UI language and load its message catalog
	// before any user-facing output. English/unset is a no-op — i18n.T then
	// returns each source string verbatim. A malformed tag is a warning, not
	// a fatal error: terva stays in English rather than refusing to start.
	if err := i18n.Configure(config.Language(), config.TervaHome()); err != nil {
		fmt.Fprintln(os.Stderr, "note:", err)
	}
	// TERVA_CAPTURE_LOCALE records every untranslated string shown this run
	// into $TERVA_HOME/locales/<lang>.todo.json on exit, so an operator can
	// fill the gaps as they use terva and PR them back.
	if envcompat.Get("CAPTURE_LOCALE") != "" {
		i18n.Capture(true)
		defer func() {
			if n, err := i18n.Flush(config.TervaHome()); err == nil && n > 0 {
				fmt.Fprintf(os.Stderr, "i18n: captured %d untranslated string(s) to %s\n",
					n, filepath.Join(config.TervaHome(), "locales", i18n.ActiveLang()+".todo.json"))
			}
		}()
	}

	// One-shot migration hint when the data dir resolved to the
	// legacy pre-rename location (which keeps working forever).
	if note, ok := envcompat.HomeMigrationNote(); ok {
		fmt.Fprintln(os.Stderr, note)
	}

	// One-time permissions repair: older installs wrote config.json (which
	// holds plaintext extension secrets), transcripts, sidecars, and logs as
	// 0644 under 0755 dirs, readable by other local users. Tighten any
	// pre-existing group/world-readable state to owner-only, once, gated by a
	// marker file so the walk never repeats. New writes already use private
	// modes via packages/privfs.
	if n, err := privfs.RepairOnce(config.TervaHome()); err == nil && n > 0 {
		fmt.Fprintf(os.Stderr, "note: tightened permissions on %d private file(s) under %s\n", n, config.TervaHome())
	}

	// A brand-new home gets at-rest encryption without being asked, so its
	// credentials are born encrypted rather than rewritten later. Existing
	// homes are never touched — see initSecretsForFreshHome.
	initSecretsForFreshHome()

	// External chat connectors ($TERVA_HOME/connectors — global only,
	// never project-local) register before any dispatch so `terva bot`
	// and the TUI's /connect both see them alongside the compiled-in
	// services.
	external.SetTervaVersion(version)
	for _, err := range external.RegisterDiscovered(config.TervaHome()) {
		fmt.Fprintln(os.Stderr, "connector load:", err)
	}

	// Connector extensions (extensions whose manifest declares
	// "connector": true — global only, never project-local) register as
	// chat services from manifests alone, so `terva bot --connector
	// <name>` resolves them before any extension spawns. The live
	// process binds later, at Connect time, through extconn.BindHost.
	extconn.SetTervaVersion(version)
	connlocal.SetTervaVersion(version)
	for _, err := range extconn.RegisterDiscovered(config.TervaHome(), GlobalExtensionsDir()) {
		fmt.Fprintln(os.Stderr, "connector extension:", err)
	}

	// Register user-defined OpenAI-compatible endpoints (config.json
	// "endpoints") as providers before any Resolve / discovery sees them.
	build.RegisterEndpointsFromConfig()

	// Open the tool-call audit log for this process (lazily backed, so a
	// no-tool run never touches disk). Every mode's BeforeToolExecute records
	// through this shared sink — see buildBeforeToolExecute.
	build.InitAudit(config.TervaHome())

	// Subcommand router: `terva bot ...` is handled separately so the
	// generic flag parser doesn't reject "bot" as a positional arg.
	if handled, err := runBotCommand(rawArgs, version); handled {
		return err
	}
	if handled, err := runProjectCommand(rawArgs); handled {
		return err
	}
	if handled, err := runExtCommand(rawArgs, version); handled {
		return err
	}
	if handled, err := runCtlCommand(rawArgs, version); handled {
		return err
	}
	if handled, err := runPersonaCommand(rawArgs); handled {
		return err
	}
	if handled, err := runLoreCommand(rawArgs); handled {
		return err
	}
	if handled, err := runCardCommand(rawArgs); handled {
		return err
	}
	if handled, err := runUpdateCommand(rawArgs, version); handled {
		return err
	}
	if handled, err := runModelsCommand(rawArgs); handled {
		return err
	}
	if handled, err := runMigrateCommand(rawArgs); handled {
		return err
	}
	if handled, err := runTrustCommand(rawArgs); handled {
		return err
	}
	if handled, err := runDoctorCommand(rawArgs); handled {
		return err
	}
	if handled, err := runSkillsCommand(rawArgs); handled {
		return err
	}
	if handled, err := runSecretCommand(rawArgs); handled {
		return err
	}
	if handled, err := runUnjailCommand(rawArgs); handled {
		return err
	}
	if handled, err := runLocaleCommand(rawArgs); handled {
		return err
	}
	if handled, err := runRaatiCommand(rawArgs, version); handled {
		return err
	}
	if handled, err := runMCPApprovalBridgeCommand(rawArgs); handled {
		return err
	}
	if handled, err := runWorkflowCommand(rawArgs, version); handled {
		return err
	}
	// `terva rpc` is shorthand for `terva --rpc` so third-party apps can
	// spawn the binary with a clean argv. Strip the leading 'rpc'
	// token and let the rest flow through the normal arg parser.
	if len(rawArgs) > 0 && rawArgs[0] == "rpc" {
		rawArgs = append([]string{"--rpc"}, rawArgs[1:]...)
	}
	// `terva acp` is shorthand for `terva --acp` (the ACP run mode), routed
	// like `terva rpc` so a spawning editor gets a clean argv. The mode
	// itself is an opt-in build (-tags terva_acp); the no-tag binary still
	// routes here and exits with "acp mode not built in".
	if len(rawArgs) > 0 && rawArgs[0] == "acp" {
		rawArgs = append([]string{"--acp"}, rawArgs[1:]...)
	}
	// `terva web` is shorthand for `terva --web` (the browser control-panel
	// mode), routed like `terva acp`. The server is an opt-in build
	// (-tags terva_web); the no-tag binary routes here and exits with
	// "web mode not built in".
	if len(rawArgs) > 0 && rawArgs[0] == "web" {
		rawArgs = append([]string{"--web"}, rawArgs[1:]...)
	}
	// `terva attach [URL]` is shorthand for `terva --attach [URL]` (the
	// remote-TUI mode), routed like `terva web`. The optional URL rides as
	// the flag's positional value.
	if len(rawArgs) > 0 && rawArgs[0] == "attach" {
		rawArgs = append([]string{"--attach"}, rawArgs[1:]...)
	}
	// `terva replay <file>` is shorthand for `terva --replay <file>` (the
	// session-player mode), routed like `terva web`. The leading path becomes
	// the flag value; ParseArgs errors cleanly if it is missing.
	if len(rawArgs) > 0 && rawArgs[0] == "replay" {
		rawArgs = append([]string{"--replay"}, rawArgs[1:]...)
	}

	args, err := build.ParseArgs(rawArgs)
	if err != nil {
		PrintHelp(version)
		return err
	}
	// Pin the at-rest encryption key path before anything resolves
	// credentials — the auth store's codec asks config for the key lazily,
	// so the flag only needs to be recorded, not threaded.
	if args.SecretsKeyFile != "" {
		config.SetSecretsKeyFile(args.SecretsKeyFile)
	}
	if args.Help {
		// Mode-scoped help: `terva web --help` documents the web flags rather
		// than falling through to the generic top-level screen (web/acp are
		// build-tag modes routed via an argv shim, so their --help lands here).
		switch args.Mode {
		case mode.Web:
			build.PrintWebHelp()
		default:
			PrintHelp(version)
		}
		return nil
	}
	if args.Version {
		fmt.Println("terva", version)
		return nil
	}

	// Project-scoped mode: redirect data into a project-local home (and load
	// only this project's extensions) BEFORE anything reads the home. Login +
	// trust stay global. Off unless --project / a .terva/config.json
	// project_scoped field asks for it.
	if note, perr := maybeEnableProjectScope(args); perr != nil {
		return perr
	} else if note != "" {
		fmt.Fprintln(os.Stderr, "terva:", note)
	}
	// Dev connectors load loudly, exactly as named, for exactly this
	// invocation — there is deliberately no discovery-based dev mode
	// (see docs/plans/chat-connectors.md).
	for _, p := range args.ConnectorManifests {
		name, err := external.RegisterManifest(p)
		if err != nil {
			return fmt.Errorf("--connector-manifest %s: %w", p, err)
		}
		fmt.Fprintf(os.Stderr, "terva: dev connector %q loaded for this run (%s)\n", name, p)
	}
	// Model catalog: load any cached discovery data before we inspect
	// the model list (list-models, print/json, interactive). User
	// models.json is applied LAST so its per-model overrides win over
	// both cached/live discovery and the openai-compatible default model
	// (which RegisterExtraModel writes with a generic 8192 max-output).
	LoadCachedModels()
	LoadCompatModel()
	// Endpoints the cache does not cover yet — the first launch after /login
	// created one. Everything below this line, Resolve included, then sees the
	// models the operator's own server actually serves instead of nothing.
	EnsureEndpointModels()
	LoadUserModels()

	// Repair config.json so a stale (provider, model) pair from an
	// interrupted /model switch can't strand the user with an
	// "unknown model" error on the first turn. Runs before any UI
	// renders so the status bar shows the post-repair pair, not the
	// broken one. Silent on success.
	ValidateAndRepairConfig()

	if args.ListModels {
		return printModels(args.ListModelsFilter)
	}

	ctx := context.Background()

	// Kick an async refresh of the live model catalog. The first run of
	// terva hits the network; subsequent runs within CacheTTL do nothing.
	RefreshModelsAsync()
	// Always re-list a configured openai-compatible endpoint (not cache
	// gated): a local server's loaded models change frequently.
	RefreshCompatModelsAsync()

	if args.DumpPrompt != "" {
		return runPromptDump(args)
	}
	switch args.Mode {
	case mode.Print:
		return runPrintMode(ctx, args, version)
	case mode.JSON:
		return runJSONMode(ctx, args, version)
	case mode.RPC:
		return runRPCMode(ctx, args, version)
	case mode.ACP:
		return runACPMode(ctx, args, version)
	case mode.Web:
		return runWebMode(ctx, args, version)
	case mode.Replay:
		return runReplayMode(ctx, args, version)
	case mode.Attach:
		return runAttachMode(ctx, args, version)
	case mode.SwarmAgent:
		return runSwarmAgentMode(ctx, args, version)
	default:
		return runInteractiveCtrlproto(ctx, args, version)
	}
}

// ---- print / json modes: require credentials, run single-shot ----

// setupNonInteractiveExtensions loads --ext paths and (unless
// --no-ext) runs discovery. Returns the manager so the caller can
// wire tools into the resolved registry, and a cleanup closure to
// defer. Mirrors the interactive-mode setup minus the TUI hooks.
func setupNonInteractiveExtensions(ctx context.Context, args build.Args, r *build.Resolved, version string) (*extensions.Manager, func()) {
	extMgr := build.NewExtensionManager(config.TervaHome(), r.CWD, version, r.Provider, r.Model, build.NonInteractiveExtHooks{})
	extMgr.SetContextDisabled(r.DisableContextExtensions)
	extMgr.SetDisabledExtensions(r.DisableExtensions) // before Discover/LoadExplicit
	extMgr.SetAllowedExtensions(args.WithExtensions)  // --extensions allowlist; --ext paths bypass
	extMgr.SetProjectTrusted(r.Trusted)               // gate project ext dirs on Workspace Trust
	if ProjectScoped() {                              // re-adopt named globals into a scoped project (trust-gated)
		extMgr.SetAdopt(GlobalExtensionsDir(), resolveAdoptExtensions(args.CWD))
	}
	extMgr.SetConfigResolver(build.ResolveExtensionConfig) // hello_ack config delivery
	build.WireSessionReader(extMgr, config.TervaHome(), r.CWD)
	build.WireExtensionSecrets(extMgr, config.TervaHome())
	for _, e := range extMgr.LoadExplicit(ctx, args.Exts) {
		fmt.Fprintln(os.Stderr, "extension load:", e)
	}
	if !args.NoExt {
		for _, e := range extMgr.Discover(ctx) {
			fmt.Fprintln(os.Stderr, "extension load:", e)
		}
	}
	extMgr.WaitForReady(3 * time.Second)
	r.MergeExtensionTools(&build.ExtToolAdapter{Mgr: extMgr})
	// Connector extensions: point the chat adapters at this manager so a
	// service registered at CLI startup can bind its live process when a
	// chat consumer connects (`terva bot run --connector <ext>`).
	extconn.BindHost(extMgr)
	// MCP servers ride the same seam, after extensions so an
	// extension tool name wins a collision (first registration).
	_, stopMCP := build.SetupMCP(ctx, args, r)
	// NOTE: session_start is emitted by the caller via emitSessionStart
	// AFTER it opens the session, so a session-keyed extension learns the
	// real session id (print / json / swarm-agent all persist a session).
	// Emitting a bare event here would (and did) leave those modes
	// reporting "no active session" even though one exists.
	return extMgr, func() {
		extconn.BindHost(nil)
		extMgr.Stop(2 * time.Second)
		stopMCP()
	}
}

func wireNonInteractiveAgentExtHooks(ctx context.Context, ag *core.Agent, extMgr *extensions.Manager, gate *core.ConfirmGate, hookEng *hooks.Engine, differ *tools.WorkspaceDiffer, tasksCtrl *tasktool.Controller) {
	if ag == nil {
		return
	}
	// Everything up to the extension check is wired whether or not this host
	// has extensions, because none of it is ABOUT extensions.
	//
	// The gate especially. This used to sit below a combined
	// `ag == nil || extMgr == nil` early return, which made the permission gate
	// conditional on an extension manager existing — the one thing that must
	// never be conditional on anything. No caller reaches it today (every one
	// sources its manager from setupNonInteractiveExtensions, which always
	// builds one; under --no-ext it is live-but-toolless precisely so the hook
	// wiring stays uniform), and the SDK avoids the helper entirely for this
	// reason: it calls BuildBeforeToolExecute(nil, gate, nil, ag) directly, which is
	// the configuration this early return would have silently disarmed.
	//
	// build.WireHostToolDispatcher already has this shape — it binds the
	// scripting host call for any agent and returns early only for the
	// extension-specific half.
	ag.BeforeToolExecute = build.BuildBeforeToolExecute(hookEng, gate, extMgr, ag)
	build.WireHostToolDispatcher(ag, extMgr, gate)

	// The extension-shaped half, and only it, is conditional.
	if extMgr != nil {
		wsObserve := build.WorkspaceChangeObserver(differ, extMgr)
		ag.BeforeTurn = func(step int) (bool, string) {
			res := extMgr.InterceptTurnStart(ctx, step)
			return !res.Block, res.Reason
		}
		ag.BeforeAssistantMessage = func(text string) (bool, string, string) {
			res := extMgr.InterceptAssistantMessage(ctx, text)
			if res.Block {
				return false, res.Reason, ""
			}
			return true, "", res.ReplaceText
		}
		ag.BeforeUserMessage = func(text string) (bool, string, string) {
			res := extMgr.InterceptUserMessage(ctx, text)
			if res.Block {
				return false, res.Reason, ""
			}
			return true, "", res.ReplaceText
		}
		// Registration order is delivery order.
		ag.AddEventObserver(wsObserve)
		ag.AddEventObserver(func(ev core.AgentEvent) { build.FanoutAgentEvent(extMgr, ev) })
		ag.AddEventObserver(func(ev core.AgentEvent) { build.ObserveAgentEventForHooks(hookEng, ev) })
	}

	// The live cards the model reads each turn: the extension managers' and the
	// built-in task board's, stacked onto the run's own lore/PHI tail. One call
	// outside the extension check, because the board is not an extension — its
	// card and its open-work gate follow the CONTROLLER, not the manager — and
	// because build.EphemeralTail owns the order, which the re-derivation path
	// has to reproduce exactly.
	build.WireEphemeralTail(ag, build.EphemeralTail{Ext: build.ExtEphemeral(extMgr), Tasks: tasksCtrl})
	ag.AddContinuationGate(build.OpenWorkGate(extMgr, tasksCtrl))
}

// wireBotAgentExtHooks wires a long-lived chat-bot agent to its extension
// manager — the same hooks the headless print/json modes use (tool gate +
// extension interception + live context cards + event fanout) — bundled so
// botcmd.go needs no extra imports. The bot loop runs turns one at a time, so
// the per-turn hooks fire exactly as they do in a headless single-shot run.
// tasksCtrl is per-CONVERSATION, not per-process: the owner DM passes the
// shared r.Tasks (bound to its durable session); each admitted group passes
// its own fresh controller so its card and open-work gate read its own board.
func wireBotAgentExtHooks(ctx context.Context, ag *core.Agent, extMgr *extensions.Manager, gate *core.ConfirmGate, args build.Args, r *build.Resolved, tasksCtrl *tasktool.Controller) {
	wireNonInteractiveAgentExtHooks(ctx, ag, extMgr, gate, build.BuildHookEngine(args, r.Trusted), tools.NewWorkspaceDiffer(workspaceRootFn(r.Sandbox, r.CWD)), tasksCtrl)
}

func runPrintMode(ctx context.Context, args build.Args, version string) error {
	confirmGate, roSet := permissions.HeadlessConfirmGate(args.PermInputs())
	r, err := build.Resolve(args, true)
	if err != nil {
		return err
	}
	permissions.WarnRestrictedWorkspace(args.CWD, r.Trusted)
	permissions.WarnPersistentlyUnjailed(args.PermInputs())
	r.AdoptReadOnlySet(roSet)
	extMgr, stopExt := setupNonInteractiveExtensions(ctx, args, &r, version)
	defer stopExt()

	ag := r.NewAgent()
	wireNonInteractiveAgentExtHooks(ctx, ag, extMgr, confirmGate, build.BuildHookEngine(args, r.Trusted), tools.NewWorkspaceDiffer(workspaceRootFn(r.Sandbox, r.CWD)), r.Tasks)
	sess, serr := openOrCreateSession(args, r, ag, version)
	if serr != nil {
		if err := headlessSessionErr(args, serr); err != nil {
			return err
		}
	}
	defer sess.Close()
	// Adopt the identity, key the board, then announce — one event in one order,
	// shared with every other host (build.BindSession). This site used to
	// announce before keying the board.
	build.BindSession(build.SessionBinding{Agent: ag, Tasks: r.Tasks, Ext: extMgr, Session: sess})

	prompt := args.Prompt
	if prompt == "" {
		piped, _ := readAllStdin()
		prompt = strings.TrimSpace(piped)
	}
	if prompt == "" {
		return fmt.Errorf("print mode requires a prompt (arg or stdin)")
	}

	start := len(ag.Messages())
	err = run.Print(ctx, ag, prompt, nil, os.Stdout)
	WriteNewTranscript(ag, sess, start)
	return err
}

func runJSONMode(ctx context.Context, args build.Args, version string) error {
	confirmGate, roSet := permissions.HeadlessConfirmGate(args.PermInputs())
	r, err := build.Resolve(args, true)
	if err != nil {
		return err
	}
	permissions.WarnRestrictedWorkspace(args.CWD, r.Trusted)
	permissions.WarnPersistentlyUnjailed(args.PermInputs())
	r.AdoptReadOnlySet(roSet)
	extMgr, stopExt := setupNonInteractiveExtensions(ctx, args, &r, version)
	defer stopExt()

	ag := r.NewAgent()
	wireNonInteractiveAgentExtHooks(ctx, ag, extMgr, confirmGate, build.BuildHookEngine(args, r.Trusted), tools.NewWorkspaceDiffer(workspaceRootFn(r.Sandbox, r.CWD)), r.Tasks)
	sess, serr := openOrCreateSession(args, r, ag, version)
	if serr != nil {
		if err := headlessSessionErr(args, serr); err != nil {
			return err
		}
	}
	defer sess.Close()
	// Adopt the identity, key the board, then announce — one event in one order,
	// shared with every other host (build.BindSession). This site used to
	// announce before keying the board.
	build.BindSession(build.SessionBinding{Agent: ag, Tasks: r.Tasks, Ext: extMgr, Session: sess})

	prompt := args.Prompt
	if prompt == "" {
		piped, _ := readAllStdin()
		prompt = strings.TrimSpace(piped)
	}
	if prompt == "" {
		return fmt.Errorf("json mode requires a prompt (arg or stdin)")
	}

	start := len(ag.Messages())
	err = modes.RunJSON(ctx, ag, prompt, nil, os.Stdout)
	WriteNewTranscript(ag, sess, start)
	return err
}

// userNameResolved reports whether a character card's {{user}} name is already
// determined without asking — via --as, a trusted project's user_name, or the
// global user_name. It mirrors the resolution Resolve performs (resolveTrustState
// + ResolveConfig), so the prompt honors the documented precedence
// --as > trusted-project > global > "User" instead of consulting only the global
// preference — which would let the prompt clobber a trusted project's user_name.
func userNameResolved(args build.Args) bool {
	if strings.TrimSpace(args.As) != "" {
		return true
	}
	trusted := permissions.ResolveTrustState(args.CWD, args.Trust).IsTrusted()
	return strings.TrimSpace(config.ResolveConfig(args.CWD, trusted).UserName) != ""
}

// maybePromptUserName asks, once, what a character card should call the user —
// but only when a card is loaded, the name isn't already resolvable (see
// userNameResolved), and stdin is an interactive terminal. The answer is used
// this run and persisted globally for later. Non-interactive runs are left
// untouched so Resolve can fall back to --as / config / "User". It reads a full
// line (unbuffered, so it can't swallow input the TUI needs and preserves spaces).
func maybePromptUserName(args build.Args) build.Args {
	if args.Card == "" || userNameResolved(args) {
		return args
	}
	if !stdinIsTTY() {
		return args
	}
	fmt.Fprint(os.Stderr, "What should the character call you? [User] ")
	name := strings.TrimSpace(readLineRaw(os.Stdin))
	if name == "" {
		name = "User"
	}
	args.As = name
	// Persist to the GLOBAL config so it's remembered across projects, not
	// written into a project-scoped run's redirected home.
	if err := config.SetGlobalUserName(name); err != nil {
		fmt.Fprintf(os.Stderr, "warning: could not save your name (%v); using it for this session only\n", err)
	}
	return args
}

// readLineRaw reads a single line from r one byte at a time (no buffering), so
// it never reads past the newline into a discarded buffer — safe to call right
// before the TUI takes over stdin. Any trailing CR is stripped.
func readLineRaw(r io.Reader) string {
	var b []byte
	buf := make([]byte, 1)
	for {
		n, err := r.Read(buf)
		if n > 0 {
			if buf[0] == '\n' {
				break
			}
			b = append(b, buf[0])
		}
		if err != nil {
			break
		}
	}
	return strings.TrimRight(string(b), "\r")
}

// ---- interactive mode: opens the TUI even without credentials ----

// loggedInProviderList returns the providers a picker can offer. Shared by
// interactive startup and the in-TUI login flow.
func loggedInProviderList() []string {
	return build.LoggedInProviders()
}

// openOrCreateSession returns a session for the run. sess may be nil
// with a nil error if session persistence is disabled.
func openOrCreateSession(args build.Args, r build.Resolved, ag *core.Agent, version string) (*core.Session, error) {
	if args.NoSess {
		return nil, nil
	}
	// Sweep meta-only files left over from older terva versions (and from
	// any session that crashed before its first AppendMessage). Cheap;
	// reads the first few bytes of each file in the cwd's session dir.
	core.PruneEmptySessions(config.TervaHome(), args.CWD)
	var (
		s    *core.Session
		msgs []provider.Message
		err  error
		// seedFresh marks a brand-new session created at an explicit
		// --session path: as fresh as the no-flag path at the bottom, so it
		// seeds the card greeting the same way.
		seedFresh bool
	)
	switch {
	case args.Session != "":
		s, msgs, err = core.OpenSession(args.Session)
		// The swarm-agent child passes a fixed --session path that
		// may not exist yet on first Spawn. Treat ENOENT as "create
		// a fresh session AT THIS PATH" so the conversation actually
		// gets persisted; without this fallback the swarm child runs
		// with sess==nil and every Resume re-starts with no memory
		// of the prior turns. Other openers (--continue / --resume /
		// the picker) never see ENOENT here because they only choose
		// paths that already exist on disk.
		if err != nil && errors.Is(err, os.ErrNotExist) {
			s, err = core.NewSessionAtPath(args.Session, args.CWD, r.Provider, r.Model, version)
			msgs = nil
			seedFresh = err == nil
		}
	case args.Continue:
		latest := core.LatestSession(config.TervaHome(), args.CWD)
		if latest != "" {
			s, msgs, err = core.OpenSession(latest)
		}
	case args.ResumeID != "":
		// --resume <id>: direct resume for headless runs (docs/cli.md's
		// "ask the agent for its session id" debugging loop). Fail loudly
		// rather than silently starting fresh.
		path, perr := resolveSessionID(args.CWD, args.ResumeID)
		if perr != nil {
			return nil, perr
		}
		s, msgs, err = core.OpenSession(path)
	case args.Resume:
		picked, perr := pickSession(args.CWD)
		if perr != nil {
			return nil, perr
		}
		if picked != "" {
			s, msgs, err = core.OpenSession(picked)
		}
	}
	if err != nil {
		return nil, err
	}
	if s != nil {
		// Every resume above lands here, which is the one place that knows the
		// session is being CONTINUED rather than merely read — so it is where the
		// running build gets recorded. Without it a session resumed after an
		// upgrade keeps re-stamping the version that created it.
		if verr := s.StampVersion(version); verr != nil {
			fmt.Fprintln(os.Stderr, "terva:", verr)
		}
		// Startup path: stderr is still ours (no TUI yet), so surface
		// anything OpenSession had to skip instead of losing it.
		for _, w := range s.LoadWarnings {
			fmt.Fprintln(os.Stderr, "terva:", w)
		}
		// Run on the session's OWN stored model, not the resolved default the
		// agent was built on: a resume must reproduce the session's model, not
		// silently drop to the config default. Non-fatal (see applyResumedModel).
		if _, _, note := applyResumedModel(ag, args, s, r.Provider, r.Model); note != "" {
			fmt.Fprintln(os.Stderr, "terva:", note)
		}
		ag.SetMessages(msgs)
		// A brand-new session at an explicit --session path seeds the card
		// greeting exactly like the fresh path below (after SetMessages, which
		// would otherwise clobber it). This deliberately includes a dispatched
		// actor child (the runner passes a fixed --session): a card actor's
		// first_mes is the character's canonical opening voice, part of the
		// actor's own context — the same rule as a user-run --chat --card.
		if seedFresh {
			seedCardGreeting(s, ag, r.CardGreeting)
		}
		if cum, _, resume, uerr := core.SessionUsageDetail(s.Path); uerr == nil {
			ag.SeedCost(cum)
			ag.SeedLastTurnUsage(resume)
		}
		return s, nil
	}
	fresh, err := core.NewSession(config.TervaHome(), args.CWD, r.Provider, r.Model, version)
	if err != nil {
		return nil, err
	}
	seedCardGreeting(fresh, ag, r.CardGreeting)
	return fresh, nil
}

// applyResumedModel re-points a freshly-built resumed agent at the session's OWN
// stored model when it differs from the model the agent was built on (the
// resolved default). Without it a --continue/--resume/rpc/swarm resume — and,
// via the ACP twin, an editor resume — keeps running on the config default while
// the session file still names another model, silently downgrading the
// conversation. The daemon/carrier resume path already gets this right by
// injecting sess.Meta.Model before build.Resolve; this is the headless/ACP
// equivalent, applied after the agent is already built.
//
// It mirrors the live model-switch rebuild (build.Resolve → HasCredential →
// NewClient → SetClientAndModel), carries the observed usage snapshot across, and
// re-binds terva_status so context-window reporting follows the swap. Non-fatal
// by design: a resume must never fail over a model swap, so a stored model that
// no longer resolves or has no credential leaves the built model in place and
// returns a note for the caller to log. The returned (prov, model) always names
// the agent's actual live model — callers use it for the menu/status line.
func applyResumedModel(ag *core.Agent, base build.Args, sess *core.Session, builtProv, builtModel string) (prov, model, note string) {
	sp, sm := sess.Meta.Provider, sess.Meta.Model
	if sp == "" || sm == "" || (sp == builtProv && sm == builtModel) {
		return builtProv, builtModel, "" // nothing stored, or already correct
	}
	next := base
	next.Provider = sp
	next.Model = sm
	// Drop launch-time key/URL pins to the OLD model's endpoint so the stored
	// model resolves its own credential/endpoint (as switchModel does).
	next.APIKey = ""
	next.BaseURL = ""
	r, err := build.Resolve(next, true)
	if err != nil {
		return builtProv, builtModel, fmt.Sprintf("session's stored model %s/%s did not resolve (%v); continuing on %s/%s", sp, sm, err, builtProv, builtModel)
	}
	if !r.HasCredential() {
		return builtProv, builtModel, fmt.Sprintf("no credential for the session's stored model %s/%s; continuing on %s/%s", sp, sm, builtProv, builtModel)
	}
	// The shared model-swap event: a resume onto the session's stored model is
	// the same move as a live switch, and was the one copy of this sequence
	// carrying no note about the three others it matched.
	build.ApplyModelSwap(build.ModelSwap{
		Agent:      ag,
		Client:     r.NewClient(),
		Provider:   r.Provider,
		Model:      r.Model,
		AuthMethod: r.AuthMethod,
		BaseURL:    r.BaseURL,
		// Normally a no-op: the swap is onto the model the file already names.
		// It writes only when Resolve landed somewhere else than the stored
		// pair — an alias that now points at a different id — and then the file
		// names the model actually running rather than the one asked for.
		Session: sess,
	})
	return r.Provider, r.Model, ""
}

// seedCardGreeting seeds a --card's opening line (first_mes) as the first
// assistant message of a fresh session, so the character "speaks first": it
// renders in the transcript, persists across resume, and gives the model
// continuity. A request-scoped guard in the provider converters keeps a
// leading assistant turn valid for APIs (Anthropic) that require the first
// message to be a user turn.
func seedCardGreeting(s *core.Session, ag *core.Agent, greeting string) {
	if greeting == "" {
		return
	}
	msg := provider.Message{
		Role:    provider.RoleAssistant,
		Content: []provider.Content{provider.TextBlock{Text: greeting}},
		Time:    time.Now(),
		Meta:    map[string]string{"source": "card:greeting"},
	}
	ag.SetMessages([]provider.Message{msg})
	if s != nil {
		_ = s.AppendMessage(msg)
	}
}

// pickSession is the pre-TUI terminal picker: the --resume fallback for
// boots that never own the terminal (print/json, the swarm child). TTY
// boots open the in-TUI session dialog instead (interactive/ctrlproto).
// Rows render through the dialog's formatter — title (or first message),
// age, model, size, cost — never raw file paths.
func pickSession(cwd string) (string, error) {
	summaries := pickableSessions(core.DescribeSessions(config.TervaHome(), cwd))
	if len(summaries) == 0 {
		fmt.Fprintln(os.Stderr, "no sessions for", cwd)
		return "", nil
	}
	// Fixed row budget: this path has no terminal-size probe (stdout may
	// not even be a TTY); the formatter hard-clamps so rows never wrap on
	// any modern terminal width.
	const rowWidth = 100
	for i, s := range summaries {
		fmt.Fprintf(os.Stderr, "  %2d) %s\n", i+1, dialogs.FormatSessionRowPlain(s, rowWidth))
	}
	fmt.Fprint(os.Stderr, "pick #: ")
	rd := bufio.NewReader(os.Stdin)
	line, _ := rd.ReadString('\n')
	line = strings.TrimSpace(line)
	var n int
	if _, err := fmt.Sscanf(line, "%d", &n); err != nil || n < 1 || n > len(summaries) {
		return "", fmt.Errorf("invalid selection")
	}
	return summaries[n-1].Path, nil
}

// headlessSessionErr triages a session-open failure for the print/json
// modes: fatal (returned) when the run named an explicit target — a
// --resume <id> or --session path that doesn't resolve must not silently
// land on a fresh transcript — otherwise surfaced as a warning and the run
// continues without persistence, the long-standing headless behavior (and
// the bot daemon's convention).
func headlessSessionErr(args build.Args, serr error) error {
	if args.ResumeID != "" || args.Session != "" {
		return serr
	}
	fmt.Fprintln(os.Stderr, "session:", serr)
	return nil
}

// resolveSessionID maps a --resume <id> (filename stem, .jsonl tolerated)
// to the transcript path in this cwd's session dir, erroring when no such
// session exists.
func resolveSessionID(cwd, id string) (string, error) {
	id = strings.TrimSuffix(id, ".jsonl")
	for _, p := range core.ListSessions(config.TervaHome(), cwd) {
		if build.SessionIDFromPath(p) == id {
			return p, nil
		}
	}
	return "", i18n.Errorf("no session %s for %s", id, cwd)
}

// pickableSessions drops zero-message sessions — same rule as the in-TUI
// dialog: resuming an empty session is a no-op, and fresh/crashed empties
// would otherwise pad the list until the next prune sweep.
func pickableSessions(all []core.SessionSummary) []core.SessionSummary {
	out := make([]core.SessionSummary, 0, len(all))
	for _, s := range all {
		if s.MessageCount == 0 {
			continue
		}
		out = append(out, s)
	}
	return out
}

// WriteNewTranscript appends only messages after index `from` from the
// agent's transcript to the session. Used by callers that don't hold
// the persistMu (non-interactive print/json modes which run a single
// turn under their own goroutine).
func WriteNewTranscript(ag *core.Agent, sess *core.Session, from int) {
	writeNewTranscriptLocked(ag, sess, from)
}

// writeNewTranscriptLocked is the same as WriteNewTranscript. The
// suffix marks that interactive callers must hold persistMu when
// invoking it so concurrent appends from the agent loop don't race
// with this catch-up flush.
func writeNewTranscriptLocked(ag *core.Agent, sess *core.Session, from int) {
	if sess == nil || ag == nil {
		return
	}
	msgs := ag.Messages()
	for i := from; i < len(msgs); i++ {
		_ = sess.AppendMessage(msgs[i])
	}
	_ = sess.AppendUsage(ag.LastTurnUsage(), ag.Cost())
}

func readAllStdin() (string, error) {
	fi, err := os.Stdin.Stat()
	if err != nil {
		return "", err
	}
	if (fi.Mode() & os.ModeCharDevice) != 0 {
		return "", nil
	}
	b, err := io.ReadAll(os.Stdin)
	return string(b), err
}

// modelSourceLabel normalizes a model's provenance the way the table
// renders it: "user" (models.json), "live" (this run's discovery),
// "cache" (the last live listing, loaded from disk), "catalog" (baked
// in), or "speculative" (upstream-announced, unconfirmed).
func modelSourceLabel(m provider.Model) string {
	if m.Speculative {
		return "speculative"
	}
	if m.Source == "" {
		return "catalog"
	}
	return m.Source
}

// modelSourceTier ranks provenance by how actionable it is, for the
// `<source>+` ("this tier and above") filter form. live and cache
// share a tier: cache IS live, one run removed.
var modelSourceTier = map[string]int{
	"speculative": 1,
	"catalog":     2,
	"cache":       3,
	"live":        3,
	"user":        4,
}

// modelListFilter compiles a --list-models FILTER into a predicate.
// Terms (comma-separated) AND together: an exact source label (where
// `live` deliberately folds in `cache` — otherwise the filter goes
// mysteriously empty once the discovery cache takes over between
// runs), a tier threshold like `live+`, or `available` — only models
// whose provider `selectable` says yes to, i.e. the (provider, model)
// pairs a --provider/--model pin could use right now.
func modelListFilter(filter string, selectable func(providerName string) bool) (func(provider.Model) bool, error) {
	var preds []func(provider.Model) bool
	for _, term := range strings.Split(filter, ",") {
		term = strings.TrimSpace(term)
		if term == "" {
			continue
		}
		switch {
		case term == "available":
			preds = append(preds, func(m provider.Model) bool { return selectable(m.Provider) })
		case strings.HasSuffix(term, "+"):
			base := strings.TrimSuffix(term, "+")
			tier, ok := modelSourceTier[base]
			if !ok {
				return nil, fmt.Errorf("--list-models: unknown source %q in %q (sources: user, live, cache, catalog, speculative)", base, term)
			}
			preds = append(preds, func(m provider.Model) bool {
				return modelSourceTier[modelSourceLabel(m)] >= tier
			})
		default:
			if _, ok := modelSourceTier[term]; !ok {
				return nil, fmt.Errorf("--list-models: unknown filter term %q (sources: user, live, cache, catalog, speculative; add + for \"and above\"; or: available)", term)
			}
			want := term
			preds = append(preds, func(m provider.Model) bool {
				l := modelSourceLabel(m)
				if want == "live" {
					return l == "live" || l == "cache"
				}
				return l == want
			})
		}
	}
	return func(m provider.Model) bool {
		for _, p := range preds {
			if !p(m) {
				return false
			}
		}
		return true
	}, nil
}

// providerSelectable reports whether --provider <name> would resolve a
// usable credential right now — the probe behind --list-models=available.
// Keyless providers count: ollama needs no key (Resolve substitutes a
// placeholder), and openai-compatible models only appear in the list
// when an endpoint is configured. Results are memoized per provider in
// cache (the auth store hits disk).
func providerSelectable(name string, cache map[string]bool) bool {
	if v, ok := cache[name]; ok {
		return v
	}
	var ok bool
	switch name {
	case "ollama", "openai-compatible":
		ok = true
	default:
		_, _, err := build.ResolveCredential(name, "")
		ok = err == nil
	}
	cache[name] = ok
	return ok
}

func printModels(filter string) error {
	credCache := map[string]bool{}
	keep, err := modelListFilter(filter, func(p string) bool { return providerSelectable(p, credCache) })
	if err != nil {
		return err
	}
	var models []provider.Model
	for _, m := range provider.Active() {
		if keep(m) {
			models = append(models, m)
		}
	}

	// Compute column widths from actual data so wide providers (e.g.
	// xiaomi-token-plan-sgp) and long bedrock model ids don't force the
	// `name` column off-screen. Floors mirror the historical layout so
	// short catalogs look the same as before.
	provW, idW, srcW := len("provider"), len("model id"), len("source")
	for _, m := range models {
		if w := len(m.Provider); w > provW {
			provW = w
		}
		if w := len(m.ID); w > idW {
			idW = w
		}
		if w := len(modelSourceLabel(m)); w > srcW {
			srcW = w
		}
	}

	header := fmt.Sprintf("%-*s  %-*s  %8s  %8s  %s  %s  %-*s  %s",
		provW, "provider",
		idW, "model id",
		"context", "max-out", "reasoning", "vision",
		srcW, "source",
		"name")
	fmt.Println(header)

	for _, m := range models {
		reason := " "
		if m.Has(provider.CapReasoning) {
			reason = "✓"
		}
		vision := " "
		if m.Has(provider.CapImageInput) {
			vision = "✓"
		}
		fmt.Printf("%-*s  %-*s  %8d  %8d     %s         %s     %-*s  %s\n",
			provW, m.Provider,
			idW, m.ID,
			m.ContextWindow, m.MaxOutput,
			reason, vision,
			srcW, modelSourceLabel(m),
			m.DisplayName)
	}
	if len(models) == 0 && filter != "" {
		fmt.Fprintf(os.Stderr, "no models match --list-models=%s\n", filter)
	}
	return nil
}
