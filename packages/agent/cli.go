package agent

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"terva.sh/terva/packages/agent/chat/external"
	"terva.sh/terva/packages/agent/extensions"
	"terva.sh/terva/packages/agent/extproto"
	"terva.sh/terva/packages/agent/modes"
	"terva.sh/terva/packages/agent/skills"
	"terva.sh/terva/packages/agent/swarm"
	"terva.sh/terva/packages/agent/tools"
	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/envcompat"
	"terva.sh/terva/packages/provider"
	"terva.sh/terva/packages/provider/auth"
	"terva.sh/terva/packages/tui"
)

// interactiveExtHooks is a tiny adapter that lets the extension
// manager call back into the Interactive instance built later in
// runInteractive. The forward-declared *modes.Interactive is filled
// in immediately after manager construction.
type interactiveExtHooks struct {
	ivPtr **modes.Interactive
}

func (h *interactiveExtHooks) iv() *modes.Interactive {
	if h == nil || h.ivPtr == nil {
		return nil
	}
	return *h.ivPtr
}

func (h *interactiveExtHooks) Notify(extName, level, message string) {
	if iv := h.iv(); iv != nil {
		iv.Notify(extName, level, message)
	}
}
func (h *interactiveExtHooks) Submit(text string) {
	if iv := h.iv(); iv != nil {
		iv.Submit(text)
	}
}
func (h *interactiveExtHooks) SubmitSlash(text string) {
	if iv := h.iv(); iv != nil {
		iv.SubmitSlash(text)
	}
}
func (h *interactiveExtHooks) Insert(text string) {
	if iv := h.iv(); iv != nil {
		iv.Insert(text)
	}
}
func (h *interactiveExtHooks) Display(extName, text string) {
	if iv := h.iv(); iv != nil {
		iv.Display(extName, text)
	}
}
func (h *interactiveExtHooks) ClearNotes(extName string) {
	if iv := h.iv(); iv != nil {
		iv.ClearNotes(extName)
	}
}
func (h *interactiveExtHooks) OpenPanel(extName string, spec extproto.PanelSpec) {
	if iv := h.iv(); iv != nil {
		iv.OpenPanel(extName, spec)
	}
}
func (h *interactiveExtHooks) UpdatePanel(extName, panelID, title string, lines []string, footer string) {
	if iv := h.iv(); iv != nil {
		iv.UpdatePanel(extName, panelID, title, lines, footer)
	}
}
func (h *interactiveExtHooks) ClosePanel(extName, panelID string) {
	if iv := h.iv(); iv != nil {
		iv.ClosePanel(extName, panelID)
	}
}

// extToolAdapter bridges *extensions.Manager to the
// ExtensionToolSource interface declared in build.go (kept narrow to
// avoid a build->extensions import cycle). One adapter instance per
// run; used at every Resolve point so re-built agents pick up the
// same set of extension tools.
type extToolAdapter struct {
	mgr *extensions.Manager
}

func (a *extToolAdapter) Tools() []ExtensionToolInfo {
	infos := a.mgr.Tools()
	out := make([]ExtensionToolInfo, len(infos))
	for i, t := range infos {
		out[i] = ExtensionToolInfo{
			Extension:   t.Extension,
			Name:        t.Name,
			Description: t.Description,
			Schema:      t.Schema,
		}
	}
	return out
}

func (a *extToolAdapter) NewExtensionTool(info ExtensionToolInfo) core.Tool {
	return extensions.NewTool(a.mgr, extensions.ToolInfo{
		Extension:   info.Extension,
		Name:        info.Name,
		Description: info.Description,
		Schema:      info.Schema,
	})
}

// fanoutAgentEvent translates a core.AgentEvent into the wire-format
// EventFromHost and pushes it through the extension manager. Only
// the events that have a clear extension-facing meaning are
// forwarded; internal-only ones (text_delta, tool_progress) are
// dropped to keep the per-extension stream sane.
func trimMessagesForResume(msgs []provider.Message, keepTail int) []provider.Message {
	if keepTail <= 0 || len(msgs) <= keepTail {
		return provider.RepairOrphanedToolResults(msgs)
	}
	var out []provider.Message
	start := len(msgs) - keepTail
	// Preserve the synthetic compaction summary when present so an
	// already-compacted session stays compacted after resume.
	if len(msgs) > 0 && msgs[0].Meta["compaction"] == "true" && start > 1 {
		out = append(out, msgs[0])
	}
	// Avoid hydrating a tail that starts with orphan tool_result rows;
	// provider APIs require those to be paired with an earlier tool_use.
	for start < len(msgs) && msgs[start].Role == provider.RoleTool {
		start++
	}
	out = append(out, msgs[start:]...)
	return provider.RepairOrphanedToolResults(out)
}

func fanoutAgentEvent(mgr *extensions.Manager, ev core.AgentEvent) {
	if mgr == nil {
		return
	}
	switch e := ev.(type) {
	case core.EvTurnStart:
		mgr.EmitEvent(extproto.EventFromHost{Event: "turn_start", Step: e.Step})
	case core.EvToolCall:
		mgr.EmitEvent(extproto.EventFromHost{
			Event: "tool_call", ToolID: e.ID, ToolName: e.Name, ToolArgs: e.Args,
		})
	case core.EvAssistantMessage:
		// Concat the visible text portions of the message; binary
		// blocks (tool_use, etc.) are skipped because subscribers
		// usually want a string they can grep / display.
		var text string
		for _, c := range e.Message.Content {
			if tb, ok := c.(provider.TextBlock); ok {
				text += tb.Text
			}
		}
		mgr.EmitEvent(extproto.EventFromHost{Event: "assistant_message", Text: text})
	case core.EvTurnEnd:
		ev := extproto.EventFromHost{Event: "turn_end", Stop: string(e.Stop)}
		if e.Err != nil {
			ev.Error = e.Err.Error()
		}
		mgr.EmitEvent(ev)
	}
}

// Run is the top-level entrypoint for the terva binary.
func Run(rawArgs []string, version string) error {
	// One-shot migration hint when the data dir resolved to the
	// legacy pre-rename location (which keeps working forever).
	if note, ok := envcompat.HomeMigrationNote(); ok {
		fmt.Fprintln(os.Stderr, note)
	}

	// External chat connectors ($TERVA_HOME/connectors — global only,
	// never project-local) register before any dispatch so `terva bot`
	// and the TUI's /connect both see them alongside the compiled-in
	// services.
	external.SetTervaVersion(version)
	for _, err := range external.RegisterDiscovered(TervaHome()) {
		fmt.Fprintln(os.Stderr, "connector load:", err)
	}

	// Subcommand router: `terva bot ...` is handled separately so the
	// generic flag parser doesn't reject "bot" as a positional arg.
	if handled, err := runBotCommand(rawArgs, version); handled {
		return err
	}
	if handled, err := runExtCommand(rawArgs); handled {
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
	// `terva rpc` is shorthand for `terva --rpc` so third-party apps can
	// spawn the binary with a clean argv. Strip the leading 'rpc'
	// token and let the rest flow through the normal arg parser.
	if len(rawArgs) > 0 && rawArgs[0] == "rpc" {
		rawArgs = append([]string{"--rpc"}, rawArgs[1:]...)
	}

	args, err := ParseArgs(rawArgs)
	if err != nil {
		PrintHelp(version)
		return err
	}
	if args.Help {
		PrintHelp(version)
		return nil
	}
	if args.Version {
		fmt.Println("terva", version)
		return nil
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
	LoadUserModels()

	// Repair config.json so a stale (provider, model) pair from an
	// interrupted /model switch can't strand the user with an
	// "unknown model" error on the first turn. Runs before any UI
	// renders so the status bar shows the post-repair pair, not the
	// broken one. Silent on success.
	ValidateAndRepairConfig()

	if args.ListModels {
		printModels()
		return nil
	}

	ctx := context.Background()

	// Kick an async refresh of the live model catalog. The first run of
	// terva hits the network; subsequent runs within CacheTTL do nothing.
	RefreshModelsAsync()
	// Always re-list a configured openai-compatible endpoint (not cache
	// gated): a local server's loaded models change frequently.
	RefreshCompatModelsAsync()

	switch args.Mode {
	case ModePrint:
		return runPrintMode(ctx, args, version)
	case ModeJSON:
		return runJSONMode(ctx, args, version)
	case ModeRPC:
		return runRPCMode(ctx, args, version)
	case ModeSwarmAgent:
		return runSwarmAgentMode(ctx, args, version)
	default:
		return runInteractive(ctx, args, version)
	}
}

// ---- print / json modes: require credentials, run single-shot ----

// nonInteractiveExtHooks is the HostHooks impl used by print / json
// modes. They have no TUI, so notify / display go to stderr and
// submit / insert are no-ops (the extension can't steer a
// single-shot run once it's in flight anyway).
type nonInteractiveExtHooks struct{}

func (nonInteractiveExtHooks) Notify(ext, level, message string) {
	fmt.Fprintf(os.Stderr, "[%s] %s: %s\n", ext, level, message)
}
func (nonInteractiveExtHooks) Submit(string)                                        {}
func (nonInteractiveExtHooks) SubmitSlash(string)                                   {}
func (nonInteractiveExtHooks) Insert(string)                                        {}
func (nonInteractiveExtHooks) Display(string, string)                               {}
func (nonInteractiveExtHooks) ClearNotes(string)                                    {}
func (nonInteractiveExtHooks) OpenPanel(string, extproto.PanelSpec)                 {}
func (nonInteractiveExtHooks) UpdatePanel(string, string, string, []string, string) {}
func (nonInteractiveExtHooks) ClosePanel(string, string)                            {}

// setupNonInteractiveExtensions loads --ext paths and (unless
// --no-ext) runs discovery. Returns the manager so the caller can
// wire tools into the resolved registry, and a cleanup closure to
// defer. Mirrors the interactive-mode setup minus the TUI hooks.
func setupNonInteractiveExtensions(ctx context.Context, args Args, r *Resolved, version string) (*extensions.Manager, func()) {
	extMgr := extensions.New(TervaHome(), r.CWD, version, r.Provider, r.Model, nonInteractiveExtHooks{})
	for _, e := range extMgr.LoadExplicit(ctx, args.Exts) {
		fmt.Fprintln(os.Stderr, "extension load:", e)
	}
	if !args.NoExt {
		for _, e := range extMgr.Discover(ctx) {
			fmt.Fprintln(os.Stderr, "extension load:", e)
		}
	}
	extMgr.WaitForReady(3 * time.Second)
	r.MergeExtensionTools(&extToolAdapter{mgr: extMgr})
	extMgr.EmitEvent(extproto.EventFromHost{Event: "session_start"})
	return extMgr, func() { extMgr.Stop(2 * time.Second) }
}

// headlessConfirmGate returns the confirmation gate for a headless
// mode (print / json / rpc / swarm-agent) when --no-yolo is set, or nil
// otherwise. There is no interactive prompt in these modes, so the gate
// is constructed with a nil inner Confirmer: every tool call is refused
// with a model-readable reason (see core.ConfirmGate.Check) instead of
// running unconfirmed. A one-line stderr note tells the human why the
// tools are being refused; the actual gating happens in the
// BeforeToolExecute closure that calls gate.Check first.
func headlessConfirmGate(args Args, mode string) *core.ConfirmGate {
	if !args.NoYolo {
		return nil
	}
	fmt.Fprintf(os.Stderr, "note: --no-yolo in %s mode refuses tool calls (no interactive prompt available to confirm them)\n", mode)
	return core.NewConfirmGate(nil)
}

// wireNonInteractiveAgentExtHooks installs the same BeforeToolExecute
// / BeforeTurn / BeforeAssistantMessage / OnEvent hooks the
// interactive path wires up, so extensions get their normal
// event-intercept surface in print / json / rpc flows too. When gate
// is non-nil (--no-yolo) it runs FIRST in BeforeToolExecute, mirroring
// interactive mode, so a refusal short-circuits before the extension
// intercept sees the call.
func wireNonInteractiveAgentExtHooks(ctx context.Context, ag *core.Agent, extMgr *extensions.Manager, gate *core.ConfirmGate) {
	if ag == nil || extMgr == nil {
		return
	}
	ag.BeforeToolExecute = func(call provider.ToolCallBlock) (bool, string, json.RawMessage) {
		if gate != nil {
			ok, reason, _ := gate.Check(call.Name, core.BuildPreview(call.Arguments, 120))
			if !ok {
				return false, reason, nil
			}
		}
		res := extMgr.InterceptToolCall(ctx, call.ID, call.Name, call.Arguments)
		if res.Block {
			return false, res.Reason, nil
		}
		return true, "", res.ModifiedArgs
	}
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
	ag.OnEvent = func(ev core.AgentEvent) { fanoutAgentEvent(extMgr, ev) }
}

func runPrintMode(ctx context.Context, args Args, version string) error {
	confirmGate := headlessConfirmGate(args, "print")
	r, err := Resolve(args, true)
	if err != nil {
		return err
	}
	extMgr, stopExt := setupNonInteractiveExtensions(ctx, args, &r, version)
	defer stopExt()

	ag := r.NewAgent()
	wireNonInteractiveAgentExtHooks(ctx, ag, extMgr, confirmGate)
	sess, _ := openOrCreateSession(args, r, ag, version)
	defer sess.Close()

	prompt := args.Prompt
	if prompt == "" {
		piped, _ := readAllStdin()
		prompt = strings.TrimSpace(piped)
	}
	if prompt == "" {
		return fmt.Errorf("print mode requires a prompt (arg or stdin)")
	}

	start := len(ag.Messages())
	err = modes.RunPrint(ctx, ag, prompt, nil, os.Stdout)
	WriteNewTranscript(ag, sess, start)
	return err
}

func runJSONMode(ctx context.Context, args Args, version string) error {
	confirmGate := headlessConfirmGate(args, "json")
	r, err := Resolve(args, true)
	if err != nil {
		return err
	}
	extMgr, stopExt := setupNonInteractiveExtensions(ctx, args, &r, version)
	defer stopExt()

	ag := r.NewAgent()
	wireNonInteractiveAgentExtHooks(ctx, ag, extMgr, confirmGate)
	sess, _ := openOrCreateSession(args, r, ag, version)
	defer sess.Close()

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

// ---- interactive mode: opens the TUI even without credentials ----

func runInteractive(ctx context.Context, args Args, version string) error {
	// Resolve WITHOUT requiring credentials.
	r, err := Resolve(args, false)
	if err != nil {
		return err
	}

	authStore := AuthStoreFor()
	mgr := auth.NewManager(authStore)
	defer mgr.Close()

	// Keep the sandbox pointer stable across agent rebuilds (login / model
	// switch). The Interactive UI toggles the lock via this pointer, and
	// rebuilt tool instances must share the same one so the lock sticks.
	sharedSandbox := r.Sandbox

	// Build the extension manager BEFORE the agent so we can fold
	// extension-defined tools into the registry. Forward-declare iv so
	// the host hooks adapter can dereference it after construction.
	var iv *modes.Interactive
	extHooks := &interactiveExtHooks{ivPtr: &iv}
	extMgr := extensions.New(TervaHome(), r.CWD, version, r.Provider, r.Model, extHooks)
	// --ext paths first so they win against installed extensions of
	// the same name (loadOne's first-write-wins semantics).
	for _, e := range extMgr.LoadExplicit(ctx, args.Exts) {
		fmt.Fprintln(os.Stderr, "extension load:", e)
	}
	// --no-ext skips the global + project-local discovery scan;
	// explicit --ext paths above are still honoured so you can run
	// "only this extension" with --no-ext --ext ./x.
	if !args.NoExt {
		for _, e := range extMgr.Discover(ctx) {
			fmt.Fprintln(os.Stderr, "extension load:", e)
		}
	}
	// Wait briefly for extensions to flush their initial register_tool
	// frames before we build the agent's tool registry. Half a second
	// is plenty for any extension that's actually well-behaved; ones
	// that don't send a ready frame eat the full grace and proceed.
	// 3s is the per-extension grace period for the ready frame.
	// Native binaries are instant; runtimes like `npx tsx` take ~1.5s
	// from cold cache. The wait is tight only for extensions that
	// haven't sent ready by then; ones that signalled earlier release
	// the wait immediately.
	extMgr.WaitForReady(3 * time.Second)
	defer extMgr.Stop(2 * time.Second)

	extToolAdapter := &extToolAdapter{mgr: extMgr}
	r.MergeExtensionTools(extToolAdapter)

	// Build the swarm supervisor BEFORE the agent so the auto-swarm
	// tool can reference it during tool-registry construction. State
	// lives under TervaHome/swarm so per-agent meta/events survive
	// restarts; the user can hunt orphaned agents down with
	// `git worktree list` if anything misbehaves.
	//
	// swarmMgr is also captured by loadSession / changeCWD closures
	// further down the function, which is why we keep the variable
	// in this outer scope rather than scoping it tighter.
	var swarmMgr *swarm.Swarm
	swarmMgr = swarm.New(swarm.Config{
		Root:     filepath.Join(TervaHome(), "swarm"),
		RepoRoot: r.CWD,
	})
	// Pull any previously-spawned agents off disk so the dashboard
	// shows them as detached and the user can resume / remove them.
	_, _ = swarmMgr.Reload()

	// onSpawnedSwarm is the OnSpawned callback the swarm_spawn tool
	// fires after every successful spawn. It hands the agent off to
	// the running Interactive so the watcher can flush a summary back
	// into chat when all sub-agents finish. Reads `iv` lazily because
	// the Interactive is constructed after the agent.
	onSpawnedSwarm := func(a *swarm.Agent, task string) {
		if iv != nil {
			iv.TrackSwarmAgent(a, task)
		}
	}

	// Inject the swarm_spawn auto-swarm tool only when /settings ->
	// auto-swarm is currently enabled. Registering it unconditionally
	// leaves the model trying to call it (and getting a polite error)
	// even when the user has switched the feature off. The /settings
	// toggle live-mutates the running agent's registry separately so
	// flipping the flag mid-session takes effect on the next turn.
	injectSwarmSpawn := func(reg core.Registry) core.Registry {
		if reg == nil {
			return reg
		}
		if !AutoSwarmEnabled() {
			return reg
		}
		reg["swarm_spawn"] = &tools.SwarmSpawnTool{
			Swarm:     swarmMgr,
			Enabled:   AutoSwarmEnabled,
			OnSpawned: onSpawnedSwarm,
		}
		return reg
	}
	injectSwarmSpawn(r.ToolRegistry)

	// Confirmation gate: when --no-yolo is on, the agent must ask
	// the user before every tool call. In interactive mode the TUI
	// provides the Confirmer; in print/json/rpc modes there's no
	// way to prompt, so the gate is constructed with a nil inner
	// which auto-refuses every call with a helpful reason.
	var confirmGate *core.ConfirmGate
	if args.NoYolo {
		confirmGate = core.NewConfirmGate(nil) // set below for interactive
	}

	// Capture current args in a closure so BuildAgent can re-resolve
	// after a successful login (picks up the newly stored credential).
	wireAgentExt := func(a *core.Agent) *core.Agent {
		if a == nil {
			return a
		}
		a.BeforeToolExecute = func(call provider.ToolCallBlock) (bool, string, json.RawMessage) {
			// Confirm gate runs FIRST: if the user refused, we don't
			// waste extension-intercept time or let guards see the call.
			if confirmGate != nil {
				ok, reason, _ := confirmGate.Check(call.Name, core.BuildPreview(call.Arguments, 120))
				if !ok {
					return false, reason, nil
				}
			}
			r := extMgr.InterceptToolCall(ctx, call.ID, call.Name, call.Arguments)
			if r.Block {
				return false, r.Reason, nil
			}
			return true, "", r.ModifiedArgs
		}
		a.BeforeTurn = func(step int) (bool, string) {
			r := extMgr.InterceptTurnStart(ctx, step)
			return !r.Block, r.Reason
		}
		a.BeforeAssistantMessage = func(text string) (bool, string, string) {
			r := extMgr.InterceptAssistantMessage(ctx, text)
			if r.Block {
				return false, r.Reason, ""
			}
			return true, "", r.ReplaceText
		}
		a.OnEvent = func(ev core.AgentEvent) { fanoutAgentEvent(extMgr, ev) }
		return a
	}

	buildAgent := func() (*core.Agent, string, string, error) {
		resolved, err := Resolve(args, true)
		if err != nil {
			return nil, "", "", err
		}
		resolved.UseSandbox(sharedSandbox)
		resolved.MergeExtensionTools(extToolAdapter)
		injectSwarmSpawn(resolved.ToolRegistry)
		return wireAgentExt(resolved.NewAgent()), resolved.Provider, resolved.Model, nil
	}

	// Rebuild agent with an explicit provider/model override.
	buildAgentFor := func(providerOverride, modelOverride string) (*core.Agent, string, string, error) {
		next := args
		if providerOverride != "" {
			next.Provider = providerOverride
		}
		if modelOverride != "" {
			next.Model = modelOverride
		}
		resolved, err := Resolve(next, true)
		if err != nil {
			return nil, "", "", err
		}
		resolved.UseSandbox(sharedSandbox)
		resolved.MergeExtensionTools(extToolAdapter)
		injectSwarmSpawn(resolved.ToolRegistry)
		return wireAgentExt(resolved.NewAgent()), resolved.Provider, resolved.Model, nil
	}

	// Rebuild agent for the rescue picker after a recoverable failure.
	// Unlike buildAgentFor, this drops launch-time --api-key and
	// --base-url overrides because those are typically the cause of the
	// rescue (a bad key, a typo'd base URL, or a corporate gateway that
	// only the originally-picked provider needed). Re-resolving without
	// them lets the rescue retry use env vars / auth.json / provider
	// defaults the way terva would have without the overrides.
	buildAgentForRescue := func(providerOverride, modelOverride string) (*core.Agent, string, string, error) {
		next := args
		next.APIKey = ""
		next.BaseURL = ""
		if providerOverride != "" {
			next.Provider = providerOverride
		}
		if modelOverride != "" {
			next.Model = modelOverride
		}
		resolved, err := Resolve(next, true)
		if err != nil {
			return nil, "", "", err
		}
		resolved.UseSandbox(sharedSandbox)
		resolved.MergeExtensionTools(extToolAdapter)
		injectSwarmSpawn(resolved.ToolRegistry)
		return wireAgentExt(resolved.NewAgent()), resolved.Provider, resolved.Model, nil
	}

	var ag *core.Agent
	if r.HasCredential() {
		ag = wireAgentExt(r.NewAgent())
	}

	// /reload-ext callback: after the manager has respawned every
	// extension, re-resolve the tool registry (built-ins + freshly-
	// registered extension tools) and swap it onto the current
	// agent in-place. The current agent may have been replaced by a
	// /model swap since spawn, so re-read the live `ag` on each
	// invocation.
	extMgr.SetOnReload(func() {
		current := ag
		if current == nil {
			return
		}
		resolved, err := Resolve(args, true)
		if err != nil {
			return
		}
		resolved.UseSandbox(sharedSandbox)
		resolved.MergeExtensionTools(extToolAdapter)
		injectSwarmSpawn(resolved.ToolRegistry)
		current.SetTools(resolved.ToolRegistry)
	})

	// Fire session_start once we know the manager's running.
	extMgr.EmitEvent(extproto.EventFromHost{Event: "session_start"})

	var sess *core.Session
	var sessBaselineMsgs int // messages already on disk when current session opened
	// persistMu guards sess + sessBaselineMsgs against concurrent access
	// from the agent loop's per-message persistence hook (runs on the
	// agent goroutine) and the TUI's session swap / flush callbacks
	// (run on the TUI goroutine). Without this, a /sessions swap that
	// races with a finishing turn could double-write or lose messages.
	var persistMu sync.Mutex
	if !args.NoSess && ag != nil {
		sess, _ = openOrCreateSession(args, r, ag, version)
		if ag != nil {
			sessBaselineMsgs = len(ag.Messages())
		}
	}
	defer func() {
		persistMu.Lock()
		defer persistMu.Unlock()
		if sess != nil {
			sess.Close()
		}
	}()

	// persistMessage is the per-message hook bound to the agent. It
	// appends each new transcript message to the live session as soon
	// as it lands, so a kill / closed terminal / OS crash costs at
	// most the in-flight turn instead of the whole session. The
	// baseline counter advances in lock-step so the exit-time flush
	// doesn't double-write rows already on disk.
	persistMessage := func(m provider.Message) {
		persistMu.Lock()
		defer persistMu.Unlock()
		if sess == nil {
			return
		}
		if err := sess.AppendMessage(m); err == nil {
			sessBaselineMsgs++
		}
	}
	persistUsage := func(cum provider.Usage) {
		persistMu.Lock()
		defer persistMu.Unlock()
		if sess == nil {
			return
		}
		_ = sess.AppendUsage(cum, cum)
	}
	persistCompaction := func(messages []provider.Message) {
		persistMu.Lock()
		defer persistMu.Unlock()
		if sess == nil {
			return
		}
		if err := sess.AppendCompaction(messages); err == nil {
			sessBaselineMsgs = len(messages)
		}
	}
	wireAgentPersist := func(a *core.Agent) *core.Agent {
		if a == nil {
			return a
		}
		a.OnMessageAppended = persistMessage
		a.OnUsage = persistUsage
		a.OnTranscriptCompacted = persistCompaction
		return a
	}
	wireAgentPersist(ag)

	// Re-wrap the build closures so any agent constructed by the TUI
	// (login, /model swap to a different provider) also gets the
	// persistence hooks. Without this, switching provider would
	// silently revert to the old in-memory-only behaviour.
	baseBuildAgent := buildAgent
	buildAgent = func() (*core.Agent, string, string, error) {
		a, p, m, err := baseBuildAgent()
		return wireAgentPersist(a), p, m, err
	}
	baseBuildAgentFor := buildAgentFor
	buildAgentFor = func(providerOverride, modelOverride string) (*core.Agent, string, string, error) {
		a, p, m, err := baseBuildAgentFor(providerOverride, modelOverride)
		return wireAgentPersist(a), p, m, err
	}
	baseBuildAgentForRescue := buildAgentForRescue
	buildAgentForRescue = func(providerOverride, modelOverride string) (*core.Agent, string, string, error) {
		a, p, m, err := baseBuildAgentForRescue(providerOverride, modelOverride)
		return wireAgentPersist(a), p, m, err
	}

	// loadSession replaces the current session with the one at path and
	// hands its messages to the agent. Used by the /sessions picker.
	loadSession := func(path string) error {
		currentAg := ag // captured
		if currentAg == nil {
			return fmt.Errorf("no agent running; log in first")
		}
		newSess, msgs, err := core.OpenSession(path)
		if err != nil {
			return err
		}
		fullMsgCount := len(msgs)
		msgs = trimMessagesForResume(msgs, 100)
		persistMu.Lock()
		// Flush any unsaved messages to the old session before swapping.
		// Per-message persistence keeps sessBaselineMsgs current, so
		// this is a defensive no-op in the common case; it still
		// matters for the rare race where a turn just finished and
		// hadn't fired its hook yet.
		if sess != nil {
			writeNewTranscriptLocked(currentAg, sess, sessBaselineMsgs)
			_ = sess.Close()
		}
		sess = newSess
		currentAg.SetMessages(msgs)
		if cum, last, uerr := core.SessionUsageDetail(path); uerr == nil {
			currentAg.SeedCost(cum)
			currentAg.SeedLastTurnUsage(last)
		}
		// The live agent only receives a compact resume window, but
		// the session file remains intact. Keep the persistence
		// baseline at the original on-disk message count so future
		// turns append after the full session instead of duplicating
		// the hydrated tail.
		sessBaselineMsgs = fullMsgCount
		persistMu.Unlock()
		// Re-scope the swarm dashboard to the new session so /swarm
		// only shows agents this session spawned. swarmMgr may be nil
		// here if we haven't reached the construction site yet (it
		// shouldn't be, since the interactive loop is what triggers
		// loadSession, but the nil check is cheap insurance).
		if swarmMgr != nil && newSess != nil {
			swarmMgr.SetActiveSession(newSess.ID)
		}
		return nil
	}

	// changeCWD switches the running session to a new working directory.
	// Wired into InteractiveConfig.ChangeCWD and invoked by the hidden
	// /cd slash command (which itself is only fired by the workspaces
	// extension's panel-key Enter handler today; the user can type /cd
	// directly but it's not in autocomplete / help / the README).
	//
	// Steps, in order:
	//   1. resolve + validate the new path (~ expansion, abs/rel)
	//   2. close the current session, flushing pending messages
	//   3. mutate captured args.CWD + r.CWD so future buildAgent
	//      calls bind to the new cwd
	//   4. re-root the shared sandbox («·«) so /jail follows the
	//      session into the new cwd instead of widening or silently
	//      dropping
	//   5. rebuild the agent via buildAgent() so tools, AGENTS.md
	//      addendum, system prompt, sessions dir all bind correctly
	//   6. open a fresh session in the new cwd's bucket
	//   7. push the new state into the running Interactive
	//   8. re-scope the swarm dashboard to the freshly-opened session
	//
	// The /jail state is preserved verbatim: if the sandbox was locked
	// to the old cwd, it stays locked, just re-pointed at the new one.
	changeCWD := func(path string) error {
		if path == "" {
			return fmt.Errorf("empty path")
		}
		// ~ expansion.
		if path == "~" || strings.HasPrefix(path, "~/") {
			home, herr := os.UserHomeDir()
			if herr != nil || home == "" {
				return fmt.Errorf("cannot expand ~: %v", herr)
			}
			if path == "~" {
				path = home
			} else {
				path = filepath.Join(home, path[2:])
			}
		}
		if !filepath.IsAbs(path) {
			path = filepath.Join(args.CWD, path)
		}
		absPath, err := filepath.Abs(path)
		if err != nil {
			return err
		}
		info, err := os.Stat(absPath)
		if err != nil {
			return err
		}
		if !info.IsDir() {
			return fmt.Errorf("not a directory: %s", absPath)
		}

		currentAg := ag
		if currentAg == nil {
			return fmt.Errorf("no agent running; log in first")
		}

		// Close the current session before we drop the reference.
		// Per-message persistence keeps it current already; this is
		// a defensive flush + final fsync via Close.
		persistMu.Lock()
		if sess != nil {
			writeNewTranscriptLocked(currentAg, sess, sessBaselineMsgs)
			_ = sess.Close()
			sess = nil
		}
		sessBaselineMsgs = 0
		persistMu.Unlock()

		// Mutate captured state so subsequent agent rebuilds and
		// session opens see the new cwd.
		wasJailed := sharedSandbox != nil && sharedSandbox.Locked()
		args.CWD = absPath
		r.CWD = absPath
		if sharedSandbox != nil {
			sharedSandbox.Root = absPath
			if wasJailed {
				sharedSandbox.Lock()
			} else {
				sharedSandbox.Unlock()
			}
		}

		// Rebuild the agent so tools / AGENTS.md / system prompt
		// re-bind to the new cwd. buildAgent() reads from args + r.
		newAg, newProvider, newModel, berr := buildAgent()
		if berr != nil {
			return fmt.Errorf("rebuild agent: %v", berr)
		}
		ag = newAg

		// Fresh session in the new cwd's bucket. We bypass
		// openOrCreateSession's --continue / --resume branches
		// because /cd's semantics are "start fresh here", matching
		// what relaunching `terva --cwd <path>` would do today.
		if !args.NoSess {
			core.PruneEmptySessions(TervaHome(), absPath)
			newSess, serr := core.NewSession(TervaHome(), absPath, newProvider, newModel, version)
			if serr != nil {
				return fmt.Errorf("open session in %s: %v", absPath, serr)
			}
			persistMu.Lock()
			sess = newSess
			sessBaselineMsgs = 0
			persistMu.Unlock()
		}

		// Push the new state into the running Interactive.
		if iv != nil {
			iv.ApplyChangedCWD(newAg, newProvider, newModel, absPath)
		}

		// Re-scope the swarm dashboard to the new session.
		if swarmMgr != nil && sess != nil {
			swarmMgr.SetActiveSession(sess.ID)
		}
		return nil
	}

	// newSession closes the current session and opens a fresh one in the
	// same cwd — the in-place equivalent of relaunching terva, and what
	// changeCWD does minus the directory change. The agent keeps its
	// provider/model/tools (no rebuild); only its transcript and running
	// cost are reset. The outgoing session is flushed and stays on disk
	// (PruneEmptySessions reclaims it only if it was empty). Wired into
	// InteractiveConfig.NewSession and invoked by /new.
	newSession := func(providerName, model string) error {
		currentAg := ag
		if currentAg == nil {
			return fmt.Errorf("no agent running; log in first")
		}
		if providerName == "" {
			providerName = r.Provider
		}
		if model == "" {
			model = currentAg.Model
		}

		// Flush + close the outgoing session so its transcript is whole
		// on disk before we let go of it.
		persistMu.Lock()
		if sess != nil {
			writeNewTranscriptLocked(currentAg, sess, sessBaselineMsgs)
			_ = sess.Close()
			sess = nil
		}
		sessBaselineMsgs = 0
		persistMu.Unlock()

		// Reset the live conversation: empty transcript, zeroed meters.
		currentAg.SetMessages(nil)
		currentAg.SeedCost(provider.Usage{})
		currentAg.SeedLastTurnUsage(provider.Usage{})

		// Fresh session in the same cwd's bucket. With --no-session the
		// transcript reset above is the whole effect.
		if !args.NoSess {
			core.PruneEmptySessions(TervaHome(), args.CWD)
			ns, serr := core.NewSession(TervaHome(), args.CWD, providerName, model, version)
			if serr != nil {
				return fmt.Errorf("open new session: %v", serr)
			}
			persistMu.Lock()
			sess = ns
			sessBaselineMsgs = 0
			persistMu.Unlock()
			if swarmMgr != nil {
				swarmMgr.SetActiveSession(ns.ID)
			}
		}
		return nil
	}

	term := tui.NewProcTerm()

	// Kick off the async update check so the banner can appear when the
	// http response eventually arrives (usually <1s on cached DNS). Map
	// agent.UpdateInfo -> modes.UpdateInfo here to avoid a cyclic import.
	updateCh := make(chan modes.UpdateInfo, 1)
	go func() {
		defer close(updateCh)
		src := <-CheckForUpdateAsync(TervaHome(), version)
		updateCh <- modes.UpdateInfo{
			Current:   src.Current,
			Latest:    src.Latest,
			Available: src.Available,
			URL:       src.URL,
		}
	}()

	// Changelog: when the running version differs from the last
	// version whose release notes the user dismissed, fetch the
	// release body from GitHub and have the TUI show it once. On
	// first-ever launch (no prior LastChangelogShown), seed the
	// stored version silently — don't dump release notes at someone
	// who just installed.
	changelogCh := make(chan modes.ChangelogPayload, 1)
	go func() {
		defer close(changelogCh)
		cfg, _ := LoadConfig()
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
		// For dev builds (0.0.0), skip if the latest release was
		// already shown (stored by the dismiss callback).
		if version == "0.0.0" && info.Version == cfg.LastChangelogShown {
			return
		}
		changelogCh <- modes.ChangelogPayload{
			Version: info.Version,
			Body:    info.Body,
			URL:     info.URL,
		}
	}()

	initialCfg, _ := LoadConfig()
	theme, _, themeErr := tui.DetectThemeWithCustom(TervaHome(), initialCfg.Theme, 80*time.Millisecond)
	if themeErr != nil {
		fmt.Fprintln(os.Stderr, "theme load:", themeErr)
		if initialCfg.Theme != "" && !tui.ThemeExists(TervaHome(), initialCfg.Theme) {
			initialCfg.Theme = ""
			_ = SaveConfig(initialCfg)
		}
	}

	// swarmMgr was constructed and reloaded earlier (before the agent
	// build, so the auto-swarm tool could capture it). Here we just
	// scope the dashboard to the active host session so /swarm only
	// shows agents this session spawned (and any pre-upgrade unscoped
	// agents — see SnapshotAll docs). Updated again whenever the
	// user swaps sessions via loadSession below.
	if sess != nil {
		swarmMgr.SetActiveSession(sess.ID)
	}
	// Best-effort shutdown on interactive exit: stop all running
	// agents so they don't outlive their parent terva.
	defer swarmMgr.StopAll()

	// /migrate hooks: the dialog drives the stages, these closures run
	// the engine (migrate.go). migPlan/migReport thread state between
	// the calls; migrationExited flips when the legacy dir — including
	// the live session file — was deleted, so the post-Run flush must
	// be skipped and the user told to restart.
	var migPlan MigrationPlan
	var migReport MigrationCopyReport
	migrationExited := false
	migration := &modes.MigrationHooks{
		Plan: func() modes.MigrationState {
			migPlan = PlanMigration(r.CWD)
			return modes.MigrationState{
				OldDir:            migPlan.OldDir,
				NewDir:            migPlan.NewDir,
				EnvNote:           migPlan.EnvNote,
				ProjectOldDir:     migPlan.ProjectOldDir,
				ProjectNewDir:     migPlan.ProjectNewDir,
				ProjectConflict:   migPlan.ProjectConflict,
				UserDirApplicable: migPlan.UserDirApplicable(),
				ProjectApplicable: migPlan.ProjectApplicable(),
				AlreadyMigrated:   migPlan.AlreadyMigrated,
				NothingToDo:       migPlan.NothingToDo(),
			}
		},
		CopyUserData: func() (modes.MigrationCopyResult, error) {
			migReport = CopyUserData(migPlan.OldDir, migPlan.NewDir)
			if migReport.Clean() {
				if err := FinalizeMigration(); err != nil {
					return modes.MigrationCopyResult{}, err
				}
			}
			return modes.MigrationCopyResult{
				FilesCopied:     migReport.FilesCopied,
				SymlinksCopied:  migReport.SymlinksCopied,
				SkippedExisting: len(migReport.SkippedExisting),
				Errors:          migReport.Errors,
				Clean:           migReport.Clean(),
			}, nil
		},
		Finalize: FinalizeMigration,
		RemoveOldDir: func() error {
			if err := RemoveOldUserDir(migPlan, migReport); err != nil {
				return err
			}
			migrationExited = true
			return nil
		},
		RenameProject: func() error { return RenameProjectDir(migPlan) },
	}

	iv = modes.NewInteractive(modes.InteractiveConfig{
		Terminal:                   term,
		Theme:                      theme,
		InlineImagesEnabled:        initialCfg.InlineImagesEnabled,
		AutoSwarmEnabled:           initialCfg.AutoSwarmEnabled,
		RecursiveFileSuggest:       initialCfg.RecursiveFileSuggest,
		RespectGitignore:           initialCfg.RespectGitignore,
		ThemeName:                  initialCfg.Theme,
		ExtensionThemes:            extMgr.ThemeOptions,
		AutoSwarmSystemAddendum:    AutoSwarmSystemAddendum,
		SettingsStore:              configSettingsStore{},
		Model:                      r.Model,
		Provider:                   r.Provider,
		AuthMethod:                 r.AuthMethod,
		BaseURL:                    r.BaseURL,
		Reasoning:                  r.Reasoning,
		SystemPrompt:               r.SystemPrompt,
		Tools:                      r.ToolRegistry,
		MaxSteps:                   r.MaxSteps,
		CWD:                        r.CWD,
		TervaHome:                  TervaHome(),
		Version:                    version,
		UpdateInfoChan:             updateCh,
		Sandbox:                    sharedSandbox,
		Agent:                      ag,
		InitialInput:               args.Prompt,
		AuthManager:                mgr,
		BuildAgent:                 buildAgent,
		SetKimiCLIFallbackDisabled: SetKimiCLIFallbackDisabled,
		Migration:                  migration,
		BuildAgentFor:              buildAgentFor,
		BuildAgentForRescue:        buildAgentForRescue,
		LoggedInProviders: func() []string {
			var out []string
			seen := map[string]bool{}
			for _, p := range knownProviders {
				if _, _, err := ResolveCredential(p, ""); err == nil && !seen[p] {
					out = append(out, p)
					seen[p] = true
				}
			}
			// Ollama models are always available (no auth needed).
			if !seen["ollama"] {
				out = append(out, "ollama")
			}
			// openai-compatible is reachable once configured (base URL
			// set; the API key is optional, so a keyless endpoint won't
			// have surfaced via ResolveCredential above).
			if !seen["openai-compatible"] {
				if bu, _, _ := AuthStoreFor().Extras("openai-compatible"); bu != "" {
					out = append(out, "openai-compatible")
				}
			}
			return out
		},
		LoadSession: loadSession,
		NewSession:  newSession,
		ChangeCWD:   changeCWD,
		CurrentSessionPath: func() string {
			if sess == nil {
				return ""
			}
			return sess.Path
		},
		FlushSession: func() {
			// Append any not-yet-persisted agent messages to the
			// current session file, then advance the baseline so
			// the final WriteNewTranscript at exit doesn't write
			// duplicates. Per-message persistence keeps the on-
			// disk file current already, so this is mostly a
			// defensive flush — still needed for /session export
			// to guarantee the exported bytes include the very
			// last in-flight turn.
			currentAg := iv.Agent()
			if currentAg == nil {
				return
			}
			persistMu.Lock()
			defer persistMu.Unlock()
			if sess == nil {
				return
			}
			writeNewTranscriptLocked(currentAg, sess, sessBaselineMsgs)
			sessBaselineMsgs = len(currentAg.Messages())
		},
		Extensions:    extMgr,
		Swarm:         swarmMgr,
		ChangelogChan: changelogCh,
		OnChangelogDismiss: func() {
			// For dev builds (0.0.0) store the actual release version
			// so the same changelog doesn't show again next launch.
			// For real builds, store the binary version.
			v := version
			if v == "0.0.0" {
				if iv != nil && iv.ChangelogVersion() != "" {
					v = iv.ChangelogVersion()
				}
			}
			_ = MarkChangelogShown(v)
		},
		SkillSnapshot: func() []*skills.Skill {
			if args.NoSkill {
				// --no-skill: nothing for the picker to show.
				return nil
			}
			// Re-discover so the picker reflects edits made during
			// the session. Cheap; SKILL.md files are small. Filter
			// out built-in skills — they're hidden from user-facing
			// surfaces because they're implementation detail; the
			// model still sees them through the system-prompt
			// manifest and the skill tool.
			userHome, _ := os.UserHomeDir()
			list, _ := skills.Discover(TervaHome(), r.CWD, userHome, args.WithSkills)
			return skills.VisibleSkills(list)
		},
		NoYolo:      args.NoYolo,
		ConfirmGate: confirmGate,
		PersistModel: func(providerName, model string) {
			// Update config.json so next launch uses the same pick.
			cfg, _ := LoadConfig()
			cfg.Provider = providerName
			cfg.Model = model
			_ = SaveConfig(cfg)
			// Update the active session's meta so resume picks this up.
			if sess != nil {
				_ = sess.UpdateModel(providerName, model)
			}
		},
		RefreshCompatModels: RefreshCompatModelsAsync,
	})

	// Bind the interactive TUI as the Confirmer. We deferred this
	// until now because the gate is constructed before the TUI
	// (the BeforeToolExecute closure captures it). SetConfirmer
	// is mutex-guarded on the gate so this is safe.
	if confirmGate != nil {
		confirmGate.SetConfirmer(iv)
	}

	// Signal-driven flush: a SIGTERM / SIGHUP to the terva process
	// (closed terminal window, system shutdown, kill) used to lose
	// the entire in-memory transcript because the deferred post-Run
	// flush below never ran. Per-message persistence above covers
	// most of it; this handler writes any in-flight remainder and
	// then exits the process so we don't double-paint over a
	// broken terminal that the TUI's restore deferreds can no
	// longer fix from a signal context.
	//
	// SIGINT is intentionally NOT handled here — the TUI consumes
	// Ctrl+C as a regular key event for cancel/clear semantics, and
	// installing a SIGINT notifier here would swallow it.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGHUP)
	defer signal.Stop(sigCh)
	go func() {
		_, ok := <-sigCh
		if !ok {
			return
		}
		if finalAg := iv.Agent(); finalAg != nil {
			persistMu.Lock()
			if sess != nil {
				writeNewTranscriptLocked(finalAg, sess, sessBaselineMsgs)
				sessBaselineMsgs = len(finalAg.Messages())
				_ = sess.Close()
				sess = nil
			}
			persistMu.Unlock()
		}
		// Exit cleanly. Re-raising the signal would skip os.Exit's
		// at-exit hooks; explicit exit is fine because we've already
		// flushed the only at-risk state (the session file).
		os.Exit(0)
	}()

	runErr := iv.Run(ctx)

	// Flush final transcript to session (only if we had / ended up with
	// an agent). Skipped when /migrate just deleted the legacy data dir:
	// the session file went with it, and the copy in the new dir was
	// flushed before the copy pass ran.
	if finalAg := iv.Agent(); finalAg != nil && !migrationExited {
		persistMu.Lock()
		if sess != nil {
			writeNewTranscriptLocked(finalAg, sess, sessBaselineMsgs)
			sessBaselineMsgs = len(finalAg.Messages())
		}
		persistMu.Unlock()
	}
	if migrationExited {
		fmt.Fprintf(os.Stderr, "terva: migration complete — data now lives in %s; the old zot dir was removed.\nRestart terva to continue.\n", migPlan.NewDir) // rename:keep
		if migPlan.EnvNote != "" {
			fmt.Fprintln(os.Stderr, "terva: "+migPlan.EnvNote)
		}
	}
	return runErr
}

// openOrCreateSession returns a session for the run. sess may be nil
// with a nil error if session persistence is disabled.
func openOrCreateSession(args Args, r Resolved, ag *core.Agent, version string) (*core.Session, error) {
	if args.NoSess {
		return nil, nil
	}
	// Sweep meta-only files left over from older terva versions (and from
	// any session that crashed before its first AppendMessage). Cheap;
	// reads the first few bytes of each file in the cwd's session dir.
	core.PruneEmptySessions(TervaHome(), args.CWD)
	var (
		s    *core.Session
		msgs []provider.Message
		err  error
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
		}
	case args.Continue:
		latest := core.LatestSession(TervaHome(), args.CWD)
		if latest != "" {
			s, msgs, err = core.OpenSession(latest)
		}
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
		// Startup path: stderr is still ours (no TUI yet), so surface
		// anything OpenSession had to skip instead of losing it.
		for _, w := range s.LoadWarnings {
			fmt.Fprintln(os.Stderr, "terva:", w)
		}
		ag.SetMessages(msgs)
		if cum, last, uerr := core.SessionUsageDetail(s.Path); uerr == nil {
			ag.SeedCost(cum)
			ag.SeedLastTurnUsage(last)
		}
		return s, nil
	}
	return core.NewSession(TervaHome(), args.CWD, r.Provider, r.Model, version)
}

func pickSession(cwd string) (string, error) {
	files := core.ListSessions(TervaHome(), cwd)
	if len(files) == 0 {
		fmt.Fprintln(os.Stderr, "no sessions for", cwd)
		return "", nil
	}
	for i, f := range files {
		fmt.Fprintf(os.Stderr, "  %2d) %s\n", i+1, f)
	}
	fmt.Fprint(os.Stderr, "pick #: ")
	rd := bufio.NewReader(os.Stdin)
	line, _ := rd.ReadString('\n')
	line = strings.TrimSpace(line)
	var n int
	if _, err := fmt.Sscanf(line, "%d", &n); err != nil || n < 1 || n > len(files) {
		return "", fmt.Errorf("invalid selection")
	}
	return files[n-1], nil
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
	cum := ag.Cost()
	_ = sess.AppendUsage(cum, cum)
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

func printModels() {
	models := provider.Active()

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
		source := m.Source
		if source == "" {
			source = "catalog"
		}
		if m.Speculative {
			source = "speculative"
		}
		if w := len(source); w > srcW {
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
		source := m.Source
		if source == "" {
			source = "catalog"
		}
		if m.Speculative {
			source = "speculative"
		}
		fmt.Printf("%-*s  %-*s  %8d  %8d     %s         %s     %-*s  %s\n",
			provW, m.Provider,
			idW, m.ID,
			m.ContextWindow, m.MaxOutput,
			reason, vision,
			srcW, source,
			m.DisplayName)
	}
}
