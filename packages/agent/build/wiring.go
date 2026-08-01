package build

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"terva.sh/terva/packages/agent/config"
	"terva.sh/terva/packages/agent/extensions"
	"terva.sh/terva/packages/agent/extproto"
	"terva.sh/terva/packages/agent/exttool"
	"terva.sh/terva/packages/agent/hooks"
	"terva.sh/terva/packages/agent/mcp"
	"terva.sh/terva/packages/agent/swarm"
	"terva.sh/terva/packages/agent/tools"
	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/privfs"
	"terva.sh/terva/packages/provider"
)

// Agent wiring: the adapters, hook engine, MCP setup and event fan-out that
// turn a Resolved into a running core.Agent. These lived in cli.go only
// because, until Workspace arrived, cli.go was the one thing that built an
// agent. They are construction, not command-line handling.

// ExtToolAdapter bridges *extensions.Manager to the
// ExtensionToolSource interface declared in go (kept narrow to
// avoid a build->extensions import cycle). One adapter instance per
// run; used at every Resolve point so re-built agents pick up the
// same set of extension tools.
type ExtToolAdapter struct {
	Mgr *extensions.Manager
}

// StaticContext exposes the extensions' aggregated static context
// contribution to MergeExtensionTools (optional ExtensionToolSource
// extension), which folds it into the cached system-prompt addendum.
func (a *ExtToolAdapter) StaticContext() string {
	return a.Mgr.StaticContext()
}

// Identities exposes the loaded extensions' names and versions to
// MergeExtensionTools (a second optional ExtensionToolSource extension), which
// hands them to terva_status. MCP sources do not implement it — an MCP server
// declares no version terva can vouch for — so they are simply absent from the
// report rather than listed as unknown.
//
// An extension that failed to load is skipped: it contributes no tools, so
// naming it in "what am I running" would describe a surface the agent does not
// have. `terva ext doctor` is where a failure is meant to surface, with the
// reason attached.
func (a *ExtToolAdapter) Identities() []tools.ExtensionIdentity {
	diags := a.Mgr.Diagnostics()
	out := make([]tools.ExtensionIdentity, 0, len(diags))
	for _, d := range diags {
		if d.FailedReason != "" {
			continue
		}
		out = append(out, tools.ExtensionIdentity{Name: d.Name, Version: d.Version})
	}
	return out
}

func (a *ExtToolAdapter) Tools() []ExtensionToolInfo {
	infos := a.Mgr.Tools()
	out := make([]ExtensionToolInfo, 0, len(infos))
	for _, t := range infos {
		// This is the single model-facing feed into MergeToolsForMode, so
		// skipping a withdrawn tool here drops it from the registry AND the
		// system-prompt tool list (set_withdrawn_tools, protocol 4). The
		// /extensions dialog reads the driver's unfiltered Tools() directly,
		// so its registered count is unaffected.
		if t.Withdrawn {
			continue
		}
		out = append(out, ExtensionToolInfo{
			Extension:   t.Extension,
			Name:        t.Name,
			Description: t.Description,
			Schema:      t.Schema,
			ReadOnly:    t.ReadOnly,
			Authority:   t.Authority,
			Essential:   t.Essential,
		})
	}
	return out
}

func (a *ExtToolAdapter) NewExtensionTool(info ExtensionToolInfo) core.Tool {
	return exttool.New(a.Mgr, exttool.Info{
		Extension:   info.Extension,
		Name:        info.Name,
		Description: info.Description,
		Schema:      info.Schema,
		Essential:   info.Essential,
	})
}

// MCPToolAdapter bridges *mcp.Manager to the same ExtensionToolSource
// seam extension tools ride (the roadmap's "MCP adapter behind
// ExtensionToolSource" bet). MCP tools therefore inherit every
// downstream behavior for free: registry merge, system-prompt
// re-render, the confirm-gate ladder, and plan mode's
// side-effect rules (readOnlyHint-annotated tools are admitted,
// the rest excluded).
type MCPToolAdapter struct {
	Mgr *mcp.Manager

	// allowed is the run's --mcp allowlist (nil = no allowlist). Kept on
	// the adapter so the /mcp dialog's LIVE enable path is gated by the
	// same rule as startup — a run scoped with --mcp cannot have an
	// excluded server resurrected mid-session.
	allowed map[string]bool

	// Per-server stderr log handles are tracked here (not in a SetupMCP
	// closure) so a server started LIVE via the /mcp dialog — long after
	// SetupMCP returned — gets its log handle closed by the same stop func.
	// An MCP server's log left open blocks temp-dir cleanup on Windows.
	logMu    sync.Mutex
	logFiles []*os.File
}

// StderrFor opens (append) and tracks the per-server log sink. Safe for
// the several goroutines StartAll spawns, and reused by live StartOne.
func (a *MCPToolAdapter) StderrFor(server string) io.Writer {
	if mkErr := privfs.MkdirAll(config.LogsPath()); mkErr != nil {
		return nil
	}
	f, ferr := privfs.OpenFile(filepath.Join(config.LogsPath(), "mcp-"+server+".log"), os.O_APPEND|os.O_CREATE|os.O_WRONLY)
	if ferr != nil {
		return nil
	}
	a.logMu.Lock()
	a.logFiles = append(a.logFiles, f)
	a.logMu.Unlock()
	return f
}

func (a *MCPToolAdapter) closeLogs() {
	a.logMu.Lock()
	for _, f := range a.logFiles {
		_ = f.Close()
	}
	a.logFiles = nil
	a.logMu.Unlock()
}

func (a *MCPToolAdapter) Tools() []ExtensionToolInfo {
	infos := a.Mgr.Tools()
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

func (a *MCPToolAdapter) NewExtensionTool(info ExtensionToolInfo) core.Tool {
	for _, t := range a.Mgr.Tools() {
		if t.Name == info.Name {
			return a.Mgr.NewTool(t)
		}
	}
	return nil
}

// SetupMCP starts the user-configured MCP servers — plus a TRUSTED project's
// servers (merged, user wins on a name collision) — and merges their tools into
// the resolved registry. Returns a stop func (never nil). Startup problems are
// stderr notes, never fatal — one broken server must not take the session down.
// Server stderr goes to $TERVA_HOME/logs/mcp-<name>.log, like extensions.
//
// r.Trusted is the Workspace Trust verdict: when false the project's MCP
// servers are NEVER started (only the user's), so a cloned repo cannot spawn
// subprocesses until the user trusts it (Phase 6).
func SetupMCP(ctx context.Context, args Args, r *Resolved) (*MCPToolAdapter, func()) {
	if args.NoMCP {
		return nil, func() {}
	}
	user, err := config.LoadConfig()
	if err != nil {
		return nil, func() {}
	}
	mcpCfg := config.MergeMCPConfigs(user.MCP, config.TrustedProjectMCP(args.CWD, r.Trusted))
	// Drop servers the user or (restrict-only) project has disabled before
	// they ever spawn. This is the same list the /mcp dialog writes.
	if mcpCfg != nil {
		disabled := config.ResolvedDisableMCP(args.CWD, r.Trusted)
		for name := range mcpCfg.Servers {
			if disabled[name] {
				delete(mcpCfg.Servers, name)
			}
		}
	}
	// The --mcp allowlist narrows further (restrict-only, like the
	// --extensions sibling): when set, only the named servers survive to
	// spawn. The same set rides the adapter so the /mcp dialog's live
	// enable path can't resurrect a server this run excluded.
	applyMCPAllowlist(mcpCfg, args.WithMCP)
	// Build a Manager even when nothing is configured or everything is
	// disabled: the /mcp dialog can live-enable a server later via
	// StartOne, and an empty Manager is a valid no-op (no subprocesses).
	// Only --no-mcp (handled above) skips the Manager entirely.
	adapter := &MCPToolAdapter{allowed: mcpAllowSet(args.WithMCP)}
	mgr := mcp.StartAll(ctx, mcpCfg, args.CWD, adapter.StderrFor)
	adapter.Mgr = mgr
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

// AllowsThisRun reports whether the --mcp allowlist admits the named
// server (vacuously true without an allowlist). Consulted by the /mcp
// dialog's live-enable path so a scoped run stays scoped.
func (a *MCPToolAdapter) AllowsThisRun(name string) bool {
	return a.allowed == nil || a.allowed[name]
}

// FanoutAgentEvent translates a core.AgentEvent into the wire-format
// EventFromHost and pushes it through the extension manager. Only
// the events that have a clear extension-facing meaning are
// forwarded; internal-only ones (text_delta, tool_progress) are
// dropped to keep the per-extension stream sane.
func TrimMessagesForResume(msgs []provider.Message, keepTail int) []provider.Message {
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

func FanoutAgentEvent(mgr *extensions.Manager, ev core.AgentEvent) {
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

// OpenWorkGateMessage is the at-close re-prompt injected once when the
// model tries to finish while an extension still has a blocking context
// card (open work). A soft nudge — it grants one more turn, not a hard
// stop — and core caps it to once per prompt. The canonical text lives
// in the swarm package: the supervisor must recognize the nudge in a
// child's event stream so the child's housekeeping answer to it can't
// clobber the recap findings (swarm.AgentSnapshot.Findings).
const OpenWorkGateMessage = swarm.OpenWorkGateMessage

// EmitSessionStart fires the session_start lifecycle event carrying the
// active session's identity, so session-aware extensions (protocol 2+)
// can key per-session state. It is the single point that announces the
// active session: once after the session opens and again on every
// switch (/sessions resume, fork, /new, /cd). sess may be nil — under
// --no-session or just after a close — in which case the id/path/title
// are empty, which the SDK surfaces as "no active session". The host
// emits this before the turn that can invoke extension tools, so a
// subscriber always sees session_start before that session's first
// tool_call (the ordered-delivery guarantee).
func EmitSessionStart(mgr *extensions.Manager, sess *core.Session) {
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

// WorkspaceChangeObserver returns an OnEvent observer that snapshots the
// workspace at the start of each run (EvTurnStart step 1, before any tool
// touches the tree) and emits the net diff at the end (EvDone) as a
// workspace_changed event. The armed flag suppresses a diff for a blocked
// prompt (EvDone with no turn) so it can't report a stale baseline.
// Compose it into a host's OnEvent. Touched only on the serial event
// goroutine, so the closure bool needs no lock. A nil differ disables it.
func WorkspaceChangeObserver(differ *tools.WorkspaceDiffer, extMgr *extensions.Manager) func(core.AgentEvent) {
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

// NonInteractiveExtHooks is the HostHooks impl used by print / json
// modes. They have no TUI, so notify / display go to stderr and
// submit / insert are no-ops (the extension can't steer a
// single-shot run once it's in flight anyway).
type NonInteractiveExtHooks struct{}

func (NonInteractiveExtHooks) Notify(ext, level, message string) {
	fmt.Fprintf(os.Stderr, "[%s] %s: %s\n", ext, level, message)
}

func (NonInteractiveExtHooks) Submit(string) {}

func (NonInteractiveExtHooks) SubmitSlash(string) {}

func (NonInteractiveExtHooks) Insert(string) {}

func (NonInteractiveExtHooks) Display(string, string) {}

func (NonInteractiveExtHooks) ClearNotes(string) {}

func (NonInteractiveExtHooks) OpenPanel(string, extproto.PanelSpec) {}

func (NonInteractiveExtHooks) UpdatePanel(string, string, string, []string, string, []extproto.Widget) {
}

func (NonInteractiveExtHooks) ClosePanel(string, string) {}

func (NonInteractiveExtHooks) RefreshStatus() {}

func (NonInteractiveExtHooks) RefreshContext() {}

func (NonInteractiveExtHooks) RefreshTools() {}

// wireNonInteractiveAgentExtHooks installs the same BeforeToolExecute
// / BeforeTurn / BeforeAssistantMessage / OnEvent hooks the
// interactive path wires up, so extensions get their normal
// event-intercept surface in print / json / rpc flows too. When gate
// is non-nil (--no-yolo) it runs FIRST in BeforeToolExecute, mirroring
// interactive mode, so a refusal short-circuits before the extension
// intercept sees the call.
// BuildHookEngine loads the user config's hooks into an engine, plus a
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
func buildHookEngine(args Args, trusted, liveTrust bool) *hooks.Engine {
	user, err := config.LoadConfig()
	if err != nil {
		return nil
	}
	cfg := config.MergeHookConfigs(user.Hooks, config.TrustedProjectHooks(args.CWD, trusted))
	if cfg == nil {
		// Nothing applies NOW. For a long-lived host that can flip trust
		// mid-run, that is not the same as nothing ever applying: an untrusted
		// repo with hooks on disk needs an engine standing by, or trusting it
		// would have nowhere to put them (HookSpecsFor is how the specs then
		// arrive). One with no hooks anywhere still gets nil — an engine whose
		// chains can only ever be empty is a log file for no reason.
		if !liveTrust || config.ProjectHooksOnDisk(args.CWD) == nil {
			return nil
		}
		cfg = &hooks.Config{}
	}
	logf := func(string, ...any) {}
	var logCloser io.Closer
	if err := privfs.MkdirAll(config.LogsPath()); err == nil {
		if f, ferr := privfs.OpenFile(filepath.Join(config.LogsPath(), "hooks.log"), os.O_APPEND|os.O_CREATE|os.O_WRONLY); ferr == nil {
			logCloser = f
			logf = func(format string, a ...any) {
				fmt.Fprintf(f, time.Now().Format(time.RFC3339)+" "+format+"\n", a...)
			}
		}
	}
	// Only the live-trust host bypasses NewEngine's nil-for-empty guard; a
	// fixed-trust host keeps the cheap nil it has always had for an empty set.
	newEngine := hooks.NewEngine
	if liveTrust {
		newEngine = hooks.NewStandingEngine
	}
	eng := newEngine(*cfg, args.CWD, logf)
	if eng == nil {
		// Empty set on a fixed-trust host: hand back the cheap nil, but do not
		// leak the log file we just opened for it.
		if logCloser != nil {
			_ = logCloser.Close()
		}
		return nil
	}
	eng.SetCloser(logCloser)
	return eng
}

// BuildHookEngine builds the hook engine for a host whose trust verdict is
// fixed for the process's lifetime (the one-shot CLI, rpc, a swarm child).
// nil means no hook can ever fire here.
//
// The test of "fixed" is whether the host has a way to CHANGE the verdict, not
// whether it happens to be long-lived. acp was on this list and should not have
// been: it carries /trust and /untrust on its wire, so a newly trusted repo's
// hooks stayed inert until the editor opened a new session.
func BuildHookEngine(args Args, trusted bool) *hooks.Engine {
	return buildHookEngine(args, trusted, false)
}

// BuildLiveTrustHookEngine builds it for a host that can flip Workspace Trust
// while running (the workspace daemon behind `terva web` and the TUI, and the
// acp editor session). It differs in one way: it returns a standing engine when
// the project has hooks on disk that trust would admit, so HookSpecsFor has
// somewhere to put them. Pair it with HookSpecsFor on every trust change.
func BuildLiveTrustHookEngine(args Args, trusted bool) *hooks.Engine {
	return buildHookEngine(args, trusted, true)
}

// HookSpecsFor is the merged hook config for a trust verdict — what a host
// hands to Engine.SetSpecs when the verdict changes. Kept beside the builder so
// the launch merge and the re-merge cannot drift.
func HookSpecsFor(args Args, trusted bool) *hooks.Config {
	user, err := config.LoadConfig()
	if err != nil {
		return nil
	}
	return config.MergeHookConfigs(user.Hooks, config.TrustedProjectHooks(args.CWD, trusted))
}

// BuildBeforeToolExecute composes the tool-call ladder in its
// canonical order — pre-hooks (may rewrite args; allow/deny are
// final), the confirm gate (sees post-rewrite args), then the
// extension intercept. One implementation shared by every mode so
// the ladders cannot drift apart. hookEng, gate, and extMgr may each
// be nil.
//
// The ladder takes the TURN's context, per call, rather than closing over the
// host's at wiring time. All three rungs can block — a hook is a subprocess, the
// gate can wait on a human for minutes, an intercept is an RPC — and each was
// previously bounded only by the process's lifetime, so cancelling a turn left
// them running for a turn that no longer existed. The per-call context is a
// descendant of the one the host used to pass, so anything carried in it is
// still there; what it adds is an end.
func BuildBeforeToolExecute(hookEng *hooks.Engine, gate *core.ConfirmGate, extMgr *extensions.Manager) func(context.Context, provider.ToolCallBlock) (bool, string, json.RawMessage) {
	return func(ctx context.Context, call provider.ToolCallBlock) (allowed bool, reason string, modArgs json.RawMessage) {
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
			auditSink.Record(time.Now(), auditViaToolCall, call.Name, args, mode, allowed, reason)
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
			ok, denyReason, _ := gate.Check(ctx, call.Name, args, core.BuildPreview(args, 120), call.ID)
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

// ObserveAgentEventForHooks feeds the two tool events into the hook
// engine's post-tool-use correlator. Nil-safe.
func ObserveAgentEventForHooks(eng *hooks.Engine, ev core.AgentEvent) {
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

// SwarmTierMap converts the user config's per-provider tier pins into the
// tools-layer override map (provider -> {tier -> model id}). Empty tier fields
// are dropped so they fall back to the built-in family guess; an all-empty
// result is nil so resolution is a clean no-op.
func SwarmTierMap(tiers map[string]config.TierConfig) tools.SwarmTierMap {
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

// FirstText returns the first text content block of an extension tool
// result, or "" when there is none. Used to parse worktree_create's
// JSON payload (the extension returns it as a single text block).
func FirstText(res extproto.ToolResultFromExt) string {
	for _, b := range res.Content {
		if b.Type == "text" {
			return b.Text
		}
	}
	return ""
}

// SlugAgent derives a stable, filesystem/branch-safe worktree name for
// an agent. The agent id is already unique (slug-of-task + nanoseconds),
// so we lead with it for collision-resistance and append a short,
// readable task slug. The terva-git-worktree extension re-slugs the
// name itself, but we hand it something already safe.
func SlugAgent(agentID, task string) string {
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

// WireHeadlessSessionPersist registers the agent's durable-persistence
// observers on the on-disk session: every appended message, usage row,
// post-compaction transcript, and image exclusion flows to disk as it happens,
// so the real terva session IS the transcript and a crash costs at most the
// in-flight turn. Used by the long-lived headless front ends — ACP sessions,
// the workspace daemon, and the bot daemon's paired-DM agent. Also records the
// session identity on the agent so terva_status can report it. A per-session
// mutex serialises writes; each of these front ends runs one turn at a time per
// agent, so this is belt-and-suspenders.
//
// Registers rather than assigns, so a host that also wants to observe messages
// (a chat-bridge mirror, say) adds its own observer instead of silently
// replacing durable persistence.
func WireHeadlessSessionPersist(ag *core.Agent, sess *core.Session) {
	ag.AdoptSessionIdentity(sess)
	var mu sync.Mutex
	ag.AddMessageObserver(func(m provider.Message) {
		mu.Lock()
		defer mu.Unlock()
		_ = sess.AppendMessage(m)
	})
	ag.AddUsageObserver(func(u, cum provider.Usage) {
		mu.Lock()
		defer mu.Unlock()
		_ = sess.AppendUsage(u, cum)
	})
	// Sub-agent spend, on its own row marker. Same stream (the cumulative
	// figure has to stay one coherent timeline so a crash recovers the true
	// total), different attribution — without which a child's cold prompt reads
	// as this session's cache collapsing to every offline analysis.
	ag.AddDelegatedUsageObserver(func(u, cum provider.Usage) {
		mu.Lock()
		defer mu.Unlock()
		_ = sess.AppendDelegatedUsage(u, cum)
	})
	ag.AddTranscriptCompactedObserver(func(messages []provider.Message, res core.CompactResult) {
		mu.Lock()
		defer mu.Unlock()
		_ = sess.AppendCompaction(messages, res)
	})
	// Lazy-tool activations. activeGroups is in-memory and NewAgent rebuilds it
	// from config, so without this row a --resume silently drops what the model
	// activated — and the tools array sits AHEAD of the system prompt and every
	// message in the provider's cached prefix. The resumed run therefore
	// invalidates the whole transcript, then invalidates it again when the model
	// notices the tool is missing and re-activates. Measured at ~$3.13 on one
	// 225-request session, from a single lost group.
	ag.AddToolGroupActivatedObserver(func(group string) {
		mu.Lock()
		defer mu.Unlock()
		_ = sess.AppendToolGroupActivation(group)
	})
	// Image-rejection recovery: the agent drops an image the provider 400'd on
	// and fires this. Persisting an exclude_image directive is what makes the
	// recovery permanent — the loader re-applies it, so a resumed session never
	// re-sends the bad image and re-fails.
	//
	// This observer did not exist before the hooks became registrations. The
	// agent fired the event and core.Session could write the directive, but no
	// host joined them, so every resume re-sent the rejected image and paid the
	// recovery again. Both doc comments claimed otherwise. The hook was invisible
	// precisely because nothing referenced it.
	ag.AddImageExcludedObserver(func(sha256Hex string) {
		mu.Lock()
		defer mu.Unlock()
		_ = sess.AppendImageExclusion(sha256Hex, "provider rejected the image")
	})
	// Rung 3 of the stuck-loop hatch swapped (or tried to swap) the model. The
	// swap already wrote a "meta" row via UpdateModel, indistinguishable from a
	// user /model switch; this records the escalation that caused it so the log
	// can tell the two apart. The observer fires only when a target is configured,
	// so unconfigured sessions grow no escalation rows.
	ag.AddEscalationObserver(func(rec core.EscalationRecord) {
		mu.Lock()
		defer mu.Unlock()
		_ = sess.AppendEscalation(rec)
	})
	// Rung 1 of the stuck-loop hatch: the detector nudged a repeating model. The
	// nudge only rides the ephemeral tail, so without this row nothing in the log
	// says it fired. Recorded for every session that runs the (default-on)
	// detector, not just ones with an escalation target.
	ag.AddStallObserver(func(rec core.StallRecord) {
		mu.Lock()
		defer mu.Unlock()
		_ = sess.AppendStall(rec)
	})
	// What the harness appended to the request after the cache breakpoint — the
	// generalization of the stall row above. The tail is composed per request and
	// discarded, so without this a session file holds the model's REACTION to a
	// prompt injection and no trace of the injection. Fires on change, so a
	// session whose tail is stable grows one row, not one per turn.
	ag.AddTailObserver(func(rec core.TailRecord) {
		mu.Lock()
		defer mu.Unlock()
		_ = sess.AppendTail(rec)
	})
	// The cacheable prefix was rebuilt rather than extended, so the provider
	// re-read everything after the divergence at full price. Nothing else records
	// it: a mutated prefix is invisible in the transcript and shows up only as a
	// cache-read figure with no explanation.
	ag.AddPrefixDivergenceObserver(func(d core.PrefixDivergence) {
		mu.Lock()
		defer mu.Unlock()
		_ = sess.AppendPrefixDivergence(d)
	})
}

// mcpAllowSet folds the --mcp names into a set; nil when no allowlist.
func mcpAllowSet(names []string) map[string]bool {
	if len(names) == 0 {
		return nil
	}
	set := make(map[string]bool, len(names))
	for _, n := range names {
		if n != "" {
			set[n] = true
		}
	}
	return set
}

// applyMCPAllowlist deletes every server the --mcp allowlist does not
// name from the merged config, before anything spawns. A nil or empty
// allowlist changes nothing; the config disable list has already been
// subtracted, so this only ever narrows (restrict-only).
func applyMCPAllowlist(cfg *mcp.Config, allow []string) {
	set := mcpAllowSet(allow)
	if cfg == nil || set == nil {
		return
	}
	for name := range cfg.Servers {
		if !set[name] {
			delete(cfg.Servers, name)
		}
	}
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
