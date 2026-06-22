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
	"terva.sh/terva/packages/agent/exttool"
	"terva.sh/terva/packages/agent/hooks"
	"terva.sh/terva/packages/agent/mcp"
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
func (h *interactiveExtHooks) RefreshStatus() {
	if iv := h.iv(); iv != nil {
		iv.RefreshStatus()
	}
}
func (h *interactiveExtHooks) RefreshContext() {
	if iv := h.iv(); iv != nil {
		iv.RefreshContext()
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

// StaticContext exposes the extensions' aggregated static context
// contribution to MergeExtensionTools (optional ExtensionToolSource
// extension), which folds it into the cached system-prompt addendum.
func (a *extToolAdapter) StaticContext() string {
	return a.mgr.StaticContext()
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
			ReadOnly:    t.ReadOnly,
			Authority:   t.Authority,
		}
	}
	return out
}

func (a *extToolAdapter) NewExtensionTool(info ExtensionToolInfo) core.Tool {
	return exttool.New(a.mgr, exttool.Info{
		Extension:   info.Extension,
		Name:        info.Name,
		Description: info.Description,
		Schema:      info.Schema,
	})
}

// mcpToolAdapter bridges *mcp.Manager to the same ExtensionToolSource
// seam extension tools ride (the roadmap's "MCP adapter behind
// ExtensionToolSource" bet). MCP tools therefore inherit every
// downstream behavior for free: registry merge, system-prompt
// re-render, the confirm-gate ladder, and plan mode's
// side-effect rules (readOnlyHint-annotated tools are admitted,
// the rest excluded).
type mcpToolAdapter struct {
	mgr *mcp.Manager

	// Per-server stderr log handles are tracked here (not in a setupMCP
	// closure) so a server started LIVE via the /mcp dialog — long after
	// setupMCP returned — gets its log handle closed by the same stop func.
	// An MCP server's log left open blocks temp-dir cleanup on Windows.
	logMu    sync.Mutex
	logFiles []*os.File
}

// stderrFor opens (append) and tracks the per-server log sink. Safe for
// the several goroutines StartAll spawns, and reused by live StartOne.
func (a *mcpToolAdapter) stderrFor(server string) io.Writer {
	if mkErr := os.MkdirAll(LogsPath(), 0o755); mkErr != nil {
		return nil
	}
	f, ferr := os.OpenFile(filepath.Join(LogsPath(), "mcp-"+server+".log"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if ferr != nil {
		return nil
	}
	a.logMu.Lock()
	a.logFiles = append(a.logFiles, f)
	a.logMu.Unlock()
	return f
}

func (a *mcpToolAdapter) closeLogs() {
	a.logMu.Lock()
	for _, f := range a.logFiles {
		_ = f.Close()
	}
	a.logFiles = nil
	a.logMu.Unlock()
}

func (a *mcpToolAdapter) Tools() []ExtensionToolInfo {
	infos := a.mgr.Tools()
	out := make([]ExtensionToolInfo, len(infos))
	for i, t := range infos {
		out[i] = ExtensionToolInfo{
			Extension:   "mcp:" + t.Server,
			Name:        t.Name,
			Description: t.Description,
			Schema:      t.Schema,
			ReadOnly:    t.ReadOnly,
		}
	}
	return out
}

func (a *mcpToolAdapter) NewExtensionTool(info ExtensionToolInfo) core.Tool {
	for _, t := range a.mgr.Tools() {
		if t.Name == info.Name {
			return a.mgr.NewTool(t)
		}
	}
	return nil
}

// setupMCP starts the user-configured MCP servers — plus a TRUSTED project's
// servers (merged, user wins on a name collision) — and merges their tools into
// the resolved registry. Returns a stop func (never nil). Startup problems are
// stderr notes, never fatal — one broken server must not take the session down.
// Server stderr goes to $TERVA_HOME/logs/mcp-<name>.log, like extensions.
//
// r.Trusted is the Workspace Trust verdict: when false the project's MCP
// servers are NEVER started (only the user's), so a cloned repo cannot spawn
// subprocesses until the user trusts it (Phase 6).
func setupMCP(ctx context.Context, args Args, r *Resolved) (*mcpToolAdapter, func()) {
	if args.NoMCP {
		return nil, func() {}
	}
	user, err := LoadConfig()
	if err != nil {
		return nil, func() {}
	}
	mcpCfg := mergeMCPConfigs(user.MCP, trustedProjectMCP(args.CWD, r.Trusted))
	// Drop servers the user or (restrict-only) project has disabled before
	// they ever spawn. This is the same list the /mcp dialog writes.
	if mcpCfg != nil {
		disabled := resolvedDisableMCP(args.CWD, r.Trusted)
		for name := range mcpCfg.Servers {
			if disabled[name] {
				delete(mcpCfg.Servers, name)
			}
		}
	}
	// Build a Manager even when nothing is configured or everything is
	// disabled: the /mcp dialog can live-enable a server later via
	// StartOne, and an empty Manager is a valid no-op (no subprocesses).
	// Only --no-mcp (handled above) skips the Manager entirely.
	adapter := &mcpToolAdapter{}
	mgr := mcp.StartAll(ctx, mcpCfg, adapter.stderrFor)
	adapter.mgr = mgr
	for _, w := range mgr.Warnings() {
		fmt.Fprintln(os.Stderr, "note:", w)
	}
	r.MergeExtensionTools(adapter)
	// StopAll first so the stderr pumps have finished writing, then release
	// the log handles (including any opened by a live StartOne).
	stop := func() {
		mgr.StopAll()
		adapter.closeLogs()
	}
	return adapter, stop
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
		mgr.EmitEvent(extproto.EventFromHost{Event: extproto.EventTurnStart, Step: e.Step})
	case core.EvToolCall:
		mgr.EmitEvent(extproto.EventFromHost{
			Event: extproto.EventToolCall, ToolID: e.ID, ToolName: e.Name, ToolArgs: e.Args,
		})
	case core.EvUserMessage:
		// Surface genuine user prompts (initial and queued) so an
		// extension can observe what the human asked — the symmetric
		// counterpart to assistant_message. The synthetic at-close gate
		// nudge is suppressed: it's a host re-prompt, not the user's
		// words, and a memory/index extension must not record it as such.
		if e.Synthetic {
			return
		}
		var text string
		for _, c := range e.Message.Content {
			if tb, ok := c.(provider.TextBlock); ok {
				text += tb.Text
			}
		}
		mgr.EmitEvent(extproto.EventFromHost{Event: extproto.EventUserMessage, Text: text})
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
		mgr.EmitEvent(extproto.EventFromHost{Event: extproto.EventAssistantMessage, Text: text})
	case core.EvToolResult:
		// Fan tool results out to subscribers so an extension can
		// observe what a tool produced, not just that it was called
		// (the tool_call event carries args; this carries the
		// outcome). The result content is flattened to its text
		// blocks — subscribers want a string they can grep/display.
		mgr.EmitEvent(extproto.EventFromHost{
			Event:   extproto.EventToolResult,
			ToolID:  e.ID,
			Text:    toolResultText(e.Result),
			IsError: e.Result.IsError,
		})
	case core.EvTurnEnd:
		ev := extproto.EventFromHost{Event: extproto.EventTurnEnd, Stop: string(e.Stop)}
		if e.Err != nil {
			ev.Error = e.Err.Error()
		}
		mgr.EmitEvent(ev)
	case core.EvDone:
		// The agent finished the whole prompt (all steps, tool loops, and
		// the at-close gate done) — the per-prompt bookend to user_message,
		// distinct from the per-step turn_end. Lets an extension act when
		// the agent goes idle (summarize the exchange, run a post-turn
		// check, flush). Fires once per run, in every mode (EvDone rides
		// the wrapped Prompt sink).
		mgr.EmitEvent(extproto.EventFromHost{Event: extproto.EventRunEnd})
	case core.EvCompactStart:
		// Compaction is about to squash the transcript. Unlike the post
		// event, this fires BEFORE detail is summarized away, so a memory
		// extension can harvest salient facts from the about-to-be-dropped
		// messages (read_session) while compaction's own LLM summarization
		// runs. Reason is short human-readable prose.
		mgr.EmitEvent(extproto.EventFromHost{Event: extproto.EventCompactStart, Text: e.Reason})
	case core.EvCompactEnd:
		// Tell subscribed extensions the transcript was compacted so they
		// can re-snapshot refreshable context (e.g. a memory extension
		// re-injecting its notes, which the frozen session-start snapshot
		// no longer reflects). Fire only on success — a failed compaction
		// left the transcript unchanged. The event is enqueued before the
		// post-compaction turn's tool calls (the FIFO outbox), the same
		// ordering session_start relies on. Additive + subscription-gated:
		// an extension that didn't subscribe never sees it.
		if e.Err == "" {
			mgr.EmitEvent(extproto.EventFromHost{Event: extproto.EventTranscriptCompacted})
		}
	}
}

// openWorkGateMessage is the at-close re-prompt injected once when the
// model tries to finish while an extension still has a blocking context
// card (open work). A soft nudge — it grants one more turn, not a hard
// stop — and core caps it to once per prompt.
const openWorkGateMessage = "You indicated you're finishing, but tracked items are still open. Complete them, or confirm they're intentionally left incomplete."

// continueOnOpenWork builds the ContinueOnStop gate for an agent: when a
// turn ends naturally and the manager reports a blocking card, re-prompt
// once with openWorkGateMessage. Shared by every host (interactive, rpc,
// non-interactive).
func continueOnOpenWork(extMgr *extensions.Manager) func(provider.StopReason) (bool, string) {
	return func(stop provider.StopReason) (bool, string) {
		if stop != provider.StopEnd || extMgr == nil || !extMgr.HasBlockingContext() {
			return false, ""
		}
		return true, openWorkGateMessage
	}
}

// emitSessionStart fires the session_start lifecycle event carrying the
// active session's identity, so session-aware extensions (protocol 2+)
// can key per-session state. It is the single point that announces the
// active session: once after the session opens and again on every
// switch (/sessions resume, fork, /new, /cd). sess may be nil — under
// --no-session or just after a close — in which case the id/path/title
// are empty, which the SDK surfaces as "no active session". The host
// emits this before the turn that can invoke extension tools, so a
// subscriber always sees session_start before that session's first
// tool_call (the ordered-delivery guarantee).
func emitSessionStart(mgr *extensions.Manager, sess *core.Session) {
	if mgr == nil {
		return
	}
	ev := extproto.EventFromHost{Event: extproto.EventSessionStart}
	if sess != nil {
		ev.SessionID = sess.ID
		ev.SessionPath = sess.Path
		ev.SessionTitle = sess.Meta.Title
		// cwd + its stable project key ride session_start so an extension
		// follows the working directory across a /cd (which re-fires this
		// event with the new cwd) instead of staying on the launch cwd.
		ev.CWD = sess.Meta.CWD
		if sess.Meta.CWD != "" {
			ev.ProjectID = core.ProjectKey(sess.Meta.CWD)
		}
	}
	// Bookend a switch: if a DIFFERENT session was last announced, tell
	// subscribers it ended before the new one starts (FIFO: end old, then
	// start new). A /cd re-announces the same id, so it ends nothing.
	// Closing to no-session (empty id) still ends the outgoing session.
	if prev, changed := mgr.SwapAnnouncedSession(extensions.SessionIdentity{
		ID: ev.SessionID, Path: ev.SessionPath, Title: ev.SessionTitle,
		CWD: ev.CWD, ProjectID: ev.ProjectID,
	}); changed {
		mgr.EmitSessionEnd(prev)
	}
	mgr.EmitEvent(ev)
}

// emitWorkspaceChanged fires the per-turn workspace diff to subscribed
// extensions as a workspace_changed event. A no-op when nothing changed,
// so a read-only turn stays silent. Paths are workspace-relative; the
// change kind is added/modified/deleted.
func emitWorkspaceChanged(mgr *extensions.Manager, changes []tools.FileChange) {
	if mgr == nil || len(changes) == 0 {
		return
	}
	files := make([]extproto.FileChange, len(changes))
	for i, c := range changes {
		files[i] = extproto.FileChange{Path: c.Path, Change: c.Kind}
	}
	mgr.EmitEvent(extproto.EventFromHost{Event: extproto.EventWorkspaceChanged, Files: files})
}

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

// workspaceChangeObserver returns an OnEvent observer that snapshots the
// workspace at the start of each run (EvTurnStart step 1, before any tool
// touches the tree) and emits the net diff at the end (EvDone) as a
// workspace_changed event. The armed flag suppresses a diff for a blocked
// prompt (EvDone with no turn) so it can't report a stale baseline.
// Compose it into a host's OnEvent. Touched only on the serial event
// goroutine, so the closure bool needs no lock. A nil differ disables it.
func workspaceChangeObserver(differ *tools.WorkspaceDiffer, extMgr *extensions.Manager) func(core.AgentEvent) {
	if differ == nil {
		return func(core.AgentEvent) {}
	}
	armed := false
	return func(ev core.AgentEvent) {
		switch e := ev.(type) {
		case core.EvTurnStart:
			if e.Step == 1 {
				differ.Rebase()
				armed = true
			}
		case core.EvDone:
			if armed {
				armed = false
				emitWorkspaceChanged(extMgr, differ.Diff())
			}
		}
	}
}

// toolResultText flattens a tool result's content blocks to their
// concatenated text, for the tool_result fanout event. Non-text
// blocks (images) are skipped — subscribers get the textual outcome.
func toolResultText(r core.ToolResult) string {
	var sb strings.Builder
	for _, c := range r.Content {
		if tb, ok := c.(provider.TextBlock); ok {
			if sb.Len() > 0 {
				sb.WriteString("\n")
			}
			sb.WriteString(tb.Text)
		}
	}
	return sb.String()
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

	// Register user-defined OpenAI-compatible endpoints (config.json
	// "endpoints") as providers before any Resolve / discovery sees them.
	RegisterEndpointsFromConfig()

	// Open the tool-call audit log for this process (lazily backed, so a
	// no-tool run never touches disk). Every mode's BeforeToolExecute records
	// through this shared sink — see buildBeforeToolExecute.
	auditSink = newAuditLog(TervaHome())

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
	if handled, err := runTrustCommand(rawArgs); handled {
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
	case ModeACP:
		return runACPMode(ctx, args, version)
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
func (nonInteractiveExtHooks) RefreshStatus()                                       {}
func (nonInteractiveExtHooks) RefreshContext()                                      {}

// setupNonInteractiveExtensions loads --ext paths and (unless
// --no-ext) runs discovery. Returns the manager so the caller can
// wire tools into the resolved registry, and a cleanup closure to
// defer. Mirrors the interactive-mode setup minus the TUI hooks.
func setupNonInteractiveExtensions(ctx context.Context, args Args, r *Resolved, version string) (*extensions.Manager, func()) {
	extMgr := extensions.New(TervaHome(), r.CWD, version, r.Provider, r.Model, nonInteractiveExtHooks{})
	extMgr.SetContextDisabled(r.DisableContextExtensions)
	extMgr.SetDisabledExtensions(r.DisableExtensions) // before Discover/LoadExplicit
	extMgr.SetProjectTrusted(r.Trusted)               // gate project ext dirs on Workspace Trust
	extMgr.SetConfigResolver(resolveExtensionConfig)  // hello_ack config delivery
	wireSessionReader(extMgr, TervaHome(), r.CWD)
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
	// MCP servers ride the same seam, after extensions so an
	// extension tool name wins a collision (first registration).
	_, stopMCP := setupMCP(ctx, args, r)
	// NOTE: session_start is emitted by the caller via emitSessionStart
	// AFTER it opens the session, so a session-keyed extension learns the
	// real session id (print / json / swarm-agent all persist a session).
	// Emitting a bare event here would (and did) leave those modes
	// reporting "no active session" even though one exists.
	return extMgr, func() {
		extMgr.Stop(2 * time.Second)
		stopMCP()
	}
}

// headlessConfirmGate returns the confirmation gate for a headless
// mode (print / json / rpc / swarm-agent), or nil when the policy is
// pure yolo (no rules, no mode override) — the historical no-gate fast
// path. There is no interactive prompt in these modes, so the gate is
// constructed with a nil inner Confirmer: a call the policy says to
// *ask* about is refused with a model-readable reason (see
// core.ConfirmGate.Check) instead of running unconfirmed — the
// refuse-by-default posture. Policy allow/deny rules and the mode's
// auto-allows still apply, so headless automation can run a curated
// tool set (e.g. plan mode permits the read-only tools). A one-line
// stderr note tells the human what stance is active; the actual gating
// happens in the BeforeToolExecute closure that calls gate.Check first.
// The second return is the policy's read-only registry, to hand to
// Resolved.AdoptReadOnlySet so read_only-annotated extension/MCP
// tools join the classification. Nil alongside a nil gate.
func headlessConfirmGate(args Args, mode string) (*core.ConfirmGate, *core.ReadOnlySet) {
	pol, warns := buildPermissionPolicy(args)
	for _, w := range warns {
		fmt.Fprintf(os.Stderr, "note: %s\n", w)
	}
	if pol == nil {
		return nil, nil
	}
	switch pol.Mode {
	case core.ApprovalPlan:
		fmt.Fprintf(os.Stderr, "note: approval mode 'plan' in %s mode: read-only tools run, everything else is refused\n", mode)
	case core.ApprovalYolo:
		// Reachable only because rules exist; allow/deny apply
		// silently. ask rules degrade to allow in yolo (yolo never
		// prompts), so only a deny rule refuses here.
	default:
		fmt.Fprintf(os.Stderr, "note: approval mode %q in %s mode refuses tool calls that would need confirmation (no interactive prompt available)\n", pol.Mode, mode)
	}
	return core.NewPolicyGate(pol, nil), pol.ReadOnly
}

// wireNonInteractiveAgentExtHooks installs the same BeforeToolExecute
// / BeforeTurn / BeforeAssistantMessage / OnEvent hooks the
// interactive path wires up, so extensions get their normal
// event-intercept surface in print / json / rpc flows too. When gate
// is non-nil (--no-yolo) it runs FIRST in BeforeToolExecute, mirroring
// interactive mode, so a refusal short-circuits before the extension
// intercept sees the call.
// buildHookEngine loads the user config's hooks into an engine, plus a
// TRUSTED project's hooks (appended — both fire, user first), or nil when
// none are configured. Hook misbehavior (timeouts, bad JSON) logs to
// $TERVA_HOME/logs/hooks.log — stderr would corrupt the TUI and a broken
// hook must never break a turn.
//
// trusted is the Workspace Trust verdict for this launch (r.Trusted). When
// false the project's hooks are NEVER loaded — only the user's — so a cloned
// repo cannot run code on tool calls until the user trusts it (Phase 6). Every
// call site must thread the real verdict; defaulting to true would re-open the
// RCE-on-clone gap this gate closes.
func buildHookEngine(args Args, trusted bool) *hooks.Engine {
	user, err := LoadConfig()
	if err != nil {
		return nil
	}
	cfg := mergeHookConfigs(user.Hooks, trustedProjectHooks(args.CWD, trusted))
	if cfg == nil {
		return nil
	}
	logf := func(string, ...any) {}
	var logCloser io.Closer
	if err := os.MkdirAll(LogsPath(), 0o755); err == nil {
		if f, ferr := os.OpenFile(filepath.Join(LogsPath(), "hooks.log"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644); ferr == nil {
			logCloser = f
			logf = func(format string, a ...any) {
				fmt.Fprintf(f, time.Now().Format(time.RFC3339)+" "+format+"\n", a...)
			}
		}
	}
	eng := hooks.NewEngine(*cfg, args.CWD, logf)
	eng.SetCloser(logCloser)
	return eng
}

// buildBeforeToolExecute composes the tool-call ladder in its
// canonical order — pre-hooks (may rewrite args; allow/deny are
// final), the confirm gate (sees post-rewrite args), then the
// extension intercept. One implementation shared by every mode so
// the ladders cannot drift apart. hookEng, gate, and extMgr may each
// be nil.
func buildBeforeToolExecute(ctx context.Context, hookEng *hooks.Engine, gate *core.ConfirmGate, extMgr *extensions.Manager) func(provider.ToolCallBlock) (bool, string, json.RawMessage) {
	return func(call provider.ToolCallBlock) (allowed bool, reason string, modArgs json.RawMessage) {
		args := call.Arguments
		// Audit every call with the gate's decision and the mode in force, so
		// even a yolo session leaves a durable record of what ran and why it
		// was permitted. Recorded once on the way out: args tracks hook/ext
		// rewrites, and the named returns carry the final verdict. auditSink is
		// nil (no-op) outside a real run, e.g. in tests.
		defer func() {
			mode := ""
			if gate != nil {
				mode = string(gate.Mode())
			}
			auditSink.Record(time.Now(), call.Name, args, mode, allowed, reason)
		}()

		hookModified := false
		skipGate := false
		if hookEng != nil {
			hr := hookEng.RunPre(ctx, call.Name, args)
			if hr.UpdatedArgs != nil {
				args = hr.UpdatedArgs
				hookModified = true
			}
			switch hr.Decision {
			case hooks.DecisionDeny:
				return false, hr.Reason, nil
			case hooks.DecisionAllow:
				skipGate = true
			}
			// DecisionAsk falls through to the gate; in a gateless
			// (pure yolo) session that is a no-op by design — deny /
			// exit 2 is the enforcement spelling.
		}
		if !skipGate && gate != nil {
			ok, denyReason, _ := gate.Check(call.Name, args, core.BuildPreview(args, 120))
			if !ok {
				return false, denyReason, nil
			}
		}
		if extMgr != nil {
			res := extMgr.InterceptToolCall(ctx, call.ID, call.Name, args)
			if res.Block {
				return false, res.Reason, nil
			}
			if res.ModifiedArgs != nil {
				args = res.ModifiedArgs // reflect the rewrite in the audit line
				return true, "", args
			}
		}
		if hookModified {
			return true, "", args
		}
		return true, "", nil
	}
}

// observeAgentEventForHooks feeds the two tool events into the hook
// engine's post-tool-use correlator. Nil-safe.
func observeAgentEventForHooks(eng *hooks.Engine, ev core.AgentEvent) {
	if eng == nil {
		return
	}
	switch e := ev.(type) {
	case core.EvToolCall:
		eng.Observe("tool_call", e.ID, e.Name, e.Args, false)
	case core.EvToolResult:
		eng.Observe("tool_result", e.ID, "", nil, e.Result.IsError)
	}
}

func wireNonInteractiveAgentExtHooks(ctx context.Context, ag *core.Agent, extMgr *extensions.Manager, gate *core.ConfirmGate, hookEng *hooks.Engine, differ *tools.WorkspaceDiffer) {
	if ag == nil || extMgr == nil {
		return
	}
	wsObserve := workspaceChangeObserver(differ, extMgr)
	ag.BeforeToolExecute = buildBeforeToolExecute(ctx, hookEng, gate, extMgr)
	wireHostToolDispatcher(ag, extMgr, gate)
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
	ag.OnEvent = func(ev core.AgentEvent) {
		wsObserve(ev)
		fanoutAgentEvent(extMgr, ev)
		observeAgentEventForHooks(hookEng, ev)
	}
	// Inject extensions' live context cards into the model each turn.
	ag.ContextProvider = extMgr.EphemeralContext
	// Re-prompt once at close if an extension flags open work.
	ag.ContinueOnStop = continueOnOpenWork(extMgr)
}

func runPrintMode(ctx context.Context, args Args, version string) error {
	confirmGate, roSet := headlessConfirmGate(args, "print")
	r, err := Resolve(args, true)
	if err != nil {
		return err
	}
	warnRestrictedWorkspace(args, r.Trusted)
	r.AdoptReadOnlySet(roSet)
	extMgr, stopExt := setupNonInteractiveExtensions(ctx, args, &r, version)
	defer stopExt()

	ag := r.NewAgent()
	wireNonInteractiveAgentExtHooks(ctx, ag, extMgr, confirmGate, buildHookEngine(args, r.Trusted), tools.NewWorkspaceDiffer(workspaceRootFn(r.Sandbox, r.CWD)))
	sess, _ := openOrCreateSession(args, r, ag, version)
	defer sess.Close()
	// Tell session-keyed extensions the real session id before any turn
	// runs, so per-session state persists in headless modes too.
	emitSessionStart(extMgr, sess)

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
	confirmGate, roSet := headlessConfirmGate(args, "json")
	r, err := Resolve(args, true)
	if err != nil {
		return err
	}
	warnRestrictedWorkspace(args, r.Trusted)
	r.AdoptReadOnlySet(roSet)
	extMgr, stopExt := setupNonInteractiveExtensions(ctx, args, &r, version)
	defer stopExt()

	ag := r.NewAgent()
	wireNonInteractiveAgentExtHooks(ctx, ag, extMgr, confirmGate, buildHookEngine(args, r.Trusted), tools.NewWorkspaceDiffer(workspaceRootFn(r.Sandbox, r.CWD)))
	sess, _ := openOrCreateSession(args, r, ag, version)
	defer sess.Close()
	// Tell session-keyed extensions the real session id before any turn
	// runs, so per-session state persists in headless modes too.
	emitSessionStart(extMgr, sess)

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

// resolveSwarmWorktrees decides whether per-agent swarm worktree
// isolation is on. The --swarm-worktrees flag (flagOverride, non-nil
// when the flag was given) wins over the user config's swarm_worktrees
// (cfg). nil/absent in both means off — today's behavior. Mirrors the
// bool-pointer precedence used for the other swarm/picker settings.
func resolveSwarmWorktrees(flagOverride, cfg *bool) bool {
	if flagOverride != nil {
		return *flagOverride
	}
	return cfg != nil && *cfg
}

// swarmTierMap converts the user config's per-provider tier pins into the
// tools-layer override map (provider -> {tier -> model id}). Empty tier fields
// are dropped so they fall back to the built-in family guess; an all-empty
// result is nil so resolution is a clean no-op.
func swarmTierMap(tiers map[string]TierConfig) tools.SwarmTierMap {
	if len(tiers) == 0 {
		return nil
	}
	out := make(tools.SwarmTierMap, len(tiers))
	for prov, tc := range tiers {
		m := map[string]string{}
		for tier, id := range map[string]string{"weak": tc.Weak, "medium": tc.Medium, "strong": tc.Strong} {
			if strings.TrimSpace(id) != "" {
				m[tier] = strings.TrimSpace(id)
			}
		}
		if len(m) > 0 {
			out[prov] = m
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// swarmWorktreeAcquirer builds the swarm.Config.AcquireWorktree hook
// that leases a dedicated git worktree per sub-agent from the
// terva-git-worktree extension. It is wired ONLY when the user opted in
// (see resolveSwarmWorktrees) and the worktree_create tool is
// registered, so any failure here is a real misconfiguration, not the
// absence of the feature: the closure returns a non-nil error and the
// spawn fails loudly rather than silently dropping back to the shared
// tree. On the terminal release it calls worktree_release (NOT
// worktree_remove) so the worktree + branch survive for review/merge
// via the extension's `/worktree collect`.
func swarmWorktreeAcquirer(extMgr *extensions.Manager) func(context.Context, swarm.WorktreeReq) (swarm.WorktreeLease, error) {
	return func(ctx context.Context, req swarm.WorktreeReq) (swarm.WorktreeLease, error) {
		name := slugAgent(req.AgentID, req.Task)
		args, err := json.Marshal(map[string]any{"name": name}) // base defaults to HEAD
		if err != nil {
			return swarm.WorktreeLease{}, fmt.Errorf("marshal worktree_create args: %w", err)
		}
		res, err := extMgr.InvokeTool(ctx, "worktree_create", args, 30*time.Second)
		if err != nil {
			// Extension unregistered (`no extension registered for tool`)
			// or a transport error. Worktree isolation was explicitly
			// requested, so surface it instead of silently sharing the
			// host tree.
			return swarm.WorktreeLease{}, fmt.Errorf("swarm worktree isolation: worktree_create via terva-git-worktree failed (install the extension or drop --swarm-worktrees): %w", err)
		}
		if res.IsError {
			return swarm.WorktreeLease{}, fmt.Errorf("swarm worktree isolation: worktree_create returned an error: %s", firstText(res))
		}
		var cr struct {
			Path string `json:"path"`
		}
		_ = json.Unmarshal([]byte(firstText(res)), &cr)
		if cr.Path == "" {
			return swarm.WorktreeLease{}, fmt.Errorf("swarm worktree isolation: worktree_create returned no path (result: %s)", firstText(res))
		}
		return swarm.WorktreeLease{
			Dir: cr.Path,
			Release: func() {
				// Release, never remove: keep the worktree + branch for
				// review/merge via `/worktree collect`. Best-effort and
				// detached from ctx — the agent is already terminal, so a
				// cancelled ctx must not block freeing the lease.
				rel, _ := json.Marshal(map[string]any{"name": name})
				_, _ = extMgr.InvokeTool(context.Background(), "worktree_release", rel, 10*time.Second)
			},
		}, nil
	}
}

// firstText returns the first text content block of an extension tool
// result, or "" when there is none. Used to parse worktree_create's
// JSON payload (the extension returns it as a single text block).
func firstText(res extproto.ToolResultFromExt) string {
	for _, b := range res.Content {
		if b.Type == "text" {
			return b.Text
		}
	}
	return ""
}

// slugAgent derives a stable, filesystem/branch-safe worktree name for
// an agent. The agent id is already unique (slug-of-task + nanoseconds),
// so we lead with it for collision-resistance and append a short,
// readable task slug. The terva-git-worktree extension re-slugs the
// name itself, but we hand it something already safe.
func slugAgent(agentID, task string) string {
	safe := func(s string, max int) string {
		var b strings.Builder
		dash := false
		for _, r := range strings.ToLower(s) {
			switch {
			case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
				b.WriteRune(r)
				dash = false
			case r == '-' || r == '_':
				b.WriteRune(r)
				dash = false
			default:
				if !dash && b.Len() > 0 {
					b.WriteByte('-')
					dash = true
				}
			}
			if b.Len() >= max {
				break
			}
		}
		return strings.Trim(b.String(), "-_")
	}
	id := safe(agentID, 48)
	if id == "" {
		id = "agent"
	}
	t := safe(task, 24)
	if t == "" {
		return id
	}
	return id + "-" + t
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
	extMgr.SetContextDisabled(r.DisableContextExtensions)
	extMgr.SetDisabledExtensions(r.DisableExtensions) // before Discover/LoadExplicit
	extMgr.SetProjectTrusted(r.Trusted)               // gate project ext dirs on Workspace Trust
	extMgr.SetConfigResolver(resolveExtensionConfig)  // hello_ack config delivery
	wireSessionReader(extMgr, TervaHome(), r.CWD)
	// --ext paths first so they win against installed extensions of
	// the same name (loadOne's first-write-wins semantics).
	for _, e := range extMgr.LoadExplicit(ctx, args.Exts) {
		fmt.Fprintln(os.Stderr, "extension load:", e)
	}
	// First-run offer: when nothing is installed yet, offer the built-in
	// core pack (once, interactive TTY only) BEFORE discovery, so an
	// accepted install is picked up by the scan below in this same session.
	maybeOfferCorePack(args)
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

	// Confirmation gate: built from the permission policy (approval
	// mode + rules — see docs/permissions.md) BEFORE any tool merge,
	// so read_only-annotated extension/MCP tools land in the policy's
	// classification via AdoptReadOnlySet. The TUI is bound as the
	// Confirmer below; "always, save to config" answers persist
	// through AppendUserPermissionRule.
	//
	// Unlike the headless modes, interactive ALWAYS builds a gate —
	// even in pure yolo (where buildPermissionPolicy returns nil) we
	// synthesize a yolo policy — so the approval mode can be switched
	// live from /settings (the gate is the thing SetMode mutates).
	pol, polWarns := buildPermissionPolicy(args)
	for _, w := range polWarns {
		fmt.Fprintf(os.Stderr, "note: %s\n", w)
	}
	if pol == nil {
		pol = &core.PermissionPolicy{Mode: core.ApprovalYolo, ReadOnly: builtinReadOnlySet(), EditTools: editTools}
	}
	confirmGate := core.NewPolicyGate(pol, nil) // Confirmer set below
	confirmGate.SetPersist(func(tool string) {
		if err := AppendUserPermissionRule(tool); err != nil {
			fmt.Fprintf(os.Stderr, "warning: could not persist allow rule for %s: %v\n", tool, err)
		}
	})
	r.AdoptReadOnlySet(pol.ReadOnly)

	extToolAdapter := &extToolAdapter{mgr: extMgr}
	r.MergeExtensionTools(extToolAdapter)
	mcpAdapter, stopMCP := setupMCP(ctx, args, &r)
	defer stopMCP()

	// Build the swarm supervisor BEFORE the agent so the auto-swarm
	// tool can reference it during tool-registry construction. State
	// lives under TervaHome/swarm so per-agent meta/events survive
	// restarts; the user can hunt orphaned agents down with
	// `git worktree list` if anything misbehaves.
	//
	// swarmMgr is also captured by loadSession / changeCWD closures
	// further down the function, which is why we keep the variable
	// in this outer scope rather than scoping it tighter.
	//
	// Per-agent worktree isolation is opt-in: --swarm-worktrees (the
	// flag) overrides the user config's swarm_worktrees. When on, each
	// spawned agent leases its own git worktree from the
	// terva-git-worktree extension instead of sharing r.CWD. Because
	// this is an explicit opt-in, a missing extension is a
	// misconfiguration — fail fast here rather than silently falling
	// back to the shared tree (which would defeat the point) or erroring
	// only on the first spawn.
	swarmCfg := swarm.Config{
		Root:     filepath.Join(TervaHome(), "swarm"),
		RepoRoot: r.CWD,
	}
	swarmWTCfg, _ := LoadConfig()
	if resolveSwarmWorktrees(args.SwarmWorktrees, swarmWTCfg.SwarmWorktrees) {
		if !extMgr.HasTool("worktree_create") {
			return fmt.Errorf("swarm worktree isolation requires the terva-git-worktree extension; install it or drop --swarm-worktrees")
		}
		swarmCfg.AcquireWorktree = swarmWorktreeAcquirer(extMgr)
	}
	var swarmMgr *swarm.Swarm
	swarmMgr = swarm.New(swarmCfg)
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
	// Per-provider tier→model overrides for swarm_spawn's `tier` param, from
	// the user config (never project — sub-agent model selection must not be
	// redirectable by a cloned repo). Captured once so every registry-rebuild
	// path that re-injects swarm_spawn carries the same overrides.
	hostTiers := swarmTierMap(swarmWTCfg.SwarmTiers)
	// swarm_spawn's host provider/model must track the agent's CURRENT
	// resolved route so an auto-spawned sub-agent follows the same auth
	// route the user is on — including after a /model swap. The build
	// closures below refresh these before re-injecting the tool; the launch
	// values seed them. (All writes/reads happen on the TUI goroutine.)
	swarmHostProvider, swarmHostModel := r.Provider, r.Model
	injectSwarmSpawn := func(reg core.Registry) core.Registry {
		if reg == nil {
			return reg
		}
		if !AutoSwarmEnabled() {
			return reg
		}
		reg["swarm_spawn"] = &tools.SwarmSpawnTool{
			Swarm:        swarmMgr,
			Enabled:      AutoSwarmEnabled,
			OnSpawned:    onSpawnedSwarm,
			HostProvider: swarmHostProvider,
			HostModel:    swarmHostModel,
			Tiers:        hostTiers,
		}
		return reg
	}
	injectSwarmSpawn(r.ToolRegistry)

	// uiAsker holds the interactive question channel (the TUI, set once
	// it exists below). Captured by setApprovalMode so the registry it
	// rebuilds for a new approval mode keeps the ask_user_question
	// channel instead of reverting to the headless "no channel" result.
	var uiAsker core.Asker

	// setApprovalMode switches the approval mode live (from /settings):
	// it swaps enforcement on the gate and rebuilds the tool registry
	// for the new mode (plan hides mutating + non-read-only extension
	// tools; other modes restore them), returning the fresh registry
	// for the TUI to install on the running agent. The registry is
	// rebuilt from scratch and re-merged through the same
	// MergeToolsForMode path the startup assembly uses, so the live and
	// initial views cannot drift.
	setApprovalMode := func(mode core.ApprovalMode) core.Registry {
		confirmGate.SetMode(mode)
		reg := buildToolRegistry(args, mode, r.CWD, sharedSandbox, r.Provider, r.AuthMethod, r.VisionCapable)
		MergeToolsForMode(reg, mode, pol.ReadOnly, extToolAdapter)
		if mcpAdapter != nil {
			MergeToolsForMode(reg, mode, pol.ReadOnly, mcpAdapter)
		}
		injectSwarmSpawn(reg)
		bindAsker(reg, uiAsker)
		return reg
	}

	hookEng := buildHookEngine(args, r.Trusted)

	// Per-turn workspace change reporting: snapshot the workspace at the
	// start of each run and diff at the end, emitting a workspace_changed
	// event so an index/memory extension learns what files moved. The root
	// is read live (sandbox root, which a /cd updates; else the cwd) so the
	// differ follows directory changes. Survives a login re-resolve (the
	// agent is rebuilt, this isn't). See packages/agent/tools/workspace_diff.go.
	workspaceDiffer := tools.NewWorkspaceDiffer(workspaceRootFn(sharedSandbox, r.CWD))

	// Capture current args in a closure so BuildAgent can re-resolve
	// after a successful login (picks up the newly stored credential).
	wireAgentExt := func(a *core.Agent) *core.Agent {
		if a == nil {
			return a
		}
		// Canonical tool-call ladder: pre-hooks, confirm gate,
		// extension intercept — shared with the headless modes via
		// buildBeforeToolExecute so the orders cannot drift.
		a.BeforeToolExecute = buildBeforeToolExecute(ctx, hookEng, confirmGate, extMgr)
		wireHostToolDispatcher(a, extMgr, confirmGate)
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
		a.BeforeUserMessage = func(text string) (bool, string, string) {
			r := extMgr.InterceptUserMessage(ctx, text)
			if r.Block {
				return false, r.Reason, ""
			}
			return true, "", r.ReplaceText
		}
		wsObserve := workspaceChangeObserver(workspaceDiffer, extMgr)
		a.OnEvent = func(ev core.AgentEvent) {
			wsObserve(ev)
			fanoutAgentEvent(extMgr, ev)
			observeAgentEventForHooks(hookEng, ev)
		}
		// Inject extensions' live context cards into the model each turn.
		a.ContextProvider = extMgr.EphemeralContext
		// Re-prompt once at close if an extension flags open work.
		a.ContinueOnStop = continueOnOpenWork(extMgr)
		return a
	}

	buildAgent := func() (*core.Agent, string, string, error) {
		resolved, err := Resolve(args, true)
		if err != nil {
			return nil, "", "", err
		}
		resolved.UseSandbox(sharedSandbox)
		if pol != nil {
			resolved.AdoptReadOnlySet(pol.ReadOnly)
		}
		resolved.MergeExtensionTools(extToolAdapter)
		if mcpAdapter != nil {
			resolved.MergeExtensionTools(mcpAdapter)
		}
		swarmHostProvider, swarmHostModel = resolved.Provider, resolved.Model
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
		if pol != nil {
			resolved.AdoptReadOnlySet(pol.ReadOnly)
		}
		resolved.MergeExtensionTools(extToolAdapter)
		if mcpAdapter != nil {
			resolved.MergeExtensionTools(mcpAdapter)
		}
		swarmHostProvider, swarmHostModel = resolved.Provider, resolved.Model
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
		if pol != nil {
			resolved.AdoptReadOnlySet(pol.ReadOnly)
		}
		resolved.MergeExtensionTools(extToolAdapter)
		if mcpAdapter != nil {
			resolved.MergeExtensionTools(mcpAdapter)
		}
		swarmHostProvider, swarmHostModel = resolved.Provider, resolved.Model
		injectSwarmSpawn(resolved.ToolRegistry)
		return wireAgentExt(resolved.NewAgent()), resolved.Provider, resolved.Model, nil
	}

	var ag *core.Agent
	if r.HasCredential() {
		ag = wireAgentExt(r.NewAgent())
	}

	// liveAgent returns the agent the TUI is actually running. A
	// cross-provider /model swap or a login rebuilds the agent and installs
	// it via i.turns.SetAgent without updating this function's `ag` variable,
	// so `ag` goes stale; iv.Agent() is the authoritative handle (the same one
	// the exit-time flush uses). Every callback that mutates "the current
	// agent" (extension reload, /cd, /new, /sessions) must resolve through
	// here, or it silently operates on an orphaned pre-swap agent — which is
	// how /new left the live transcript intact and seeded the new session with
	// the old conversation. Falls back to `ag` before the TUI exists.
	liveAgent := func() *core.Agent {
		if iv != nil {
			if a := iv.Agent(); a != nil {
				return a
			}
		}
		return ag
	}

	// triggerReload re-resolves the tool registry (built-ins + freshly-
	// registered extension tools + MCP tools) and swaps it onto the current
	// agent in-place. Used by BOTH the extension reload (/reload-ext, a
	// single /extensions toggle) and a live /mcp toggle — because the
	// registry is rebuilt from scratch each time, a server or extension
	// whose tools disappeared is genuinely removed. The current agent may
	// have been replaced by a /model swap since spawn, so re-read the live
	// agent each invocation.
	triggerReload := func() {
		current := liveAgent()
		if current == nil {
			return
		}
		resolved, err := Resolve(args, true)
		if err != nil {
			return
		}
		resolved.UseSandbox(sharedSandbox)
		if pol != nil {
			resolved.AdoptReadOnlySet(pol.ReadOnly)
		}
		resolved.MergeExtensionTools(extToolAdapter)
		if mcpAdapter != nil {
			resolved.MergeExtensionTools(mcpAdapter)
		}
		injectSwarmSpawn(resolved.ToolRegistry)
		current.SetTools(resolved.ToolRegistry)
	}
	extMgr.SetOnReload(triggerReload)

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
	// Announce the active session now that it's open (or nil under
	// --no-session). Moved here from before openOrCreateSession so the
	// event carries the real session identity, and so it fires after the
	// session exists but before the first turn — the ordering a
	// session-aware extension relies on.
	emitSessionStart(extMgr, sess)
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
	persistImageExclusion := func(sha string) {
		persistMu.Lock()
		defer persistMu.Unlock()
		if sess == nil {
			return
		}
		// Append-only directive: a resume re-applies it instead of re-sending
		// the image the provider rejected, so recovery is paid once.
		_ = sess.AppendImageExclusion(sha, "provider rejected the image as invalid/unreadable")
	}
	wireAgentPersist := func(a *core.Agent) *core.Agent {
		if a == nil {
			return a
		}
		a.OnMessageAppended = persistMessage
		a.OnUsage = persistUsage
		a.OnTranscriptCompacted = persistCompaction
		a.OnImageExcluded = persistImageExclusion
		return a
	}
	wireAgentPersist(ag)

	// Re-wrap the build closures so any agent constructed by the TUI
	// (login, /model swap to a different provider) also gets the
	// persistence hooks. Without this, switching provider would
	// silently revert to the old in-memory-only behaviour.
	// withAsker re-binds the interactive question channel onto a freshly
	// built agent's registry. Every rebuild path (login, /model swap)
	// constructs a new registry with a fresh ask_user_question tool whose
	// Asker is nil; without this it would silently fall back to the
	// headless "no channel" result. uiAsker is set once the TUI exists,
	// before any of these closures can run.
	withAsker := func(a *core.Agent) *core.Agent {
		if a != nil {
			bindAsker(a.Tools, uiAsker)
		}
		return a
	}
	baseBuildAgent := buildAgent
	buildAgent = func() (*core.Agent, string, string, error) {
		a, p, m, err := baseBuildAgent()
		return withAsker(wireAgentPersist(a)), p, m, err
	}
	baseBuildAgentFor := buildAgentFor
	buildAgentFor = func(providerOverride, modelOverride string) (*core.Agent, string, string, error) {
		a, p, m, err := baseBuildAgentFor(providerOverride, modelOverride)
		return withAsker(wireAgentPersist(a)), p, m, err
	}
	baseBuildAgentForRescue := buildAgentForRescue
	buildAgentForRescue = func(providerOverride, modelOverride string) (*core.Agent, string, string, error) {
		a, p, m, err := baseBuildAgentForRescue(providerOverride, modelOverride)
		return withAsker(wireAgentPersist(a)), p, m, err
	}

	// loadSession replaces the current session with the one at path and
	// hands its messages to the agent. Used by the /sessions picker.
	loadSession := func(path string) error {
		currentAg := liveAgent() // the agent the TUI runs, not a stale capture
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
		// Re-announce the active session to extensions: /sessions resume
		// and fork both route through here, so this one emit covers both.
		emitSessionStart(extMgr, newSess)
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

		currentAg := liveAgent()
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
		// Announce the freshly-opened session (or nil under --no-session).
		emitSessionStart(extMgr, sess)
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
		currentAg := liveAgent() // reset the agent the TUI runs (see liveAgent)
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
		// Announce the new session (or nil under --no-session, where the
		// transcript reset is the whole effect).
		emitSessionStart(extMgr, sess)
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
		Terminal:                term,
		Theme:                   theme,
		InlineImagesEnabled:     initialCfg.InlineImagesEnabled,
		AutoSwarmEnabled:        initialCfg.AutoSwarmEnabled,
		RecursiveFileSuggest:    initialCfg.RecursiveFileSuggest,
		RespectGitignore:        initialCfg.RespectGitignore,
		ThemeName:               initialCfg.Theme,
		PersonaName:             PersonaName(),
		PersonaPhonetic:         personaPhonetic(),
		ExtensionThemes:         func() []tui.ThemeOption { return extensionThemeOptions(extMgr) },
		AutoSwarmSystemAddendum: AutoSwarmSystemAddendum,
		SwarmTiers:              hostTiers,
		SettingsStore:           configSettingsStore{},
		RebuildExtensionContext: func() (string, bool) {
			if extMgr == nil {
				return r.SystemPrompt, false
			}
			return r.RefreshExtensionContext(extToolAdapter)
		},
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
		UserModelsPath:             UserModelsPath(),
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
			// User-defined endpoints are reachable once configured (base URL
			// set; key optional), so surface each like openai-compatible.
			if uc, err := LoadConfig(); err == nil {
				for id, ep := range uc.Endpoints {
					if ep.BaseURL != "" && !seen[id] {
						out = append(out, id)
						seen[id] = true
					}
				}
			}
			return out
		},
		LoadSession:         loadSession,
		NewSession:          newSession,
		ChangeCWD:           changeCWD,
		Trusted:             r.Trusted,
		GatedContentPresent: hasGatedProjectContent(r.CWD),
		TrustWorkspace: func(parent bool) error {
			// Persist trust for the live cwd (it may have moved via /cd).
			return TrustPath(args.CWD, parent)
		},
		UntrustWorkspace: func() error {
			return UntrustPath(args.CWD)
		},
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
		Extensions: extMgr,
		ListExtensions: func() []modes.ExtInfo {
			return listInstalledExtensions(r.CWD, r.Trusted, extMgr)
		},
		SetExtensionGlobalEnabled: func(name string, on bool) error {
			dir, err := findExtensionDirIn(r.CWD, name)
			if err != nil {
				return err
			}
			return setManifestEnabled(dir, on)
		},
		SetExtensionProjectEnabled: func(name string, on bool) error {
			// on=true → ensure NOT in the project's disable list.
			return setProjectExtensionDisabled(r.CWD, name, !on)
		},
		ApplyExtensionChange: func(name string) {
			applyExtensionChangeLive(extMgr, r.CWD, r.Trusted, name)
		},
		ExtensionConfigFields: func(name string) []modes.ConfigField {
			return extensionConfigFields(r.CWD, name)
		},
		SetExtensionConfig: func(name string, values map[string]string) error {
			return setExtensionConfigFromForm(r.CWD, name, values)
		},
		ApplyExtensionConfig: func(name string) {
			applyExtensionConfigLive(extMgr, r.CWD, name)
		},
		ListMCP: func() []modes.MCPInfo {
			var mgr *mcp.Manager
			if mcpAdapter != nil {
				mgr = mcpAdapter.mgr
			}
			return listMCPServers(r.CWD, r.Trusted, mgr)
		},
		SetMCPGlobalEnabled: func(name string, on bool) error {
			// on=true → ensure NOT in the user's disable_mcp list.
			return setUserMCPDisabled(name, !on)
		},
		SetMCPProjectEnabled: func(name string, on bool) error {
			// on=true → ensure NOT in the project's disable_mcp list.
			return setProjectMCPDisabled(r.CWD, name, !on)
		},
		ApplyMCPChange: func(name string) {
			applyMCPChangeLive(mcpAdapter, r.CWD, r.Trusted, name, triggerReload)
		},
		ReadLogTail:   readLogTail,
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
			// Honor the launch trust verdict: project skills stay hidden
			// from the picker while restricted (they aren't loaded for the
			// model either).
			list, _ := skills.Discover(TervaHome(), r.CWD, userHome, args.WithSkills, r.Trusted)
			return skills.VisibleSkills(list)
		},
		NoYolo:          args.NoYolo,
		ConfirmGate:     confirmGate,
		SetApprovalMode: setApprovalMode,
		SetSessionModel: func(providerName, model string) {
			// Session-only: resume picks up the model, but the global/project
			// default is untouched. Promoting to a default is explicit
			// (PromoteModelDefault / Ctrl+D in the picker).
			if sess != nil {
				_ = sess.UpdateModel(providerName, model)
			}
		},
		PromoteModelDefault: func(providerName, model, scope string) error {
			switch scope {
			case "project":
				if err := setProjectModel(r.CWD, providerName, model); err != nil {
					return err
				}
			case "global":
				cfg, _ := LoadConfig()
				cfg.Provider = providerName
				cfg.Model = model
				if err := SaveConfig(cfg); err != nil {
					return err
				}
			default:
				return fmt.Errorf("unknown model-default scope %q", scope)
			}
			// Either way the running session adopts it for resume.
			if sess != nil {
				_ = sess.UpdateModel(providerName, model)
			}
			return nil
		},
		FavoriteModels: func() []string {
			cfg, _ := LoadConfig()
			return cfg.FavoriteModels
		},
		SetFavoriteModel: func(key string, on bool) error {
			cfg, _ := LoadConfig()
			cfg.FavoriteModels = toggleStringMember(cfg.FavoriteModels, key, on)
			return SaveConfig(cfg)
		},
		RefreshCompatModels: RefreshCompatModelsAsync,
		RefreshModels:       RefreshModelsForceAsync,
	})

	// Bind the interactive TUI as the Confirmer. We deferred this
	// until now because the gate is constructed before the TUI
	// (the BeforeToolExecute closure captures it). SetConfirmer
	// is mutex-guarded on the gate so this is safe.
	if confirmGate != nil {
		confirmGate.SetConfirmer(iv)
	}
	// Bind the TUI as the ask_user_question channel, same deferred
	// construction-order unknot as SetConfirmer. uiAsker feeds future
	// approval-mode rebuilds (setApprovalMode); r.SetAsker covers the
	// agent already running on the initial registry.
	uiAsker = iv
	r.SetAsker(iv)

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
